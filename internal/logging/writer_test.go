package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriter_StdoutPrefix(t *testing.T) {
	var buf bytes.Buffer
	w := &Writer{
		processName: "myproc",
		stdout:      &buf,
	}

	_, err := w.Write([]byte("hello world\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "[myproc]") {
		t.Errorf("output should contain process name prefix, got: %q", output)
	}
	if !strings.Contains(output, "hello world") {
		t.Errorf("output should contain message, got: %q", output)
	}
	// Should contain a timestamp in RFC3339 format
	if !strings.Contains(output, "T") || !strings.Contains(output, "Z") {
		t.Errorf("output should contain RFC3339 timestamp, got: %q", output)
	}
}

func TestWriter_LogFile(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")

	w, err := NewWriter("proc", logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = w.Write([]byte("log line\n"))
	if err != nil {
		t.Fatalf("write error: %v", err)
	}

	w.Close()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	// Log file should contain raw output (no prefix)
	content := string(data)
	if content != "log line\n" {
		t.Errorf("log file content: got %q, want %q", content, "log line\n")
	}
}

func TestWriter_NoLogFile(t *testing.T) {
	w, err := NewWriter("proc", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer w.Close()

	if w.logFile != nil {
		t.Error("logFile should be nil when no path is given")
	}
}

func TestWriter_LogFileCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "sub", "dir", "test.log")

	w, err := NewWriter("proc", logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer w.Close()

	_, err = w.Write([]byte("test\n"))
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
}

func TestWriter_Close_NoLogFile(t *testing.T) {
	w, _ := NewWriter("proc", "")
	if err := w.Close(); err != nil {
		t.Errorf("Close should not error when no log file: %v", err)
	}
}

func TestLineScanner_MultipleLines(t *testing.T) {
	var buf bytes.Buffer
	w := &Writer{
		processName: "test",
		stdout:      &buf,
	}

	input := strings.NewReader("line1\nline2\nline3\n")
	LineScanner(input, w)

	output := buf.String()
	if strings.Count(output, "[test]") != 3 {
		t.Errorf("expected 3 prefixed lines, got output: %q", output)
	}
}
