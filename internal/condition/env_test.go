package condition

import (
	"os"
	"testing"
)

func TestEnvCondition_KeyValue_Match(t *testing.T) {
	t.Setenv("TEST_COND_KEY", "expected")
	cond := NewEnvCondition("TEST_COND_KEY=expected")

	ok, err := cond.Evaluate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected condition to match")
	}
}

func TestEnvCondition_KeyValue_Mismatch(t *testing.T) {
	t.Setenv("TEST_COND_KEY", "other")
	cond := NewEnvCondition("TEST_COND_KEY=expected")

	ok, err := cond.Evaluate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected condition to not match")
	}
}

func TestEnvCondition_KeyValue_Unset(t *testing.T) {
	os.Unsetenv("TEST_COND_UNSET")
	cond := NewEnvCondition("TEST_COND_UNSET=value")

	ok, err := cond.Evaluate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected condition to not match for unset var")
	}
}

func TestEnvCondition_KeyOnly_Set(t *testing.T) {
	t.Setenv("TEST_COND_EXISTS", "anything")
	cond := NewEnvCondition("TEST_COND_EXISTS")

	ok, err := cond.Evaluate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected condition to match for set var")
	}
}

func TestEnvCondition_KeyOnly_Unset(t *testing.T) {
	os.Unsetenv("TEST_COND_ABSENT")
	cond := NewEnvCondition("TEST_COND_ABSENT")

	ok, err := cond.Evaluate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected condition to not match for unset var")
	}
}

func TestEnvCondition_KeyOnly_Empty(t *testing.T) {
	t.Setenv("TEST_COND_EMPTY", "")
	cond := NewEnvCondition("TEST_COND_EMPTY")

	ok, err := cond.Evaluate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected condition to not match for empty var (key-only mode)")
	}
}

func TestEnvCondition_KeyValue_EmptyExpected(t *testing.T) {
	t.Setenv("TEST_COND_EMPTY_VAL", "")
	cond := NewEnvCondition("TEST_COND_EMPTY_VAL=")

	ok, err := cond.Evaluate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("KEY= should match when env var is empty string")
	}
}

func TestEnvCondition_Type(t *testing.T) {
	cond := NewEnvCondition("FOO=BAR")
	if cond.Type() != "env" {
		t.Errorf("Type(): got %q, want %q", cond.Type(), "env")
	}
}
