// Package control exposes management only over a protected same-UID Unix socket.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/michaelishri/gonner/internal/config"
	"github.com/michaelishri/gonner/internal/runner"
)

const Version = 1

type Manager interface {
	Instances() []runner.InstanceInfo
	Restart(string, string, time.Duration) error
	IsShuttingDown() bool
}

func Start(ctx context.Context, cfg config.ControlConfig, mgr Manager) (func(), error) {
	dir := filepath.Dir(cfg.Socket)
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 || !owned(info) {
		return nil, errors.New("control socket requires private owned directory")
	}
	if resolved, err := filepath.EvalSymlinks(dir); err != nil || resolved != dir {
		return nil, errors.New("control socket directory must be canonical")
	}
	if info, err := os.Lstat(cfg.Socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 || !owned(info) {
			return nil, errors.New("unsafe control socket")
		}
		conn, dialErr := net.DialTimeout("unix", cfg.Socket, 100*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			return nil, errors.New("control socket in use")
		}
		if !refused(dialErr) {
			return nil, dialErr
		}
		if err := os.Remove(cfg.Socket); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: cfg.Socket, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(cfg.Socket, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	server := &http.Server{Handler: Handler(cfg, mgr), ReadHeaderTimeout: time.Second, ReadTimeout: time.Second,
		WriteTimeout: time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 4096, ErrorLog: log.New(io.Discard, "", 0)}
	secured := &peerListener{UnixListener: listener, slots: make(chan struct{}, 16)}
	stop := func() { server.Close(); listener.Close() }
	go func() {
		if err := server.Serve(secured); err != nil && !errors.Is(err, http.ErrServerClosed) {
			stop()
		}
	}()
	go func() { <-ctx.Done(); stop() }()
	return stop, nil
}

func Handler(cfg config.ControlConfig, mgr Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.RawQuery != "" || r.URL.RawPath != "" {
			w.WriteHeader(400)
			return
		}
		if r.Method == "GET" && r.URL.Path == "/v1/instances" {
			_ = json.NewEncoder(w).Encode(struct {
				Version      int                   `json:"version"`
				ShuttingDown bool                  `json:"shuttingDown"`
				Instances    []runner.InstanceInfo `json:"instances"`
			}{Version, mgr.IsShuttingDown(), mgr.Instances()})
			return
		}
		if r.Method != "POST" || r.URL.Path != "/v1/restart" {
			w.WriteHeader(404)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(415)
			return
		}
		var target struct {
			Instance           string `json:"instance"`
			ExpectedGeneration string `json:"expectedGeneration"`
		}
		// Reject duplicate keys as well as unknown fields and trailing documents.
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		token, err := decoder.Token()
		if err != nil || token != json.Delim('{') {
			w.WriteHeader(400)
			return
		}
		seen := map[string]bool{}
		for decoder.More() {
			k, err := decoder.Token()
			key, ok := k.(string)
			if err != nil || !ok || seen[key] {
				w.WriteHeader(400)
				return
			}
			seen[key] = true
			var value string
			if decoder.Decode(&value) != nil {
				w.WriteHeader(400)
				return
			}
			switch key {
			case "instance":
				target.Instance = value
			case "expectedGeneration":
				target.ExpectedGeneration = value
			default:
				w.WriteHeader(400)
				return
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			w.WriteHeader(400)
			return
		}
		if _, err := decoder.Token(); err != io.EOF || target.Instance == "" || len(target.ExpectedGeneration) != 32 {
			w.WriteHeader(400)
			return
		}
		if mgr.Restart(target.Instance, target.ExpectedGeneration, time.Duration(cfg.RestartGrace)) != nil {
			w.WriteHeader(409)
			return
		}
		w.WriteHeader(202)
		_ = json.NewEncoder(w).Encode(map[string]any{"version": Version, "accepted": true, "generation": target.ExpectedGeneration})
	})
}

type peerListener struct {
	*net.UnixListener
	slots chan struct{}
}
type limitedConn struct {
	net.Conn
	release func()
}

func (c *limitedConn) Close() error { err := c.Conn.Close(); c.release(); return err }
func (l *peerListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		if !sameUID(conn) {
			conn.Close()
			continue
		}
		select {
		case l.slots <- struct{}{}:
			return wrap(conn, func() { <-l.slots }), nil
		default:
			conn.Close()
		}
	}
}
