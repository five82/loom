package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/five82/loom/internal/library"
	"github.com/five82/loom/internal/store"
)

var (
	sciFi = store.Genre{ID: 1, Name: "Sci-Fi"}
	drama = store.Genre{ID: 2, Name: "Drama"}
)

type shelfMovie struct {
	id         int64
	genre      store.Genre
	rating     float64
	durationMS int64
	started    bool
}

func makeShelfMovie(m shelfMovie) store.Item {
	if m.genre.ID == 0 {
		m.genre = sciFi
	}
	if m.rating == 0 {
		m.rating = 8.0
	}
	if m.durationMS == 0 {
		m.durationMS = 2 * 60 * 60 * 1000
	}
	item := store.Item{
		ID: m.id, Kind: "movie", Title: "Movie", Genres: []store.Genre{m.genre},
		VoteAverage: m.rating, DurationMS: m.durationMS,
	}
	if m.started {
		item.Progress = &store.Progress{PositionMS: 1}
	}
	return item
}

func makeShelfShow(id int64, unwatched int) store.Item {
	return store.Item{
		ID: id, Kind: "show", Title: "Show", Genres: []store.Genre{drama},
		VoteAverage: 8.0, EpisodeCount: 8, UnwatchedCount: unwatched,
	}
}

func shelfFixture() (movies, shows []store.Item, collections []collection, recentlyPlayed []store.Item) {
	for id := int64(1); id <= 20; id++ {
		m := shelfMovie{id: id, genre: drama}
		if id%2 == 0 {
			m.genre = sciFi
		}
		if id%5 == 0 {
			m.durationMS = 80 * 60 * 1000
		}
		movies = append(movies, makeShelfMovie(m))
	}
	for id := int64(21); id <= 30; id++ {
		shows = append(shows, makeShelfShow(id, 8))
	}
	collections = []collection{
		{Slug: "first", Title: "First Collection", Items: []store.Item{movies[0], movies[2]}},
		{Slug: "second", Title: "Second Collection", Items: []store.Item{movies[1], movies[3]}},
	}
	recentlyPlayed = []store.Item{
		makeShelfMovie(shelfMovie{id: 5, started: true}), makeShelfMovie(shelfMovie{id: 7, started: true}),
		makeShelfShow(21, 0), makeShelfShow(22, 0),
	}
	return movies, shows, collections, recentlyPlayed
}

func shelvesFor(t *testing.T, day int64) []shelf {
	t.Helper()
	movies, shows, collections, recentlyPlayed := shelfFixture()
	return discoveryShelves(movies, shows, collections, recentlyPlayed, day)
}

func shelfIDs(s shelf) []int64 {
	ids := make([]int64, len(s.Items))
	for index, item := range s.Items {
		ids[index] = item.ID
	}
	return ids
}

func TestDiscoveryShelvesStableWithinDayAndCapped(t *testing.T) {
	first := shelvesFor(t, 100)
	second := shelvesFor(t, 100)
	if len(first) != shelfCount {
		t.Fatalf("shelves = %d, want %d", len(first), shelfCount)
	}
	for index := range first {
		if first[index].Key != second[index].Key || !slices.Equal(shelfIDs(first[index]), shelfIDs(second[index])) {
			t.Fatalf("shelf %d differs between two builds of the same day: %v vs %v", index, first[index], second[index])
		}
		if len(first[index].Items) > shelfItems {
			t.Fatalf("shelf %q has %d items, want at most %d", first[index].Key, len(first[index].Items), shelfItems)
		}
	}
}

func TestDiscoveryShelvesRotateAcrossDays(t *testing.T) {
	seen := map[string]bool{}
	for day := int64(0); day <= 14; day++ {
		var keys []string
		for _, s := range shelvesFor(t, day) {
			keys = append(keys, s.Key)
		}
		seen[strings.Join(keys, ",")] = true
	}
	if len(seen) < 2 {
		t.Fatalf("shelves never changed across two weeks: %v", seen)
	}
}

