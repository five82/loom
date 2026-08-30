// Package channels runs Loom's linear TV lineup: a hand-built list of channels,
// each with a daily grid of blocks, and a rolling schedule of back-to-back
// programs that a client can tune into at any moment.
package channels

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"sync"
	"time"

	"github.com/five82/loom/internal/store"
)

const (
	// Horizon is how far ahead every channel is scheduled.
	Horizon = 24 * time.Hour
	// retention keeps finished programs long enough to cover a shuffled
	// source's whole cycle, so a title a marathon aired is skipped when the
	// shuffle reaches it. The longest cycle in the lineup is about two weeks.
	retention = 30 * 24 * time.Hour
)

// Generator owns the lineup and the schedule. The daemon worker and the API
// share one, and its lock is what keeps a self-healing request from scheduling
// the same hours the worker is already appending.
type Generator struct {
	catalog  *store.Store
	lineup   []Channel
	location *time.Location
	mu       sync.Mutex
}

// Stats reports what one update changed, for the daemon log.
type Stats struct {
	ChannelsCreated int
	ChannelsRemoved int
	ProgramsPruned  int
	ProgramsAdded   int
}

// New runs Lineup on the server's local clock, which is what the block times
// are written in.
func New(catalog *store.Store) *Generator {
	return NewWith(catalog, Lineup, time.Local)
}

// NewWith runs a specific lineup in a specific zone, so a test can build a
// small grid and a fixed clock. The lineup is a compile-time table, so an
// invalid one is a programming error rather than a runtime condition.
func NewWith(catalog *store.Store, lineup []Channel, location *time.Location) *Generator {
	if err := validate(lineup); err != nil {
		panic(err)
	}
	return &Generator{catalog: catalog, lineup: lineup, location: location}
}

// Update reconciles the lineup and extends every schedule.
func (g *Generator) Update(ctx context.Context, now time.Time) (Stats, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	stats, err := g.reconcile(ctx, now)
	if err != nil {
		return stats, err
	}
	pruned, added, err := g.extend(ctx, now)
	stats.ProgramsPruned, stats.ProgramsAdded = pruned, added
	return stats, err
}

// Reconcile makes the catalog's channel list match the lineup.
func (g *Generator) Reconcile(ctx context.Context, now time.Time) (Stats, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reconcile(ctx, now)
}

// Extend prunes finished programs and fills every channel out to the horizon,
// reporting how many programs it removed and appended.
func (g *Generator) Extend(ctx context.Context, now time.Time) (pruned, added int, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.extend(ctx, now)
}

// reconcile creates a channel for every lineup key the catalog lacks, follows
// renames, and drops channels the lineup no longer names. Numbers are assigned
// by the catalog and never reused, so a channel keeps its place across
// lineup edits around it.
func (g *Generator) reconcile(ctx context.Context, now time.Time) (Stats, error) {
	var stats Stats
	existing, err := g.catalog.Channels(ctx)
	if err != nil {
		return stats, err
	}
	current := make(map[string]store.Channel, len(existing))
	for _, channel := range existing {
		current[channel.Key] = channel
	}
	wanted := make(map[string]bool, len(g.lineup))
	for _, spec := range g.lineup {
		wanted[spec.Key] = true
		held, ok := current[spec.Key]
		if !ok {
			if _, err := g.catalog.CreateChannel(ctx, spec.Key, spec.Name, store.ChannelTime(now)); err != nil {
				return stats, err
			}
			stats.ChannelsCreated++
			continue
		}
		if held.Name != spec.Name {
			if err := g.catalog.RenameChannel(ctx, held.ID, spec.Name); err != nil {
				return stats, err
			}
		}
	}
	for _, channel := range existing {
		if wanted[channel.Key] {
			continue
		}
		if err := g.catalog.DeleteChannel(ctx, channel.ID); err != nil {
			return stats, err
		}
		stats.ChannelsRemoved++
	}
	return stats, nil
}

