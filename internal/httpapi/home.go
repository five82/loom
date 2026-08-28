package httpapi

import (
	"errors"
	"math/rand"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/five82/loom/internal/store"
)

// The home screen in one response: the featured pick as the hero, the three
// playback-driven rows, and the day's rotating discovery shelves. Building the
// shelves here rather than in each client means one request instead of paging
// the whole library to shuffle a dozen posters, and both clients show the same
// shelves on the same day.
type homeResponse struct {
	Featured         *store.Item  `json:"featured"`
	ContinueWatching []store.Item `json:"continue_watching"`
	NextUp           []store.Item `json:"next_up"`
	RecentlyAdded    []store.Item `json:"recently_added"`
	Shelves          []shelf      `json:"shelves"`
	// ExpiresAt is the UTC instant at which this response goes stale: the
	// next 6am/6pm featured-pick change or the next server-local midnight
	// shelf rotation, whichever comes first. Clients reload at that moment
	// rather than guessing at Loom's schedule from their own clock.
	ExpiresAt string `json:"expires_at"`
}

// shelf is one rotating discovery row. The key is stable across days for a
// given shelf kind (or collection), so a client can use it as a list identity.
type shelf struct {
	Key   string       `json:"key"`
	Title string       `json:"title"`
	Items []store.Item `json:"items"`
}

const (
	homeRowLimit = 20
	shelfCount   = 3
	shelfItems   = 12
	// A shelf with a couple of stragglers reads as a mistake, so thin shelves
	// are skipped and another candidate takes the slot.
	minShelfItems   = 4
	quickWatchMaxMS = 90 * 60 * 1000
)

