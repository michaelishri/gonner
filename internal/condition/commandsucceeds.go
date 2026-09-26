package condition

import (
	"context"
	"errors"
	"syscall"
	"time"

	"github.com/michaelishri/gonner/internal/execution"
)

type CommandSucceedsCondition struct {
	command string
	timeout time.Duration
	service *execution.Service
	spec    execution.Spec
}

func NewCommandSucceedsCondition(value string) Condition {
	return &CommandSucceedsCondition{command: value, timeout: 10 * time.Second, service: execution.NewService()}
}
func (c *CommandSucceedsCondition) Type() string { return "commandSucceeds" }
func (c *CommandSucceedsCondition) Evaluate(parent context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()
	spec := c.spec
	spec.Command = c.command
	spec.StopSignal = syscall.SIGKILL
	spec.StopTimeout = time.Second
	r := c.service.Run(ctx, spec)
	var supervisor *execution.SupervisorError
	if errors.As(r.Err, &supervisor) {
		return false, r.Err
	}
	if parent.Err() != nil {
		return false, parent.Err()
	}
	return ctx.Err() == nil && r.Err == nil && r.ExitCode == 0, nil
}
