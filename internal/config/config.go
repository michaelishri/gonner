// Package config defines the configuration structures and parsing for gonner.
package config

import "time"

// Config is the top-level gonner configuration.
type Config struct {
	// Mode controls global startup strategy: "parallel" (default) or "sequential".
	Mode string `json:"mode" yaml:"mode"`

	// ShutdownTimeout is how long to wait for processes to exit before SIGKILL.
	ShutdownTimeout Duration `json:"shutdownTimeout" yaml:"shutdownTimeout"`

	// Health configures the optional HTTP health endpoint.
	Health *HealthConfig `json:"health,omitempty" yaml:"health,omitempty"`

	// Run is the list of process definitions to manage.
	Run []ProcessConfig `json:"run" yaml:"run"`

	// PIDFile is a path where gonner writes its own PID on startup.
	// Useful for sysadmins / process supervisors.
	PIDFile string `json:"pidFile,omitempty" yaml:"pidFile,omitempty"`
}

// HealthConfig defines HTTP health endpoint settings.
type HealthConfig struct {
	// Port is the TCP port to listen on.
	Port int `json:"port" yaml:"port"`

	// BindAddr is the address to bind the health endpoint to.
	// Defaults to "0.0.0.0" (all interfaces). Use "127.0.0.1" to restrict to localhost.
	BindAddr string `json:"bindAddr,omitempty" yaml:"bindAddr,omitempty"`

	// AuthToken, when set, requires "Authorization: Bearer <token>" on /status and /metrics.
	// /health is always unauthenticated for use as a Docker/Kubernetes probe.
	AuthToken string `json:"authToken,omitempty" yaml:"authToken,omitempty"`

	// Metrics enables the Prometheus-compatible /metrics endpoint.
	Metrics bool `json:"metrics,omitempty" yaml:"metrics,omitempty"`

	// TLS, when set, serves HTTPS using the provided certificate and key.
	TLS *TLSConfig `json:"tls,omitempty" yaml:"tls,omitempty"`
}

// TLSConfig configures TLS for the health endpoint.
type TLSConfig struct {
	CertFile string `json:"certFile" yaml:"certFile"`
	KeyFile  string `json:"keyFile" yaml:"keyFile"`
}

// LogRotateConfig configures size-based log file rotation.
type LogRotateConfig struct {
	// MaxSizeMB is the maximum size in megabytes before the file is rotated. 0 disables rotation.
	MaxSizeMB int `json:"maxSizeMB,omitempty" yaml:"maxSizeMB,omitempty"`

	// MaxBackups is the maximum number of rotated files to retain. 0 means no limit.
	MaxBackups int `json:"maxBackups,omitempty" yaml:"maxBackups,omitempty"`

	// Compress, if true, gzips rotated files.
	Compress bool `json:"compress,omitempty" yaml:"compress,omitempty"`
}

// ProcessConfig defines a single managed process.
type ProcessConfig struct {
	// Name is the unique identifier for this process.
	Name string `json:"name" yaml:"name"`

	// Command is the shell command to execute (passed to sh -c).
	Command string `json:"command" yaml:"command"`

	// WorkDir is the working directory for the command.
	WorkDir string `json:"workDir,omitempty" yaml:"workDir,omitempty"`

	// LogFile is the path to write process output.
	LogFile string `json:"logFile,omitempty" yaml:"logFile,omitempty"`

	// LogFileMode sets the permission bits for the log file. Defaults to 0o600.
	LogFileMode int `json:"logFileMode,omitempty" yaml:"logFileMode,omitempty"`

	// LogRotate configures size-based log rotation for LogFile.
	LogRotate *LogRotateConfig `json:"logRotate,omitempty" yaml:"logRotate,omitempty"`

	// User drops privileges to this username (or numeric UID) before exec.
	// Requires gonner to start as root. Linux/macOS only.
	User string `json:"user,omitempty" yaml:"user,omitempty"`

	// Group sets the primary group (name or numeric GID) for the process. Requires User.
	Group string `json:"group,omitempty" yaml:"group,omitempty"`

	// StopSignal is the signal sent on shutdown. Defaults to "SIGTERM".
	// Accepts: SIGTERM, SIGINT, SIGHUP, SIGQUIT, SIGUSR1, SIGUSR2, SIGKILL.
	StopSignal string `json:"stopSignal,omitempty" yaml:"stopSignal,omitempty"`

	// StopTimeout overrides the global shutdownTimeout for this process.
	StopTimeout Duration `json:"stopTimeout,omitempty" yaml:"stopTimeout,omitempty"`

	// AutoRestart enables automatic restart on process exit.
	AutoRestart bool `json:"autoRestart,omitempty" yaml:"autoRestart,omitempty"`

	// MaxRetries limits restart attempts (0 = unlimited when AutoRestart is true).
	MaxRetries int `json:"maxRetries,omitempty" yaml:"maxRetries,omitempty"`

	// Backoff configures exponential backoff for restarts.
	Backoff *BackoffConfig `json:"backoff,omitempty" yaml:"backoff,omitempty"`

	// Instances is the number of identical copies to run (default 1).
	Instances int `json:"instances,omitempty" yaml:"instances,omitempty"`

	// Critical marks this process as critical — its unexpected exit triggers full shutdown.
	Critical bool `json:"critical,omitempty" yaml:"critical,omitempty"`

	// DependsOn lists process names that must be running before this one starts.
	DependsOn []string `json:"dependsOn,omitempty" yaml:"dependsOn,omitempty"`

	// WhenAll requires all conditions to be true for the process to start.
	// Each element is a single condition as a {type: value} object, e.g.
	// {"env": "FOO=1"}. Repeat the same type across elements to require
	// multiple checks of one kind.
	WhenAll []map[string]string `json:"whenAll,omitempty" yaml:"whenAll,omitempty"`

	// WhenAny requires at least one condition to be true for the process to start.
	// Each element is a single condition as a {type: value} object.
	WhenAny []map[string]string `json:"whenAny,omitempty" yaml:"whenAny,omitempty"`

	// Env is a map of environment variables to set for this process.
	Env map[string]string `json:"env,omitempty" yaml:"env,omitempty"`

	// CommandsBefore are commands to run sequentially before the main process.
	CommandsBefore []PreCommand `json:"commandsBefore,omitempty" yaml:"commandsBefore,omitempty"`
}

