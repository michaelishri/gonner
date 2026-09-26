package safefile

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPOSIXACLWriteGrantIsRejected(t *testing.T) {
	path := secureDir(t)
	// Linux POSIX ACL v2: owner rwx, named UID 2001 wx, group none,
	// effective mask wx, other none. A grant through the mask changes mode bits.
	data := make([]byte, 4+5*8)
	binary.LittleEndian.PutUint32(data, 2)
	entries := [][3]uint32{{1, 7, 0xffffffff}, {2, 3, 2001}, {4, 0, 0xffffffff}, {16, 3, 0xffffffff}, {32, 0, 0xffffffff}}
	for i, e := range entries {
		b := data[4+i*8:]
		binary.LittleEndian.PutUint16(b, uint16(e[0]))
		binary.LittleEndian.PutUint16(b[2:], uint16(e[1]))
		binary.LittleEndian.PutUint32(b[4:], e[2])
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Fsetxattr(int(f.Fd()), "system.posix_acl_access", data, 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) {
			t.Skip("filesystem has no POSIX ACL support")
		}
		t.Fatal(err)
	}
	if dir, err := OpenDirectory(filepath.Join(path, "logs")); err == nil {
		_ = dir.Close()
		t.Fatal("ACL write grant accepted")
	}
}
