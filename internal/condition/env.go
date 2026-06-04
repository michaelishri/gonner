package condition

import (
	"os"
	"strings"
)

// EnvCondition checks environment variable values.
// Supports two formats:
//   - "KEY=VALUE" — checks if KEY equals VALUE
//   - "KEY" — checks if KEY is set and non-empty
type EnvCondition struct {
	raw string
}

// NewEnvCondition creates a new EnvCondition from a raw config value.
func NewEnvCondition(value string) Condition {
	return &EnvCondition{raw: value}
}

// Type returns "env".
func (c *EnvCondition) Type() string {
	return "env"
}

// Evaluate checks the environment variable.
func (c *EnvCondition) Evaluate() (bool, error) {
	if idx := strings.Index(c.raw, "="); idx > 0 {
		key := c.raw[:idx]
		expected := c.raw[idx+1:]
		actual := os.Getenv(key)
		return actual == expected, nil
	}

	// Just check if the variable is set and non-empty.
	val := os.Getenv(c.raw)
	return val != "", nil
}
