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

func TestChildrenAndCuratedCollectionsReportMalformedItems(t *testing.T) {
	for _, tc := range []struct {
		name, kind, childKind, route string
		tmdb                         int64
	}{
		{"children", "show", "season", "/api/v1/items/%d/children", 0},
		{"curated collection", "movie", "", "/api/v1/collections", 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "loom.db")
			catalog, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = catalog.Close() }()
			kind := "tv"
			if tc.kind == "movie" {
				kind = "movies"
			}
			libraryID, scanID, err := catalog.StartScan(ctx, kind, "/"+kind)
			if err != nil {
				t.Fatal(err)
			}
			parentID, err := catalog.UpsertItem(ctx, store.ItemInput{LibraryID: libraryID, SourceKey: "parent", Kind: tc.kind, Title: "Parent", ScanID: scanID, TMDBID: tc.tmdb})
			if err != nil {
				t.Fatal(err)
			}
			corruptedID := parentID
			if tc.childKind != "" {
				corruptedID, err = catalog.UpsertItem(ctx, store.ItemInput{LibraryID: libraryID, ParentID: &parentID, SourceKey: "child", Kind: tc.childKind, Title: "Child", ScanID: scanID})
				if err != nil {
					t.Fatal(err)
				}
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE items SET year = 'invalid' WHERE id = ?`, corruptedID); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			api := New(catalog, library.NewManager(nil, 0, slog.Default()), nil, channels.New(catalog), make(chan struct{}, 1), ListenAddresses{})
			route := tc.route
			if tc.childKind != "" {
				route = fmt.Sprintf(route, parentID)
			}
			response := httptest.NewRecorder()
			api.PublicHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
			if response.Code != 500 || !strings.Contains(response.Body.String(), "year") {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}
