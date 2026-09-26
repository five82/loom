package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestMetadataUpdateRollsBackWhenOldRelationsCannotBeCleared(t *testing.T) {
	for _, tc := range []struct {
		table, want string
		update      MetadataUpdate
	}{
		{"item_genres", "clear item genres", MetadataUpdate{TMDBID: 2, Genres: []Genre{{ID: 1, Name: "Drama"}}}},
		{"item_credits", "clear item credits", MetadataUpdate{TMDBID: 2, Credits: []Credit{{PersonID: 1, Name: "Actor", Role: "actor"}}}},
	} {
		t.Run(tc.table, func(t *testing.T) {
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
			id, err := s.UpsertItem(ctx, ItemInput{LibraryID: libraryID, SourceKey: "movie", Kind: "movie", Title: "Movie", ScanID: scanID})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.UpdateMetadata(ctx, id, MetadataUpdate{TMDBID: 1, Genres: []Genre{{ID: 1, Name: "Drama"}}, Credits: []Credit{{PersonID: 1, Name: "Actor", Role: "actor"}}}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`CREATE TRIGGER reject_clear BEFORE DELETE ON ` + tc.table + ` BEGIN SELECT RAISE(FAIL, 'clear refused'); END`); err != nil {
				t.Fatal(err)
			}
			if err := s.UpdateMetadata(ctx, id, tc.update); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("update error = %v", err)
			}
			if _, err := s.db.Exec(`DROP TRIGGER reject_clear`); err != nil {
				t.Fatal(err)
			}
			item, err := s.Item(ctx, id)
			if err != nil || item.TMDBID != 1 || len(item.Genres) != 1 || len(item.Credits) != 1 {
				t.Fatalf("metadata after rollback = %+v, %v", item, err)
			}
		})
	}
}

func TestImageDeleteAndStatsReportBrokenCatalog(t *testing.T) {
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
	id, err := s.UpsertItem(ctx, ItemInput{LibraryID: libraryID, SourceKey: "movie", Kind: "movie", Title: "Movie", ScanID: scanID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertImage(ctx, Image{ItemID: id, Kind: "poster", Path: "/poster.jpg"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_image BEFORE DELETE ON images BEGIN SELECT RAISE(FAIL, 'delete refused'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteItemImage(ctx, id, "poster"); err == nil || !strings.Contains(err.Error(), "delete poster image") {
		t.Fatalf("delete image = %v", err)
	}
	if _, err := s.db.Exec(`DROP TABLE media_files`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stats(ctx); err == nil || !strings.Contains(err.Error(), "media_files") {
		t.Fatalf("stats = %v", err)
	}
}
