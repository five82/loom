package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCatalogReadsRejectMalformedMediaAndPlayback(t *testing.T) {
	for _, tc := range []struct {
		name, change string
		run          func(*Store, context.Context, int64, int64) error
	}{
		{"media", `UPDATE media_files SET size = 'invalid'`, func(s *Store, ctx context.Context, _, mediaID int64) error {
			_, err := s.Media(ctx, mediaID)
			return err
		}},
		{"media by path", `UPDATE media_files SET size = 'invalid'`, func(s *Store, ctx context.Context, _, _ int64) error {
			_, err := s.MediaByPath(ctx, "/movie.mkv")
			return err
		}},
		{"streams", `UPDATE media_streams SET width = 'invalid'`, func(s *Store, ctx context.Context, _, mediaID int64) error {
			_, err := s.streams(ctx, mediaID)
			return err
		}},
		{"chapters", `UPDATE media_chapters SET start_ms = 'invalid'`, func(s *Store, ctx context.Context, _, mediaID int64) error {
			_, err := s.chapters(ctx, mediaID)
			return err
		}},
		{"progress", `UPDATE playback_state SET position_ms = 'invalid'`, func(s *Store, ctx context.Context, itemID, _ int64) error { _, err := s.Item(ctx, itemID); return err }},
		{"image", `UPDATE images SET width = 'invalid'`, func(s *Store, ctx context.Context, itemID, _ int64) error {
			_, err := s.ItemImage(ctx, itemID, "poster")
			return err
		}},
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
			itemID, err := s.UpsertItem(ctx, ItemInput{LibraryID: libraryID, SourceKey: "movie", Kind: "movie", Title: "Movie", ScanID: scanID})
			if err != nil {
				t.Fatal(err)
			}
			mediaID, err := s.UpsertMedia(ctx, MediaFile{ItemID: itemID, Path: "/movie.mkv", Size: 1, DurationMS: 600_000, LastSeenScanID: scanID}, []Stream{{Index: 0, Kind: "video", Width: 1920, Height: 1080}}, []Chapter{{Index: 0, StartMS: 1000}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.SetProgress(ctx, itemID, 200_000, 600_000); err != nil {
				t.Fatal(err)
			}
			if _, err := s.UpsertImage(ctx, Image{ItemID: itemID, Kind: "poster", Path: "/poster.jpg", Width: 100}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(tc.change); err != nil {
				t.Fatal(err)
			}
			if err := tc.run(s, ctx, itemID, mediaID); err == nil {
				t.Fatal("corrupt value was silently accepted")
			}
		})
	}
}
