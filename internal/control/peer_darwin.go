package control

import "golang.org/x/sys/unix"

func peerUID(fd uintptr) (uint32, bool) {
	c, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, false
	}
	return c.Uid, true
}
