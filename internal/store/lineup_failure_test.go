package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestChannelLineupRejectsMissingAndMalformedVideoStreams(t *testing.T) {
	for _, tc := range []struct{ name, change, want string }{
		{"missing streams", `DROP TABLE media_streams`, "list channel program video streams"},
		{"invalid stream dimensions", `UPDATE media_streams SET width = 'invalid'`, "scan channel program video stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(filepath.Join(t.TempDir(), "loom.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			libraryID, scanID, err := s.StartScan(ctx, "movies", "/movies")
			if err != nil {
				t.Fatal(err)
			}
			id, err := s.UpsertItem(ctx, ItemInput{LibraryID: libraryID, SourceKey: "movie", Kind: "movie", Title: "Movie", ScanID: scanID})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.UpsertMedia(ctx, MediaFile{ItemID: id, Path: "/movie.mkv", Size: 1, DurationMS: 600_000, LastSeenScanID: scanID}, []Stream{{Index: 0, Kind: "video", Codec: "av1", Width: 1920}}, nil); err != nil {
				t.Fatal(err)
			}
			channel, err := s.CreateChannel(ctx, "movie", "Movie", now())
			if err != nil {
				t.Fatal(err)
			}
			if err := s.AppendChannelPrograms(ctx, channel.ID, []ScheduledProgram{{ItemID: id, StartsAt: "2025-01-01T00:00:00Z", EndsAt: "2025-01-01T01:00:00Z"}}, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(tc.change); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.ChannelLineup(ctx, "2025-01-01T00:00:00Z", "2025-01-01T01:00:00Z"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("lineup = %v", err)
			}
		})
	}
}
