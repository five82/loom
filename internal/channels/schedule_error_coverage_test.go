package channels

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/five82/loom/internal/store"
	_ "modernc.org/sqlite"
)

func TestExtendReportsCorruptScheduleAndCatalogFailures(t *testing.T) {
	for _, tc := range []struct{ name, setup, want string }{
		{"prune", `DROP TABLE channel_programs`, "prune channel programs"},
		{"invalid tail", `UPDATE channel_programs SET ends_at = 'invalid'`, "parse channel time"},
		{"cursors", `DROP TABLE channel_cursors`, "list channel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "loom.db")
			s, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			channel, err := s.CreateChannel(ctx, "films", "Films", store.ChannelTime(testNow))
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "invalid tail" {
				lib, scan, err := s.StartScan(ctx, "movies", "/movies")
				if err != nil {
					t.Fatal(err)
				}
				item, err := s.UpsertItem(ctx, store.ItemInput{LibraryID: lib, SourceKey: "movie", Kind: "movie", Title: "Movie", ScanID: scan})
				if err != nil {
					t.Fatal(err)
				}
				if err := s.AppendChannelPrograms(ctx, channel.ID, []store.ScheduledProgram{{ItemID: item, StartsAt: store.ChannelTime(testNow), EndsAt: store.ChannelTime(testNow.Add(time.Hour))}}, nil); err != nil {
					t.Fatal(err)
				}
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			if _, err := db.Exec(tc.setup); err != nil {
				t.Fatal(err)
			}
			g := NewWith(s, []Channel{{Key: "films", Name: "Films", Blocks: []Block{daily(gridStart, gridEnd, &Source{Name: "films", Titles: []int64{42}})}}}, time.UTC)
			if _, _, err := g.Extend(ctx, testNow); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s failure = %v", tc.name, err)
			}
		})
	}
}
