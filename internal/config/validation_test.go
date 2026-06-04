package config

import (
	"strings"
	"testing"
	"time"
)

func TestValidate_ValidConfig(t *testing.T) {
	cfg := &Config{
		Mode:            "parallel",
		ShutdownTimeout: Duration(30 * time.Second),
		Run: []ProcessConfig{
			{Name: "web", Command: "echo hello", Instances: 1},
			{Name: "worker", Command: "echo work", Instances: 2},
		},
	}

	if err := Validate(cfg); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidate_InvalidMode(t *testing.T) {
	cfg := &Config{
		Mode: "invalid",
		Run:  []ProcessConfig{{Name: "test", Command: "echo hi", Instances: 1}},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
	if !strings.Contains(err.Error(), "invalid mode") {
		t.Errorf("error should mention invalid mode, got: %v", err)
	}
}

func TestValidate_EmptyRun(t *testing.T) {
	cfg := &Config{Mode: "parallel", Run: []ProcessConfig{}}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for empty run list")
	}
	if !strings.Contains(err.Error(), "at least one process") {
		t.Errorf("error should mention empty run, got: %v", err)
	}
}

func TestValidate_MissingName(t *testing.T) {
	cfg := &Config{
		Mode: "parallel",
		Run:  []ProcessConfig{{Command: "echo hi", Instances: 1}},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Errorf("error should mention missing name, got: %v", err)
	}
}

func TestValidate_MissingCommand(t *testing.T) {
	cfg := &Config{
		Mode: "parallel",
		Run:  []ProcessConfig{{Name: "test", Instances: 1}},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing command")
	}
	if !strings.Contains(err.Error(), "command is required") {
		t.Errorf("error should mention missing command, got: %v", err)
	}
}

func TestValidate_DuplicateNames(t *testing.T) {
	cfg := &Config{
		Mode: "parallel",
		Run: []ProcessConfig{
			{Name: "web", Command: "echo 1", Instances: 1},
			{Name: "web", Command: "echo 2", Instances: 1},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for duplicate names")
	}
	if !strings.Contains(err.Error(), "duplicate process name") {
		t.Errorf("error should mention duplicate name, got: %v", err)
	}
}

func TestValidate_InvalidInstances(t *testing.T) {
	cfg := &Config{
		Mode: "parallel",
		Run:  []ProcessConfig{{Name: "test", Command: "echo hi", Instances: 0}},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for zero instances")
	}
	if !strings.Contains(err.Error(), "instances must be >= 1") {
		t.Errorf("error should mention invalid instances, got: %v", err)
	}
}

func TestValidate_InvalidHealthPort(t *testing.T) {
	cfg := &Config{
		Mode:   "parallel",
		Health: &HealthConfig{Port: 99999},
		Run:    []ProcessConfig{{Name: "test", Command: "echo hi", Instances: 1}},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for invalid health port")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("error should mention port out of range, got: %v", err)
	}
}

func TestValidate_DependsOnUnknown(t *testing.T) {
	cfg := &Config{
		Mode: "parallel",
		Run: []ProcessConfig{
			{Name: "app", Command: "echo app", Instances: 1, DependsOn: []string{"missing"}},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for unknown dependency")
	}
	if !strings.Contains(err.Error(), "unknown process") {
		t.Errorf("error should mention unknown process, got: %v", err)
	}
}

func TestValidate_SelfDependency(t *testing.T) {
	cfg := &Config{
		Mode: "parallel",
		Run: []ProcessConfig{
			{Name: "app", Command: "echo app", Instances: 1, DependsOn: []string{"app"}},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for self-dependency")
	}
	if !strings.Contains(err.Error(), "depend on itself") {
		t.Errorf("error should mention self-dependency, got: %v", err)
	}
}

func TestValidate_CyclicDependency(t *testing.T) {
	cfg := &Config{
		Mode: "parallel",
		Run: []ProcessConfig{
			{Name: "a", Command: "echo a", Instances: 1, DependsOn: []string{"b"}},
			{Name: "b", Command: "echo b", Instances: 1, DependsOn: []string{"c"}},
			{Name: "c", Command: "echo c", Instances: 1, DependsOn: []string{"a"}},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for cyclic dependency")
	}
	if !strings.Contains(err.Error(), "cycle detected") {
		t.Errorf("error should mention cycle, got: %v", err)
	}
}

func TestValidate_ValidDependencyChain(t *testing.T) {
	cfg := &Config{
		Mode: "parallel",
		Run: []ProcessConfig{
			{Name: "db", Command: "echo db", Instances: 1},
			{Name: "cache", Command: "echo cache", Instances: 1},
			{Name: "app", Command: "echo app", Instances: 1, DependsOn: []string{"db", "cache"}},
		},
	}

	if err := Validate(cfg); err != nil {
		t.Errorf("expected no error for valid chain, got: %v", err)
	}
}

func TestValidate_InvalidBackoff(t *testing.T) {
	cfg := &Config{
		Mode: "parallel",
		Run: []ProcessConfig{
			{
				Name: "test", Command: "echo hi", Instances: 1,
				AutoRestart: true,
				Backoff: &BackoffConfig{
					InitialDelay: Duration(1 * time.Second),
					MaxDelay:     Duration(30 * time.Second),
					Multiplier:   -1.0,
				},
			},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for negative multiplier")
	}
	if !strings.Contains(err.Error(), "multiplier must be > 0") {
		t.Errorf("error should mention multiplier, got: %v", err)
	}
}

func TestValidate_EmptyCommandsBefore(t *testing.T) {
	cfg := &Config{
		Mode: "parallel",
		Run: []ProcessConfig{
			{
				Name: "test", Command: "echo hi", Instances: 1,
				CommandsBefore: []PreCommand{{Command: ""}},
			},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for empty commandsBefore command")
	}
	if !strings.Contains(err.Error(), "commandsBefore[0].command is required") {
		t.Errorf("error should mention commandsBefore, got: %v", err)
	}
}

func TestValidateWithWarnings_MaxRetriesWithoutAutoRestart(t *testing.T) {
	cfg := &Config{
		Mode: "parallel",
		Run: []ProcessConfig{
			{Name: "test", Command: "echo hi", Instances: 1, MaxRetries: 5, AutoRestart: false},
		},
	}

	result, err := ValidateWithWarnings(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Warnings) == 0 {
		t.Error("expected warning about maxRetries without autoRestart")
	}

	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "maxRetries is set but autoRestart is false") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning about maxRetries, got: %v", result.Warnings)
	}
}

func TestValidate_MultipleErrors(t *testing.T) {
	cfg := &Config{
		Mode: "invalid",
		Run:  []ProcessConfig{},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error")
	}
	// Should contain both "invalid mode" and "at least one process"
	errStr := err.Error()
	if !strings.Contains(errStr, "invalid mode") {
		t.Error("should mention invalid mode")
	}
	if !strings.Contains(errStr, "at least one process") {
		t.Error("should mention empty run list")
	}
}

func TestValidate_EmptyEnvKey(t *testing.T) {
	cfg := &Config{
		Mode: "parallel",
		Run: []ProcessConfig{
			{
				Name: "test", Command: "echo hi", Instances: 1,
				Env: map[string]string{"": "value"},
			},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for empty env key")
	}
	if !strings.Contains(err.Error(), "env key must not be empty") {
		t.Errorf("error should mention empty env key, got: %v", err)
	}
}

func TestValidate_ValidEnv(t *testing.T) {
	cfg := &Config{
		Mode: "parallel",
		Run: []ProcessConfig{
			{
				Name: "test", Command: "echo hi", Instances: 1,
				Env: map[string]string{"FOO": "bar", "BAZ": "qux"},
			},
		},
	}

	if err := Validate(cfg); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}
