package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParse_JSON(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "gonner.json")
	content := `{
		"mode": "sequential",
		"shutdownTimeout": "10s",
		"health": {"port": 9090},
		"run": [
			{
				"name": "web",
				"command": "echo hello",
				"autoRestart": true,
				"instances": 2,
				"critical": true
			}
		]
	}`
	if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse(cfgFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Mode != "sequential" {
		t.Errorf("mode: got %q, want %q", cfg.Mode, "sequential")
	}
	if time.Duration(cfg.ShutdownTimeout) != 10*time.Second {
		t.Errorf("shutdownTimeout: got %v, want 10s", time.Duration(cfg.ShutdownTimeout))
	}
	if cfg.Health == nil || cfg.Health.Port != 9090 {
		t.Errorf("health.port: got %v, want 9090", cfg.Health)
	}
	if len(cfg.Run) != 1 {
		t.Fatalf("run: got %d processes, want 1", len(cfg.Run))
	}

	proc := cfg.Run[0]
	if proc.Name != "web" {
		t.Errorf("name: got %q, want %q", proc.Name, "web")
	}
	if proc.Instances != 2 {
		t.Errorf("instances: got %d, want 2", proc.Instances)
	}
	if !proc.AutoRestart {
		t.Error("autoRestart: got false, want true")
	}
	if !proc.Critical {
		t.Error("critical: got false, want true")
	}
	// AutoRestart should trigger default backoff
	if proc.Backoff == nil {
		t.Error("backoff should be populated when autoRestart is true")
	}
}

func TestParse_YAML(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "gonner.yaml")
	content := `
mode: parallel
shutdownTimeout: "15s"
run:
  - name: worker
    command: echo work
    instances: 3
`
	if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse(cfgFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Mode != "parallel" {
		t.Errorf("mode: got %q, want %q", cfg.Mode, "parallel")
	}
	if len(cfg.Run) != 1 {
		t.Fatalf("run: got %d processes, want 1", len(cfg.Run))
	}
	if cfg.Run[0].Instances != 3 {
		t.Errorf("instances: got %d, want 3", cfg.Run[0].Instances)
	}
}

func TestParse_Defaults(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "gonner.json")
	content := `{"run": [{"name": "test", "command": "echo hi"}]}`
	if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse(cfgFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Mode != "parallel" {
		t.Errorf("default mode: got %q, want %q", cfg.Mode, "parallel")
	}
	if time.Duration(cfg.ShutdownTimeout) != 30*time.Second {
		t.Errorf("default shutdownTimeout: got %v, want 30s", time.Duration(cfg.ShutdownTimeout))
	}
	if cfg.Run[0].Instances != 1 {
		t.Errorf("default instances: got %d, want 1", cfg.Run[0].Instances)
	}
}

func TestParse_EnvInterpolation(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "gonner.json")
	content := `{"run": [{"name": "test", "command": "echo {{env://TEST_GONNER_VAR}}"}]}`
	if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TEST_GONNER_VAR", "hello_world")

	cfg, err := Parse(cfgFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Run[0].Command != "echo hello_world" {
		t.Errorf("command: got %q, want %q", cfg.Run[0].Command, "echo hello_world")
	}
}

func TestParse_EnvInterpolation_WithDefault(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "gonner.json")
	content := `{"run": [{"name": "test", "command": "echo {{env://UNSET_VAR_12345:fallback}}"}]}`
	if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse(cfgFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Run[0].Command != "echo fallback" {
		t.Errorf("command: got %q, want %q", cfg.Run[0].Command, "echo fallback")
	}
}

func TestParse_EnvInterpolation_MissingRequired(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "gonner.json")
	content := `{"run": [{"name": "test", "command": "echo {{env://MISSING_VAR_99999}}"}]}`
	if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Parse(cfgFile)
	if err == nil {
		t.Fatal("expected error for missing env var without default")
	}
	if !strings.Contains(err.Error(), "MISSING_VAR_99999") {
		t.Errorf("error should mention missing var name, got: %v", err)
	}
}