func TestDiscoveryShelvesHonorTheirFilters(t *testing.T) {
	_, _, _, recentlyPlayed := shelfFixture()
	for day := int64(0); day <= 30; day++ {
		for _, s := range shelvesFor(t, day) {
			switch s.Key {
			case "genre":
				genre := strings.TrimPrefix(s.Title, "Tonight: ")
				for _, item := range s.Items {
					if !slices.ContainsFunc(item.Genres, func(g store.Genre) bool { return g.Name == genre }) {
						t.Fatalf("day %d: %s shelf holds item %d without that genre", day, s.Title, item.ID)
					}
					if started(item) {
						t.Fatalf("day %d: genre shelf holds started item %d", day, item.ID)
					}
				}
			case "quick":
				for _, item := range s.Items {
					if item.Kind != "movie" || item.DurationMS >= quickWatchMaxMS || started(item) {
						t.Fatalf("day %d: quick shelf holds %+v", day, item)
					}
				}
			case "again":
				want := make([]int64, len(recentlyPlayed))
				for index, item := range recentlyPlayed {
					want[index] = item.ID
				}
				if !slices.Equal(shelfIDs(s), want) {
					t.Fatalf("day %d: again shelf = %v, want store order %v", day, shelfIDs(s), want)
				}
			case "unstarted", "rated":
				for _, item := range s.Items {
					if started(item) {
						t.Fatalf("day %d: %s shelf holds started item %d", day, s.Key, item.ID)
					}
					if s.Key == "rated" && item.VoteAverage < store.FeaturedRatingThreshold {
						t.Fatalf("day %d: rated shelf holds item %d rated %v", day, item.ID, item.VoteAverage)
					}
				}
			}
		}
	}
}

func TestDiscoveryThinShelvesAreSkipped(t *testing.T) {
	movies, shows, collections, _ := shelfFixture()
	// Two finished movies cannot fill the Watch It Again shelf.
	sparse := []store.Item{
		makeShelfMovie(shelfMovie{id: 5, started: true}), makeShelfMovie(shelfMovie{id: 7, started: true}),
	}
	for day := int64(0); day <= 30; day++ {
		for _, s := range discoveryShelves(movies, shows, collections, sparse, day) {
			if s.Key == "again" {
				t.Fatalf("day %d: again shelf built from two items", day)
			}
		}
	}
	// Three unstarted movies are below the floor for every shelf.
	three := movies[:3]
	if shelves := discoveryShelves(three, nil, nil, nil, 5); len(shelves) != 0 {
		t.Fatalf("shelves from three movies = %v, want none", shelves)
	}
}

func TestDiscoveryCollectionShelfMayRunThin(t *testing.T) {
	var found bool
	for day := int64(0); day <= 30; day++ {
		for _, s := range shelvesFor(t, day) {
			if strings.HasPrefix(s.Key, "col-") {
				found = true
				if len(s.Items) != 2 {
					t.Fatalf("day %d: collection shelf has %d items, want the two owned", day, len(s.Items))
				}
			}
		}
	}
	if !found {
		t.Fatal("collection shelf never surfaced in a month")
	}
}

func TestStartedCountsWatchedEpisodesAndAnyMoviePlayback(t *testing.T) {
	if started(makeShelfShow(1, 10)) || !started(makeShelfShow(2, 4)) {
		t.Fatal("show startedness should follow unwatched count")
	}
	if started(makeShelfMovie(shelfMovie{id: 1})) || !started(makeShelfMovie(shelfMovie{id: 2, started: true})) {
		t.Fatal("movie startedness should follow playback state")
	}
}

