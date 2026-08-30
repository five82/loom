package channels

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/five82/loom/internal/collections"
	"github.com/five82/loom/internal/store"
)

// The generator is driven entirely by the now it is handed, so every test runs
// against this fixed instant, a Saturday evening, in a zone with no offset so
// block times read the same as the stored UTC boundaries.
var testNow = time.Date(2026, 8, 29, 20, 0, 0, 0, time.UTC)

var genreIDs = map[string]int64{
	"Action": 28, "Comedy": 35, "Drama": 18, "Family": 10751, "Animation": 16,
	"Science Fiction": 878, "Western": 37,
}

type catalog struct {
	t          *testing.T
	ctx        context.Context
	store      *store.Store
	libs       map[string]int64
	scans      map[string]int64
	nextSource int
	// sources records which test source every item was added for, so a
	// schedule can be checked against the grid.
	sources map[int64]string
}

func newCatalog(t *testing.T) *catalog {
	t.Helper()
	ctx := context.Background()
	opened, err := store.Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	c := &catalog{t: t, ctx: ctx, store: opened, libs: map[string]int64{}, scans: map[string]int64{},
		sources: map[int64]string{}}
	for _, kind := range []string{"movies", "shorts", "tv"} {
		c.libs[kind], c.scans[kind], err = opened.StartScan(ctx, kind, "/"+kind)
		if err != nil {
			t.Fatal(err)
		}
	}
	return c
}

// addMovie stores an available movie in the movies library with a file of the
// given runtime.
func (c *catalog) addMovie(title string, minutes int, tmdbID int64, rating string, genres ...string) int64 {
	c.t.Helper()
	return c.addFilm("movies", title, minutes, tmdbID, rating, genres...)
}

func (c *catalog) addShort(title string, minutes int, tmdbID int64) int64 {
	c.t.Helper()
	return c.addFilm("shorts", title, minutes, tmdbID, "")
}

func (c *catalog) addFilm(library, title string, minutes int, tmdbID int64, rating string, genres ...string) int64 {
	c.t.Helper()
	return c.addFilmFrom(library, title, minutes, tmdbID, rating, 0, 0, genres...)
}

// addReleasedMovie is addMovie with a release year and TMDB vote average, for
// the era and pedigree pools.
func (c *catalog) addReleasedMovie(title string, tmdbID int64, year int, vote float64, genres ...string) int64 {
	c.t.Helper()
	return c.addFilmFrom("movies", title, 60, tmdbID, "PG", year, vote, genres...)
}

func (c *catalog) addFilmFrom(
	library, title string, minutes int, tmdbID int64, rating string, year int, vote float64, genres ...string,
) int64 {
	c.t.Helper()
	c.nextSource++
	id, err := c.store.UpsertItem(c.ctx, store.ItemInput{
		LibraryID: c.libs[library], SourceKey: fmt.Sprintf("film-%d", c.nextSource), Kind: "movie",
		Title: title, ScanID: c.scans[library],
	})
	if err != nil {
		c.t.Fatal(err)
	}
	c.addMedia(id, "/"+library+"/"+title+".mkv", minutes)
	var genreList []store.Genre
	for _, genre := range genres {
		genreList = append(genreList, store.Genre{ID: genreIDs[genre], Name: genre})
	}
	if err := c.store.UpdateMetadata(c.ctx, id, store.MetadataUpdate{
		TMDBID: tmdbID, Title: title, Genres: genreList, ContentRating: rating, Year: year, VoteAverage: vote,
	}); err != nil {
		c.t.Fatal(err)
	}
	c.sources[id] = title
	return id
}

type episode struct {
	season  int
	number  int
	minutes int
}

