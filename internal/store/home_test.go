package store

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

func TestDiscoveryLibraryListsTopLevelMoviesShowsAndShortsWithState(t *testing.T) {
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

	movies, shows, shorts, err := catalog.DiscoveryLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(movies) != 2 || movies[0].ID != ids["movie1"] || movies[1].ID != ids["movie2"] {
		t.Fatalf("movies = %+v, want the two feature-library movies in id order", movies)
	}
	if len(shows) != 2 || shows[0].ID != ids["showA"] || shows[1].ID != ids["showB"] {
		t.Fatalf("shows = %+v, want the two shows in id order", shows)
	}
	if len(shorts) != 1 || shorts[0].Title != "A Short" {
		t.Fatalf("shorts = %+v, want the one short on its own", shorts)
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

func TestHeadlineCreditsKeepDirectorsAndTopThreeBilled(t *testing.T) {
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
	movieID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: libraryID, SourceKey: "Arrival", Kind: "movie", Title: "Arrival", ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Stored in display order: director, notable producer, then the cast.
	if err := catalog.UpdateMetadata(ctx, movieID, MetadataUpdate{TMDBID: 1, Credits: []Credit{
		{PersonID: 1, Name: "Denis Villeneuve", Role: "director"},
		{PersonID: 9, Name: "A Producer", Role: "producer"},
		{PersonID: 2, Name: "Amy Adams", Role: "actor"},
		{PersonID: 3, Name: "Jeremy Renner", Role: "actor"},
		{PersonID: 4, Name: "Forest Whitaker", Role: "actor"},
		{PersonID: 5, Name: "Michael Stuhlbarg", Role: "actor"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, libraryID, scanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	tvLibrary, tvScan, err := catalog.StartScan(ctx, "tv", "/tv")
	if err != nil {
		t.Fatal(err)
	}
	showID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: tvLibrary, SourceKey: "Show", Kind: "show", Title: "Show", ScanID: tvScan,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.UpdateMetadata(ctx, showID, MetadataUpdate{TMDBID: 2, Credits: []Credit{
		{PersonID: 2, Name: "Amy Adams", Role: "actor"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, tvLibrary, tvScan, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}

	credits, err := catalog.HeadlineCredits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var people []int64
	for _, credit := range credits {
		if credit.ItemID != movieID {
			t.Fatalf("headline credit outside the movie library: %+v", credit)
		}
		people = append(people, credit.PersonID)
	}
	if want := []int64{1, 2, 3, 4}; !slices.Equal(people, want) {
		t.Fatalf("headline people = %v, want director and top three billed %v", people, want)
	}
}
