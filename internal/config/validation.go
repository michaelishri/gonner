package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/michaelishri/gonner/internal/condition"
)

// validStopSignals are the signals allowed for ProcessConfig.StopSignal.
var validStopSignals = map[string]bool{
	"":        true, // default
	"SIGTERM": true,
	"SIGINT":  true,
	"SIGHUP":  true,
	"SIGQUIT": true,
	"SIGUSR1": true,
	"SIGUSR2": true,
	"SIGKILL": true,
}

// ValidationResult holds both errors and warnings from config validation.
type ValidationResult struct {
	Warnings []string
}

// Validate checks the parsed config for logical errors and returns all issues found.
// Warnings are returned via the ValidationResult even on success.
func Validate(cfg *Config) error {
	result, err := ValidateWithWarnings(cfg)
	if err != nil {
		return err
	}
	_ = result // caller can use ValidateWithWarnings to access warnings
	return nil
}

// ValidateWithWarnings checks the parsed config and returns both warnings and errors.
func ValidateWithWarnings(cfg *Config) (*ValidationResult, error) {
	var errs []string
	result := &ValidationResult{}

	// Validate mode
	if cfg.Mode != "parallel" && cfg.Mode != "sequential" {
		errs = append(errs, fmt.Sprintf("invalid mode %q: must be \"parallel\" or \"sequential\"", cfg.Mode))
	}

	// Validate shutdownTimeout
	if time.Duration(cfg.ShutdownTimeout) < 0 {
		errs = append(errs, "shutdownTimeout must be non-negative")
	}

	// Validate health port
	if cfg.Health != nil {
		if cfg.Health.Port < 1 || cfg.Health.Port > 65535 {
			errs = append(errs, fmt.Sprintf("health port %d is out of range (1-65535)", cfg.Health.Port))
		}
		if cfg.Health.TLS != nil {
			if cfg.Health.TLS.CertFile == "" || cfg.Health.TLS.KeyFile == "" {
				errs = append(errs, "health.tls requires both certFile and keyFile")
			}
		}
		if cfg.Health.AuthToken != "" && len(cfg.Health.AuthToken) < 16 {
			result.Warnings = append(result.Warnings, "health.authToken is shorter than 16 characters — consider a stronger token")
		}
	}

	// Must have at least one process
	if len(cfg.Run) == 0 {
		errs = append(errs, "at least one process must be defined in \"run\"")
	}

	// Collect names for uniqueness and dependency validation
	names := make(map[string]bool)
	for i, proc := range cfg.Run {
		prefix := fmt.Sprintf("run[%d] (%s)", i, proc.Name)

		// Name is required
		if proc.Name == "" {
			errs = append(errs, fmt.Sprintf("run[%d]: name is required", i))
			continue
		}

		// Unique name
		if names[proc.Name] {
			errs = append(errs, fmt.Sprintf("%s: duplicate process name", prefix))
		}
		names[proc.Name] = true

		// Command is required
		if proc.Command == "" {
			errs = append(errs, fmt.Sprintf("%s: command is required", prefix))
		}

		// Instances
		if proc.Instances < 1 {
			errs = append(errs, fmt.Sprintf("%s: instances must be >= 1, got %d", prefix, proc.Instances))
		}

		// MaxRetries
		if proc.MaxRetries < 0 {
			errs = append(errs, fmt.Sprintf("%s: maxRetries must be >= 0, got %d", prefix, proc.MaxRetries))
		}

		// Warn if maxRetries set without autoRestart
		if proc.MaxRetries > 0 && !proc.AutoRestart {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: maxRetries is set but autoRestart is false", prefix))
		}

		// Backoff validation
		if proc.Backoff != nil {
			if proc.Backoff.Multiplier <= 0 {
				errs = append(errs, fmt.Sprintf("%s: backoff.multiplier must be > 0, got %f", prefix, proc.Backoff.Multiplier))
			}
			if time.Duration(proc.Backoff.InitialDelay) < 0 {
				errs = append(errs, fmt.Sprintf("%s: backoff.initialDelay must be non-negative", prefix))
			}
			if time.Duration(proc.Backoff.MaxDelay) < 0 {
				errs = append(errs, fmt.Sprintf("%s: backoff.maxDelay must be non-negative", prefix))
			}
		}

		// CommandsBefore validation
		for j, cmd := range proc.CommandsBefore {
			if cmd.Command == "" {
				errs = append(errs, fmt.Sprintf("%s: commandsBefore[%d].command is required", prefix, j))
			}
		}

		// Env validation
		for key := range proc.Env {
			if key == "" {
				errs = append(errs, fmt.Sprintf("%s: env key must not be empty", prefix))
			}
		}

		// StopSignal validation
		if !validStopSignals[strings.ToUpper(proc.StopSignal)] {
			errs = append(errs, fmt.Sprintf("%s: invalid stopSignal %q", prefix, proc.StopSignal))
		}

		// StopTimeout validation
		if time.Duration(proc.StopTimeout) < 0 {
			errs = append(errs, fmt.Sprintf("%s: stopTimeout must be non-negative", prefix))
		}

		// LogFileMode validation
		if proc.LogFileMode != 0 && (proc.LogFileMode < 0 || proc.LogFileMode > 0o777) {
			errs = append(errs, fmt.Sprintf("%s: logFileMode must be a valid POSIX mode (0-0777)", prefix))
		}

		// LogRotate validation
		if proc.LogRotate != nil {
			if proc.LogRotate.MaxSizeMB < 0 {
				errs = append(errs, fmt.Sprintf("%s: logRotate.maxSizeMB must be non-negative", prefix))
			}
			if proc.LogRotate.MaxBackups < 0 {
				errs = append(errs, fmt.Sprintf("%s: logRotate.maxBackups must be non-negative", prefix))
			}
			if proc.LogRotate.MaxSizeMB > 0 && proc.LogFile == "" {
				result.Warnings = append(result.Warnings, fmt.Sprintf("%s: logRotate configured without logFile", prefix))
			}
		}

		// Group requires User
		if proc.Group != "" && proc.User == "" {
			errs = append(errs, fmt.Sprintf("%s: group requires user to be set", prefix))
		}

		// Condition validation
		errs = append(errs, validateConditions(prefix, "whenAll", proc.WhenAll)...)
		errs = append(errs, validateConditions(prefix, "whenAny", proc.WhenAny)...)
	}

	// Validate dependsOn references and detect cycles
	for _, proc := range cfg.Run {
		for _, dep := range proc.DependsOn {
			if !names[dep] {
				errs = append(errs, fmt.Sprintf("%s: dependsOn references unknown process %q", proc.Name, dep))
			}
			if dep == proc.Name {
				errs = append(errs, fmt.Sprintf("%s: process cannot depend on itself", proc.Name))
			}
		}
	}

	// DAG cycle detection
	if cycle := detectCycle(cfg.Run); cycle != nil {
		errs = append(errs, fmt.Sprintf("dependency cycle detected: %s", strings.Join(cycle, " -> ")))
	}

	if len(errs) > 0 {
		return result, fmt.Errorf("config validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}

	return result, nil
}

// validateConditions checks that each condition entry references a known type
// and carries a non-empty value. block is "whenAll" or "whenAny".
func validateConditions(prefix, block string, conditions []map[string]string) []string {
	var errs []string
	for i, m := range conditions {
		if len(m) == 0 {
			errs = append(errs, fmt.Sprintf("%s: %s[%d] is empty", prefix, block, i))
			continue
		}
		for typeName, value := range m {
			if !condition.Known(typeName) {
				errs = append(errs, fmt.Sprintf("%s: %s[%d] unknown condition type %q", prefix, block, i, typeName))
			}
			if value == "" {
				errs = append(errs, fmt.Sprintf("%s: %s[%d] condition %q has an empty value", prefix, block, i, typeName))
			}
		}
	}
	return errs
}

// detectCycle uses Kahn's algorithm for topological sort to detect cycles.
// Returns the cycle as a list of names, or nil if no cycle exists.
func detectCycle(procs []ProcessConfig) []string {
	// Build adjacency list and in-degree map
	graph := make(map[string][]string)
	inDegree := make(map[string]int)

	for _, proc := range procs {
		if _, exists := inDegree[proc.Name]; !exists {
			inDegree[proc.Name] = 0
		}
		for _, dep := range proc.DependsOn {
			graph[dep] = append(graph[dep], proc.Name)
			inDegree[proc.Name]++
		}
	}

	// Find all nodes with in-degree 0
	var queue []string
	for name, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, name)
		}
	}

	// Process queue
	visited := 0
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		visited++

		for _, neighbor := range graph[node] {
			inDegree[neighbor]--
			if inDegree[neighbor] == 0 {
				queue = append(queue, neighbor)
			}
		}
	}

	// If not all nodes were visited, there's a cycle
	if visited < len(inDegree) {
		// Find the nodes in the cycle for a useful error message
		var cycleNodes []string
		for name, degree := range inDegree {
			if degree > 0 {
				cycleNodes = append(cycleNodes, name)
			}
		}
		return cycleNodes
	}

	return nil
}
