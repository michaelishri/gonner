// Package safefile opens supervisor-owned files without following leaf symlinks.
// All subsequent mutations are relative to a verified, retained directory fd.
package safefile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type Directory struct {
	file *os.File
	Path string
	Key  string
}

func trusted(uid uint32) bool { return uid == 0 || uid == uint32(os.Geteuid()) }

// OpenDirectory permits root/current-user-owned ancestors, including sticky
// temporary directories. Symlinks in trusted ancestors are resolved explicitly;
// every target ancestor is checked again. A workload UID must not own this path.
func OpenDirectory(path string) (*Directory, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
	}()
	parts := strings.Split(strings.TrimPrefix(abs, "/"), "/")
	resolved := "/"
	links := 0
	for len(parts) > 0 {
		part := parts[0]
		parts = parts[1:]
		if part == "" || part == "." {
			continue
		}
		if err := checkDirectory(fd); err != nil {
			return nil, fmt.Errorf("unsafe directory %s: %w", resolved, err)
		}
		var st unix.Stat_t
		err = unix.Fstatat(fd, part, &st, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			if err = unix.Mkdirat(fd, part, 0750); err != nil && !errors.Is(err, unix.EEXIST) {
				return nil, err
			}
			err = unix.Fstatat(fd, part, &st, unix.AT_SYMLINK_NOFOLLOW)
		}
		if err != nil {
			return nil, err
		}
		if !trusted(st.Uid) {
			return nil, fmt.Errorf("untrusted owner of %s", filepath.Join(resolved, part))
		}
		if st.Mode&unix.S_IFMT == unix.S_IFLNK {
			links++
			if links > 40 {
				return nil, fmt.Errorf("too many directory symlinks")
			}
			buf := make([]byte, 4096)
			n, e := unix.Readlinkat(fd, part, buf)
			if e != nil {
				return nil, e
			}
			if n == len(buf) {
				return nil, fmt.Errorf("symlink target too long")
			}
			target := string(buf[:n])
			if filepath.IsAbs(target) {
				_ = unix.Close(fd)
				fd, err = unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
				if err != nil {
					return nil, err
				}
				resolved = "/"
			}
			parts = append(strings.Split(target, "/"), parts...)
			continue
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		_ = unix.Close(fd)
		fd = next
		resolved = filepath.Join(resolved, part)
	}
	if err := checkDirectory(fd); err != nil {
		return nil, fmt.Errorf("unsafe directory %s: %w", resolved, err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	d := &Directory{file: os.NewFile(uintptr(fd), resolved), Path: resolved, Key: fmt.Sprintf("%d:%d", st.Dev, st.Ino)}
	fd = -1
	return d, nil
}
func checkDirectory(fd int) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if !trusted(st.Uid) {
		return fmt.Errorf("directory is owned by UID %d", st.Uid)
	}
	if st.Mode&0022 != 0 && st.Mode&unix.S_ISVTX == 0 {
		return fmt.Errorf("directory is writable by another user")
	}
	return checkACL(fd)
}
func (d *Directory) Close() error { return d.file.Close() }
func (d *Directory) FD() int      { return int(d.file.Fd()) }
func validName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name
}

// OpenRegular validates before chmod; O_NONBLOCK prevents a FIFO from hanging
// startup. O_NOFOLLOW and nlink==1 exclude symlink and hardlink targets.
func (d *Directory) OpenRegular(name string, flags int, mode os.FileMode) (*os.File, error) {
	if !validName(name) {
		return nil, fmt.Errorf("invalid file name %q", name)
	}
	if mode.Perm()&0022 != 0 {
		return nil, fmt.Errorf("file mode %04o permits other users to write", mode.Perm())
	}
	if err := checkDirectory(d.FD()); err != nil {
		return nil, err
	}
	// Reject existing special files before opening them: even a nonblocking
	// device open can have side effects. The descriptor is validated again below.
	var before unix.Stat_t
	if err := unix.Fstatat(d.FD(), name, &before, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		if before.Mode&unix.S_IFMT != unix.S_IFREG {
			return nil, fmt.Errorf("not a regular file: %s", name)
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return nil, err
	}
	if flags&unix.O_TRUNC != 0 {
		return nil, fmt.Errorf("truncate only after descriptor validation")
	}
	fd, err := unix.Openat(d.FD(), name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), filepath.Join(d.Path, name))
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
		}
	}()
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || !trusted(st.Uid) || st.Mode&0022 != 0 {
		return nil, fmt.Errorf("unsafe file %s: require a trusted, singly linked regular file without other-user write access", name)
	}
	if err = checkACL(fd); err != nil {
		return nil, err
	}
	if flags&(unix.O_WRONLY|unix.O_RDWR) != 0 {
		if err = f.Chmod(mode.Perm()); err != nil {
			return nil, err
		}
	}
	ok = true
	return f, nil
}
func (d *Directory) Remove(name string) error {
	if !validName(name) {
		return fmt.Errorf("invalid file name")
	}
	return unix.Unlinkat(d.FD(), name, 0)
}

// Link publishes a name exclusively: an existing destination is never replaced.
func (d *Directory) Link(from, to string) error {
	if !validName(from) || !validName(to) {
		return fmt.Errorf("invalid file name")
	}
	return unix.Linkat(d.FD(), from, d.FD(), to, 0)
}
func (d *Directory) Entries() ([]os.DirEntry, error) {
	fd, err := unix.Openat(d.FD(), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), d.Path)
	defer f.Close()
	return f.ReadDir(-1)
}
