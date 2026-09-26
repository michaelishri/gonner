//go:build linux || darwin

package control

import (
	"errors"
	"net"
	"os"
	"sync"
	"syscall"
)

func owned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
func refused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }
func sameUID(c *net.UnixConn) bool {
	raw, err := c.SyscallConn()
	if err != nil {
		return false
	}
	uid, ok := uint32(0), false
	err = raw.Control(func(fd uintptr) { uid, ok = peerUID(fd) })
	return err == nil && ok && uid == uint32(os.Geteuid())
}
func wrap(c net.Conn, release func()) net.Conn {
	var once sync.Once
	return &limitedConn{c, func() { once.Do(release) }}
}
