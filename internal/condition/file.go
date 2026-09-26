package condition

import (
	"context"
	"os"
)

// FileCondition checks whether a file or directory exists at a given path.
type FileCondition struct {
	path string
}

// NewFileCondition creates a new FileCondition from a file path.
func NewFileCondition(value string) Condition {
	return &FileCondition{path: value}
}

// Type returns "fileExists".
func (c *FileCondition) Type() string {
	return "fileExists"
}

// Evaluate returns true if the path exists.
func (c *FileCondition) Evaluate(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	_, err := os.Stat(c.path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
