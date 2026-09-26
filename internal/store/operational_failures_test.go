package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogOpenAndScanReportInvalidPathsAndIDs(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(file, "loom.db")); err == nil {
		t.Fatal("opened a database inside a file")
	}
	s, err := Open(filepath.Join(root, "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	if _, _, err := s.StartScan(ctx, "invalid", "/invalid"); err == nil {
		t.Fatal("invalid library kind accepted")
	}
	libraryID, scanID, err := s.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScan(ctx, libraryID+100, scanID, 0, 0, 0, nil); err == nil || !strings.Contains(err.Error(), "read scanned library kind") {
		t.Fatalf("unknown library = %v", err)
	}
	if _, err := s.UpsertItem(ctx, ItemInput{LibraryID: libraryID + 100, SourceKey: "movie", Kind: "movie", Title: "Movie", ScanID: scanID}); err == nil {
		t.Fatal("invalid library reference accepted")
	}
}

func TestPlaybackWritesReportRejectedStatements(t *testing.T) {
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
	if _, err := s.db.Exec(`CREATE TRIGGER reject_played BEFORE INSERT ON playback_state BEGIN SELECT RAISE(FAIL, 'played refused'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPlayed(ctx, id); err == nil || !strings.Contains(err.Error(), "mark played") {
		t.Fatalf("mark played = %v", err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_played`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPlayed(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_clear BEFORE DELETE ON playback_state BEGIN SELECT RAISE(FAIL, 'clear refused'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClearPlayback(ctx, id); err == nil || !strings.Contains(err.Error(), "clear playback state") {
		t.Fatalf("clear playback = %v", err)
	}
	progress, err := s.progress(ctx, id)
	if err != nil || !progress.Played {
		t.Fatalf("playback lost after rejected delete: %+v, %v", progress, err)
	}
}
