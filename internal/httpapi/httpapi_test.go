package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/five82/loom/internal/library"
	"github.com/five82/loom/internal/metadata"
	"github.com/five82/loom/internal/store"
	"github.com/five82/loom/internal/tmdb"
)

func TestMediaDownloadMetadataAndVersionedResponses(t *testing.T) {
	catalog, itemID, mediaID, contents := testCatalog(t)
	defer func() { _ = catalog.Close() }()
	manager := library.NewManager(nil, 0, slog.Default())
	api := New(catalog, manager, nil, make(chan struct{}, 1))
	server := httptest.NewServer(api.PublicHandler())
	defer server.Close()

	response, err := http.Get(server.URL + "/api/v1/items/" + strconv.FormatInt(itemID, 10) + "/playback")
	if err != nil {
		t.Fatal(err)
	}
	var playback struct {
		Media     store.MediaFile `json:"media"`
		StreamURL string          `json:"stream_url"`
	}
	if err := json.NewDecoder(response.Body).Decode(&playback); err != nil {
		_ = response.Body.Close()
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || playback.Media.Size != int64(len(contents)) ||
		playback.Media.Filename != "Movie.mkv" || playback.Media.Tag == "" {
		t.Fatalf("playback download metadata status=%d response=%+v", response.StatusCode, playback)
	}
	// The player builds its chapter menu from what playback returns, so the
	// marks have to ride along with the stream URL rather than needing a
	// second request.
	if len(playback.Media.Chapters) != 2 || playback.Media.Chapters[1].StartMS != 264_264 {
		t.Fatalf("playback response omitted chapters: %+v", playback.Media.Chapters)
	}
	wantStreamURL := "/api/v1/media/" + strconv.FormatInt(mediaID, 10) + "?tag=" + playback.Media.Tag
	if playback.StreamURL != wantStreamURL {
		t.Fatalf("stream URL = %q, want %q", playback.StreamURL, wantStreamURL)
	}

	request, err := http.NewRequest(http.MethodGet, server.URL+playback.StreamURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Range", "bytes=2-5")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusPartialContent || string(body) != string(contents[2:6]) {
		t.Fatalf("range response status=%d body=%q", response.StatusCode, body)
	}
	if response.Header.Get("ETag") != strconv.Quote(playback.Media.Tag) ||
		response.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("versioned media headers = %v", response.Header)
	}

	request, err = http.NewRequest(http.MethodHead, server.URL+playback.StreamURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength != int64(len(contents)) ||
		response.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("HEAD response status=%d length=%d headers=%v", response.StatusCode,
			response.ContentLength, response.Header)
	}

	response, err = http.Get(server.URL + "/api/v1/media/" + strconv.FormatInt(mediaID, 10) + "?tag=stale")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("stale media tag status = %d", response.StatusCode)
	}

	media, err := catalog.Media(context.Background(), mediaID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(media.Path, append(contents, 'x'), 0o644); err != nil {
		t.Fatal(err)
	}
	response, err = http.Get(server.URL + playback.StreamURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("replaced media version status = %d", response.StatusCode)
	}
}