// PreCommand defines a command to run before the main process starts.
type PreCommand struct {
	// Command is the shell command to execute.
	Command string `json:"command" yaml:"command"`

	// WorkDir is the working directory for this command.
	WorkDir string `json:"workDir,omitempty" yaml:"workDir,omitempty"`

	// ContinueOnError allows the main process to start even if this command fails.
	ContinueOnError bool `json:"continueOnError,omitempty" yaml:"continueOnError,omitempty"`
}

// BackoffConfig defines exponential backoff parameters for process restarts.
type BackoffConfig struct {
	// InitialDelay is the delay before the first restart.
	InitialDelay Duration `json:"initialDelay" yaml:"initialDelay"`

	// MaxDelay is the maximum delay between restarts.
	MaxDelay Duration `json:"maxDelay" yaml:"maxDelay"`

	// Multiplier is applied to the delay after each restart.
	Multiplier float64 `json:"multiplier" yaml:"multiplier"`
}

// Defaults returns a Config with sensible defaults applied.
func Defaults() Config {
	return Config{
		Mode:            "parallel",
		ShutdownTimeout: Duration(30 * time.Second),
	}
}

// DefaultBackoff returns the default backoff configuration.
func DefaultBackoff() BackoffConfig {
	return BackoffConfig{
		InitialDelay: Duration(1 * time.Second),
		MaxDelay:     Duration(30 * time.Second),
		Multiplier:   2.0,
	}
}

// ApplyDefaults fills in default values for fields that are zero-valued.
func (c *Config) ApplyDefaults() {
	if c.Mode == "" {
		c.Mode = "parallel"
	}
	if time.Duration(c.ShutdownTimeout) == 0 {
		c.ShutdownTimeout = Duration(30 * time.Second)
	}
	if c.Health != nil && c.Health.BindAddr == "" {
		c.Health.BindAddr = "0.0.0.0"
	}
	for i := range c.Run {
		if c.Run[i].Instances <= 0 {
			c.Run[i].Instances = 1
		}
		if c.Run[i].LogFileMode == 0 {
			c.Run[i].LogFileMode = 0o600
		}
		if c.Run[i].StopSignal == "" {
			c.Run[i].StopSignal = "SIGTERM"
		}
		if c.Run[i].AutoRestart && c.Run[i].Backoff == nil {
			defaults := DefaultBackoff()
			c.Run[i].Backoff = &defaults
		}
		if c.Run[i].Backoff != nil {
			if time.Duration(c.Run[i].Backoff.InitialDelay) == 0 {
				c.Run[i].Backoff.InitialDelay = Duration(1 * time.Second)
			}
			if time.Duration(c.Run[i].Backoff.MaxDelay) == 0 {
				c.Run[i].Backoff.MaxDelay = Duration(30 * time.Second)
			}
			if c.Run[i].Backoff.Multiplier == 0 {
				c.Run[i].Backoff.Multiplier = 2.0
			}
		}
	}
}
