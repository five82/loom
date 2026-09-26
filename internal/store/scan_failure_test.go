package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestFinishScanRollsBackAfterReconciliationFailure(t *testing.T) {
	for _, tc := range []struct{ name, trigger, want string }{
		{"reconcile items", `CREATE TRIGGER reject_scan BEFORE UPDATE ON items BEGIN SELECT RAISE(FAIL, 'reconcile failed'); END`, "reconcile scan"},
		{"record library", `CREATE TRIGGER reject_scan BEFORE UPDATE ON libraries BEGIN SELECT RAISE(FAIL, 'library failed'); END`, "record library scan"},
		{"rotation", `CREATE TRIGGER reject_scan BEFORE INSERT ON featured_rotation BEGIN SELECT RAISE(FAIL, 'rotation failed'); END`, "add eligible featured movies"},
		{"record result", `CREATE TRIGGER reject_scan BEFORE UPDATE ON scan_runs BEGIN SELECT RAISE(FAIL, 'result failed'); END`, "record scan result"},
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
			addFeaturedTestMovie(t, s, libraryID, scanID, "Movie", 8, nil)
			if _, err := s.db.Exec(tc.trigger); err != nil {
				t.Fatal(err)
			}
			err = s.FinishScan(ctx, libraryID, scanID, 1, 1, 0, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("finish scan = %v", err)
			}
			var lastScanID int64
			if err := s.db.QueryRow(`SELECT COALESCE(last_scan_id, 0) FROM libraries WHERE id = ?`, libraryID).Scan(&lastScanID); err != nil {
				t.Fatal(err)
			}
			if lastScanID != 0 {
				t.Fatalf("partial library scan committed: %d", lastScanID)
			}
			var status string
			if err := s.db.QueryRow(`SELECT status FROM scan_runs WHERE id = ?`, scanID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "running" {
				t.Fatalf("partial scan result committed: %q", status)
			}
		})
	}
}

func TestStartScanRejectsMissingScanTable(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, _, err := s.StartScan(ctx, "movies", "/movies"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartScan(ctx, "movies", "/movies"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE scan_runs`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartScan(ctx, "movies", "/movies"); err == nil || !strings.Contains(err.Error(), "start scan") {
		t.Fatalf("missing scan table = %v", err)
	}
}