// A file can change between scans. Playback must report the version currently on
// disk, otherwise it hands out a stream URL that the media handler itself rejects.
func TestPlaybackReportsLiveFileVersion(t *testing.T) {
	catalog, itemID, mediaID, contents := testCatalog(t)
	defer func() { _ = catalog.Close() }()
	manager := library.NewManager(nil, 0, slog.Default())
	api := New(catalog, manager, nil, make(chan struct{}, 1))
	server := httptest.NewServer(api.PublicHandler())
	defer server.Close()

	playbackURL := server.URL + "/api/v1/items/" + strconv.FormatInt(itemID, 10) + "/playback"
	readPlayback := func() (store.MediaFile, string) {
		t.Helper()
		response, err := http.Get(playbackURL)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		var playback struct {
			Media     store.MediaFile `json:"media"`
			StreamURL string          `json:"stream_url"`
		}
		if err := json.NewDecoder(response.Body).Decode(&playback); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("playback status = %d", response.StatusCode)
		}
		return playback.Media, playback.StreamURL
	}

	before, _ := readPlayback()

	media, err := catalog.Media(context.Background(), mediaID)
	if err != nil {
		t.Fatal(err)
	}
	replaced := append(append([]byte{}, contents...), 'x')
	if err := os.WriteFile(media.Path, replaced, 0o644); err != nil {
		t.Fatal(err)
	}

	after, streamURL := readPlayback()
	if after.Tag == before.Tag {
		t.Fatalf("tag unchanged after the file was replaced: %q", after.Tag)
	}
	if after.Size != int64(len(replaced)) {
		t.Fatalf("size = %d, want %d", after.Size, len(replaced))
	}

	response, err := http.Get(server.URL + streamURL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !bytes.Equal(body, replaced) {
		t.Fatalf("stream after replacement status=%d body=%q", response.StatusCode, body)
	}
}

func TestProgress(t *testing.T) {
	catalog, itemID, _, _ := testCatalog(t)
	defer func() { _ = catalog.Close() }()
	api := New(catalog, library.NewManager(nil, 0, slog.Default()), nil, make(chan struct{}, 1))
	server := httptest.NewServer(api.PublicHandler())
	defer server.Close()

	progressBody, _ := json.Marshal(map[string]int64{"position_ms": 540_000, "duration_ms": 600_000})
	request, err := http.NewRequest(http.MethodPut, server.URL+"/api/v1/items/"+strconv.FormatInt(itemID, 10)+"/progress", bytes.NewReader(progressBody))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var progress store.Progress
	if err := json.NewDecoder(response.Body).Decode(&progress); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !progress.Played {
		t.Fatalf("progress response status=%d progress=%+v", response.StatusCode, progress)
	}
}

