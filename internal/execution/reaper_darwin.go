package execution

import (
	"context"
	"errors"
	"syscall"
)

func (s *Service) StartReaper(ctx context.Context) (func(), error) { return func() {}, nil }
func groupAlive(pid int) (bool, error) {
	err := syscall.Kill(-pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return err == nil, err
}
