package metadata

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/five82/loom/internal/store"
)

func TestMatchAndAutoMatchRejectInvalidItemKinds(t *testing.T) {
	service, catalog, movieID := testMovieService(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("provider should not be called") }))
	ctx := context.Background()
	libraries, err := catalog.Libraries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, scanID, err := catalog.StartScan(ctx, "movies", "/movies")
	if err != nil {
		t.Fatal(err)
	}
	seasonID, err := catalog.UpsertItem(ctx, store.ItemInput{LibraryID: libraries[0].ID, ParentID: &movieID, SourceKey: "season", Kind: "season", Title: "Season", ScanID: scanID})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AutoMatch(ctx, seasonID); err != nil {
		t.Fatalf("season should not be auto-matched: %v", err)
	}
	if err := service.Match(ctx, seasonID, 17); err == nil || !strings.Contains(err.Error(), "only movies and shows") {
		t.Fatalf("match season = %v", err)
	}
	if _, err := service.ResetImage(ctx, movieID, "invalid"); !errors.Is(err, ErrImageUnavailable) {
		t.Fatalf("reset invalid kind = %v", err)
	}
	if _, err := service.SelectImage(ctx, movieID, "poster", "tmdb", ""); !errors.Is(err, ErrImageOptionNotFound) {
		t.Fatalf("empty provider path = %v", err)
	}
}
