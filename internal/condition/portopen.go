package condition

import (
	"net"
	"strings"
	"time"
)

// PortOpenCondition checks if a TCP port is reachable.
// Format: "host:port" or ":port" (defaults host to 127.0.0.1).
// Optional suffix "@timeout" sets the dial timeout (e.g. "db:5432@2s"). Default 1s.
type PortOpenCondition struct {
	target  string
	timeout time.Duration
}

// NewPortOpenCondition creates a PortOpen condition.
func NewPortOpenCondition(value string) Condition {
	target := value
	timeout := 1 * time.Second
	if idx := strings.LastIndex(value, "@"); idx > 0 {
		if d, err := time.ParseDuration(value[idx+1:]); err == nil {
			timeout = d
			target = value[:idx]
		}
	}
	if strings.HasPrefix(target, ":") {
		target = "127.0.0.1" + target
	}
	return &PortOpenCondition{target: target, timeout: timeout}
}

// Type returns "portOpen".
func (c *PortOpenCondition) Type() string { return "portOpen" }

// Evaluate attempts a TCP dial within the configured timeout.
// Connection refused and timeouts both yield (false, nil) rather than an error.
func (c *PortOpenCondition) Evaluate() (bool, error) {
	conn, err := net.DialTimeout("tcp", c.target, c.timeout)
	if err != nil {
		return false, nil
	}
	_ = conn.Close()
	return true, nil
}
