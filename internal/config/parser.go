package config

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// envPattern matches {{env://VAR_NAME}} and {{env://VAR_NAME:default}}
// Group 1: variable name, Group 2: ":default" (including colon) if present.
var envPattern = regexp.MustCompile(`\{\{env://([A-Za-z_][A-Za-z0-9_]*)(:[^}]*)?\}\}`)

// Parse reads and parses a config file, applying env interpolation and defaults.
func Parse(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	// Apply environment variable interpolation before unmarshalling.
	interpolated, err := interpolateEnv(string(data))
	if err != nil {
		return nil, fmt.Errorf("interpolating env vars: %w", err)
	}

	cfg := Defaults()

	if isYAML(path) {
		if err := yaml.Unmarshal([]byte(interpolated), &cfg); err != nil {
			return nil, fmt.Errorf("parsing YAML config: %w", err)
		}
	} else {
		if err := json.Unmarshal([]byte(interpolated), &cfg); err != nil {
			return nil, fmt.Errorf("parsing JSON config: %w", err)
		}
	}

	cfg.ApplyDefaults()

	return &cfg, nil
}

// interpolateEnv replaces all {{env://VAR}} and {{env://VAR:default}} patterns
// in the raw config string with their corresponding environment variable values.
func interpolateEnv(raw string) (string, error) {
	var missingVars []string

	result := envPattern.ReplaceAllStringFunc(raw, func(match string) string {
		groups := envPattern.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}

		varName := groups[1]
		defaultVal := ""
		hasDefault := len(groups) >= 3 && groups[2] != ""

		if hasDefault {
			defaultVal = groups[2][1:] // strip leading ':'
		}

		value, exists := os.LookupEnv(varName)
		if !exists {
			if hasDefault {
				return defaultVal
			}
			missingVars = append(missingVars, varName)
			return match
		}
		return value
	})

	if len(missingVars) > 0 {
		return "", fmt.Errorf("missing required environment variables: %s", strings.Join(missingVars, ", "))
	}

	return result, nil
}

// isYAML checks if a filepath ends with a YAML extension.
func isYAML(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml")
}
