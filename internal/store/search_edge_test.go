package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSearchPaginationDoesNotFallBackToFuzzyOnLaterStrictPage(t *testing.T) {
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
	for _, title := range []string{"Arrival", "Arival"} {
		if _, err := s.UpsertItem(ctx, ItemInput{LibraryID: libraryID, SourceKey: title, Kind: "movie", Title: title, ScanID: scanID}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		query         string
		limit, offset int
		want          int
		fuzzy         bool
	}{
		{"Arrival", 1, 1, 0, false},
		{"Arrival", 1, -1, 1, false},
		{"Arival", 999, 0, 1, false},
		{"Arrivl", 1, 0, 1, true},
		{"Arrivl", 1, 1, 0, true},
		{"nonsense", 0, 0, 0, true},
	} {
		results, fuzzy, err := s.SearchItems(ctx, tc.query, tc.limit, tc.offset)
		if err != nil || len(results) != tc.want || fuzzy != tc.fuzzy {
			t.Fatalf("search %q limit %d offset %d: %d results, fuzzy=%v, err=%v", tc.query, tc.limit, tc.offset, len(results), fuzzy, err)
		}
	}
	if _, _, err := s.SearchItems(ctx, "  ", 10, 0); err == nil {
		t.Fatal("blank query accepted")
	}
}
