package library

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/five82/loom/internal/store"
)

type fakeProber struct {
	paths []string
}

func (p *fakeProber) Probe(_ context.Context, path string) (ProbeResult, error) {
	p.paths = append(p.paths, path)
	return ProbeResult{DurationMS: 600_000, Container: "matroska", Streams: []store.Stream{{Index: 0, Kind: "video", Codec: "av1"}}}, nil
}

func TestParseEpisodeFilename(t *testing.T) {
	tests := []struct {
		name               string
		season, start, end int
		ok                 bool
	}{
		{"Show - S01E02.mkv", 1, 2, 2, true},
		{"Show - s04e01-02 - Title.mkv", 4, 1, 2, true},
		{"looney tunes s1946e15.mkv", 1946, 15, 15, true},
		{"Stephen King's IT (1990).mkv", 0, 0, 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parseEpisodeFilename(test.name)
			if ok != test.ok || got.Season != test.season || got.Start != test.start || got.End != test.end {
				t.Fatalf("parse = %+v, %v; want season=%d start=%d end=%d ok=%v",
					got, ok, test.season, test.start, test.end, test.ok)
			}
		})
	}
}

func TestScannerUsesMovieRootFilesAndRecursiveTVEpisodes(t *testing.T) {
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	shorts := filepath.Join(root, "shorts")
	tv := filepath.Join(root, "tv")
	writeTestFile(t, filepath.Join(movies, "Arrival (2016)", "Arrival (2016).mkv"))
	writeTestFile(t, filepath.Join(shorts, "Presto (2008)", "Presto (2008).mkv"))
	writeTestFile(t, filepath.Join(movies, "Arrival (2016)", "extras", "Bonus.mkv"))
	writeTestFile(t, filepath.Join(tv, "The Office (US)", "Season 4", "The Office (US) - S04E01-02 - Fun Run.mkv"))
	writeTestFile(t, filepath.Join(tv, "Stephen King's IT (1990)", "Stephen King's IT (1990).mkv"))

	catalog, err := store.Open(filepath.Join(root, "state", "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	prober := &fakeProber{}
	scanner := NewScanner(catalog, prober, nil, movies, shorts, tv, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := scanner.Scan(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if len(prober.paths) != 4 {
		t.Fatalf("probed %d files, want 4; paths=%v", len(prober.paths), prober.paths)
	}
	for _, path := range prober.paths {
		if filepath.Base(path) == "Bonus.mkv" {
			t.Fatal("movie extra was probed")
		}
	}

	stats, err := catalog.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Movies != 1 || stats.Shorts != 1 || stats.Shows != 2 || stats.Episodes != 1 || stats.Unmatched != 5 || stats.Media != 4 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	libraries, err := catalog.Libraries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(libraries) != 3 || libraries[0].Kind != "movies" ||
		libraries[1].Kind != "shorts" || libraries[1].Name != "Short Films" ||
		libraries[2].Kind != "tv" {
		t.Fatalf("libraries = %+v", libraries)
	}

	shortItems, err := catalog.ListItems(context.Background(), store.ListOptions{LibraryKind: "shorts", TopLevel: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(shortItems) != 1 || shortItems[0].Kind != "movie" || shortItems[0].Title != "Presto" {
		t.Fatalf("short items = %+v", shortItems)
	}

	shows, err := catalog.ListItems(context.Background(), store.ListOptions{LibraryKind: "tv", TopLevel: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(shows) != 2 {
		t.Fatalf("shows = %d, want 2", len(shows))
	}
	var officeID int64
	for _, show := range shows {
		if show.Title == "The Office (US)" {
			officeID = show.ID
		}
	}
	seasons, err := catalog.ListItems(context.Background(), store.ListOptions{ParentID: &officeID})
	if err != nil {
		t.Fatal(err)
	}
	if len(seasons) != 1 || seasons[0].SeasonNumber != 4 {
		t.Fatalf("unexpected seasons: %+v", seasons)
	}
	episodes, err := catalog.ListItems(context.Background(), store.ListOptions{ParentID: &seasons[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 1 || episodes[0].EpisodeNumber != 1 || episodes[0].EpisodeEndNumber != 2 {
		t.Fatalf("unexpected episodes: %+v", episodes)
	}
}

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseProbeOutput(t *testing.T) {
	result, err := parseProbeOutput([]byte(`{
  "streams": [
    {"index":0,"codec_name":"hevc","codec_type":"video","profile":"Main 10","width":3840,"height":1604,"color_transfer":"smpte2084","disposition":{"default":1}},
    {"index":1,"codec_name":"opus","codec_type":"audio","channels":8,"channel_layout":"7.1","tags":{"language":"eng","title":"Main"}},
    {"index":2,"codec_name":"subrip","codec_type":"subtitle","tags":{"language":"eng"}},
    {"index":3,"codec_name":"mjpeg","codec_type":"video","width":600,"height":900,"disposition":{"attached_pic":1}}
  ],
  "format":{"format_name":"matroska,webm","duration":"600.125"}
}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.DurationMS != 600_125 || result.Container != "matroska,webm" || len(result.Streams) != 3 {
		t.Fatalf("unexpected probe result: %+v", result)
	}
	if !result.Streams[0].IsDefault || result.Streams[0].Profile != "Main 10" ||
		result.Streams[0].DynamicRange != "hdr" || result.Streams[1].ChannelLayout != "7.1" ||
		result.Streams[2].Codec != "subrip" {
		t.Fatalf("unexpected streams: %+v", result.Streams)
	}
}

func TestDynamicRange(t *testing.T) {
	tests := []struct {
		name, codecTag, transfer, sideData, want string
	}{
		{name: "SDR", want: "sdr"},
		{name: "HDR10", transfer: "smpte2084", want: "hdr"},
		{name: "HLG", transfer: "arib-std-b67", want: "hdr"},
		{name: "Dolby Vision codec tag", codecTag: "dvh1", want: "dolby_vision"},
		{name: "Dolby Vision side data", transfer: "smpte2084", sideData: "DOVI configuration record", want: "dolby_vision"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var sideData []probeSideData
			if test.sideData != "" {
				sideData = []probeSideData{{Type: test.sideData}}
			}
			if got := dynamicRange(test.codecTag, test.transfer, sideData); got != test.want {
				t.Fatalf("dynamicRange() = %q, want %q", got, test.want)
			}
		})
	}
}
