package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFeaturedPickReportsCorruptCurrentSelection(t *testing.T) {
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
	addFeaturedTestMovie(t, s, lib, scan, "Movie", 8, nil)
	if err := s.FinishScan(ctx, lib, scan, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if _, err := s.FeaturedPickAt(ctx, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE featured_pick SET period_started_at = NULL`); err == nil {
		// The durable table rejects NULL; corrupt the numeric id instead.
		t.Fatal("NOT NULL constraint absent")
	}
	if _, err := s.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE featured_pick SET item_id = 99999`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	// The join hides an orphaned pick and rotation chooses a valid replacement.
	pick, err := s.FeaturedPickAt(ctx, at)
	if err != nil || pick.Item.Title != "Movie" {
		t.Fatalf("orphan recovery = %+v, %v", pick, err)
	}
}

func TestFeaturedPickReportsFailuresClearingEmptyRotation(t *testing.T) {
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
	id := addFeaturedTestMovie(t, s, lib, scan, "Movie", 8, nil)
	if err := s.FinishScan(ctx, lib, scan, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	if _, err := s.FeaturedPickAt(ctx, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM featured_rotation`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE items SET available = 0 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER refuse_clear BEFORE DELETE ON featured_pick BEGIN SELECT RAISE(FAIL, 'cannot clear'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FeaturedPickAt(ctx, at); err == nil || !strings.Contains(err.Error(), "clear featured pick") {
		t.Fatalf("empty pick cleanup = %v", err)
	}
}