func TestParse_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "gonner.json")
	if err := os.WriteFile(cfgFile, []byte(`{invalid`), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Parse(cfgFile)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParse_BackoffConfig(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "gonner.json")
	content := `{
		"run": [{
			"name": "test",
			"command": "echo hi",
			"autoRestart": true,
			"backoff": {
				"initialDelay": "5s",
				"maxDelay": "2m",
				"multiplier": 3.0
			}
		}]
	}`
	if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse(cfgFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b := cfg.Run[0].Backoff
	if b == nil {
		t.Fatal("backoff should not be nil")
	}
	if time.Duration(b.InitialDelay) != 5*time.Second {
		t.Errorf("initialDelay: got %v, want 5s", time.Duration(b.InitialDelay))
	}
	if time.Duration(b.MaxDelay) != 2*time.Minute {
		t.Errorf("maxDelay: got %v, want 2m", time.Duration(b.MaxDelay))
	}
	if b.Multiplier != 3.0 {
		t.Errorf("multiplier: got %f, want 3.0", b.Multiplier)
	}
}

func TestParse_DependsOn(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "gonner.json")
	content := `{
		"run": [
			{"name": "db", "command": "echo db"},
			{"name": "app", "command": "echo app", "dependsOn": ["db"]}
		]
	}`
	if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse(cfgFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Run[1].DependsOn) != 1 || cfg.Run[1].DependsOn[0] != "db" {
		t.Errorf("dependsOn: got %v, want [db]", cfg.Run[1].DependsOn)
	}
}

func TestParse_CommandsBefore(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "gonner.json")
	content := `{
		"run": [{
			"name": "app",
			"command": "echo app",
			"commandsBefore": [
				{"command": "echo migrate", "workDir": "/app", "continueOnError": true}
			]
		}]
	}`
	if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse(cfgFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmds := cfg.Run[0].CommandsBefore
	if len(cmds) != 1 {
		t.Fatalf("commandsBefore: got %d, want 1", len(cmds))
	}
	if cmds[0].Command != "echo migrate" {
		t.Errorf("command: got %q, want %q", cmds[0].Command, "echo migrate")
	}
	if cmds[0].WorkDir != "/app" {
		t.Errorf("workDir: got %q, want %q", cmds[0].WorkDir, "/app")
	}
	if !cmds[0].ContinueOnError {
		t.Error("continueOnError: got false, want true")
	}
}

