package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFeaturedRotationFailureKeepsLastGoodScan(t *testing.T) {
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
	id := addFeaturedTestMovie(t, s, libraryID, scanID, "Movie", 8, nil)
	if err := s.FinishScan(ctx, libraryID, scanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_removal BEFORE DELETE ON featured_rotation BEGIN SELECT RAISE(FAIL, 'rotation is locked'); END`); err != nil {
		t.Fatal(err)
	}
	_, nextScan, err := s.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScan(ctx, libraryID, nextScan, 0, 0, 0, nil); err == nil || !strings.Contains(err.Error(), "remove ineligible featured movies") {
		t.Fatalf("failed removal = %v", err)
	}
	item, err := s.Item(ctx, id)
	if err != nil || item.ID != id {
		t.Fatalf("failed scan changed availability: %+v, %v", item, err)
	}
}

func TestFeaturedCycleRestartFailurePreservesPreviousPick(t *testing.T) {
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
	addFeaturedTestMovie(t, s, libraryID, scanID, "Only", 8, nil)
	if err := s.FinishScan(ctx, libraryID, scanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2025, 5, 10, 6, 0, 0, 0, time.UTC)
	first, err := s.FeaturedPickAt(ctx, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_restart BEFORE UPDATE ON featured_rotation BEGIN SELECT RAISE(FAIL, 'restart refused'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FeaturedPickAt(ctx, at.Add(12*time.Hour)); err == nil || !strings.Contains(err.Error(), "restart featured rotation") {
		t.Fatalf("rotation restart = %v", err)
	}
	again, err := s.FeaturedPickAt(ctx, at)
	if err != nil || again.Item.ID != first.Item.ID {
		t.Fatalf("current period changed: %+v, %v", again, err)
	}
}
