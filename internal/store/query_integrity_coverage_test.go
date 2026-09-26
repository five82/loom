package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogQueriesRejectMalformedFields(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	lib, scan, err := s.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.UpsertItem(ctx, ItemInput{LibraryID: lib, SourceKey: "movie", Kind: "movie", Title: "Movie", ScanID: scan})
	if err != nil {
		t.Fatal(err)
	}
	media, err := s.UpsertMedia(ctx, MediaFile{ItemID: id, Path: "/movies/movie.mkv", Size: 1, DurationMS: 600000, LastSeenScanID: scan}, []Stream{{Index: 0, Kind: "video", Width: 1920}}, []Chapter{{Index: 0, StartMS: 0}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScan(ctx, lib, scan, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, corrupt, restore string
		run                    func() error
	}{
		{"search item", `UPDATE items SET year = 'bad'`, `UPDATE items SET year = 0`, func() error { _, _, err := s.SearchItems(ctx, "Movie", 10, 0); return err }},
		{"list item", `UPDATE items SET year = 'bad'`, `UPDATE items SET year = 0`, func() error { _, err := s.ListItems(ctx, ListOptions{}); return err }},
		{"single item", `UPDATE items SET year = 'bad'`, `UPDATE items SET year = 0`, func() error { _, err := s.Item(ctx, id); return err }},
		{"media lookup", `UPDATE media_files SET duration_ms = 'bad'`, `UPDATE media_files SET duration_ms = 600000`, func() error { _, err := s.Media(ctx, media); return err }},
		{"item media", `UPDATE media_files SET duration_ms = 'bad'`, `UPDATE media_files SET duration_ms = 600000`, func() error { _, err := s.mediaForItem(ctx, id); return err }},
		{"streams", `UPDATE media_streams SET width = 'bad'`, `UPDATE media_streams SET width = 1920`, func() error { _, err := s.streams(ctx, media); return err }},
		{"chapters", `UPDATE media_chapters SET start_ms = 'bad'`, `UPDATE media_chapters SET start_ms = 0`, func() error { _, err := s.chapters(ctx, media); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.db.Exec(tc.corrupt); err != nil {
				t.Fatal(err)
			}
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), "bad") {
				t.Fatalf("malformed row = %v", err)
			}
			if _, err := s.db.Exec(tc.restore); err != nil {
				t.Fatal(err)
			}
		})
	}
}
