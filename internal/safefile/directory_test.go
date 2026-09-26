package safefile

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func secureDir(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestRejectLeafSymlinkHardlinkFIFOWithoutTouchingVictim(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			path := secureDir(t)
			victim := filepath.Join(path, "victim")
			if err := os.WriteFile(victim, []byte("unchanged"), 0400); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(path, "log")
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(victim, target)
			case "hardlink":
				err = os.Link(victim, target)
			case "fifo":
				err = unix.Mkfifo(target, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			dir, err := OpenDirectory(path)
			if err != nil {
				t.Fatal(err)
			}
			defer dir.Close()
			if f, err := dir.OpenRegular("log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
				_ = f.Close()
				t.Fatal("unsafe file accepted")
			}
			b, err := os.ReadFile(victim)
			if err != nil || string(b) != "unchanged" {
				t.Fatal("victim changed")
			}
			st, _ := os.Stat(victim)
			if st.Mode().Perm() != 0400 {
				t.Fatal("victim mode changed")
			}
		})
	}
}
func TestRejectWritableAncestorAndAllowTrustedSymlinks(t *testing.T) {
	path := secureDir(t)
	unsafe := filepath.Join(path, "unsafe")
	if err := os.Mkdir(unsafe, 0777); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(unsafe, 0777)
	if dir, err := OpenDirectory(filepath.Join(unsafe, "child")); err == nil {
		_ = dir.Close()
		t.Fatal("accepted writable ancestor")
	}
	target := filepath.Join(path, "trusted")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(path, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	dir, err := OpenDirectory(alias)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	f, err := dir.OpenRegular("log", os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := os.Stat(filepath.Join(target, "log")); err != nil {
		t.Fatal(err)
	}
}
func TestStickyAncestorAndDescriptorSurviveRename(t *testing.T) {
	path := secureDir(t)
	sticky := filepath.Join(path, "sticky")
	if err := os.Mkdir(sticky, 0777); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(sticky, os.ModeSticky|0777)
	logdir := filepath.Join(sticky, "logs")
	dir, err := OpenDirectory(logdir)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	moved := filepath.Join(sticky, "moved")
	if err := os.Rename(logdir, moved); err != nil {
		t.Fatal(err)
	}
	f, err := dir.OpenRegular("log", os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := os.Stat(filepath.Join(moved, "log")); err != nil {
		t.Fatal(err)
	}
}
