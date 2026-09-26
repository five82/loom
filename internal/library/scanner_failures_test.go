package library

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/five82/loom/internal/store"
)

func TestScannerMissingRootsAndNoVideoDirectories(t *testing.T) {
	root := t.TempDir()
	catalog, err := store.Open(filepath.Join(root, "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	scanner := NewScanner(catalog, &fakeProber{}, nil, filepath.Join(root, "missing"), filepath.Join(root, "shorts"), filepath.Join(root, "tv"), slog.Default())
	if err := scanner.Scan(context.Background(), "movies"); err == nil || !strings.Contains(err.Error(), "read movies library root") {
		t.Fatalf("missing root = %v", err)
	}
	for _, kind := range []string{"movies", "shorts", "tv"} {
		path := filepath.Join(root, kind)
		if err := os.MkdirAll(filepath.Join(path, ".hidden"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(path, "Empty"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	scanner.movies = filepath.Join(root, "movies")
	if err := scanner.Scan(context.Background(), ""); err != nil {
		t.Fatalf("empty directories: %v", err)
	}
	stats, err := catalog.Stats(context.Background())
	if err != nil || stats.Media != 0 {
		t.Fatalf("empty directory media: %+v, %v", stats, err)
	}
}

func TestScannerCancelledDuringMovieAndTVWalks(t *testing.T) {
	root := t.TempDir()
	catalog, err := store.Open(filepath.Join(root, "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	scanner := NewScanner(catalog, &fakeProber{}, nil, root, root, root, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, kind := range []string{"movies", "tv", ""} {
		if err := scanner.Scan(ctx, kind); !errors.Is(err, context.Canceled) {
			t.Fatalf("scan %q: %v", kind, err)
		}
	}
}

func TestScanMediaRejectsDeletedFileAndClosedCatalog(t *testing.T) {
	root := t.TempDir()
	catalog, err := store.Open(filepath.Join(root, "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	scanner := NewScanner(catalog, &fakeProber{}, nil, root, root, root, slog.Default())
	if err := scanner.scanMedia(context.Background(), 1, 1, filepath.Join(root, "missing.mkv"), &scanCounters{}); err == nil || !strings.Contains(err.Error(), "stat media") {
		t.Fatalf("missing file: %v", err)
	}
	path := filepath.Join(root, "file.mkv")
	writeTestFile(t, path)
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	if err := scanner.scanMedia(context.Background(), 1, 1, path, &scanCounters{}); err == nil {
		t.Fatal("closed catalog accepted scan")
	}
}
