package condition

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreate_Env(t *testing.T) {
	cond, err := Create("env", "KEY=VALUE")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cond.Type() != "env" {
		t.Errorf("Type(): got %q, want %q", cond.Type(), "env")
	}
}

func TestCreate_FileExists(t *testing.T) {
	cond, err := Create("fileExists", "/tmp/test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cond.Type() != "fileExists" {
		t.Errorf("Type(): got %q, want %q", cond.Type(), "fileExists")
	}
}

func TestCreate_Unknown(t *testing.T) {
	_, err := Create("unknown", "value")
	if err == nil {
		t.Fatal("expected error for unknown condition type")
	}
}

func TestEvaluateAll_AllTrue(t *testing.T) {
	t.Setenv("TEST_ALL_A", "yes")
	dir := t.TempDir()
	f := filepath.Join(dir, "marker")
	os.WriteFile(f, []byte(""), 0644)

	conditions := []map[string]string{
		{"env": "TEST_ALL_A=yes"},
		{"fileExists": f},
	}

	ok, _, err := EvaluateAll(conditions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected all conditions to pass")
	}
}

func TestEvaluateAll_OneFalse(t *testing.T) {
	t.Setenv("TEST_ALL_B", "yes")

	conditions := []map[string]string{
		{"env": "TEST_ALL_B=yes"},
		{"fileExists": "/nonexistent/path"},
	}

	ok, reason, err := EvaluateAll(conditions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected condition to fail")
	}
	if reason == "" {
		t.Error("expected a reason for failure")
	}
}

func TestEvaluateAll_Empty(t *testing.T) {
	ok, _, err := EvaluateAll(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("empty conditions should return true")
	}
}

func TestEvaluateAll_MultipleSameType(t *testing.T) {
	t.Setenv("TEST_ALL_M1", "a")
	t.Setenv("TEST_ALL_M2", "b")

	conditions := []map[string]string{
		{"env": "TEST_ALL_M1=a"},
		{"env": "TEST_ALL_M2=b"},
	}

	ok, _, err := EvaluateAll(conditions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected all conditions to pass with multiple same-type checks")
	}
}

func TestEvaluateAny_OneTrue(t *testing.T) {
	os.Unsetenv("TEST_ANY_MISSING")

	conditions := []map[string]string{
		{"env": "TEST_ANY_MISSING"},
		{"fileExists": os.TempDir()}, // exists
	}

	ok, _, err := EvaluateAny(conditions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected at least one condition to pass")
	}
}

func TestEvaluateAny_NoneTrue(t *testing.T) {
	os.Unsetenv("TEST_ANY_NOPE")

	conditions := []map[string]string{
		{"env": "TEST_ANY_NOPE=yes"},
		{"fileExists": "/nonexistent/path"},
	}

	ok, _, err := EvaluateAny(conditions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected no conditions to pass")
	}
}

func TestEvaluateAny_Empty(t *testing.T) {
	ok, _, err := EvaluateAny(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("empty conditions should return true")
	}
}

func TestShouldRun_BothMet(t *testing.T) {
	t.Setenv("TEST_SHOULD_A", "1")
	dir := t.TempDir()

	ok, _, err := ShouldRun(
		[]map[string]string{{"env": "TEST_SHOULD_A=1"}},
		[]map[string]string{{"fileExists": dir}},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected process to run")
	}
}

func TestShouldRun_WhenAllFails(t *testing.T) {
	os.Unsetenv("TEST_SHOULD_MISSING")

	ok, reason, err := ShouldRun(
		[]map[string]string{{"env": "TEST_SHOULD_MISSING=yes"}},
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected process to not run")
	}
	if reason == "" {
		t.Error("expected a reason")
	}
}

func TestShouldRun_NilConditions(t *testing.T) {
	ok, _, err := ShouldRun(nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("nil conditions should allow process to run")
	}
}

func TestRegister_Custom(t *testing.T) {
	// Register a custom always-true condition
	Register("alwaysTrue", func(value string) Condition {
		return &alwaysTrueCond{}
	})
	defer func() {
		// Cleanup: restore original registry
		delete(registry, "alwaysTrue")
	}()

	cond, err := Create("alwaysTrue", "anything")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ok, err := cond.Evaluate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("custom condition should return true")
	}
}

type alwaysTrueCond struct{}

func (c *alwaysTrueCond) Type() string            { return "alwaysTrue" }
func (c *alwaysTrueCond) Evaluate() (bool, error) { return true, nil }
