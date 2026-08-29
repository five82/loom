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

	"github.com/five82/loom/internal/channels"
	"github.com/five82/loom/internal/library"
	"github.com/five82/loom/internal/store"
)

var (
	sciFi    = store.Genre{ID: 1, Name: "Sci-Fi"}
	drama    = store.Genre{ID: 2, Name: "Drama"}
	docGenre = store.Genre{ID: 3, Name: "Documentary"}
	horror   = store.Genre{ID: 4, Name: "Horror"}
)

type shelfMovie struct {
	id         int64
	genres     []store.Genre
	rating     float64
	durationMS int64
	year       int
	rated      string
	// sampled leaves a playback row at the opening seconds, which is not
	// watching; started records a real watch.
	sampled, started, played bool
}

func makeShelfMovie(m shelfMovie) store.Item {
	if m.genres == nil {
		m.genres = []store.Genre{sciFi}
	}
	if m.rating == 0 {
		m.rating = 8.0
	}
	if m.durationMS == 0 {
		m.durationMS = 2 * 60 * 60 * 1000
	}
	if m.year == 0 {
		m.year = 1999
	}
	item := store.Item{
		ID: m.id, Kind: "movie", Title: "Movie", Genres: m.genres, Year: m.year, ContentRating: m.rated,
		VoteAverage: m.rating, DurationMS: m.durationMS,
	}
	switch {
	case m.played:
		item.Progress = &store.Progress{PositionMS: m.durationMS, DurationMS: m.durationMS, Played: true}
	case m.started:
		item.Progress = &store.Progress{PositionMS: m.durationMS / 2, DurationMS: m.durationMS}
	case m.sampled:
		item.Progress = &store.Progress{PositionMS: 1000, DurationMS: m.durationMS}
	}
	return item
}

func makeShelfShow(id int64, unwatched int) store.Item {
	return store.Item{
		ID: id, Kind: "show", Title: "Show", Genres: []store.Genre{drama},
		VoteAverage: 8.0, EpisodeCount: 8, UnwatchedCount: unwatched,
	}
}

// shelfFixture is a library wide enough to fill every shelf kind: twenty
// well-rated movies across two genres and decades, four of them short and
// four of them long, ten unstarted shows, a documentary block, a family block,
// two collections, a two-item viewing history, a director with four films,
// and shorts.
func shelfFixture() discoveryInput {
	var in discoveryInput
	for id := int64(1); id <= 20; id++ {
		m := shelfMovie{id: id, genres: []store.Genre{drama}, year: 1985}
		if id%2 == 0 {
			m.genres = []store.Genre{sciFi}
			m.year = 1995
		}
		if id%5 == 0 {
			m.durationMS = 80 * 60 * 1000
		}
		if id%5 == 1 {
			m.durationMS = 160 * 60 * 1000
		}
		if id <= 4 {
			m.rated = "PG"
		}
		in.movies = append(in.movies, makeShelfMovie(m))
	}
	for id := int64(21); id <= 24; id++ {
		in.movies = append(in.movies, makeShelfMovie(shelfMovie{id: id, genres: []store.Genre{docGenre}}))
	}
	for id := int64(31); id <= 40; id++ {
		in.shows = append(in.shows, makeShelfShow(id, 8))
	}
	for id := int64(41); id <= 44; id++ {
		in.shorts = append(in.shorts, makeShelfMovie(shelfMovie{id: id, durationMS: 5 * 60 * 1000}))
	}
	in.collections = []collection{
		{Slug: "first", Title: "First Collection", Items: []store.Item{in.movies[0], in.movies[2]}},
		{Slug: "second", Title: "Second Collection", Items: []store.Item{in.movies[1], in.movies[3]}},
	}
	in.recentlyPlayed = []store.Item{
		makeShelfMovie(shelfMovie{id: 5, played: true}), makeShelfMovie(shelfMovie{id: 7, played: true}),
	}
	for id := int64(1); id <= 4; id++ {
		in.credits = append(in.credits, store.PersonCredit{PersonID: 100, Name: "A Director", Role: "director", ItemID: id})
	}
	in.now = time.Date(2025, time.March, 1, 12, 0, 0, 0, time.UTC)
	return in
}

func onDay(in discoveryInput, day int64) discoveryInput {
	in.now = in.now.AddDate(0, 0, int(day))
	return in
}

func shelvesFor(day int64) []shelf {
	return discoveryShelves(onDay(shelfFixture(), day))
}

func shelfIDs(s shelf) []int64 {
	ids := make([]int64, len(s.Items))
	for index, item := range s.Items {
		ids[index] = item.ID
	}
	return ids
}

func shelfKind(s shelf) string {
	kind, _, _ := strings.Cut(s.Key, "-")
	return kind
}