func (g *Generator) extend(ctx context.Context, now time.Time) (int, int, error) {
	now = now.UTC().Truncate(time.Second)
	pruned, err := g.catalog.PruneChannelPrograms(ctx, store.ChannelTime(now.Add(-retention)))
	if err != nil {
		return 0, 0, err
	}
	channels, err := g.catalog.Channels(ctx)
	if err != nil {
		return pruned, 0, err
	}
	specs := make(map[string]Channel, len(g.lineup))
	for _, spec := range g.lineup {
		specs[spec.Key] = spec
	}
	// Pools are loaded once per update and shared by every channel that draws
	// on the same show or genre.
	pools := &poolCache{ctx: ctx, catalog: g.catalog, pools: map[*Source]*pool{}}
	added := 0
	for _, channel := range channels {
		spec, ok := specs[channel.Key]
		if !ok {
			continue
		}
		count, err := g.extendChannel(ctx, pools, channel, spec, now)
		added += count
		if err != nil {
			return pruned, added, err
		}
	}
	return pruned, added, nil
}

func (g *Generator) extendChannel(
	ctx context.Context, pools *poolCache, channel store.Channel, spec Channel, now time.Time,
) (int, error) {
	lastItem, end, err := g.catalog.ChannelLastProgram(ctx, channel.ID)
	if err != nil {
		return 0, err
	}
	// A channel with a future tail continues from it. One without - new, or
	// dark because Loom was down long enough for its schedule to run out -
	// is joined in progress: its first program started a while ago, the way a
	// channel that never stopped would look, rather than starting a fresh
	// program on the instant Loom noticed.
	cursor := now
	joinInProgress := true
	if end != "" {
		last, err := store.ParseChannelTime(end)
		if err != nil {
			return 0, err
		}
		if last.After(now) {
			cursor = last
			joinInProgress = false
		}
	}
	horizon := now.Add(Horizon)
	if !cursor.Before(horizon) {
		return 0, nil
	}
	stored, err := g.catalog.ChannelCursors(ctx, channel.ID)
	if err != nil {
		return 0, err
	}
	aired, err := g.catalog.ChannelAired(ctx, channel.ID)
	if err != nil {
		return 0, err
	}
	run := &channelRun{key: channel.Key, spec: spec, pools: pools, cursors: map[string]store.ChannelCursor{}, aired: aired}
	for _, cursor := range stored {
		run.cursors[cursor.Source] = cursor
	}
	var programs []store.ScheduledProgram
	for cursor.Before(horizon) {
		block := spec.blockAt(g.local(cursor))
		source, items, err := run.choose(block, lastItem)
		if err != nil {
			return 0, err
		}
		// Nothing in the lineup for this channel is in the library yet. The
		// channel keeps its number and simply stays dark.
		if source == nil {
			break
		}
		next := run.next(source, items, store.ChannelTime(cursor))
		starts := cursor
		if joinInProgress {
			starts = cursor.Add(-time.Duration(next.DurationMS) * time.Millisecond * time.Duration(joinPercent(channel.Key)) / 100)
			starts = starts.Truncate(time.Second)
			joinInProgress = false
		}
		cursor = starts.Add(time.Duration(next.DurationMS) * time.Millisecond).Truncate(time.Second)
		// Program boundaries are whole seconds, so a file shorter than a second
		// still has to occupy one or the schedule would never advance.
		if !cursor.After(starts) {
			cursor = starts.Add(time.Second)
		}
		programs = append(programs, store.ScheduledProgram{
			ItemID: next.ItemID, StartsAt: store.ChannelTime(starts), EndsAt: store.ChannelTime(cursor),
		})
		run.aired = append(run.aired, store.AiredProgram{ItemID: next.ItemID, StartsAt: store.ChannelTime(starts)})
		lastItem = next.ItemID
	}
	if err := g.catalog.AppendChannelPrograms(ctx, channel.ID, programs, run.changed()); err != nil {
		return 0, err
	}
	return len(programs), nil
}

func (g *Generator) local(at time.Time) timeOfWeek {
	local := at.In(g.location)
	return timeOfWeek{weekday: int(local.Weekday()), minutes: local.Hour()*60 + local.Minute()}
}

