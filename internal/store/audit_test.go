package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func auditCounts(t *testing.T, report AuditReport) map[string]int {
	t.Helper()
	counts := make(map[string]int, len(report.Findings))
	for _, finding := range report.Findings {
		if _, duplicate := counts[finding.Check]; duplicate {
			t.Fatalf("two findings named %q", finding.Check)
		}
		counts[finding.Check] = finding.Count
	}
	return counts
}

func TestAuditPassesOnACompleteCatalog(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	catalog, err := Open(filepath.Join(dir, "loom.db"))
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
		ItemID: itemID, Path: "/movies/Arrival (2016)/Arrival.mkv", Size: 100, MTimeNS: 20,
		DurationMS: 600_000, Container: "matroska", LastSeenScanID: scanID,
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := catalog.UpdateMetadata(ctx, itemID, MetadataUpdate{
		TMDBID: 329865, Genres: []Genre{{ID: 878, Name: "Science Fiction"}},
		Credits: []Credit{
			{PersonID: 1, Name: "Denis Villeneuve", Role: "director"},
			{PersonID: 2, Name: "Amy Adams", Role: "actor", Character: "Louise Banks"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	poster := filepath.Join(dir, "poster.jpg")
	if err := os.WriteFile(poster, []byte("poster"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertImage(ctx, Image{
		ItemID: itemID, Kind: "poster", Path: poster, SourceURL: "https://example/poster.jpg",
		Provider: "tmdb", ProviderPath: "/poster.jpg", Tag: "poster-tag",
		ContentType: "image/jpeg", Width: 200, Height: 300, UpdatedAt: now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, libraryID, scanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}

	report, err := catalog.Audit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 11 {
		t.Fatalf("schema version = %d, want 11", report.SchemaVersion)
	}
	for _, finding := range report.Findings {
		if finding.Count != 0 {
			t.Errorf("%s matched %d rows: %v", finding.Check, finding.Count, finding.Matches)
		}
	}
	if report.IntegrityProblems() != 0 {
		t.Fatalf("integrity problems = %d, want 0", report.IntegrityProblems())
	}
}

func TestAuditFindsCatalogProblems(t *testing.T) {
	ctx := context.Background()
	catalog, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()

	movies, movieScan, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	// Matched and detailed, but the provider supplied no cast, director, or
	// genres, and the probe reported no duration.
	bare, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: movies, SourceKey: "Bare", Kind: "movie", Title: "Bare", ScanID: movieScan,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertMedia(ctx, MediaFile{
		ItemID: bare, Path: "/movies/Bare/Bare.mkv", Size: 1, MTimeNS: 1, LastSeenScanID: movieScan,
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := catalog.UpdateMetadata(ctx, bare, MetadataUpdate{TMDBID: 1}); err != nil {
		t.Fatal(err)
	}
	// Never matched, and its file has gone missing from the catalog entirely.
	if _, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: movies, SourceKey: "Fileless", Kind: "movie", Title: "Fileless", ScanID: movieScan,
	}); err != nil {
		t.Fatal(err)
	}
	// Matched, but the detail fetch never ran.
	stuck, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: movies, SourceKey: "Stuck", Kind: "movie", Title: "Stuck", ScanID: movieScan,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.db.ExecContext(ctx,
		`UPDATE items SET tmdb_id = 3 WHERE id = ?`, stuck); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertMedia(ctx, MediaFile{
		ItemID: stuck, Path: "/movies/Stuck/Stuck.mkv", Size: 1, MTimeNS: 1,
		DurationMS: 1000, LastSeenScanID: movieScan,
	}, nil, nil); err != nil {
		t.Fatal(err)
	}

	tv, tvScan, err := catalog.StartScan(ctx, "tv", "/tv")
	if err != nil {
		t.Fatal(err)
	}
	show, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: tv, SourceKey: "show:Show", Kind: "show", Title: "Show", ScanID: tvScan,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.UpdateMetadata(ctx, show, MetadataUpdate{TMDBID: 2, Credits: []Credit{
		{PersonID: 10, Name: "Lead", Role: "actor", Character: "Lead"},
	}}); err != nil {
		t.Fatal(err)
	}
	// Directors are movie-only, so one on a show is a defect rather than a gap.
	if _, err := catalog.db.ExecContext(ctx,
		`INSERT INTO people(id, name) VALUES (11, 'Director')`); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.db.ExecContext(ctx,
		`INSERT INTO item_credits(item_id, person_id, role) VALUES (?, 11, 'director')`, show); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertImage(ctx, Image{
		ItemID: show, Kind: "poster", Path: filepath.Join(t.TempDir(), "deleted.jpg"),
		SourceURL: "https://example/poster.jpg", Provider: "tmdb", ProviderPath: "/poster.jpg",
		Tag: "poster-tag", ContentType: "image/jpeg", Width: 200, Height: 300, UpdatedAt: now(),
	}); err != nil {
		t.Fatal(err)
	}
	season, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: tv, ParentID: &show, SourceKey: "show:Show:season:1", Kind: "season",
		Title: "Season 1", SeasonNumber: 1, ScanID: tvScan,
	})
	if err != nil {
		t.Fatal(err)
	}
	episode, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: tv, ParentID: &season, SourceKey: "show:Show:season:1:episode:1-1",
		Kind: "episode", Title: "S01E01", SeasonNumber: 1, EpisodeNumber: 1, ScanID: tvScan,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertMedia(ctx, MediaFile{
		ItemID: episode, Path: "/tv/Show/S01E01.mkv", Size: 1, MTimeNS: 1,
		ProbeError: "ffprobe exited 1", LastSeenScanID: tvScan,
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	// A matched episode beside the unmatched one, which is what makes the
	// season suspect: the numbering ran past the end of the provider's list,
	// so this episode is sitting in a slot that may describe its neighbour.
	matchedEpisode, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: tv, ParentID: &season, SourceKey: "show:Show:season:1:episode:2-2",
		Kind: "episode", Title: "S01E02", SeasonNumber: 1, EpisodeNumber: 2, ScanID: tvScan,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.UpdateMetadata(ctx, matchedEpisode, MetadataUpdate{TMDBID: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertMedia(ctx, MediaFile{
		ItemID: matchedEpisode, Path: "/tv/Show/S01E02.mkv", Size: 1, MTimeNS: 1,
		DurationMS: 1000, LastSeenScanID: tvScan,
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	orphan, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: tv, SourceKey: "show:Show:season:2", Kind: "season",
		Title: "Season 2", SeasonNumber: 2, ScanID: tvScan,
	})
	if err != nil {
		t.Fatal(err)
	}

	report, err := catalog.Audit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counts := auditCounts(t, report)
	want := map[string]int{
		"foreign key violations":                     0,
		"items without a media file":                 1,
		"media probe failures":                       1,
		"media without a duration":                   1,
		"items with a broken parent link":            1,
		"director credits outside movies":            1,
		"artwork files missing from disk":            1,
		"items awaiting a metadata match":            1,
		"matches without loaded details":             1,
		"titles without a cast":                      1,
		"movies without a director":                  1,
		"movies without genres":                      1,
		"episodes of a matched show without a match": 1,
		"seasons whose numbering disagrees with TMDB": 1,
		"titles without a poster":                     3,
	}
	if len(counts) != len(want) {
		t.Fatalf("audit reported %d checks, want %d", len(counts), len(want))
	}
	for check, expected := range want {
		count, ok := counts[check]
		if !ok {
			t.Errorf("audit did not run %q", check)
			continue
		}
		if count != expected {
			t.Errorf("%s = %d, want %d", check, count, expected)
		}
	}
	// A count alone does not say which row to look at, so every match is named
	// and none of them are dropped.
	for _, finding := range report.Findings {
		if finding.Count != len(finding.Matches) {
			t.Errorf("%s matched %d rows but named %d", finding.Check,
				finding.Count, len(finding.Matches))
		}
		if finding.Check == "items with a broken parent link" &&
			!strings.Contains(finding.Matches[0], "Season 2") {
			t.Errorf("broken parent link named %q, want the orphaned season %d",
				finding.Matches[0], orphan)
		}
		// An unmatched episode's own title is the filename placeholder, so the
		// show it belongs to has to come from somewhere else.
		if finding.Check == "episodes of a matched show without a match" &&
			!strings.Contains(finding.Matches[0], "Show S01E01") {
			t.Errorf("unmatched episode named %q, want the show and episode number",
				finding.Matches[0])
		}
		// The point of the season check is that the matched episodes beside an
		// unmatched one are suspect, so both counts have to be reported.
		if finding.Check == "seasons whose numbering disagrees with TMDB" &&
			!strings.Contains(finding.Matches[0], "Show S1 (1 matched, 1 unmatched)") {
			t.Errorf("suspect season named %q, want the show, season, and both counts",
				finding.Matches[0])
		}
	}
	if report.IntegrityProblems() != 6 {
		t.Fatalf("integrity problems = %d, want 6", report.IntegrityProblems())
	}
}
