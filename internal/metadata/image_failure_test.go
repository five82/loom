package metadata

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/five82/loom/internal/store"
)

func TestDownloadImageFailuresLeaveNoCatalogSelection(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		body, want   string
		badDirectory bool
	}{
		{name: "provider failure", status: 404, want: "image server returned"},
		{name: "invalid image", status: 200, body: "not an image", want: "validate downloaded image"},
		{name: "unwritable image directory", status: 200, badDirectory: true, want: "create image directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, catalog, id := testMovieService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			ctx := context.Background()
			if err := catalog.UpdateMetadata(ctx, id, store.MetadataUpdate{TMDBID: 1}); err != nil {
				t.Fatal(err)
			}
			if tc.badDirectory {
				if err := os.WriteFile(service.imageDir, []byte("file"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := service.SelectImage(ctx, id, "poster", "tmdb", "/image.png")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("select error = %v", err)
			}
			if _, err := catalog.ItemImage(ctx, id, "poster"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("image saved despite failure: %v", err)
			}
		})
	}
}

func TestDownloadImagePreservesManualSelectionUnlessForced(t *testing.T) {
 imageBytes := encodedPNG(t, color.RGBA{R: 255, A: 255})
 requests := 0
	service, catalog, id := testMovieService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write(imageBytes)
	}))
	ctx := context.Background()
	if err := catalog.UpdateMetadata(ctx, id, store.MetadataUpdate{TMDBID: 1}); err != nil {
		t.Fatal(err)
	}
	selected, err := service.SelectImage(ctx, id, "poster", "tmdb", "/manual.png")
	if err != nil {
		t.Fatal(err)
	}
	service.saveDefaultImage(ctx, id, "poster", "/automatic.png")
	stillSelected, err := catalog.ItemImage(ctx, id, "poster")
	if err != nil || !stillSelected.ManuallySelected || stillSelected.ProviderPath != "/manual.png" || requests != 1 {
		t.Fatalf("default overrode manual image: %+v, %v, requests=%d", stillSelected, err, requests)
	}
	if selected.Path != stillSelected.Path {
		t.Fatal("image path changed")
	}
	if _, err := os.Stat(filepath.Dir(selected.Path)); err != nil {
		t.Fatal(err)
	}
}
