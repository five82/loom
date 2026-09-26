package metadata

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/five82/loom/internal/store"
)

func TestImageItemRejectsNonSelectableItems(t *testing.T) {
	service, catalog, movieID := testMovieService(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("provider should not be called") }))
	ctx := context.Background()
	libraries, err := catalog.Libraries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind, imageKind string
		parent          *int64
		want            error
	}{
		{"unmatched", "poster", nil, ErrImageUnavailable},
		{"season", "poster", nil, ErrImageUnavailable},
		{"season", "backdrop", &movieID, ErrImageUnavailable},
	} {
		t.Run(tc.kind+"-"+tc.imageKind, func(t *testing.T) {
			_, scanID, err := catalog.StartScan(ctx, "movies", "/movies")
			if err != nil {
				t.Fatal(err)
			}
			id, err := catalog.UpsertItem(ctx, store.ItemInput{LibraryID: libraries[0].ID, ParentID: tc.parent, SourceKey: tc.kind + tc.imageKind, Kind: tc.kind, Title: "Item", ScanID: scanID})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.ImageOptions(ctx, id, tc.imageKind); !errors.Is(err, tc.want) {
				t.Fatalf("image options = %v", err)
			}
		})
	}
	if _, err := service.ImageOptions(ctx, movieID+9999, "poster"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing item = %v", err)
	}
}
