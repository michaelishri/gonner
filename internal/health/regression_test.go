package health

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/michaelishri/gonner/internal/config"
	"github.com/michaelishri/gonner/internal/runner"
)

func certificates(t *testing.T) (string, string) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	cert, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	rawKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rawKey}), 0600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}
func TestTLSValidationIsSynchronousAndDoesNotLeakListener(t *testing.T) {
	cert, key := certificates(t)
	_, otherKey := certificates(t)
	bad := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(bad, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"/gonner-missing-cert", key}, {bad, key}, {cert, otherKey}} {
		port := freePort(t)
		srv := NewServerWithOptions(Options{BindAddr: "127.0.0.1", Port: port, TLS: &config.TLSConfig{CertFile: pair[0], KeyFile: pair[1]}}, runner.NewManager(&config.Config{}))
		if err := srv.Start(context.Background()); err == nil {
			t.Fatal("invalid TLS accepted")
		}
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			t.Fatalf("failed TLS leaked listener: %v", err)
		}
		_ = l.Close()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewServerWithOptions(Options{BindAddr: "127.0.0.1", Port: 0, TLS: &config.TLSConfig{CertFile: cert, KeyFile: key}}, runner.NewManager(&config.Config{}))
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, Timeout: time.Second} // test certificate
	defer client.CloseIdleConnections()
	resp, err := client.Get("https://" + srv.listener.Addr().String() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	cancel()
	<-srv.Done()
}
func TestUnexpectedServeFailureIsReported(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewServerWithOptions(Options{BindAddr: "127.0.0.1", Port: 0}, runner.NewManager(&config.Config{}))
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	_ = srv.listener.Close()
	select {
	case err := <-srv.Errors():
		if err == nil {
			t.Fatal("serve failure suppressed")
		}
	case <-time.After(time.Second):
		t.Fatal("serve failure not reported")
	}
	<-srv.Done()
}
func TestReadyAndStatusIncludePendingAndSkippedCritical(t *testing.T) {
	mgr := runner.NewManager(&config.Config{Run: []config.ProcessConfig{{Name: "critical", Command: "true", Critical: true, Instances: 2, WhenAll: []map[string]string{{"fileExists": "/gonner-missing"}}}, {Name: "other", Command: "exec sleep 30"}}, ShutdownTimeout: config.Duration(100 * time.Millisecond)})
	srv := NewServer(0, mgr)
	check := func() {
		t.Helper()
		w := httptest.NewRecorder()
		srv.handleReady(w, httptest.NewRequest("GET", "/ready", nil))
		if w.Code != 503 {
			t.Fatalf("ready=%d", w.Code)
		}
	}
	check()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Run(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(time.Second)
	for mgr.Processes()[0].Status != runner.StateSkipped {
		if time.Now().After(deadline) {
			t.Fatal("critical condition did not resolve")
		}
		time.Sleep(time.Millisecond)
	}
	if mgr.IsShuttingDown() {
		t.Fatal("intentional skip should not terminate unrelated work")
	}
	check()
	w := httptest.NewRecorder()
	srv.handleStatus(w, httptest.NewRequest("GET", "/status", nil))
	body := w.Body.String()
	if !strings.Contains(body, `"status":"skipped"`) || !strings.Contains(body, `"instances":2`) {
		t.Fatalf("status=%s", body)
	}
}
