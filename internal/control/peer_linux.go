package control

import "golang.org/x/sys/unix"

func peerUID(fd uintptr) (uint32, bool) {
	c, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, false
	}
	return c.Uid, true
}
