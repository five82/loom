package httpapi

import (
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/five82/loom/internal/collections"
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

// shelf is one rotating discovery row. The key names the shelf kind and, for
// kinds that pick a genre, decade, person, or collection, the pick itself, so
// a client using it as list identity never carries one day's row state onto a
// different set of posters tomorrow.
type shelf struct {
	Key   string       `json:"key"`
	Title string       `json:"title"`
	Items []store.Item `json:"items"`
}

const (
	homeRowLimit = 20
	// Continue Watching is capped tighter than the other rows: a movie
	// abandoned at twenty minutes three weeks ago is still resumable, but a
	// row of thirteen of them reads as a backlog rather than an invitation.
	homeContinueLimit = 8
	shelfCount        = 5
	shelfItems        = 12
	// A shelf with a couple of stragglers reads as a mistake, so thin shelves
	// are skipped and another candidate takes the slot.
	minShelfItems = 4
	// Watch It Again is allowed to run thinner, because viewing history is
	// slow to accumulate and two finished favourites are still worth a row.
	minAgainItems = 2
	// discoveryRatingFloor keeps the shelves reading as recommendations: the
	// library's handful of guilty pleasures stay reachable by browsing, but
	// nothing on the home screen suggests them.
	discoveryRatingFloor = 6.5
	// personShelfMinMovies is the floor for a "Starring" or "Directed by"
	// shelf, counted over the unstarted movies the shelf would actually hold.
	personShelfMinMovies = 4
	quickWatchMaxMS      = 90 * 60 * 1000
	longHaulMinMS        = 150 * 60 * 1000
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
	continueWatching, err := a.store.ContinueWatching(ctx, homeContinueLimit)
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
	movies, shows, shorts, err := a.store.DiscoveryLibrary(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	credits, err := a.store.HeadlineCredits(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resolved, err := a.resolveCollections(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// HDR is a collection for browsing but not a discovery hook: a shelf
	// titled after a video format says nothing about what to watch.
	var spotlight []collection
	for _, c := range resolved {
		if c.Slug != hdrCollectionSlug {
			spotlight = append(spotlight, c)
		}
	}
	shelves := discoveryShelves(discoveryInput{
		movies: movies, shows: shows, shorts: shorts, collections: spotlight,
		recentlyPlayed: recentlyPlayed, seed: watchedSeed(continueWatching, recentlyPlayed),
		credits: credits, now: now,
	})

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

// watchedSeed is the movie "Because You Watched" builds on: the most recently
// resumed movie, or failing that the most recently finished one. Episodes are
// passed over because a show's genres and credits say little about which
// movie to watch next.
func watchedSeed(continueWatching, recentlyPlayed []store.Item) *store.Item {
	for _, row := range [][]store.Item{continueWatching, recentlyPlayed} {
		for _, item := range row {
			if item.Kind == "movie" {
				return &item
			}
		}
	}
	return nil
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

// discoveryInput is everything the day's shelves are drawn from.
type discoveryInput struct {
	movies, shows, shorts []store.Item
	collections           []collection
	// recentlyPlayed arrives in the store's finish order and keeps it.
	recentlyPlayed []store.Item
	seed           *store.Item
	credits        []store.PersonCredit
	now            time.Time
}

// discoveryShelves draws the day's shelves from a pool of candidates seeded by
// the server-local day: stable all day, different tomorrow. Candidates that
// cannot fill a shelf drop out and the next takes the slot. A seasonal shelf,
// when the calendar offers one, always leads.
func discoveryShelves(in discoveryInput) []shelf {
	random := rand.New(rand.NewSource(epochDay(in.now))) //nolint:gosec // seeded on purpose for a stable daily shuffle
	shuffled := func(items []store.Item) []store.Item {
		out := slices.Clone(items)
		random.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		return out
	}
	library := slices.Concat(in.movies, in.shows)

	// The pool is what the generic shelves recommend from: not yet watched,
	// not a documentary, and rated well enough to stand behind. Documentaries
	// have a shelf of their own, and a nature series in "New to You" or a
	// making-of in "A Quick Watch" reads as filler.
	var pool, poolMovies []store.Item
	for _, item := range library {
		if !started(item) && !documentary(item) && item.VoteAverage >= discoveryRatingFloor {
			pool = append(pool, item)
			if item.Kind == "movie" {
				poolMovies = append(poolMovies, item)
			}
		}
	}
	var highlyRated, series, documentaries, family, quick, longHaul, unstartedShorts, different []store.Item
	for _, item := range pool {
		if item.VoteAverage >= store.FeaturedRatingThreshold {
			highlyRated = append(highlyRated, item)
			if item.Kind == "show" {
				series = append(series, item)
			}
		}
	}
	for _, item := range poolMovies {
		switch {
		case item.DurationMS > 0 && item.DurationMS < quickWatchMaxMS:
			quick = append(quick, item)
		case item.DurationMS > longHaulMinMS:
			longHaul = append(longHaul, item)
		}
		if item.ContentRating == "G" || item.ContentRating == "PG" {
			family = append(family, item)
		}
	}
	for _, item := range library {
		if documentary(item) {
			if !started(item) {
				documentaries = append(documentaries, item)
			}
		} else if item.VoteAverage >= discoveryRatingFloor {
			different = append(different, item)
		}
	}
	for _, item := range in.shorts {
		if !started(item) {
			unstartedShorts = append(unstartedShorts, item)
		}
	}

	builders := []func() *shelf{
		func() *shelf { return genreSpotlight(pool, random, shuffled) },
		func() *shelf { return decadeSpotlight(poolMovies, random, shuffled) },
		func() *shelf { return personSpotlight(poolMovies, in.credits, random, shuffled) },
		func() *shelf { return becauseYouWatched(in.seed, poolMovies, in.credits, shuffled) },
		func() *shelf { return newShelf("unstarted", "New to You", shuffled(pool), minShelfItems) },
		func() *shelf { return newShelf("rated", "Highly Rated", shuffled(highlyRated), minShelfItems) },
		func() *shelf { return newShelf("series", "Start a Series", shuffled(series), minShelfItems) },
		func() *shelf {
			return newShelf("documentary", "Nature & Documentary", shuffled(documentaries), minShelfItems)
		},
		func() *shelf { return newShelf("family", "Family Night", shuffled(family), minShelfItems) },
		func() *shelf { return collectionSpotlight(in.collections, random) },
		func() *shelf { return newShelf("different", "Something Different", shuffled(different), minShelfItems) },
		func() *shelf { return newShelf("again", "Watch It Again", in.recentlyPlayed, minAgainItems) },
		func() *shelf { return newShelf("quick", "A Quick Watch", shuffled(quick), minShelfItems) },
		func() *shelf { return newShelf("epic", "The Long Haul", shuffled(longHaul), minShelfItems) },
		func() *shelf { return newShelf("shorts", "Short & Sweet", shuffled(unstartedShorts), minShelfItems) },
	}
	random.Shuffle(len(builders), func(i, j int) { builders[i], builders[j] = builders[j], builders[i] })
	var result []shelf
	if seasonal := seasonalShelf(in.movies, in.now, shuffled); seasonal != nil {
		result = append(result, *seasonal)
	}
	for _, build := range builders {
		if len(result) == shelfCount {
			break
		}
		if built := build(); built != nil {
			result = append(result, *built)
		}
	}
	return result
}

// A show counts as started once any episode is watched; a movie once it has
// been played or watched past the resume floor. A playback row at the opening
// seconds is a viewer who opened the player and backed out, and treating that
// as watched would quietly drop the best of the library from every shelf.
func started(item store.Item) bool {
	if item.Kind == "show" {
		return item.EpisodeCount > 0 && item.UnwatchedCount < item.EpisodeCount
	}
	progress := item.Progress
	if progress == nil {
		return false
	}
	return progress.Played ||
		(progress.DurationMS > 0 && float64(progress.PositionMS)/float64(progress.DurationMS) >= 0.05)
}

func documentary(item store.Item) bool {
	return slices.ContainsFunc(item.Genres, func(g store.Genre) bool {
		return strings.EqualFold(g.Name, "Documentary")
	})
}

func newShelf(key, title string, items []store.Item, floor int) *shelf {
	if len(items) < floor {
		return nil
	}
	return &shelf{Key: key, Title: title, Items: items[:min(len(items), shelfItems)]}
}

// weightedPick chooses a key with probability proportional to how many items
// stand behind it, so the library's big genres and decades come up more often
// than a corner with four films. Keys are sorted first so the seeded pick does
// not depend on map iteration order.
func weightedPick(groups map[string][]store.Item, random *rand.Rand) (string, bool) {
	var keys []string
	total := 0
	for key, items := range groups {
		if len(items) >= minShelfItems {
			keys = append(keys, key)
			total += len(items)
		}
	}
	if len(keys) == 0 {
		return "", false
	}
	sort.Strings(keys)
	roll := random.Intn(total)
	for _, key := range keys {
		roll -= len(groups[key])
		if roll < 0 {
			return key, true
		}
	}
	return keys[len(keys)-1], true
}

func genreSpotlight(pool []store.Item, random *rand.Rand, shuffled func([]store.Item) []store.Item) *shelf {
	byGenre := map[string][]store.Item{}
	for _, item := range pool {
		for _, genre := range item.Genres {
			byGenre[genre.Name] = append(byGenre[genre.Name], item)
		}
	}
	pick, ok := weightedPick(byGenre, random)
	if !ok {
		return nil
	}
	return newShelf("genre-"+pick, "Tonight: "+pick, shuffled(byGenre[pick]), minShelfItems)
}

func decadeSpotlight(movies []store.Item, random *rand.Rand, shuffled func([]store.Item) []store.Item) *shelf {
	byDecade := map[string][]store.Item{}
	for _, item := range movies {
		if item.Year > 0 {
			decade := fmt.Sprint(item.Year / 10 * 10)
			byDecade[decade] = append(byDecade[decade], item)
		}
	}
	pick, ok := weightedPick(byDecade, random)
	if !ok {
		return nil
	}
	return newShelf("decade-"+pick, "Back to the "+decadeLabel(pick), shuffled(byDecade[pick]), minShelfItems)
}

// decadeLabel writes 1980 as '80s and 2010 as 2010s, the way people say them.
func decadeLabel(decade string) string {
	if strings.HasPrefix(decade, "19") {
		return "'" + decade[2:] + "s"
	}
	return decade + "s"
}

// headlinePeople groups the headline credits of the given movies by person and
// role, so one person directing some films and starring in others gets a shelf
// for each rather than a muddled one.
type headlinePerson struct {
	key, name, role string
	items           []store.Item
}

func headlinePeople(movies []store.Item, credits []store.PersonCredit) []headlinePerson {
	byID := make(map[int64]store.Item, len(movies))
	for _, item := range movies {
		byID[item.ID] = item
	}
	grouped := map[string]*headlinePerson{}
	var order []string
	for _, credit := range credits {
		item, ok := byID[credit.ItemID]
		if !ok {
			continue
		}
		key := fmt.Sprintf("person-%d-%s", credit.PersonID, credit.Role)
		person, ok := grouped[key]
		if !ok {
			person = &headlinePerson{key: key, name: credit.Name, role: credit.Role}
			grouped[key] = person
			order = append(order, key)
		}
		person.items = append(person.items, item)
	}
	result := make([]headlinePerson, 0, len(order))
	for _, key := range order {
		result = append(result, *grouped[key])
	}
	return result
}

func personSpotlight(
	movies []store.Item, credits []store.PersonCredit, random *rand.Rand, shuffled func([]store.Item) []store.Item,
) *shelf {
	var candidates []headlinePerson
	for _, person := range headlinePeople(movies, credits) {
		if len(person.items) >= personShelfMinMovies {
			candidates = append(candidates, person)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].key < candidates[j].key })
	pick := candidates[random.Intn(len(candidates))]
	title := "Starring " + pick.name
	if pick.role == "director" {
		title = "Directed by " + pick.name
	}
	return newShelf(pick.key, title, shuffled(pick.items), personShelfMinMovies)
}

// becauseYouWatched offers unwatched movies that share a headline credit with
// the seed - its director or one of its top-billed actors - or at least two of
// its genres. Credits come first because a shared lead is the stronger pull:
// after The Bourne Identity, the other Matt Damon films, then the thrillers.
func becauseYouWatched(
	seed *store.Item, movies []store.Item, credits []store.PersonCredit, shuffled func([]store.Item) []store.Item,
) *shelf {
	if seed == nil {
		return nil
	}
	seedPeople := map[int64]bool{}
	for _, credit := range credits {
		if credit.ItemID == seed.ID {
			seedPeople[credit.PersonID] = true
		}
	}
	related := map[int64]bool{}
	for _, credit := range credits {
		if seedPeople[credit.PersonID] {
			related[credit.ItemID] = true
		}
	}
	var byCredit, byGenre []store.Item
	for _, item := range movies {
		switch {
		case item.ID == seed.ID:
		case related[item.ID]:
			byCredit = append(byCredit, item)
		case sharedGenres(seed.Genres, item.Genres) >= 2:
			byGenre = append(byGenre, item)
		}
	}
	items := slices.Concat(shuffled(byCredit), shuffled(byGenre))
	return newShelf(fmt.Sprintf("because-%d", seed.ID), "Because You Watched "+seed.Title, items, minShelfItems)
}

func sharedGenres(a, b []store.Genre) int {
	count := 0
	for _, genre := range a {
		if slices.ContainsFunc(b, func(other store.Genre) bool { return other.ID == genre.ID }) {
			count++
		}
	}
	return count
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

// seasonalShelf leads the home screen through October with the horror films
// and through December with the holiday films. Both draw on the whole movie
// library, watched or not and however rated, because these are the weeks for
// rewatching them.
func seasonalShelf(movies []store.Item, now time.Time, shuffled func([]store.Item) []store.Item) *shelf {
	var items []store.Item
	switch now.Month() {
	case time.October:
		for _, item := range movies {
			if slices.ContainsFunc(item.Genres, func(g store.Genre) bool { return strings.EqualFold(g.Name, "Horror") }) {
				items = append(items, item)
			}
		}
		return newShelf("halloween", "Halloween", shuffled(items), minShelfItems)
	case time.December:
		for _, item := range movies {
			if slices.Contains(collections.Holiday, item.TMDBID) {
				items = append(items, item)
			}
		}
		return newShelf("holiday", "Holiday Movies", shuffled(items), minShelfItems)
	default:
		return nil
	}
}
