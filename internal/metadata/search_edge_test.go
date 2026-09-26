package metadata

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/five82/loom/internal/tmdb"
)

func TestSearchForwardsYearAndType(t *testing.T) {
	var path, query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.Path, r.URL.Query().Encode()
		_, _ = fmt.Fprint(w, `{"results":[{"id":19,"title":"Movie","release_date":"2020-01-01"}]}`)
	}))
	defer server.Close()
	service := New(nil, tmdb.NewWithURLs("key", "en-US", server.URL, server.URL+"/images", server.Client()), "", slog.Default())
	results, err := service.Search(context.Background(), "movie", "Movie", 2020)
	if err != nil || len(results) != 1 || results[0].ID != 19 {
		t.Fatalf("Search: %+v, %v", results, err)
	}
	if path != "/search/movie" || query == "" {
		t.Fatalf("provider request: %s?%s", path, query)
	}
}
