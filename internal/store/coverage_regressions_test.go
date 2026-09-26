package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A broken genre lookup must not turn a populated shelf into a successful,
// genre-less response. Exercise each shelf after its primary query has succeeded.
func TestPopulatedShelvesReportGenreLookupFailure(t *testing.T) {
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
	movie, err := s.UpsertItem(ctx, ItemInput{LibraryID: lib, SourceKey: "movie", Kind: "movie", Title: "Movie", TMDBID: 123, ScanID: scan})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertMedia(ctx, MediaFile{ItemID: movie, Path: "/movies/movie.mkv", Size: 1, DurationMS: 600000, LastSeenScanID: scan}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScan(ctx, lib, scan, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProgress(ctx, movie, 60000, 600000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE items SET release_date = '2020-01-01' WHERE id = ?`, movie); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE item_genres`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"collection by TMDB", func() error { _, err := s.ItemsByTMDBID(ctx, []int64{123}); return err }},
		{"collection by release", func() error { _, err := s.ItemsReleasedBetween(ctx, time.Time{}, time.Now()); return err }},
		{"discovery", func() error { _, _, _, err := s.DiscoveryLibrary(ctx); return err }},
		{"continue watching", func() error { _, err := s.ContinueWatching(ctx, 10); return err }},
		{"recently added", func() error { _, err := s.RecentlyAdded(ctx, 10); return err }},
		{"list items", func() error { _, err := s.ListItems(ctx, ListOptions{}); return err }},
		{"search", func() error { _, _, err := s.SearchItems(ctx, "Movie", 10, 0); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), "item_genres") {
				t.Fatalf("missing genre lookup failure: %v", err)
			}
		})
	}
}

func TestCollectionAndDiscoveryRejectMalformedItems(t *testing.T) {
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
	id, err := s.UpsertItem(ctx, ItemInput{LibraryID: lib, SourceKey: "movie", Kind: "movie", Title: "Movie", TMDBID: 123, ScanID: scan})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScan(ctx, lib, scan, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE items SET year = 'not a year' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"collection", func() error { _, err := s.ItemsByTMDBID(ctx, []int64{123}); return err }},
		{"discovery", func() error { _, _, _, err := s.DiscoveryLibrary(ctx); return err }},
		{"recently added", func() error { _, err := s.RecentlyAdded(ctx, 10); return err }},
		{"unmatched", func() error {
			if _, err := s.db.Exec(`UPDATE items SET tmdb_id = 0 WHERE id = ?`, id); err != nil {
				return err
			}
			_, err := s.UnmatchedItems(ctx)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), "year") {
				t.Fatalf("malformed item = %v", err)
			}
		})
	}
}
