package daemonctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func testDaemon(t *testing.T, handler http.Handler) (string, string) {
	t.Helper()
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "daemon.lock")
	socketPath := filepath.Join(dir, "daemon.sock")
	lock := flock.New(lockPath)
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Unlock() })
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return lockPath, socketPath
}

func TestIsRunningRequiresLockAndSocket(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "daemon.lock")
	socketPath := filepath.Join(dir, "daemon.sock")
	if IsRunning(lockPath, socketPath) {
		t.Fatal("unlocked daemon reported running")
	}
	lock := flock.New(lockPath)
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Unlock() }()
	if IsRunning(lockPath, socketPath) {
		t.Fatal("locked daemon with no socket reported running")
	}
	activeLock, activeSocket := testDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	if !IsRunning(activeLock, activeSocket) {
		t.Fatal("listening daemon reported stopped")
	}
}

func TestWaitUntilRunning(t *testing.T) {
	lock, socket := testDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	if err := WaitUntilRunning(context.Background(), lock, socket, time.Second); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if err := WaitUntilRunning(context.Background(), missing, missing, time.Millisecond); err == nil || !strings.Contains(err.Error(), "did not become ready") {
		t.Fatalf("timeout error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WaitUntilRunning(ctx, missing, missing, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestStop(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if err := Stop(missing, missing); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("stopped daemon error = %v", err)
	}
	for _, status := range []int{http.StatusAccepted, http.StatusConflict} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var socket string
			lock, path := testDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/_loom/stop" {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(status)
				if status == http.StatusAccepted {
					if err := os.Remove(socket); err != nil {
						t.Error(err)
					}
				}
			}))
			socket = path
			err := Stop(lock, socket)
			if status == http.StatusAccepted && err != nil {
				t.Fatal(err)
			}
			if status == http.StatusConflict && (err == nil || !strings.Contains(err.Error(), "409")) {
				t.Fatalf("Stop error = %v", err)
			}
		})
	}
}

func TestJSONRequests(t *testing.T) {
	_, socket := testDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get":
			if r.Method != http.MethodGet {
				t.Errorf("method = %s", r.Method)
			}
			_, _ = w.Write([]byte(`{"value":"ok"}`))
		case "/post":
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("request = %s, content type = %q", r.Method, r.Header.Get("Content-Type"))
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["value"] != "sent" {
				t.Errorf("request body = %v, %v", body, err)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"value":"received"}`))
		case "/error":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"scan in progress"}`))
		case "/plain":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("unavailable"))
		case "/bad-json":
			_, _ = w.Write([]byte("not JSON"))
		}
	}))
	var got struct {
		Value string `json:"value"`
	}
	if err := GetJSON(socket, "/get", &got); err != nil || got.Value != "ok" {
		t.Fatalf("GetJSON = %+v, %v", got, err)
	}
	status, err := PostJSON(socket, "/post", map[string]string{"value": "sent"}, &got)
	if err != nil || status != http.StatusAccepted || got.Value != "received" {
		t.Fatalf("PostJSON = %d, %+v, %v", status, got, err)
	}
	if err := GetJSON(socket, "/error", &got); err == nil || err.Error() != "scan in progress" {
		t.Fatalf("JSON error = %v", err)
	}
	if err := GetJSON(socket, "/plain", &got); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("HTTP error = %v", err)
	}
	if err := GetJSON(socket, "/bad-json", &got); err == nil || !strings.Contains(err.Error(), "decode daemon response") {
		t.Fatalf("decode error = %v", err)
	}
	if status, err := PostJSON(socket, "/bad-json", nil, &got); status != 200 || err == nil {
		t.Fatalf("post decode = %d, %v", status, err)
	}
	if status, err := PostJSON(socket, "/post", make(chan int), nil); status != 0 || err == nil {
		t.Fatalf("encode error = %d, %v", status, err)
	}
	missing := filepath.Join(t.TempDir(), "missing.sock")
	if err := GetJSON(missing, "/get", &got); err == nil {
		t.Fatal("GetJSON accepted missing socket")
	}
}

