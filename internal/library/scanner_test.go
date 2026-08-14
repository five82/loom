package library

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/five82/loom/internal/store"
)

type fakeProber struct {
	paths []string
}

func (p *fakeProber) Probe(_ context.Context, path string) (ProbeResult, error) {
	p.paths = append(p.paths, path)
	return ProbeResult{DurationMS: 600_000, Container: "matroska", Streams: []store.Stream{{Index: 0, Kind: "video", Codec: "av1"}}}, nil
}

func TestParseNamedIdentity(t *testing.T) {
	tests := []struct {
		name      string
		wantTitle string
		wantYear  int
		wantTMDB  int64
	}{
		{"Arrival (2016) [tmdbid-329865]", "Arrival", 2016, 329865},
		{"The Office (US) (2005) [tmdbid-2316]", "The Office (US)", 2005, 2316},
		{"Presto (2008)", "Presto", 2008, 0},
		{"Home Video", "Home Video", 0, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			title, year, tmdbID := parseNamedIdentity(test.name)
			if title != test.wantTitle || year != test.wantYear || tmdbID != test.wantTMDB {
				t.Fatalf("parse = %q, %d, %d; want %q, %d, %d",
					title, year, tmdbID, test.wantTitle, test.wantYear, test.wantTMDB)
			}
		})
	}
}

func TestParseEpisodeFilename(t *testing.T) {
	tests := []struct {
		name               string
		season, start, end int
		ok                 bool
	}{
		{"Show - S01E02.mkv", 1, 2, 2, true},
		{"Show - s04e01-02 - Title.mkv", 4, 1, 2, true},
		{"looney tunes s1946e15.mkv", 1946, 15, 15, true},
		{"Stephen King's IT (1990).mkv", 0, 0, 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parseEpisodeFilename(test.name)
			if ok != test.ok || got.Season != test.season || got.Start != test.start || got.End != test.end {
				t.Fatalf("parse = %+v, %v; want season=%d start=%d end=%d ok=%v",
					got, ok, test.season, test.start, test.end, test.ok)
			}
		})
	}
}

