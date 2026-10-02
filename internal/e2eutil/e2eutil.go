// Package e2eutil provides helpers to run the alertmanager2mackerel binary in end-to-end tests.
package e2eutil

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

// Binary builds the alertmanager2mackerel binary once and returns its path.
func Binary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "alertmanager2mackerel-e2e")
		if err != nil {
			buildErr = err
			return
		}
		binPath = filepath.Join(dir, "alertmanager2mackerel")
		cmd := exec.Command("go", "build", "-o", binPath, "./cmd/alertmanager2mackerel")
		cmd.Dir = moduleRoot()
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build failed: %w\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binPath
}

func moduleRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// FreeAddr returns a free local TCP address.
func FreeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// Proxy is a running alertmanager2mackerel process.
type Proxy struct {
	URL string
	cmd *exec.Cmd
	log *syncBuffer
}

// Log returns the log output of the process.
func (p *Proxy) Log() string {
	return p.log.String()
}

// WaitLog waits until the log contains s.
func (p *Proxy) WaitLog(t *testing.T, s string, timeout time.Duration) {
	t.Helper()
	Eventually(t, timeout, func() bool { return strings.Contains(p.Log(), s) }, "log %q not found:\n%s", s, p.Log())
}

// StartProxy starts alertmanager2mackerel listening on addr and waits until it is ready.
func StartProxy(t *testing.T, addr string, env []string, args ...string) *Proxy {
	t.Helper()
	cmd := exec.Command(Binary(t), append([]string{"--listen", addr, "--log-format", "json"}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	logBuf := &syncBuffer{}
	cmd.Stdout = logBuf
	cmd.Stderr = logBuf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &Proxy{URL: "http://" + addr, cmd: cmd, log: logBuf}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
		}
		if t.Failed() {
			t.Logf("proxy log:\n%s", logBuf)
		}
	})
	Eventually(t, 10*time.Second, func() bool {
		resp, err := http.Get(p.URL + "/health")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, "proxy did not become ready:\n%s", logBuf)
	return p
}

// Run runs the binary until it exits and returns its combined output and error.
func Run(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, Binary(t), args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Eventually waits until cond returns true, or fails the test after timeout.
func Eventually(t *testing.T, timeout time.Duration, cond func() bool, format string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf(format, args...)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
