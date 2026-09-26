package httpapi

import (
	"log/slog"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/five82/loom/internal/channels"
	"github.com/five82/loom/internal/library"
)

func TestCatalogFailuresReturnServerErrors(t *testing.T) {
	catalog, itemID, mediaID, _ := testCatalog(t)
	api := New(catalog, library.NewManager(nil, 0, slog.Default()), nil, channels.New(catalog), make(chan struct{}, 1), ListenAddresses{})
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(itemID, 10)
	media := strconv.FormatInt(mediaID, 10)
	for _, tc := range []struct {
		method, path, body string
		local              bool
	}{
		{"GET", "/api/v1/libraries", "", false},
		{"GET", "/api/v1/home", "", false},
		{"GET", "/api/v1/channels", "", false},
		{"GET", "/api/v1/genres", "", false},
		{"GET", "/api/v1/collections", "", false},
		{"GET", "/api/v1/featured-pick", "", false},
		{"GET", "/api/v1/search?q=Movie", "", false},
		{"GET", "/api/v1/items", "", false},
		{"GET", "/api/v1/items/" + id, "", false},
		{"GET", "/api/v1/items/" + id + "/children", "", false},
		{"GET", "/api/v1/items/" + id + "/playback", "", false},
		{"GET", "/api/v1/media/" + media, "", false},
		{"GET", "/api/v1/images/1", "", false},
		{"POST", "/api/v1/items/" + id + "/played", "", false},
		{"PUT", "/api/v1/items/" + id + "/progress", `{"position_ms":1}`, false},
		{"GET", "/api/v1/continue-watching", "", false},
		{"GET", "/api/v1/next-up", "", false},
		{"GET", "/api/v1/recently-added", "", false},
		{"GET", "/api/v1/recently-played", "", false},
		{"GET", "/_loom/status", "", true},
		{"GET", "/_loom/unmatched", "", true},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			handler := api.PublicHandler()
			if tc.local {
				handler = api.LocalHandler()
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			// Progress rejects storage failures as bad requests; the other endpoints
			// classify catalog failures as server errors.
			want := 500
			if strings.HasSuffix(tc.path, "/progress") {
				want = 400
			}
			if response.Code != want {
				t.Fatalf("got %d %s; want %d", response.Code, response.Body.String(), want)
			}
		})
	}
}
