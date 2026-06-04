// Package logging provides multiplexed log writers for gonner processes.
package logging

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RotateOptions configures size-based log rotation.
type RotateOptions struct {
	MaxSizeMB  int  // rotate when file exceeds this size (0 = disabled)
	MaxBackups int  // number of rotated files to retain (0 = unlimited)
	Compress   bool // gzip rotated files
}

// Options configures a Writer.
type Options struct {
	ProcessName string
	LogFilePath string
	LogFileMode os.FileMode // default 0o600 if 0
	Rotate      *RotateOptions
}

// Writer is a multiplexed writer that writes process output to both
// gonner's stdout (with prefix) and an optional log file (raw).
type Writer struct {
	processName string
	stdout      io.Writer
	logFile     *os.File
	logFilePath string
	logFileMode os.FileMode
	rotate      *RotateOptions
	written     int64
	mu          sync.Mutex
}

// NewWriter creates a multiplexed writer with default settings.
// Kept for backwards compatibility; for new code, use NewWriterWithOptions.
func NewWriter(processName, logFilePath string) (*Writer, error) {
	return NewWriterWithOptions(Options{
		ProcessName: processName,
		LogFilePath: logFilePath,
	})
}

// NewWriterWithOptions creates a multiplexed writer using the provided options.
func NewWriterWithOptions(opts Options) (*Writer, error) {
	mode := opts.LogFileMode
	if mode == 0 {
		mode = 0o600
	}
	w := &Writer{
		processName: opts.ProcessName,
		stdout:      os.Stdout,
		logFilePath: opts.LogFilePath,
		logFileMode: mode,
		rotate:      opts.Rotate,
	}

	if opts.LogFilePath != "" {
		if err := w.openLogFile(); err != nil {
			return nil, err
		}
	}

	return w, nil
}

func (w *Writer) openLogFile() error {
	dir := filepath.Dir(w.logFilePath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("creating log directory %s: %w", dir, err)
	}

	f, err := os.OpenFile(w.logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, w.logFileMode)
	if err != nil {
		return fmt.Errorf("opening log file %s: %w", w.logFilePath, err)
	}

	// Enforce permissions even if the file existed.
	if err := os.Chmod(w.logFilePath, w.logFileMode); err != nil {
		Gonner("warning: chmod %s: %v", w.logFilePath, err)
	}

	w.logFile = f

	if info, err := f.Stat(); err == nil {
		w.written = info.Size()
	}

	return nil
}

// Write implements io.Writer. Each write is prefixed with timestamp and process name
// on stdout, and written raw to the log file. Performs rotation if configured.
func (w *Writer) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.logFile != nil {
		if err := w.maybeRotate(len(p)); err != nil {
			Gonner("warning: log rotation failed for %s: %v", w.processName, err)
		}
		if _, werr := w.logFile.Write(p); werr != nil {
			return 0, fmt.Errorf("writing to log file: %w", werr)
		}
		w.written += int64(len(p))
	}

	ts := time.Now().UTC().Format(time.RFC3339)
	prefix := fmt.Sprintf("[%s] [%s] ", ts, w.processName)
	if _, err := fmt.Fprintf(w.stdout, "%s%s", prefix, p); err != nil {
		return 0, fmt.Errorf("writing to stdout: %w", err)
	}

	return len(p), nil
}

// maybeRotate rotates the active log file if it would exceed the configured size.
// Called with the lock held.
func (w *Writer) maybeRotate(incoming int) error {
	if w.rotate == nil || w.rotate.MaxSizeMB <= 0 || w.logFile == nil {
		return nil
	}
	limit := int64(w.rotate.MaxSizeMB) * 1024 * 1024
	if w.written+int64(incoming) <= limit {
		return nil
	}

	if err := w.logFile.Close(); err != nil {
		return err
	}

	ts := time.Now().UTC().Format("20060102T150405Z")
	rotated := w.logFilePath + "." + ts
	if err := os.Rename(w.logFilePath, rotated); err != nil {
		return err
	}

	if w.rotate.Compress {
		if err := gzipFile(rotated); err != nil {
			Gonner("warning: gzip %s: %v", rotated, err)
		} else {
			_ = os.Remove(rotated)
		}
	}

	if w.rotate.MaxBackups > 0 {
		w.pruneBackups()
	}

	w.written = 0
	return w.openLogFile()
}

func (w *Writer) pruneBackups() {
	dir := filepath.Dir(w.logFilePath)
	base := filepath.Base(w.logFilePath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var matches []string
	for _, e := range entries {
		name := e.Name()
		if name == base {
			continue
		}
		if strings.HasPrefix(name, base+".") {
			matches = append(matches, filepath.Join(dir, name))
		}
	}
	sort.Strings(matches)
	if len(matches) <= w.rotate.MaxBackups {
		return
	}
	excess := matches[:len(matches)-w.rotate.MaxBackups]
	for _, p := range excess {
		_ = os.Remove(p)
	}
}

func gzipFile(path string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(path+".gz", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()

	gz := gzip.NewWriter(out)
	if _, err := io.Copy(gz, in); err != nil {
		_ = gz.Close()
		return err
	}
	return gz.Close()
}

// Close closes the underlying log file if one was opened.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.logFile != nil {
		err := w.logFile.Close()
		w.logFile = nil
		return err
	}
	return nil
}

// LineScanner reads from a reader line-by-line and writes each line to the writer.
// This ensures log prefixes are applied cleanly per line rather than mid-line.
// It blocks until the reader is exhausted or an error occurs.
func LineScanner(r io.Reader, w *Writer) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		_, _ = w.Write([]byte(line + "\n"))
	}
}

// Gonner writes gonner's own operational messages to stderr.
func Gonner(format string, args ...interface{}) {
	ts := time.Now().UTC().Format(time.RFC3339)
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "[%s] [gonner] %s\n", ts, msg)
}

// Recover is a panic recovery helper for long-running goroutines.
// Use as: defer logging.Recover("name").
func Recover(name string) {
	if r := recover(); r != nil {
		Gonner("PANIC in %s: %v", name, r)
	}
}
