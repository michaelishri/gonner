package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/michaelishri/gonner/internal/config"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Validate the configuration file",
		Long:  "Load and validate the config file without starting any processes. Useful for CI/CD pipelines.",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfgPath, err := config.Discover(configPath)
			if err != nil {
				return fmt.Errorf("config discovery failed: %w", err)
			}
			fmt.Printf("Config file: %s\n", cfgPath)

			cfg, err := config.Parse(cfgPath)
			if err != nil {
				return fmt.Errorf("parse error: %w", err)
			}

			result, err := config.ValidateWithWarnings(cfg)
			if err != nil {
				return fmt.Errorf("validation failed:\n%w", err)
			}

			for _, w := range result.Warnings {
				fmt.Printf("Warning: %s\n", w)
			}

			fmt.Printf("Mode: %s\n", cfg.Mode)
			fmt.Printf("Processes: %d\n", len(cfg.Run))
			for _, proc := range cfg.Run {
				instances := proc.Instances
				fmt.Printf("  - %s (instances: %d, autoRestart: %v, critical: %v)\n",
					proc.Name, instances, proc.AutoRestart, proc.Critical)
			}
			fmt.Println("Validation passed!")
			return nil
		},
	})
}
