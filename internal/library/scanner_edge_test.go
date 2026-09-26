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

type failingMatcher struct{ calls int }

func (m *failingMatcher) AutoMatch(context.Context, int64) error {
	m.calls++
	return errors.New("provider unavailable")
}

type failingProber struct{ cancel context.CancelFunc }

func (p failingProber) Probe(context.Context, string) (ProbeResult, error) {
	if p.cancel != nil {
		p.cancel()
	}
	return ProbeResult{}, errors.New("bad video")
}

func TestScannerProbeFailureIsRecordedAndRetried(t *testing.T) {
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	path := filepath.Join(movies, "Movie", "Movie.mkv")
	writeTestFile(t, path)
	catalog, err := store.Open(filepath.Join(root, "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	scanner := NewScanner(catalog, failingProber{}, nil, movies, "", "", slog.Default())
	ctx := context.Background()
	for attempt := 0; attempt < 2; attempt++ {
		if err := scanner.Scan(ctx, "movies"); err != nil {
			t.Fatal(err)
		}
		media, err := catalog.MediaByPath(ctx, path)
		if err != nil || media.ProbeError != "bad video" {
			t.Fatalf("attempt %d: media = %+v, %v", attempt, media, err)
		}
	}
	scans, err := catalog.LastScans(ctx)
	if err != nil || len(scans) != 1 || scans[0].ProbeErrors != 1 {
		t.Fatalf("scans = %+v, %v", scans, err)
	}
}

func TestScannerPausesMetadataAfterOneProviderFailure(t *testing.T) {
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	for _, name := range []string{"One", "Two"} {
		writeTestFile(t, filepath.Join(movies, name, name+".mkv"))
	}
	catalog, err := store.Open(filepath.Join(root, "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	matcher := &failingMatcher{}
	scanner := NewScanner(catalog, &fakeProber{}, matcher, movies, "", "", slog.Default())
	if err := scanner.Scan(context.Background(), "movies"); err != nil {
		t.Fatal(err)
	}
	if matcher.calls != 1 {
		t.Fatalf("provider called %d times, want once", matcher.calls)
	}
	if err := scanner.Scan(context.Background(), "movies"); err != nil {
		t.Fatal(err)
	}
	if matcher.calls != 2 {
		t.Fatalf("next scan did not retry provider: %d", matcher.calls)
	}
}

func TestScannerEmptyRootAndCancelledProbe(t *testing.T) {
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	writeTestFile(t, filepath.Join(movies, "Movie", "Movie.mkv"))
	catalog, err := store.Open(filepath.Join(root, "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	scanner := NewScanner(catalog, &fakeProber{}, nil, movies, "", "", slog.Default())
	if err := scanner.Scan(context.Background(), "movies"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(movies, "Movie")); err != nil {
		t.Fatal(err)
	}
	if err := scanner.Scan(context.Background(), "movies"); err == nil || !strings.Contains(err.Error(), "refusing to reconcile") {
		t.Fatalf("empty mount error = %v", err)
	}
	if err := scanner.Scan(context.Background(), "invalid"); err == nil || !strings.Contains(err.Error(), "unknown library") {
		t.Fatalf("unknown library error = %v", err)
	}
	writeTestFile(t, filepath.Join(movies, "New", "New.mkv"))
	ctx, cancel := context.WithCancel(context.Background())
	scanner.prober = failingProber{cancel: cancel}
	if err := scanner.Scan(ctx, "movies"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled probe error = %v", err)
	}
}

func TestScannerCancelledBetweenLibraries(t *testing.T) {
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	writeTestFile(t, filepath.Join(movies, "Movie", "Movie.mkv"))
	catalog, err := store.Open(filepath.Join(root, "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	scanner := NewScanner(catalog, failingProber{cancel: cancel}, nil, movies, "", "", slog.Default())
	if err := scanner.Scan(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan = %v", err)
	}
}
