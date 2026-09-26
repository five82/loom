package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChannelQueriesRejectMalformedStoredRows(t *testing.T) {
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
	id, err := s.UpsertItem(ctx, ItemInput{LibraryID: lib, SourceKey: "movie", Kind: "movie", Title: "Movie", TMDBID: 42, ScanID: scan})
	if err != nil {
		t.Fatal(err)
	}
	mediaID, err := s.UpsertMedia(ctx, MediaFile{ItemID: id, Path: "/movies/movie.mkv", Size: 1, DurationMS: 60000, LastSeenScanID: scan}, []Stream{{Index: 0, Kind: "video", Codec: "h264"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	channel, err := s.CreateChannel(ctx, "films", "Films", ChannelTime(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	start, end := ChannelTime(time.Now()), ChannelTime(time.Now().Add(time.Hour))
	if err := s.AppendChannelPrograms(ctx, channel.ID, []ScheduledProgram{{ItemID: id, StartsAt: start, EndsAt: end}}, []ChannelCursor{{Source: "films", ItemID: id}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, corrupt string
		run           func() error
	}{
		{"titles", `UPDATE media_files SET duration_ms = 'bad' WHERE id = ?`, func() error { _, err := s.ChannelTitles(ctx, []int64{42}); return err }},
		{"movies", `UPDATE media_files SET duration_ms = 'bad' WHERE id = ?`, func() error { _, err := s.ChannelMovies(ctx, MovieFilter{Years: [2]int{0, 9999}}); return err }},
		{"last program", `UPDATE channel_programs SET item_id = 'bad'`, func() error { _, _, err := s.ChannelLastProgram(ctx, channel.ID); return err }},
		{"lineup", `UPDATE items SET year = 'bad' WHERE id = ?`, func() error { _, _, err := s.ChannelLineup(ctx, start, end); return err }},
		{"video stream", `UPDATE media_streams SET width = 'bad' WHERE media_file_id = ?`, func() error { _, err := s.firstVideoStreams(ctx, []ChannelProgram{{MediaID: mediaID}}); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
				t.Fatal(err)
			}
			var args []any
			if strings.Contains(tc.corrupt, "id = ?") {
				args = []any{mediaID}
				if strings.HasPrefix(tc.corrupt, "UPDATE items") {
					args = []any{id}
				}
			}
			if _, err := s.db.Exec(tc.corrupt, args...); err != nil {
				t.Fatal(err)
			}
			if err := tc.run(); err == nil {
				t.Fatalf("malformed %s row accepted", tc.name)
			}
			for _, query := range []string{`UPDATE media_files SET duration_ms = 60000`, `UPDATE channel_programs SET item_id = 1`, `UPDATE items SET year = 2020`, `UPDATE media_streams SET width = 1920`, `PRAGMA foreign_keys = ON`} {
				if _, err := s.db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestChannelQueriesReportMissingTables(t *testing.T) {
	for _, tc := range []struct {
		name, table string
		run         func(*Store) error
	}{
		{"titles", "media_files", func(s *Store) error { _, err := s.ChannelTitles(context.Background(), []int64{42}); return err }},
		{"movies", "media_files", func(s *Store) error {
			_, err := s.ChannelMovies(context.Background(), MovieFilter{MinVote: 7})
			return err
		}},
		{"shorts", "media_files", func(s *Store) error { _, err := s.ChannelShorts(context.Background()); return err }},
		{"last program", "channel_programs", func(s *Store) error { _, _, err := s.ChannelLastProgram(context.Background(), 1); return err }},
		{"cursors", "channel_cursors", func(s *Store) error { _, err := s.ChannelCursors(context.Background(), 1); return err }},
		{"history", "channel_programs", func(s *Store) error { _, err := s.ChannelAired(context.Background(), 1); return err }},
		{"reach", "channel_programs", func(s *Store) error { _, err := s.ChannelScheduleReach(context.Background()); return err }},
		{"lineup", "channel_programs", func(s *Store) error { _, _, err := s.ChannelLineup(context.Background(), "a", "z"); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "loom.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			if _, err := s.db.Exec("DROP TABLE " + tc.table); err != nil {
				t.Fatal(err)
			}
			if err := tc.run(s); err == nil || !strings.Contains(err.Error(), tc.table) {
				t.Fatalf("missing %s table = %v", tc.table, err)
			}
		})
	}
}