func TestScannerUsesMovieRootFilesAndRecursiveTVEpisodes(t *testing.T) {
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	shorts := filepath.Join(root, "shorts")
	tv := filepath.Join(root, "tv")
	writeTestFile(t, filepath.Join(movies, "Arrival (2016) [tmdbid-329865]", "Arrival (2016) [tmdbid-329865].mkv"))
	writeTestFile(t, filepath.Join(shorts, "Presto (2008) [tmdbid-13042]", "Presto (2008) [tmdbid-13042].mkv"))
	writeTestFile(t, filepath.Join(movies, "Arrival (2016) [tmdbid-329865]", "extras", "Bonus.mkv"))
	writeTestFile(t, filepath.Join(tv, "The Office (US) (2005) [tmdbid-2316]", "Season 4", "The Office (US) - S04E01-02 - Fun Run.mkv"))
	writeTestFile(t, filepath.Join(tv, "Stephen King's IT (1990)", "Stephen King's IT (1990).mkv"))

	catalog, err := store.Open(filepath.Join(root, "state", "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	prober := &fakeProber{}
	scanner := NewScanner(catalog, prober, nil, movies, shorts, tv, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := scanner.Scan(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if len(prober.paths) != 4 {
		t.Fatalf("probed %d files, want 4; paths=%v", len(prober.paths), prober.paths)
	}
	for _, path := range prober.paths {
		if filepath.Base(path) == "Bonus.mkv" {
			t.Fatal("movie extra was probed")
		}
	}

	stats, err := catalog.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Movies != 1 || stats.Shorts != 1 || stats.Shows != 2 || stats.Episodes != 1 || stats.Unmatched != 2 || stats.Media != 4 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	libraries, err := catalog.Libraries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(libraries) != 3 || libraries[0].Kind != "movies" ||
		libraries[1].Kind != "shorts" || libraries[1].Name != "Short Films" ||
		libraries[2].Kind != "tv" {
		t.Fatalf("libraries = %+v", libraries)
	}

	shortItems, err := catalog.ListItems(context.Background(), store.ListOptions{LibraryKind: "shorts", TopLevel: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(shortItems) != 1 || shortItems[0].Kind != "movie" || shortItems[0].Title != "Presto" ||
		shortItems[0].Year != 2008 || shortItems[0].TMDBID != 13042 {
		t.Fatalf("short items = %+v", shortItems)
	}

	shows, err := catalog.ListItems(context.Background(), store.ListOptions{LibraryKind: "tv", TopLevel: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(shows) != 2 {
		t.Fatalf("shows = %d, want 2", len(shows))
	}
	var officeID int64
	for _, show := range shows {
		if show.Title == "The Office (US)" && show.Year == 2005 && show.TMDBID == 2316 {
			officeID = show.ID
		}
	}
	if officeID == 0 {
		t.Fatalf("standard TV name did not seed TMDB identity: %+v", shows)
	}
	seasons, err := catalog.ListItems(context.Background(), store.ListOptions{ParentID: &officeID})
	if err != nil {
		t.Fatal(err)
	}
	if len(seasons) != 1 || seasons[0].SeasonNumber != 4 {
		t.Fatalf("unexpected seasons: %+v", seasons)
	}
	episodes, err := catalog.ListItems(context.Background(), store.ListOptions{ParentID: &seasons[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 1 || episodes[0].EpisodeNumber != 1 || episodes[0].EpisodeEndNumber != 2 {
		t.Fatalf("unexpected episodes: %+v", episodes)
	}
}

func TestScannerPreservesMovieStateAcrossStandardRename(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	oldDir := filepath.Join(movies, "Arrival (2016)")
	oldVideo := filepath.Join(oldDir, "Arrival (2016).mkv")
	writeTestFile(t, oldVideo)

	catalog, err := store.Open(filepath.Join(root, "state", "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	scanner := NewScanner(catalog, &fakeProber{}, nil, movies,
		filepath.Join(root, "shorts"), filepath.Join(root, "tv"),
		slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := scanner.Scan(ctx, "movies"); err != nil {
		t.Fatal(err)
	}
	items, err := catalog.ListItems(ctx, store.ListOptions{LibraryKind: "movies", TopLevel: true})
	if err != nil || len(items) != 1 {
		t.Fatalf("initial movies = %+v, %v", items, err)
	}
	beforeID := items[0].ID
	if err := catalog.UpdateMetadata(ctx, beforeID, store.MetadataUpdate{
		TMDBID: 329865, Title: "Arrival", Year: 2016,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.SetProgress(ctx, beforeID, 120_000, 600_000); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertImage(ctx, store.Image{
		ItemID: beforeID, Kind: "poster", Path: filepath.Join(root, "poster.jpg"),
		SourceURL: "https://image.test/poster.jpg", Provider: "tmdb",
		ProviderPath: "/manual.jpg", Tag: "manual", ContentType: "image/jpeg",
		ManuallySelected: true, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}

	newDir := filepath.Join(movies, "Arrival (2016) [tmdbid-329865]")
	if err := os.Rename(oldDir, newDir); err != nil {
		t.Fatal(err)
	}
	newVideo := filepath.Join(newDir, "Arrival (2016) [tmdbid-329865].mkv")
	if err := os.Rename(filepath.Join(newDir, filepath.Base(oldVideo)), newVideo); err != nil {
		t.Fatal(err)
	}
	if err := scanner.Scan(ctx, "movies"); err != nil {
		t.Fatal(err)
	}

	items, err = catalog.ListItems(ctx, store.ListOptions{LibraryKind: "movies", TopLevel: true})
	if err != nil || len(items) != 1 || items[0].ID != beforeID {
		t.Fatalf("renamed movies = %+v, %v; want item %d", items, err, beforeID)
	}
	after, err := catalog.Item(ctx, beforeID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Progress == nil || after.Progress.ResumePositionMS != 120_000 {
		t.Fatalf("rename lost playback state: %+v", after.Progress)
	}
	poster, err := catalog.ItemImage(ctx, beforeID, "poster")
	if err != nil || !poster.ManuallySelected || poster.ProviderPath != "/manual.jpg" {
		t.Fatalf("rename lost manual poster: %+v, %v", poster, err)
	}
	if after.Media == nil || after.Media.Path != newVideo {
		t.Fatalf("rename did not update media path: %+v", after.Media)
	}
}

func TestScannerPreservesTVStateAcrossStandardRename(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	oldShowDir := filepath.Join(root, "tv", "The Office")
	oldVideo := filepath.Join(oldShowDir, "Season 01", "The Office - S01E01.mkv")
	writeTestFile(t, oldVideo)
	catalog, scanner := newTVScanner(t, root)
	if err := scanner.Scan(ctx, "tv"); err != nil {
		t.Fatal(err)
	}
	shows, err := catalog.ListItems(ctx, store.ListOptions{LibraryKind: "tv", TopLevel: true})
	if err != nil || len(shows) != 1 {
		t.Fatalf("initial shows = %+v, %v", shows, err)
	}
	showID := shows[0].ID
	if err := catalog.UpdateMetadata(ctx, showID, store.MetadataUpdate{
		TMDBID: 2316, Title: "The Office", Year: 2005,
	}); err != nil {
		t.Fatal(err)
	}
	seasons, err := catalog.ListItems(ctx, store.ListOptions{ParentID: &showID})
	if err != nil || len(seasons) != 1 {
		t.Fatalf("initial seasons = %+v, %v", seasons, err)
	}
	seasonID := seasons[0].ID
	episodes := tvEpisodes(t, catalog)
	if len(episodes) != 1 {
		t.Fatalf("initial episodes = %+v", episodes)
	}
	episodeID := episodes[0].ID
	if _, err := catalog.SetProgress(ctx, episodeID, 120_000, 600_000); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertImage(ctx, store.Image{
		ItemID: seasonID, Kind: "poster", Path: filepath.Join(root, "season-poster.jpg"),
		SourceURL: "https://image.test/season.jpg", Provider: "tmdb",
		ProviderPath: "/manual-season.jpg", Tag: "manual", ContentType: "image/jpeg",
		ManuallySelected: true, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}

	newShowDir := filepath.Join(root, "tv", "The Office (2005) [tmdbid-2316]")
	if err := os.Rename(oldShowDir, newShowDir); err != nil {
		t.Fatal(err)
	}
	newVideo := filepath.Join(newShowDir, "Season 01", "The Office - S01E01 - Pilot.mkv")
	if err := os.Rename(filepath.Join(newShowDir, "Season 01", filepath.Base(oldVideo)), newVideo); err != nil {
		t.Fatal(err)
	}
	if err := scanner.Scan(ctx, "tv"); err != nil {
		t.Fatal(err)
	}

	shows, err = catalog.ListItems(ctx, store.ListOptions{LibraryKind: "tv", TopLevel: true})
	if err != nil || len(shows) != 1 || shows[0].ID != showID {
		t.Fatalf("renamed shows = %+v, %v; want item %d", shows, err, showID)
	}
	seasons, err = catalog.ListItems(ctx, store.ListOptions{ParentID: &showID})
	if err != nil || len(seasons) != 1 || seasons[0].ID != seasonID {
		t.Fatalf("renamed seasons = %+v, %v; want item %d", seasons, err, seasonID)
	}
	episodes = tvEpisodes(t, catalog)
	if len(episodes) != 1 || episodes[0].ID != episodeID {
		t.Fatalf("renamed episodes = %+v; want item %d", episodes, episodeID)
	}
	after, err := catalog.Item(ctx, episodeID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Progress == nil || after.Progress.ResumePositionMS != 120_000 ||
		after.Media == nil || after.Media.Path != newVideo {
		t.Fatalf("rename lost episode state: %+v", after)
	}
	poster, err := catalog.ItemImage(ctx, seasonID, "poster")
	if err != nil || !poster.ManuallySelected || poster.ProviderPath != "/manual-season.jpg" {
		t.Fatalf("rename lost manual season poster: %+v, %v", poster, err)
	}
}

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	writeTestFileContent(t, path, "video")
}

func writeTestFileContent(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setModTime pins a file's timestamp relative to a fixed base so tests that turn
// on which file is newest never depend on how fast they wrote the files.
func setModTime(t *testing.T, path string, offset time.Duration) {
	t.Helper()
	stamp := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC).Add(offset)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

// newTVScanner prepares a scanner over a TV library alone. Movie and short film
// roots are never read because these tests only scan "tv".
func newTVScanner(t *testing.T, root string) (*store.Store, *Scanner) {
	t.Helper()
	catalog, err := store.Open(filepath.Join(root, "state", "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	scanner := NewScanner(catalog, &fakeProber{}, nil,
		filepath.Join(root, "movies"), filepath.Join(root, "shorts"), filepath.Join(root, "tv"),
		slog.New(slog.NewTextHandler(os.Stderr, nil)))
	return catalog, scanner
}

// tvEpisodes returns the episodes of the one show a test staged.
func tvEpisodes(t *testing.T, catalog *store.Store) []store.Item {
	t.Helper()
	ctx := context.Background()
	shows, err := catalog.ListItems(ctx, store.ListOptions{LibraryKind: "tv", TopLevel: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(shows) != 1 {
		t.Fatalf("shows = %d, want 1", len(shows))
	}
	seasons, err := catalog.ListItems(ctx, store.ListOptions{ParentID: &shows[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(seasons) != 1 {
		t.Fatalf("seasons = %d, want 1", len(seasons))
	}
	episodes, err := catalog.ListItems(ctx, store.ListOptions{ParentID: &seasons[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	return episodes
}

func TestEpisodeSurvivesReplacementEncode(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	season := filepath.Join(root, "tv", "The Office (US)", "Season 4")
	original := filepath.Join(season, "The Office (US) - S04E01 - Fun Run.mkv")
	writeTestFile(t, original)
	catalog, scanner := newTVScanner(t, root)
	if err := scanner.Scan(ctx, "tv"); err != nil {
		t.Fatal(err)
	}
	episodes := tvEpisodes(t, catalog)
	if len(episodes) != 1 {
		t.Fatalf("episodes = %+v, want 1", episodes)
	}
	before := episodes[0]
	if _, err := catalog.SetProgress(ctx, before.ID, 120_000, 600_000); err != nil {
		t.Fatal(err)
	}

	// Swap the h264 encode for an AV1 one under a new filename, the way a
	// re-encode pipeline normally names its output.
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(season, "The Office (US) - S04E01 - Fun Run [AV1 2160p].mkv")
	writeTestFileContent(t, replacement, "a considerably larger av1 encode")
	if err := scanner.Scan(ctx, "tv"); err != nil {
		t.Fatal(err)
	}

	episodes = tvEpisodes(t, catalog)
	if len(episodes) != 1 {
		t.Fatalf("episodes after replacement = %+v, want 1", episodes)
	}
	after := episodes[0]
	if after.ID != before.ID {
		t.Fatalf("replacement encode created a new episode: %d became %d", before.ID, after.ID)
	}
	if after.MediaTag == "" || after.MediaTag == before.MediaTag {
		t.Fatalf("media version did not change: %q", after.MediaTag)
	}
	item, err := catalog.Item(ctx, after.ID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Progress == nil || item.Progress.ResumePositionMS != 120_000 {
		t.Fatalf("replacement encode lost playback state: %+v", item.Progress)
	}
	if item.Media == nil || filepath.Base(item.Media.Path) != filepath.Base(replacement) {
		t.Fatalf("catalog still points at the replaced file: %+v", item.Media)
	}
}

// Removing the duplicate hands the surviving item a file that another item still
// claims in the catalog. The episode has to follow the file that remains rather
// than keep pointing at the deleted one.
func TestSurvivingEpisodeAdoptsRemainingDuplicateFile(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	season := filepath.Join(root, "tv", "Robot Chicken", "Season 10")
	newer := filepath.Join(season, "Robot Chicken - S10E17 - Hemorrhoids HDTV-1080p.mkv")
	older := filepath.Join(season, "Robot Chicken - S10E17 - Hemorrhoids WEBDL-1080p.mkv")
	writeTestFileContent(t, newer, "the file in use")
	writeTestFileContent(t, older, "the leftover duplicate")
	setModTime(t, newer, 0)
	setModTime(t, older, -2*time.Hour)
	catalog, scanner := newTVScanner(t, root)
	if err := scanner.Scan(ctx, "tv"); err != nil {
		t.Fatal(err)
	}
	episodes := tvEpisodes(t, catalog)
	if len(episodes) != 1 {
		t.Fatalf("episodes = %+v, want 1", episodes)
	}
	kept, err := catalog.Item(ctx, episodes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Media == nil || kept.Media.Path != newer {
		t.Fatalf("expected the newest file to win, got %+v", kept.Media)
	}
	if _, err := catalog.SetProgress(ctx, kept.ID, 120_000, 600_000); err != nil {
		t.Fatal(err)
	}

	// Reproduce a catalog written before episodes were keyed by number, where the
	// duplicate file has its own item and, crucially, its own media row holding
	// the path the surviving episode is about to inherit.
	libraryID, scanID, err := catalog.StartScan(ctx, "tv", filepath.Join(root, "tv"))
	if err != nil {
		t.Fatal(err)
	}
	legacyID, err := catalog.UpsertItem(ctx, store.ItemInput{
		LibraryID: libraryID, ParentID: kept.ParentID,
		SourceKey: "file:Robot Chicken/Season 10/" + filepath.Base(older),
		Kind:      "episode", Title: "Episode 17", SeasonNumber: 10,
		EpisodeNumber: 17, EpisodeEndNumber: 17, ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(older)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertMedia(ctx, store.MediaFile{
		ItemID: legacyID, Path: older, Size: info.Size(), MTimeNS: info.ModTime().UnixNano(),
		DurationMS: 600_000, LastSeenScanID: scanID,
	}, nil, nil); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(newer); err != nil {
		t.Fatal(err)
	}
	if err := scanner.Scan(ctx, "tv"); err != nil {
		t.Fatal(err)
	}
	episodes = tvEpisodes(t, catalog)
	if len(episodes) != 1 {
		t.Fatalf("episodes after cleanup = %+v, want 1", episodes)
	}
	after, err := catalog.Item(ctx, episodes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != kept.ID {
		t.Fatalf("cleanup created a new episode: %d became %d", kept.ID, after.ID)
	}
	if after.Media == nil || after.Media.Path != older {
		t.Fatalf("episode still points at the deleted file: %+v", after.Media)
	}
	if after.Progress == nil || after.Progress.ResumePositionMS != 120_000 {
		t.Fatalf("cleanup lost playback state: %+v", after.Progress)
	}
}

func TestDuplicateEpisodeFilesResolveToOneEpisode(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	season := filepath.Join(root, "tv", "The Office (US)", "Season 4")
	replaced := filepath.Join(season, "The Office (US) - S04E01 - Fun Run.mkv")
	replacement := filepath.Join(season, "The Office (US) - S04E01 - Fun Run [AV1].mkv")
	writeTestFile(t, replaced)
	writeTestFileContent(t, replacement, "replacement")
	setModTime(t, replaced, -2*time.Hour)
	setModTime(t, replacement, 0)
	catalog, scanner := newTVScanner(t, root)
	if err := scanner.Scan(ctx, "tv"); err != nil {
		t.Fatal(err)
	}
	episodes := tvEpisodes(t, catalog)
	if len(episodes) != 1 {
		t.Fatalf("episodes = %+v, want 1", episodes)
	}
	first, err := catalog.Item(ctx, episodes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Media == nil || first.Media.Path != replacement {
		t.Fatalf("expected the newest file to win, got %+v", first.Media)
	}

	// The choice must not alternate between scans while both files are present.
	if err := scanner.Scan(ctx, "tv"); err != nil {
		t.Fatal(err)
	}
	episodes = tvEpisodes(t, catalog)
	if len(episodes) != 1 {
		t.Fatalf("episodes after rescan = %+v, want 1", episodes)
	}
	second, err := catalog.Item(ctx, episodes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Media == nil || second.Media == nil || first.Media.Path != second.Media.Path {
		t.Fatalf("episode file flipped between scans: %+v then %+v", first.Media, second.Media)
	}
}

// A movie stays in the catalog while a replacement encode and the file it
// replaces briefly share its directory.
func TestMovieSurvivesSwapWindow(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	dir := filepath.Join(movies, "Arrival (2016)")
	old := filepath.Join(dir, "Arrival (2016).mkv")
	replacement := filepath.Join(dir, "Arrival (2016) [AV1 2160p].mkv")
	writeTestFile(t, old)
	catalog, err := store.Open(filepath.Join(root, "state", "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	scanner := NewScanner(catalog, &fakeProber{}, nil, movies,
		filepath.Join(root, "shorts"), filepath.Join(root, "tv"),
		slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := scanner.Scan(ctx, "movies"); err != nil {
		t.Fatal(err)
	}
	listed, err := catalog.ListItems(ctx, store.ListOptions{LibraryKind: "movies", TopLevel: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("movies = %+v, want 1", listed)
	}
	movieID := listed[0].ID
	if _, err := catalog.SetProgress(ctx, movieID, 120_000, 600_000); err != nil {
		t.Fatal(err)
	}

	// The new encode lands before the old file is deleted.
	writeTestFileContent(t, replacement, "a considerably larger av1 encode")
	setModTime(t, old, -2*time.Hour)
	setModTime(t, replacement, 0)
	if err := scanner.Scan(ctx, "movies"); err != nil {
		t.Fatal(err)
	}
	listed, err = catalog.ListItems(ctx, store.ListOptions{LibraryKind: "movies", TopLevel: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != movieID {
		t.Fatalf("movie left the catalog during the swap: %+v", listed)
	}
	during, err := catalog.Item(ctx, movieID)
	if err != nil {
		t.Fatal(err)
	}
	if during.Media == nil || during.Media.Path != replacement {
		t.Fatalf("swap window did not serve the newest file: %+v", during.Media)
	}
	if during.Progress == nil || during.Progress.ResumePositionMS != 120_000 {
		t.Fatalf("swap window lost playback state: %+v", during.Progress)
	}

	// Finishing the swap must not disturb the item any further.
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	if err := scanner.Scan(ctx, "movies"); err != nil {
		t.Fatal(err)
	}
	after, err := catalog.Item(ctx, movieID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Media == nil || after.Media.Path != replacement || after.Media.Tag != during.Media.Tag {
		t.Fatalf("completing the swap churned the media file: %+v", after.Media)
	}
}

func TestParseProbeOutput(t *testing.T) {
	result, err := parseProbeOutput([]byte(`{
  "streams": [
    {"index":0,"codec_name":"hevc","codec_type":"video","profile":"Main 10","width":3840,"height":1604,"color_transfer":"smpte2084","disposition":{"default":1}},
    {"index":1,"codec_name":"opus","codec_type":"audio","channels":8,"channel_layout":"7.1","tags":{"language":"eng","title":"Main"}},
    {"index":2,"codec_name":"subrip","codec_type":"subtitle","tags":{"language":"eng"}},
    {"index":3,"codec_name":"mjpeg","codec_type":"video","width":600,"height":900,"disposition":{"attached_pic":1}}
  ],
  "chapters": [
    {"start_time":"0.000000","tags":{"title":"Opening"}},
    {"start_time":"264.264000","tags":{"title":"Chapter 02"}},
    {"start_time":"512.500000"}
  ],
  "format":{"format_name":"matroska,webm","duration":"600.125"}
}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.DurationMS != 600_125 || result.Container != "matroska,webm" || len(result.Streams) != 3 {
		t.Fatalf("unexpected probe result: %+v", result)
	}
	if !result.Streams[0].IsDefault || result.Streams[0].Profile != "Main 10" ||
		result.Streams[0].DynamicRange != "hdr" || result.Streams[1].ChannelLayout != "7.1" ||
		result.Streams[2].Codec != "subrip" {
		t.Fatalf("unexpected streams: %+v", result.Streams)
	}
	want := []store.Chapter{
		{Index: 0, StartMS: 0, Title: "Opening"},
		{Index: 1, StartMS: 264_264, Title: "Chapter 02"},
		{Index: 2, StartMS: 512_500},
	}
	if !reflect.DeepEqual(result.Chapters, want) {
		t.Fatalf("chapters = %+v, want %+v", result.Chapters, want)
	}
}

func TestParseProbeOutputIgnoresUnnavigableChapters(t *testing.T) {
	tests := []struct {
		name, chapters string
	}{
		{name: "no chapters", chapters: ""},
		{name: "single chapter spanning the file", chapters: `"chapters":[{"start_time":"0.000000"}],`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := parseProbeOutput([]byte(`{` + test.chapters +
				`"format":{"format_name":"matroska","duration":"600.125"}}`))
			if err != nil {
				t.Fatal(err)
			}
			if result.Chapters != nil {
				t.Fatalf("chapters = %+v, want none", result.Chapters)
			}
		})
	}
}

func TestDynamicRange(t *testing.T) {
	tests := []struct {
		name, codecTag, transfer, sideData, want string
	}{
		{name: "SDR", want: "sdr"},
		{name: "HDR10", transfer: "smpte2084", want: "hdr"},
		{name: "HLG", transfer: "arib-std-b67", want: "hdr"},
		{name: "Dolby Vision codec tag", codecTag: "dvh1", want: "dolby_vision"},
		{name: "Dolby Vision side data", transfer: "smpte2084", sideData: "DOVI configuration record", want: "dolby_vision"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var sideData []probeSideData
			if test.sideData != "" {
				sideData = []probeSideData{{Type: test.sideData}}
			}
			if got := dynamicRange(test.codecTag, test.transfer, sideData); got != test.want {
				t.Fatalf("dynamicRange() = %q, want %q", got, test.want)
			}
		})
	}
}
