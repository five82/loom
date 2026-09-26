package httpapi

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/five82/loom/internal/channels"
	"github.com/five82/loom/internal/library"
	"github.com/five82/loom/internal/store"
)

func TestMissingMediaAndArtwork(t *testing.T) {
	catalog, itemID, mediaID, _ := testCatalog(t)
	defer func() { _ = catalog.Close() }()
	media, err := catalog.Media(context.Background(), mediaID)
	if err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(t.TempDir(), "poster.jpg")
	imageID, err := catalog.UpsertImage(context.Background(), store.Image{ItemID: itemID, Kind: "poster", Path: imagePath, Provider: "tmdb", ProviderPath: "/poster.jpg", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	api := New(catalog, library.NewManager(nil, 0, slog.Default()), nil, channels.New(catalog), make(chan struct{}, 1), ListenAddresses{})
	if err := os.Remove(media.Path); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(itemID, 10)
	fileID := strconv.FormatInt(mediaID, 10)
	artID := strconv.FormatInt(imageID, 10)
	for _, tc := range []struct{ path, want string }{
		{"/api/v1/items/" + id + "/playback", "media file is unavailable"},
		{"/api/v1/media/" + fileID, "media file is unavailable"},
		{"/api/v1/images/" + artID, "image file is unavailable"},
		{"/api/v1/images/" + artID + "?width=0", "width must be"},
		{"/api/v1/images/" + artID + "?width=x", "width must be"},
		{"/api/v1/images/" + artID + "?width=200", "image file is unavailable"},
	} {
		response := httptest.NewRecorder()
		api.PublicHandler().ServeHTTP(response, httptest.NewRequest("GET", tc.path, nil))
		if !strings.Contains(response.Body.String(), tc.want) {
			t.Errorf("%s: %d %s", tc.path, response.Code, response.Body.String())
		}
	}
}

func TestMediaContentTypes(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"movie.MKV", "video/x-matroska"}, {"movie.avi", "video/x-msvideo"},
		{"movie.webm", "video/webm"}, {"movie.mpg", "video/mpeg"},
		{"movie.mpeg", "video/mpeg"}, {"movie.mp4", "video/mp4"},
	} {
		if got := mediaContentType(tc.path); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.path, got, tc.want)
		}
	}
}