func TestPlayedWrites(t *testing.T) {
	catalog, itemID, _, _ := testCatalog(t)
	defer func() { _ = catalog.Close() }()
	api := New(catalog, library.NewManager(nil, 0, slog.Default()), nil, make(chan struct{}, 1))
	server := httptest.NewServer(api.PublicHandler())
	defer server.Close()

	playedURL := server.URL + "/api/v1/items/" + strconv.FormatInt(itemID, 10) + "/played"
	call := func(method, url string) (int, int64) {
		t.Helper()
		request, err := http.NewRequest(method, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		var body struct {
			Updated int64 `json:"updated"`
		}
		_ = json.NewDecoder(response.Body).Decode(&body)
		return response.StatusCode, body.Updated
	}

	if status, updated := call(http.MethodPost, playedURL); status != http.StatusOK || updated != 1 {
		t.Fatalf("mark played status=%d updated=%d", status, updated)
	}
	item, err := catalog.Item(context.Background(), itemID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Progress == nil || !item.Progress.Played {
		t.Fatalf("item was not marked played: %+v", item.Progress)
	}

	if status, updated := call(http.MethodDelete, playedURL); status != http.StatusOK || updated != 1 {
		t.Fatalf("clear played status=%d updated=%d", status, updated)
	}
	if item, err = catalog.Item(context.Background(), itemID); err != nil {
		t.Fatal(err)
	} else if item.Progress != nil {
		t.Fatalf("item kept playback state: %+v", item.Progress)
	}

	missingURL := server.URL + "/api/v1/items/" + strconv.FormatInt(itemID+10_000, 10) + "/played"
	if status, _ := call(http.MethodPost, missingURL); status != http.StatusNotFound {
		t.Fatalf("mark played on an unknown item status = %d", status)
	}
}

func TestItemTechnicalMetadata(t *testing.T) {
	catalog, itemID, _, _ := testCatalog(t)
	defer func() { _ = catalog.Close() }()
	api := New(catalog, library.NewManager(nil, 0, slog.Default()), nil, make(chan struct{}, 1))
	server := httptest.NewServer(api.PublicHandler())
	defer server.Close()

	response, err := http.Get(server.URL + "/api/v1/items/" + strconv.FormatInt(itemID, 10))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var item store.Item
	if err := json.NewDecoder(response.Body).Decode(&item); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || item.Media == nil || len(item.Media.Streams) != 2 {
		t.Fatalf("technical metadata response status=%d item=%+v", response.StatusCode, item)
	}
	video, audio := item.Media.Streams[0], item.Media.Streams[1]
	if video.Codec != "hevc" || video.Profile != "Main 10" || video.DynamicRange != "hdr" ||
		audio.Codec != "opus" || audio.ChannelLayout != "7.1" {
		t.Fatalf("unexpected technical metadata: %+v", item.Media.Streams)
	}
	if len(item.Media.Chapters) != 2 || item.Media.Chapters[0].Title != "Opening" ||
		item.Media.Chapters[1].StartMS != 264_264 {
		t.Fatalf("unexpected chapters: %+v", item.Media.Chapters)
	}
}

func TestMovieGenreAPI(t *testing.T) {
	catalog, itemID, _, _ := testCatalog(t)
	defer func() { _ = catalog.Close() }()
	if err := catalog.UpdateMetadata(context.Background(), itemID, store.MetadataUpdate{
		TMDBID: 10, Title: "Movie",
		Genres: []store.Genre{{ID: 878, Name: "Science Fiction"}},
	}); err != nil {
		t.Fatal(err)
	}
	api := New(catalog, library.NewManager(nil, 0, slog.Default()), nil, make(chan struct{}, 1))
	server := httptest.NewServer(api.PublicHandler())
	defer server.Close()

	response, err := http.Get(server.URL + "/api/v1/genres")
	if err != nil {
		t.Fatal(err)
	}
	var genres struct {
		Items []store.GenreSummary `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&genres); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || len(genres.Items) != 1 ||
		genres.Items[0].ID != 878 || genres.Items[0].ItemCount != 1 {
		t.Fatalf("genres status=%d items=%+v", response.StatusCode, genres.Items)
	}

	response, err = http.Get(server.URL + "/api/v1/items?library=movies&genre_id=878")
	if err != nil {
		t.Fatal(err)
	}
	var items struct {
		Items []store.Item `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&items); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || len(items.Items) != 1 ||
		len(items.Items[0].Genres) != 1 || items.Items[0].Genres[0].ID != 878 {
		t.Fatalf("genre items status=%d items=%+v", response.StatusCode, items.Items)
	}

	response, err = http.Get(server.URL + "/api/v1/items?library=shorts")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("shorts library status=%d", response.StatusCode)
	}

	response, err = http.Get(server.URL + "/api/v1/items?genre_id=invalid")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid genre status=%d", response.StatusCode)
	}
}

