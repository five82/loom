package metadata

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/five82/loom/internal/store"
	"github.com/five82/loom/internal/tmdb"
)

func TestMovieMetadataImageSelectionAndReset(t *testing.T) {
	defaultImage := encodedPNG(t, color.RGBA{R: 255, A: 255})
	alternateImage := encodedPNG(t, color.RGBA{B: 255, A: 255})
	alternateLogo := encodedPNG(t, color.RGBA{G: 255, A: 255})
	mux := http.NewServeMux()
	mux.HandleFunc("/search/movie", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"results":[{"id":329865,"title":"Arrival","release_date":"2016-11-10","vote_average":7.6,"vote_count":19608},{"id":472349,"title":"Arrival","release_date":"2016-05-25","vote_average":4.9,"vote_count":8}]}`)
	})
	mux.HandleFunc("/movie/329865", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":329865,"title":"Arrival","overview":"A linguist meets visitors.","release_date":"2016-11-10","poster_path":"/poster.jpg","backdrop_path":"/backdrop.jpg","genres":[{"id":18,"name":"Drama"},{"id":878,"name":"Science Fiction"}]}`)
	})
	mux.HandleFunc("/movie/22", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":22,"title":"Different Movie"}`)
	})
	mux.HandleFunc("/movie/329865/images", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"posters":[{"file_path":"/poster.jpg","width":2,"height":3},{"file_path":"/alternate.jpg","width":2,"height":3}],"backdrops":[{"file_path":"/titled.jpg","iso_639_1":"en","width":16,"height":9},{"file_path":"/clean.jpg","width":16,"height":9}],"logos":[{"file_path":"/logo.png","width":3,"height":2},{"file_path":"/alternate-logo.png","width":3,"height":2}]}`)
	})
	mux.HandleFunc("/images/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/alternate.jpg"):
			_, _ = w.Write(alternateImage)
		case strings.HasSuffix(r.URL.Path, "/alternate-logo.png"):
			_, _ = w.Write(alternateLogo)
		default:
			_, _ = w.Write(defaultImage)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx := context.Background()
	root := t.TempDir()
	catalog, err := store.Open(filepath.Join(root, "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	libraryID, scanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := catalog.UpsertItem(ctx, store.ItemInput{
		LibraryID: libraryID, SourceKey: "Arrival (2016)", Kind: "movie",
		Title: "Arrival", Year: 2016, ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	client := tmdb.NewWithURLs("key", "en-US", server.URL, server.URL+"/images", server.Client())
	service := New(catalog, client, filepath.Join(root, "images"), slog.Default())
	if err := service.AutoMatch(ctx, itemID); err != nil {
		t.Fatal(err)
	}
	item, err := catalog.Item(ctx, itemID)
	if err != nil {
		t.Fatal(err)
	}
	if item.TMDBID != 329865 || item.Overview == "" || item.PosterImageID == 0 ||
		item.BackdropImageID == 0 || item.LogoImageID == 0 || item.ThumbImageID == 0 ||
		item.PosterImageTag == "" || item.LogoImageTag == "" || item.ThumbImageTag == "" ||
		!item.GenresLoaded || len(item.Genres) != 2 || item.Genres[1].ID != 878 {
		t.Fatalf("metadata not applied: %+v", item)
	}
	backdrop, err := catalog.Image(ctx, item.BackdropImageID)
	if err != nil {
		t.Fatal(err)
	}
	if backdrop.ProviderPath != "/clean.jpg" {
		t.Fatalf("default backdrop should be the textless one: %+v", backdrop)
	}
	thumb, err := catalog.Image(ctx, item.ThumbImageID)
	if err != nil {
		t.Fatal(err)
	}
	if thumb.ProviderPath != "/titled.jpg" {
		t.Fatalf("default thumb should be the titled backdrop: %+v", thumb)
	}
	backdropOptions, err := service.ImageOptions(ctx, itemID, "backdrop")
	if err != nil {
		t.Fatal(err)
	}
	if len(backdropOptions) != 1 || backdropOptions[0].ProviderPath != "/clean.jpg" ||
		!backdropOptions[0].Selected {
		t.Fatalf("backdrop options = %+v", backdropOptions)
	}
	thumbOptions, err := service.ImageOptions(ctx, itemID, "thumb")
	if err != nil {
		t.Fatal(err)
	}
	if len(thumbOptions) != 1 || thumbOptions[0].ProviderPath != "/titled.jpg" ||
		!thumbOptions[0].Selected || !strings.Contains(thumbOptions[0].ThumbnailURL, "/w780/") {
		t.Fatalf("thumb options = %+v", thumbOptions)
	}
	poster, err := catalog.Image(ctx, item.PosterImageID)
	if err != nil {
		t.Fatal(err)
	}
	if poster.ManuallySelected || poster.ProviderPath != "/poster.jpg" || poster.Width != 2 || poster.Height != 3 {
		t.Fatalf("default poster = %+v", poster)
	}
	if data, err := os.ReadFile(poster.Path); err != nil || !bytes.Equal(data, defaultImage) {
		t.Fatalf("saved default image differs: %v", err)
	}
	logo, err := catalog.Image(ctx, item.LogoImageID)
	if err != nil {
		t.Fatal(err)
	}
	if logo.ManuallySelected || logo.ProviderPath != "/logo.png" || logo.Width != 2 || logo.Height != 3 {
		t.Fatalf("default logo = %+v", logo)
	}
	logoOptions, err := service.ImageOptions(ctx, itemID, "logo")
	if err != nil {
		t.Fatal(err)
	}
	if len(logoOptions) != 2 || !logoOptions[0].Selected ||
		!strings.Contains(logoOptions[1].ThumbnailURL, "/w300/") {
		t.Fatalf("logo options = %+v", logoOptions)
	}
	selectedLogo, err := service.SelectImage(ctx, itemID, "logo", "tmdb", "/alternate-logo.png")
	if err != nil {
		t.Fatal(err)
	}
	if !selectedLogo.ManuallySelected || selectedLogo.ProviderPath != "/alternate-logo.png" ||
		selectedLogo.Tag == logo.Tag {
		t.Fatalf("selected logo = %+v", selectedLogo)
	}

	options, err := service.ImageOptions(ctx, itemID, "poster")
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 2 || !options[0].Selected || options[1].ThumbnailURL == "" {
		t.Fatalf("poster options = %+v", options)
	}
	selected, err := service.SelectImage(ctx, itemID, "poster", "tmdb", "/alternate.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if !selected.ManuallySelected || selected.ProviderPath != "/alternate.jpg" || selected.Tag == poster.Tag {
		t.Fatalf("selected poster = %+v", selected)
	}

	if err := service.Match(ctx, itemID, 329865); err != nil {
		t.Fatal(err)
	}
	preserved, err := catalog.ItemImage(ctx, itemID, "poster")
	if err != nil {
		t.Fatal(err)
	}
	if preserved.ProviderPath != "/alternate.jpg" || !preserved.ManuallySelected {
		t.Fatalf("manual poster was not preserved: %+v", preserved)
	}
	preservedLogo, err := catalog.ItemImage(ctx, itemID, "logo")
	if err != nil {
		t.Fatal(err)
	}
	if preservedLogo.ProviderPath != "/alternate-logo.png" || !preservedLogo.ManuallySelected {
		t.Fatalf("manual logo was not preserved: %+v", preservedLogo)
	}

	reset, err := service.ResetImage(ctx, itemID, "poster")
	if err != nil {
		t.Fatal(err)
	}
	if reset.ManuallySelected || reset.ProviderPath != "/poster.jpg" || reset.Tag != poster.Tag {
		t.Fatalf("reset poster = %+v", reset)
	}
	resetLogo, err := service.ResetImage(ctx, itemID, "logo")
	if err != nil {
		t.Fatal(err)
	}
	if resetLogo.ManuallySelected || resetLogo.ProviderPath != "/logo.png" || resetLogo.Tag != logo.Tag {
		t.Fatalf("reset logo = %+v", resetLogo)
	}

	if _, err := service.SelectImage(ctx, itemID, "poster", "tmdb", "/alternate.jpg"); err != nil {
		t.Fatal(err)
	}
	if err := service.Match(ctx, itemID, 22); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.ItemImage(ctx, itemID, "poster"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("poster survived identity change: %v", err)
	}
	if _, err := catalog.ItemImage(ctx, itemID, "logo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("logo survived identity change: %v", err)
	}
	if _, err := catalog.ItemImage(ctx, itemID, "thumb"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("thumb survived identity change: %v", err)
	}
	rematched, err := catalog.Item(ctx, itemID)
	if err != nil {
		t.Fatal(err)
	}
	if !rematched.GenresLoaded || len(rematched.Genres) != 0 {
		t.Fatalf("genres survived identity change: %+v", rematched.Genres)
	}
}

func TestTVSeasonPosterArtwork(t *testing.T) {
	defaultImage := encodedPNG(t, color.RGBA{R: 255, A: 255})
	alternateImage := encodedPNG(t, color.RGBA{B: 255, A: 255})
	mux := http.NewServeMux()
	mux.HandleFunc("/tv/100", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":100,"name":"Show"}`)
	})
	mux.HandleFunc("/tv/200", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":200,"name":"Other Show"}`)
	})
	for _, id := range []string{"100", "200"} {
		mux.HandleFunc("/tv/"+id+"/images", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"posters":[],"backdrops":[],"logos":[]}`)
		})
	}
	mux.HandleFunc("/tv/100/season/1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":1001,"poster_path":"/season.jpg","episodes":[{"id":101,"episode_number":1,"name":"Pilot","overview":"First episode","air_date":"2020-01-01"}]}`)
	})
	mux.HandleFunc("/tv/200/season/1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":2001,"poster_path":"/other-season.jpg","episodes":[{"id":201,"episode_number":1,"name":"New Pilot"}]}`)
	})
	mux.HandleFunc("/tv/100/season/1/images", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"posters":[{"file_path":"/season.jpg","width":2,"height":3},{"file_path":"/alternate-season.jpg","width":2,"height":3}]}`)
	})
	mux.HandleFunc("/tv/200/season/1/images", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"posters":[{"file_path":"/other-season.jpg","width":2,"height":3}]}`)
	})
	mux.HandleFunc("/images/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/alternate-season.jpg") {
			_, _ = w.Write(alternateImage)
			return
		}
		_, _ = w.Write(defaultImage)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx := context.Background()
	root := t.TempDir()
	catalog, err := store.Open(filepath.Join(root, "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	libraryID, scanID, err := catalog.StartScan(ctx, "tv", "/tv")
	if err != nil {
		t.Fatal(err)
	}
	showID, err := catalog.UpsertItem(ctx, store.ItemInput{
		LibraryID: libraryID, SourceKey: "show:Show", Kind: "show", Title: "Show", ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	seasonID, err := catalog.UpsertItem(ctx, store.ItemInput{
		LibraryID: libraryID, ParentID: &showID, SourceKey: "show:Show:season:1",
		Kind: "season", Title: "Season 1", SeasonNumber: 1, ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	episodeID, err := catalog.UpsertItem(ctx, store.ItemInput{
		LibraryID: libraryID, ParentID: &seasonID, SourceKey: "file:Show/S01E01.mkv",
		Kind: "episode", Title: "Episode 1", SeasonNumber: 1, EpisodeNumber: 1, ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	client := tmdb.NewWithURLs("key", "en-US", server.URL, server.URL+"/images", server.Client())
	service := New(catalog, client, filepath.Join(root, "images"), slog.Default())

	if err := service.Match(ctx, showID, 100); err != nil {
		t.Fatal(err)
	}
	poster, err := catalog.ItemImage(ctx, seasonID, "poster")
	if err != nil {
		t.Fatal(err)
	}
	if poster.ProviderPath != "/season.jpg" || poster.ManuallySelected {
		t.Fatalf("default season poster = %+v", poster)
	}
	episode, err := catalog.Item(ctx, episodeID)
	if err != nil {
		t.Fatal(err)
	}
	if episode.Title != "Pilot" || episode.PosterImageID != poster.ID {
		t.Fatalf("episode metadata and inherited poster = %+v", episode)
	}
	options, err := service.ImageOptions(ctx, seasonID, "poster")
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 2 || !options[0].Selected || !strings.Contains(options[0].ThumbnailURL, "/w342/") {
		t.Fatalf("season poster options = %+v", options)
	}
	selected, err := service.SelectImage(ctx, seasonID, "poster", "tmdb", "/alternate-season.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if !selected.ManuallySelected || selected.ProviderPath != "/alternate-season.jpg" {
		t.Fatalf("selected season poster = %+v", selected)
	}
	if err := service.Match(ctx, showID, 100); err != nil {
		t.Fatal(err)
	}
	preserved, err := catalog.ItemImage(ctx, seasonID, "poster")
	if err != nil {
		t.Fatal(err)
	}
	if !preserved.ManuallySelected || preserved.ProviderPath != "/alternate-season.jpg" {
		t.Fatalf("manual season poster was not preserved: %+v", preserved)
	}
	reset, err := service.ResetImage(ctx, seasonID, "poster")
	if err != nil {
		t.Fatal(err)
	}
	if reset.ManuallySelected || reset.ProviderPath != "/season.jpg" {
		t.Fatalf("reset season poster = %+v", reset)
	}

	if _, err := service.SelectImage(ctx, seasonID, "poster", "tmdb", "/alternate-season.jpg"); err != nil {
		t.Fatal(err)
	}
	if err := service.Match(ctx, showID, 200); err != nil {
		t.Fatal(err)
	}
	replaced, err := catalog.ItemImage(ctx, seasonID, "poster")
	if err != nil {
		t.Fatal(err)
	}
	if replaced.ManuallySelected || replaced.ProviderPath != "/other-season.jpg" {
		t.Fatalf("season poster survived show identity change: %+v", replaced)
	}

	if err := catalog.DeleteItemImage(ctx, seasonID, "poster"); err != nil {
		t.Fatal(err)
	}
	if err := service.AutoMatch(ctx, showID); err != nil {
		t.Fatal(err)
	}
	backfilled, err := catalog.ItemImage(ctx, seasonID, "poster")
	if err != nil {
		t.Fatal(err)
	}
	if backfilled.ProviderPath != "/other-season.jpg" {
		t.Fatalf("backfilled season poster = %+v", backfilled)
	}
}

func TestMissingTMDBSeasonHasNoPosterArtwork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"status_code":34,"status_message":"The resource you requested could not be found."}`, http.StatusNotFound)
	}))
	defer server.Close()

	ctx := context.Background()
	root := t.TempDir()
	catalog, err := store.Open(filepath.Join(root, "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	libraryID, scanID, err := catalog.StartScan(ctx, "tv", "/tv")
	if err != nil {
		t.Fatal(err)
	}
	showID, err := catalog.UpsertItem(ctx, store.ItemInput{
		LibraryID: libraryID, SourceKey: "show:Show", Kind: "show", Title: "Show", ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	seasonID, err := catalog.UpsertItem(ctx, store.ItemInput{
		LibraryID: libraryID, ParentID: &showID, SourceKey: "show:Show:season:9",
		Kind: "season", Title: "Season 9", SeasonNumber: 9, ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.UpdateMetadata(ctx, showID, store.MetadataUpdate{TMDBID: 100, Title: "Show"}); err != nil {
		t.Fatal(err)
	}
	client := tmdb.NewWithURLs("key", "en-US", server.URL, server.URL+"/images", server.Client())
	service := New(catalog, client, filepath.Join(root, "images"), slog.Default())

	options, err := service.ImageOptions(ctx, seasonID, "poster")
	if err != nil {
		t.Fatal(err)
	}
	if options == nil || len(options) != 0 {
		t.Fatalf("season poster options = %+v, want an empty list", options)
	}
	if _, err := service.ResetImage(ctx, seasonID, "poster"); !errors.Is(err, ErrImageUnavailable) {
		t.Fatalf("reset missing season poster error = %v, want ErrImageUnavailable", err)
	}
}

func TestAutoMatchBackfillsMissingArtwork(t *testing.T) {
	imageBytes := encodedPNG(t, color.RGBA{R: 255, G: 255, A: 255})
	var searchRequests, detailRequests, imageRequests atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/search/movie", func(w http.ResponseWriter, _ *http.Request) {
		searchRequests.Add(1)
		_, _ = fmt.Fprint(w, `{"results":[]}`)
	})
	mux.HandleFunc("/movie/10", func(w http.ResponseWriter, _ *http.Request) {
		detailRequests.Add(1)
		_, _ = fmt.Fprint(w, `{"id":10,"title":"Movie","poster_path":"/poster.jpg","backdrop_path":"/backdrop.jpg","genres":[{"id":53,"name":"Thriller"}]}`)
	})
	mux.HandleFunc("/movie/10/images", func(w http.ResponseWriter, _ *http.Request) {
		imageRequests.Add(1)
		_, _ = fmt.Fprint(w, `{"posters":[],"backdrops":[{"file_path":"/clean.jpg"},{"file_path":"/titled.jpg","iso_639_1":"en"}],"logos":[{"file_path":"/logo.png"}]}`)
	})
	mux.HandleFunc("/images/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(imageBytes)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx := context.Background()
	root := t.TempDir()
	catalog, err := store.Open(filepath.Join(root, "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	libraryID, scanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := catalog.UpsertItem(ctx, store.ItemInput{
		LibraryID: libraryID, SourceKey: "Movie", Kind: "movie", Title: "Movie", ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.UpdateMetadata(ctx, itemID, store.MetadataUpdate{TMDBID: 10, Title: "Movie"}); err != nil {
		t.Fatal(err)
	}
	client := tmdb.NewWithURLs("key", "en-US", server.URL, server.URL+"/images", server.Client())
	service := New(catalog, client, filepath.Join(root, "images"), slog.Default())

	if err := service.AutoMatch(ctx, itemID); err != nil {
		t.Fatal(err)
	}
	item, err := catalog.Item(ctx, itemID)
	if err != nil {
		t.Fatal(err)
	}
	if item.PosterImageID == 0 || item.BackdropImageID == 0 || item.LogoImageID == 0 ||
		item.ThumbImageID == 0 || !item.GenresLoaded || len(item.Genres) != 1 ||
		item.Genres[0].ID != 53 {
		t.Fatalf("metadata was not backfilled: %+v", item)
	}
	if searchRequests.Load() != 0 || detailRequests.Load() != 1 || imageRequests.Load() != 1 {
		t.Fatalf("provider requests = search %d, details %d, images %d", searchRequests.Load(),
			detailRequests.Load(), imageRequests.Load())
	}

	if err := service.AutoMatch(ctx, itemID); err != nil {
		t.Fatal(err)
	}
	if detailRequests.Load() != 1 || imageRequests.Load() != 1 {
		t.Fatalf("complete artwork was fetched again: details %d, images %d",
			detailRequests.Load(), imageRequests.Load())
	}
}

func TestAutoMatchBackfillsEpisodesAddedAfterTheShowWasMatched(t *testing.T) {
	var seasonRequests [3]atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/tv/100/season/1", func(w http.ResponseWriter, _ *http.Request) {
		seasonRequests[1].Add(1)
		_, _ = fmt.Fprint(w, `{"season_number":1,"poster_path":"/season.jpg","episodes":[{"id":501,"episode_number":1,"name":"Pilot"},{"id":502,"episode_number":2,"name":"Second Contact","overview":"They meet again."}]}`)
	})
	mux.HandleFunc("/tv/100/season/2", func(w http.ResponseWriter, _ *http.Request) {
		seasonRequests[2].Add(1)
		_, _ = fmt.Fprint(w, `{"season_number":2,"poster_path":"/season-two.jpg","episodes":[{"id":511,"episode_number":1,"name":"Return"}]}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx := context.Background()
	root := t.TempDir()
	catalog, err := store.Open(filepath.Join(root, "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	libraryID, scanID, err := catalog.StartScan(ctx, "tv", "/tv")
	if err != nil {
		t.Fatal(err)
	}
	showID, err := catalog.UpsertItem(ctx, store.ItemInput{
		LibraryID: libraryID, SourceKey: "show:Show", Kind: "show", Title: "Show", ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.UpdateMetadata(ctx, showID, store.MetadataUpdate{TMDBID: 100, Title: "Show"}); err != nil {
		t.Fatal(err)
	}
	// Existing artwork keeps the backfill from making its own provider requests,
	// so the season requests below can only come from the episode backfill.
	for _, kind := range []string{"poster", "backdrop", "logo", "thumb"} {
		placeholderImage(ctx, t, catalog, showID, kind)
	}
	episodeIDs := make(map[string]int64)
	for _, seasonNumber := range []int{1, 2} {
		seasonID, err := catalog.UpsertItem(ctx, store.ItemInput{
			LibraryID: libraryID, ParentID: &showID,
			SourceKey:    fmt.Sprintf("show:Show:season:%d", seasonNumber),
			Kind:         "season",
			Title:        fmt.Sprintf("Season %d", seasonNumber),
			SeasonNumber: seasonNumber, ScanID: scanID,
		})
		if err != nil {
			t.Fatal(err)
		}
		placeholderImage(ctx, t, catalog, seasonID, "poster")
		episodeID, err := catalog.UpsertItem(ctx, store.ItemInput{
			LibraryID: libraryID, ParentID: &seasonID,
			SourceKey:    fmt.Sprintf("show:Show:season:%d:episode:1-1", seasonNumber),
			Kind:         "episode", Title: "Episode 1",
			SeasonNumber: seasonNumber, EpisodeNumber: 1, EpisodeEndNumber: 1, ScanID: scanID,
		})
		if err != nil {
			t.Fatal(err)
		}
		episodeIDs[fmt.Sprintf("s%de1", seasonNumber)] = episodeID
		if seasonNumber == 1 {
			// The show was matched while only this episode existed.
			if err := catalog.UpdateMetadata(ctx, episodeID, store.MetadataUpdate{
				TMDBID: 501, Title: "Pilot",
			}); err != nil {
				t.Fatal(err)
			}
			newEpisodeID, err := catalog.UpsertItem(ctx, store.ItemInput{
				LibraryID: libraryID, ParentID: &seasonID,
				SourceKey: "show:Show:season:1:episode:2-2", Kind: "episode", Title: "Episode 2",
				SeasonNumber: 1, EpisodeNumber: 2, EpisodeEndNumber: 2, ScanID: scanID,
			})
			if err != nil {
				t.Fatal(err)
			}
			episodeIDs["s1e2"] = newEpisodeID
			continue
		}
		if err := catalog.UpdateMetadata(ctx, episodeID, store.MetadataUpdate{
			TMDBID: 511, Title: "Return",
		}); err != nil {
			t.Fatal(err)
		}
	}

	client := tmdb.NewWithURLs("key", "en-US", server.URL, server.URL+"/images", server.Client())
	service := New(catalog, client, filepath.Join(root, "images"), slog.Default())
	if err := service.AutoMatch(ctx, showID); err != nil {
		t.Fatal(err)
	}
	added, err := catalog.Item(ctx, episodeIDs["s1e2"])
	if err != nil {
		t.Fatal(err)
	}
	if added.TMDBID != 502 || added.Title != "Second Contact" || added.Overview != "They meet again." {
		t.Fatalf("episode added after the match was not backfilled: %+v", added)
	}
	if seasonRequests[1].Load() != 1 || seasonRequests[2].Load() != 0 {
		t.Fatalf("season requests = season 1 %d, season 2 %d; only seasons missing an episode match should be fetched",
			seasonRequests[1].Load(), seasonRequests[2].Load())
	}

	if err := service.AutoMatch(ctx, showID); err != nil {
		t.Fatal(err)
	}
	if seasonRequests[1].Load() != 1 {
		t.Fatalf("a fully matched show fetched season 1 again: %d requests", seasonRequests[1].Load())
	}
}

func placeholderImage(ctx context.Context, t *testing.T, catalog *store.Store, itemID int64, kind string) {
	t.Helper()
	if _, err := catalog.UpsertImage(ctx, store.Image{
		ItemID: itemID, Kind: kind, Path: filepath.Join(t.TempDir(), kind+".jpg"),
		Provider: "tmdb", ProviderPath: "/" + kind + ".jpg", Tag: kind,
		ContentType: "image/jpeg", Width: 1, Height: 1,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSelectAutomaticMatch(t *testing.T) {
	tests := []struct {
		name       string
		matches    []tmdb.SearchResult
		wantID     int64
		wantReason string
	}{
		{
			name:       "no exact candidates",
			wantReason: "no exact title and year candidates",
		},
		{
			name:    "unique candidate retains existing behavior",
			matches: []tmdb.SearchResult{{ID: 1}},
			wantID:  1,
		},
		{
			name: "dominant candidate wins exact collision",
			matches: []tmdb.SearchResult{
				{ID: 2, VoteAverage: 7.0, VoteCount: 2},
				{ID: 1, VoteAverage: 6.1, VoteCount: 274},
			},
			wantID: 1,
		},
		{
			name: "close candidates remain unmatched",
			matches: []tmdb.SearchResult{
				{ID: 1, VoteAverage: 7.5, VoteCount: 100},
				{ID: 2, VoteAverage: 7.0, VoteCount: 11},
			},
			wantReason: "leading candidate does not clearly dominate the alternatives",
		},
		{
			name: "weak leader remains unmatched",
			matches: []tmdb.SearchResult{
				{ID: 1, VoteAverage: 8.0, VoteCount: 4},
				{ID: 2, VoteAverage: 0, VoteCount: 0},
			},
			wantReason: "leading candidate is below the vote threshold",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, reason := selectAutomaticMatch(test.matches)
			if test.wantID == 0 {
				if got != nil || reason != test.wantReason {
					t.Fatalf("selectAutomaticMatch() = %+v, %q; want nil, %q", got, reason, test.wantReason)
				}
				return
			}
			if got == nil || got.ID != test.wantID || reason != "" {
				t.Fatalf("selectAutomaticMatch() = %+v, %q; want ID %d", got, reason, test.wantID)
			}
		})
	}
}

func TestCombinedEpisodeMetadata(t *testing.T) {
	item := store.Item{EpisodeNumber: 1, EpisodeEndNumber: 2}
	got, ok := combinedEpisodeMetadata(item, map[int]tmdb.Episode{
		1: {ID: 10, Number: 1, Title: "Part One", Overview: "First", ReleaseDate: "2020-01-01"},
		2: {ID: 11, Number: 2, Title: "Part Two", Overview: "Second", ReleaseDate: "2020-01-08"},
	})
	if !ok || got.TMDBID != 10 || got.Title != "Part One / Part Two" || got.Overview != "First\n\nSecond" {
		t.Fatalf("combined metadata = %+v, %v", got, ok)
	}
}

func encodedPNG(t *testing.T, fill color.Color) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 2, 3))
	for y := range 3 {
		for x := range 2 {
			picture.Set(x, y, fill)
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, picture); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
