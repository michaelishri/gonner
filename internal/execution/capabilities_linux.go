package execution

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func checkCapabilities(c *syscall.Credential) error {
	var data [2]unix.CapUserData
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	if err := unix.Capget(&header, &data[0]); err != nil {
		return fmt.Errorf("reading capabilities: %w", err)
	}
	has := func(cap uint) bool { return data[cap/32].Effective&(1<<(cap%32)) != 0 }
	// setgroups(empty) always needs SETGID, even when keeping the primary GID.
	if !has(unix.CAP_SETGID) {
		return fmt.Errorf("user/group requires CAP_SETGID to clear supplementary groups")
	}
	if c.Uid != uint32(os.Geteuid()) {
		if !has(unix.CAP_SETUID) {
			return fmt.Errorf("changing user requires CAP_SETUID")
		}
		if !has(unix.CAP_KILL) {
			return fmt.Errorf("supervising another UID requires CAP_KILL")
		}
	}
	return nil
}
