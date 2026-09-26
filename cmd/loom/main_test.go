package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"github.com/five82/loom/internal/daemonctl"
)

func testCLI(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	configFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configFile, []byte(fmt.Sprintf("[paths]\nstate_dir = %q\n", filepath.Join(dir, "state"))), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, configFile
}

func runCLI(t *testing.T, configFile string, args ...string) (string, error) {
	t.Helper()
	command := newRootCommand()
	command.SetArgs(append([]string{"--config", configFile}, args...))
	// CLI commands write directly to stdout rather than Cobra's output writer.
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = original }()
	err = command.ExecuteContext(context.Background())
	_ = writer.Close()
	output, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(output), err
}

func testCLIDaemon(t *testing.T, dir string, handler http.Handler) {
	t.Helper()
	lock := flock.New(filepath.Join(dir, "loom.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Unlock() })
	listener, err := net.Listen("unix", filepath.Join(dir, "loom.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
}

func TestCLIWithoutDaemon(t *testing.T) {
	_, configFile := testCLI(t)
	for _, tc := range []struct {
		args []string
		want string
		err  error
	}{
		{[]string{"status"}, "Daemon stopped", nil},
		{[]string{"status", "--json"}, `{"running":false}`, nil},
		{[]string{"scan", "movies"}, "", daemonctl.ErrNotRunning},
		{[]string{"unmatched"}, "", daemonctl.ErrNotRunning},
		{[]string{"search", "movie", "Title"}, "", daemonctl.ErrNotRunning},
		{[]string{"match", "1", "2"}, "", daemonctl.ErrNotRunning},
		{[]string{"config", "validate"}, "Configuration valid:", nil},
	} {
		output, err := runCLI(t, configFile, tc.args...)
		if tc.err != nil {
			if !errors.Is(err, tc.err) {
				t.Errorf("%v: error = %v, want %v", tc.args, err, tc.err)
			}
		} else if err != nil || !strings.Contains(output, tc.want) {
			t.Errorf("%v: output = %q, error = %v", tc.args, output, err)
		}
	}
}

func TestCLICommandValidation(t *testing.T) {
	_, configFile := testCLI(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"scan", "music"}, "library must be"},
		{[]string{"search", "book", "title"}, "type must be"},
		{[]string{"search", "movie", "title", "--year", "1799"}, "year must be"},
		{[]string{"match", "zero", "2"}, "item-id must be"},
		{[]string{"match", "1", "0"}, "tmdb-id must be"},
		{[]string{"logs", "--lines", "-1"}, "lines must be"},
	} {
		_, err := runCLI(t, configFile, tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: error = %v, want %q", tc.args, err, tc.want)
		}
	}
}

func TestCLIWithDaemon(t *testing.T) {
	dir, configFile := testCLI(t)
	var requests []string
	var requestsMu sync.Mutex
	testCLIDaemon(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestsMu.Lock()
		requests = append(requests, r.Method+" "+r.URL.String())
		requestsMu.Unlock()
		switch r.URL.Path {
		case "/_loom/status":
			_, _ = w.Write([]byte(`{"running":true,"pid":123,"listeners":{"api":["127.0.0.1:8097"],"control":"local"},"scan":{"last_error":"failed"},"catalog":{"movies":2},"last_scans":[{"library":"movies","finished_at":"2025-01-02T03:04:05Z","status":"failed","error":"oops"}]}`))
		case "/api/v1/scan":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["library"] == "tv" {
				w.WriteHeader(http.StatusConflict)
			} else {
				w.WriteHeader(http.StatusAccepted)
			}
			_, _ = w.Write([]byte(`{}`))
		case "/_loom/unmatched":
			_, _ = w.Write([]byte(`{"items":[{"id":7,"kind":"movie","title":"Missing","year":2020}]}`))
		case "/_loom/metadata/search":
			_, _ = w.Write([]byte(`{"items":[{"id":42,"title":"Example","year":2020}]}`))
		case "/_loom/metadata/match":
			var body map[string]int64
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["item_id"] == 7 && body["tmdb_id"] == 42 {
				_, _ = w.Write([]byte(`{}`))
			} else {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"not matchable"}`))
			}
		default:
			t.Errorf("unexpected request: %s", r.URL)
		}
	}))
	for _, tc := range []struct {
		args []string
		want string
		err  string
	}{
		{[]string{"status"}, "Daemon running (PID 123)", ""},
		{[]string{"status", "--json"}, `"running": true`, ""},
		{[]string{"scan"}, "Scan started: all libraries", ""},
		{[]string{"scan", "movies"}, "Scan started: movies", ""},
		{[]string{"scan", "tv"}, "", "already running"},
		{[]string{"unmatched"}, "7\tmovie\tMissing (2020)", ""},
		{[]string{"unmatched", "--json"}, `"title": "Missing"`, ""},
		{[]string{"search", "movie", "Example", "Film", "--year", "2020"}, "42\tExample (2020)", ""},
		{[]string{"search", "tv", "Example", "--json"}, `"title": "Example"`, ""},
		{[]string{"match", "7", "42"}, "Matched item 7", ""},
		{[]string{"match", "8", "42"}, "", "not matchable"},
	} {
		output, err := runCLI(t, configFile, tc.args...)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%v: error = %v, want %q", tc.args, err, tc.err)
			}
		} else if err != nil || !strings.Contains(output, tc.want) {
			t.Errorf("%v: output = %q, error = %v, want %q", tc.args, output, err, tc.want)
		}
	}
	requestsMu.Lock()
	defer requestsMu.Unlock()
	for _, want := range []string{"GET /_loom/status", "POST /api/v1/scan", "GET /_loom/metadata/search?query=Example+Film&type=movie&year=2020"} {
		if !strings.Contains(strings.Join(requests, "\n"), want) {
			t.Errorf("missing request %q from %v", want, requests)
		}
	}
}

func TestCLIConfigAndLogs(t *testing.T) {
	dir, configFile := testCLI(t)
	output, err := runCLI(t, configFile, "config", "init")
	if err == nil || !strings.Contains(err.Error(), "file exists") {
		t.Fatalf("config init over existing file = %q, %v", output, err)
	}
	sample := filepath.Join(dir, "new", "config.toml")
	output, err = runCLI(t, sample, "config", "init")
	if err != nil || !strings.Contains(output, sample) {
		t.Fatalf("config init = %q, %v", output, err)
	}
	output, err = runCLI(t, sample, "config", "validate")
	if err != nil || !strings.Contains(output, "Configuration valid:") {
		t.Fatalf("config validate = %q, %v", output, err)
	}
	logPath := filepath.Join(dir, "state", "daemon.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("first\nsecond\nthird\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err = runCLI(t, configFile, "logs", "-n", "2")
	if err != nil || output != "second\nthird\n" {
		t.Fatalf("logs = %q, %v", output, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := displayLogs(ctx, logPath, 0, true); err != nil {
		t.Fatal(err)
	}
	if got := localTime("invalid"); got != "invalid" {
		t.Fatalf("localTime(invalid) = %q", got)
	}
	if got := localTime(time.Now().UTC().Format(time.RFC3339)); len(got) != len("2006-01-02 15:04") {
		t.Fatalf("localTime = %q", got)
	}
}
