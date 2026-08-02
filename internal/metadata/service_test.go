package metadata

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/five82/loom/internal/store"
	"github.com/five82/loom/internal/tmdb"
)

func TestAutoMatchExactMovieAndSaveImages(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/search/movie", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"results":[{"id":329865,"title":"Arrival","release_date":"2016-11-10"}]}`)
	})
	mux.HandleFunc("/movie/329865", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":329865,"title":"Arrival","overview":"A linguist meets visitors.","release_date":"2016-11-10","poster_path":"/poster.jpg","backdrop_path":"/backdrop.jpg"}`)
	})
	mux.HandleFunc("/images/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("image"))
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
	if item.TMDBID != 329865 || item.Overview == "" || item.PosterImageID == 0 || item.BackdropImageID == 0 {
		t.Fatalf("metadata not applied: %+v", item)
	}
	image, err := catalog.Image(ctx, item.PosterImageID)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(image.Path); err != nil || string(data) != "image" {
		t.Fatalf("saved image = %q, %v", data, err)
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
