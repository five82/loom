package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCatalogAndProgress(t *testing.T) {
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
	itemID, err := catalog.UpsertItem(ctx, ItemInput{
		LibraryID: libraryID, SourceKey: "Arrival (2016)", Kind: "movie",
		Title: "Arrival", Year: 2016, ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalog.UpsertMedia(ctx, MediaFile{
		ItemID: itemID, Path: "/movies/Arrival (2016)/Arrival (2016).mkv",
		Size: 100, MTimeNS: 20, DurationMS: 600_000, Container: "matroska",
		LastSeenScanID: scanID,
	}, []Stream{{Index: 0, Kind: "video", Codec: "av1"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, libraryID, scanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}

	item, err := catalog.Item(ctx, itemID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Title != "Arrival" || item.Media == nil || len(item.Media.Streams) != 1 {
		t.Fatalf("unexpected item: %+v", item)
	}

	progress, err := catalog.SetProgress(ctx, itemID, 60_000, 600_000)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Played || progress.ResumePositionMS != 60_000 {
		t.Fatalf("unexpected resumable progress: %+v", progress)
	}
	continuing, err := catalog.ContinueWatching(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(continuing) != 1 || continuing[0].Progress == nil || continuing[0].Progress.ResumePositionMS != 60_000 {
		t.Fatalf("unexpected continue-watching items: %+v", continuing)
	}
	progress, err = catalog.SetProgress(ctx, itemID, 540_000, 600_000)
	if err != nil {
		t.Fatal(err)
	}
	if !progress.Played || progress.ResumePositionMS != 0 {
		t.Fatalf("unexpected completed progress: %+v", progress)
	}
}

func TestFailedScanDoesNotMakeItemsUnavailable(t *testing.T) {
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
	_, err = catalog.UpsertItem(ctx, ItemInput{
		LibraryID: libraryID, SourceKey: "movie", Kind: "movie", Title: "Movie", ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, libraryID, scanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}

	_, failedScanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, libraryID, failedScanID, 0, 0, 0, context.Canceled); err != nil {
		t.Fatal(err)
	}
	items, err := catalog.ListItems(ctx, ListOptions{LibraryKind: "movies", TopLevel: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("available items = %d, want 1", len(items))
	}
}
