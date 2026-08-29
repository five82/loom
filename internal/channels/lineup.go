package channels

import (
	"fmt"

	"github.com/five82/loom/internal/collections"
)

// Source is a pool of programs and the order they air in. A source lists what
// it draws on by TMDB id or genre, so it survives re-matches and title
// corrections the way a collection does. Its playlist is every show's episodes
// in turn (seasons in order, specials after the last season), then the titles
// in the order given, then the genre movies, then the shorts library. An
// in-order source walks that playlist and loops; a shuffled one deals it in a
// fresh random order every cycle, so nothing repeats until everything has
// aired.
type Source struct {
	// Name identifies the source's cursor within its channel, so two blocks of
	// the same show share one place in the run.
	Name    string
	Shows   []int64  // TMDB show ids
	Titles  []int64  // TMDB movie ids, from the movies or shorts library
	Genres  []string // movies carrying any of these genres
	Ratings []string // restricts Genres to these content ratings
	Shorts  bool     // the whole short films library
	Shuffle bool
}

// Block is a daily window of a channel's grid. Times are minutes after local
// midnight of the block's day; an End past 1440 runs into the next morning,
// which is how the grid day starts at 06:00 and the overnight block wraps.
// Boundaries are soft: a program that overruns finishes, and the next block
// begins whenever it ends.
type Block struct {
	// Days is empty for every day, or "sat", "sun", or "weekend". A day-specific
	// block overrides the daily grid for its window.
	Days   string
	Start  int
	End    int
	Source *Source
	// Interstitial follows every program from Source with one of its own, which
	// is how Family runs a short after each feature.
	Interstitial *Source
}

// Channel is one lineup entry. Key is durable: it names the channel's row and
// number in the catalog, so renaming a channel keeps its place.
type Channel struct {
	Key    string
	Name   string
	Blocks []Block
}

// The grid day runs from 06:00 to 06:00, like a broadcast day.
const (
	gridStart  = 6 * 60
	gridEnd    = 30 * 60
	minutesDay = 24 * 60
)

// TMDB ids of the shows in the lineup.
const (
	southPark     = 2190
	marriedWith   = 4239
	cheers        = 141
	theOffice     = 2316
	arrestedDev   = 4589
	studio60      = 1691
	bostonLegal   = 4598
	thePractice   = 3050
	betterCallSal = 60059
	billions      = 62852
	westWing      = 688
	cobraKai      = 77169
	breakingBad   = 1396
	itMiniseries  = 19614
	anneOfGreen   = 5874
	batman66      = 2287
	dukesOfHazz   = 397
	simpsons      = 456
	robotChicken  = 709
	beavis        = 13943
	jerseyShore   = 31343
	planetEarth   = 1044
	planetEarth2  = 68595
	bluePlanet2   = 74313
	ourPlanet     = 83880
	southPacific  = 19338
	yellowstone   = 19355
	lastDance     = 79525
	neistat       = 70888
	starTrekTNG   = 655
)

// Documentary features, which air with the documentary series.
var documentaries = []int64{
	1667,   // March of the Penguins (2005)
	15302,  // The Pixar Story (2007)
	76360,  // 6 Days to Air: The Making of South Park (2011)
	65058,  // Conan O'Brien Can't Stop (2011)
	565312, // Knock Down the House (2019)
	725239, // The Swamp (2020)
}

func show(name string, tmdbIDs ...int64) *Source {
	return &Source{Name: name, Shows: tmdbIDs}
}

func shuffledShow(name string, tmdbID int64) *Source {
	return &Source{Name: name, Shows: []int64{tmdbID}, Shuffle: true}
}

func movies(name string, genres ...string) *Source {
	return &Source{Name: name, Genres: genres, Shuffle: true}
}

// marathon concatenates hand-picked collections in the order given, each in
// release order, and walks them in turn across successive weekends.
func marathon(slugs ...string) *Source {
	source := &Source{Name: "marathon"}
	for _, slug := range slugs {
		found := false
		for _, collection := range collections.All {
			if collection.Slug == slug {
				source.Titles = append(source.Titles, collection.TMDBIDs...)
				found = true
				break
			}
		}
		if !found {
			panic(fmt.Sprintf("channel marathon names unknown collection %q", slug))
		}
	}
	return source
}

func daily(start, end int, source *Source) Block {
	return Block{Start: start, End: end, Source: source}
}

