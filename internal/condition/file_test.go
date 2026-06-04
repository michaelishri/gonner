package condition

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileCondition_FileExists(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "exists.txt")
	if err := os.WriteFile(f, []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}

	cond := NewFileCondition(f)

	ok, err := cond.Evaluate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected condition to match for existing file")
	}
}

func TestFileCondition_FileNotExists(t *testing.T) {
	cond := NewFileCondition("/nonexistent/path/file.txt")

	ok, err := cond.Evaluate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected condition to not match for nonexistent file")
	}
}

func TestFileCondition_DirectoryExists(t *testing.T) {
	dir := t.TempDir()
	cond := NewFileCondition(dir)

	ok, err := cond.Evaluate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected condition to match for existing directory")
	}
}

func TestFileCondition_Type(t *testing.T) {
	cond := NewFileCondition("/tmp/test")
	if cond.Type() != "fileExists" {
		t.Errorf("Type(): got %q, want %q", cond.Type(), "fileExists")
	}
}
