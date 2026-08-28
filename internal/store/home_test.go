package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestDiscoveryLibraryListsTopLevelMoviesAndShowsWithState(t *testing.T) {
	ctx := context.Background()
	catalog, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()

	ids := seedMixedCatalog(t, ctx, catalog)
	shortsLibrary, shortsScan, err := catalog.StartScan(ctx, "shorts", "/shorts")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: shortsLibrary, SourceKey: "short", Kind: "movie", Title: "A Short", ScanID: shortsScan,
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, shortsLibrary, shortsScan, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := catalog.UpdateMetadata(ctx, ids["movie1"], MetadataUpdate{
		TMDBID: 1, Title: "First Movie", Genres: []Genre{{ID: 18, Name: "Drama"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.SetProgress(ctx, ids["movie1"], 60_000, 600_000); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.SetPlayed(ctx, ids["showA-e1"]); err != nil {
		t.Fatal(err)
	}

	movies, shows, err := catalog.DiscoveryLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(movies) != 2 || movies[0].ID != ids["movie1"] || movies[1].ID != ids["movie2"] {
		t.Fatalf("movies = %+v, want the two feature-library movies in id order", movies)
	}
	if len(shows) != 2 || shows[0].ID != ids["showA"] || shows[1].ID != ids["showB"] {
		t.Fatalf("shows = %+v, want the two shows in id order", shows)
	}
	// The shelves decide started-ness from these fields, so the listing has
	// to carry them: progress on the movie, watched counts on the show, and
	// genres for the genre spotlight.
	if movies[0].Progress == nil || movies[0].Progress.PositionMS != 60_000 || movies[1].Progress != nil {
		t.Fatalf("movie progress = %+v / %+v", movies[0].Progress, movies[1].Progress)
	}
	if len(movies[0].Genres) != 1 || movies[0].Genres[0].Name != "Drama" {
		t.Fatalf("movie genres = %+v", movies[0].Genres)
	}
	if shows[0].EpisodeCount != 2 || shows[0].UnwatchedCount != 1 || shows[1].UnwatchedCount != 2 {
		t.Fatalf("show counts = %d/%d and %d/%d", shows[0].UnwatchedCount, shows[0].EpisodeCount,
			shows[1].UnwatchedCount, shows[1].EpisodeCount)
	}
}
