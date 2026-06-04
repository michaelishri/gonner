package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// configFileNames are the filenames gonner searches for, in order.
var configFileNames = []string{"gonner.json", "gonner.yaml"}

// Discover searches for a gonner config file using the standard priority order:
// 1. Explicit path (from --config flag)
// 2. Current working directory
// 3. XDG user config (~/.config/gonner/)
// 4. System-wide (/etc/gonner/)
//
// Returns the path to the first config file found, or an error listing all searched paths.
func Discover(explicitPath string) (string, error) {
	var searched []string

	// Priority 1: Explicit path
	if explicitPath != "" {
		path, err := resolveExplicitPath(explicitPath)
		if err == nil {
			return path, nil
		}
		return "", fmt.Errorf("config not found at explicit path %q: %w", explicitPath, err)
	}

	// Priority 2: Current working directory
	cwd, err := os.Getwd()
	if err == nil {
		for _, name := range configFileNames {
			p := filepath.Join(cwd, name)
			searched = append(searched, p)
			if fileExists(p) {
				return p, nil
			}
		}
	}

	// Priority 3: XDG user config
	configDir := xdgConfigHome()
	if configDir != "" {
		for _, name := range configFileNames {
			p := filepath.Join(configDir, "gonner", name)
			searched = append(searched, p)
			if fileExists(p) {
				return p, nil
			}
		}
	}

	// Priority 4: System-wide
	for _, name := range configFileNames {
		p := filepath.Join("/etc/gonner", name)
		searched = append(searched, p)
		if fileExists(p) {
			return p, nil
		}
	}

	return "", fmt.Errorf("no config file found; searched:\n  %s", strings.Join(searched, "\n  "))
}

// resolveExplicitPath handles the --config flag value, which can be a file or directory.
func resolveExplicitPath(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}

	if !info.IsDir() {
		return path, nil
	}

	// It's a directory — look for config files inside it.
	for _, name := range configFileNames {
		p := filepath.Join(path, name)
		if fileExists(p) {
			return p, nil
		}
	}

	return "", fmt.Errorf("no gonner.json or gonner.yaml found in directory %q", path)
}

// xdgConfigHome returns the XDG config home directory.
func xdgConfigHome() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config")
}

// fileExists checks if a file exists and is not a directory.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
