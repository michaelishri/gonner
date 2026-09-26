package execution

import (
	"fmt"
	"os"
	"syscall"
)

func checkCapabilities(c *syscall.Credential) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("setting user/group requires root")
	}
	return nil
}
