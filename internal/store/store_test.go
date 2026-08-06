package store

import (
	"context"
	"database/sql"
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
	}})
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
	for id, genres := range map[int64][]Genre{
		arrivalID: {{ID: 18, Name: "Drama"}, {ID: 878, Name: "Science Fiction"}},
		alienID:   {{ID: 878, Name: "Science Fiction"}},
	} {
		if err := catalog.UpdateMetadata(ctx, id, MetadataUpdate{
			TMDBID: id, Genres: genres, GenresLoaded: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.FinishScan(ctx, libraryID, scanID, 2, 2, 0, nil); err != nil {
		t.Fatal(err)
	}

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
	if len(items) != 1 || items[0].ID != arrivalID || len(items[0].Genres) != 2 || !items[0].GenresLoaded {
		t.Fatalf("drama movies = %+v", items)
	}

	if err := catalog.UpdateGenres(ctx, arrivalID, []Genre{{ID: 53, Name: "Thriller"}}); err != nil {
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
		TMDBID: 1, GenresLoaded: true, Genres: []Genre{{ID: 18, Name: "Drama"}},
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

// TestSchemaV6MigratesToV7 exercises the one-shot images-table rebuild.
// Remove together with migrateV6AddThumbImageKind after the live database
// has migrated.
func TestSchemaV6MigratesToV7(t *testing.T) {
	const schemaV6 = `
CREATE TABLE libraries (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL UNIQUE CHECK (kind IN ('movies', 'shorts', 'tv')),
    name TEXT NOT NULL,
    path TEXT NOT NULL,
    last_scan_id INTEGER
);
CREATE TABLE scan_runs (
    id INTEGER PRIMARY KEY,
    library_id INTEGER NOT NULL REFERENCES libraries(id),
    started_at TEXT NOT NULL,
    finished_at TEXT,
    status TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed')),
    discovered_files INTEGER NOT NULL DEFAULT 0,
    changed_files INTEGER NOT NULL DEFAULT 0,
    probe_errors INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE items (
    id INTEGER PRIMARY KEY,
    library_id INTEGER NOT NULL REFERENCES libraries(id),
    parent_id INTEGER REFERENCES items(id),
    source_key TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('movie', 'show', 'season', 'episode', 'unmatched')),
    title TEXT NOT NULL,
    year INTEGER NOT NULL DEFAULT 0,
    season_number INTEGER NOT NULL DEFAULT 0,
    episode_number INTEGER NOT NULL DEFAULT 0,
    episode_end_number INTEGER NOT NULL DEFAULT 0,
    tmdb_id INTEGER NOT NULL DEFAULT 0,
    overview TEXT NOT NULL DEFAULT '',
    release_date TEXT NOT NULL DEFAULT '',
    genres_loaded INTEGER NOT NULL DEFAULT 0,
    available INTEGER NOT NULL DEFAULT 1,
    last_seen_scan_id INTEGER NOT NULL,
    added_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (library_id, source_key)
);
CREATE INDEX items_parent_idx ON items(parent_id, available, title);
CREATE INDEX items_library_idx ON items(library_id, available, kind);
CREATE TABLE genres (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL
);
CREATE TABLE item_genres (
    item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    genre_id INTEGER NOT NULL REFERENCES genres(id),
    PRIMARY KEY (item_id, genre_id)
);
CREATE INDEX item_genres_genre_idx ON item_genres(genre_id, item_id);
CREATE TABLE media_files (
    id INTEGER PRIMARY KEY,
    item_id INTEGER NOT NULL UNIQUE REFERENCES items(id) ON DELETE CASCADE,
    path TEXT NOT NULL UNIQUE,
    size INTEGER NOT NULL,
    mtime_ns INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    container TEXT NOT NULL DEFAULT '',
    probe_error TEXT NOT NULL DEFAULT '',
    last_seen_scan_id INTEGER NOT NULL
);
CREATE TABLE media_streams (
    id INTEGER PRIMARY KEY,
    media_file_id INTEGER NOT NULL REFERENCES media_files(id) ON DELETE CASCADE,
    stream_index INTEGER NOT NULL,
    kind TEXT NOT NULL,
    codec TEXT NOT NULL DEFAULT '',
    profile TEXT NOT NULL DEFAULT '',
    language TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    width INTEGER NOT NULL DEFAULT 0,
    height INTEGER NOT NULL DEFAULT 0,
    channels INTEGER NOT NULL DEFAULT 0,
    channel_layout TEXT NOT NULL DEFAULT '',
    dynamic_range TEXT NOT NULL DEFAULT '',
    is_default INTEGER NOT NULL DEFAULT 0,
    is_forced INTEGER NOT NULL DEFAULT 0,
    UNIQUE (media_file_id, stream_index)
);
CREATE TABLE images (
    id INTEGER PRIMARY KEY,
    item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('poster', 'backdrop', 'logo')),
    path TEXT NOT NULL UNIQUE,
    source_url TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT 'tmdb',
    provider_path TEXT NOT NULL DEFAULT '',
    tag TEXT NOT NULL,
    content_type TEXT NOT NULL,
    width INTEGER NOT NULL DEFAULT 0,
    height INTEGER NOT NULL DEFAULT 0,
    manually_selected INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL,
    UNIQUE (item_id, kind)
);
CREATE TABLE playback_state (
    item_id INTEGER PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
    position_ms INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL,
    played INTEGER NOT NULL,
    updated_at TEXT NOT NULL
);
PRAGMA user_version = 6;
INSERT INTO libraries(kind, name, path) VALUES ('movies', 'Movies', '/movies');
INSERT INTO items(library_id, source_key, kind, title, last_seen_scan_id, added_at, updated_at)
VALUES (1, 'movie', 'movie', 'Movie', 1, '2026-01-01', '2026-01-01');
INSERT INTO images(item_id, kind, path, source_url, provider, provider_path, tag,
    content_type, manually_selected, updated_at)
VALUES (1, 'poster', '/images/1/poster.jpg', 'https://example/poster.jpg', 'tmdb',
    '/poster.jpg', 'poster-tag', 'image/jpeg', 1, '2026-01-01');
`
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "loom.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaV6); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	catalog, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	poster, err := catalog.ItemImage(ctx, 1, "poster")
	if err != nil {
		t.Fatal(err)
	}
	if poster.ProviderPath != "/poster.jpg" || poster.Tag != "poster-tag" || !poster.ManuallySelected {
		t.Fatalf("poster did not survive migration: %+v", poster)
	}
	if _, err := catalog.UpsertImage(ctx, Image{
		ItemID: 1, Kind: "thumb", Path: "/images/1/thumb.jpg", SourceURL: "https://example/thumb.jpg",
		Provider: "tmdb", ProviderPath: "/thumb.jpg", Tag: "thumb-tag",
		ContentType: "image/jpeg", UpdatedAt: now(),
	}); err != nil {
		t.Fatalf("thumb kind rejected after migration: %v", err)
	}
	var version int
	if err := catalog.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 7 {
		t.Fatalf("schema version after migration = %d, want 7", version)
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
