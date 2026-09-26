package safefile

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// POSIX ACL permissions are bounded by the group mode mask. checkDirectory
// and OpenRegular reject group/other write access; sticky ancestors protect
// trusted entries even when a POSIX ACL permits creation by other users.
// Rich/NFSv4 ACLs do not have the same mode-mask guarantee, so fail closed when
// one is present rather than guessing how its ordered allow/deny rules apply.
func checkACL(fd int) error {
	for _, name := range []string{"system.nfs4_acl", "system.richacl"} {
		n, err := unix.Fgetxattr(fd, name, nil)
		if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) {
			continue
		}
		if err != nil {
			return fmt.Errorf("checking %s: %w", name, err)
		}
		if n > 0 {
			return fmt.Errorf("unsupported extended access ACL: %s", name)
		}
	}
	return nil
}
