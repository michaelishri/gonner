package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscover_ExplicitFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "custom.json")
	if err := os.WriteFile(cfgFile, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}

	path, err := Discover(cfgFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != cfgFile {
		t.Errorf("got %q, want %q", path, cfgFile)
	}
}

func TestDiscover_ExplicitDirectory(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "gonner.json")
	if err := os.WriteFile(cfgFile, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}

	path, err := Discover(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != cfgFile {
		t.Errorf("got %q, want %q", path, cfgFile)
	}
}

func TestDiscover_ExplicitDirectoryPreferJSON(t *testing.T) {
	dir := t.TempDir()
	jsonFile := filepath.Join(dir, "gonner.json")
	yamlFile := filepath.Join(dir, "gonner.yaml")
	if err := os.WriteFile(jsonFile, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(yamlFile, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}

	path, err := Discover(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != jsonFile {
		t.Errorf("should prefer JSON: got %q, want %q", path, jsonFile)
	}
}

func TestDiscover_ExplicitDirectoryYAMLFallback(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "gonner.yaml")
	if err := os.WriteFile(yamlFile, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}

	path, err := Discover(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != yamlFile {
		t.Errorf("got %q, want %q", path, yamlFile)
	}
}

func TestDiscover_CWDFallback(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "gonner.json")
	if err := os.WriteFile(cfgFile, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Change to temp dir so CWD search finds it
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	path, err := Discover("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Resolve symlinks (macOS /var -> /private/var)
	expected, _ := filepath.EvalSymlinks(cfgFile)
	actual, _ := filepath.EvalSymlinks(path)
	if actual != expected {
		t.Errorf("got %q, want %q", actual, expected)
	}
}

func TestDiscover_NotFound(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	_, err := Discover("")
	if err == nil {
		t.Fatal("expected error when no config found")
	}
}

func TestDiscover_ExplicitPathNotFound(t *testing.T) {
	_, err := Discover("/nonexistent/path/config.json")
	if err == nil {
		t.Fatal("expected error for nonexistent explicit path")
	}
}

func TestDiscover_ExplicitDirectoryEmpty(t *testing.T) {
	dir := t.TempDir()

	_, err := Discover(dir)
	if err == nil {
		t.Fatal("expected error for empty directory")
	}
}

func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "exists.txt")
	if err := os.WriteFile(f, []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}

	if !fileExists(f) {
		t.Error("expected file to exist")
	}
	if fileExists(filepath.Join(dir, "nope.txt")) {
		t.Error("expected file to not exist")
	}
	// Directory should not count as a file
	if fileExists(dir) {
		t.Error("directory should not count as file")
	}
}

func TestIsYAML(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"config.yaml", true},
		{"config.yml", true},
		{"config.YAML", true},
		{"config.YML", true},
		{"config.json", false},
		{"config.txt", false},
	}

	for _, tt := range tests {
		if got := isYAML(tt.path); got != tt.want {
			t.Errorf("isYAML(%q): got %v, want %v", tt.path, got, tt.want)
		}
	}
}
