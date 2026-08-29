// Package channels generates Loom's linear TV lineup: a handful of channels
// derived from the catalog, each with a rolling schedule of back-to-back
// programs that a client can tune into at any moment.
package channels

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/five82/loom/internal/store"
)

const (
	// Horizon is how far ahead every channel is scheduled.
	Horizon = 24 * time.Hour
	// retention keeps finished programs around for a while so a client that
	// tuned in late still sees what it is playing, and so a schedule survives a
	// short outage without a visible hole.
	retention = 6 * time.Hour
	// The proof-of-concept lineup: four shows, four movie genres, HDR and Mix.
	showChannels  = 4
	genreChannels = 4
)

// Generator owns the lineup and the schedule. The daemon worker and the API
// share one, and its lock is what keeps a self-healing request from scheduling
// the same hours the worker is already appending.
type Generator struct {
	catalog *store.Store
	mu      sync.Mutex
	random  *rand.Rand
}

// Stats reports what one update changed, for the daemon log.
type Stats struct {
	ChannelsCreated int
	ProgramsPruned  int
	ProgramsAdded   int
}

func New(catalog *store.Store) *Generator {
	//nolint:gosec // channel order only has to look unplanned, not be unguessable
	return NewWithRandom(catalog, rand.New(rand.NewSource(time.Now().UnixNano())))
}

// NewWithRandom fixes the source the shuffled channels draw from, so a test can
// predict the lineup.
func NewWithRandom(catalog *store.Store, random *rand.Rand) *Generator {
	return &Generator{catalog: catalog, random: random}
}

// Update reconciles the lineup and extends every schedule.
func (g *Generator) Update(ctx context.Context, now time.Time) (Stats, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	created, err := g.reconcile(ctx, now)
	if err != nil {
		return Stats{}, err
	}
	pruned, added, err := g.extend(ctx, now)
	return Stats{ChannelsCreated: created, ProgramsPruned: pruned, ProgramsAdded: added}, err
}

// Reconcile makes the channel list match the catalog and reports how many
// channels it added.
func (g *Generator) Reconcile(ctx context.Context, now time.Time) (int, error) {
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

// reconcile adds a channel for every key the catalog currently justifies: the
// four shows with the most episodes, the four best represented movie genres,
// HDR, and Mix. A channel whose key stops qualifying keeps its number and its
// schedule, because a lineup that renumbers itself is worse than one carrying a
// channel that has gone quiet.
func (g *Generator) reconcile(ctx context.Context, now time.Time) (int, error) {
	shows, err := g.catalog.TopChannelShows(ctx, showChannels)
	if err != nil {
		return 0, err
	}
	genres, err := g.catalog.TopChannelMovieGenres(ctx, genreChannels)
	if err != nil {
		return 0, err
	}
	desired := make([]store.Channel, 0, showChannels+genreChannels+2)
	for _, show := range shows {
		desired = append(desired, store.Channel{
			Key: fmt.Sprintf("show:%d", show.ID), Name: show.Name, Kind: "show", ItemID: show.ID,
		})
	}
	for _, genre := range genres {
		desired = append(desired, store.Channel{
			Key: fmt.Sprintf("genre:%d", genre.ID), Name: genre.Name, Kind: "genre", GenreID: genre.ID,
		})
	}
	desired = append(desired,
		store.Channel{Key: "hdr", Name: "HDR", Kind: "hdr"},
		store.Channel{Key: "mix", Name: "Mix", Kind: "mix"})

	existing, err := g.catalog.Channels(ctx)
	if err != nil {
		return 0, err
	}
	current := make(map[string]store.Channel, len(existing))
	for _, channel := range existing {
		current[channel.Key] = channel
	}
	created := 0
	for _, wanted := range desired {
		held, ok := current[wanted.Key]
		if !ok {
			// A channel appears once it has something to air, so a library with
			// no HDR in it is not given an HDR channel that stays dark.
			eligible, err := g.catalog.ChannelEligibleItems(ctx, wanted)
			if err != nil {
				return created, err
			}
			if len(eligible) == 0 {
				continue
			}
			if _, err := g.catalog.CreateChannel(ctx, wanted, store.ChannelTime(now)); err != nil {
				return created, err
			}
			created++
			continue
		}
		if held.Name != wanted.Name {
			if err := g.catalog.RenameChannel(ctx, held.ID, wanted.Name); err != nil {
				return created, err
			}
		}
	}
	return created, nil
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
	added := 0
	for _, channel := range channels {
		count, err := g.extendChannel(ctx, channel, now)
		added += count
		if err != nil {
			return pruned, added, err
		}
	}
	return pruned, added, nil
}

func (g *Generator) extendChannel(ctx context.Context, channel store.Channel, now time.Time) (int, error) {
	eligible, err := g.catalog.ChannelEligibleItems(ctx, channel)
	if err != nil {
		return 0, err
	}
	// A channel with nothing left to air keeps its programs and its number and
	// simply stops being extended.
	if len(eligible) == 0 {
		return 0, nil
	}
	end, err := g.catalog.ChannelScheduleEnd(ctx, channel.ID)
	if err != nil {
		return 0, err
	}
	// A channel that ran dry while Loom was down restarts at now instead of
	// replaying the hours it missed.
	cursor := now
	if end != "" {
		last, err := store.ParseChannelTime(end)
		if err != nil {
			return 0, err
		}
		if last.After(now) {
			cursor = last
		}
	}
	horizon := now.Add(Horizon)
	if !cursor.Before(horizon) {
		return 0, nil
	}
	scheduled, err := g.catalog.ChannelProgramItems(ctx, channel.ID)
	if err != nil {
		return 0, err
	}
	aired := make(map[int64]bool, len(scheduled))
	for _, itemID := range scheduled {
		aired[itemID] = true
	}
	previous := int64(0)
	if len(scheduled) > 0 {
		previous = scheduled[len(scheduled)-1]
	}
	var programs []store.ScheduledProgram
	for cursor.Before(horizon) {
		next := g.pick(channel, eligible, aired, previous)
		starts := cursor
		cursor = starts.Add(time.Duration(next.DurationMS) * time.Millisecond).Truncate(time.Second)
		// Program boundaries are whole seconds, so a file shorter than a second
		// still has to occupy one or the schedule would never advance.
		if !cursor.After(starts) {
			cursor = starts.Add(time.Second)
		}
		programs = append(programs, store.ScheduledProgram{
			ItemID: next.ItemID, StartsAt: store.ChannelTime(starts), EndsAt: store.ChannelTime(cursor),
		})
		aired[next.ItemID] = true
		previous = next.ItemID
	}
	if err := g.catalog.AppendChannelPrograms(ctx, channel.ID, programs); err != nil {
		return 0, err
	}
	return len(programs), nil
}

// pick chooses a channel's next program. A show airs its episodes in order and
// loops; every other channel picks at random from what it has not aired inside
// its stored window, and repeats only once it has run out.
func (g *Generator) pick(
	channel store.Channel, eligible []store.ChannelItem, aired map[int64]bool, previous int64,
) store.ChannelItem {
	if channel.Kind == "show" {
		next := 0
		for index, item := range eligible {
			if item.ItemID == previous {
				next = (index + 1) % len(eligible)
				break
			}
		}
		return eligible[next]
	}
	candidates := make([]store.ChannelItem, 0, len(eligible))
	for _, item := range eligible {
		if !aired[item.ItemID] {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		candidates = eligible
	}
	return candidates[g.random.Intn(len(candidates))]
}