func TestDiscoveryShelvesStableWithinDayAndCapped(t *testing.T) {
	first := shelvesFor(100)
	second := shelvesFor(100)
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

func TestDiscoveryShelvesRotateAcrossDaysAndReachEveryKind(t *testing.T) {
	seen := map[string]bool{}
	kinds := map[string]bool{}
	for day := int64(0); day <= 60; day++ {
		var keys []string
		for _, s := range shelvesFor(day) {
			keys = append(keys, s.Key)
			kinds[shelfKind(s)] = true
		}
		seen[strings.Join(keys, ",")] = true
	}
	if len(seen) < 2 {
		t.Fatalf("shelves never changed across two months: %v", seen)
	}
	for _, kind := range []string{
		"genre", "decade", "person", "unstarted", "rated", "series", "documentary",
		"family", "col", "different", "again", "quick", "epic", "shorts",
	} {
		if !kinds[kind] {
			t.Errorf("shelf kind %q never surfaced in two months of %v", kind, kinds)
		}
	}
}

func TestDiscoveryShelvesHonorTheirFilters(t *testing.T) {
	fixture := shelfFixture()
	for day := int64(0); day <= 60; day++ {
		for _, s := range shelvesFor(day) {
			genericPool := true
			switch shelfKind(s) {
			case "genre":
				genre := strings.TrimPrefix(s.Title, "Tonight: ")
				if s.Key != "genre-"+genre {
					t.Fatalf("day %d: genre shelf key %q does not name its pick %q", day, s.Key, genre)
				}
				for _, item := range s.Items {
					if !slices.ContainsFunc(item.Genres, func(g store.Genre) bool { return g.Name == genre }) {
						t.Fatalf("day %d: %s shelf holds item %d without that genre", day, s.Title, item.ID)
					}
				}
			case "decade":
				decade := strings.TrimPrefix(s.Key, "decade-")
				for _, item := range s.Items {
					if item.Kind != "movie" || strings.TrimPrefix(s.Key, "decade-") != decade || item.Year/10*10 != atoi(t, decade) {
						t.Fatalf("day %d: %s shelf holds %+v", day, s.Title, item)
					}
				}
				if !strings.HasPrefix(s.Title, "Back to the '") {
					t.Fatalf("day %d: decade title %q", day, s.Title)
				}
			case "person":
				if s.Key != "person-100-director" || s.Title != "Directed by A Director" {
					t.Fatalf("day %d: person shelf = %q %q", day, s.Key, s.Title)
				}
				for _, item := range s.Items {
					if item.ID > 4 {
						t.Fatalf("day %d: director shelf holds uncredited movie %d", day, item.ID)
					}
				}
			case "series":
				for _, item := range s.Items {
					if item.Kind != "show" || item.VoteAverage < store.FeaturedRatingThreshold {
						t.Fatalf("day %d: series shelf holds %+v", day, item)
					}
				}
			case "documentary":
				genericPool = false
				for _, item := range s.Items {
					if !documentary(item) || started(item) {
						t.Fatalf("day %d: documentary shelf holds %+v", day, item)
					}
				}
			case "family":
				for _, item := range s.Items {
					if item.ContentRating != "PG" {
						t.Fatalf("day %d: family shelf holds %+v", day, item)
					}
				}
			case "quick":
				for _, item := range s.Items {
					if item.Kind != "movie" || item.DurationMS >= quickWatchMaxMS {
						t.Fatalf("day %d: quick shelf holds %+v", day, item)
					}
				}
			case "epic":
				for _, item := range s.Items {
					if item.Kind != "movie" || item.DurationMS <= longHaulMinMS {
						t.Fatalf("day %d: epic shelf holds %+v", day, item)
					}
				}
			case "again":
				genericPool = false
				want := make([]int64, len(fixture.recentlyPlayed))
				for index, item := range fixture.recentlyPlayed {
					want[index] = item.ID
				}
				if !slices.Equal(shelfIDs(s), want) {
					t.Fatalf("day %d: again shelf = %v, want store order %v", day, shelfIDs(s), want)
				}
			case "rated":
				for _, item := range s.Items {
					if item.VoteAverage < store.FeaturedRatingThreshold {
						t.Fatalf("day %d: rated shelf holds item %d rated %v", day, item.ID, item.VoteAverage)
					}
				}
			case "shorts":
				genericPool = false
				for _, item := range s.Items {
					if item.ID < 41 || started(item) {
						t.Fatalf("day %d: shorts shelf holds %+v", day, item)
					}
				}
			case "different", "col":
				genericPool = false
			case "unstarted":
			default:
				t.Fatalf("day %d: unexpected shelf %q", day, s.Key)
			}
			if !genericPool {
				continue
			}
			for _, item := range s.Items {
				if started(item) || documentary(item) || item.VoteAverage < discoveryRatingFloor {
					t.Fatalf("day %d: %s shelf holds started, documentary, or low-rated item %+v", day, s.Key, item)
				}
			}
		}
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			t.Fatalf("%q is not a number", s)
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func TestDiscoveryGenericShelvesApplyTheRatingFloorAndKeepSampledMovies(t *testing.T) {
	in := shelfFixture()
	// A guilty pleasure and a movie opened and abandoned at the first second.
	in.movies = append(in.movies, makeShelfMovie(shelfMovie{id: 90, rating: discoveryRatingFloor - 0.1}))
	in.movies = append(in.movies, makeShelfMovie(shelfMovie{id: 91, sampled: true}))
	// A movie half-watched.
	in.movies = append(in.movies, makeShelfMovie(shelfMovie{id: 92, started: true}))
	var sawSampled bool
	for day := int64(0); day <= 60; day++ {
		for _, s := range discoveryShelves(onDay(in, day)) {
			if shelfKind(s) == "different" || shelfKind(s) == "col" || shelfKind(s) == "again" {
				continue
			}
			ids := shelfIDs(s)
			if slices.Contains(ids, 90) || slices.Contains(ids, 92) {
				t.Fatalf("day %d: %s shelf offers a low-rated or half-watched movie: %v", day, s.Key, ids)
			}
			if slices.Contains(ids, 91) {
				sawSampled = true
			}
		}
	}
	if !sawSampled {
		t.Fatal("a movie merely opened and closed never reached a shelf in two months")
	}
}

func TestDiscoveryDocumentariesStayOffGenericShelves(t *testing.T) {
	in := shelfFixture()
	// Make the documentaries short and highly rated, so every shelf they are
	// wrongly eligible for would want them.
	for index := range in.movies {
		if documentary(in.movies[index]) {
			in.movies[index].DurationMS = 60 * 60 * 1000
			in.movies[index].VoteAverage = 9.5
		}
	}
	for day := int64(0); day <= 60; day++ {
		for _, s := range discoveryShelves(onDay(in, day)) {
			if shelfKind(s) == "documentary" {
				continue
			}
			for _, item := range s.Items {
				if documentary(item) {
					t.Fatalf("day %d: %s shelf holds documentary %d", day, s.Key, item.ID)
				}
			}
		}
	}
}

func TestDiscoveryGenreAndDecadePicksFollowPoolSize(t *testing.T) {
	// Drama has ten movies and ten shows behind it; Sci-Fi ten movies. Over
	// two months the bigger pool should lead, without shutting the other out.
	counts := map[string]int{}
	for day := int64(0); day <= 120; day++ {
		for _, s := range shelvesFor(day) {
			if shelfKind(s) == "genre" {
				counts[s.Key]++
			}
		}
	}
	if counts["genre-Drama"] <= counts["genre-Sci-Fi"] || counts["genre-Sci-Fi"] == 0 {
		t.Fatalf("genre picks = %v, want Drama ahead and Sci-Fi present", counts)
	}
}

func TestDiscoveryBecauseYouWatchedPrefersSharedCreditsThenGenres(t *testing.T) {
	in := shelfFixture()
	seed := makeShelfMovie(shelfMovie{id: 2, genres: []store.Genre{sciFi, horror}, started: true})
	in.seed = &seed
	// The fixture's director shares movies 1, 3, and 4 with the seed, and its
	// lead carries movies 6 and 8; movie 10 shares both genres; movie 12
	// shares only one.
	in.credits = append(in.credits,
		store.PersonCredit{PersonID: 200, Name: "A Lead", Role: "actor", ItemID: 2},
		store.PersonCredit{PersonID: 200, Name: "A Lead", Role: "actor", ItemID: 6},
		store.PersonCredit{PersonID: 200, Name: "A Lead", Role: "actor", ItemID: 8},
	)
	in.movies[9].Genres = []store.Genre{sciFi, horror}
	in.movies[11].Genres = []store.Genre{horror}
	built := becauseYouWatched(in.seed, in.movies, in.credits, func(items []store.Item) []store.Item { return items })
	if built == nil {
		t.Fatal("because shelf was not built")
	}
	if built.Key != "because-2" || built.Title != "Because You Watched Movie" {
		t.Fatalf("because shelf = %q %q", built.Key, built.Title)
	}
	if got := shelfIDs(*built); !slices.Equal(got, []int64{1, 3, 4, 6, 8, 10}) {
		t.Fatalf("because shelf = %v, want shared-credit movies first then the two-genre match", got)
	}
	if becauseYouWatched(nil, in.movies, in.credits, nil) != nil {
		t.Fatal("because shelf built without a seed")
	}
}

func TestDiscoverySeasonalShelfLeadsInSeason(t *testing.T) {
	in := shelfFixture()
	for id := int64(50); id < 54; id++ {
		in.movies = append(in.movies, makeShelfMovie(shelfMovie{id: id, genres: []store.Genre{horror}, rating: 5.0, played: true}))
	}
	october := discoveryShelves(onDay(in, 0))
	if october[0].Key != "halloween" && in.now.Month() == time.October {
		t.Fatalf("october shelves = %+v", october)
	}
	in.now = time.Date(2025, time.October, 15, 12, 0, 0, 0, time.UTC)
	shelves := discoveryShelves(in)
	if len(shelves) != shelfCount || shelves[0].Key != "halloween" || shelves[0].Title != "Halloween" {
		t.Fatalf("october shelves = %+v, want Halloween first", shelves)
	}
	// Rewatching is the point in season: watched and low-rated horror belong.
	if got := shelfIDs(shelves[0]); len(got) != 4 {
		t.Fatalf("halloween shelf = %v, want the four horror films", got)
	}
	in.now = time.Date(2025, time.March, 15, 12, 0, 0, 0, time.UTC)
	for _, s := range discoveryShelves(in) {
		if s.Key == "halloween" {
			t.Fatal("halloween shelf served in March")
		}
	}
}

func TestDiscoveryThinShelvesAreSkipped(t *testing.T) {
	in := shelfFixture()
	// One finished movie cannot fill even the lenient Watch It Again shelf.
	in.recentlyPlayed = in.recentlyPlayed[:1]
	for day := int64(0); day <= 60; day++ {
		for _, s := range discoveryShelves(onDay(in, day)) {
			if s.Key == "again" {
				t.Fatalf("day %d: again shelf built from one item", day)
			}
		}
	}
	// Three unstarted movies are below the floor for every shelf.
	three := discoveryInput{movies: in.movies[:3], now: in.now}
	if shelves := discoveryShelves(three); len(shelves) != 0 {
		t.Fatalf("shelves from three movies = %v, want none", shelves)
	}
}

func TestDiscoveryCollectionShelfMayRunThin(t *testing.T) {
	var found bool
	for day := int64(0); day <= 60; day++ {
		for _, s := range shelvesFor(day) {
			if strings.HasPrefix(s.Key, "col-") {
				found = true
				if len(s.Items) != 2 {
					t.Fatalf("day %d: collection shelf has %d items, want the two owned", day, len(s.Items))
				}
			}
		}
	}
	if !found {
		t.Fatal("collection shelf never surfaced in two months")
	}
}

func TestStartedCountsWatchedEpisodesAndRealMoviePlayback(t *testing.T) {
	if started(makeShelfShow(1, 10)) || !started(makeShelfShow(2, 4)) {
		t.Fatal("show startedness should follow unwatched count")
	}
	if started(makeShelfMovie(shelfMovie{id: 1})) || started(makeShelfMovie(shelfMovie{id: 2, sampled: true})) {
		t.Fatal("a movie without a real watch is not started")
	}
	if !started(makeShelfMovie(shelfMovie{id: 3, started: true})) || !started(makeShelfMovie(shelfMovie{id: 4, played: true})) {
		t.Fatal("movie startedness should follow playback past the resume floor")
	}
}

func TestWatchedSeedIsTheMostRecentMovie(t *testing.T) {
	episode := store.Item{ID: 1, Kind: "episode"}
	resumed := store.Item{ID: 2, Kind: "movie"}
	finished := store.Item{ID: 3, Kind: "movie"}
	if seed := watchedSeed([]store.Item{episode, resumed}, []store.Item{finished}); seed == nil || seed.ID != 2 {
		t.Fatalf("seed = %+v, want the resumed movie past the episode", seed)
	}
	if seed := watchedSeed([]store.Item{episode}, []store.Item{finished}); seed == nil || seed.ID != 3 {
		t.Fatalf("seed = %+v, want the finished movie", seed)
	}
	if watchedSeed([]store.Item{episode}, nil) != nil {
		t.Fatal("an episode seeded the because shelf")
	}
}

func TestDecadeLabel(t *testing.T) {
	for decade, want := range map[string]string{"1980": "'80s", "1990": "'90s", "2000": "2000s", "2010": "2010s"} {
		if got := decadeLabel(decade); got != want {
			t.Fatalf("decadeLabel(%s) = %q, want %q", decade, got, want)
		}
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
	// The hero is chosen before any movie is mid-watch; then half-watch every
	// movie so the hero sits in Continue Watching and would be drawn twice
	// without the dedup.
	if _, err := catalog.FeaturedPickAt(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if _, err := catalog.SetProgress(ctx, id, 3_000_000, 6_000_000); err != nil {
			t.Fatal(err)
		}
	}

	api := New(catalog, library.NewManager(nil, 0, slog.Default()), nil, channels.New(catalog), make(chan struct{}, 1), ListenAddresses{})
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
