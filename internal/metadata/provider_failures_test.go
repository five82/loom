package metadata

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/five82/loom/internal/store"
)

func TestMatchedMovieBackfillFailsWhenProviderIsDown(t *testing.T) {
	for _, detailsLoaded := range []bool{false, true} {
		t.Run(fmt.Sprint(detailsLoaded), func(t *testing.T) {
			service, catalog, id := testMovieService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "provider down", http.StatusServiceUnavailable)
			}))
			ctx := context.Background()
			if detailsLoaded {
				if err := catalog.UpdateMetadata(ctx, id, store.MetadataUpdate{TMDBID: 7}); err != nil {
					t.Fatal(err)
				}
			} else {
				// A directory name may supply the TMDB id before details are loaded.
				libraries, err := catalog.Libraries(ctx)
				if err != nil {
					t.Fatal(err)
				}
				_, scanID, err := catalog.StartScan(ctx, "movies", "/movies")
				if err != nil {
					t.Fatal(err)
				}
				sameID, err := catalog.UpsertItem(ctx, store.ItemInput{LibraryID: libraries[0].ID, SourceKey: "Movie", Kind: "movie", Title: "Movie", TMDBID: 7, ScanID: scanID})
				if err != nil || sameID != id {
					t.Fatalf("seeded identity = %d, %v", sameID, err)
				}
			}
			err := service.AutoMatch(ctx, id)
			if err == nil || !strings.Contains(err.Error(), "503") {
				t.Fatalf("provider error = %v", err)
			}
		})
	}
}

func TestImageOptionsAndResetReportProviderFailures(t *testing.T) {
	for _, tc := range []struct {
		name, suffix string
		status       int
		action       string
	}{
		{"options", "/movie/7/images", 503, "options"},
		{"poster reset", "/movie/7", 503, "poster"},
		{"backdrop reset", "/movie/7/images", 503, "backdrop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, catalog, id := testMovieService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tc.suffix {
					http.Error(w, "down", tc.status)
					return
				}
				_, _ = fmt.Fprint(w, `{"posters":[],"backdrops":[],"logos":[]}`)
			}))
			ctx := context.Background()
			if err := catalog.UpdateMetadata(ctx, id, store.MetadataUpdate{TMDBID: 7}); err != nil {
				t.Fatal(err)
			}
			var err error
			if tc.action == "options" {
				_, err = service.ImageOptions(ctx, id, "poster")
			} else {
				_, err = service.ResetImage(ctx, id, tc.action)
			}
			if err == nil || !strings.Contains(err.Error(), "503") {
				t.Fatalf("%s error = %v", tc.name, err)
			}
			if _, err := catalog.ItemImage(ctx, id, "poster"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("image saved: %v", err)
			}
		})
	}
}