func TestControlClientRejectsBadPathsAndMissingSockets(t *testing.T) {
	dir := t.TempDir()
	if IsRunning(dir, filepath.Join(dir, "missing.sock")) {
		t.Fatal("directory cannot be a daemon lock")
	}
	if _, err := do(filepath.Join(dir, "missing.sock"), http.MethodGet, "/bad\npath", nil); err == nil {
		t.Fatal("invalid HTTP request path accepted")
	}
	if _, err := PostJSON(filepath.Join(dir, "missing.sock"), "/", nil, nil); err == nil {
		t.Fatal("missing socket accepted")
	}
}

func TestStartAlreadyRunningAndInvalidLog(t *testing.T) {
	lock, socket := testDaemon(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if err := Start(StartOptions{LockPath: lock, SocketPath: socket}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("Start on running daemon = %v", err)
	}
	dir := t.TempDir()
	if err := Start(StartOptions{LockPath: filepath.Join(dir, "lock"), SocketPath: filepath.Join(dir, "socket"), LogPath: dir}); err == nil || !strings.Contains(err.Error(), "open daemon console log") {
		t.Fatalf("Start with directory as log = %v", err)
	}
}

func TestMain(m *testing.M) {
	if os.Getenv("LOOM_TEST_DAEMON_CHILD") == "1" {
		os.Exit(23)
	}
	if os.Getenv("LOOM_TEST_DAEMON_CHILD") == "ready" {
		lock := flock.New(os.Getenv("LOOM_TEST_CHILD_LOCK"))
		if err := lock.Lock(); err != nil {
			os.Exit(2)
		}
		listener, err := net.Listen("unix", os.Getenv("LOOM_TEST_CHILD_SOCKET"))
		if err != nil {
			os.Exit(3)
		}
		stopped := make(chan struct{})
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/_loom/stop" {
				w.WriteHeader(http.StatusAccepted)
				close(stopped)
			}
		})}
		go func() { _ = server.Serve(listener) }()
		<-stopped
		_ = server.Close()
		_ = lock.Unlock()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestStartWaitsForReadyDaemon(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "lock")
	socket := filepath.Join(dir, "socket")
	t.Setenv("LOOM_TEST_DAEMON_CHILD", "ready")
	t.Setenv("LOOM_TEST_CHILD_LOCK", lock)
	t.Setenv("LOOM_TEST_CHILD_SOCKET", socket)
	t.Cleanup(func() {
		if IsRunning(lock, socket) {
			_ = Stop(lock, socket)
		}
	})
	if err := Start(StartOptions{LockPath: lock, SocketPath: socket, LogPath: filepath.Join(dir, "console.log")}); err != nil {
		t.Fatal(err)
	}
	if !IsRunning(lock, socket) {
		t.Fatal("child daemon was not ready")
	}
	if err := Stop(lock, socket); err != nil {
		t.Fatal(err)
	}
}

func TestStartChildExitsDuringStartup(t *testing.T) {
	dir := t.TempDir()
	// The test executable exits via TestMain when launched as the daemon.
	t.Setenv("LOOM_TEST_DAEMON_CHILD", "1")
	err := Start(StartOptions{LockPath: filepath.Join(dir, "lock"), SocketPath: filepath.Join(dir, "socket"), LogPath: filepath.Join(dir, "console.log"), ConfigPath: filepath.Join(dir, "config.toml")})
	if err == nil || !strings.Contains(err.Error(), "exited during startup") {
		t.Fatalf("Start = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "console.log"))
	if err != nil || len(data) != 0 {
		t.Fatalf("child log = %q, %v", data, err)
	}
}

func TestStartLogDirectoryError(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Start(StartOptions{LockPath: filepath.Join(dir, "lock"), SocketPath: filepath.Join(dir, "socket"), LogPath: filepath.Join(file, "log")}); err == nil || !strings.Contains(err.Error(), "create daemon log directory") {
		t.Fatalf("Start error = %v", err)
	}
}
