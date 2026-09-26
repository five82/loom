package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestChannelPoolsFilterAndOrderAirableItems(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	add := func(library string, input ItemInput, duration int64) int64 {
		t.Helper()
		lib, scan, err := s.StartScan(ctx, library, "/"+library)
		if err != nil {
			t.Fatal(err)
		}
		input.LibraryID, input.ScanID = lib, scan
		id, err := s.UpsertItem(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if duration != 0 {
			_, err = s.UpsertMedia(ctx, MediaFile{ItemID: id, Path: "/" + library + "/" + input.SourceKey, DurationMS: duration, LastSeenScanID: scan}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	movie := add("movies", ItemInput{SourceKey: "one", Kind: "movie", Title: "One", Year: 2020, TMDBID: 11}, 1000)
	other := add("movies", ItemInput{SourceKey: "two", Kind: "movie", Title: "Two", Year: 2021, TMDBID: 22}, 2000)
	short := add("shorts", ItemInput{SourceKey: "short", Kind: "movie", Title: "Short", TMDBID: 33}, 500)
	_ = add("movies", ItemInput{SourceKey: "no-file", Kind: "movie", Title: "No file", TMDBID: 44}, 0)
	_ = add("movies", ItemInput{SourceKey: "zero", Kind: "movie", Title: "Zero", TMDBID: 55}, -1)
	for _, query := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE items SET content_rating = 'PG', vote_average = 8 WHERE id = ?`, []any{movie}},
		{`UPDATE items SET content_rating = 'R', vote_average = 6 WHERE id = ?`, []any{other}},
		{`INSERT INTO genres(name) VALUES ('Drama')`, nil},
		{`INSERT INTO item_genres(item_id, genre_id) VALUES (?, 1)`, []any{movie}},
	} {
		if _, err := s.db.ExecContext(ctx, query.sql, query.args...); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.ChannelTitles(ctx, []int64{22, 44, 11, 22, 55}); err != nil || !reflect.DeepEqual(got, []ChannelItem{{other, 2000}, {movie, 1000}, {other, 2000}}) {
		t.Fatalf("titles = %v, %v", got, err)
	}
	if got, err := s.ChannelTitles(ctx, nil); err != nil || got != nil {
		t.Fatalf("empty titles = %v, %v", got, err)
	}
	if got, err := s.ChannelMovies(ctx, MovieFilter{}); err != nil || got != nil {
		t.Fatalf("empty filter = %v, %v", got, err)
	}
	for _, filter := range []MovieFilter{
		{Genres: []string{"Comedy", "Drama"}, Ratings: []string{"PG"}, Years: [2]int{2019, 2020}, MinVote: 7},
		{MinVote: 7},
	} {
		if got, err := s.ChannelMovies(ctx, filter); err != nil || !reflect.DeepEqual(got, []ChannelItem{{movie, 1000}}) {
			t.Fatalf("movies for %+v = %v, %v", filter, got, err)
		}
	}
	if got, err := s.ChannelShorts(ctx); err != nil || !reflect.DeepEqual(got, []ChannelItem{{short, 500}}) {
		t.Fatalf("shorts = %v, %v", got, err)
	}

	show := add("tv", ItemInput{SourceKey: "show", Kind: "show", Title: "Show", TMDBID: 99}, 0)
	season := add("tv", ItemInput{SourceKey: "season", Kind: "season", Title: "Season", ParentID: &show}, 0)
	special := add("tv", ItemInput{SourceKey: "special", Kind: "episode", Title: "Special", ParentID: &season, SeasonNumber: 0, EpisodeNumber: 1}, 400)
	episode := add("tv", ItemInput{SourceKey: "episode", Kind: "episode", Title: "Episode", ParentID: &season, SeasonNumber: 1, EpisodeNumber: 1}, 900)
	if got, err := s.ChannelShowEpisodes(ctx, 99); err != nil || !reflect.DeepEqual(got, []ChannelItem{{episode, 900}, {special, 400}}) {
		t.Fatalf("episodes = %v, %v", got, err)
	}
}

func TestChannelSchedulePersistenceAndWindow(t *testing.T) {
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
	item, err := s.UpsertItem(ctx, ItemInput{LibraryID: lib, ScanID: scan, SourceKey: "film", Kind: "movie", Title: "Film"})
	if err != nil {
		t.Fatal(err)
	}
	media, err := s.UpsertMedia(ctx, MediaFile{ItemID: item, Path: "/movies/film", DurationMS: 1000, LastSeenScanID: scan}, []Stream{{Index: 0, Kind: "video", Codec: "h264", Width: 1920, Height: 1080}, {Index: 1, Kind: "video", Codec: "hevc"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.CreateChannel(ctx, "first", "First", "2025-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateChannel(ctx, "second", "Second", "2025-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if first.Number != 1 || second.Number != 2 {
		t.Fatalf("numbers: %d %d", first.Number, second.Number)
	}
	if err := s.RenameChannel(ctx, first.ID, "Renamed"); err != nil {
		t.Fatal(err)
	}
	if id, end, err := s.ChannelLastProgram(ctx, first.ID); err != nil || id != 0 || end != "" {
		t.Fatalf("empty last = %d %q %v", id, end, err)
	}
	if reach, err := s.ChannelScheduleReach(ctx); err != nil || reach != "" {
		t.Fatalf("empty reach = %q %v", reach, err)
	}

	start := "2025-01-01T00:00:00Z"
	mid := "2025-01-01T01:00:00Z"
	end := "2025-01-01T02:00:00Z"
	cursor := ChannelCursor{Source: "movies", Cycle: 1, CycleStartedAt: start, ItemID: item}
	if err := s.AppendChannelPrograms(ctx, first.ID, []ScheduledProgram{{item, start, mid}, {item, mid, end}}, []ChannelCursor{cursor}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChannelPrograms(ctx, second.ID, []ScheduledProgram{{item, start, mid}}, nil); err != nil {
		t.Fatal(err)
	}
	// A bad program must roll back the valid program written earlier in the same call.
	if err := s.AppendChannelPrograms(ctx, second.ID, []ScheduledProgram{{item, mid, end}, {0, end, end}}, nil); err == nil {
		t.Fatal("accepted missing item")
	}
	if got, err := s.ChannelAired(ctx, second.ID); err != nil || !reflect.DeepEqual(got, []AiredProgram{{item, start}}) {
		t.Fatalf("schedule after rollback = %v, %v", got, err)
	}
	cursor.Cycle = 2
	if err := s.AppendChannelPrograms(ctx, first.ID, nil, []ChannelCursor{cursor}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ChannelCursors(ctx, first.ID); err != nil || !reflect.DeepEqual(got, []ChannelCursor{cursor}) {
		t.Fatalf("cursors = %v, %v", got, err)
	}
	if got, err := s.ChannelAired(ctx, first.ID); err != nil || !reflect.DeepEqual(got, []AiredProgram{{item, start}, {item, mid}}) {
		t.Fatalf("aired = %v, %v", got, err)
	}
	if id, last, err := s.ChannelLastProgram(ctx, first.ID); err != nil || id != item || last != end {
		t.Fatalf("last = %d %q %v", id, last, err)
	}
	if reach, err := s.ChannelScheduleReach(ctx); err != nil || reach != mid {
		t.Fatalf("reach = %q %v", reach, err)
	}
	channels, programs, err := s.ChannelLineup(ctx, mid, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 2 || channels[0].Name != "Renamed" || len(programs[first.ID]) != 1 || len(programs[second.ID]) != 0 {
		t.Fatalf("window: channels=%v programs=%v", channels, programs)
	}
	got := programs[first.ID][0]
	if got.Item.ID != item || got.MediaID != media || got.MediaPath != "/movies/film" || got.Video == nil || got.Video.Codec != "h264" || got.Video.Resolution != "1080p" {
		t.Fatalf("program = %+v", got)
	}
	if removed, err := s.PruneChannelPrograms(ctx, mid); err != nil || removed != 0 {
		t.Fatalf("pruned at boundary = %d, %v", removed, err)
	}
	if removed, err := s.PruneChannelPrograms(ctx, end); err != nil || removed != 2 {
		t.Fatalf("pruned = %d, %v", removed, err)
	}
	if err := s.DeleteChannel(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	third, err := s.CreateChannel(ctx, "third", "Third", end)
	if err != nil || third.Number != 3 {
		t.Fatalf("third = %+v, %v", third, err)
	}
	if got, err := s.ChannelCursors(ctx, first.ID); err != nil || len(got) != 0 {
		t.Fatalf("deleted cursors = %v, %v", got, err)
	}
}

func TestChannelTimeRoundTrip(t *testing.T) {
	at := time.Date(2025, 1, 2, 3, 4, 5, 123, time.FixedZone("offset", 3600))
	got, err := ParseChannelTime(ChannelTime(at))
	if err != nil || !got.Equal(at.Truncate(time.Second)) || got.Location() != time.UTC {
		t.Fatalf("parsed = %v, %v", got, err)
	}
	if _, err := ParseChannelTime("invalid"); err == nil {
		t.Fatal("accepted invalid time")
	}
}