func (a *API) home(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now()
	var hero *store.Item
	pick, err := a.store.FeaturedPickAt(ctx, now)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if pick != nil {
		hero = &pick.Item
	}
	continueWatching, err := a.store.ContinueWatching(ctx, homeRowLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	nextUp, err := a.store.NextUp(ctx, homeRowLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recentlyAdded, err := a.store.RecentlyAdded(ctx, homeRowLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recentlyPlayed, err := a.store.RecentlyPlayed(ctx, homeRowLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	movies, shows, err := a.store.DiscoveryLibrary(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	collections, err := a.resolveCollections(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	shelves := discoveryShelves(movies, shows, collections, recentlyPlayed, epochDay(now))

	// The hero is already the biggest thing on the screen, so it leaves every
	// row. A shelf emptied by that is dropped rather than drawn as a heading.
	response := homeResponse{
		Featured:         hero,
		ContinueWatching: withoutHero(continueWatching, hero),
		NextUp:           withoutHero(nextUp, hero),
		RecentlyAdded:    withoutHero(recentlyAdded, hero),
		Shelves:          []shelf{},
		ExpiresAt:        homeExpiry(now).UTC().Format(time.RFC3339),
	}
	for _, s := range shelves {
		s.Items = withoutHero(s.Items, hero)
		if len(s.Items) > 0 {
			response.Shelves = append(response.Shelves, s)
		}
	}
	writeJSON(w, http.StatusOK, response)
}

// homeExpiry is the earlier of the next featured-pick boundary and the next
// server-local midnight, when the shelves rotate.
func homeExpiry(at time.Time) time.Time {
	year, month, day := at.Date()
	midnight := time.Date(year, month, day+1, 0, 0, 0, 0, at.Location())
	if pick := store.NextFeaturedPickTime(at); pick.Before(midnight) {
		return pick
	}
	return midnight
}

// epochDay counts server-local calendar days, so the shelves roll over at the
// server's midnight along with the rest of its clock-driven behaviour.
func epochDay(at time.Time) int64 {
	year, month, day := at.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC).Unix() / 86400
}

func withoutHero(items []store.Item, hero *store.Item) []store.Item {
	result := make([]store.Item, 0, len(items))
	for _, item := range items {
		if hero == nil || item.ID != hero.ID {
			result = append(result, item)
		}
	}
	return result
}

// discoveryShelves draws the day's shelves from a pool of candidates seeded by
// epochDay: stable all day, different tomorrow. Candidates that cannot fill a
// shelf drop out and the next takes the slot.
func discoveryShelves(
	movies, shows []store.Item, collections []collection, recentlyPlayed []store.Item, epochDay int64,
) []shelf {
	random := rand.New(rand.NewSource(epochDay)) //nolint:gosec // seeded on purpose for a stable daily shuffle
	shuffled := func(items []store.Item) []store.Item {
		out := slices.Clone(items)
		random.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		return out
	}
	library := slices.Concat(movies, shows)
	var unstarted []store.Item
	for _, item := range library {
		if !started(item) {
			unstarted = append(unstarted, item)
		}
	}
	var highlyRated []store.Item
	for _, item := range unstarted {
		if item.VoteAverage >= store.FeaturedRatingThreshold {
			highlyRated = append(highlyRated, item)
		}
	}
	var quick []store.Item
	for _, item := range movies {
		if !started(item) && item.DurationMS > 0 && item.DurationMS < quickWatchMaxMS {
			quick = append(quick, item)
		}
	}
	builders := []func() *shelf{
		func() *shelf { return genreSpotlight(unstarted, random, shuffled) },
		func() *shelf { return newShelf("unstarted", "New to You", shuffled(unstarted)) },
		func() *shelf { return newShelf("rated", "Highly Rated", shuffled(highlyRated)) },
		func() *shelf { return collectionSpotlight(collections, random) },
		func() *shelf { return newShelf("different", "Something Different", shuffled(library)) },
		// The store orders these by finish time; keep that order.
		func() *shelf { return newShelf("again", "Watch It Again", recentlyPlayed) },
		func() *shelf { return newShelf("quick", "A Quick Watch", shuffled(quick)) },
	}
	random.Shuffle(len(builders), func(i, j int) { builders[i], builders[j] = builders[j], builders[i] })
	var result []shelf
	for _, build := range builders {
		if built := build(); built != nil {
			result = append(result, *built)
			if len(result) == shelfCount {
				break
			}
		}
	}
	return result
}

// A show counts as started once any episode is watched; a movie once it has
// any playback state at all.
func started(item store.Item) bool {
	if item.Kind == "show" {
		return item.EpisodeCount > 0 && item.UnwatchedCount < item.EpisodeCount
	}
	return item.Progress != nil
}

func newShelf(key, title string, items []store.Item) *shelf {
	if len(items) < minShelfItems {
		return nil
	}
	return &shelf{Key: key, Title: title, Items: items[:min(len(items), shelfItems)]}
}

func genreSpotlight(unstarted []store.Item, random *rand.Rand, shuffled func([]store.Item) []store.Item) *shelf {
	byGenre := map[string][]store.Item{}
	for _, item := range unstarted {
		for _, genre := range item.Genres {
			byGenre[genre.Name] = append(byGenre[genre.Name], item)
		}
	}
	// Sorted so the seeded pick does not depend on map iteration order.
	var candidates []string
	for name, items := range byGenre {
		if len(items) >= minShelfItems {
			candidates = append(candidates, name)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Strings(candidates)
	pick := candidates[random.Intn(len(candidates))]
	return newShelf("genre", "Tonight: "+pick, shuffled(byGenre[pick]))
}

// Collections are served only with at least two owned members, and a two-movie
// franchise is still a real shelf, so this skips the usual floor.
func collectionSpotlight(collections []collection, random *rand.Rand) *shelf {
	if len(collections) == 0 {
		return nil
	}
	pick := collections[random.Intn(len(collections))]
	return &shelf{Key: "col-" + pick.Slug, Title: pick.Title, Items: pick.Items[:min(len(pick.Items), shelfItems)]}
}
