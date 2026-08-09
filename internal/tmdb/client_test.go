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

func TestDetailsReadCastInBillingOrderAndDirectorsFromCrew(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movie/10" {
			t.Fatalf("unexpected details request: %s", r.URL.String())
		}
		// The cast arrives out of billing order, one entry has no name, and the
		// crew holds jobs other than directing.
		_, _ = fmt.Fprint(w, `{"id":10,"title":"Movie","credits":{"cast":[
{"id":3,"name":"Third Billed","character":"Third","order":2},
{"id":1,"name":"Top Billed","character":"Lead","order":0},
{"id":9,"name":"","character":"Nobody","order":1}],
"crew":[{"id":50,"name":"A Writer","job":"Screenplay"},
{"id":51,"name":"A Director","job":"Director"},
{"id":52,"name":"A Producer","job":"Producer"},
{"id":1,"name":"George Lucas","job":"Executive Producer"}]}}`)
	}))
	defer server.Close()

	client := NewWithURLs("key", "en-US", server.URL, server.URL, server.Client())
	details, err := client.Details(context.Background(), "movie", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(details.Cast) != 2 || details.Cast[0].Name != "Top Billed" ||
		details.Cast[0].Character != "Lead" || details.Cast[1].ID != 3 {
		t.Fatalf("details cast = %+v", details.Cast)
	}
	if len(details.Directors) != 1 || details.Directors[0].ID != 51 ||
		details.Directors[0].Name != "A Director" {
		t.Fatalf("details directors = %+v", details.Directors)
	}
	if len(details.Producers) != 2 || details.Producers[0].ID != 52 ||
		details.Producers[1].ID != 1 {
		t.Fatalf("details producers = %+v", details.Producers)
	}
}

func TestMovieDetailsReadCertificationFromAppendedReleaseDates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movie/10" || r.URL.Query().Get("append_to_response") != "release_dates,credits" {
			t.Fatalf("unexpected details request: %s", r.URL.String())
		}
		// The theatrical window carries the certification here and the digital
		// one does not, which is how TMDB usually reports a US release.
		_, _ = fmt.Fprint(w, `{"id":10,"title":"Movie","tagline":"One line","vote_average":7.5,
"status":"Released","release_dates":{"results":[
{"iso_3166_1":"GB","release_dates":[{"certification":"15"}]},
{"iso_3166_1":"US","release_dates":[{"certification":""},{"certification":"R"}]}]}}`)
	}))
	defer server.Close()

	client := NewWithURLs("key", "en-US", server.URL, server.URL, server.Client())
	details, err := client.Details(context.Background(), "movie", 10)
	if err != nil {
		t.Fatal(err)
	}
	if details.Tagline != "One line" || details.VoteAverage != 7.5 || details.ContentRating != "R" {
		t.Fatalf("movie details = %+v", details)
	}
	if details.Status != "" || details.TotalSeasons != 0 {
		t.Fatalf("movie borrowed a show's fields: %+v", details)
	}
}

func TestShowDetailsReadStatusAndContentRating(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tv/20" || r.URL.Query().Get("append_to_response") != "content_ratings,credits" {
			t.Fatalf("unexpected details request: %s", r.URL.String())
		}
		_, _ = fmt.Fprint(w, `{"id":20,"name":"Show","tagline":"Still going","vote_average":8.25,
"status":"Returning Series","number_of_seasons":4,"content_ratings":{"results":[
{"iso_3166_1":"AU","rating":"M"},{"iso_3166_1":"US","rating":"TV-14"}]}}`)
	}))
	defer server.Close()

	client := NewWithURLs("key", "en-US", server.URL, server.URL, server.Client())
	details, err := client.Details(context.Background(), "tv", 20)
	if err != nil {
		t.Fatal(err)
	}
	if details.Tagline != "Still going" || details.VoteAverage != 8.25 ||
		details.ContentRating != "TV-14" || details.Status != "Returning Series" ||
		details.TotalSeasons != 4 {
		t.Fatalf("show details = %+v", details)
	}
}

func TestDetailsLeaveContentRatingEmptyWithoutTheConfiguredRegion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":10,"title":"Movie","release_dates":{"results":[
{"iso_3166_1":"FR","release_dates":[{"certification":"12"}]}]}}`)
	}))
	defer server.Close()

	client := NewWithURLs("key", "en-US", server.URL, server.URL, server.Client())
	details, err := client.Details(context.Background(), "movie", 10)
	if err != nil {
		t.Fatal(err)
	}
	if details.ContentRating != "" {
		t.Fatalf("content rating = %q, want empty rather than another country's board", details.ContentRating)
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
