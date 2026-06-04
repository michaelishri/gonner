package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/michaelishri/gonner/internal/config"
	"github.com/michaelishri/gonner/internal/health"
	"github.com/michaelishri/gonner/internal/logging"
	"github.com/michaelishri/gonner/internal/runner"
)

var (
	healthPort     int
	healthBindAddr string
)

func init() {
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Start the process manager",
		Long:  "Start gonner and run all configured processes. This is the default command.",
		RunE:  runRun,
	}

	runCmd.Flags().IntVar(&healthPort, "health-port", 0, "override health endpoint port")
	runCmd.Flags().StringVar(&healthBindAddr, "health-bind", "", "override health endpoint bind address (e.g. 127.0.0.1)")
	rootCmd.AddCommand(runCmd)
}

func runRun(_ *cobra.Command, _ []string) error {
	// Discover config
	cfgPath, err := config.Discover(configPath)
	if err != nil {
		return fmt.Errorf("config discovery failed: %w", err)
	}
	logging.Gonner("Using config: %s", cfgPath)

	// Parse config
	cfg, err := config.Parse(cfgPath)
	if err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	// Validate config
	result, err := config.ValidateWithWarnings(cfg)
	if err != nil {
		return fmt.Errorf("config validation failed: %w", err)
	}
	for _, w := range result.Warnings {
		logging.Gonner("Warning: %s", w)
	}

	// Determine health port (CLI flag > env var > config)
	port := resolveHealthPort(cfg)

	// Create process manager
	mgr := runner.NewManager(cfg)

	// Set up context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigHandler := runner.NewSignalHandler(cancel, mgr.ForwardSignal)
	go sigHandler.Start()
	defer sigHandler.Stop()

	// Start health endpoint if configured
	if port > 0 {
		opts := health.Options{
			Port:     port,
			BindAddr: resolveHealthBind(cfg),
		}
		if cfg.Health != nil {
			opts.AuthToken = cfg.Health.AuthToken
			opts.EnableMetrics = cfg.Health.Metrics
			opts.TLS = cfg.Health.TLS
		}
		// Env var override for auth token (preferred for secrets).
		if t := os.Getenv("GONNER_HEALTH_TOKEN"); t != "" {
			opts.AuthToken = t
		}
		healthSrv := health.NewServerWithOptions(opts, mgr)
		if err := healthSrv.Start(ctx); err != nil {
			return fmt.Errorf("failed to start health endpoint: %w", err)
		}
	}

	logging.Gonner("Gonner starting (PID %d)", os.Getpid())

	// Write PID file if configured.
	if cfg.PIDFile != "" {
		if err := os.WriteFile(cfg.PIDFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644); err != nil {
			logging.Gonner("warning: failed to write pidFile %s: %v", cfg.PIDFile, err)
		} else {
			defer os.Remove(cfg.PIDFile)
		}
	}

	// Run all processes (blocks until done)
	if err := mgr.Run(ctx); err != nil {
		return fmt.Errorf("manager exited with error: %w", err)
	}

	logging.Gonner("Gonner shutdown complete")
	return nil
}

func resolveHealthPort(cfg *config.Config) int {
	// CLI flag takes priority
	if healthPort > 0 {
		return healthPort
	}

	// Then env var
	if envPort := os.Getenv("GONNER_HEALTH_PORT"); envPort != "" {
		var port int
		if _, err := fmt.Sscanf(envPort, "%d", &port); err == nil && port > 0 {
			return port
		}
	}

	// Then config
	if cfg.Health != nil && cfg.Health.Port > 0 {
		return cfg.Health.Port
	}

	return 0
}

// resolveHealthBind returns the bind address for the health endpoint.
// Priority: CLI flag > GONNER_HEALTH_BIND env var > config > "0.0.0.0".
func resolveHealthBind(cfg *config.Config) string {
	if healthBindAddr != "" {
		return healthBindAddr
	}
	if v := os.Getenv("GONNER_HEALTH_BIND"); v != "" {
		return v
	}
	if cfg.Health != nil && cfg.Health.BindAddr != "" {
		return cfg.Health.BindAddr
	}
	return "0.0.0.0"
}