func allDay(source *Source) []Block {
	return []Block{daily(gridStart, gridEnd, source)}
}

func on(days string, start, end int, source *Source) Block {
	return Block{Days: days, Start: start, End: end, Source: source}
}

// Sources that appear in more than one block of their channel share one
// cursor, so the run continues across the blocks.
var (
	batman         = show("batman", batman66)
	dukes          = show("dukes", dukesOfHazz)
	scienceFiction = movies("features", "Science Fiction")
)

// Lineup is Loom's channel list, hand-built from the library the way the
// collections are. Numbers come from the catalog: a channel takes the next
// unused number the first time its key appears, so on a fresh catalog the
// numbers follow this order and a channel added later takes the next one.
var Lineup = []Channel{
	{Key: "south-park", Name: "South Park", Blocks: allDay(show("run", southPark))},
	{Key: "south-park-shuffle", Name: "South Park Shuffle", Blocks: allDay(shuffledShow("shuffle", southPark))},
	{Key: "married-with-children", Name: "Married... with Children", Blocks: allDay(show("run", marriedWith))},
	{Key: "mwc-shuffle", Name: "MWC Shuffle", Blocks: allDay(shuffledShow("shuffle", marriedWith))},
	{Key: "sitcoms", Name: "Sitcoms", Blocks: []Block{
		daily(6*60, 14*60, show("cheers", cheers)),
		daily(14*60, 23*60, show("office", theOffice)),
		daily(23*60, 28*60, show("arrested", arrestedDev)),
		daily(28*60, 30*60, show("studio60", studio60)),
	}},
	{Key: "drama", Name: "Drama", Blocks: []Block{
		daily(6*60, 11*60+30, show("boston-legal", bostonLegal)),
		daily(11*60+30, 12*60+30, show("practice", thePractice)),
		daily(12*60+30, 15*60+30, show("saul", betterCallSal)),
		daily(15*60+30, 19*60+30, show("billions", billions)),
		daily(19*60+30, 28*60, show("west-wing", westWing)),
		daily(28*60, 30*60, show("cobra-kai", cobraKai)),
		// The short runs and the miniseries share a weekend afternoon.
		on("weekend", 12*60, 16*60, show("feature", breakingBad, itMiniseries, anneOfGreen)),
	}},
	{Key: "classics", Name: "Classics", Blocks: []Block{
		daily(6*60, 10*60, batman),
		daily(10*60, 18*60, dukes),
		daily(18*60, 21*60, batman),
		daily(21*60, 30*60, dukes),
	}},
	{Key: "slacker", Name: "Slacker", Blocks: []Block{
		daily(6*60, 13*60, show("simpsons", simpsons)),
		daily(13*60, 15*60, show("robot-chicken", robotChicken)),
		daily(15*60, 16*60, show("beavis", beavis)),
		daily(16*60, 30*60, show("jersey-shore", jerseyShore)),
	}},
	{Key: "nature-docs", Name: "Nature & Docs", Blocks: []Block{
		daily(6*60, 21*60, show("nature", planetEarth, planetEarth2, bluePlanet2, ourPlanet, southPacific, yellowstone)),
		daily(21*60, 30*60, &Source{Name: "stories", Shows: []int64{lastDance, neistat}, Titles: documentaries}),
	}},
	{Key: "cinema", Name: "Cinema", Blocks: []Block{
		daily(gridStart, gridEnd, movies("features", "Drama", "Romance", "History", "War", "Mystery", "Western")),
		on("sat", 12*60, gridEnd, marathon("tarantino", "the-godfather", "world-war-ii", "spaceflight")),
	}},
	{Key: "action", Name: "Action", Blocks: []Block{
		daily(gridStart, gridEnd, movies("features", "Action", "Thriller", "Crime")),
		on("sat", 12*60, gridEnd, marathon("james-bond", "jason-bourne", "mission-impossible",
			"indiana-jones", "x-men", "dark-knight", "kill-bill", "deadpool")),
	}},
	{Key: "comedy", Name: "Comedy", Blocks: []Block{
		daily(gridStart, gridEnd, movies("features", "Comedy")),
		on("sun", 12*60, gridEnd, marathon("pink-panther", "naked-gun", "vacation", "view-askew", "bill-and-ted")),
	}},
	{Key: "sci-fi", Name: "Sci-Fi", Blocks: []Block{
		daily(6*60, 18*60, scienceFiction),
		daily(18*60, 22*60, show("tng", starTrekTNG)),
		daily(22*60, 30*60, scienceFiction),
		on("sat", 12*60, gridEnd, marathon("star-wars", "star-trek", "blade-runner", "hunger-games")),
	}},
	{Key: "family", Name: "Family", Blocks: []Block{
		{Start: gridStart, End: gridEnd,
			Source:       &Source{Name: "features", Genres: []string{"Family", "Animation"}, Ratings: []string{"G", "PG"}, Shuffle: true},
			Interstitial: &Source{Name: "shorts", Shorts: true, Shuffle: true}},
		on("weekend", 12*60, 18*60, marathon("toy-story", "pixar", "disney-animation")),
	}},
}

