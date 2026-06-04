package condition

import (
	"context"
	"os/exec"
	"time"
)

// CommandSucceedsCondition runs a shell command and returns true if it exits 0.
// Format: the raw shell command string. A default 10s timeout is applied.
type CommandSucceedsCondition struct {
	command string
	timeout time.Duration
}

// NewCommandSucceedsCondition creates a CommandSucceeds condition.
func NewCommandSucceedsCondition(value string) Condition {
	return &CommandSucceedsCondition{command: value, timeout: 10 * time.Second}
}

// Type returns "commandSucceeds".
func (c *CommandSucceedsCondition) Type() string { return "commandSucceeds" }

// Evaluate executes the command via sh -c and returns true if exit code is 0.
func (c *CommandSucceedsCondition) Evaluate() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", c.command)
	if err := cmd.Run(); err != nil {
		return false, nil
	}
	return true, nil
}
