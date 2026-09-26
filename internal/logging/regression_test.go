package logging

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func secureTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}
func quietWriter(t *testing.T, opts Options) *Writer {
	t.Helper()
	w, err := NewWriterWithOptions(opts)
	if err != nil {
		t.Fatal(err)
	}
	w.stdout = io.Discard
	t.Cleanup(func() { _ = w.Close() })
	return w
}
func TestOversizedAndUnterminatedOutputIsByteExact(t *testing.T) {
	path := filepath.Join(secureTempDir(t), "output.log")
	w := quietWriter(t, Options{ProcessName: "large", LogFilePath: path})
	var console bytes.Buffer
	w.stdout = &console
	input := bytes.Repeat([]byte("x"), 3*1024*1024+17)
	input = append(input, []byte("\nlast line without newline")...)
	if err := LineScanner(bytes.NewReader(input), w); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, input) {
		t.Fatalf("raw length=%d want=%d", len(got), len(input))
	}
	if n := strings.Count(console.String(), "[large]"); n != 50 {
		t.Fatalf("chunk prefixes=%d want=50", n)
	}
}
func TestRapidRotationAndSharedSinkPreserveEveryByte(t *testing.T) {
	for _, compress := range []bool{false, true} {
		t.Run(fmt.Sprint(compress), func(t *testing.T) {
			dir := secureTempDir(t)
			path := filepath.Join(dir, "shared.log")
			const workers = 4
			const writes = 20
			chunkSize := 96 * 1024
			var wg sync.WaitGroup
			for i := 0; i < workers; i++ {
				w := quietWriter(t, Options{ProcessName: fmt.Sprint(i), LogFilePath: path, Rotate: &RotateOptions{MaxSizeMB: 1, Compress: compress}})
				wg.Go(func() {
					data := bytes.Repeat([]byte{byte('A' + i)}, chunkSize)
					for j := 0; j < writes; j++ {
						if _, err := w.Write(data); err != nil {
							t.Error(err)
							return
						}
					}
				})
			}
			wg.Wait()
			var counts [workers]int
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) < 5 {
				t.Fatalf("only %d files after rapid rotation", len(entries))
			}
			for _, entry := range entries {
				f, err := os.Open(filepath.Join(dir, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				var r io.Reader = f
				var gz *gzip.Reader
				if strings.HasSuffix(entry.Name(), ".gz") {
					gz, err = gzip.NewReader(f)
					if err != nil {
						t.Fatal(err)
					}
					r = gz
				}
				data, err := io.ReadAll(r)
				if err != nil {
					t.Fatal(err)
				}
				if gz != nil {
					_ = gz.Close()
				}
				_ = f.Close()
				for _, b := range data {
					if b < 'A' || b >= 'A'+workers {
						t.Fatalf("unexpected byte %q", b)
					}
					counts[b-'A']++
				}
			}
			for i, n := range counts {
				if n != writes*chunkSize {
					t.Errorf("worker%d bytes=%d want=%d", i, n, writes*chunkSize)
				}
			}
		})
	}
}
func TestSharedPathRejectsConflictingSettings(t *testing.T) {
	dir := secureTempDir(t)
	path := filepath.Join(dir, "same.log")
	quietWriter(t, Options{ProcessName: "a", LogFilePath: path})
	_, err := NewWriterWithOptions(Options{ProcessName: "b", LogFilePath: filepath.Join(dir, ".", "same.log"), LogFileMode: 0640})
	if err == nil {
		t.Fatal("accepted conflicting settings")
	}
}
func TestPruningPreservesUnrelatedLegacyAndActivePaths(t *testing.T) {
	dir := secureTempDir(t)
	base := "app.log"
	protected := []string{base + ".notes", base + ".20260101T000000Z", base + ".gonner-invalid.log", base + ".gonner-20000101T000000.000000000Z-" + strings.Repeat("0", 32) + ".log"}
	for _, name := range protected {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	quietWriter(t, Options{ProcessName: "other", LogFilePath: filepath.Join(dir, protected[3])})
	w := quietWriter(t, Options{ProcessName: "a", LogFilePath: filepath.Join(dir, base), Rotate: &RotateOptions{MaxSizeMB: 1, MaxBackups: 1}})
	for i := 0; i < 6; i++ {
		if _, err := w.Write(bytes.Repeat([]byte("x"), 700*1024)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range protected {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(b) != "keep" {
			t.Errorf("protected %s changed: %q, %v", name, b, err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != len(protected)+2 {
		t.Fatalf("backup retention: %d entries", len(entries))
	}
}
func TestFileErrorStillWritesConsoleAndRecovers(t *testing.T) {
	w := quietWriter(t, Options{ProcessName: "failure", LogFilePath: filepath.Join(secureTempDir(t), "app.log")})
	var console bytes.Buffer
	w.stdout = &console
	original := w.sink.file
	closed, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	w.sink.file = closed
	if _, err := w.Write([]byte("visible\n")); err == nil {
		t.Fatal("expected file failure")
	}
	if !strings.Contains(console.String(), "visible") {
		t.Fatal("console lost output")
	}
	w.sink.file = original
	if _, err := w.Write([]byte("recovered\n")); err != nil {
		t.Fatal(err)
	}
	if !w.lastError.IsZero() {
		t.Fatal("error state did not recover")
	}
}
func TestRotationFailureKeepsOpenSinkUsable(t *testing.T) {
	dir := secureTempDir(t)
	path := filepath.Join(dir, "app.log")
	w := quietWriter(t, Options{ProcessName: "a", LogFilePath: path, Rotate: &RotateOptions{MaxSizeMB: 1}})
	data := bytes.Repeat([]byte("x"), 700*1024)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(dir, "saved.log")
	if err := os.Rename(path, saved); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err == nil {
		t.Fatal("expected rotation failure")
	}
	b, err := os.ReadFile(saved)
	if err != nil || len(b) != 2*len(data) {
		t.Fatalf("open sink lost: %d %v", len(b), err)
	}
}