func TestHomeExpiryIsNextPickChangeOrMidnight(t *testing.T) {
	location := time.FixedZone("server", -7*60*60)
	cases := []struct{ at, want time.Time }{
		{time.Date(2025, 8, 12, 2, 0, 0, 0, location), time.Date(2025, 8, 12, 6, 0, 0, 0, location)},
		{time.Date(2025, 8, 12, 10, 0, 0, 0, location), time.Date(2025, 8, 12, 18, 0, 0, 0, location)},
		{time.Date(2025, 8, 12, 20, 0, 0, 0, location), time.Date(2025, 8, 13, 0, 0, 0, 0, location)},
	}
	for _, c := range cases {
		if got := homeExpiry(c.at); !got.Equal(c.want) {
			t.Fatalf("homeExpiry(%v) = %v, want %v", c.at, got, c.want)
		}
	}
}

func TestHomeAPI(t *testing.T) {
	ctx := context.Background()
	catalog, err := store.Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	libraryID, scanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for index, title := range []string{"Arrival", "Alien", "The Matrix", "Heat", "Solaris", "Gattaca"} {
		id, err := catalog.UpsertItem(ctx, store.ItemInput{
			LibraryID: libraryID, SourceKey: title, Kind: "movie", Title: title, ScanID: scanID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := catalog.UpsertMedia(ctx, store.MediaFile{
			ItemID: id, Path: "/movies/" + title + ".mkv", Size: 100, MTimeNS: int64(index),
			DurationMS: 6_000_000, Container: "matroska", LastSeenScanID: scanID,
		}, nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := catalog.UpdateMetadata(ctx, id, store.MetadataUpdate{
			TMDBID: id, Title: title, VoteAverage: 8.0, Genres: []store.Genre{sciFi},
		}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := catalog.FinishScan(ctx, libraryID, scanID, len(ids), len(ids), 0, nil); err != nil {
		t.Fatal(err)
	}
	// Half-watch every movie so the hero, whichever it is, sits in Continue
	// Watching and would be drawn twice without the dedup.
	for _, id := range ids {
		if _, err := catalog.SetProgress(ctx, id, 3_000_000, 6_000_000); err != nil {
			t.Fatal(err)
		}
	}

	api := New(catalog, library.NewManager(nil, 0, slog.Default()), nil, make(chan struct{}, 1), ListenAddresses{})
	server := httptest.NewServer(api.PublicHandler())
	defer server.Close()

	response, err := http.Get(server.URL + "/api/v1/home")
	if err != nil {
		t.Fatal(err)
	}
	var home homeResponse
	if err := json.NewDecoder(response.Body).Decode(&home); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || home.Featured == nil {
		t.Fatalf("home status=%d featured=%+v", response.StatusCode, home.Featured)
	}
	if len(home.ContinueWatching) != len(ids)-1 {
		t.Fatalf("continue watching has %d items, want the %d that are not the hero", len(home.ContinueWatching), len(ids)-1)
	}
	for _, item := range home.ContinueWatching {
		if item.ID == home.Featured.ID {
			t.Fatalf("hero %d repeated in continue watching", item.ID)
		}
	}
	// Every movie is started, so the only shelf that can fill is Something
	// Different; a genre or unstarted shelf must not appear.
	if len(home.Shelves) != 1 || home.Shelves[0].Key != "different" {
		t.Fatalf("shelves = %+v, want only Something Different", home.Shelves)
	}
	for _, item := range home.Shelves[0].Items {
		if item.ID == home.Featured.ID {
			t.Fatalf("hero %d repeated in shelf", item.ID)
		}
	}
	if home.NextUp == nil || home.RecentlyAdded == nil {
		t.Fatal("empty rows must be arrays, not null")
	}
	expires, err := time.Parse(time.RFC3339, home.ExpiresAt)
	if err != nil {
		t.Fatalf("expires_at %q: %v", home.ExpiresAt, err)
	}
	if until := time.Until(expires); until <= 0 || until > 24*time.Hour {
		t.Fatalf("expires_at %v is %v away, want within the next day", expires, until)
	}
}
