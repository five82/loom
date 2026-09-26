package library

import "testing"

func TestParseEpisodeFilenameRejectsZeroAndDescendingRanges(t *testing.T) {
	for _, name := range []string{"Show.S01E00.mkv", "Show.S01E03-E02.mkv"} {
		if _, ok := parseEpisodeFilename(name); ok {
			t.Fatalf("accepted invalid episode name %q", name)
		}
	}
}