// addShow stores a show with the given TMDB id, one season per distinct season
// number, and the given episodes. It returns the episode ids in the order
// given.
func (c *catalog) addShow(title string, tmdbID int64, episodes ...episode) []int64 {
	c.t.Helper()
	tv := c.libs["tv"]
	showID, err := c.store.UpsertItem(c.ctx, store.ItemInput{
		LibraryID: tv, SourceKey: title, Kind: "show", Title: title, ScanID: c.scans["tv"],
	})
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.store.UpdateMetadata(c.ctx, showID, store.MetadataUpdate{TMDBID: tmdbID, Title: title}); err != nil {
		c.t.Fatal(err)
	}
	seasons := map[int]int64{}
	var ids []int64
	for _, spec := range episodes {
		seasonID, ok := seasons[spec.season]
		if !ok {
			seasonID, err = c.store.UpsertItem(c.ctx, store.ItemInput{
				LibraryID: tv, ParentID: &showID,
				SourceKey: fmt.Sprintf("%s/season-%d", title, spec.season), Kind: "season",
				Title: fmt.Sprintf("Season %d", spec.season), SeasonNumber: spec.season, ScanID: c.scans["tv"],
			})
			if err != nil {
				c.t.Fatal(err)
			}
			seasons[spec.season] = seasonID
		}
		key := fmt.Sprintf("%s/S%02dE%02d", title, spec.season, spec.number)
		id, err := c.store.UpsertItem(c.ctx, store.ItemInput{
			LibraryID: tv, ParentID: &seasonID, SourceKey: key, Kind: "episode", Title: key,
			SeasonNumber: spec.season, EpisodeNumber: spec.number, ScanID: c.scans["tv"],
		})
		if err != nil {
			c.t.Fatal(err)
		}
		c.addMedia(id, "/tv/"+key+".mkv", spec.minutes)
		c.sources[id] = title
		ids = append(ids, id)
	}
	return ids
}

// episodes adds count half-hour episodes across seasons of ten.
func (c *catalog) episodes(title string, tmdbID int64, count int) []int64 {
	c.t.Helper()
	specs := make([]episode, 0, count)
	for index := range count {
		specs = append(specs, episode{season: index/10 + 1, number: index%10 + 1, minutes: 30})
	}
	return c.addShow(title, tmdbID, specs...)
}

func (c *catalog) addMedia(itemID int64, path string, minutes int) {
	c.t.Helper()
	if _, err := c.store.UpsertMedia(c.ctx, store.MediaFile{
		ItemID: itemID, Path: path, Size: 100, MTimeNS: 20,
		DurationMS: int64(minutes) * 60_000, Container: "matroska", LastSeenScanID: c.scans["movies"],
	}, []store.Stream{{
		Index: 0, Kind: "video", Codec: "hevc", Width: 1920, Height: 1080, DynamicRange: "sdr", IsDefault: true,
	}}, nil); err != nil {
		c.t.Fatal(err)
	}
}

func (c *catalog) generator(lineup ...Channel) *Generator {
	return NewWith(c.store, lineup, time.UTC)
}

func (c *catalog) channelByKey(key string) store.Channel {
	c.t.Helper()
	channels, err := c.store.Channels(c.ctx)
	if err != nil {
		c.t.Fatal(err)
	}
	for _, channel := range channels {
		if channel.Key == key {
			return channel
		}
	}
	c.t.Fatalf("channel %q is not in the lineup", key)
	return store.Channel{}
}

// programs returns one channel's whole stored schedule, oldest first.
func (c *catalog) programs(key string) []store.ChannelProgram {
	c.t.Helper()
	channel := c.channelByKey(key)
	_, byChannel, err := c.store.ChannelLineup(c.ctx, "0000", "9999")
	if err != nil {
		c.t.Fatal(err)
	}
	return byChannel[channel.ID]
}

// aired returns the source names of a channel's programs in order.
func (c *catalog) aired(key string) []string {
	c.t.Helper()
	var result []string
	for _, program := range c.programs(key) {
		result = append(result, c.sources[program.Item.ID])
	}
	return result
}

// assertBackToBack checks the invariants every schedule keeps: programs abut,
// the first is already under way at now, and the last reaches the horizon
// without another starting past it.
func assertBackToBack(t *testing.T, key string, programs []store.ChannelProgram, now time.Time) {
	t.Helper()
	if len(programs) == 0 {
		t.Fatalf("channel %q has no programs", key)
	}
	if programs[0].StartsAt >= store.ChannelTime(now) || programs[0].EndsAt <= store.ChannelTime(now) {
		t.Fatalf("channel %q first program runs %s to %s, want it under way at %s",
			key, programs[0].StartsAt, programs[0].EndsAt, store.ChannelTime(now))
	}
	for index, program := range programs {
		if index > 0 && program.StartsAt != programs[index-1].EndsAt {
			t.Fatalf("channel %q has a gap before program %d: %s after %s",
				key, index, program.StartsAt, programs[index-1].EndsAt)
		}
		if program.EndsAt <= program.StartsAt {
			t.Fatalf("channel %q program %d ends at %s, starts at %s", key, index, program.EndsAt, program.StartsAt)
		}
	}
	horizon := store.ChannelTime(now.Add(Horizon))
	if last := programs[len(programs)-1]; last.EndsAt < horizon {
		t.Fatalf("channel %q is scheduled only to %s", key, last.EndsAt)
	}
	if len(programs) > 1 && programs[len(programs)-2].EndsAt >= horizon {
		t.Fatalf("channel %q scheduled past the horizon", key)
	}
}