// joinPercent is how far into its first program a joined channel is: between a
// tenth and nine tenths, fixed per channel so a restart lands in the same place.
func joinPercent(key string) int64 {
	return int64(hash64(key, "join")%80) + 10
}

// channelRun is the state of one channel while its schedule is extended: the
// cursor of every source it has aired, updated as programs are laid down and
// written back with them, and everything the channel has aired or scheduled,
// growing as programs are added.
type channelRun struct {
	key     string
	spec    Channel
	pools   *poolCache
	cursors map[string]store.ChannelCursor
	dirty   map[string]bool
	aired   []store.AiredProgram
}

// choose picks the source that airs next in the block: the interstitial after
// a program from the block's own source, otherwise the block's source, and
// when that has nothing in the library, the daily grid at this time and then
// the rest of it in order. A nil source means the whole channel is dark.
func (r *channelRun) choose(block Block, lastItem int64) (*Source, []store.ChannelItem, error) {
	if block.Interstitial != nil && lastItem != 0 {
		fillers, err := r.pools.load(block.Interstitial)
		if err != nil {
			return nil, nil, err
		}
		if len(fillers.items) > 0 && !fillers.contains(lastItem) {
			return block.Interstitial, fillers.items, nil
		}
	}
	candidates := []*Source{block.Source}
	for _, daily := range r.spec.dailyBlocks() {
		candidates = append(candidates, daily.Source)
	}
	for _, source := range candidates {
		loaded, err := r.pools.load(source)
		if err != nil {
			return nil, nil, err
		}
		if len(loaded.items) > 0 {
			return source, loaded.items, nil
		}
	}
	return nil, nil, nil
}

// next advances a source's cursor and returns the program it lands on. The
// cursor is the last item aired and the cycle it aired in; the next item is
// the one after it in that cycle's order, and when the order runs out a new
// cycle begins. A shuffled source also passes over anything the channel has
// aired since its cycle began, so a title a weekend marathon showed is not
// shown again days later by the shuffle. A source without a cursor starts at
// a fixed, key-derived point in its run rather than the top, so a channel is
// born mid-season, as if it had been on the air for years.
func (r *channelRun) next(source *Source, items []store.ChannelItem, at string) store.ChannelItem {
	cursor, ok := r.cursors[source.Name]
	ordered := r.order(source, items, cursor.Cycle)
	index := 0
	if !ok {
		// A source's first cycle counts everything the channel has aired, so
		// a shuffle joining a channel does not reopen with last weekend's film.
		index = int(hash64(r.key, source.Name, "start") % uint64(len(items)))
		cursor.CycleStartedAt = ""
	} else {
		index = r.indexAfter(source, ordered, cursor)
	}
	if source.Shuffle {
		index = r.skipAired(ordered, index, cursor.CycleStartedAt)
	}
	if index == len(ordered) {
		cursor.Cycle++
		cursor.CycleStartedAt = at
		ordered = r.order(source, items, cursor.Cycle)
		index = 0
	}
	picked := ordered[index]
	r.cursors[source.Name] = store.ChannelCursor{
		Source: source.Name, Cycle: cursor.Cycle, CycleStartedAt: cursor.CycleStartedAt, ItemID: picked.ItemID,
	}
	if r.dirty == nil {
		r.dirty = map[string]bool{}
	}
	r.dirty[source.Name] = true
	return picked
}

// skipAired moves index past every item the channel has aired since the cycle
// began, returning len(ordered) when nothing is left.
func (r *channelRun) skipAired(ordered []store.ChannelItem, index int, since string) int {
	aired := map[int64]bool{}
	for _, program := range r.aired {
		if program.StartsAt >= since {
			aired[program.ItemID] = true
		}
	}
	for index < len(ordered) && aired[ordered[index].ItemID] {
		index++
	}
	return index
}

