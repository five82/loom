package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFeaturedPickErrorsDoNotPartiallyAdvanceRotation(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"missing pick table", `DROP TABLE featured_pick`, "read featured pick"},
		{"missing rotation table", `DROP TABLE featured_rotation`, "no such table"},
		{"cannot mark shown", `CREATE TRIGGER fail_pick BEFORE UPDATE ON featured_rotation BEGIN SELECT RAISE(FAIL, 'cannot mark shown'); END`, "mark featured movie shown"},
		{"cannot save pick", `CREATE TRIGGER fail_pick BEFORE INSERT ON featured_pick BEGIN SELECT RAISE(FAIL, 'cannot save pick'); END`, "save featured pick"},
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
			id := addFeaturedTestMovie(t, s, libraryID, scanID, "Movie", 8, nil)
			if err := s.FinishScan(ctx, libraryID, scanID, 1, 1, 0, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(tc.sql); err != nil {
				t.Fatal(err)
			}
			if _, err := s.FeaturedPickAt(ctx, time.Now()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("pick failure = %v", err)
			}
			if _, err := s.db.Exec(`DROP TRIGGER IF EXISTS fail_pick`); err != nil {
				t.Fatal(err)
			}
			if tc.name == "missing rotation table" || tc.name == "missing pick table" {
				return
			}
			var shown int
			if err := s.db.QueryRow(`SELECT shown FROM featured_rotation WHERE item_id = ?`, id).Scan(&shown); err != nil {
				t.Fatal(err)
			}
			if shown != 0 {
				t.Fatalf("rotation advanced on failure: %d", shown)
			}
		})
	}
}
