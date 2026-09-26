package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestFeaturedEmptyAndRemovedLastEligibleMovie(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2025, 3, 9, 6, 0, 0, 0, time.UTC)
	if _, err := s.FeaturedPickAt(ctx, at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no movies: %v", err)
	}
	libraryID, scanID, err := s.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	id := addFeaturedTestMovie(t, s, libraryID, scanID, "Only", 8, nil)
	if err := s.FinishScan(ctx, libraryID, scanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	first, err := s.FeaturedPickAt(ctx, at)
	if err != nil || first.Item.ID != id {
		t.Fatalf("pick = %+v, %v", first, err)
	}
	_, scanID, err = s.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScan(ctx, libraryID, scanID, 0, 0, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FeaturedPickAt(ctx, at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed last pick: %v", err)
	}
	var picks, rotation int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM featured_pick`).Scan(&picks); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM featured_rotation`).Scan(&rotation); err != nil {
		t.Fatal(err)
	}
	if picks != 0 || rotation != 0 {
		t.Fatalf("stale pick/rotation: %d/%d", picks, rotation)
	}
}

func TestFeaturedOnlyResumableMovieIsStillPicked(t *testing.T) {
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
	id := addFeaturedTestMovie(t, s, libraryID, scanID, "Only", 8, nil)
	if _, err := s.UpsertMedia(ctx, MediaFile{ItemID: id, Path: "/movies/Only.mkv", Size: 1, DurationMS: 600_000, LastSeenScanID: scanID}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScan(ctx, libraryID, scanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProgress(ctx, id, 200_000, 600_000); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{time.Date(2025, 8, 12, 6, 0, 0, 0, time.UTC), time.Date(2025, 8, 12, 18, 0, 0, 0, time.UTC)} {
		pick, err := s.FeaturedPickAt(ctx, at)
		if err != nil || pick.Item.ID != id {
			t.Fatalf("pick = %+v, %v", pick, err)
		}
	}
}