func TestConfig_MarshalRoundTrip(t *testing.T) {
	cfg := Config{
		Mode:            "parallel",
		ShutdownTimeout: Duration(30 * time.Second),
		Health:          &HealthConfig{Port: 8080},
		Run: []ProcessConfig{
			{
				Name:        "test",
				Command:     "echo hi",
				AutoRestart: true,
				Instances:   2,
			},
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var restored Config
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if restored.Mode != cfg.Mode {
		t.Errorf("mode: got %q, want %q", restored.Mode, cfg.Mode)
	}
	if time.Duration(restored.ShutdownTimeout) != time.Duration(cfg.ShutdownTimeout) {
		t.Errorf("shutdownTimeout mismatch")
	}
}

func TestParse_YAML_EnvInterpolation(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "gonner.yaml")
	content := `
run:
  - name: test
    command: "echo {{env://TEST_YAML_VAR}}"
`
	if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TEST_YAML_VAR", "yaml_works")

	cfg, err := Parse(cfgFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Run[0].Command != "echo yaml_works" {
		t.Errorf("command: got %q, want %q", cfg.Run[0].Command, "echo yaml_works")
	}
}

func TestParse_YAML_Duration(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "gonner.yaml")
	content := `
mode: parallel
shutdownTimeout: "45s"
run:
  - name: test
    command: echo hi
    autoRestart: true
    backoff:
      initialDelay: "3s"
      maxDelay: "1m"
      multiplier: 2.5
`
	if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse(cfgFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if time.Duration(cfg.ShutdownTimeout) != 45*time.Second {
		t.Errorf("shutdownTimeout: got %v, want 45s", time.Duration(cfg.ShutdownTimeout))
	}
	if cfg.Run[0].Backoff == nil {
		t.Fatal("backoff should not be nil")
	}
	if time.Duration(cfg.Run[0].Backoff.InitialDelay) != 3*time.Second {
		t.Errorf("initialDelay: got %v, want 3s", time.Duration(cfg.Run[0].Backoff.InitialDelay))
	}
	if time.Duration(cfg.Run[0].Backoff.MaxDelay) != time.Minute {
		t.Errorf("maxDelay: got %v, want 1m", time.Duration(cfg.Run[0].Backoff.MaxDelay))
	}
	if cfg.Run[0].Backoff.Multiplier != 2.5 {
		t.Errorf("multiplier: got %f, want 2.5", cfg.Run[0].Backoff.Multiplier)
	}
}

func TestParse_YAML_CommandsBefore(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "gonner.yaml")
	content := `
run:
  - name: app
    command: echo app
    commandsBefore:
      - command: echo migrate
        workDir: /app
        continueOnError: true
      - command: echo seed
`
	if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse(cfgFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmds := cfg.Run[0].CommandsBefore
	if len(cmds) != 2 {
		t.Fatalf("commandsBefore: got %d, want 2", len(cmds))
	}
	if cmds[0].Command != "echo migrate" {
		t.Errorf("cmd[0]: got %q, want %q", cmds[0].Command, "echo migrate")
	}
	if cmds[0].WorkDir != "/app" {
		t.Errorf("workDir[0]: got %q, want %q", cmds[0].WorkDir, "/app")
	}
	if !cmds[0].ContinueOnError {
		t.Error("continueOnError[0]: got false, want true")
	}
	if cmds[1].Command != "echo seed" {
		t.Errorf("cmd[1]: got %q, want %q", cmds[1].Command, "echo seed")
	}
}

func TestParse_YAML_Conditions(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "gonner.yaml")
	content := `
run:
  - name: conditional
    command: echo hi
    whenAll:
      - env: "MY_FLAG=true"
    whenAny:
      - fileExists: /tmp
`
	if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse(cfgFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Run[0].WhenAll[0]["env"] != "MY_FLAG=true" {
		t.Errorf("whenAll.env: got %q, want %q", cfg.Run[0].WhenAll[0]["env"], "MY_FLAG=true")
	}
	if cfg.Run[0].WhenAny[0]["fileExists"] != "/tmp" {
		t.Errorf("whenAny.fileExists: got %q, want %q", cfg.Run[0].WhenAny[0]["fileExists"], "/tmp")
	}
}

func TestParse_YAML_DependsOn(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "gonner.yaml")
	content := `
run:
  - name: db
    command: echo db
  - name: cache
    command: echo cache
  - name: app
    command: echo app
    dependsOn:
      - db
      - cache
`
	if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse(cfgFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	deps := cfg.Run[2].DependsOn
	if len(deps) != 2 {
		t.Fatalf("dependsOn: got %d, want 2", len(deps))
	}
	if deps[0] != "db" || deps[1] != "cache" {
		t.Errorf("dependsOn: got %v, want [db cache]", deps)
	}
}

func TestParse_Env(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "gonner.json")
	content := `{
		"run": [{
			"name": "test",
			"command": "echo hi",
			"env": {
				"FOO": "bar",
				"BAZ": "qux"
			}
		}]
	}`
	if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse(cfgFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	env := cfg.Run[0].Env
	if len(env) != 2 {
		t.Fatalf("env: got %d entries, want 2", len(env))
	}
	if env["FOO"] != "bar" {
		t.Errorf("env[FOO]: got %q, want %q", env["FOO"], "bar")
	}
	if env["BAZ"] != "qux" {
		t.Errorf("env[BAZ]: got %q, want %q", env["BAZ"], "qux")
	}
}

func TestParse_YAML_Env(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "gonner.yaml")
	content := `
run:
  - name: test
    command: echo hi
    env:
      QUEUE_CONNECTION: redis
      QUEUE_TIMEOUT: "60"
`
	if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse(cfgFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	env := cfg.Run[0].Env
	if len(env) != 2 {
		t.Fatalf("env: got %d entries, want 2", len(env))
	}
	if env["QUEUE_CONNECTION"] != "redis" {
		t.Errorf("env[QUEUE_CONNECTION]: got %q, want %q", env["QUEUE_CONNECTION"], "redis")
	}
	if env["QUEUE_TIMEOUT"] != "60" {
		t.Errorf("env[QUEUE_TIMEOUT]: got %q, want %q", env["QUEUE_TIMEOUT"], "60")
	}
}