// order is a cycle's playlist: the pool as listed for an in-order source, or
// sorted by a per-cycle hash for a shuffled one. Hashing rather than drawing
// means an item added mid-cycle simply takes its place in the order, and an
// item removed leaves the rest where they were.
func (r *channelRun) order(source *Source, items []store.ChannelItem, cycle int) []store.ChannelItem {
	if !source.Shuffle {
		return items
	}
	ordered := make([]store.ChannelItem, len(items))
	copy(ordered, items)
	sort.Slice(ordered, func(a, b int) bool {
		ha, hb := r.shuffleKey(source, cycle, ordered[a].ItemID), r.shuffleKey(source, cycle, ordered[b].ItemID)
		if ha != hb {
			return ha < hb
		}
		return ordered[a].ItemID < ordered[b].ItemID
	})
	return ordered
}

// indexAfter finds where the cursor's item sits in the order and returns the
// next position. A shuffled item that has left the library still has a hash,
// so the cycle continues from where it would have been; an in-order item that
// has gone restarts the run.
func (r *channelRun) indexAfter(source *Source, ordered []store.ChannelItem, cursor store.ChannelCursor) int {
	if source.Shuffle {
		last := r.shuffleKey(source, cursor.Cycle, cursor.ItemID)
		for index, item := range ordered {
			key := r.shuffleKey(source, cursor.Cycle, item.ItemID)
			if key > last || key == last && item.ItemID > cursor.ItemID {
				return index
			}
		}
		return len(ordered)
	}
	for index, item := range ordered {
		if item.ItemID == cursor.ItemID {
			return index + 1
		}
	}
	return 0
}

func (r *channelRun) shuffleKey(source *Source, cycle int, itemID int64) uint64 {
	return hash64(r.key, source.Name, fmt.Sprint(cycle), fmt.Sprint(itemID))
}

// changed returns the cursors this run moved, for writing back with the
// programs they produced.
func (r *channelRun) changed() []store.ChannelCursor {
	result := make([]store.ChannelCursor, 0, len(r.dirty))
	for name := range r.dirty {
		result = append(result, r.cursors[name])
	}
	sort.Slice(result, func(a, b int) bool { return result[a].Source < result[b].Source })
	return result
}

// hash64 hashes the parts and then mixes the result, because the order of
// raw FNV values for near-sequential ids is nearly the order of the ids, which
// is no shuffle at all.
func hash64(parts ...string) uint64 {
	h := fnv.New64a()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	x := h.Sum64()
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

// pool is one source's playlist as the library holds it right now.
type pool struct {
	items []store.ChannelItem
	ids   map[int64]bool
}

func (p *pool) contains(itemID int64) bool { return p.ids[itemID] }

type poolCache struct {
	ctx     context.Context
	catalog *store.Store
	pools   map[*Source]*pool
}

// load assembles a source's playlist in its documented order, dropping an item
// that would appear twice, which a marathon of overlapping collections
// otherwise produces.
func (c *poolCache) load(source *Source) (*pool, error) {
	if loaded, ok := c.pools[source]; ok {
		return loaded, nil
	}
	var items []store.ChannelItem
	for _, tmdbID := range source.Shows {
		episodes, err := c.catalog.ChannelShowEpisodes(c.ctx, tmdbID)
		if err != nil {
			return nil, err
		}
		items = append(items, episodes...)
	}
	if len(source.Titles) > 0 {
		titles, err := c.catalog.ChannelTitles(c.ctx, source.Titles)
		if err != nil {
			return nil, err
		}
		items = append(items, titles...)
	}
	if source.hasMovies() {
		movies, err := c.catalog.ChannelMovies(c.ctx, source.movies())
		if err != nil {
			return nil, err
		}
		items = append(items, movies...)
	}
	if source.Shorts {
		shorts, err := c.catalog.ChannelShorts(c.ctx)
		if err != nil {
			return nil, err
		}
		items = append(items, shorts...)
	}
	loaded := &pool{ids: make(map[int64]bool, len(items))}
	for _, item := range items {
		if loaded.ids[item.ItemID] {
			continue
		}
		loaded.ids[item.ItemID] = true
		loaded.items = append(loaded.items, item)
	}
	c.pools[source] = loaded
	return loaded, nil
}
