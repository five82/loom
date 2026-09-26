package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// SQLite's dynamic column typing allows a damaged catalog to contain values
// that cannot be decoded into an Item. Listing endpoints should return an
// error, not a partially populated response or a panic.
func TestCatalogReadsRejectMalformedItem(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	libraryID, scanID, err := s.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.UpsertItem(ctx, ItemInput{LibraryID: libraryID, SourceKey: "Movie", Kind: "movie", Title: "Movie", ScanID: scanID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertMedia(ctx, MediaFile{ItemID: id, Path: "/movies/Movie.mkv", Size: 1, DurationMS: 600_000, LastSeenScanID: scanID}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProgress(ctx, id, 200_000, 600_000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE items SET year = 'not-a-year' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"item", func() error { _, err := s.Item(ctx, id); return err }},
		{"list", func() error { _, err := s.ListItems(ctx, ListOptions{}); return err }},
		{"search", func() error { _, _, err := s.SearchItems(ctx, "Movie", 10, 0); return err }},
		{"recently added", func() error { _, err := s.RecentlyAdded(ctx, 10); return err }},
		{"continue watching", func() error { _, err := s.ContinueWatching(ctx, 10); return err }},
		{"unmatched", func() error { _, err := s.UnmatchedItems(ctx); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil || !strings.Contains(err.Error(), "year") {
				t.Fatalf("malformed year: %v", err)
			}
		})
	}
}
