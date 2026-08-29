package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/five82/loom/internal/channels"
	"github.com/five82/loom/internal/store"
)

// channelHoursDefault is the horizon the generator keeps filled; a client asking
// for more gets whatever is already scheduled.
const channelHoursDefault = 24

const channelHoursMax = 48

// channelVideo is the first video stream of a program's file, cut down to what
// a player needs to decide whether it can play the program at all.
type channelVideo struct {
	Codec        string `json:"codec"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Resolution   string `json:"resolution"`
	DynamicRange string `json:"dynamic_range"`
}

type channelProgram struct {
	ID       int64      `json:"id"`
	StartsAt string     `json:"starts_at"`
	EndsAt   string     `json:"ends_at"`
	Item     store.Item `json:"item"`
	// Video and StreamURL are absent when the file was never probed or is no
	// longer on disk. The program still airs; the client shows what it cannot
	// play rather than a hole in the schedule.
	Video     *channelVideo `json:"video,omitempty"`
	StreamURL string        `json:"stream_url,omitempty"`
}

type channelLineup struct {
	store.Channel
	Programs []channelProgram `json:"programs"`
}

// channels serves the whole lineup with each channel's schedule around now, so
// a client can flip channels and land mid-program without asking Loom anything
// on the way.
func (a *API) channels(w http.ResponseWriter, r *http.Request) {
	hours := channelHoursDefault
	if value := r.URL.Query().Get("hours"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > channelHoursMax {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("hours must be between 1 and %d", channelHoursMax))
			return
		}
		hours = parsed
	}
	now := time.Now().UTC().Truncate(time.Second)
	window := time.Duration(hours) * time.Hour

	// The generator worker keeps the schedule ahead of now, but a request that
	// arrives before it has run - a daemon that just started, or one whose
	// channels are new - regenerates first. Generating is cheap and it keeps
	// this endpoint correct on its own, like the featured pick.
	needed := window
	if needed > channels.Horizon {
		needed = channels.Horizon
	}
	reach, err := a.store.ChannelScheduleReach(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Stored program times are fixed-width UTC, so the empty reach of a lineup
	// with no programs compares as earlier than any horizon.
	if reach < store.ChannelTime(now.Add(needed)) {
		if _, err := a.channelGenerator.Update(r.Context(), now); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	lineup, programs, err := a.store.ChannelLineup(r.Context(),
		store.ChannelTime(now.Add(-time.Hour)), store.ChannelTime(now.Add(window)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	streamURLs := make(map[int64]string)
	items := make([]channelLineup, 0, len(lineup))
	for _, channel := range lineup {
		scheduled := programs[channel.ID]
		entry := channelLineup{Channel: channel, Programs: make([]channelProgram, 0, len(scheduled))}
		for _, program := range scheduled {
			entry.Programs = append(entry.Programs, channelProgram{
				ID: program.ID, StartsAt: program.StartsAt, EndsAt: program.EndsAt,
				Item: program.Item, Video: programVideo(program.Video),
				StreamURL: streamURL(program.MediaID, program.MediaPath, streamURLs),
			})
		}
		items = append(items, entry)
	}
	// A struct rather than a map, so the response reads in the documented order
	// with the server clock first.
	writeJSON(w, http.StatusOK, struct {
		Now   string          `json:"now"`
		Items []channelLineup `json:"items"`
	}{Now: store.ChannelTime(now), Items: items})
}

func programVideo(stream *store.Stream) *channelVideo {
	if stream == nil {
		return nil
	}
	return &channelVideo{
		Codec: stream.Codec, Width: stream.Width, Height: stream.Height,
		Resolution: stream.Resolution, DynamicRange: stream.DynamicRange,
	}
}

// streamURL reports the file the way the playback endpoint does, from a stat at
// request time, so the media endpoint accepts every URL the lineup hands out. A
// channel repeats files inside its window, so each one is stat'ed once.
func streamURL(mediaID int64, path string, cache map[int64]string) string {
	if mediaID == 0 || path == "" {
		return ""
	}
	if url, ok := cache[mediaID]; ok {
		return url
	}
	url := ""
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		url = fmt.Sprintf("/api/v1/media/%d?tag=%s", mediaID,
			store.MediaTag(mediaID, info.Size(), info.ModTime().UnixNano()))
	}
	cache[mediaID] = url
	return url
}
