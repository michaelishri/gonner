package execution

import (
	"fmt"
	"os/user"
	"strconv"
	"strings"
	"syscall"
)

func numeric(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) < 0
}
func parseID(s string) (uint32, error) {
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil || n == 1<<32-1 {
		return 0, fmt.Errorf("invalid UID/GID %q", s)
	}
	return uint32(n), nil
}

// ResolveCredential uses the account's real primary group for numeric and named
// users. Unknown numeric UIDs require an explicit group. Supplementary groups
// are intentionally empty, and NoSetGroups is false.
func ResolveCredential(userSpec, groupSpec string) (*syscall.Credential, error) {
	if userSpec == "" {
		if groupSpec != "" {
			return nil, fmt.Errorf("group requires user")
		}
		return nil, nil
	}
	var account *user.User
	var err error
	var uid, gid uint32
	if numeric(userSpec) {
		uid, err = parseID(userSpec)
		if err != nil {
			return nil, err
		}
		account, err = user.LookupId(strconv.FormatUint(uint64(uid), 10))
	} else {
		account, err = user.Lookup(userSpec)
	}
	if err != nil {
		if !numeric(userSpec) || groupSpec == "" {
			return nil, fmt.Errorf("resolving user %q (unknown numeric UID requires group): %w", userSpec, err)
		}
	}
	if account != nil {
		uid, err = parseID(account.Uid)
		if err != nil {
			return nil, err
		}
		gid, err = parseID(account.Gid)
		if err != nil {
			return nil, err
		}
	}
	if groupSpec != "" {
		g := groupSpec
		if !numeric(g) {
			grp, e := user.LookupGroup(g)
			if e != nil {
				return nil, fmt.Errorf("resolving group: %w", e)
			}
			g = grp.Gid
		}
		gid, err = parseID(g)
		if err != nil {
			return nil, err
		}
	}
	cred := &syscall.Credential{Uid: uid, Gid: gid, Groups: []uint32{}}
	if err := checkCapabilities(cred); err != nil {
		return nil, err
	}
	return cred, nil
}
