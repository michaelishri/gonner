// Package cmd defines the CLI commands for gonner.
package cmd

import "github.com/spf13/cobra"

// Version info — set via ldflags at build time.
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

var configPath string

var rootCmd = &cobra.Command{
	Use:   "gonner",
	Short: "A lightweight process manager for containers",
	Long: `Gonner is a PID-1-aware process manager written in Go.
It runs and monitors multiple processes from a single configuration file,
with support for conditional startup, auto-restart, dependency ordering,
and health checking.`,
	// Default to "run" when no subcommand is given
	RunE: runRun,
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&configPath, "config", "c", "", "path to config file or directory")
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}
