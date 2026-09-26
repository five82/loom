package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSetProgressValidationAndDurationFallback(t *testing.T) {
	ctx := context.Background()
	catalog, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	libraryID, scanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := catalog.UpsertItem(ctx, ItemInput{LibraryID: libraryID, ScanID: scanID, SourceKey: "Movie", Kind: "movie", Title: "Movie"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, position, duration int64
		want                   string
		notFound               bool
	}{
		{itemID, -1, 10, "non-negative", false},
		{itemID, 1, -1, "non-negative", false},
		{itemID + 999, 1, 100, "", true},
		{itemID, 1, 0, "", true},
	} {
		_, err := catalog.SetProgress(ctx, tc.id, tc.position, tc.duration)
		if tc.notFound {
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("SetProgress(%+v): %v", tc, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("SetProgress(%+v): %v", tc, err)
		}
	}
	_, err = catalog.UpsertMedia(ctx, MediaFile{ItemID: itemID, Path: "/movies/Movie.mkv", Size: 10, MTimeNS: time.Now().UnixNano(), LastSeenScanID: scanID}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.SetProgress(ctx, itemID, 1, 0); err == nil || !strings.Contains(err.Error(), "duration must be positive") {
		t.Fatalf("zero duration: %v", err)
	}
	result, err := catalog.SetProgress(ctx, itemID, 500_000, 100_000)
	if err != nil || !result.Played || result.PositionMS != 100_000 {
		t.Fatalf("clamped progress: %+v, %v", result, err)
	}
	result, err = catalog.SetProgress(ctx, itemID, 1, 100_000)
	if err != nil || result.Played || result.ResumePositionMS != 0 {
		t.Fatalf("early progress: %+v, %v", result, err)
	}
}

func TestNextFeaturedPickTimeAtBoundaries(t *testing.T) {
	zone := time.FixedZone("test", 2*60*60)
	for _, tc := range []struct{ hour, wantHour, wantDay int }{
		{5, 6, 1}, {6, 18, 1}, {17, 18, 1}, {18, 6, 2},
	} {
		at := time.Date(2025, 1, 1, tc.hour, 0, 0, 0, zone)
		next := NextFeaturedPickTime(at)
		if next.Hour() != tc.wantHour || next.Day() != tc.wantDay || next.Location() != zone {
			t.Errorf("after %s: %s", at, next)
		}
	}
}
