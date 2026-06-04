// Package condition provides an extensible condition evaluation system
// for deciding whether a process should start.
package condition

// Condition is the interface that all condition types must implement.
type Condition interface {
	// Type returns the condition type name (e.g., "env", "fileExists").
	Type() string

	// Evaluate returns true if the condition is met.
	Evaluate() (bool, error)
}
