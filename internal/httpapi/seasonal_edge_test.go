package httpapi

import (
	"testing"
	"time"

	"github.com/five82/loom/internal/store"
)

func TestDecemberShelfIncludesOnlyOwnedHolidayMovies(t *testing.T) {
	now := time.Date(2025, time.December, 10, 12, 0, 0, 0, time.UTC)
	movies := []store.Item{{ID: 1, TMDBID: 850}, {ID: 2, TMDBID: 5825}, {ID: 3, TMDBID: 1621}, {ID: 4, TMDBID: 2609}, {ID: 5, TMDBID: 99}}
	shelf := seasonalShelf(movies, now, func(items []store.Item) []store.Item { return items })
	if shelf == nil || shelf.Key != "holiday" || len(shelf.Items) != 4 || shelf.Items[0].ID != 1 || shelf.Items[3].ID != 4 {
		t.Fatalf("December shelf = %+v", shelf)
	}
	if got := seasonalShelf(movies, now.AddDate(0, -1, 0), func(items []store.Item) []store.Item { return items }); got != nil {
		t.Fatalf("November shelf = %+v", got)
	}
}
