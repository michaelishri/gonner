package cmd

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/michaelishri/gonner/internal/health"
)

func TestExplicitDefaultStatusPortOverridesEnvironment(t *testing.T) {
	old := statusPort
	defer func() { statusPort = old }()
	t.Setenv("GONNER_HEALTH_PORT", "9999")
	command := &cobra.Command{}
	command.Flags().IntVar(&statusPort, "port", health.DefaultHealthPort, "")
	if got := resolveStatusPort(command.Flags().Changed("port")); got != 9999 {
		t.Fatalf("env port=%d", got)
	}
	if err := command.Flags().Set("port", "8089"); err != nil {
		t.Fatal(err)
	}
	if got := resolveStatusPort(command.Flags().Changed("port")); got != 8089 {
		t.Fatalf("explicit port=%d", got)
	}
}
