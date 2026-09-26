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

func TestReconcilePropagatesChannelWriteFailures(t *testing.T) {
	for _, tc := range []struct {
		name, trigger string
		existing      bool
		lineupName    string
	}{
		{"create", `CREATE TRIGGER fail_channel BEFORE INSERT ON channels BEGIN SELECT RAISE(FAIL, 'create rejected'); END`, false, "Films"},
		{"rename", `CREATE TRIGGER fail_channel BEFORE UPDATE ON channels BEGIN SELECT RAISE(FAIL, 'rename rejected'); END`, true, "New Name"},
		{"delete", `CREATE TRIGGER fail_channel BEFORE DELETE ON channels BEGIN SELECT RAISE(FAIL, 'delete rejected'); END`, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "loom.db")
			s, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			if tc.existing {
				if _, err := s.CreateChannel(ctx, "films", "Films", store.ChannelTime(testNow)); err != nil {
					t.Fatal(err)
				}
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			if _, err := db.Exec(tc.trigger); err != nil {
				t.Fatal(err)
			}
			var lineup []Channel
			if tc.lineupName != "" {
				lineup = []Channel{{Key: "films", Name: tc.lineupName, Blocks: []Block{daily(gridStart, gridEnd, &Source{Name: "films", Titles: []int64{42}})}}}
			}
			g := NewWith(s, lineup, time.UTC)
			if _, err := g.Reconcile(ctx, testNow); err == nil || !strings.Contains(err.Error(), "rejected") {
				t.Fatalf("%s failure = %v", tc.name, err)
			}
		})
	}
}

func TestPoolLoadingPropagatesCatalogFailures(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		source Source
	}{
		{"show episodes", Source{Name: "shows", Shows: []int64{123}}},
		{"titles", Source{Name: "titles", Titles: []int64{123}}},
		{"filtered movies", Source{Name: "movies", MinVote: 7}},
		{"shorts", Source{Name: "shorts", Shorts: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := &poolCache{ctx: context.Background(), catalog: s, pools: map[*Source]*pool{}}
			if _, err := cache.load(&tc.source); err == nil || !strings.Contains(err.Error(), "closed") {
				t.Fatalf("pool loading = %v", err)
			}
		})
	}
}
