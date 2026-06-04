package cmd

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/michaelishri/gonner/internal/health"
)

var (
	statusHost     string
	statusPort     int
	statusToken    string
	statusTLS      bool
	statusInsecure bool
)

func init() {
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Query a running gonner instance for process status",
		RunE:  runStatus,
	}

	statusCmd.Flags().StringVar(&statusHost, "host", "127.0.0.1", "host to query")
	statusCmd.Flags().IntVarP(&statusPort, "port", "p", health.DefaultHealthPort, "port to query")
	statusCmd.Flags().StringVarP(&statusToken, "token", "t", "", "bearer token for authenticated endpoints (falls back to GONNER_HEALTH_TOKEN)")
	statusCmd.Flags().BoolVar(&statusTLS, "tls", false, "query over HTTPS")
	statusCmd.Flags().BoolVar(&statusInsecure, "insecure", false, "skip TLS certificate verification (use with --tls)")

	rootCmd.AddCommand(statusCmd)
}

type statusAPIResponse struct {
	Uptime    string `json:"uptime"`
	Mode      string `json:"mode"`
	PID       int    `json:"pid"`
	Processes []struct {
		Name             string `json:"name"`
		Status           string `json:"status"`
		PID              int    `json:"pid"`
		Instances        int    `json:"instances"`
		RunningInstances int    `json:"runningInstances"`
		Restarts         int    `json:"restarts"`
		Uptime           string `json:"uptime"`
		Critical         bool   `json:"critical"`
	} `json:"processes"`
}

func runStatus(_ *cobra.Command, _ []string) error {
	port := resolveStatusPort()
	scheme := "http"
	if statusTLS {
		scheme = "https"
	}
	url := fmt.Sprintf("%s://%s:%d/status", scheme, statusHost, port)

	client := &http.Client{Timeout: 5 * time.Second}
	if statusTLS && statusInsecure {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // opt-in via --insecure
		}
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}
	if token := resolveStatusToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to connect to gonner at %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("unauthorized: set --token or GONNER_HEALTH_TOKEN to query an authenticated endpoint")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gonner returned HTTP %d", resp.StatusCode)
	}

	var status statusAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	fmt.Printf("Gonner PID %d | Mode: %s | Uptime: %s\n\n", status.PID, status.Mode, status.Uptime)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATUS\tPID\tINSTANCES\tRESTARTS\tUPTIME")
	for _, p := range status.Processes {
		pid := "-"
		if p.PID > 0 {
			pid = fmt.Sprintf("%d", p.PID)
		}
		instances := fmt.Sprintf("%d/%d", p.RunningInstances, p.Instances)
		uptime := p.Uptime
		if uptime == "" {
			uptime = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n",
			p.Name, p.Status, pid, instances, p.Restarts, uptime)
	}
	w.Flush()

	return nil
}

// resolveStatusPort determines the status endpoint port.
// CLI flag takes priority, then GONNER_HEALTH_PORT env var, then default.
func resolveStatusPort() int {
	if envPort := os.Getenv("GONNER_HEALTH_PORT"); envPort != "" {
		var port int
		if _, err := fmt.Sscanf(envPort, "%d", &port); err == nil && port > 0 {
			if statusPort == health.DefaultHealthPort {
				return port
			}
		}
	}
	return statusPort
}

// resolveStatusToken returns the bearer token to use for authenticated endpoints.
// The --token flag takes priority, falling back to the GONNER_HEALTH_TOKEN env var.
func resolveStatusToken() string {
	if statusToken != "" {
		return statusToken
	}
	return os.Getenv("GONNER_HEALTH_TOKEN")
}
