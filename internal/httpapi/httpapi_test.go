package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/five82/loom/internal/library"
	"github.com/five82/loom/internal/store"
)

func TestMediaRangeAndProgress(t *testing.T) {
	catalog, itemID, mediaID, contents := testCatalog(t)
	defer func() { _ = catalog.Close() }()
	manager := library.NewManager(nil, 0, slog.Default())
	api := New(catalog, manager, nil, make(chan struct{}, 1))
	server := httptest.NewServer(api.PublicHandler())
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/media/"+strconv.FormatInt(mediaID, 10), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Range", "bytes=2-5")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusPartialContent || string(body) != string(contents[2:6]) {
		t.Fatalf("range response status=%d body=%q", response.StatusCode, body)
	}

	progressBody, _ := json.Marshal(map[string]int64{"position_ms": 540_000, "duration_ms": 600_000})
	request, err = http.NewRequest(http.MethodPut, server.URL+"/api/v1/items/"+strconv.FormatInt(itemID, 10)+"/progress", bytes.NewReader(progressBody))
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var progress store.Progress
	if err := json.NewDecoder(response.Body).Decode(&progress); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !progress.Played {
		t.Fatalf("progress response status=%d progress=%+v", response.StatusCode, progress)
	}
}

func testCatalog(t *testing.T) (*store.Store, int64, int64, []byte) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	contents := []byte("0123456789")
	path := filepath.Join(root, "Movie.mkv")
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := store.Open(filepath.Join(root, "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	libraryID, scanID, err := catalog.StartScan(ctx, "movies", root)
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := catalog.UpsertItem(ctx, store.ItemInput{
		LibraryID: libraryID, SourceKey: "movie", Kind: "movie", Title: "Movie", ScanID: scanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	mediaID, err := catalog.UpsertMedia(ctx, store.MediaFile{
		ItemID: itemID, Path: path, Size: int64(len(contents)), DurationMS: 600_000, LastSeenScanID: scanID,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.FinishScan(ctx, libraryID, scanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	return catalog, itemID, mediaID, contents
}
