package tmdb

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDetailsIncludesGenres(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movie/10" {
			t.Fatalf("unexpected details request: %s", r.URL.String())
		}
		_, _ = fmt.Fprint(w, `{"id":10,"title":"Movie","genres":[{"id":878,"name":"Science Fiction"}]}`)
	}))
	defer server.Close()

	client := NewWithURLs("key", "en-US", server.URL, server.URL, server.Client())
	details, err := client.Details(context.Background(), "movie", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(details.Genres) != 1 || details.Genres[0].ID != 878 || details.Genres[0].Name != "Science Fiction" {
		t.Fatalf("details genres = %+v", details.Genres)
	}
}

func TestSearchIncludesCandidateVotes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/movie" || r.URL.Query().Get("year") != "1990" {
			t.Fatalf("unexpected search request: %s", r.URL.String())
		}
		_, _ = fmt.Fprint(w, `{"results":[{"id":16384,"title":"My Blue Heaven","release_date":"1990-08-17","vote_average":6.084,"vote_count":274}]}`)
	}))
	defer server.Close()

	client := NewWithURLs("key", "en-US", server.URL, server.URL, server.Client())
	results, err := client.Search(context.Background(), "movie", "My Blue Heaven", 1990)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].VoteAverage != 6.084 || results[0].VoteCount != 274 {
		t.Fatalf("search results = %+v", results)
	}
}
