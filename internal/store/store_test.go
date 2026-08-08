package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogAndProgress(t *testing.T) {
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
	itemID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: libraryID, SourceKey: "Arrival (2016)", Kind: "movie",
		Title: "Arrival", Year: 2016, ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalog.UpsertMedia(ctx, MediaFile{
		ItemID: itemID, Path: "/movies/Arrival (2016)/Arrival (2016).mkv",
		Size: 100, MTimeNS: 20, DurationMS: 600_000, Container: "matroska",
		LastSeenScanID: scanID,
	}, []Stream{{
		Index: 0, Kind: "video", Codec: "hevc", Profile: "Main 10",
		Width: 3840, Height: 1604, DynamicRange: "hdr", IsDefault: true,
	}, {
		Index: 1, Kind: "audio", Codec: "opus", Channels: 8, ChannelLayout: "7.1",
	}}, []Chapter{
		{Index: 0, StartMS: 0, Title: "Opening"},
		{Index: 1, StartMS: 264_264},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, libraryID, scanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}

	item, err := catalog.Item(ctx, itemID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Title != "Arrival" || item.Media == nil || len(item.Media.Streams) != 2 {
		t.Fatalf("unexpected item: %+v", item)
	}
	video, audio := item.Media.Streams[0], item.Media.Streams[1]
	if video.Profile != "Main 10" || video.DynamicRange != "hdr" ||
		audio.ChannelLayout != "7.1" {
		t.Fatalf("technical metadata was not persisted: %+v", item.Media.Streams)
	}
	if len(item.Media.Chapters) != 2 || item.Media.Chapters[0].Title != "Opening" ||
		item.Media.Chapters[1].StartMS != 264_264 {
		t.Fatalf("chapters were not persisted: %+v", item.Media.Chapters)
	}

	// Replacing an encode changes nothing else about an item, so the browse
	// listing has to carry the media version a client can compare against.
	listed, err := catalog.ListItems(ctx, ListOptions{LibraryKind: "movies", TopLevel: true})
	if err != nil {
		t.Fatal(err)
	}
	if item.Media.Tag == "" || len(listed) != 1 || listed[0].MediaTag != item.Media.Tag {
		t.Fatalf("browse listing did not expose media version %q: %+v", item.Media.Tag, listed)
	}

	progress, err := catalog.SetProgress(ctx, itemID, 60_000, 600_000)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Played || progress.ResumePositionMS != 60_000 {
		t.Fatalf("unexpected resumable progress: %+v", progress)
	}
	continuing, err := catalog.ContinueWatching(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(continuing) != 1 || continuing[0].Progress == nil || continuing[0].Progress.ResumePositionMS != 60_000 {
		t.Fatalf("unexpected continue-watching items: %+v", continuing)
	}
	progress, err = catalog.SetProgress(ctx, itemID, 540_000, 600_000)
	if err != nil {
		t.Fatal(err)
	}
	if !progress.Played || progress.ResumePositionMS != 0 {
		t.Fatalf("unexpected completed progress: %+v", progress)
	}
}

func TestMovieGenres(t *testing.T) {
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
	arrivalID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: libraryID, SourceKey: "Arrival", Kind: "movie", Title: "Arrival", ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	alienID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: libraryID, SourceKey: "Alien", Kind: "movie", Title: "Alien", ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	shortsID, shortsScanID, err := catalog.StartScan(ctx, "shorts", "/shorts")
	if err != nil {
		t.Fatal(err)
	}
	prestoID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: shortsID, SourceKey: "Presto", Kind: "movie", Title: "Presto", ScanID: shortsScanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	for id, genres := range map[int64][]Genre{
		arrivalID: {{ID: 18, Name: "Drama"}, {ID: 878, Name: "Science Fiction"}},
		alienID:   {{ID: 878, Name: "Science Fiction"}},
		prestoID:  {{ID: 878, Name: "Science Fiction"}, {ID: 16, Name: "Animation"}},
	} {
		if err := catalog.UpdateMetadata(ctx, id, MetadataUpdate{
			TMDBID: id, Genres: genres,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.FinishScan(ctx, libraryID, scanID, 2, 2, 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, shortsID, shortsScanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}

	// Short films are browsed as their own library, so they contribute no genres
	// and no counts here.
	genres, err := catalog.Genres(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(genres) != 2 || genres[0].Name != "Drama" || genres[0].ItemCount != 1 ||
		genres[1].ID != 878 || genres[1].ItemCount != 2 {
		t.Fatalf("genre summaries = %+v", genres)
	}
	items, err := catalog.ListItems(ctx, ListOptions{LibraryKind: "movies", TopLevel: true, GenreID: 18})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != arrivalID || len(items[0].Genres) != 2 || !items[0].DetailsLoaded {
		t.Fatalf("drama movies = %+v", items)
	}

	if err := catalog.UpdateMetadata(ctx, arrivalID, MetadataUpdate{
		TMDBID: arrivalID, Genres: []Genre{{ID: 53, Name: "Thriller"}},
	}); err != nil {
		t.Fatal(err)
	}
	item, err := catalog.Item(ctx, arrivalID)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Genres) != 1 || item.Genres[0].ID != 53 {
		t.Fatalf("replaced movie genres = %+v", item.Genres)
	}

	_, nextScanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: libraryID, SourceKey: "Arrival", Kind: "movie", Title: "Arrival", ScanID: nextScanID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, libraryID, nextScanID, 1, 0, 0, nil); err != nil {
		t.Fatal(err)
	}
	genres, err = catalog.Genres(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(genres) != 1 || genres[0].ID != 53 || genres[0].ItemCount != 1 {
		t.Fatalf("available genre summaries = %+v", genres)
	}
}

func TestSearchItems(t *testing.T) {
	ctx := context.Background()
	catalog, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()

	moviesID, moviesScanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	movieID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: moviesID, SourceKey: "Pilot", Kind: "movie", Title: "Pilot", ScanID: moviesScanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.UpdateMetadata(ctx, movieID, MetadataUpdate{
		TMDBID: 1, Genres: []Genre{{ID: 18, Name: "Drama"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, moviesID, moviesScanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}

	tvID, tvScanID, err := catalog.StartScan(ctx, "tv", "/tv")
	if err != nil {
		t.Fatal(err)
	}
	showID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: tvID, SourceKey: "show:Pilot Light", Kind: "show", Title: "Pilot Light", ScanID: tvScanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	seasonID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: tvID, ParentID: &showID, SourceKey: "show:Pilot Light:season:1",
		Kind: "season", Title: "Pilot Season", SeasonNumber: 1, ScanID: tvScanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	episodeID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: tvID, ParentID: &seasonID, SourceKey: "show:Pilot Light:S01E01",
		Kind: "episode", Title: "The Pilot", SeasonNumber: 1, EpisodeNumber: 1, ScanID: tvScanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: tvID, ParentID: &showID, SourceKey: "show:Pilot Light:unmatched",
		Kind: "unmatched", Title: "Pilot Recording", ScanID: tvScanID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, tvID, tvScanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}

	results, err := catalog.SearchItems(ctx, "PILOT", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[0].ID != movieID || results[1].ID != showID ||
		results[2].ID != episodeID {
		t.Fatalf("search results = %+v", results)
	}
	if len(results[0].Genres) != 1 || results[0].Genres[0].Name != "Drama" {
		t.Fatalf("movie search result genres = %+v", results[0].Genres)
	}
	if results[2].SeriesTitle != "Pilot Light" || results[2].SeasonTitle != "Pilot Season" {
		t.Fatalf("episode search context = %+v", results[2])
	}

	results, err = catalog.SearchItems(ctx, "pilot", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != showID {
		t.Fatalf("paginated search results = %+v", results)
	}
	results, err = catalog.SearchItems(ctx, "%", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("literal wildcard search results = %+v", results)
	}
}

func TestSeasonsAndEpisodesInheritImages(t *testing.T) {
	ctx := context.Background()
	catalog, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()

	libraryID, scanID, err := catalog.StartScan(ctx, "tv", "/tv")
	if err != nil {
		t.Fatal(err)
	}
	showID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: libraryID, SourceKey: "Show", Kind: "show", Title: "Show", ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	seasonID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: libraryID, ParentID: &showID, SourceKey: "Show/season-1",
		Kind: "season", Title: "Season 1", SeasonNumber: 1, ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	episodeID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: libraryID, ParentID: &seasonID, SourceKey: "Show/S01E01.mkv",
		Kind: "episode", Title: "Episode 1", SeasonNumber: 1, EpisodeNumber: 1, ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	posterID, err := catalog.UpsertImage(ctx, Image{
		ItemID: showID, Kind: "poster", Path: "/images/poster.jpg", SourceURL: "https://example/poster.jpg",
		Provider: "tmdb", ProviderPath: "/poster.jpg", Tag: "poster-tag",
		ContentType: "image/jpeg", Width: 200, Height: 300, UpdatedAt: now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	logoID, err := catalog.UpsertImage(ctx, Image{
		ItemID: showID, Kind: "logo", Path: "/images/logo.png", SourceURL: "https://example/logo.png",
		Provider: "tmdb", ProviderPath: "/logo.png", Tag: "logo-tag",
		ContentType: "image/png", Width: 300, Height: 100, UpdatedAt: now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	thumbID, err := catalog.UpsertImage(ctx, Image{
		ItemID: showID, Kind: "thumb", Path: "/images/thumb.jpg", SourceURL: "https://example/thumb.jpg",
		Provider: "tmdb", ProviderPath: "/thumb.jpg", Tag: "thumb-tag",
		ContentType: "image/jpeg", Width: 1920, Height: 1080, UpdatedAt: now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{seasonID, episodeID} {
		item, err := catalog.Item(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if item.PosterImageID != posterID || item.PosterImageTag != "poster-tag" {
			t.Fatalf("item %d poster = %d/%q, want inherited %d/poster-tag", id,
				item.PosterImageID, item.PosterImageTag, posterID)
		}
		if item.LogoImageID != logoID || item.LogoImageTag != "logo-tag" {
			t.Fatalf("item %d logo = %d/%q, want inherited %d/logo-tag", id,
				item.LogoImageID, item.LogoImageTag, logoID)
		}
		if item.ThumbImageID != thumbID || item.ThumbImageTag != "thumb-tag" {
			t.Fatalf("item %d thumb = %d/%q, want inherited %d/thumb-tag", id,
				item.ThumbImageID, item.ThumbImageTag, thumbID)
		}
	}

	seasonPosterID, err := catalog.UpsertImage(ctx, Image{
		ItemID: seasonID, Kind: "poster", Path: "/images/season-poster.jpg",
		SourceURL: "https://example/season-poster.jpg", Provider: "tmdb",
		ProviderPath: "/season-poster.jpg", Tag: "season-poster-tag",
		ContentType: "image/jpeg", Width: 200, Height: 300, UpdatedAt: now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{seasonID, episodeID} {
		item, err := catalog.Item(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if item.PosterImageID != seasonPosterID || item.PosterImageTag != "season-poster-tag" {
			t.Fatalf("item %d poster = %d/%q, want season poster %d/season-poster-tag", id,
				item.PosterImageID, item.PosterImageTag, seasonPosterID)
		}
		if item.LogoImageID != logoID || item.LogoImageTag != "logo-tag" {
			t.Fatalf("item %d logo = %d/%q, want inherited %d/logo-tag", id,
				item.LogoImageID, item.LogoImageTag, logoID)
		}
	}
}

func TestCurrentSchemaCreatedAndAccepted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loom.db")
	catalog, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := catalog.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 10 {
		t.Fatalf("created schema version = %d, want 10", version)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}

	catalog, err = Open(path)
	if err != nil {
		t.Fatalf("open current schema: %v", err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUnsupportedSchemaRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loom.db")
	catalog, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 5"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "loom developer reset") {
		t.Fatalf("Open with unsupported schema error = %v", err)
	}
}

func TestFailedScanDoesNotMakeItemsUnavailable(t *testing.T) {
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
	_, err = catalog.UpsertItem(ctx, ItemInput{
		LibraryID: libraryID, SourceKey: "movie", Kind: "movie", Title: "Movie", ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, libraryID, scanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}

	_, failedScanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, libraryID, failedScanID, 0, 0, 0, context.Canceled); err != nil {
		t.Fatal(err)
	}
	items, err := catalog.ListItems(ctx, ListOptions{LibraryKind: "movies", TopLevel: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("available items = %d, want 1", len(items))
	}
}

func TestLastScansReportsNewestFinishedScanPerLibrary(t *testing.T) {
	ctx := context.Background()
	catalog, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()

	movieID, movieScanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, movieID, movieScanID, 12, 3, 1, nil); err != nil {
		t.Fatal(err)
	}
	// A later failed scan must replace the successful one in the report, even
	// though libraries.last_scan_id still points at the success.
	_, failedScanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, movieID, failedScanID, 4, 0, 0, errors.New("mount gone")); err != nil {
		t.Fatal(err)
	}
	// A scan still running has no result to report yet.
	if _, _, err := catalog.StartScan(ctx, "tv", "/tv"); err != nil {
		t.Fatal(err)
	}

	scans, err := catalog.LastScans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []LibraryScan{{
		Library: "movies", Status: "failed", Discovered: 4, Error: "mount gone",
	}}
	if len(scans) != len(want) {
		t.Fatalf("last scans = %+v, want one movies entry", scans)
	}
	got := scans[0]
	if got.FinishedAt == "" {
		t.Fatalf("last scan has no finished_at: %+v", got)
	}
	got.FinishedAt = ""
	if got != want[0] {
		t.Fatalf("last scan = %+v, want %+v", got, want[0])
	}
}

func TestBackupSnapshotsLiveCatalog(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "loom.db")
	catalog, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()

	libraryID, scanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: libraryID, SourceKey: "Arrival (2016)", Kind: "movie",
		Title: "Arrival", Year: 2016, ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertMedia(ctx, MediaFile{
		ItemID: itemID, Path: "/movies/Arrival (2016)/Arrival (2016).mkv",
		Size: 100, MTimeNS: 20, DurationMS: 600_000, LastSeenScanID: scanID,
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.SetProgress(ctx, itemID, 120_000, 600_000); err != nil {
		t.Fatal(err)
	}

	// The catalog stays open throughout, as it would be under a serving daemon.
	destination := filepath.Join(dir, "snapshot.db")
	if err := Backup(ctx, source, destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode = %v, want -rw-------", info.Mode().Perm())
	}

	snapshot, err := Open(destination)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer func() { _ = snapshot.Close() }()
	item, err := snapshot.Item(ctx, itemID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Title != "Arrival" || item.Progress == nil || item.Progress.PositionMS != 120_000 {
		t.Fatalf("snapshot lost catalog or playback state: %+v", item)
	}

	if err := Backup(ctx, source, destination); err == nil {
		t.Fatal("Backup overwrote an existing snapshot")
	}
	if err := Backup(ctx, filepath.Join(dir, "missing.db"), filepath.Join(dir, "other.db")); err == nil {
		t.Fatal("Backup of a missing catalog succeeded")
	}
}

func TestNextUp(t *testing.T) {
	ctx := context.Background()
	catalog, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()

	libraryID, scanID, err := catalog.StartScan(ctx, "tv", "/tv")
	if err != nil {
		t.Fatal(err)
	}
	// episode records one episode and its file, and returns the item id.
	episode := func(show string, seasonID int64, season, number int) int64 {
		t.Helper()
		key := fmt.Sprintf("%s/S%02dE%02d", show, season, number)
		id, err := catalog.UpsertItem(ctx, ItemInput{
			LibraryID: libraryID, ParentID: &seasonID, SourceKey: key, Kind: "episode",
			Title: key, SeasonNumber: season, EpisodeNumber: number, ScanID: scanID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := catalog.UpsertMedia(ctx, MediaFile{
			ItemID: id, Path: "/tv/" + key + ".mkv", Size: 100, MTimeNS: 20,
			DurationMS: 1_800_000, Container: "matroska", LastSeenScanID: scanID,
		}, nil, nil); err != nil {
			t.Fatal(err)
		}
		return id
	}
	season := func(show string, showID int64, number int) int64 {
		t.Helper()
		id, err := catalog.UpsertItem(ctx, ItemInput{
			LibraryID: libraryID, ParentID: &showID,
			SourceKey: fmt.Sprintf("%s/season-%d", show, number), Kind: "season",
			Title: fmt.Sprintf("Season %d", number), SeasonNumber: number, ScanID: scanID,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	show := func(title string) int64 {
		t.Helper()
		id, err := catalog.UpsertItem(ctx, ItemInput{
			LibraryID: libraryID, SourceKey: title, Kind: "show", Title: title, ScanID: scanID,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	watch := func(itemID int64, positionMS int64) {
		t.Helper()
		if _, err := catalog.SetProgress(ctx, itemID, positionMS, 1_800_000); err != nil {
			t.Fatal(err)
		}
	}

	// Office: first episode finished, so the second is next.
	officeID := show("Office")
	officeS1 := season("Office", officeID, 1)
	officeSpecials := season("Office", officeID, 0)
	special := episode("Office", officeSpecials, 0, 1)
	officeE1 := episode("Office", officeS1, 1, 1)
	officeE2 := episode("Office", officeS1, 1, 2)

	// Gap: episode 2 is missing from disk, so episode 3 follows episode 1.
	gapID := show("Gap")
	gapS1 := season("Gap", gapID, 1)
	gapE1 := episode("Gap", gapS1, 1, 1)
	gapE3 := episode("Gap", gapS1, 1, 3)

	// Partial: an episode is mid-watch, so Continue Watching owns this show.
	partialID := show("Partial")
	partialS1 := season("Partial", partialID, 1)
	partialE1 := episode("Partial", partialS1, 1, 1)
	episode("Partial", partialS1, 1, 2)

	// Glance: sampled below the resume floor, so Continue Watching does not
	// carry it and Next Up still has to offer the episode itself.
	glanceID := show("Glance")
	glanceS1 := season("Glance", glanceID, 1)
	glanceE1 := episode("Glance", glanceS1, 1, 1)
	episode("Glance", glanceS1, 1, 2)

	// Done: every episode watched. Fresh: never started.
	doneID := show("Done")
	doneE1 := episode("Done", season("Done", doneID, 1), 1, 1)
	freshID := show("Fresh")
	episode("Fresh", season("Fresh", freshID, 1), 1, 1)

	if err := catalog.FinishScan(ctx, libraryID, scanID, 8, 8, 0, nil); err != nil {
		t.Fatal(err)
	}

	if items, err := catalog.NextUp(ctx, 20); err != nil {
		t.Fatal(err)
	} else if len(items) != 0 {
		t.Fatalf("next up listed unstarted shows: %+v", items)
	}

	watch(doneE1, 1_750_000)
	watch(glanceE1, 30_000)
	watch(gapE1, 1_750_000)
	watch(partialE1, 900_000)
	watch(officeE1, 1_750_000)

	// A glance is below the resume floor, so it reaches neither home-screen row
	// unless Next Up still counts the episode as unfinished.
	continuing, err := catalog.ContinueWatching(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range continuing {
		if item.ID == glanceE1 {
			t.Fatalf("continue watching carried a glance: %+v", continuing)
		}
	}

	items, err := catalog.NextUp(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	// Office was watched last, so it leads. Done and Fresh have nothing to
	// offer, and Partial is already in Continue Watching.
	want := []int64{officeE2, gapE3, glanceE1}
	if len(items) != len(want) {
		t.Fatalf("next up = %+v, want episodes %v", items, want)
	}
	for index, id := range want {
		if items[index].ID != id {
			t.Fatalf("next up[%d] = %d (%s), want %d", index, items[index].ID, items[index].Title, id)
		}
	}

	// Finishing the mid-watch episode releases the show from Continue Watching,
	// so Next Up has to take over rather than let it fall off the home screen.
	watch(partialE1, 1_750_000)
	items, err = catalog.NextUp(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 || items[0].EpisodeNumber != 2 || items[0].Title != "Partial/S01E02" {
		t.Fatalf("next up after finishing an episode = %+v", items)
	}

	// A watched special must not become the next episode of its show.
	if _, err := catalog.SetProgress(ctx, special, 1_750_000, 1_800_000); err != nil {
		t.Fatal(err)
	}
	items, err = catalog.NextUp(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == special {
			t.Fatalf("next up offered a special: %+v", items)
		}
	}
}

// Watching happens away from Loom too, and an abandoned title otherwise sits in
// Continue Watching forever, so played state has to be writable without playing
// anything.
func TestPlayedWritesCascade(t *testing.T) {
	ctx := context.Background()
	catalog, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()

	libraryID, scanID, err := catalog.StartScan(ctx, "tv", "/tv")
	if err != nil {
		t.Fatal(err)
	}
	showID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: libraryID, SourceKey: "Show", Kind: "show", Title: "Show", ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var seasons []int64
	var episodes []int64
	for season := 1; season <= 2; season++ {
		seasonID, err := catalog.UpsertItem(ctx, ItemInput{
			LibraryID: libraryID, ParentID: &showID,
			SourceKey: fmt.Sprintf("Show/season-%d", season), Kind: "season",
			Title: fmt.Sprintf("Season %d", season), SeasonNumber: season, ScanID: scanID,
		})
		if err != nil {
			t.Fatal(err)
		}
		seasons = append(seasons, seasonID)
		for number := 1; number <= 2; number++ {
			id, err := catalog.UpsertItem(ctx, ItemInput{
				LibraryID: libraryID, ParentID: &seasonID,
				SourceKey: fmt.Sprintf("Show/S%02dE%02d", season, number), Kind: "episode",
				Title: fmt.Sprintf("S%02dE%02d", season, number), SeasonNumber: season,
				EpisodeNumber: number, ScanID: scanID,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := catalog.UpsertMedia(ctx, MediaFile{
				ItemID: id, Path: fmt.Sprintf("/tv/S%02dE%02d.mkv", season, number), Size: 100,
				MTimeNS: 20, DurationMS: 1_800_000, Container: "matroska", LastSeenScanID: scanID,
			}, nil, nil); err != nil {
				t.Fatal(err)
			}
			episodes = append(episodes, id)
		}
	}
	if err := catalog.FinishScan(ctx, libraryID, scanID, 4, 4, 0, nil); err != nil {
		t.Fatal(err)
	}

	// One season watched elsewhere: the other season must be left alone.
	updated, err := catalog.SetPlayed(ctx, seasons[0])
	if err != nil {
		t.Fatal(err)
	}
	if updated != 2 {
		t.Fatalf("marking a season played updated %d rows, want 2", updated)
	}
	listed, err := catalog.ListItems(ctx, ListOptions{ParentID: &seasons[0]})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range listed {
		if item.Progress == nil || !item.Progress.Played || item.Progress.ResumePositionMS != 0 {
			t.Fatalf("episode was not marked played: %+v", item.Progress)
		}
	}
	if listed, err = catalog.ListItems(ctx, ListOptions{ParentID: &seasons[1]}); err != nil {
		t.Fatal(err)
	} else if listed[0].Progress != nil {
		t.Fatalf("marking one season played reached another: %+v", listed[0].Progress)
	}

	// A half-watched episode that is then marked played must leave Continue
	// Watching rather than keep offering a resume point.
	if _, err := catalog.SetProgress(ctx, episodes[2], 900_000, 1_800_000); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.SetPlayed(ctx, episodes[2]); err != nil {
		t.Fatal(err)
	}
	continuing, err := catalog.ContinueWatching(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(continuing) != 0 {
		t.Fatalf("continue watching kept a played episode: %+v", continuing)
	}

	// Clearing the show returns the whole series to a first watch.
	cleared, err := catalog.ClearPlayback(ctx, showID)
	if err != nil {
		t.Fatal(err)
	}
	if cleared != 3 {
		t.Fatalf("clearing a show removed %d rows, want 3", cleared)
	}
	nextUp, err := catalog.NextUp(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(nextUp) != 0 {
		t.Fatalf("next up still had history for a cleared show: %+v", nextUp)
	}
	for _, id := range episodes {
		item, err := catalog.Item(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if item.Progress != nil {
			t.Fatalf("episode %d kept playback state: %+v", id, item.Progress)
		}
	}

	// Clearing again is harmless, and an unknown item is still an error.
	if cleared, err = catalog.ClearPlayback(ctx, showID); err != nil || cleared != 0 {
		t.Fatalf("second clear updated=%d err=%v", cleared, err)
	}
	if _, err := catalog.SetPlayed(ctx, showID+10_000); !errors.Is(err, ErrNotFound) {
		t.Fatalf("marking an unknown item played returned %v", err)
	}
	if _, err := catalog.ClearPlayback(ctx, showID+10_000); !errors.Is(err, ErrNotFound) {
		t.Fatalf("clearing an unknown item returned %v", err)
	}
}

func TestListItemsCarryProgress(t *testing.T) {
	ctx := context.Background()
	catalog, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()

	libraryID, scanID, err := catalog.StartScan(ctx, "tv", "/tv")
	if err != nil {
		t.Fatal(err)
	}
	showID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: libraryID, SourceKey: "Show", Kind: "show", Title: "Show", ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	seasonID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: libraryID, ParentID: &showID, SourceKey: "Show/season-1",
		Kind: "season", Title: "Season 1", SeasonNumber: 1, ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var episodes []int64
	for number := 1; number <= 3; number++ {
		id, err := catalog.UpsertItem(ctx, ItemInput{
			LibraryID: libraryID, ParentID: &seasonID,
			SourceKey: fmt.Sprintf("Show/S01E%02d", number), Kind: "episode",
			Title: fmt.Sprintf("Episode %d", number), SeasonNumber: 1,
			EpisodeNumber: number, ScanID: scanID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := catalog.UpsertMedia(ctx, MediaFile{
			ItemID: id, Path: fmt.Sprintf("/tv/S01E%02d.mkv", number), Size: 100,
			MTimeNS: 20, DurationMS: 1_800_000, Container: "matroska", LastSeenScanID: scanID,
		}, nil, nil); err != nil {
			t.Fatal(err)
		}
		episodes = append(episodes, id)
	}
	if err := catalog.FinishScan(ctx, libraryID, scanID, 3, 3, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.SetProgress(ctx, episodes[0], 1_750_000, 1_800_000); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.SetProgress(ctx, episodes[1], 600_000, 1_800_000); err != nil {
		t.Fatal(err)
	}

	// Browsing a season has to say what has already been watched, otherwise a
	// client can only find out one request per episode.
	listed, err := catalog.ListItems(ctx, ListOptions{ParentID: &seasonID})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 3 {
		t.Fatalf("season children = %+v", listed)
	}
	if listed[0].Progress == nil || !listed[0].Progress.Played {
		t.Fatalf("watched episode lost its played flag: %+v", listed[0].Progress)
	}
	if listed[1].Progress == nil || listed[1].Progress.ResumePositionMS != 600_000 {
		t.Fatalf("partly watched episode lost its resume point: %+v", listed[1].Progress)
	}
	if listed[2].Progress != nil {
		t.Fatalf("untouched episode reported progress: %+v", listed[2].Progress)
	}
}

func TestShowAndSeasonWatchedCounts(t *testing.T) {
	ctx := context.Background()
	catalog, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()

	// Season one holds two episodes, season two holds one.
	counts := []int{2, 1}
	seed := func(scanID, libraryID int64, skip string) (showID int64, seasons, episodes []int64) {
		showID, err := catalog.UpsertItem(ctx, ItemInput{
			LibraryID: libraryID, SourceKey: "Show", Kind: "show", Title: "Show", ScanID: scanID,
		})
		if err != nil {
			t.Fatal(err)
		}
		for season, episodeCount := range counts {
			season++
			seasonID, err := catalog.UpsertItem(ctx, ItemInput{
				LibraryID: libraryID, ParentID: &showID,
				SourceKey: fmt.Sprintf("Show/season-%d", season), Kind: "season",
				Title: fmt.Sprintf("Season %d", season), SeasonNumber: season, ScanID: scanID,
			})
			if err != nil {
				t.Fatal(err)
			}
			seasons = append(seasons, seasonID)
			for number := 1; number <= episodeCount; number++ {
				key := fmt.Sprintf("Show/S%02dE%02d", season, number)
				if key == skip {
					continue
				}
				id, err := catalog.UpsertItem(ctx, ItemInput{
					LibraryID: libraryID, ParentID: &seasonID, SourceKey: key, Kind: "episode",
					Title: key, SeasonNumber: season, EpisodeNumber: number, ScanID: scanID,
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := catalog.UpsertMedia(ctx, MediaFile{
					ItemID: id, Path: "/tv/" + key + ".mkv", Size: 100, MTimeNS: 20,
					DurationMS: 1_800_000, Container: "matroska", LastSeenScanID: scanID,
				}, nil, nil); err != nil {
					t.Fatal(err)
				}
				episodes = append(episodes, id)
			}
		}
		return showID, seasons, episodes
	}

	libraryID, scanID, err := catalog.StartScan(ctx, "tv", "/tv")
	if err != nil {
		t.Fatal(err)
	}
	showID, _, episodes := seed(scanID, libraryID, "")
	if err := catalog.FinishScan(ctx, libraryID, scanID, 3, 3, 0, nil); err != nil {
		t.Fatal(err)
	}

	shows, err := catalog.ListItems(ctx, ListOptions{TopLevel: true, Kind: "show"})
	if err != nil {
		t.Fatal(err)
	}
	if len(shows) != 1 || shows[0].EpisodeCount != 3 || shows[0].UnwatchedCount != 3 {
		t.Fatalf("unwatched show counts = %+v", shows)
	}

	// Watching one episode leaves the rest of the show and its season unwatched.
	if _, err := catalog.SetPlayed(ctx, episodes[0]); err != nil {
		t.Fatal(err)
	}
	show, err := catalog.Item(ctx, showID)
	if err != nil {
		t.Fatal(err)
	}
	if show.EpisodeCount != 3 || show.UnwatchedCount != 2 {
		t.Fatalf("show counts after one episode = %d/%d, want 3/2", show.EpisodeCount, show.UnwatchedCount)
	}
	listedSeasons, err := catalog.SeasonsForShow(ctx, showID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listedSeasons) != 2 {
		t.Fatalf("seasons = %+v", listedSeasons)
	}
	if listedSeasons[0].EpisodeCount != 2 || listedSeasons[0].UnwatchedCount != 1 {
		t.Fatalf("season one counts = %d/%d, want 2/1",
			listedSeasons[0].EpisodeCount, listedSeasons[0].UnwatchedCount)
	}
	if listedSeasons[1].EpisodeCount != 1 || listedSeasons[1].UnwatchedCount != 1 {
		t.Fatalf("season two counts = %d/%d, want 1/1",
			listedSeasons[1].EpisodeCount, listedSeasons[1].UnwatchedCount)
	}

	// Marking the show played empties both rollups, and an episode of it never
	// claims episodes of its own.
	if _, err := catalog.SetPlayed(ctx, showID); err != nil {
		t.Fatal(err)
	}
	if show, err = catalog.Item(ctx, showID); err != nil {
		t.Fatal(err)
	} else if show.EpisodeCount != 3 || show.UnwatchedCount != 0 {
		t.Fatalf("watched show counts = %d/%d, want 3/0", show.EpisodeCount, show.UnwatchedCount)
	}
	episode, err := catalog.Item(ctx, episodes[0])
	if err != nil {
		t.Fatal(err)
	}
	if episode.EpisodeCount != 0 || episode.UnwatchedCount != 0 {
		t.Fatalf("episode carried rollup counts: %d/%d", episode.EpisodeCount, episode.UnwatchedCount)
	}

	// An episode whose file disappeared drops out of both counts rather than
	// leaving a show that can never be finished.
	if _, err := catalog.ClearPlayback(ctx, episodes[1]); err != nil {
		t.Fatal(err)
	}
	_, scanID, err = catalog.StartScan(ctx, "tv", "/tv")
	if err != nil {
		t.Fatal(err)
	}
	seed(scanID, libraryID, "Show/S01E02")
	if err := catalog.FinishScan(ctx, libraryID, scanID, 2, 0, 0, nil); err != nil {
		t.Fatal(err)
	}
	if show, err = catalog.Item(ctx, showID); err != nil {
		t.Fatal(err)
	} else if show.EpisodeCount != 2 || show.UnwatchedCount != 0 {
		t.Fatalf("counts after a removal = %d/%d, want 2/0", show.EpisodeCount, show.UnwatchedCount)
	}
}
