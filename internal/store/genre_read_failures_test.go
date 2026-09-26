package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogReadsFailWhenGenreRelationsAreMissing(t *testing.T) {
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
	first, err := s.UpsertItem(ctx, ItemInput{LibraryID: libraryID, SourceKey: "First", Kind: "movie", Title: "First", ScanID: scanID})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.UpsertItem(ctx, ItemInput{LibraryID: libraryID, SourceKey: "Second", Kind: "movie", Title: "Second", ScanID: scanID})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{first, second} {
		if _, err := s.UpsertMedia(ctx, MediaFile{ItemID: id, Path: fmt.Sprintf("/movie%d.mkv", id), Size: 1, DurationMS: 600_000, LastSeenScanID: scanID}, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetProgress(ctx, first, 200_000, 600_000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPlayed(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE item_genres`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"item", func() error { _, err := s.Item(ctx, first); return err }},
		{"search", func() error { _, _, err := s.SearchItems(ctx, "First", 10, 0); return err }},
		{"list", func() error { _, err := s.ListItems(ctx, ListOptions{}); return err }},
		{"continue watching", func() error { _, err := s.ContinueWatching(ctx, 10); return err }},
		{"recently added", func() error { _, err := s.RecentlyAdded(ctx, 10); return err }},
		{"recently played", func() error { _, err := s.RecentlyPlayed(ctx, 10); return err }},
		{"discovery", func() error { _, _, _, err := s.DiscoveryLibrary(ctx); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil || !strings.Contains(err.Error(), "item_genres") {
				t.Fatalf("missing genre relation: %v", err)
			}
		})
	}
}

func TestItemRejectsMissingCreditsAndMediaRelations(t *testing.T) {
	for _, table := range []string{"item_credits", "media_streams", "media_chapters"} {
		t.Run(table, func(t *testing.T) {
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
			if _, err := s.UpsertMedia(ctx, MediaFile{ItemID: id, Path: "/movie.mkv", Size: 1, LastSeenScanID: scanID}, nil, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`DROP TABLE ` + table); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Item(ctx, id); err == nil || !strings.Contains(err.Error(), table) {
				t.Fatalf("broken relation %s: %v", table, err)
			}
		})
	}
}
