package library

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/five82/loom/internal/store"
)

func TestManagerSerializesScansAndReportsStatus(t *testing.T) {
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	if err := os.Mkdir(movies, 0o755); err != nil {
		t.Fatal(err)
	}
	catalog, err := store.Open(filepath.Join(root, "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := NewManager(NewScanner(catalog, &fakeProber{}, nil, movies, "", "", logger), 0, logger)
	finished := make(chan ScanStatus, 2)
	manager.AfterScan(func(context.Context) { finished <- manager.Status() })
	if !manager.Trigger("movies") || manager.Trigger("movies") {
		t.Fatal("queued scan did not block another trigger")
	}
	if status := manager.Status(); !status.Running {
		t.Fatalf("queued status = %+v", status)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { manager.Run(ctx); close(done) }()
	await := func() ScanStatus {
		t.Helper()
		select {
		case status := <-finished:
			return status
		case <-time.After(5 * time.Second):
			t.Fatal("scan did not finish")
			return ScanStatus{}
		}
	}
	status := await()
	if status.Running || status.Library != "" || status.StartedAt != "" || status.LastEndedAt == "" || status.LastError != "" {
		t.Fatalf("finished status = %+v", status)
	}
	if !manager.Trigger("missing") {
		t.Fatal("could not queue second scan")
	}
	status = await()
	if !strings.Contains(status.LastError, `unknown library "missing"`) || status.Running {
		t.Fatalf("failed scan status = %+v", status)
	}
	if !manager.Trigger("movies") {
		t.Fatal("failed scan left manager busy")
	}
	if status = await(); status.LastError != "" {
		t.Fatalf("successful scan did not clear error: %+v", status)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("manager did not stop")
	}
}

func TestManagerScheduledScan(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"movies", "shorts", "tv"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := store.Open(filepath.Join(root, "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := NewManager(NewScanner(catalog, &fakeProber{}, nil, filepath.Join(root, "movies"), filepath.Join(root, "shorts"), filepath.Join(root, "tv"), logger), time.Millisecond, logger)
	finished := make(chan ScanStatus, 1)
	manager.AfterScan(func(context.Context) {
		select {
		case finished <- manager.Status():
		default:
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { manager.Run(ctx); close(done) }()
	select {
	case status := <-finished:
		if status.LastError != "" || status.LastEndedAt == "" {
			t.Fatalf("scheduled status = %+v", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled scan did not finish")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("manager did not stop")
	}
}
