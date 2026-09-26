package tmdb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestSeasonAndImageEndpoints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != "key" || r.URL.Query().Get("language") != "fr-CA" {
			t.Errorf("query = %v", r.URL.Query())
		}
		switch r.URL.Path {
		case "/tv/42/season/2":
			_, _ = fmt.Fprint(w, `{"id":20,"poster_path":"/season.jpg","episodes":[{"id":9,"episode_number":3,"name":"Third","overview":"Plot","air_date":"2020-01-02","still_path":"/still.jpg"}]}`)
		case "/movie/12/images":
			if got := r.URL.Query().Get("include_image_language"); got != "fr,null" {
				t.Errorf("image language = %q", got)
			}
			_, _ = fmt.Fprint(w, `{"posters":[{"file_path":"/poster.jpg","width":100}],"backdrops":[{"file_path":"/backdrop.jpg"}],"logos":[{"file_path":"/logo.png"}]}`)
		case "/tv/42/season/2/images":
			_, _ = fmt.Fprint(w, `{"posters":[{"file_path":"/season.jpg"}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewWithURLs("key", "fr-CA", server.URL+"/", server.URL+"/", server.Client())
	season, err := client.Season(context.Background(), 42, 2)
	if err != nil || season.ID != 20 || season.PosterPath != "/season.jpg" || !reflect.DeepEqual(season.Episodes, []Episode{{ID: 9, Number: 3, Title: "Third", Overview: "Plot", ReleaseDate: "2020-01-02", StillPath: "/still.jpg"}}) {
		t.Fatalf("season = %+v, %v", season, err)
	}
	images, err := client.Images(context.Background(), "movie", 12)
	if err != nil || len(images.Posters) != 1 || images.Posters[0].Width != 100 || len(images.Backdrops) != 1 || len(images.Logos) != 1 {
		t.Fatalf("images = %+v, %v", images, err)
	}
	posters, err := client.SeasonImages(context.Background(), 42, 2)
	if err != nil || len(posters) != 1 || posters[0].FilePath != "/season.jpg" {
		t.Fatalf("season posters = %+v, %v", posters, err)
	}
	if got := client.ImageURL("/poster.jpg"); got != server.URL+"/original/poster.jpg" {
		t.Fatalf("image URL = %q", got)
	}
	if got := client.ImageURLSize("poster.jpg", "w500"); got != server.URL+"/w500/poster.jpg" {
		t.Fatalf("sized image URL = %q", got)
	}
	if got := client.ImageURL(""); got != "" {
		t.Fatalf("empty image URL = %q", got)
	}
}

func TestTMDBEndpointFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/tv":
			if r.URL.Query().Get("first_air_date_year") != "2021" || r.URL.Query().Get("query") != "Example" {
				t.Errorf("search query = %v", r.URL.Query())
			}
			_, _ = fmt.Fprint(w, `{"results":[{"id":7,"name":"Example","original_name":"Original","first_air_date":"2021-05-06"}]}`)
		case "/tv/7/season/1":
			http.Error(w, "no season", http.StatusNotFound)
		case "/movie/7/images":
			_, _ = fmt.Fprint(w, "not json")
		default:
			http.Error(w, "failure", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	client := NewWithURLs("key", "en-US", server.URL, server.URL, server.Client())
	results, err := client.Search(context.Background(), "tv", "Example", 2021)
	if err != nil || len(results) != 1 || results[0].Title != "Example" || results[0].OriginalTitle != "Original" || results[0].Year != 2021 {
		t.Fatalf("search = %+v, %v", results, err)
	}
	for _, mediaType := range []string{"bad", ""} {
		if _, err := client.Search(context.Background(), mediaType, "x", 0); err == nil {
			t.Fatal("invalid search type accepted")
		}
		if _, err := client.Details(context.Background(), mediaType, 1); err == nil {
			t.Fatal("invalid details type accepted")
		}
		if _, err := client.Images(context.Background(), mediaType, 1); err == nil {
			t.Fatal("invalid images type accepted")
		}
	}
	if _, err := client.Season(context.Background(), 7, 1); err == nil {
		t.Fatal("missing season accepted")
	} else {
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || httpErr.StatusCode != 404 || !strings.Contains(httpErr.Error(), "no season") {
			t.Fatalf("season error = %v", err)
		}
	}
	if _, err := client.Images(context.Background(), "movie", 7); err == nil || !strings.Contains(err.Error(), "decode TMDB response") {
		t.Fatalf("images error = %v", err)
	}
	noKey := NewWithURLs("", "en-US", server.URL, server.URL, server.Client())
	if _, err := noKey.SeasonImages(context.Background(), 7, 1); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("missing key error = %v", err)
	}
}
