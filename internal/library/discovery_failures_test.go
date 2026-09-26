package library

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/five82/loom/internal/store"
)

type badEntry struct{}

func (badEntry) Name() string               { return "broken.mkv" }
func (badEntry) IsDir() bool                { return false }
func (badEntry) Type() fs.FileMode          { return 0 }
func (badEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrPermission }

func TestVideoDiscoveryHandlesStatFailureAndTimestampTies(t *testing.T) {
	if _, err := newVideoFile("/broken.mkv", badEntry{}); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("bad stat = %v", err)
	}
	at := time.Now()
	files := []videoFile{{path: "z.mkv", modTime: at}, {path: "a.mkv", modTime: at}, {path: "new.mkv", modTime: at.Add(time.Second)}}
	sortNewestFirst(files)
	if files[0].path != "new.mkv" || files[1].path != "a.mkv" || files[2].path != "z.mkv" {
		t.Fatalf("file sort = %+v", files)
	}
	if seasonTitle(0) != "Specials" {
		t.Fatal("season zero has no title")
	}
}

func TestScannerReportsUnreadableMovieDirectoryAndTVWalk(t *testing.T) {
	for _, kind := range []string{"movies", "tv"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			dir := filepath.Join(root, "Show")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(dir); err != nil {
				t.Fatal(err)
			}
			catalog, err := store.Open(filepath.Join(t.TempDir(), "loom.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = catalog.Close() }()
			scanner := NewScanner(catalog, &fakeProber{}, nil, root, root, root, slog.Default())
			libraryID, scanID, err := catalog.StartScan(ctx, kind, root)
			if err != nil {
				t.Fatal(err)
			}
			counters := &scanCounters{}
			if kind == "movies" {
				err = scanner.scanMovies(ctx, libraryID, scanID, root, entries, counters)
			} else {
				err = scanner.scanTV(ctx, libraryID, scanID, root, entries, counters)
			}
			if err == nil || !strings.Contains(err.Error(), "directory") {
				t.Fatalf("missing directory = %v", err)
			}
		})
	}
}
