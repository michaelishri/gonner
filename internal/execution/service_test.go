package execution

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWaitDoesNotCloseUndrainedPipes(t *testing.T) {
	service := NewService()
	exited := make(chan struct{})
	var mu sync.Mutex
	var data []byte
	result := service.Run(context.Background(), Spec{Command: "printf '%04096d' 0", OnExit: func(time.Duration) { close(exited) }, Output: func(r io.Reader) error {
		<-exited // intentionally do not read until cmd.Wait has returned
		b, err := io.ReadAll(r)
		mu.Lock()
		data = append(data, b...)
		mu.Unlock()
		return err
	}})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if string(data) != strings.Repeat("0", 4096) {
		t.Fatalf("tail truncated: %d bytes", len(data))
	}
}
func TestLargeOutputDrainsWithoutDeadlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input")
	input := bytes.Repeat([]byte("x"), 3*1024*1024+17)
	if err := os.WriteFile(path, input, 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []byte
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := NewService().Run(ctx, Spec{Command: "cat '" + path + "'", Output: func(r io.Reader) error {
		b, err := io.ReadAll(r)
		mu.Lock()
		got = append(got, b...)
		mu.Unlock()
		return err
	}})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if !bytes.Equal(input, got) {
		t.Fatalf("got %d bytes, want %d", len(got), len(input))
	}
}
func TestCancellationWaitsForDirectChild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	result := NewService().Run(ctx, Spec{Command: "exec sleep 30", OnStart: func(int) { cancel() }, StopTimeout: 50 * time.Millisecond})
	if result.Err != context.Canceled {
		t.Fatalf("error=%v", result.Err)
	}
	children.Lock()
	defer children.Unlock()
	if len(children.managed) != 0 {
		t.Fatal("managed child retained after wait")
	}
}
func TestCredentialRejectsInvalidAndUnknownIDs(t *testing.T) {
	for _, spec := range [][2]string{{"4294967295", "1"}, {"-1", "1"}, {"4294967296", "1"}, {"4000000000", ""}, {"", "1"}} {
		if _, err := ResolveCredential(spec[0], spec[1]); err == nil {
			t.Errorf("accepted %q", spec)
		}
	}
}

func TestLaterCancellationPreservesObservedExitFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := NewService().Run(ctx, Spec{Command: "exit 19", OnExit: func(time.Duration) { cancel() }})
	if r.Cancelled || r.ExitCode != 19 || r.Err == nil || r.Err == context.Canceled {
		t.Fatalf("lost pre-cancellation failure: %+v", r)
	}
}

func TestTargetedStopUsesOwnGraceAndConfirmsReaping(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stopRequest := make(chan time.Duration, 1)
	started := time.Now()
	r := NewService().Run(ctx, Spec{
		Command:     "trap '' TERM; printf 'ready\\n'; exec sleep 60",
		StopTimeout: 2 * time.Second, StopRequest: stopRequest, ConfirmReaped: true,
		Output: func(reader io.Reader) error {
			buffer := bufio.NewReader(reader)
			line, err := buffer.ReadString('\n')
			if line == "ready\n" {
				stopRequest <- 20 * time.Millisecond
			}
			if err != nil && err != io.EOF {
				return err
			}
			_, err = io.Copy(io.Discard, buffer)
			return err
		},
	})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("targeted stop used global grace: %s", elapsed)
	}
	if ctx.Err() != nil || r.Cancelled || !r.Reaped {
		t.Fatalf("targeted stop affected parent or failed cleanup: %+v, %v", r, ctx.Err())
	}
}
