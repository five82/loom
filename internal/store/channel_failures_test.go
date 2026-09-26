package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestChannelQueriesOnClosedCatalog(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tests := []struct {
		name string
		run  func() error
	}{
		{"channels", func() error { _, err := s.Channels(ctx); return err }},
		{"create", func() error { _, err := s.CreateChannel(ctx, "key", "Name", now()); return err }},
		{"rename", func() error { return s.RenameChannel(ctx, 1, "Name") }},
		{"delete", func() error { return s.DeleteChannel(ctx, 1) }},
		{"episodes", func() error { _, err := s.ChannelShowEpisodes(ctx, 1); return err }},
		{"titles", func() error { _, err := s.ChannelTitles(ctx, []int64{1}); return err }},
		{"movies", func() error { _, err := s.ChannelMovies(ctx, MovieFilter{MinVote: 8}); return err }},
		{"shorts", func() error { _, err := s.ChannelShorts(ctx); return err }},
		{"cursors", func() error { _, err := s.ChannelCursors(ctx, 1); return err }},
		{"aired", func() error { _, err := s.ChannelAired(ctx, 1); return err }},
		{"last", func() error { _, _, err := s.ChannelLastProgram(ctx, 1); return err }},
		{"reach", func() error { _, err := s.ChannelScheduleReach(ctx); return err }},
		{"append", func() error { return s.AppendChannelPrograms(ctx, 1, []ScheduledProgram{{ItemID: 1}}, nil) }},
		{"prune", func() error { _, err := s.PruneChannelPrograms(ctx, now()); return err }},
		{"lineup", func() error { _, _, err := s.ChannelLineup(ctx, now(), now()); return err }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil || !strings.Contains(err.Error(), "closed") {
				t.Fatalf("expected closed database error, got %v", err)
			}
		})
	}
}

func TestAppendChannelProgramsRollsBackWhenCursorFails(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	libraryID, scanID, err := s.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := s.UpsertItem(ctx, ItemInput{LibraryID: libraryID, SourceKey: "one", Kind: "movie", Title: "One", ScanID: scanID})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := s.CreateChannel(ctx, "channel", "Channel", now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_cursor BEFORE INSERT ON channel_cursors BEGIN SELECT RAISE(FAIL, 'cursor rejected'); END`); err != nil {
		t.Fatal(err)
	}
	err = s.AppendChannelPrograms(ctx, channel.ID, []ScheduledProgram{{ItemID: itemID, StartsAt: "2025-01-01T00:00:00Z", EndsAt: "2025-01-01T01:00:00Z"}}, []ChannelCursor{{Source: "source", ItemID: itemID, CycleStartedAt: "2025-01-01T00:00:00Z"}})
	if err == nil || !strings.Contains(err.Error(), "save channel") {
		t.Fatalf("append = %v", err)
	}
	programs, err := s.ChannelAired(ctx, channel.ID)
	if err != nil || len(programs) != 0 {
		t.Fatalf("partial schedule persisted: %+v, %v", programs, err)
	}
}

func TestChannelScanRejectsMalformedCatalogRows(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, tc := range []struct {
		schema, row string
		run         func() error
		want        string
	}{
		{`CREATE TEMP TABLE channels (id TEXT, number TEXT, key TEXT, name TEXT)`, `INSERT INTO channels VALUES ('invalid', '1', 'key', 'name')`, func() error { _, err := s.Channels(ctx); return err }, "scan channel"},
		{`CREATE TEMP TABLE channel_cursors (channel_id TEXT, source TEXT, cycle TEXT, cycle_started_at TEXT, item_id TEXT)`, `INSERT INTO channel_cursors VALUES ('1', 'source', 'invalid', 'time', '1')`, func() error { _, err := s.ChannelCursors(ctx, 1); return err }, "scan channel cursor"},
		{`CREATE TEMP TABLE channel_programs (id TEXT, channel_id TEXT, item_id TEXT, starts_at TEXT, ends_at TEXT)`, `INSERT INTO channel_programs VALUES ('1', '1', 'invalid', 'time', 'time')`, func() error { _, err := s.ChannelAired(ctx, 1); return err }, "scan channel history"},
	} {
		if _, err := s.db.Exec(tc.schema); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(tc.row); err != nil {
			t.Fatal(err)
		}
		if err := tc.run(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("malformed row: %v, want %s", err, tc.want)
		}
	}
}
