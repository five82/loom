package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogListsRejectMalformedScanAndLibraryRows(t *testing.T) {
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
	if err := s.FinishScan(ctx, libraryID, scanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE scan_runs SET discovered_files = 'invalid' WHERE id = ?`, scanID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LastScans(ctx); err == nil || !strings.Contains(err.Error(), "discovered_files") {
		t.Fatalf("invalid scan counters = %v", err)
	}
	if _, err := s.db.Exec(`CREATE TEMP TABLE libraries (id TEXT, kind TEXT, name TEXT, path TEXT, last_scan_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO libraries VALUES ('invalid', 'movies', 'Movies', '/movies', '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Libraries(ctx); err == nil {
		t.Fatal("invalid library id accepted")
	}
}

func TestRecentlyPlayedRejectsMalformedItemAndTouchReportsMissingTable(t *testing.T) {
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
	if _, err := s.UpsertMedia(ctx, MediaFile{ItemID: id, Path: "/movie.mkv", Size: 1, DurationMS: 600_000, LastSeenScanID: scanID}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPlayed(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE items SET year = 'invalid' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecentlyPlayed(ctx, 5); err == nil || !strings.Contains(err.Error(), "year") {
		t.Fatalf("recently played with invalid year = %v", err)
	}
	if _, err := s.db.Exec(`DROP TABLE media_files`); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchMedia(ctx, id, scanID); err == nil || !strings.Contains(err.Error(), "media_files") {
		t.Fatalf("touch missing media table = %v", err)
	}
}
