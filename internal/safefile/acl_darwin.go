package safefile

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"unsafe"
)

// Darwin ACLs can grant rights beyond mode bits. Query the descriptor's extended
// security attributes; reject allow ACEs that grant mutation. Deny-only ACLs
// (common on home directories) are safe. No cgo or pathname lookup is involved.
// Layout: https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/kauth.h
func checkACL(fd int) error {
	attrs := struct {
		Count, Reserved                 uint16
		Common, Volume, Dir, File, Fork uint32
	}{Count: 5, Common: 0x00400000}
	var buf [8192]byte
	_, _, errno := syscall.Syscall6(syscall.SYS_FGETATTRLIST, uintptr(fd), uintptr(unsafe.Pointer(&attrs)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
	if errno != 0 {
		return fmt.Errorf("reading file ACL: %w", errno)
	}
	size := int(binary.LittleEndian.Uint32(buf[:4]))
	if size < 12 || size > len(buf) {
		return fmt.Errorf("invalid ACL attributes")
	}
	off := 4 + int(int32(binary.LittleEndian.Uint32(buf[4:8])))
	n := int(binary.LittleEndian.Uint32(buf[8:12]))
	if n == 0 {
		return nil
	}
	if off < 12 || n < 44 || off+n > size {
		return fmt.Errorf("invalid ACL data")
	}
	count := binary.LittleEndian.Uint32(buf[off+36 : off+40])
	if count == 0xffffffff {
		return nil
	}
	if count > uint32((n-44)/24) {
		return fmt.Errorf("invalid ACL entry count")
	}
	for i := 0; i < int(count); i++ {
		ace := buf[off+44+i*24:]
		flags := binary.LittleEndian.Uint32(ace[16:20])
		rights := binary.LittleEndian.Uint32(ace[20:24])
		if flags&0xf == 1 && rights&((1<<2)|(1<<4)|(1<<5)|(1<<6)|(1<<8)|(1<<10)|(1<<12)|(1<<13)|(1<<21)|(1<<23)) != 0 {
			return fmt.Errorf("ACL grants mutation rights")
		}
	}
	return nil
}
