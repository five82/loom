package metadata

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/five82/loom/internal/store"
)

func TestAutoMatchPropagatesArtworkListFailureAfterPosterIsPresent(t *testing.T) {
	service, catalog, id := testMovieService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/movie/7/images" {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		t.Errorf("unexpected provider request %s", r.URL.Path)
	}))
	ctx := context.Background()
	if err := catalog.UpdateMetadata(ctx, id, store.MetadataUpdate{TMDBID: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertImage(ctx, store.Image{ItemID: id, Kind: "poster", Path: "/existing.png"}); err != nil {
		t.Fatal(err)
	}
	if err := service.AutoMatch(ctx, id); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("artwork list failure = %v", err)
	}
}

func TestMatchReportsDetailAndCatalogFailures(t *testing.T) {
	var catalog *store.Store
	service, opened, id := testMovieService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/movie/5" {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/movie/6" {
			if err := catalog.Close(); err != nil {
				t.Error(err)
			}
			_, _ = fmt.Fprint(w, `{"id":6,"title":"Movie"}`)
			return
		}
	}))
	catalog = opened
	if err := service.Match(context.Background(), id, 5); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("details failure = %v", err)
	}
	if err := service.Match(context.Background(), id, 6); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("catalog closed mid-match = %v", err)
	}
}