func TestLineupIsValid(t *testing.T) {
	if err := validate(Lineup); err != nil {
		t.Fatal(err)
	}
	if len(Lineup) != 27 {
		t.Fatalf("lineup has %d channels", len(Lineup))
	}
}

func TestValidateRejectsBrokenGrids(t *testing.T) {
	run := show("run", 1)
	cases := map[string][]Channel{
		"duplicate key": {
			{Key: "a", Name: "A", Blocks: allDay(run)},
			{Key: "a", Name: "B", Blocks: allDay(run)},
		},
		"gap in the daily grid": {{Key: "a", Name: "A", Blocks: []Block{
			daily(gridStart, 12*60, run), daily(13*60, gridEnd, run),
		}}},
		"grid ending early": {{Key: "a", Name: "A", Blocks: []Block{daily(gridStart, 12*60, run)}}},
		"source name shared by two sources": {{Key: "a", Name: "A", Blocks: []Block{
			daily(gridStart, 12*60, show("run", 1)), daily(12*60, gridEnd, show("run", 2)),
		}}},
		"empty source": {{Key: "a", Name: "A", Blocks: allDay(&Source{Name: "nothing"})}},
		"unknown days": {{Key: "a", Name: "A", Blocks: []Block{
			daily(gridStart, gridEnd, run), on("mon", 12*60, 13*60, run),
		}}},
	}
	for name, lineup := range cases {
		if err := validate(lineup); err == nil {
			t.Errorf("%s: validated", name)
		}
	}
	if err := validate([]Channel{{Key: "a", Name: "A", Blocks: []Block{
		daily(gridStart, 12*60, run), daily(12*60, gridEnd, run), on("weekend", 12*60, 13*60, show("other", 2)),
	}}}); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileFollowsTheLineupAndKeepsNumbers(t *testing.T) {
	c := newCatalog(t)
	first := []Channel{
		{Key: "one", Name: "One", Blocks: allDay(show("run", 1))},
		{Key: "two", Name: "Two", Blocks: allDay(show("run", 2))},
		{Key: "three", Name: "Three", Blocks: allDay(show("run", 3))},
	}
	stats, err := c.generator(first...).Reconcile(c.ctx, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ChannelsCreated != 3 {
		t.Fatalf("created %d channels, want 3", stats.ChannelsCreated)
	}
	channels, err := c.store.Channels(c.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range first {
		if channels[index].Key != want.Key || channels[index].Number != index+1 || channels[index].Name != want.Name {
			t.Fatalf("channel %d = %+v, want %s at %d", index, channels[index], want.Key, index+1)
		}
	}
	stats, err = c.generator(first...).Reconcile(c.ctx, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ChannelsCreated != 0 || stats.ChannelsRemoved != 0 {
		t.Fatalf("second reconcile = %+v", stats)
	}

	// Dropping a channel, renaming one, and adding one: the survivors keep
	// their numbers and the newcomer takes the next unused one.
	second := []Channel{
		{Key: "three", Name: "Three", Blocks: allDay(show("run", 3))},
		{Key: "one", Name: "Uno", Blocks: allDay(show("run", 1))},
		{Key: "four", Name: "Four", Blocks: allDay(show("run", 4))},
	}
	stats, err = c.generator(second...).Reconcile(c.ctx, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ChannelsCreated != 1 || stats.ChannelsRemoved != 1 {
		t.Fatalf("lineup edit = %+v", stats)
	}
	channels, err = c.store.Channels(c.ctx)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		number int
		key    string
		name   string
	}
	want := []row{{1, "one", "Uno"}, {3, "three", "Three"}, {4, "four", "Four"}}
	if len(channels) != len(want) {
		t.Fatalf("lineup = %+v, want %+v", channels, want)
	}
	for index, channel := range channels {
		if (row{channel.Number, channel.Key, channel.Name}) != want[index] {
			t.Fatalf("channel %d = %+v, want %+v", index, channel, want[index])
		}
	}
}

func TestExtendFillsHorizonJoinedInProgress(t *testing.T) {
	c := newCatalog(t)
	for _, title := range []string{"Alpha", "Beta", "Gamma"} {
		c.addMovie(title, 100, int64(len(title)), "PG", "Action")
	}
	lineup := []Channel{{Key: "action", Name: "Action", Blocks: allDay(movies("features", "Action"))}}
	generator := c.generator(lineup...)
	if _, err := generator.Update(c.ctx, testNow); err != nil {
		t.Fatal(err)
	}
	assertBackToBack(t, "action", c.programs("action"), testNow)

	// A second run at the same instant has nothing to add.
	stats, err := generator.Update(c.ctx, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ProgramsAdded != 0 || stats.ProgramsPruned != 0 || stats.ChannelsCreated != 0 {
		t.Fatalf("repeat update = %+v, want no changes", stats)
	}
}

func TestExtendKeepsHistoryAndAppendsToTheTail(t *testing.T) {
	c := newCatalog(t)
	for _, title := range []string{"Alpha", "Beta", "Gamma"} {
		c.addMovie(title, 100, int64(len(title)), "PG", "Action")
	}
	lineup := []Channel{{Key: "action", Name: "Action", Blocks: allDay(movies("features", "Action"))}}
	generator := c.generator(lineup...)
	if _, err := generator.Update(c.ctx, testNow); err != nil {
		t.Fatal(err)
	}
	before := c.programs("action")
	later := testNow.Add(12 * time.Hour)
	stats, err := generator.Update(c.ctx, later)
	if err != nil {
		t.Fatal(err)
	}
	// Finished programs are history a shuffle consults, so nothing is pruned
	// yet; everything stayed exactly where it was, and the tail grew.
	if stats.ProgramsPruned != 0 || stats.ProgramsAdded == 0 {
		t.Fatalf("update after twelve hours = %+v", stats)
	}
	after := c.programs("action")
	for index, program := range before {
		if after[index].ID != program.ID || after[index].StartsAt != program.StartsAt {
			t.Fatalf("program %d moved: %+v, want %+v", index, after[index], program)
		}
	}
	if after[len(before)].StartsAt != after[len(before)-1].EndsAt {
		t.Fatalf("new tail starts at %s, previous program ends at %s",
			after[len(before)].StartsAt, after[len(before)-1].EndsAt)
	}
	if last := after[len(after)-1]; last.EndsAt < store.ChannelTime(later.Add(Horizon)) {
		t.Fatalf("schedule reaches only %s", last.EndsAt)
	}

	// A month on, exactly the programs that ended before the retention window
	// are gone.
	monthLater := later.Add(retention)
	stats, err = generator.Update(c.ctx, monthLater)
	if err != nil {
		t.Fatal(err)
	}
	expired := 0
	for _, program := range after {
		if program.EndsAt < store.ChannelTime(monthLater.Add(-retention)) {
			expired++
		}
	}
	if expired == 0 || stats.ProgramsPruned != expired {
		t.Fatalf("a month later pruned %d programs, want %d of %d", stats.ProgramsPruned, expired, len(after))
	}
}

// Loom being down long enough for a schedule to run out leaves a gap. The
// channel is joined in progress again rather than replaying the hours nobody
// watched or starting a program on the instant Loom came back.
func TestExtendRejoinsAfterAGap(t *testing.T) {
	c := newCatalog(t)
	episodes := c.episodes("Show", 1, 40)
	lineup := []Channel{{Key: "show", Name: "Show", Blocks: allDay(show("run", 1))}}
	generator := c.generator(lineup...)
	if _, err := generator.Update(c.ctx, testNow); err != nil {
		t.Fatal(err)
	}
	beforeOutage := c.programs("show")
	lastBefore := beforeOutage[len(beforeOutage)-1].Item.ID
	outage := testNow.Add(3 * Horizon)
	if _, err := generator.Update(c.ctx, outage); err != nil {
		t.Fatal(err)
	}
	// The programs from before the outage are retained history; the rejoined
	// schedule is what ends after the outage.
	var programs []store.ChannelProgram
	for _, program := range c.programs("show") {
		if program.EndsAt > store.ChannelTime(outage) {
			programs = append(programs, program)
		}
	}
	assertBackToBack(t, "show", programs, outage)
	// An episode on a channel is listed outside its show, so it names the show.
	if programs[0].Item.SeriesTitle != "Show" {
		t.Fatalf("lineup episode series_title = %q, want %q", programs[0].Item.SeriesTitle, "Show")
	}
	// The run picks up after the last episode scheduled before the outage.
	for index, id := range episodes {
		if id == lastBefore && programs[0].Item.ID != episodes[(index+1)%len(episodes)] {
			t.Fatalf("rejoined at item %d after %d", programs[0].Item.ID, lastBefore)
		}
	}
}

func TestShowRunsSeasonsInOrderThenSpecialsAndLoops(t *testing.T) {
	c := newCatalog(t)
	// Seven-hour episodes keep a day's schedule short enough to read, and the
	// out-of-order seeding proves the channel sorts rather than replays.
	ids := c.addShow("Show", 1,
		episode{season: 0, number: 1, minutes: 420},
		episode{season: 2, number: 1, minutes: 420},
		episode{season: 1, number: 2, minutes: 420},
		episode{season: 1, number: 1, minutes: 420})
	order := []int64{ids[3], ids[2], ids[1], ids[0]}
	lineup := []Channel{{Key: "show", Name: "Show", Blocks: allDay(show("run", 1))}}
	generator := c.generator(lineup...)
	if _, err := generator.Update(c.ctx, testNow.Add(-20*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := generator.Update(c.ctx, testNow); err != nil {
		t.Fatal(err)
	}
	programs := c.programs("show")
	if len(programs) < len(order)+1 {
		t.Fatalf("scheduled only %d programs", len(programs))
	}
	// Wherever the run was joined, every program is followed by the next in
	// season order, the special last, and then the top again.
	for index := 1; index < len(programs); index++ {
		previous, current := programs[index-1].Item.ID, programs[index].Item.ID
		for position, id := range order {
			if id == previous && current != order[(position+1)%len(order)] {
				t.Fatalf("program %d aired item %d after %d", index, current, previous)
			}
		}
		if programs[index].StartsAt != programs[index-1].EndsAt {
			t.Fatalf("continued schedule has a gap at program %d", index)
		}
	}
}

// A new channel is born mid-run, and the same lineup on a fresh catalog is
// born in the same place.
func TestNewChannelStartsMidRunDeterministically(t *testing.T) {
	var firsts []int
	for range 2 {
		c := newCatalog(t)
		episodes := c.episodes("South Park", southPark, 100)
		lineup := []Channel{{Key: "south-park", Name: "South Park", Blocks: allDay(show("run", southPark))}}
		if _, err := c.generator(lineup...).Update(c.ctx, testNow); err != nil {
			t.Fatal(err)
		}
		first := c.programs("south-park")[0].Item.ID
		for index, id := range episodes {
			if id == first {
				firsts = append(firsts, index)
			}
		}
	}
	if len(firsts) != 2 || firsts[0] != firsts[1] {
		t.Fatalf("fresh catalogs joined at episodes %v", firsts)
	}
	if firsts[0] == 0 {
		t.Fatal("a new channel started at the pilot")
	}
}

func TestBlocksFollowTheLocalGridWithSoftBoundaries(t *testing.T) {
	c := newCatalog(t)
	// Forty-five minute episodes never divide a block evenly, so programs
	// straddle every boundary.
	specs := make([]episode, 0, 60)
	for index := range 60 {
		specs = append(specs, episode{season: 1, number: index + 1, minutes: 45})
	}
	c.addShow("Day", 1, specs...)
	c.addShow("Night", 2, specs...)
	c.addShow("Saturday", 3, specs...)
	spec := Channel{Key: "grid", Name: "Grid", Blocks: []Block{
		daily(gridStart, 18*60, show("day", 1)),
		daily(18*60, gridEnd, show("night", 2)),
		on("sat", 20*60, 22*60, show("saturday", 3)),
	}}
	generator := c.generator(spec)
	// Saturday 20:00 through Sunday 20:00 covers the weekend block, the
	// overnight wrap, and the whole daily grid.
	if _, err := generator.Update(c.ctx, testNow); err != nil {
		t.Fatal(err)
	}
	programs := c.programs("grid")
	assertBackToBack(t, "grid", programs, testNow)
	counts := map[string]int{}
	for index, program := range programs {
		// The joined program is chosen for the block at now and then backed
		// up to look under way, so it can begin before its block does.
		if index == 0 {
			continue
		}
		starts, err := store.ParseChannelTime(program.StartsAt)
		if err != nil {
			t.Fatal(err)
		}
		want := spec.blockAt(generator.local(starts)).Source.Name
		if c.sources[program.Item.ID] != map[string]string{"day": "Day", "night": "Night", "saturday": "Saturday"}[want] {
			t.Fatalf("program %d at %s aired %q in the %q block", index, program.StartsAt,
				c.sources[program.Item.ID], want)
		}
		counts[want]++
	}
	if counts["saturday"] < 2 || counts["night"] < 10 || counts["day"] < 10 {
		t.Fatalf("block counts = %v", counts)
	}
	// The block that starts at 18:00 on Sunday is not entered until the
	// program that began inside the day block finishes: a soft boundary.
	crossed := false
	for index := 1; index < len(programs); index++ {
		previous, current := c.sources[programs[index-1].Item.ID], c.sources[programs[index].Item.ID]
		if previous == "Day" && current == "Night" {
			if programs[index].StartsAt == "2026-08-30T18:00:00Z" {
				t.Fatalf("the evening block started on the minute at %s", programs[index].StartsAt)
			}
			crossed = true
		}
	}
	if !crossed {
		t.Fatal("the schedule never crossed from the day block into the evening")
	}
}

func TestShuffledSourceAirsEverythingBeforeRepeating(t *testing.T) {
	c := newCatalog(t)
	// Six long movies make four programs a day, so three days cross a cycle
	// boundary at least once.
	for _, title := range []string{"Alpha", "Beta", "Gamma", "Delta", "Epsilon", "Zeta"} {
		c.addMovie(title, 400, int64(len(title))*7, "R", "Drama")
	}
	lineup := []Channel{{Key: "cinema", Name: "Cinema", Blocks: allDay(movies("features", "Drama"))}}
	generator := c.generator(lineup...)
	// Retention trims the stored schedule as the days pass, so the run is
	// collected day by day, keyed by program id.
	seen := map[int64]bool{}
	var sequence []string
	for day := range 4 {
		if _, err := generator.Update(c.ctx, testNow.Add(time.Duration(day)*Horizon)); err != nil {
			t.Fatal(err)
		}
		for _, program := range c.programs("cinema") {
			if seen[program.ID] {
				continue
			}
			seen[program.ID] = true
			sequence = append(sequence, c.sources[program.Item.ID])
		}
	}
	if len(sequence) < 13 {
		t.Fatalf("collected only %d programs: %v", len(sequence), sequence)
	}
	// The channel was joined partway through its first cycle, so the run is a
	// short prefix of distinct titles followed by whole cycles, each a
	// permutation of the six.
	if !cyclesOf(sequence, 6) {
		t.Fatalf("run is not a prefix followed by whole cycles: %v", sequence)
	}
}

func cyclesOf(sequence []string, size int) bool {
	distinct := func(part []string) bool {
		seen := map[string]bool{}
		for _, title := range part {
			if seen[title] {
				return false
			}
			seen[title] = true
		}
		return true
	}
	for prefix := 1; prefix <= size; prefix++ {
		if prefix > len(sequence) || !distinct(sequence[:prefix]) {
			continue
		}
		ok := true
		for start := prefix; start < len(sequence); start += size {
			end := min(start+size, len(sequence))
			if !distinct(sequence[start:end]) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// A marathon shows films the channel's shuffle also draws on. Those count as
// aired for the shuffle's current cycle, so the weekend does not cause the
// same film to come round again days later.
func TestShuffleSkipsWhatAMarathonAiredThisCycle(t *testing.T) {
	c := newCatalog(t)
	for index, title := range []string{"Alpha", "Beta", "Gamma", "Delta"} {
		c.addMovie(title, 400, int64(index+1), "PG", "Science Fiction")
	}
	spec := Channel{Key: "sci-fi", Name: "Sci-Fi", Blocks: []Block{
		daily(gridStart, gridEnd, movies("features", "Science Fiction")),
		on("sat", 20*60, gridEnd, &Source{Name: "marathon", Titles: []int64{1, 2}}),
	}}
	generator := c.generator(spec)
	if _, err := generator.Update(c.ctx, testNow); err != nil {
		t.Fatal(err)
	}
	aired := c.aired("sci-fi")
	// The marathon runs Alpha and Beta from Saturday evening into Sunday
	// morning, and the shuffle that follows reaches for the two films that
	// have not aired.
	if len(aired) < 4 || aired[0] != "Alpha" || aired[1] != "Beta" {
		t.Fatalf("aired %v", aired)
	}
	rest := map[string]bool{aired[2]: true, aired[3]: true}
	if !rest["Gamma"] || !rest["Delta"] {
		t.Fatalf("after the marathon the shuffle aired %v, want Gamma and Delta", aired[2:4])
	}
}

func TestInterstitialFollowsEveryFeature(t *testing.T) {
	c := newCatalog(t)
	c.addMovie("Feature A", 100, 1, "G", "Family")
	c.addMovie("Feature B", 100, 2, "PG", "Animation")
	c.addShort("Short A", 5, 3)
	c.addShort("Short B", 5, 4)
	lineup := []Channel{{Key: "family", Name: "Family", Blocks: []Block{{
		Start: gridStart, End: gridEnd,
		Source:       &Source{Name: "features", Genres: []string{"Family", "Animation"}, Ratings: []string{"G", "PG"}, Shuffle: true},
		Interstitial: &Source{Name: "shorts", Shorts: true, Shuffle: true},
	}}}}
	generator := c.generator(lineup...)
	if _, err := generator.Update(c.ctx, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := generator.Update(c.ctx, testNow.Add(20*time.Hour)); err != nil {
		t.Fatal(err)
	}
	aired := c.aired("family")
	if len(aired) < 10 {
		t.Fatalf("aired only %v", aired)
	}
	for index := 1; index < len(aired); index++ {
		previous, current := aired[index-1][:5] == "Short", aired[index][:5] == "Short"
		if previous == current {
			t.Fatalf("program %d: %q follows %q", index, aired[index], aired[index-1])
		}
	}
}

func TestSourcePoolsFilterByGenreRatingYearVoteAndLibrary(t *testing.T) {
	c := newCatalog(t)
	c.addMovie("Kids", 60, 1, "G", "Family")
	c.addMovie("Teens", 60, 2, "PG-13", "Family")
	c.addMovie("Space", 60, 3, "PG", "Science Fiction")
	c.addShort("Tiny", 5, 4)
	c.addReleasedMovie("Eighties", 5, 1985, 6.5, "Comedy")
	c.addReleasedMovie("Nineties", 6, 1990, 8.1, "Comedy")
	c.addReleasedMovie("Beloved Eighties Drama", 7, 1989, 8.0, "Drama")
	lineup := []Channel{
		{Key: "family", Name: "Family", Blocks: allDay(&Source{
			Name: "features", Genres: []string{"Family"}, Ratings: []string{"G", "PG"}, Shuffle: true})},
		{Key: "sci-fi", Name: "Sci-Fi", Blocks: allDay(movies("features", "Science Fiction"))},
		{Key: "eighties-comedy", Name: "'80s Comedy", Blocks: allDay(&Source{
			Name: "features", Genres: []string{"Comedy"}, Years: [2]int{1980, 1989}, Shuffle: true})},
		{Key: "nineties", Name: "'90s", Blocks: allDay(decade("features", 1990))},
		{Key: "top-shelf", Name: "Top Shelf", Blocks: allDay(&Source{Name: "features", MinVote: 8.1, Shuffle: true})},
	}
	if _, err := c.generator(lineup...).Update(c.ctx, testNow); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"family": "Kids", "sci-fi": "Space", "eighties-comedy": "Eighties", "nineties": "Nineties", "top-shelf": "Nineties",
	} {
		aired := c.aired(key)
		if len(aired) == 0 {
			t.Fatalf("channel %q aired nothing", key)
		}
		for _, title := range aired {
			if title != want {
				t.Fatalf("channel %q aired %q", key, title)
			}
		}
	}
}

func TestTitlesAirInTheOrderGivenWithoutDuplicates(t *testing.T) {
	c := newCatalog(t)
	c.addMovie("First", 300, 11, "PG", "Action")
	c.addMovie("Second", 300, 12, "PG", "Action")
	c.addMovie("Third", 300, 13, "PG", "Action")
	c.addShort("Short", 300, 14)
	lineup := []Channel{{Key: "marathon", Name: "Marathon", Blocks: allDay(&Source{
		Name: "marathon", Titles: []int64{13, 11, 14, 99, 12, 11}})}}
	generator := c.generator(lineup...)
	if _, err := generator.Update(c.ctx, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := generator.Update(c.ctx, testNow.Add(20*time.Hour)); err != nil {
		t.Fatal(err)
	}
	aired := c.aired("marathon")
	order := []string{"Third", "First", "Short", "Second"}
	if len(aired) < len(order)+1 {
		t.Fatalf("aired only %v", aired)
	}
	for index := 1; index < len(aired); index++ {
		for position, title := range order {
			if title == aired[index-1] && aired[index] != order[(position+1)%len(order)] {
				t.Fatalf("aired %q after %q: %v", aired[index], aired[index-1], aired)
			}
		}
	}
}

func TestBlockWithNothingToAirFallsBackToTheDailyGrid(t *testing.T) {
	c := newCatalog(t)
	c.episodes("Owned", 1, 40)
	lineup := []Channel{
		{Key: "mixed", Name: "Mixed", Blocks: []Block{
			daily(gridStart, 18*60, show("missing", 2)),
			daily(18*60, gridEnd, show("owned", 1)),
			on("sat", 20*60, 22*60, show("absent", 3)),
		}},
		{Key: "dark", Name: "Dark", Blocks: allDay(show("missing", 2))},
	}
	stats, err := c.generator(lineup...).Update(c.ctx, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ChannelsCreated != 2 {
		t.Fatalf("created %d channels", stats.ChannelsCreated)
	}
	programs := c.programs("mixed")
	assertBackToBack(t, "mixed", programs, testNow)
	for _, program := range programs {
		if c.sources[program.Item.ID] != "Owned" {
			t.Fatalf("aired %q", c.sources[program.Item.ID])
		}
	}
	if dark := c.programs("dark"); len(dark) != 0 {
		t.Fatalf("dark channel scheduled %d programs", len(dark))
	}
	if kept := c.channelByKey("dark"); kept.Number != 2 {
		t.Fatalf("dark channel = %+v", kept)
	}
}

func TestNewItemsJoinTheCycleOnTheNextExtend(t *testing.T) {
	c := newCatalog(t)
	c.addMovie("Alpha", 200, 1, "R", "Drama")
	c.addMovie("Beta", 200, 2, "R", "Drama")
	lineup := []Channel{{Key: "cinema", Name: "Cinema", Blocks: allDay(movies("features", "Drama"))}}
	generator := c.generator(lineup...)
	if _, err := generator.Update(c.ctx, testNow); err != nil {
		t.Fatal(err)
	}
	added := c.addMovie("Omega", 200, 3, "R", "Drama")
	if _, err := generator.Update(c.ctx, testNow.Add(20*time.Hour)); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, program := range c.programs("cinema") {
		if program.Item.ID == added {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("a movie added after the first schedule never aired")
	}
}

func TestMarathonWithoutSlugsTakesEveryCollection(t *testing.T) {
	everything := marathon()
	var want []int64
	for _, collection := range collections.All {
		want = append(want, collection.TMDBIDs...)
	}
	if len(want) == 0 || len(everything.Titles) != len(want) {
		t.Fatalf("marathon() lists %d titles, want %d", len(everything.Titles), len(want))
	}
	for index, id := range want {
		if everything.Titles[index] != id {
			t.Fatalf("marathon() title %d = %d, want %d", index, everything.Titles[index], id)
		}
	}
	if one := marathon("star-trek"); len(one.Titles) == 0 || len(one.Titles) >= len(want) {
		t.Fatalf("marathon(star-trek) lists %d titles", len(one.Titles))
	}
}

// Trek airs one film a night: a block narrower than a film admits a single
// program and then hands back to the series, which needs the soft boundary
// to work in both directions.
func TestBlockNarrowerThanItsProgramsAirsOneAndReturns(t *testing.T) {
	c := newCatalog(t)
	specs := make([]episode, 0, 40)
	for index := range 40 {
		specs = append(specs, episode{season: 1, number: index + 1, minutes: 45})
	}
	c.addShow("Series", 1, specs...)
	c.addMovie("Film A", 120, 11, "PG", "Science Fiction")
	c.addMovie("Film B", 120, 12, "PG", "Science Fiction")
	series := show("series", 1)
	generator := c.generator(Channel{Key: "trek", Name: "Trek", Blocks: []Block{
		daily(6*60, 20*60, series),
		daily(20*60, 21*60, &Source{Name: "films", Titles: []int64{11, 12}}),
		daily(21*60, 30*60, series),
	}})
	// Start in the morning so the joined program is an episode and the
	// horizon covers one whole evening.
	if _, err := generator.Update(c.ctx, testNow.Add(-12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	films := 0
	aired := c.aired("trek")
	for index, title := range aired {
		if title != "Series" {
			films++
			if index > 0 && aired[index-1] != "Series" {
				t.Fatalf("two films back to back: %v", aired)
			}
		}
	}
	if films != 1 {
		t.Fatalf("aired %d films in a day: %v", films, aired)
	}
}
