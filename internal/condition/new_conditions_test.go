package condition

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestPortOpen_Open(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	cond := NewPortOpenCondition(l.Addr().String())
	ok, err := cond.Evaluate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected port to be reachable")
	}
}

func TestPortOpen_Closed(t *testing.T) {
	// Use a port that's very likely unused (high random port).
	cond := NewPortOpenCondition("127.0.0.1:1@200ms")
	ok, err := cond.Evaluate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected port to be unreachable")
	}
}

func TestPortOpen_Type(t *testing.T) {
	cond := NewPortOpenCondition(":80")
	if cond.Type() != "portOpen" {
		t.Errorf("Type(): got %q", cond.Type())
	}
}

func TestCommandSucceeds_True(t *testing.T) {
	cond := NewCommandSucceedsCondition("true")
	ok, err := cond.Evaluate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected `true` to succeed")
	}
}

func TestCommandSucceeds_False(t *testing.T) {
	cond := NewCommandSucceedsCondition("false")
	ok, err := cond.Evaluate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected `false` to fail")
	}
}

func TestCommandSucceeds_Type(t *testing.T) {
	cond := NewCommandSucceedsCondition("ls")
	if cond.Type() != "commandSucceeds" {
		t.Errorf("Type(): got %q", cond.Type())
	}
}

func TestPortOpenBareAndColonPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{port, ":" + port, "localhost:" + port, l.Addr().String()} {
		ok, err := NewPortOpenCondition(target).Evaluate(context.Background())
		if err != nil || !ok {
			t.Errorf("%q: %v %v", target, ok, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewPortOpenCondition(port).Evaluate(ctx); err != context.Canceled {
		t.Fatalf("cancel: %v", err)
	}
}
func TestCommandConditionCancellationAndTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewCommandSucceedsCondition("exec sleep 30").Evaluate(ctx); err != context.Canceled {
		t.Fatalf("cancel: %v", err)
	}
	c := NewCommandSucceedsCondition("exec sleep 30").(*CommandSucceedsCondition)
	c.timeout = 20 * time.Millisecond
	if ok, err := c.Evaluate(context.Background()); err != nil || ok {
		t.Fatalf("timeout: %v %v", ok, err)
	}
}
