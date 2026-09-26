package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestCatalogMediaAndImageQueries(t *testing.T) {
	ctx := context.Background()
	catalog, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	libraryID, scanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := catalog.UpsertItem(ctx, ItemInput{LibraryID: libraryID, SourceKey: "Example", Kind: "movie", Title: "Example", ScanID: scanID})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.TouchMedia(ctx, itemID, scanID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("touch without media = %v", err)
	}
	path := "/movies/Example.mkv"
	if _, err := catalog.MediaByPath(ctx, path); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing path = %v", err)
	}
	mediaID, err := catalog.UpsertMedia(ctx, MediaFile{ItemID: itemID, Path: path, Size: 100, MTimeNS: 123, DurationMS: 1000, LastSeenScanID: scanID}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, libraryID, scanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	media, err := catalog.MediaByPath(ctx, path)
	if err != nil || media.ID != mediaID || media.Tag == "" {
		t.Fatalf("media by path = %+v, %v", media, err)
	}
	media, err = catalog.Media(ctx, mediaID)
	if err != nil || media.Path != path {
		t.Fatalf("media by ID = %+v, %v", media, err)
	}
	nextLibraryID, nextScanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	if nextLibraryID != libraryID {
		t.Fatalf("library ID changed: %d", nextLibraryID)
	}
	if _, err := catalog.UpsertItem(ctx, ItemInput{LibraryID: libraryID, SourceKey: "Example", Kind: "movie", Title: "Example", ScanID: nextScanID}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.TouchMedia(ctx, itemID, nextScanID); err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, libraryID, nextScanID, 1, 0, 0, nil); err != nil {
		t.Fatal(err)
	}
	if media, err := catalog.MediaByPath(ctx, path); err != nil || media.LastSeenScanID != nextScanID {
		t.Fatalf("touched media = %+v, %v", media, err)
	}
	count, err := catalog.AvailableMediaCount(ctx, libraryID)
	if err != nil || count != 1 {
		t.Fatalf("available media = %d, %v", count, err)
	}
	libraries, err := catalog.Libraries(ctx)
	if err != nil || len(libraries) != 1 || libraries[0].Kind != "movies" {
		t.Fatalf("libraries = %+v, %v", libraries, err)
	}
	stats, err := catalog.Stats(ctx)
	if err != nil || stats.Movies != 1 || stats.Media != 1 {
		t.Fatalf("stats = %+v, %v", stats, err)
	}
	unmatched, err := catalog.UnmatchedItems(ctx)
	if err != nil || len(unmatched) != 1 || unmatched[0].ID != itemID {
		t.Fatalf("unmatched = %+v, %v", unmatched, err)
	}
	episodes, err := catalog.EpisodesForShow(ctx, itemID)
	if err != nil || len(episodes) != 0 {
		t.Fatalf("episodes = %+v, %v", episodes, err)
	}
	if _, err := catalog.Image(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing image = %v", err)
	}
	if _, err := catalog.ItemImage(ctx, itemID, "poster"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing poster = %v", err)
	}
	imageID, err := catalog.UpsertImage(ctx, Image{ItemID: itemID, Kind: "poster", Path: "/poster.jpg", Provider: "tmdb", Tag: "tag", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	for _, get := range []func() (*Image, error){func() (*Image, error) { return catalog.Image(ctx, imageID) }, func() (*Image, error) { return catalog.ItemImage(ctx, itemID, "poster") }} {
		image, err := get()
		if err != nil || image.ID != imageID || image.Path != "/poster.jpg" {
			t.Fatalf("image = %+v, %v", image, err)
		}
	}
	if err := catalog.DeleteItemImage(ctx, itemID, "poster"); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Image(ctx, imageID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted image = %v", err)
	}
}
