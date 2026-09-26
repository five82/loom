package metadata

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type imageRoundTrip func(*http.Request) (*http.Response, error)

func (f imageRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("connection lost") }

func TestImageDownloadRejectsInvalidRequestsAndStreamingFailures(t *testing.T) {
	service, _, id := testMovieService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	ctx := context.Background()
	if _, err := service.downloadProviderImage(ctx, id, "poster", "/image\npath", true, true); err == nil || !strings.Contains(err.Error(), "create image request") {
		t.Fatalf("invalid image URL: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := service.downloadProviderImage(cancelled, id, "poster", "/image.jpg", true, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled download: %v", err)
	}
	service.http = &http.Client{Transport: imageRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(errorReader{})}, nil
	})}
	if _, err := service.downloadProviderImage(ctx, id, "poster", "/image.jpg", true, true); err == nil || !strings.Contains(err.Error(), "save temporary image") {
		t.Fatalf("broken image stream: %v", err)
	}
}

func TestImageDownloadRejectsOversizeAndCleansUpAfterCatalogFailure(t *testing.T) {
	bytes := encodedPNG(t, color.RGBA{R: 255, A: 255})
	service, catalog, id := testMovieService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/images/original/huge" {
			_, _ = w.Write(make([]byte, maxImageBytes+1))
			return
		}
		_, _ = w.Write(bytes)
	}))
	ctx := context.Background()
	if _, err := service.downloadProviderImage(ctx, id, "poster", "/huge", true, true); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize image: %v", err)
	}
	service.http = &http.Client{Transport: imageRoundTrip(func(*http.Request) (*http.Response, error) {
		if err := catalog.Close(); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(bytes)))}, nil
	})}
	if _, err := service.downloadProviderImage(ctx, id, "poster", "/small", true, true); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("closed catalog during download: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(service.imageDir, fmt.Sprint(id)))
	if err != nil || len(entries) != 0 {
		t.Fatalf("orphan downloaded image: %v entries, err %v", entries, err)
	}
}
