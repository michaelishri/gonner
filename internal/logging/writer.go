// Package logging multiplexes bounded output chunks to console and shared files.
package logging

import (
	"bufio"
	"compress/gzip"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/michaelishri/gonner/internal/safefile"
)

type RotateOptions struct {
	MaxSizeMB, MaxBackups int
	Compress              bool
}
type Options struct {
	ProcessName, LogFilePath string
	LogFileMode              os.FileMode
	Rotate                   *RotateOptions
}
type Writer struct {
	processName string
	stdout      io.Writer
	sink        *fileSink
	mu          sync.Mutex
	closed      bool
	lastError   time.Time
}
type fileSink struct {
	mu        sync.Mutex
	dir       *safefile.Directory
	name, key string
	file      *os.File
	mode      os.FileMode
	rotate    RotateOptions
	written   int64
	refs      int
}

var sinks = struct {
	sync.Mutex
	files map[string]*fileSink
}{files: make(map[string]*fileSink)}
var activePaths sync.Map
var consoleMu sync.Mutex

func NewWriter(name, path string) (*Writer, error) {
	return NewWriterWithOptions(Options{ProcessName: name, LogFilePath: path})
}
func NewWriterWithOptions(opts Options) (*Writer, error) {
	w := &Writer{processName: opts.ProcessName, stdout: os.Stdout}
	if opts.LogFilePath == "" {
		return w, nil
	}
	mode := opts.LogFileMode
	if mode == 0 {
		mode = 0600
	}
	dir, err := safefile.OpenDirectory(filepath.Dir(opts.LogFilePath))
	if err != nil {
		return nil, fmt.Errorf("log directory: %w", err)
	}
	name := filepath.Base(opts.LogFilePath)
	key := dir.Key + "/" + name
	rotate := RotateOptions{}
	if opts.Rotate != nil {
		rotate = *opts.Rotate
	}
	sinks.Lock()
	defer sinks.Unlock()
	if s := sinks.files[key]; s != nil {
		_ = dir.Close()
		if s.mode != mode || s.rotate != rotate {
			return nil, fmt.Errorf("conflicting log settings for %s", opts.LogFilePath)
		}
		s.refs++
		w.sink = s
		return w, nil
	}
	f, err := dir.OpenRegular(name, os.O_WRONLY|os.O_CREATE|os.O_APPEND, mode)
	if err != nil {
		_ = dir.Close()
		return nil, fmt.Errorf("opening log: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		_ = dir.Close()
		return nil, err
	}
	s := &fileSink{dir: dir, name: name, key: key, file: f, mode: mode, rotate: rotate, written: st.Size(), refs: 1}
	sinks.files[key] = s
	activePaths.Store(key, true)
	w.sink = s
	return w, nil
}

// Write always attempts both destinations. A file error never stops console
// output or future file retries. Callers must keep draining after write errors.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, os.ErrClosed
	}
	var fileErr error
	if w.sink != nil {
		fileErr = w.sink.write(p)
	}
	consoleMu.Lock()
	_, consoleErr := fmt.Fprintf(w.stdout, "[%s] [%s] %s", time.Now().UTC().Format(time.RFC3339), w.processName, p)
	// Continuation chunks and unterminated final lines get a console newline;
	// raw files receive exactly p, including its original lack of a newline.
	if len(p) > 0 && p[len(p)-1] != '\n' {
		_, err := fmt.Fprintln(w.stdout)
		consoleErr = errors.Join(consoleErr, err)
	}
	consoleMu.Unlock()
	err := errors.Join(fileErr, consoleErr)
	now := time.Now()
	if err != nil {
		if w.lastError.IsZero() || now.Sub(w.lastError) >= time.Minute {
			Gonner("Output error for %q (continuing to drain): %v", w.processName, err)
			w.lastError = now
		}
	} else if !w.lastError.IsZero() {
		Gonner("Output recovered for %q", w.processName)
		w.lastError = time.Time{}
	}
	return len(p), err
}
func (s *fileSink) write(p []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rotationErr error
	if s.rotate.MaxSizeMB > 0 && s.written > 0 && s.written+int64(len(p)) > int64(s.rotate.MaxSizeMB)*1024*1024 {
		rotationErr = s.rotateFile()
	}
	n, err := s.file.Write(p)
	s.written += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return errors.Join(rotationErr, err)
}
func (s *fileSink) rotateFile() error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	backup := fmt.Sprintf("%s.gonner-%s-%x.log", s.name, time.Now().UTC().Format("20060102T150405.000000000Z"), nonce)
	// Link reserves the backup exclusively. Retain the original fd until its
	// replacement is fully opened, and roll back if opening fails.
	if err := s.dir.Link(s.name, backup); err != nil {
		return err
	}
	if err := s.dir.Remove(s.name); err != nil {
		_ = s.dir.Remove(backup)
		return err
	}
	next, err := s.dir.OpenRegular(s.name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, s.mode)
	if err != nil {
		if restoreErr := s.dir.Link(backup, s.name); restoreErr == nil {
			_ = s.dir.Remove(backup)
		}
		return err
	}
	old := s.file
	s.file = next
	s.written = 0
	closeErr := old.Close()
	if s.rotate.Compress {
		if err := s.compress(backup); err != nil {
			return errors.Join(closeErr, err)
		}
	}
	return errors.Join(closeErr, s.pruneBackups())
}
func (s *fileSink) compress(name string) error {
	in, err := s.dir.OpenRegular(name, os.O_RDONLY, s.mode)
	if err != nil {
		return err
	}
	defer in.Close()
	temp := name + ".gz.tmp"
	out, err := s.dir.OpenRegular(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, s.mode)
	if err != nil {
		return err
	}
	defer s.dir.Remove(temp)
	gz := gzip.NewWriter(out)
	_, copyErr := io.Copy(gz, in)
	gzipErr := gz.Close()
	closeErr := out.Close()
	if err = errors.Join(copyErr, gzipErr, closeErr); err != nil {
		return err
	}
	if err = s.dir.Link(temp, name+".gz"); err != nil {
		return err
	}
	if err = s.dir.Remove(temp); err != nil {
		return err
	}
	return s.dir.Remove(name)
}
func (s *fileSink) pruneBackups() error {
	if s.rotate.MaxBackups <= 0 {
		return nil
	}
	entries, err := s.dir.Entries()
	if err != nil {
		return err
	}
	pattern := regexp.MustCompile("^" + regexp.QuoteMeta(s.name) + `\.gonner-[0-9]{8}T[0-9]{6}\.[0-9]{9}Z-[0-9a-f]{32}\.log(\.gz)?$`)
	var backups []string
	for _, entry := range entries {
		n := entry.Name()
		if !pattern.MatchString(n) || !entry.Type().IsRegular() {
			continue
		}
		if _, active := activePaths.Load(s.dir.Key + "/" + n); active {
			continue
		}
		// Descriptor-relative validation excludes links, devices and untrusted files.
		f, e := s.dir.OpenRegular(n, os.O_RDONLY, s.mode)
		if e != nil {
			continue
		}
		_ = f.Close()
		backups = append(backups, n)
	}
	sort.Strings(backups)
	for len(backups) > s.rotate.MaxBackups {
		if err := s.dir.Remove(backups[0]); err != nil {
			return err
		}
		backups = backups[1:]
	}
	return nil
}
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.sink == nil {
		return nil
	}
	s := w.sink
	sinks.Lock()
	defer sinks.Unlock()
	s.refs--
	if s.refs > 0 {
		return nil
	}
	delete(sinks.files, s.key)
	activePaths.Delete(s.key)
	s.mu.Lock()
	defer s.mu.Unlock()
	return errors.Join(s.file.Close(), s.dir.Close())
}

// LineScanner preserves all bytes, using at most 64 KiB for each stream chunk.
// ReadSlice returns oversized lines in fragments instead of stopping the drain.
// Destination failures are reported by Writer; only read errors end a stream.
func LineScanner(r io.Reader, w *Writer) error {
	reader := bufio.NewReaderSize(r, 64*1024)
	for {
		part, err := reader.ReadSlice('\n')
		if len(part) > 0 {
			_, _ = w.Write(part)
		}
		if err == nil || errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
}
func Gonner(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "[%s] [gonner] %s\n", time.Now().UTC().Format(time.RFC3339), strings.TrimSuffix(msg, "\n"))
}
func Recover(name string) {
	if r := recover(); r != nil {
		Gonner("PANIC in %s: %v", name, r)
	}
}
