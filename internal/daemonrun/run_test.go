package daemonrun

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/five82/loom/internal/config"
	"github.com/five82/loom/internal/store"
	"github.com/gofrs/flock"
)

func testRunConfig(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", root)
	path := filepath.Join(root, "bin")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "ffprobe"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", path+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &config.Config{
		Name: "Loom Test", API: config.APIConfig{Bind: "127.0.0.1:0"},
		Paths:   config.PathsConfig{StateDir: filepath.Join(root, "state")},
		Library: config.LibraryConfig{MoviesDir: filepath.Join(root, "movies"), ShortsDir: filepath.Join(root, "shorts"), TVDir: filepath.Join(root, "tv")},
		Scanner: config.ScannerConfig{Interval: "0"}, TMDB: config.TMDBConfig{Language: "en-US"},
	}
}

func TestRunServesLocalControlAndShutsDown(t *testing.T) {
	cfg := testRunConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", cfg.SocketPath())
	}}, Timeout: time.Second}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("daemon exited before ready: %v", err)
		case <-deadline:
			t.Fatal("daemon did not start")
		default:
		}
		response, err := client.Get("http://unix/api/v1/health")
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if response.StatusCode != 200 || !strings.Contains(string(body), `"ok"`) {
				t.Fatalf("health: %d %s", response.StatusCode, body)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	request, err := http.NewRequest(http.MethodPost, "http://unix/_loom/stop", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("stop status = %d", response.StatusCode)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not stop")
	}
	if _, err := os.Stat(cfg.SocketPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket after shutdown: %v", err)
	}
}

func TestRunRejectsLockAndUnavailableDependencies(t *testing.T) {
	t.Run("lock", func(t *testing.T) {
		cfg := testRunConfig(t)
		lock := flock.New(cfg.LockPath())
		held, err := lock.TryLock()
		if err != nil || !held {
			t.Fatalf("lock: %v, %v", held, err)
		}
		defer func() { _ = lock.Unlock() }()
		if err := Run(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "another Loom daemon") {
			t.Fatalf("Run = %v", err)
		}
	})
	t.Run("ffprobe", func(t *testing.T) {
		cfg := testRunConfig(t)
		t.Setenv("PATH", t.TempDir())
		if err := Run(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "ffprobe") {
			t.Fatalf("Run = %v", err)
		}
	})
	t.Run("catalog", func(t *testing.T) {
		cfg := testRunConfig(t)
		if err := cfg.EnsureStateDir(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfg.DBPath(), []byte("not a sqlite database"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := Run(context.Background(), cfg); err == nil {
			t.Fatal("corrupt catalog accepted")
		}
	})
	t.Run("tcp listener", func(t *testing.T) {
		cfg := testRunConfig(t)
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = listener.Close() }()
		cfg.API.Bind = listener.Addr().String()
		if err := Run(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "listen on LAN API") {
			t.Fatalf("Run = %v", err)
		}
	})
	t.Run("pending migration", func(t *testing.T) {
		cfg := testRunConfig(t)
		// A current catalog is accepted and only an explicit migration can upgrade an old one.
		catalog, err := store.Open(cfg.DBPath())
		if err != nil {
			t.Fatal(err)
		}
		_ = catalog.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := Run(ctx, cfg); err != nil {
			t.Fatalf("Run with existing catalog = %v", err)
		}
	})
}
