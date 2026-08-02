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
	"testing"

	"github.com/five82/loom/internal/store"
	"github.com/five82/loom/internal/tmdb"
)

func TestMovieMetadataImageSelectionAndReset(t *testing.T) {
	defaultImage := encodedPNG(t, color.RGBA{R: 255, A: 255})
	alternateImage := encodedPNG(t, color.RGBA{B: 255, A: 255})
	mux := http.NewServeMux()
	mux.HandleFunc("/search/movie", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"results":[{"id":329865,"title":"Arrival","release_date":"2016-11-10"}]}`)
	})
	mux.HandleFunc("/movie/329865", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":329865,"title":"Arrival","overview":"A linguist meets visitors.","release_date":"2016-11-10","poster_path":"/poster.jpg","backdrop_path":"/backdrop.jpg"}`)
	})
	mux.HandleFunc("/movie/22", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":22,"title":"Different Movie"}`)
	})
	mux.HandleFunc("/movie/329865/images", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"posters":[{"file_path":"/poster.jpg","width":2,"height":3},{"file_path":"/alternate.jpg","width":2,"height":3}],"backdrops":[]}`)
	})
	mux.HandleFunc("/images/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/alternate.jpg") {
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
	if item.TMDBID != 329865 || item.Overview == "" || item.PosterImageID == 0 || item.BackdropImageID == 0 || item.PosterImageTag == "" {
		t.Fatalf("metadata not applied: %+v", item)
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

	reset, err := service.ResetImage(ctx, itemID, "poster")
	if err != nil {
		t.Fatal(err)
	}
	if reset.ManuallySelected || reset.ProviderPath != "/poster.jpg" || reset.Tag != poster.Tag {
		t.Fatalf("reset poster = %+v", reset)
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