func TestSearchAPI(t *testing.T) {
	catalog, itemID, _, _ := testCatalog(t)
	defer func() { _ = catalog.Close() }()
	api := New(catalog, library.NewManager(nil, 0, slog.Default()), nil, make(chan struct{}, 1))
	server := httptest.NewServer(api.PublicHandler())
	defer server.Close()

	response, err := http.Get(server.URL + "/api/v1/search?q=mov&limit=1")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Items  []store.SearchResult `json:"items"`
		Limit  int                  `json:"limit"`
		Offset int                  `json:"offset"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || len(result.Items) != 1 ||
		result.Items[0].ID != itemID || result.Limit != 1 || result.Offset != 0 {
		t.Fatalf("search status=%d result=%+v", response.StatusCode, result)
	}

	response, err = http.Get(server.URL + "/api/v1/search?q=+")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty search status=%d", response.StatusCode)
	}
}

func TestImageSelectionAPI(t *testing.T) {
	catalog, itemID, _, _ := testCatalog(t)
	defer func() { _ = catalog.Close() }()
	if err := catalog.UpdateMetadata(context.Background(), itemID, store.MetadataUpdate{
		TMDBID: 10, Title: "Movie",
	}); err != nil {
		t.Fatal(err)
	}
	imageBytes := testPNG(t)
	provider := http.NewServeMux()
	provider.HandleFunc("/movie/10", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":10,"title":"Movie"}`)
	})
	provider.HandleFunc("/movie/10/images", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"posters":[],"backdrops":[],"logos":[{"file_path":"/selected.png","width":2,"height":3}]}`)
	})
	provider.HandleFunc("/images/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(imageBytes)
	})
	providerServer := httptest.NewServer(provider)
	defer providerServer.Close()
	client := tmdb.NewWithURLs("key", "en-US", providerServer.URL,
		providerServer.URL+"/images", providerServer.Client())
	metadataService := metadata.New(catalog, client, filepath.Join(t.TempDir(), "images"), slog.Default())
	api := New(catalog, library.NewManager(nil, 0, slog.Default()), metadataService, make(chan struct{}, 1))
	server := httptest.NewServer(api.PublicHandler())
	defer server.Close()
	baseURL := server.URL + "/api/v1/items/" + strconv.FormatInt(itemID, 10) + "/images/logo"

	response, err := http.Get(baseURL + "/options")
	if err != nil {
		t.Fatal(err)
	}
	var options struct {
		Items []metadata.ImageOption `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&options); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || len(options.Items) != 1 || options.Items[0].Selected {
		t.Fatalf("image options status=%d options=%+v", response.StatusCode, options.Items)
	}

	request, err := http.NewRequest(http.MethodPut, baseURL,
		bytes.NewBufferString(`{"provider":"tmdb","provider_path":"/selected.png"}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var selected store.Image
	if err := json.NewDecoder(response.Body).Decode(&selected); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || selected.Kind != "logo" ||
		selected.ProviderPath != "/selected.png" || !selected.ManuallySelected || selected.Tag == "" {
		t.Fatalf("selected image status=%d image=%+v", response.StatusCode, selected)
	}

	request, err = http.NewRequest(http.MethodPost, baseURL+"/reset", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var reset store.Image
	if err := json.NewDecoder(response.Body).Decode(&reset); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || reset.Kind != "logo" || reset.ManuallySelected {
		t.Fatalf("reset image status=%d image=%+v", response.StatusCode, reset)
	}
}

func TestImageTagCaching(t *testing.T) {
	catalog, itemID, _, _ := testCatalog(t)
	defer func() { _ = catalog.Close() }()
	contents := []byte("image contents")
	path := filepath.Join(t.TempDir(), "poster.jpg")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	imageID, err := catalog.UpsertImage(context.Background(), store.Image{
		ItemID: itemID, Kind: "poster", Path: path, SourceURL: "https://example/poster.jpg",
		Provider: "tmdb", ProviderPath: "/poster.jpg", Tag: "content-tag",
		ContentType: "image/jpeg", Width: 200, Height: 300,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	api := New(catalog, library.NewManager(nil, 0, slog.Default()), nil, make(chan struct{}, 1))
	server := httptest.NewServer(api.PublicHandler())
	defer server.Close()
	imageURL := server.URL + "/api/v1/images/" + strconv.FormatInt(imageID, 10)

	response, err := http.Get(imageURL + "?tag=content-tag")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusOK || !bytes.Equal(body, contents) ||
		response.Header.Get("ETag") != `"content-tag"` ||
		!strings.Contains(response.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("image response status=%d headers=%v body=%q", response.StatusCode, response.Header, body)
	}

	request, err := http.NewRequest(http.MethodGet, imageURL+"?tag=content-tag", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("If-None-Match", `"content-tag"`)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional image status = %d", response.StatusCode)
	}

	response, err = http.Get(imageURL + "?tag=old-tag")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("stale image tag status = %d", response.StatusCode)
	}

	response, err = http.Get(server.URL + "/api/v1/items/" + strconv.FormatInt(itemID, 10))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var item store.Item
	if err := json.NewDecoder(response.Body).Decode(&item); err != nil {
		t.Fatal(err)
	}
	if item.PosterImageID != imageID || item.PosterImageTag != "content-tag" {
		t.Fatalf("item image reference = %d/%q", item.PosterImageID, item.PosterImageTag)
	}
}

func TestImageWidthVariants(t *testing.T) {
	catalog, itemID, _, _ := testCatalog(t)
	defer func() { _ = catalog.Close() }()
	picture := image.NewRGBA(image.Rect(0, 0, 600, 400))
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "backdrop-testtag.png")
	if err := os.WriteFile(path, encoded.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	imageID, err := catalog.UpsertImage(context.Background(), store.Image{
		ItemID: itemID, Kind: "backdrop", Path: path, SourceURL: "https://example/backdrop.png",
		Provider: "tmdb", ProviderPath: "/backdrop.png", Tag: "backdrop-tag",
		ContentType: "image/png", Width: 600, Height: 400,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	api := New(catalog, library.NewManager(nil, 0, slog.Default()), nil, make(chan struct{}, 1))
	server := httptest.NewServer(api.PublicHandler())
	defer server.Close()
	imageURL := server.URL + "/api/v1/images/" + strconv.FormatInt(imageID, 10)

	response, err := http.Get(imageURL + "?width=240&tag=backdrop-tag")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusOK ||
		!strings.Contains(response.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("variant response status=%d headers=%v", response.StatusCode, response.Header)
	}
	resized, format, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if format != "png" || resized.Bounds().Dx() != 240 || resized.Bounds().Dy() != 160 {
		t.Fatalf("variant = %s %dx%d, want png 240x160", format,
			resized.Bounds().Dx(), resized.Bounds().Dy())
	}

	// An oversized request snaps to the largest bucket, which is wider than
	// the original, so the original is served unchanged.
	response, err = http.Get(imageURL + "?width=5000")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusOK || !bytes.Equal(body, encoded.Bytes()) {
		t.Fatalf("oversized width should serve the original: status=%d", response.StatusCode)
	}

	for _, value := range []string{"abc", "0", "-1"} {
		response, err = http.Get(imageURL + "?width=" + value)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("width=%s status = %d, want 400", value, response.StatusCode)
		}
	}
}

// Scans are triggered over the LAN API so a client or an off-host ingest workflow
// can pick up new files. The local socket handler must keep serving the same route
// for the CLI.
func TestScanTriggerAndStatus(t *testing.T) {
	catalog, _, _, _ := testCatalog(t)
	defer func() { _ = catalog.Close() }()
	// A manager with no Run goroutine leaves the first triggered scan queued, which
	// is what the busy rejection below needs.
	api := New(catalog, library.NewManager(nil, 0, slog.Default()), nil, make(chan struct{}, 1))
	server := httptest.NewServer(api.PublicHandler())
	defer server.Close()
	local := httptest.NewServer(api.LocalHandler())
	defer local.Close()

	var status library.ScanStatus
	if code := getJSON(t, server.URL+"/api/v1/scan", &status); code != http.StatusOK || status.Running {
		t.Fatalf("initial scan status code=%d status=%+v", code, status)
	}
	if code := postScan(t, server.URL, `{"library":"nope"}`); code != http.StatusBadRequest {
		t.Fatalf("unknown library status = %d, want 400", code)
	}
	if code := postScan(t, server.URL, `{"library":"movies"}`); code != http.StatusAccepted {
		t.Fatalf("scan trigger status = %d, want 202", code)
	}
	if code := postScan(t, server.URL, ""); code != http.StatusConflict {
		t.Fatalf("second scan trigger status = %d, want 409", code)
	}
	if code := postScan(t, local.URL, ""); code != http.StatusConflict {
		t.Fatalf("scan trigger over local handler status = %d, want 409", code)
	}
	if code := getJSON(t, server.URL+"/api/v1/scan", &status); code != http.StatusOK || !status.Running {
		t.Fatalf("running scan status code=%d status=%+v", code, status)
	}
}

func postScan(t *testing.T, baseURL, body string) int {
	t.Helper()
	response, err := http.Post(baseURL+"/api/v1/scan", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode
}

func getJSON(t *testing.T, url string, value any) int {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if err := json.NewDecoder(response.Body).Decode(value); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 2, 3))
	for y := range 3 {
		for x := range 2 {
			picture.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, picture); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func testCatalog(t *testing.T) (*store.Store, int64, int64, []byte) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	contents := []byte("0123456789")
	path := filepath.Join(root, "Movie.mkv")
	if err := os.WriteFile(path, contents, 0o644); err != nil {
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
	itemID, err := catalog.UpsertItem(ctx, store.ItemInput{
		LibraryID: libraryID, SourceKey: "movie", Kind: "movie", Title: "Movie", ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, err := catalog.UpsertMedia(ctx, store.MediaFile{
		ItemID: itemID, Path: path, Size: info.Size(), MTimeNS: info.ModTime().UnixNano(),
		DurationMS: 600_000, LastSeenScanID: scanID,
	}, []store.Stream{{
		Index: 0, Kind: "video", Codec: "hevc", Profile: "Main 10", Width: 3840,
		Height: 1604, DynamicRange: "hdr", IsDefault: true,
	}, {
		Index: 1, Kind: "audio", Codec: "opus", Channels: 8, ChannelLayout: "7.1",
		IsDefault: true,
	}}, []store.Chapter{
		{Index: 0, StartMS: 0, Title: "Opening"},
		{Index: 1, StartMS: 264_264},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, libraryID, scanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	return catalog, itemID, mediaID, contents
}

func TestCollectionsServeOwnedMembersOnly(t *testing.T) {
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
	// Three of the five Star Wars films, and one lone Nolan film. The Star Wars
	// shelf is served without the two it is missing; the Nolan shelf is not
	// served at all, because one movie under a heading is worse than none.
	for _, movie := range []struct {
		tmdbID      int64
		title       string
		year        int
		releaseDate string
	}{
		{1892, "Return of the Jedi", 1983, "1983-05-25"},
		{11, "Star Wars", 1977, "1977-05-25"},
		{1891, "The Empire Strikes Back", 1980, "1980-05-20"},
		{27205, "Inception", 2010, "2010-07-15"},
	} {
		itemID, err := catalog.UpsertItem(ctx, store.ItemInput{
			LibraryID: libraryID, SourceKey: movie.title, Kind: "movie",
			Title: movie.title, Year: movie.year, ScanID: scanID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := catalog.UpdateMetadata(ctx, itemID, store.MetadataUpdate{
			TMDBID: movie.tmdbID, Title: movie.title, Year: movie.year,
			ReleaseDate: movie.releaseDate,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.FinishScan(ctx, libraryID, scanID, 4, 4, 0, nil); err != nil {
		t.Fatal(err)
	}

	api := New(catalog, library.NewManager(nil, 0, slog.Default()), nil, make(chan struct{}, 1))
	server := httptest.NewServer(api.PublicHandler())
	defer server.Close()
	response, err := http.Get(server.URL + "/api/v1/collections")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Items []struct {
			Slug  string       `json:"slug"`
			Title string       `json:"title"`
			Items []store.Item `json:"items"`
		} `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		_ = response.Body.Close()
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("collections status = %d", response.StatusCode)
	}
	if len(body.Items) != 1 {
		t.Fatalf("expected only the Star Wars shelf: %+v", body.Items)
	}
	shelf := body.Items[0]
	if shelf.Slug != "star-wars" || shelf.Title != "Star Wars" || len(shelf.Items) != 3 {
		t.Fatalf("unexpected shelf: %+v", shelf)
	}
	// Release order, not the alphabetical order a grid would use.
	if shelf.Items[0].Title != "Star Wars" || shelf.Items[1].Title != "The Empire Strikes Back" ||
		shelf.Items[2].Title != "Return of the Jedi" {
		t.Fatalf("shelf is not in release order: %+v", shelf.Items)
	}
}
