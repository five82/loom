package metadata

import (
	"testing"

	"github.com/five82/loom/internal/tmdb"
)

func TestArtworkFallsBackToTitledBackdrop(t *testing.T) {
	images := []tmdb.ImageCandidate{{FilePath: "/titled.jpg", Language: "en"}}
	if got := defaultBackdropPath(images); got != "/titled.jpg" {
		t.Fatalf("fallback backdrop = %q", got)
	}
	if got := normalizedTitle("A + B!"); got != "ab" {
		t.Fatalf("normalized punctuation = %q", got)
	}
}
