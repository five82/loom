package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestItemsByTMDBID(t *testing.T) {
	ctx := context.Background()
	catalog, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()

	movieID, scanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	starWars := upsertMatchedMovie(t, catalog, movieID, scanID, 11, "Star Wars", 1977, "1977-05-25")
	empire := upsertMatchedMovie(t, catalog, movieID, scanID, 1891, "The Empire Strikes Back", 1980, "1980-05-20")
	upsertMatchedMovie(t, catalog, movieID, scanID, 999, "Zodiac", 2007, "2007-03-02")
	if err := catalog.FinishScan(ctx, movieID, scanID, 3, 3, 0, nil); err != nil {
		t.Fatal(err)
	}

	// A collection reaches across libraries: the Toy Story shorts are catalogued
	// as shorts but belong on the same shelf as the features.
	shortsID, shortsScanID, err := catalog.StartScan(ctx, "shorts", "/shorts")
	if err != nil {
		t.Fatal(err)
	}
	upsertMatchedMovie(t, catalog, shortsID, shortsScanID, 77887, "Hawaiian Vacation", 2011, "2011-06-24")
	if err := catalog.FinishScan(ctx, shortsID, shortsScanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}

	// TMDB numbers episodes independently of movies, so an episode id may equal a
	// movie id a collection asks for. Only movies may answer.
	tvID, tvScanID, err := catalog.StartScan(ctx, "tv", "/tv")
	if err != nil {
		t.Fatal(err)
	}
	showID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: tvID, SourceKey: "Show", Kind: "show", Title: "Show", ScanID: tvScanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	seasonID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: tvID, ParentID: &showID, SourceKey: "Show/season-1",
		Kind: "season", Title: "Season 1", SeasonNumber: 1, ScanID: tvScanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	episodeID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: tvID, ParentID: &seasonID, SourceKey: "Show/S01E01.mkv",
		Kind: "episode", Title: "Episode 1", SeasonNumber: 1, EpisodeNumber: 1, ScanID: tvScanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.UpdateMetadata(ctx, episodeID, MetadataUpdate{
		TMDBID: 11, Title: "Episode 1", ReleaseDate: "2001-01-01",
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, tvID, tvScanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}

	// A shelf draws watched markers from the listing itself, so playback state has
	// to ride along rather than needing a request per movie.
	if _, err := catalog.UpsertMedia(ctx, MediaFile{
		ItemID: starWars, Path: "/movies/Star Wars (1977)/Star Wars (1977).mkv",
		Size: 100, MTimeNS: 20, DurationMS: 600_000, Container: "matroska",
		LastSeenScanID: scanID,
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.SetProgress(ctx, starWars, 60_000, 600_000); err != nil {
		t.Fatal(err)
	}

	// 424242 is owned by nobody, which is how an unowned collection member drops
	// out instead of erroring.
	items, err := catalog.ItemsByTMDBID(ctx, []int64{11, 1891, 77887, 424242})
	if err != nil {
		t.Fatal(err)
	}
	titles := make([]string, len(items))
	for index, item := range items {
		titles[index] = item.Title
	}
	if len(items) != 3 || titles[0] != "Star Wars" || titles[1] != "The Empire Strikes Back" ||
		titles[2] != "Hawaiian Vacation" {
		t.Fatalf("collection members are not in release order: %v", titles)
	}
	if items[0].Progress == nil || items[0].Progress.PositionMS != 60_000 {
		t.Fatalf("collection member lost its playback state: %+v", items[0].Progress)
	}

	// A movie that leaves the library leaves its shelves with it.
	_, rescanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	upsertMatchedMovie(t, catalog, movieID, rescanID, 11, "Star Wars", 1977, "1977-05-25")
	if err := catalog.FinishScan(ctx, movieID, rescanID, 1, 0, 0, nil); err != nil {
		t.Fatal(err)
	}
	items, err = catalog.ItemsByTMDBID(ctx, []int64{11, 1891})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != starWars {
		t.Fatalf("unavailable member %d was still served: %+v", empire, items)
	}

	if empty, err := catalog.ItemsByTMDBID(ctx, nil); err != nil || empty != nil {
		t.Fatalf("empty membership = %+v, %v", empty, err)
	}
}

func TestItemsReleasedBetween(t *testing.T) {
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
	for index, movie := range []struct {
		title       string
		releaseDate string
	}{
		{"Before", "2023-12-31"},
		{"First Day", "2024-01-01"},
		{"Last Day", "2025-07-01"},
		{"After", "2025-07-02"},
	} {
		upsertMatchedMovie(t, catalog, libraryID, scanID, int64(500000+index),
			movie.title, 2024, movie.releaseDate)
	}
	if err := catalog.FinishScan(ctx, libraryID, scanID, 4, 4, 0, nil); err != nil {
		t.Fatal(err)
	}

	start := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, time.July, 1, 0, 0, 0, 0, time.UTC)
	items, err := catalog.ItemsReleasedBetween(ctx, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Title != "Last Day" || items[1].Title != "First Day" {
		t.Fatalf("recent releases = %+v, want Last Day and First Day", items)
	}
	if empty, err := catalog.ItemsReleasedBetween(ctx, end, start); err != nil || empty != nil {
		t.Fatalf("reversed release range = %+v, %v", empty, err)
	}
}

func TestItemsByVideoDynamicRangeAreMovieOnly(t *testing.T) {
	ctx := context.Background()
	catalog, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()

	movieLibraryID, movieScanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	arrival := upsertMatchedMovie(t, catalog, movieLibraryID, movieScanID, 329865,
		"Arrival", 2016, "2016-11-11")
	dune := upsertMatchedMovie(t, catalog, movieLibraryID, movieScanID, 438631,
		"Dune", 2021, "2021-10-22")
	alien := upsertMatchedMovie(t, catalog, movieLibraryID, movieScanID, 348,
		"Alien", 1979, "1979-05-25")
	for _, media := range []struct {
		itemID       int64
		dynamicRange string
	}{
		{arrival, "hdr"},
		{dune, "dolby_vision"},
		{alien, "sdr"},
	} {
		if _, err := catalog.UpsertMedia(ctx, MediaFile{
			ItemID: media.itemID, Path: fmt.Sprintf("/media/%s-%d.mkv", media.dynamicRange, media.itemID),
			Size: 100, MTimeNS: 20, DurationMS: 600_000, LastSeenScanID: movieScanID,
		}, []Stream{{Index: 0, Kind: "video", DynamicRange: media.dynamicRange}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.FinishScan(ctx, movieLibraryID, movieScanID, 3, 3, 0, nil); err != nil {
		t.Fatal(err)
	}

	// Even an HDR episode does not promote its show into a movie collection.
	tvLibraryID, tvScanID, err := catalog.StartScan(ctx, "tv", "/tv")
	if err != nil {
		t.Fatal(err)
	}
	showID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: tvLibraryID, SourceKey: "Show", Kind: "show", Title: "Show", ScanID: tvScanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	seasonID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: tvLibraryID, ParentID: &showID, SourceKey: "Show/season-1",
		Kind: "season", Title: "Season 1", SeasonNumber: 1, ScanID: tvScanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	episodeID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: tvLibraryID, ParentID: &seasonID, SourceKey: "Show/S01E01.mkv",
		Kind: "episode", Title: "HDR Episode", SeasonNumber: 1, EpisodeNumber: 1,
		ScanID: tvScanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertMedia(ctx, MediaFile{
		ItemID: episodeID, Path: "/tv/Show/S01E01.mkv", Size: 100, MTimeNS: 20,
		DurationMS: 600_000, LastSeenScanID: tvScanID,
	}, []Stream{{Index: 0, Kind: "video", DynamicRange: "hdr"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, tvLibraryID, tvScanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}

	items, err := catalog.ItemsByVideoDynamicRange(ctx, []string{"hdr", "dolby_vision"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Title != "Arrival" || items[1].Title != "Dune" {
		t.Fatalf("HDR movies = %+v, want Arrival and Dune", items)
	}
	if empty, err := catalog.ItemsByVideoDynamicRange(ctx, nil); err != nil || empty != nil {
		t.Fatalf("empty dynamic ranges = %+v, %v", empty, err)
	}
}

func upsertMatchedMovie(t *testing.T, catalog *Store, libraryID, scanID, tmdbID int64,
	title string, year int, releaseDate string) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: libraryID, SourceKey: title, Kind: "movie", Title: title,
		Year: year, ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.UpdateMetadata(ctx, id, MetadataUpdate{
		TMDBID: tmdbID, Title: title, Year: year, ReleaseDate: releaseDate,
	}); err != nil {
		t.Fatal(err)
	}
	return id
}
