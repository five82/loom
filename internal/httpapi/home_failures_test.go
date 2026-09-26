package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/five82/loom/internal/channels"
	"github.com/five82/loom/internal/library"
	"github.com/five82/loom/internal/store"
)

func TestHomeReportsEachCatalogStageFailure(t *testing.T) {
	for _, tc := range []struct{ name, setup, want string }{
		{"featured", `DROP TABLE featured_pick`, "featured"},
		{"continue watching", `DROP TABLE playback_state`, "playback"},
		{"corrupt resumable movie", `UPDATE items SET year = 'invalid' WHERE kind = 'movie'`, "year"},
		{"next up", `UPDATE items SET year = 'invalid' WHERE kind = 'episode' AND episode_number = 2`, "year"},
		{"recently added", `UPDATE items SET year = 'invalid' WHERE kind = 'movie'`, "year"},
		{"discovery", `UPDATE items SET year = 'invalid' WHERE kind = 'show'`, "year"},
		{"credits", `DROP TABLE item_credits`, "credits"},
		{"collections", `DROP TABLE media_streams`, "media_streams"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "loom.db")
			catalog, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = catalog.Close() }()
			movieLibrary, scanID, err := catalog.StartScan(ctx, "movies", "/movies")
			if err != nil {
				t.Fatal(err)
			}
			movieID, err := catalog.UpsertItem(ctx, store.ItemInput{LibraryID: movieLibrary, SourceKey: "movie", Kind: "movie", Title: "Movie", ScanID: scanID})
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "corrupt resumable movie" {
				if _, err := catalog.UpsertMedia(ctx, store.MediaFile{ItemID: movieID, Path: "/movie.mkv", Size: 1, DurationMS: 600_000, LastSeenScanID: scanID}, nil, nil); err != nil {
					t.Fatal(err)
				}
				if _, err := catalog.SetProgress(ctx, movieID, 200_000, 600_000); err != nil {
					t.Fatal(err)
				}
			}
			tvLibrary, tvScanID, err := catalog.StartScan(ctx, "tv", "/tv")
			if err != nil {
				t.Fatal(err)
			}
			showID, err := catalog.UpsertItem(ctx, store.ItemInput{LibraryID: tvLibrary, SourceKey: "show", Kind: "show", Title: "Show", ScanID: tvScanID})
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "next up" {
				seasonID, err := catalog.UpsertItem(ctx, store.ItemInput{LibraryID: tvLibrary, ParentID: &showID, SourceKey: "season", Kind: "season", Title: "Season", SeasonNumber: 1, ScanID: tvScanID})
				if err != nil {
					t.Fatal(err)
				}
				for n := 1; n <= 2; n++ {
					episodeID, err := catalog.UpsertItem(ctx, store.ItemInput{LibraryID: tvLibrary, ParentID: &seasonID, SourceKey: fmt.Sprintf("episode%d", n), Kind: "episode", Title: "Episode", SeasonNumber: 1, EpisodeNumber: n, ScanID: tvScanID})
					if err != nil {
						t.Fatal(err)
					}
					if n == 1 {
						if _, err := catalog.UpsertMedia(ctx, store.MediaFile{ItemID: episodeID, Path: "/episode.mkv", Size: 1, DurationMS: 600_000, LastSeenScanID: tvScanID}, nil, nil); err != nil {
							t.Fatal(err)
						}
						if _, err := catalog.SetPlayed(ctx, episodeID); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tc.setup); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			api := New(catalog, library.NewManager(nil, 0, slog.Default()), nil, channels.New(catalog), make(chan struct{}, 1), ListenAddresses{})
			response := httptest.NewRecorder()
			api.PublicHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/home", nil))
			if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), tc.want) {
				t.Fatalf("response = %d %s, want %q", response.Code, response.Body.String(), tc.want)
			}
		})
	}
}
