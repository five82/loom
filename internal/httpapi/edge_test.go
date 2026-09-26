package httpapi

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/five82/loom/internal/channels"
	"github.com/five82/loom/internal/library"
	"github.com/five82/loom/internal/metadata"
	"github.com/five82/loom/internal/store"
	"github.com/five82/loom/internal/tmdb"
)

func TestMetadataControlValidationAndProviderFailures(t *testing.T) {
	catalog, itemID, _, _ := testCatalog(t)
	defer func() { _ = catalog.Close() }()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "search") {
			http.Error(w, "provider down", http.StatusBadGateway)
			return
		}
		_, _ = fmt.Fprint(w, `{"id":42,"title":"Matched"}`)
	}))
	defer provider.Close()
	client := tmdb.NewWithURLs("key", "en-US", provider.URL, provider.URL+"/images", provider.Client())
	service := metadata.New(catalog, client, filepath.Join(t.TempDir(), "images"), slog.Default())
	api := New(catalog, library.NewManager(nil, 0, slog.Default()), service, channels.New(catalog), make(chan struct{}, 1), ListenAddresses{})
	for _, tc := range []struct {
		method, path, body string
		status             int
		want               string
	}{
		{"GET", "/_loom/metadata/search?type=bad&query=Movie", "", 400, "type must be"},
		{"GET", "/_loom/metadata/search?type=movie&query=+", "", 400, "query must not be empty"},
		{"GET", "/_loom/metadata/search?type=movie&query=Movie&year=no", "", 400, "year must be"},
		{"GET", "/_loom/metadata/search?type=movie&query=Movie&year=1799", "", 400, "year must be"},
		{"GET", "/_loom/metadata/search?type=movie&query=Movie", "", 502, "provider"},
		{"POST", "/_loom/metadata/match", `{"bad":1}`, 400, "positive integers"},
		{"POST", "/_loom/metadata/match", `{"item_id":0,"tmdb_id":42}`, 400, "positive integers"},
		{"POST", "/_loom/metadata/match", `{"item_id":999999,"tmdb_id":42}`, 404, "item not found"},
		{"POST", "/_loom/metadata/match", fmt.Sprintf(`{"item_id":%d,"tmdb_id":42}`, itemID), 200, "matched"},
	} {
		t.Run(tc.method+" "+tc.path+" "+tc.body, func(t *testing.T) {
			response := httptest.NewRecorder()
			api.LocalHandler().ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.want) {
				t.Fatalf("got %d %s; want %d %s", response.Code, response.Body.String(), tc.status, tc.want)
			}
		})
	}
}

func TestImageErrorMapping(t *testing.T) {
	api := &API{}
	for _, tc := range []struct {
		err    error
		status int
	}{
		{store.ErrNotFound, 404}, {metadata.ErrImageOptionNotFound, 400}, {metadata.ErrImageUnavailable, 409}, {errors.New("provider down"), 502},
	} {
		response := httptest.NewRecorder()
		api.writeImageError(response, tc.err)
		if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.err.Error()) && tc.err != store.ErrNotFound {
			t.Errorf("%v: %d %s", tc.err, response.Code, response.Body.String())
		}
	}
}

func TestCatalogEndpointMissingAndMalformedRequests(t *testing.T) {
	catalog, itemID, mediaID, _ := testCatalog(t)
	defer func() { _ = catalog.Close() }()
	api := New(catalog, library.NewManager(nil, 0, slog.Default()), nil, channels.New(catalog), make(chan struct{}, 1), ListenAddresses{})
	id := strconv.FormatInt(itemID, 10)
	media := strconv.FormatInt(mediaID, 10)
	for _, tc := range []struct {
		method, path, body string
		status             int
		want               string
	}{
		{"GET", "/api/v1/items/bad", "", 400, "positive integer"},
		{"GET", "/api/v1/items/999999", "", 404, "item not found"},
		{"GET", "/api/v1/items/999999/playback", "", 404, "item not found"},
		{"GET", "/api/v1/media/999999", "", 404, "media not found"},
		{"GET", "/api/v1/images/999999", "", 404, "image not found"},
		{"GET", "/api/v1/items?library=music", "", 400, "library"},
		{"GET", "/api/v1/items?genre_id=bad", "", 400, "genre_id"},
		{"GET", "/api/v1/items?parent_id=-1", "", 400, "parent_id"},
		{"GET", "/api/v1/items?parent_id=" + id, "", 200, "items"},
		{"GET", "/api/v1/search?q=Movie&offset=-1", "", 400, "offset"},
		{"GET", "/api/v1/media/" + media, "", 200, "0123456789"},
		{"PUT", "/api/v1/items/" + id + "/progress", `{"extra":1}`, 400, "invalid progress body"},
		{"PUT", "/api/v1/items/999999/progress", `{"position_ms":1}`, 404, "not found"},
		{"POST", "/api/v1/items/no/played", "", 400, "positive integer"},
		{"GET", "/api/v1/items/" + id + "/images/poster/options", "", 503, "disabled"},
		{"PUT", "/api/v1/items/" + id + "/images/poster", `{}`, 503, "disabled"},
		{"POST", "/api/v1/items/" + id + "/images/poster/reset", "", 503, "disabled"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			api.PublicHandler().ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.want) {
				t.Fatalf("got %d %s; want %d %s", response.Code, response.Body.String(), tc.status, tc.want)
			}
		})
	}
}
