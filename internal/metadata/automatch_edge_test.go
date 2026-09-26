package metadata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/five82/loom/internal/store"
	"github.com/five82/loom/internal/tmdb"
)

func testMovieService(t *testing.T, handler http.Handler) (*Service, *store.Store, int64) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	root := t.TempDir()
	catalog, err := store.Open(filepath.Join(root, "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	ctx := context.Background()
	libraryID, scanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	id, err := catalog.UpsertItem(ctx, store.ItemInput{LibraryID: libraryID, SourceKey: "Movie", Kind: "movie", Title: "Movie", Year: 2000, ScanID: scanID})
	if err != nil {
		t.Fatal(err)
	}
	client := tmdb.NewWithURLs("key", "en-US", server.URL, server.URL+"/images", server.Client())
	return New(catalog, client, filepath.Join(root, "images"), slog.Default()), catalog, id
}

func TestAutoMatchRejectsUncertainSearchResults(t *testing.T) {
	for _, tc := range []struct{ name, results string }{
		{"different title", `[{"id":1,"title":"Another","release_date":"2000-01-01","vote_count":100}]`},
		{"wrong year", `[{"id":1,"title":"Movie","release_date":"2001-01-01","vote_count":100}]`},
		{"too few votes", `[{"id":1,"title":"Movie","release_date":"2000-01-01","vote_count":1},{"id":2,"title":"Movie","release_date":"2000-01-01","vote_count":0}]`},
		{"ambiguous", `[{"id":1,"title":"Movie","release_date":"2000-01-01","vote_count":100,"vote_average":8},{"id":2,"title":"Movie","release_date":"2000-01-01","vote_count":100,"vote_average":8}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, catalog, id := testMovieService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/search/movie" {
					t.Errorf("unexpected request %s", r.URL.Path)
				}
				_, _ = fmt.Fprintf(w, `{"results":%s}`, tc.results)
			}))
			if err := service.AutoMatch(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			item, err := catalog.Item(context.Background(), id)
			if err != nil || item.TMDBID != 0 {
				t.Fatalf("unexpected match: %+v, %v", item, err)
			}
		})
	}
}

func TestAutoMatchProviderAndCatalogErrors(t *testing.T) {
	service, catalog, id := testMovieService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	if err := service.AutoMatch(context.Background(), id); err == nil {
		t.Fatal("search outage ignored")
	}
	if err := service.AutoMatch(context.Background(), id+100); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing item: %v", err)
	}
	if err := catalog.UpdateMetadata(context.Background(), id, store.MetadataUpdate{TMDBID: 7}); err != nil {
		t.Fatal(err)
	}
	if err := service.AutoMatch(context.Background(), id); err == nil {
		t.Fatal("details outage ignored")
	}
}

func TestImageSelectionErrorPaths(t *testing.T) {
	service, catalog, id := testMovieService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/images/"):
			http.Error(w, "not found", http.StatusNotFound)
		default:
			_, _ = fmt.Fprint(w, `{"posters":[],"backdrops":[],"logos":[]}`)
		}
	}))
	ctx := context.Background()
	if _, err := service.SelectImage(ctx, id, "poster", "other", "/poster"); !errors.Is(err, ErrImageOptionNotFound) {
		t.Fatalf("bad provider: %v", err)
	}
	if _, err := service.ImageOptions(ctx, id, "unsupported"); !errors.Is(err, ErrImageUnavailable) {
		t.Fatalf("bad kind: %v", err)
	}
	if _, err := service.ImageOptions(ctx, id, "poster"); !errors.Is(err, ErrImageUnavailable) {
		t.Fatalf("unmatched item: %v", err)
	}
	if err := catalog.UpdateMetadata(ctx, id, store.MetadataUpdate{TMDBID: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SelectImage(ctx, id, "poster", "tmdb", "/poster"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("image download: %v", err)
	}
	for _, kind := range []string{"poster", "backdrop", "thumb", "logo"} {
		if _, err := service.ResetImage(ctx, id, kind); !errors.Is(err, ErrImageUnavailable) {
			t.Fatalf("missing %s: %v", kind, err)
		}
	}
	if _, err := service.ImageOptions(ctx, id, "poster"); err != nil {
		t.Fatalf("empty options: %v", err)
	}
}
