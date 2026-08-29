package channels

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/five82/loom/internal/store"
)

// The generator is driven entirely by the now it is handed, so every test runs
// against this fixed instant instead of the wall clock.
var testNow = time.Date(2026, 8, 29, 20, 0, 0, 0, time.UTC)

type catalog struct {
	t          *testing.T
	ctx        context.Context
	store      *store.Store
	movieLib   int64
	movieScan  int64
	tvLib      int64
	tvScan     int64
	nextSource int
	shows      map[string]int64
}

func newCatalog(t *testing.T) *catalog {
	t.Helper()
	ctx := context.Background()
	opened, err := store.Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	c := &catalog{t: t, ctx: ctx, store: opened, shows: map[string]int64{}}
	c.movieLib, c.movieScan, err = opened.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	c.tvLib, c.tvScan, err = opened.StartScan(ctx, "tv", "/tv")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// addMovie stores an available movie with a file of the given runtime. A
// dynamic range makes it a candidate for the HDR channel.
func (c *catalog) addMovie(title string, minutes int, dynamicRange string, genres ...store.Genre) int64 {
	c.t.Helper()
	c.nextSource++
	id, err := c.store.UpsertItem(c.ctx, store.ItemInput{
		LibraryID: c.movieLib, SourceKey: fmt.Sprintf("movie-%d", c.nextSource), Kind: "movie",
		Title: title, ScanID: c.movieScan,
	})
	if err != nil {
		c.t.Fatal(err)
	}
	c.addMedia(id, "/movies/"+title+".mkv", minutes, dynamicRange)
	if len(genres) > 0 {
		if err := c.store.UpdateMetadata(c.ctx, id, store.MetadataUpdate{
			TMDBID: id, Title: title, Genres: genres,
		}); err != nil {
			c.t.Fatal(err)
		}
	}
	return id
}

type episode struct {
	season  int
	number  int
	minutes int
}

// addShow stores a show, one season per distinct season number, and the given
// episodes. It returns the show id and the episode ids in the order given.
func (c *catalog) addShow(title string, episodes ...episode) (int64, []int64) {
	c.t.Helper()
	showID, err := c.store.UpsertItem(c.ctx, store.ItemInput{
		LibraryID: c.tvLib, SourceKey: title, Kind: "show", Title: title, ScanID: c.tvScan,
	})
	if err != nil {
		c.t.Fatal(err)
	}
	seasons := map[int]int64{}
	var ids []int64
	for _, spec := range episodes {
		seasonID, ok := seasons[spec.season]
		if !ok {
			seasonID, err = c.store.UpsertItem(c.ctx, store.ItemInput{
				LibraryID: c.tvLib, ParentID: &showID,
				SourceKey: fmt.Sprintf("%s/season-%d", title, spec.season), Kind: "season",
				Title: fmt.Sprintf("Season %d", spec.season), SeasonNumber: spec.season, ScanID: c.tvScan,
			})
			if err != nil {
				c.t.Fatal(err)
			}
			seasons[spec.season] = seasonID
		}
		key := fmt.Sprintf("%s/S%02dE%02d", title, spec.season, spec.number)
		id, err := c.store.UpsertItem(c.ctx, store.ItemInput{
			LibraryID: c.tvLib, ParentID: &seasonID, SourceKey: key, Kind: "episode", Title: key,
			SeasonNumber: spec.season, EpisodeNumber: spec.number, ScanID: c.tvScan,
		})
		if err != nil {
			c.t.Fatal(err)
		}
		c.addMedia(id, "/tv/"+key+".mkv", spec.minutes, "sdr")
		ids = append(ids, id)
	}
	c.shows[title] = showID
	return showID, ids
}

// showKey is the lineup key of the channel a seeded show holds.
func (c *catalog) showKey(title string) string {
	c.t.Helper()
	return fmt.Sprintf("show:%d", c.shows[title])
}

func (c *catalog) addMedia(itemID int64, path string, minutes int, dynamicRange string) {
	c.t.Helper()
	var streams []store.Stream
	if dynamicRange != "" {
		streams = []store.Stream{{
			Index: 0, Kind: "video", Codec: "hevc", Width: 1920, Height: 1080,
			DynamicRange: dynamicRange, IsDefault: true,
		}}
	}
	if _, err := c.store.UpsertMedia(c.ctx, store.MediaFile{
		ItemID: itemID, Path: path, Size: 100, MTimeNS: 20,
		DurationMS: int64(minutes) * 60_000, Container: "matroska", LastSeenScanID: c.movieScan,
	}, streams, nil); err != nil {
		c.t.Fatal(err)
	}
}

func (c *catalog) generator() *Generator {
	// A fixed source makes every shuffled channel's picks reproducible.
	return NewWithRandom(c.store, rand.New(rand.NewSource(1)))
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

// A small library: two shows, movies across three genres, one of them HDR.
func seedLineup(t *testing.T) *catalog {
	t.Helper()
	c := newCatalog(t)
	c.addMovie("Alpha", 90, "sdr", store.Genre{ID: 28, Name: "Action"})
	c.addMovie("Beta", 100, "hdr", store.Genre{ID: 28, Name: "Action"})
	c.addMovie("Gamma", 110, "sdr", store.Genre{ID: 35, Name: "Comedy"})
	c.addMovie("Delta", 120, "dolby_vision", store.Genre{ID: 35, Name: "Comedy"})
	c.addMovie("Epsilon", 95, "sdr", store.Genre{ID: 18, Name: "Drama"})
	c.addShow("Show A",
		episode{season: 0, number: 1, minutes: 45},
		episode{season: 1, number: 1, minutes: 30},
		episode{season: 1, number: 2, minutes: 30},
		episode{season: 2, number: 1, minutes: 30})
	c.addShow("Show B", episode{season: 1, number: 1, minutes: 60})
	return c
}

func TestReconcileBuildsLineupAndKeepsNumbersStable(t *testing.T) {
	c := seedLineup(t)
	generator := c.generator()
	created, err := generator.Reconcile(c.ctx, testNow)
	if err != nil {
		t.Fatal(err)
	}
	// Two shows, three genres, HDR and Mix.
	if created != 7 {
		t.Fatalf("created %d channels, want 7", created)
	}
	channels, err := c.store.Channels(c.ctx)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		number int
		key    string
		name   string
		kind   string
	}
	got := make([]row, 0, len(channels))
	for _, channel := range channels {
		got = append(got, row{channel.Number, channel.Key, channel.Name, channel.Kind})
	}
	// Shows rank by episode count, genres by movie count, and ties break by
	// name, so the whole lineup is predictable.
	want := []row{
		{1, c.showKey("Show A"), "Show A", "show"},
		{2, c.showKey("Show B"), "Show B", "show"},
		{3, "genre:28", "Action", "genre"},
		{4, "genre:35", "Comedy", "genre"},
		{5, "genre:18", "Drama", "genre"},
		{6, "hdr", "HDR", "hdr"},
		{7, "mix", "Mix", "mix"},
	}
	if len(got) != len(want) {
		t.Fatalf("lineup = %+v, want %+v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("channel %d = %+v, want %+v", index, got[index], want[index])
		}
	}

	// A second reconcile adds nothing and leaves every number where it was, and
	// a new genre takes the next number rather than one already in use.
	created, err = generator.Reconcile(c.ctx, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Fatalf("second reconcile created %d channels", created)
	}
	c.addMovie("Zeta", 80, "sdr", store.Genre{ID: 99, Name: "Western"})
	c.addMovie("Eta", 80, "sdr", store.Genre{ID: 99, Name: "Western"})
	c.addMovie("Theta", 80, "sdr", store.Genre{ID: 27, Name: "Horror"})
	c.addMovie("Iota", 80, "sdr", store.Genre{ID: 27, Name: "Horror"})
	if _, err := generator.Reconcile(c.ctx, testNow); err != nil {
		t.Fatal(err)
	}
	if added := c.channelByKey("genre:27"); added.Number != 8 {
		t.Fatalf("Horror channel number = %d, want 8", added.Number)
	}
	if added := c.channelByKey("genre:99"); added.Number != 9 {
		t.Fatalf("Western channel number = %d, want 9", added.Number)
	}
	if first := c.channelByKey(c.showKey("Show A")); first.Number != 1 {
		t.Fatalf("existing channel renumbered to %d", first.Number)
	}
	// Drama has dropped out of the top four genres and keeps its channel and
	// its number anyway.
	if kept := c.channelByKey("genre:18"); kept.Number != 5 {
		t.Fatalf("channel that stopped qualifying = %+v", kept)
	}
}

func TestExtendFillsHorizonAndKeepsProgramsBackToBack(t *testing.T) {
	c := seedLineup(t)
	generator := c.generator()
	if _, err := generator.Update(c.ctx, testNow); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{c.showKey("Show A"), "genre:28", "hdr", "mix"} {
		programs := c.programs(key)
		if len(programs) == 0 {
			t.Fatalf("channel %q has no programs", key)
		}
		if programs[0].StartsAt != store.ChannelTime(testNow) {
			t.Fatalf("channel %q starts at %s, want %s", key, programs[0].StartsAt,
				store.ChannelTime(testNow))
		}
		for index, program := range programs {
			if index > 0 && program.StartsAt != programs[index-1].EndsAt {
				t.Fatalf("channel %q has a gap before program %d: %s after %s",
					key, index, program.StartsAt, programs[index-1].EndsAt)
			}
			if program.EndsAt <= program.StartsAt {
				t.Fatalf("channel %q program %d ends at %s, starts at %s",
					key, index, program.EndsAt, program.StartsAt)
			}
		}
		last := programs[len(programs)-1]
		if last.EndsAt < store.ChannelTime(testNow.Add(Horizon)) {
			t.Fatalf("channel %q is scheduled only to %s", key, last.EndsAt)
		}
		// The horizon is a floor, not a target: only the program covering it
		// may run past it.
		if programs[len(programs)-2].EndsAt >= store.ChannelTime(testNow.Add(Horizon)) {
			t.Fatalf("channel %q scheduled past the horizon", key)
		}
	}

	// A second run at the same instant has nothing to add.
	stats, err := generator.Update(c.ctx, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ProgramsAdded != 0 || stats.ProgramsPruned != 0 || stats.ChannelsCreated != 0 {
		t.Fatalf("repeat update = %+v, want no changes", stats)
	}
}

func TestExtendPrunesFinishedProgramsAndAppendsToTheTail(t *testing.T) {
	c := seedLineup(t)
	generator := c.generator()
	if _, err := generator.Update(c.ctx, testNow); err != nil {
		t.Fatal(err)
	}
	before := c.programs("mix")
	later := testNow.Add(12 * time.Hour)
	stats, err := generator.Update(c.ctx, later)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ProgramsPruned == 0 || stats.ProgramsAdded == 0 {
		t.Fatalf("update after twelve hours = %+v", stats)
	}
	after := c.programs("mix")
	// Everything that ended more than six hours ago is gone, everything else
	// stayed exactly where it was, and the tail grew.
	cutoff := store.ChannelTime(later.Add(-6 * time.Hour))
	kept := 0
	for _, program := range before {
		if program.EndsAt < cutoff {
			continue
		}
		if after[kept].ID != program.ID || after[kept].StartsAt != program.StartsAt {
			t.Fatalf("program %d moved: %+v, want %+v", kept, after[kept], program)
		}
		kept++
	}
	if kept == 0 || kept == len(before) {
		t.Fatalf("pruning kept %d of %d programs", kept, len(before))
	}
	if after[kept].StartsAt != after[kept-1].EndsAt {
		t.Fatalf("new tail starts at %s, previous program ends at %s",
			after[kept].StartsAt, after[kept-1].EndsAt)
	}
	if last := after[len(after)-1]; last.EndsAt < store.ChannelTime(later.Add(Horizon)) {
		t.Fatalf("schedule reaches only %s", last.EndsAt)
	}
}

// Loom being down long enough for a schedule to run out leaves a gap. The
// channel restarts at now rather than replaying the hours nobody watched.
func TestExtendRestartsAtNowAfterAGap(t *testing.T) {
	c := seedLineup(t)
	generator := c.generator()
	if _, err := generator.Update(c.ctx, testNow); err != nil {
		t.Fatal(err)
	}
	outage := testNow.Add(3 * Horizon)
	if _, err := generator.Update(c.ctx, outage); err != nil {
		t.Fatal(err)
	}
	programs := c.programs("mix")
	if len(programs) == 0 {
		t.Fatal("channel is empty after the outage")
	}
	if programs[0].StartsAt != store.ChannelTime(outage) {
		t.Fatalf("schedule restarts at %s, want %s", programs[0].StartsAt, store.ChannelTime(outage))
	}
	if last := programs[len(programs)-1]; last.EndsAt < store.ChannelTime(outage.Add(Horizon)) {
		t.Fatalf("schedule reaches only %s", last.EndsAt)
	}
}

func TestShowChannelAirsEpisodesInOrderAndLoops(t *testing.T) {
	c := seedLineup(t)
	// Seven-hour episodes keep a day's schedule short enough to read, and the
	// out-of-order seeding proves the channel sorts rather than replays.
	_, episodes := c.addShow("Show C",
		episode{season: 0, number: 1, minutes: 420},
		episode{season: 2, number: 1, minutes: 420},
		episode{season: 1, number: 2, minutes: 420},
		episode{season: 1, number: 1, minutes: 420})
	generator := c.generator()
	if _, err := generator.Update(c.ctx, testNow); err != nil {
		t.Fatal(err)
	}
	programs := c.programs(c.showKey("Show C"))
	var aired []int64
	for _, program := range programs {
		aired = append(aired, program.Item.ID)
	}
	// Season and episode order, the special skipped, then back to the top.
	want := []int64{episodes[3], episodes[2], episodes[1], episodes[3]}
	if len(aired) != len(want) {
		t.Fatalf("aired %v, want %v", aired, want)
	}
	for index := range want {
		if aired[index] != want[index] {
			t.Fatalf("program %d aired item %d, want %d", index, aired[index], want[index])
		}
	}

	// The next extension continues after the last episode scheduled rather than
	// starting the show over.
	if _, err := generator.Update(c.ctx, testNow.Add(20*time.Hour)); err != nil {
		t.Fatal(err)
	}
	programs = c.programs(c.showKey("Show C"))
	for index := 1; index < len(programs); index++ {
		if programs[index].StartsAt != programs[index-1].EndsAt {
			t.Fatalf("continued schedule has a gap at program %d", index)
		}
	}
	continued := programs[len(programs)-1].Item.ID
	previous := programs[len(programs)-2].Item.ID
	order := []int64{episodes[3], episodes[2], episodes[1]}
	for index, id := range order {
		if id == previous && continued != order[(index+1)%len(order)] {
			t.Fatalf("continued with item %d after %d", continued, previous)
		}
	}
}

func TestShuffledChannelDoesNotRepeatWhileItHasChoices(t *testing.T) {
	c := newCatalog(t)
	// Four long movies fill a day with four programs, so a repeat inside the
	// stored window would be a bug rather than an exhausted channel.
	c.addMovie("Alpha", 400, "sdr", store.Genre{ID: 28, Name: "Action"})
	c.addMovie("Beta", 400, "sdr", store.Genre{ID: 28, Name: "Action"})
	c.addMovie("Gamma", 400, "sdr", store.Genre{ID: 28, Name: "Action"})
	c.addMovie("Delta", 400, "sdr", store.Genre{ID: 28, Name: "Action"})
	generator := c.generator()
	if _, err := generator.Update(c.ctx, testNow); err != nil {
		t.Fatal(err)
	}
	programs := c.programs("mix")
	if len(programs) != 4 {
		t.Fatalf("scheduled %d programs, want 4", len(programs))
	}
	seen := map[int64]bool{}
	for _, program := range programs {
		if seen[program.Item.ID] {
			t.Fatalf("item %d aired twice inside one window: %+v", program.Item.ID, programs)
		}
		seen[program.Item.ID] = true
	}

	// With everything aired, the channel repeats rather than going dark.
	if _, err := generator.Update(c.ctx, testNow.Add(20*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if extended := c.programs("mix"); len(extended) <= len(programs) {
		t.Fatalf("exhausted channel stopped scheduling: %d programs", len(extended))
	}
}

func TestHDRChannelSchedulesOnlyHighDynamicRangeItems(t *testing.T) {
	c := seedLineup(t)
	generator := c.generator()
	if _, err := generator.Update(c.ctx, testNow); err != nil {
		t.Fatal(err)
	}
	for _, program := range c.programs("hdr") {
		if title := program.Item.Title; title != "Beta" && title != "Delta" {
			t.Fatalf("HDR channel aired %q", title)
		}
	}
}

func TestNewItemsBecomeEligibleOnTheNextExtend(t *testing.T) {
	c := seedLineup(t)
	generator := c.generator()
	if _, err := generator.Update(c.ctx, testNow); err != nil {
		t.Fatal(err)
	}
	added := c.addMovie("Omega", 240, "sdr", store.Genre{ID: 18, Name: "Drama"})
	if _, err := generator.Update(c.ctx, testNow.Add(20*time.Hour)); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, program := range c.programs("mix") {
		if program.Item.ID == added {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("a movie added after the first schedule never aired")
	}
}

// A channel with nothing left to air keeps its number and its place in the
// lineup and simply stops growing.
func TestChannelWithoutEligibleItemsIsKeptAndSkipped(t *testing.T) {
	c := seedLineup(t)
	// Only a special, which a show channel never airs.
	showID, _ := c.addShow("Show D", episode{season: 0, number: 1, minutes: 30})
	generator := c.generator()
	if _, err := generator.Update(c.ctx, testNow); err != nil {
		t.Fatal(err)
	}
	empty, err := c.store.CreateChannel(c.ctx, store.Channel{
		Key: c.showKey("Show D"), Name: "Show D", Kind: "show", ItemID: showID,
	}, store.ChannelTime(testNow))
	if err != nil {
		t.Fatal(err)
	}
	stats, err := generator.Update(c.ctx, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ProgramsAdded != 0 {
		t.Fatalf("update added %d programs to a full schedule", stats.ProgramsAdded)
	}
	if kept := c.channelByKey(c.showKey("Show D")); kept.ID != empty.ID || kept.Number != empty.Number {
		t.Fatalf("empty channel changed: %+v, want %+v", kept, empty)
	}
	if programs := c.programs(c.showKey("Show D")); len(programs) != 0 {
		t.Fatalf("empty channel scheduled %d programs", len(programs))
	}
}
