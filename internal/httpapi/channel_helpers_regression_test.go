package httpapi

import (
	"path/filepath"
	"testing"

	"github.com/five82/loom/internal/store"
)

func TestChannelHelpersHandleMissingVideoAndMedia(t *testing.T) {
	if got := programVideo(nil); got != nil {
		t.Fatalf("missing video = %+v", got)
	}
	if got := streamURL(0, "/missing", map[int64]string{}); got != "" {
		t.Fatalf("missing media ID = %q", got)
	}
	if got := streamURL(123, "", map[int64]string{}); got != "" {
		t.Fatalf("missing path = %q", got)
	}
	path := filepath.Join(t.TempDir(), "missing.mkv")
	cache := map[int64]string{}
	if got := streamURL(123, path, cache); got != "" {
		t.Fatalf("missing file = %q", got)
	}
	if got, ok := cache[123]; !ok || got != "" {
		t.Fatalf("missing file not cached: %q, %v", got, ok)
	}
	if got := programVideo(&store.Stream{Codec: "hevc", Width: 1920, Height: 1080, Resolution: "1080p"}); got == nil || got.Codec != "hevc" || got.Resolution != "1080p" {
		t.Fatalf("video = %+v", got)
	}
}
