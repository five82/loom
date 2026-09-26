package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCatalogQueriesOnClosedDatabase(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		run  func() error
	}{
		{"libraries", func() error { _, err := s.Libraries(ctx); return err }},
		{"ensure library", func() error { _, err := s.EnsureLibrary(ctx, "movies", "/movies"); return err }},
		{"start scan", func() error { _, _, err := s.StartScan(ctx, "movies", "/movies"); return err }},
		{"finish scan", func() error { return s.FinishScan(ctx, 1, 1, 1, 1, 0, nil) }},
		{"last scans", func() error { _, err := s.LastScans(ctx); return err }},
		{"upsert item", func() error {
			_, err := s.UpsertItem(ctx, ItemInput{LibraryID: 1, SourceKey: "a", Kind: "movie", Title: "A"})
			return err
		}},
		{"media by path", func() error { _, err := s.MediaByPath(ctx, "/missing"); return err }},
		{"touch media", func() error { return s.TouchMedia(ctx, 1, 1) }},
		{"upsert media", func() error { _, err := s.UpsertMedia(ctx, MediaFile{ItemID: 1, Path: "/movie"}, nil, nil); return err }},
		{"available media", func() error { _, err := s.AvailableMediaCount(ctx, 1); return err }},
		{"search", func() error { _, _, err := s.SearchItems(ctx, "a", 10, 0); return err }},
		{"list", func() error { _, err := s.ListItems(ctx, ListOptions{}); return err }},
		{"item", func() error { _, err := s.Item(ctx, 1); return err }},
		{"media", func() error { _, err := s.Media(ctx, 1); return err }},
		{"streams", func() error { _, err := s.streams(ctx, 1); return err }},
		{"chapters", func() error { _, err := s.chapters(ctx, 1); return err }},
		{"progress", func() error { _, err := s.SetProgress(ctx, 1, 5, 10); return err }},
		{"played", func() error { _, err := s.SetPlayed(ctx, 1); return err }},
		{"clear playback", func() error { _, err := s.ClearPlayback(ctx, 1); return err }},
		{"continue watching", func() error { _, err := s.ContinueWatching(ctx, 10); return err }},
		{"next up", func() error { _, err := s.NextUp(ctx, 10); return err }},
		{"recently added", func() error { _, err := s.RecentlyAdded(ctx, 10); return err }},
		{"recently played", func() error { _, err := s.RecentlyPlayed(ctx, 10); return err }},
		{"stats", func() error { _, err := s.Stats(ctx); return err }},
		{"update metadata", func() error { return s.UpdateMetadata(ctx, 1, MetadataUpdate{TMDBID: 1}) }},
		{"genres", func() error { _, err := s.Genres(ctx); return err }},
		{"seasons", func() error { _, err := s.SeasonsForShow(ctx, 1); return err }},
		{"episodes", func() error { _, err := s.EpisodesForShow(ctx, 1); return err }},
		{"unmatched", func() error { _, err := s.UnmatchedItems(ctx); return err }},
		{"image", func() error { _, err := s.Image(ctx, 1); return err }},
		{"item image", func() error { _, err := s.ItemImage(ctx, 1, "poster"); return err }},
		{"upsert image", func() error { _, err := s.UpsertImage(ctx, Image{ItemID: 1, Kind: "poster"}); return err }},
		{"delete image", func() error { return s.DeleteItemImage(ctx, 1, "poster") }},
		{"featured", func() error { _, err := s.FeaturedPickAt(ctx, time.Now()); return err }},
		{"audit", func() error { _, err := s.Audit(ctx); return err }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil || !strings.Contains(err.Error(), "closed") {
				t.Fatalf("expected closed database error, got %v", err)
			}
		})
	}
}

func TestMetadataUpdateRollsBackOnInvalidRelationships(t *testing.T) {
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
	itemID, err := s.UpsertItem(ctx, ItemInput{LibraryID: libraryID, SourceKey: "movie", Kind: "movie", Title: "Original", ScanID: scanID})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		update MetadataUpdate
	}{
		{"invalid genre", MetadataUpdate{TMDBID: 7, Title: "Changed", Genres: []Genre{{ID: 1, Name: "Drama"}, {ID: 2, Name: ""}}}},
		{"invalid credit", MetadataUpdate{TMDBID: 7, Title: "Changed", Credits: []Credit{{PersonID: 1, Name: "Actor", Role: "actor"}, {PersonID: 2, Name: "Actor", Role: "actor"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Reject the second insert after the first succeeds, to exercise rollback.
			table := "genres"
			if tc.name == "invalid credit" {
				table = "people"
			}
			trigger := "CREATE TRIGGER reject_metadata BEFORE INSERT ON " + table + " WHEN NEW.id = 2 BEGIN SELECT RAISE(FAIL, 'rejected'); END"
			if _, err := s.db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			err := s.UpdateMetadata(ctx, itemID, tc.update)
			if err == nil || !strings.Contains(err.Error(), "rejected") {
				t.Fatalf("update error = %v", err)
			}
			if _, err := s.db.Exec("DROP TRIGGER reject_metadata"); err != nil {
				t.Fatal(err)
			}
			item, err := s.Item(ctx, itemID)
			if err != nil {
				t.Fatal(err)
			}
			if item.Title != "Original" || item.TMDBID != 0 || len(item.Genres) != 0 || len(item.Credits) != 0 {
				t.Fatalf("partial metadata persisted: %+v", item)
			}
			var count int
			if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("partial %s persisted: %d", table, count)
			}
		})
	}
	if err := s.UpdateMetadata(ctx, itemID+1, MetadataUpdate{TMDBID: 7}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing item = %v", err)
	}
}
