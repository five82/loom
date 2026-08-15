package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func addFeaturedTestMovie(
	t *testing.T, catalog *Store, libraryID, scanID int64, title string, rating float64, genres []Genre,
) int64 {
	t.Helper()
	itemID, err := catalog.UpsertItem(context.Background(), ItemInput{
		LibraryID: libraryID, SourceKey: title, Kind: "movie", Title: title, ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.UpdateMetadata(context.Background(), itemID, MetadataUpdate{
		TMDBID: itemID, Title: title, VoteAverage: rating, Genres: genres,
	}); err != nil {
		t.Fatal(err)
	}
	return itemID
}

func TestFeaturedPickRotatesAtSixWithoutRepeatingCycle(t *testing.T) {
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
	for _, title := range []string{"Arrival", "Alien", "The Matrix"} {
		addFeaturedTestMovie(t, catalog, libraryID, scanID, title, FeaturedRatingThreshold, []Genre{{ID: 18, Name: "Drama"}})
	}
	addFeaturedTestMovie(t, catalog, libraryID, scanID, "Low Rated", FeaturedRatingThreshold-0.1, []Genre{{ID: 18, Name: "Drama"}})
	addFeaturedTestMovie(t, catalog, libraryID, scanID, "Documentary", 9.0, []Genre{{ID: 99, Name: "Documentary"}})
	if err := catalog.FinishScan(ctx, libraryID, scanID, 5, 5, 0, nil); err != nil {
		t.Fatal(err)
	}
	var rotationCount int
	if err := catalog.db.QueryRow(`SELECT COUNT(*) FROM featured_rotation`).Scan(&rotationCount); err != nil {
		t.Fatal(err)
	}
	if rotationCount != 3 {
		t.Fatalf("rotation count = %d, want 3 eligible non-documentaries", rotationCount)
	}

	location := time.FixedZone("server", -7*60*60)
	beforeSix := time.Date(2025, 8, 12, 5, 59, 0, 0, location)
	first, err := catalog.FeaturedPickAt(ctx, beforeSix)
	if err != nil {
		t.Fatal(err)
	}
	again, err := catalog.FeaturedPickAt(ctx, beforeSix.Add(time.Minute-1))
	if err != nil {
		t.Fatal(err)
	}
	if again.Item.ID != first.Item.ID {
		t.Fatalf("pick changed within a period: %d then %d", first.Item.ID, again.Item.ID)
	}

	second, err := catalog.FeaturedPickAt(ctx, time.Date(2025, 8, 12, 6, 0, 0, 0, location))
	if err != nil {
		t.Fatal(err)
	}
	secondAgain, err := catalog.FeaturedPickAt(ctx, time.Date(2025, 8, 12, 17, 59, 59, 0, location))
	if err != nil {
		t.Fatal(err)
	}
	third, err := catalog.FeaturedPickAt(ctx, time.Date(2025, 8, 12, 18, 0, 0, 0, location))
	if err != nil {
		t.Fatal(err)
	}
	if secondAgain.Item.ID != second.Item.ID {
		t.Fatalf("daytime pick changed before 6pm: %d then %d", second.Item.ID, secondAgain.Item.ID)
	}
	seen := map[int64]bool{first.Item.ID: true, second.Item.ID: true, third.Item.ID: true}
	if len(seen) != 3 {
		t.Fatalf("pick repeated before the full rotation: %d, %d, %d", first.Item.ID, second.Item.ID, third.Item.ID)
	}

	fourth, err := catalog.FeaturedPickAt(ctx, time.Date(2025, 8, 13, 6, 0, 0, 0, location))
	if err != nil {
		t.Fatal(err)
	}
	if fourth.Item.ID == third.Item.ID {
		t.Fatalf("new cycle repeated the previous pick back-to-back: %d", fourth.Item.ID)
	}
	if first.StartsAt != "2025-08-12T01:00:00Z" || first.EndsAt != "2025-08-12T13:00:00Z" {
		t.Fatalf("first period = %s to %s", first.StartsAt, first.EndsAt)
	}
}

func TestFeaturedScanKeepsCurrentAndReplacesItOnlyWhenRemoved(t *testing.T) {
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
	for _, title := range []string{"One", "Two", "Three"} {
		addFeaturedTestMovie(t, catalog, libraryID, scanID, title, 8.0, []Genre{{ID: 18, Name: "Drama"}})
	}
	if err := catalog.FinishScan(ctx, libraryID, scanID, 3, 3, 0, nil); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2025, 8, 12, 10, 0, 0, 0, time.Local)
	current, err := catalog.FeaturedPickAt(ctx, at)
	if err != nil {
		t.Fatal(err)
	}

	// Add an eligible movie and complete a scan. It joins the unseen rotation,
	// but cannot disturb the pick being shown in the active period.
	_, nextScanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"One", "Two", "Three"} {
		addFeaturedTestMovie(t, catalog, libraryID, nextScanID, title, 8.0, []Genre{{ID: 18, Name: "Drama"}})
	}
	newID := addFeaturedTestMovie(t, catalog, libraryID, nextScanID, "New", 8.0, []Genre{{ID: 18, Name: "Drama"}})
	if err := catalog.FinishScan(ctx, libraryID, nextScanID, 4, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	unchanged, err := catalog.FeaturedPickAt(ctx, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Item.ID != current.Item.ID {
		t.Fatalf("scan changed current pick from %d to %d", current.Item.ID, unchanged.Item.ID)
	}
	var newShown bool
	if err := catalog.db.QueryRow(`SELECT shown FROM featured_rotation WHERE item_id = ?`, newID).Scan(&newShown); err != nil {
		t.Fatal(err)
	}
	if newShown {
		t.Fatal("new movie entered the rotation as already shown")
	}

	// A later successful scan omits the current movie. FinishScan replaces it in
	// the same period because a client can no longer display the removed item.
	_, removalScanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"One", "Two", "Three", "New"} {
		var id int64
		if err := catalog.db.QueryRow(`SELECT id FROM items WHERE source_key = ?`, title).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if id == current.Item.ID {
			continue
		}
		addFeaturedTestMovie(t, catalog, libraryID, removalScanID, title, 8.0, []Genre{{ID: 18, Name: "Drama"}})
	}
	if err := catalog.FinishScan(ctx, libraryID, removalScanID, 3, 0, 0, nil); err != nil {
		t.Fatal(err)
	}
	replacement, err := catalog.FeaturedPickAt(ctx, at.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Item.ID == current.Item.ID || replacement.StartsAt != current.StartsAt {
		t.Fatalf("removed pick replacement = %+v, previous = %+v", replacement, current)
	}
	var removedRotationCount int
	if err := catalog.db.QueryRow(`SELECT COUNT(*) FROM featured_rotation WHERE item_id = ?`, current.Item.ID).Scan(&removedRotationCount); err != nil {
		t.Fatal(err)
	}
	if removedRotationCount != 0 {
		t.Fatal("removed movie remained in featured rotation")
	}
}

func TestFeaturedPeriodKeepsLocalSixAcrossDST(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2025, 3, 8, 20, 0, 0, 0, location)
	start, end := featuredPeriod(at)
	if start.Hour() != 18 || end.Hour() != 6 || end.Day() != 9 {
		t.Fatalf("DST period = %v to %v", start, end)
	}
	if end.Sub(start) != 11*time.Hour {
		t.Fatalf("DST period duration = %v, want 11h between local boundaries", end.Sub(start))
	}
}
