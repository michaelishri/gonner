package logging

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriter_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "secure.log")

	w, err := NewWriterWithOptions(Options{ProcessName: "p", LogFilePath: logPath})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("x\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm: got %o want 0600", info.Mode().Perm())
	}
}

func TestWriter_Rotation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "rotate.log")

	w := &Writer{
		processName: "p",
		stdout:      io.Discard,
		logFilePath: logPath,
		logFileMode: 0o600,
		rotate:      &RotateOptions{MaxSizeMB: 1, MaxBackups: 2},
	}
	if err := w.openLogFile(); err != nil {
		t.Fatalf("new: %v", err)
	}
	defer w.Close()

	// Write > 1 MB to trigger rotation.
	big := strings.Repeat("a", 600*1024) + "\n"
	for i := 0; i < 4; i++ {
		if _, err := w.Write([]byte(big)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) < 2 {
		t.Errorf("expected at least 2 files after rotation, got %d", len(entries))
	}
}