// blockAt returns the block airing at the given local instant. Day-specific
// blocks win over the daily grid, and a block that runs past midnight is found
// through the day it started on.
func (c Channel) blockAt(local timeOfWeek) Block {
	for _, dayBlocks := range [2]bool{true, false} {
		for _, block := range c.Blocks {
			if (block.Days != "") != dayBlocks {
				continue
			}
			if block.covers(local) {
				return block
			}
		}
	}
	panic(fmt.Sprintf("channel %q has no block at %+v", c.Key, local))
}

// timeOfWeek is a local instant reduced to what block selection needs.
type timeOfWeek struct {
	weekday int // 0 is Sunday, as in time.Weekday
	minutes int // since local midnight
}

func (b Block) covers(local timeOfWeek) bool {
	if b.appliesOn(local.weekday) && b.Start <= local.minutes && local.minutes < b.End {
		return true
	}
	// A block started yesterday and running into today.
	yesterday := (local.weekday + 6) % 7
	late := local.minutes + minutesDay
	return b.appliesOn(yesterday) && b.Start <= late && late < b.End
}

func (b Block) appliesOn(weekday int) bool {
	switch b.Days {
	case "":
		return true
	case "sat":
		return weekday == 6
	case "sun":
		return weekday == 0
	case "weekend":
		return weekday == 0 || weekday == 6
	}
	return false
}

// dailyBlocks returns the channel's everyday grid in order, which is also the
// fallback order when a block has nothing to air.
func (c Channel) dailyBlocks() []Block {
	var result []Block
	for _, block := range c.Blocks {
		if block.Days == "" {
			result = append(result, block)
		}
	}
	return result
}

// validate checks the invariants the scheduler relies on: unique keys and
// source names, every source drawing on something, and a daily grid that tiles
// the broadcast day exactly, so blockAt always finds a block.
func validate(lineup []Channel) error {
	keys := map[string]bool{}
	for _, channel := range lineup {
		if channel.Key == "" || channel.Name == "" || keys[channel.Key] {
			return fmt.Errorf("channel %q: missing or duplicate key or name", channel.Key)
		}
		keys[channel.Key] = true
		sources := map[string]*Source{}
		cursor := gridStart
		for _, block := range channel.Blocks {
			for _, source := range []*Source{block.Source, block.Interstitial} {
				if source == nil {
					continue
				}
				if source.Name == "" || len(source.Shows)+len(source.Titles)+len(source.Genres) == 0 && !source.Shorts {
					return fmt.Errorf("channel %q: source %q is unnamed or empty", channel.Key, source.Name)
				}
				if held, ok := sources[source.Name]; ok && held != source {
					return fmt.Errorf("channel %q: source name %q is used by two sources", channel.Key, source.Name)
				}
				sources[source.Name] = source
			}
			if block.Source == nil || block.Start >= block.End || block.Start < 0 || block.End > gridEnd+minutesDay {
				return fmt.Errorf("channel %q: block %+v is malformed", channel.Key, block)
			}
			switch block.Days {
			case "":
				if block.Start != cursor {
					return fmt.Errorf("channel %q: daily grid has a gap or overlap at %d", channel.Key, block.Start)
				}
				cursor = block.End
			case "sat", "sun", "weekend":
			default:
				return fmt.Errorf("channel %q: unknown days %q", channel.Key, block.Days)
			}
		}
		if cursor != gridEnd {
			return fmt.Errorf("channel %q: daily grid ends at %d, want %d", channel.Key, cursor, gridEnd)
		}
	}
	return nil
}
