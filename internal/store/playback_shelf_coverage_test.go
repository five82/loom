package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlaybackShelvesReportMalformedTVRows(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	lib, scan, err := s.StartScan(ctx, "tv", "/tv")
	if err != nil {
		t.Fatal(err)
	}
	show, err := s.UpsertItem(ctx, ItemInput{LibraryID: lib, SourceKey: "show", Kind: "show", Title: "Show", ScanID: scan})
	if err != nil {
		t.Fatal(err)
	}
	season, err := s.UpsertItem(ctx, ItemInput{LibraryID: lib, ParentID: &show, SourceKey: "season", Kind: "season", Title: "Season", SeasonNumber: 1, ScanID: scan})
	if err != nil {
		t.Fatal(err)
	}
	var episodes []int64
	for _, number := range []int{1, 2} {
		id, err := s.UpsertItem(ctx, ItemInput{LibraryID: lib, ParentID: &season, SourceKey: string(rune('0' + number)), Kind: "episode", Title: "Episode", SeasonNumber: 1, EpisodeNumber: number, ScanID: scan})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpsertMedia(ctx, MediaFile{ItemID: id, Path: "/tv/episode" + string(rune('0'+number)), Size: 1, DurationMS: 600000, LastSeenScanID: scan}, nil, nil); err != nil {
			t.Fatal(err)
		}
		episodes = append(episodes, id)
	}
	if err := s.FinishScan(ctx, lib, scan, 2, 2, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPlayed(ctx, episodes[0]); err != nil {
		t.Fatal(err)
	}
	// A completed first episode makes the second eligible for Next Up.
	if _, err := s.db.Exec(`UPDATE items SET year = 'bad' WHERE id = ?`, episodes[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextUp(ctx, 10); err == nil || !strings.Contains(err.Error(), "year") {
		t.Fatalf("next up = %v", err)
	}
	if _, err := s.db.Exec(`UPDATE items SET year = 0 WHERE id = ?`, episodes[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProgress(ctx, episodes[1], 60000, 600000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE items SET year = 'bad' WHERE id = ?`, episodes[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ContinueWatching(ctx, 10); err == nil || !strings.Contains(err.Error(), "year") {
		t.Fatalf("continue watching = %v", err)
	}
	if _, err := s.db.Exec(`UPDATE items SET year = 0 WHERE id = ?`, episodes[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPlayed(ctx, episodes[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE items SET year = 'bad' WHERE id = ?`, show); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecentlyPlayed(ctx, 10); err == nil || !strings.Contains(err.Error(), "year") {
		t.Fatalf("recently played = %v", err)
	}
}

func TestPlaybackShelvesReportGenreLookupFailure(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	lib, scan, err := s.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.UpsertItem(ctx, ItemInput{LibraryID: lib, SourceKey: "movie", Kind: "movie", Title: "Movie", ScanID: scan})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertMedia(ctx, MediaFile{ItemID: id, Path: "/movies/movie.mkv", Size: 1, DurationMS: 600000, LastSeenScanID: scan}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScan(ctx, lib, scan, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPlayed(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE item_genres`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecentlyPlayed(ctx, 10); err == nil || !strings.Contains(err.Error(), "item_genres") {
		t.Fatalf("recently played genres = %v", err)
	}
}
