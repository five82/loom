package metadata

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/five82/loom/internal/store"
)

func TestClearImageRemovesCatalogRowEvenIfFileIsUndeletable(t *testing.T) {
	service, catalog, id := testMovieService(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "poster.jpg")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertImage(ctx, store.Image{ItemID: id, Kind: "poster", Path: path}); err != nil {
		t.Fatal(err)
	}
	service.clearImage(ctx, id, "poster")
	if _, err := catalog.ItemImage(ctx, id, "poster"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("image row survived: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("directory was removed: %v", err)
	}
	// Clearing an already absent selection is also harmless.
	service.clearImage(ctx, id, "poster")
}

func TestClearImageLeavesFileWhenDatabaseDeleteFails(t *testing.T) {
	service, catalog, id := testMovieService(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "poster.jpg")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.UpsertImage(ctx, store.Image{ItemID: id, Kind: "poster", Path: path}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(filepath.Dir(service.imageDir), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_delete BEFORE DELETE ON images BEGIN SELECT RAISE(FAIL, 'delete refused'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	service.clearImage(ctx, id, "poster")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("image file removed despite failed database delete: %v", err)
	}
	if _, err := catalog.ItemImage(ctx, id, "poster"); err != nil {
		t.Fatalf("image row was deleted: %v", err)
	}
}

func TestMetadataCleanupOnClosedCatalogDoesNotPanic(t *testing.T) {
	service, catalog, id := testMovieService(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	service.clearImage(ctx, id, "poster")
	service.clearSeasonPosters(ctx, id)
	service.clearEpisodeThumbs(ctx, id)
	if err := service.AutoMatch(ctx, id); err == nil {
		t.Fatal("AutoMatch accepted closed catalog")
	}
	if err := service.Match(ctx, id, 1); err == nil {
		t.Fatal("Match accepted closed catalog")
	}
}
