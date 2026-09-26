package httpapi

import (
	"context"
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

func TestImageSelectionValidation(t *testing.T) {
	catalog, itemID, _, _ := testCatalog(t)
	defer func() { _ = catalog.Close() }()
	if err := catalog.UpdateMetadata(context.Background(), itemID, store.MetadataUpdate{TMDBID: 42, Title: "Movie"}); err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "provider unavailable", http.StatusBadGateway)
	}))
	defer provider.Close()
	client := tmdb.NewWithURLs("key", "en-US", provider.URL, provider.URL+"/images", provider.Client())
	service := metadata.New(catalog, client, filepath.Join(t.TempDir(), "images"), slog.Default())
	api := New(catalog, library.NewManager(nil, 0, slog.Default()), service, channels.New(catalog), make(chan struct{}, 1), ListenAddresses{})
	base := "/api/v1/items/" + strconv.FormatInt(itemID, 10) + "/images/"
	for _, tc := range []struct {
		method, path, body string
		status             int
		want               string
	}{
		{"GET", "/api/v1/items/invalid/images/logo/options", "", 400, "positive integer"},
		{"GET", base + "invalid/options", "", 400, "image kind"},
		{"PUT", base + "invalid", `{}`, 400, "image kind"},
		{"POST", base + "invalid/reset", "", 400, "image kind"},
		{"PUT", base + "logo", `{invalid}`, 400, "invalid image selection"},
		{"PUT", base + "logo", `{"unknown":1}`, 400, "invalid image selection"},
		{"GET", base + "logo/options", "", 502, "provider"},
		{"POST", base + "logo/reset", "", 502, "provider"},
		{"PUT", base + "logo", `{}`, 400, "image option not found"},
		{"GET", "/api/v1/items/999999/images/logo/options", "", 404, "item not found"},
	} {
		t.Run(tc.method+" "+tc.path+tc.body, func(t *testing.T) {
			response := httptest.NewRecorder()
			api.PublicHandler().ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.want) {
				t.Fatalf("got %d %s; want %d %s", response.Code, response.Body.String(), tc.status, tc.want)
			}
		})
	}
}
