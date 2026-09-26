package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestStatsCountsOnlyAvailableItemsAndMediaByLibrary(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	type entry struct {
		key, kind string
		tmdbID    int64
		media     bool
	}
	for _, library := range []struct {
		kind  string
		items []entry
	}{
		{"movies", []entry{
			{"matched", "movie", 101, true},
			{"unmatched movie", "movie", 0, true},
			{"unmatched file", "unmatched", 0, true},
			{"removed", "movie", 0, true},
		}},
		{"shorts", []entry{{"short", "movie", 102, true}}},
		{"tv", []entry{{"show", "show", 0, false}, {"season", "season", 0, false}, {"episode", "episode", 0, true}}},
	} {
		libraryID, scanID, err := s.StartScan(ctx, library.kind, "/"+library.kind)
		if err != nil {
			t.Fatal(err)
		}
		var showID, seasonID int64
		for _, item := range library.items {
			var parentID *int64
			switch item.kind {
			case "season":
				parentID = &showID
			case "episode":
				parentID = &seasonID
			}
			id, err := s.UpsertItem(ctx, ItemInput{
				LibraryID: libraryID, ParentID: parentID, SourceKey: item.key, Kind: item.kind,
				Title: item.key, TMDBID: item.tmdbID, ScanID: scanID,
			})
			if err != nil {
				t.Fatal(err)
			}
			switch item.kind {
			case "show":
				showID = id
			case "season":
				seasonID = id
			}
			if item.media {
				if _, err := s.UpsertMedia(ctx, MediaFile{ItemID: id, Path: "/" + library.kind + "/" + item.key, LastSeenScanID: scanID}, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := s.FinishScan(ctx, libraryID, scanID, len(library.items), len(library.items), 0, nil); err != nil {
			t.Fatal(err)
		}
		if library.kind == "movies" {
			// A removed item retains its catalog and media rows, but must not count.
			if _, err := s.db.ExecContext(ctx, `UPDATE items SET available = 0 WHERE source_key = 'removed'`); err != nil {
				t.Fatal(err)
			}
		}
	}
	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := Stats{Movies: 2, Shorts: 1, Shows: 1, Episodes: 1, Unmatched: 3, Media: 5}
	if stats != want {
		t.Fatalf("Stats = %+v, want %+v", stats, want)
	}
}
