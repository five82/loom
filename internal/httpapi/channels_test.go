package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/five82/loom/internal/channels"
	"github.com/five82/loom/internal/library"
	"github.com/five82/loom/internal/store"
)

type channelsResponse struct {
	Now   string         `json:"now"`
	Items []channelEntry `json:"items"`
}

type channelEntry = struct {
	ID       int64  `json:"id"`
	Number   int    `json:"number"`
	Key      string `json:"key"`
	Name     string `json:"name"`
	Programs []struct {
		ID       int64      `json:"id"`
		StartsAt string     `json:"starts_at"`
		EndsAt   string     `json:"ends_at"`
		Item     store.Item `json:"item"`
		Video    *struct {
			Codec        string `json:"codec"`
			Width        int    `json:"width"`
			Height       int    `json:"height"`
			Resolution   string `json:"resolution"`
			DynamicRange string `json:"dynamic_range"`
		} `json:"video"`
		StreamURL string `json:"stream_url"`
	} `json:"programs"`
}

// channelCatalog holds one movie whose file is on disk, one whose file was
// removed after the scan, and one the scanner could not probe, so a lineup
// drawn from it exercises both stream URL outcomes and leaves the Western genre
// with nothing to air.
func channelCatalog(t *testing.T) (catalog *store.Store, path string, presentID, presentMediaID, missingID int64) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	path = filepath.Join(root, "Present.mkv")
	if err := os.WriteFile(path, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := store.Open(filepath.Join(root, "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	libraryID, scanID, err := catalog.StartScan(ctx, "movies", root)
	if err != nil {
		t.Fatal(err)
	}
	add := func(title, mediaPath string, minutes int, genre store.Genre) (int64, int64) {
		itemID, err := catalog.UpsertItem(ctx, store.ItemInput{
			LibraryID: libraryID, SourceKey: title, Kind: "movie", Title: title, ScanID: scanID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := catalog.UpdateMetadata(ctx, itemID, store.MetadataUpdate{
			TMDBID: itemID, Title: title, Genres: []store.Genre{genre},
		}); err != nil {
			t.Fatal(err)
		}
		var streams []store.Stream
		if minutes > 0 {
			streams = []store.Stream{{
				Index: 0, Kind: "video", Codec: "hevc", Width: 1920, Height: 1080,
				DynamicRange: "sdr", IsDefault: true,
			}, {
				Index: 1, Kind: "audio", Codec: "opus", Channels: 6, IsDefault: true,
			}}
		}
		mediaID, err := catalog.UpsertMedia(ctx, store.MediaFile{
			ItemID: itemID, Path: mediaPath, Size: 10, MTimeNS: 20,
			DurationMS: int64(minutes) * 60_000, Container: "matroska", LastSeenScanID: scanID,
		}, streams, nil)
		if err != nil {
			t.Fatal(err)
		}
		return itemID, mediaID
	}
	action := store.Genre{ID: 28, Name: "Action"}
	presentID, presentMediaID = add("Present", path, 90, action)
	missingID, _ = add("Missing", filepath.Join(root, "Missing.mkv"), 120, action)
	add("Unprobed", filepath.Join(root, "Unprobed.mkv"), 0, store.Genre{ID: 99, Name: "Western"})
	if err := catalog.FinishScan(ctx, libraryID, scanID, 3, 3, 0, nil); err != nil {
		t.Fatal(err)
	}
	return catalog, path, presentID, presentMediaID, missingID
}

func channelsServer(t *testing.T, catalog *store.Store) *httptest.Server {
	t.Helper()
	api := New(catalog, library.NewManager(nil, 0, slog.Default()), nil, channels.New(catalog),
		make(chan struct{}, 1), ListenAddresses{})
	server := httptest.NewServer(api.PublicHandler())
	t.Cleanup(server.Close)
	return server
}

func getChannels(t *testing.T, server *httptest.Server, query string) (channelsResponse, string) {
	t.Helper()
	response, err := http.Get(server.URL + "/api/v1/channels" + query)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("channels status = %d, body %s", response.StatusCode, body)
	}
	var lineup channelsResponse
	if err := json.Unmarshal(body, &lineup); err != nil {
		t.Fatal(err)
	}
	return lineup, string(body)
}

func channelByKey(t *testing.T, lineup channelsResponse, key string) channelEntry {
	t.Helper()
	for _, channel := range lineup.Items {
		if channel.Key == key {
			return channel
		}
	}
	t.Fatalf("channel %q is not in the lineup", key)
	return channelEntry{}
}

// The endpoint answers from a schedule it generates itself, so a client can ask
// a daemon that has only just started.
func TestChannelsAPIServesTheWholeLineup(t *testing.T) {
	catalog, path, present, presentMedia, missing := channelCatalog(t)
	defer func() { _ = catalog.Close() }()
	// A channel the lineup no longer names is dropped.
	if _, err := catalog.CreateChannel(context.Background(), "genre:99", "Western",
		store.ChannelTime(time.Now())); err != nil {
		t.Fatal(err)
	}
	server := channelsServer(t, catalog)
	before := time.Now().UTC().Truncate(time.Second)

	lineup, body := getChannels(t, server, "")
	now, err := time.Parse(time.RFC3339, lineup.Now)
	if err != nil {
		t.Fatalf("now = %q: %v", lineup.Now, err)
	}
	if now.Before(before) || now.After(before.Add(time.Minute)) || !strings.HasSuffix(lineup.Now, "Z") {
		t.Fatalf("now = %q, want a UTC server clock near %s", lineup.Now, before)
	}
	// Every lineup channel is served in lineup order, whether or not the
	// catalog has anything for it, and the stale one is gone. Its number is
	// not reused.
	if len(lineup.Items) != len(channels.Lineup) {
		var keys []string
		for _, channel := range lineup.Items {
			keys = append(keys, channel.Key)
		}
		t.Fatalf("lineup = %v", keys)
	}
	for index, channel := range lineup.Items {
		if channel.Key != channels.Lineup[index].Key || channel.Name != channels.Lineup[index].Name ||
			channel.Number != index+2 || channel.ID == 0 {
			t.Fatalf("channel %d = %+v, want %s at %d", index, channel, channels.Lineup[index].Key, index+2)
		}
	}
	// An empty schedule is an empty list, not null.
	if southPark := channelByKey(t, lineup, "south-park"); len(southPark.Programs) != 0 ||
		!strings.Contains(body, `"programs":[]`) {
		t.Fatalf("South Park programs = %d", len(southPark.Programs))
	}

	action := channelByKey(t, lineup, "action").Programs
	if len(action) < 2 {
		t.Fatalf("Action channel scheduled %d programs", len(action))
	}
	// The channel was joined in progress: the first program is already under
	// way rather than starting on the instant the schedule was made.
	if action[0].StartsAt >= lineup.Now || action[0].EndsAt <= lineup.Now {
		t.Fatalf("first program runs %s to %s, want it under way at %s",
			action[0].StartsAt, action[0].EndsAt, lineup.Now)
	}
	if last := action[len(action)-1]; last.EndsAt < store.ChannelTime(now.Add(24*time.Hour)) {
		t.Fatalf("schedule reaches only %s", last.EndsAt)
	}
	seen := map[int64]bool{}
	for index, program := range action {
		seen[program.Item.ID] = true
		if program.ID == 0 || program.Item.Title == "" || program.Item.DurationMS == 0 {
			t.Fatalf("program %d: id %d, title %q, duration %d",
				index, program.ID, program.Item.Title, program.Item.DurationMS)
		}
		if program.Item.Progress != nil || len(program.Item.Credits) > 0 {
			t.Fatalf("program %d carries progress or credits", index)
		}
		if len(program.Item.Genres) != 1 || program.Item.Genres[0].Name != "Action" {
			t.Fatalf("program %d genres = %+v", index, program.Item.Genres)
		}
		if index > 0 && program.StartsAt != action[index-1].EndsAt {
			t.Fatalf("program %d starts at %s, previous ends at %s",
				index, program.StartsAt, action[index-1].EndsAt)
		}
		if program.Video == nil || program.Video.Codec != "hevc" || program.Video.Width != 1920 ||
			program.Video.Height != 1080 || program.Video.Resolution != "1080p" ||
			program.Video.DynamicRange != "sdr" {
			t.Fatalf("program %d video = %+v", index, program.Video)
		}
		switch program.Item.ID {
		case present:
			if program.StreamURL == "" {
				t.Fatalf("program %d has no stream URL", index)
			}
		case missing:
			// The file is gone, so the program airs without a stream URL.
			if program.StreamURL != "" {
				t.Fatalf("program %d for a missing file = %q", index, program.StreamURL)
			}
		default:
			t.Fatalf("program %d aired unknown item %d", index, program.Item.ID)
		}
	}
	if !seen[present] || !seen[missing] {
		t.Fatalf("a day of Action aired only %v", seen)
	}

	// The stream URL is built from the file on disk, so the media endpoint
	// accepts it.
	streamURL := ""
	for _, program := range action {
		if program.Item.ID == present {
			streamURL = program.StreamURL
			break
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("/api/v1/media/%d?tag=%s", presentMedia,
		store.MediaTag(presentMedia, info.Size(), info.ModTime().UnixNano()))
	if streamURL != want {
		t.Fatalf("stream URL = %q, want %q", streamURL, want)
	}
	response, err := http.Get(server.URL + streamURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("stream URL from the lineup returned %d", response.StatusCode)
	}
}

func TestChannelsAPIWindowAndValidation(t *testing.T) {
	catalog, _, _, _, _ := channelCatalog(t)
	defer func() { _ = catalog.Close() }()
	server := channelsServer(t, catalog)

	full, _ := getChannels(t, server, "?hours=24")
	short, _ := getChannels(t, server, "?hours=1")
	if len(short.Items) != len(full.Items) {
		t.Fatalf("one hour returned %d channels, want %d", len(short.Items), len(full.Items))
	}
	fullAction := channelByKey(t, full, "action").Programs
	shortAction := channelByKey(t, short, "action").Programs
	if len(shortAction) == 0 || len(shortAction) >= len(fullAction) {
		t.Fatalf("one hour returned %d programs, twenty-four returned %d",
			len(shortAction), len(fullAction))
	}
	now, err := time.Parse(time.RFC3339, short.Now)
	if err != nil {
		t.Fatal(err)
	}
	for _, program := range shortAction {
		if program.StartsAt >= store.ChannelTime(now.Add(time.Hour)) {
			t.Fatalf("program starting at %s is outside the requested window", program.StartsAt)
		}
		if program.EndsAt <= store.ChannelTime(now.Add(-time.Hour)) {
			t.Fatalf("program ending at %s is outside the requested window", program.EndsAt)
		}
	}

	for _, query := range []string{"?hours=0", "?hours=49", "?hours=-1", "?hours=abc"} {
		response, err := http.Get(server.URL + "/api/v1/channels" + query)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("channels%s status = %d, want 400", query, response.StatusCode)
		}
	}
}

// A catalog with nothing to air still answers with the whole lineup, every
// channel dark.
func TestChannelsAPIEmptyCatalog(t *testing.T) {
	catalog, err := store.Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	server := channelsServer(t, catalog)
	lineup, body := getChannels(t, server, "")
	if len(lineup.Items) != len(channels.Lineup) || !strings.Contains(body, `"programs":[]`) {
		t.Fatalf("empty catalog lineup = %s", body)
	}
	for _, channel := range lineup.Items {
		if len(channel.Programs) != 0 {
			t.Fatalf("channel %q scheduled %d programs from an empty catalog", channel.Key, len(channel.Programs))
		}
	}
}
