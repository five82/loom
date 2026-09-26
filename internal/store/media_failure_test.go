package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpsertMediaAtomicOnStreamAndChapterFailures(t *testing.T) {
	for _, tc := range []struct{ name, trigger, want string }{
		{"release old path", `CREATE TRIGGER reject_media BEFORE DELETE ON media_files BEGIN SELECT RAISE(FAIL, 'release failed'); END`, "release media path"},
		{"update media", `CREATE TRIGGER reject_media BEFORE UPDATE ON media_files BEGIN SELECT RAISE(FAIL, 'update failed'); END`, "upsert media file"},
		{"clear streams", `CREATE TRIGGER reject_media BEFORE DELETE ON media_streams BEGIN SELECT RAISE(FAIL, 'clear streams failed'); END`, "replace media streams"},
		{"add stream", `CREATE TRIGGER reject_media BEFORE INSERT ON media_streams BEGIN SELECT RAISE(FAIL, 'stream failed'); END`, "insert media stream"},
		{"clear chapters", `CREATE TRIGGER reject_media BEFORE DELETE ON media_chapters BEGIN SELECT RAISE(FAIL, 'clear chapters failed'); END`, "replace media chapters"},
		{"add chapter", `CREATE TRIGGER reject_media BEFORE INSERT ON media_chapters BEGIN SELECT RAISE(FAIL, 'chapter failed'); END`, "insert media chapter"},
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
			first, err := s.UpsertItem(ctx, ItemInput{LibraryID: libraryID, SourceKey: "first", Kind: "movie", Title: "First", ScanID: scanID})
			if err != nil {
				t.Fatal(err)
			}
			second, err := s.UpsertItem(ctx, ItemInput{LibraryID: libraryID, SourceKey: "second", Kind: "movie", Title: "Second", ScanID: scanID})
			if err != nil {
				t.Fatal(err)
			}
			old := MediaFile{ItemID: first, Path: "/first.mkv", Size: 1, DurationMS: 1000, LastSeenScanID: scanID}
			if _, err := s.UpsertMedia(ctx, old, []Stream{{Index: 0, Kind: "video", Codec: "h264"}}, []Chapter{{Index: 0, Title: "Beginning"}}); err != nil {
				t.Fatal(err)
			}
			// For the release case, the new item claims the old path. Other cases
			// update the same item to ensure all earlier writes roll back on failure.
			candidate := old
			candidate.Size = 2
			if tc.name == "release old path" {
				candidate.ItemID = second
			}
			if _, err := s.db.Exec(tc.trigger); err != nil {
				t.Fatal(err)
			}
			_, err = s.UpsertMedia(ctx, candidate, []Stream{{Index: 0, Kind: "video", Codec: "av1"}}, []Chapter{{Index: 0, Title: "Replacement"}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("upsert error = %v", err)
			}
			if _, err := s.db.Exec(`DROP TRIGGER reject_media`); err != nil {
				t.Fatal(err)
			}
			still, err := s.MediaByPath(ctx, old.Path)
			if err != nil || still.ItemID != first || still.Size != 1 {
				t.Fatalf("media changed despite failure: %+v, %v", still, err)
			}
			streams, err := s.streams(ctx, still.ID)
			if err != nil || len(streams) != 1 || streams[0].Codec != "h264" {
				t.Fatalf("streams changed: %+v, %v", streams, err)
			}
			chapters, err := s.chapters(ctx, still.ID)
			if err != nil || len(chapters) != 1 || chapters[0].Title != "Beginning" {
				t.Fatalf("chapters changed: %+v, %v", chapters, err)
			}
		})
	}
}
