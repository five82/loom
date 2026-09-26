package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestTVHierarchyQueriesRejectCorruptItem(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	libraryID, scanID, err := s.StartScan(ctx, "tv", "/tv")
	if err != nil {
		t.Fatal(err)
	}
	showID, err := s.UpsertItem(ctx, ItemInput{LibraryID: libraryID, SourceKey: "show", Kind: "show", Title: "Show", ScanID: scanID})
	if err != nil {
		t.Fatal(err)
	}
	seasonID, err := s.UpsertItem(ctx, ItemInput{LibraryID: libraryID, ParentID: &showID, SourceKey: "season", Kind: "season", Title: "Season", SeasonNumber: 1, ScanID: scanID})
	if err != nil {
		t.Fatal(err)
	}
	episodeID, err := s.UpsertItem(ctx, ItemInput{LibraryID: libraryID, ParentID: &seasonID, SourceKey: "episode", Kind: "episode", Title: "Episode", SeasonNumber: 1, EpisodeNumber: 1, ScanID: scanID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE items SET year = 'invalid' WHERE id = ?`, seasonID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SeasonsForShow(ctx, showID); err == nil || !strings.Contains(err.Error(), "year") {
		t.Fatalf("season = %v", err)
	}
	if _, err := s.db.Exec(`UPDATE items SET year = 0 WHERE id = ?`, seasonID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE items SET year = 'invalid' WHERE id = ?`, episodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EpisodesForShow(ctx, showID); err == nil || !strings.Contains(err.Error(), "year") {
		t.Fatalf("episodes = %v", err)
	}
}
