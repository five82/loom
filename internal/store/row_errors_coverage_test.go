package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
	"time"
)

var errRowsInterrupted = errors.New("row iteration interrupted")
var registerInterruptedRows sync.Once

type interruptedDriver struct{}
type interruptedConn struct{}
type interruptedRows struct{}

func (interruptedDriver) Open(string) (driver.Conn, error) { return interruptedConn{}, nil }
func (interruptedConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (interruptedConn) Close() error              { return nil }
func (interruptedConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected transaction") }
func (interruptedConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return interruptedRows{}, nil
}
func (interruptedRows) Columns() []string         { return []string{"value"} }
func (interruptedRows) Close() error              { return nil }
func (interruptedRows) Next([]driver.Value) error { return errRowsInterrupted }

// A database can fail after Query succeeds (for example, when stepping a
// SQLite cursor). Callers must not return a successful, incomplete listing.
func TestCatalogListsPropagateRowIterationErrors(t *testing.T) {
	registerInterruptedRows.Do(func() { sql.Register("loom-interrupted-rows", interruptedDriver{}) })
	db, err := sql.Open("loom-interrupted-rows", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	s := &Store{db: db}
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"libraries", func() error { _, err := s.Libraries(ctx); return err }},
		{"last scans", func() error { _, err := s.LastScans(ctx); return err }},
		{"search", func() error { _, _, err := s.SearchItems(ctx, "movie", 10, 0); return err }},
		{"list items", func() error { _, err := s.ListItems(ctx, ListOptions{}); return err }},
		{"continue watching", func() error { _, err := s.ContinueWatching(ctx, 10); return err }},
		{"next up", func() error { _, err := s.NextUp(ctx, 10); return err }},
		{"recently added", func() error { _, err := s.RecentlyAdded(ctx, 10); return err }},
		{"recently played", func() error { _, err := s.RecentlyPlayed(ctx, 10); return err }},
		{"collection", func() error { _, err := s.ItemsByTMDBID(ctx, []int64{1}); return err }},
		{"discovery", func() error { _, _, _, err := s.DiscoveryLibrary(ctx); return err }},
		{"headline credits", func() error { _, err := s.HeadlineCredits(ctx); return err }},
		{"genres", func() error { _, err := s.Genres(ctx); return err }},
		{"seasons", func() error { _, err := s.SeasonsForShow(ctx, 1); return err }},
		{"episodes", func() error { _, err := s.EpisodesForShow(ctx, 1); return err }},
		{"unmatched", func() error { _, err := s.UnmatchedItems(ctx); return err }},
		{"credits", func() error { _, err := s.populateCredits(ctx, 1); return err }},
		{"channels", func() error { _, err := s.Channels(ctx); return err }},
		{"channel episodes", func() error { _, err := s.ChannelShowEpisodes(ctx, 1); return err }},
		{"channel titles", func() error { _, err := s.ChannelTitles(ctx, []int64{1}); return err }},
		{"channel movies", func() error { _, err := s.ChannelMovies(ctx, MovieFilter{MinVote: 7}); return err }},
		{"channel shorts", func() error { _, err := s.ChannelShorts(ctx); return err }},
		{"channel cursors", func() error { _, err := s.ChannelCursors(ctx, 1); return err }},
		{"channel aired", func() error { _, err := s.ChannelAired(ctx, 1); return err }},
		{"channel lineup", func() error {
			_, _, err := s.ChannelLineup(ctx, ChannelTime(time.Now()), ChannelTime(time.Now().Add(time.Hour)))
			return err
		}},
		{"media streams", func() error { _, err := s.streams(ctx, 1); return err }},
		{"media chapters", func() error { _, err := s.chapters(ctx, 1); return err }},
		{"audit check", func() error {
			_, err := s.runAuditCheck(ctx, auditCheck{name: "sample", query: "SELECT 1"}, true)
			return err
		}},
		{"missing artwork", func() error { _, err := s.missingArtworkFiles(ctx); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !errors.Is(err, errRowsInterrupted) {
				t.Fatalf("row iteration error = %v, want %v", err, errRowsInterrupted)
			}
		})
	}
}

var _ driver.QueryerContext = interruptedConn{}
var _ driver.Rows = interruptedRows{}
