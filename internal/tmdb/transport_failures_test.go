package tmdb

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("offline")
}

func TestTMDBClientRejectsInvalidRequestsAndTransportFailures(t *testing.T) {
	ctx := context.Background()
	client := NewWithURLs("key", "en", "http://example.com/\n", "http://example.com", http.DefaultClient)
	if _, err := client.Search(ctx, "movie", "Movie", 0); err == nil || !strings.Contains(err.Error(), "create TMDB request") {
		t.Fatalf("invalid URL = %v", err)
	}
	client = NewWithURLs("key", "en", "http://example.com", "http://example.com", &http.Client{Transport: failingTransport{}})
	if _, err := client.Search(ctx, "movie", "Movie", 0); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("transport error = %v", err)
	}
	client = NewWithURLs("", "en", "http://example.com", "http://example.com", http.DefaultClient)
	if _, err := client.Search(ctx, "movie", "Movie", 0); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("search without key = %v", err)
	}
	if _, err := client.Details(ctx, "movie", 1); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("details without key = %v", err)
	}
	if client.certificationRegion() != "US" {
		t.Fatalf("fallback region = %q", client.certificationRegion())
	}
}
