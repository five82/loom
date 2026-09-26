package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestFinishScanRollsBackOnCatalogWriteFailures(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"reconcile", `CREATE TRIGGER fail_scan BEFORE UPDATE ON items BEGIN SELECT RAISE(FAIL, 'reconcile rejected'); END`, "reconcile scan"},
		{"library", `CREATE TRIGGER fail_scan BEFORE UPDATE ON libraries BEGIN SELECT RAISE(FAIL, 'library rejected'); END`, "record library scan"},
		{"rotation removal", `CREATE TRIGGER fail_scan BEFORE DELETE ON featured_rotation BEGIN SELECT RAISE(FAIL, 'removal rejected'); END`, "remove ineligible featured movies"},
		{"rotation addition", `CREATE TRIGGER fail_scan BEFORE INSERT ON featured_rotation BEGIN SELECT RAISE(FAIL, 'addition rejected'); END`, "add eligible featured movies"},
		{"scan result", `CREATE TRIGGER fail_scan BEFORE UPDATE ON scan_runs BEGIN SELECT RAISE(FAIL, 'result rejected'); END`, "record scan result"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(filepath.Join(t.TempDir(), "loom.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			lib, scan, err := s.StartScan(ctx, "movies", "/movies")
			if err != nil {
				t.Fatal(err)
			}
			id := addFeaturedTestMovie(t, s, lib, scan, "Movie", 8, nil)
			if err := s.FinishScan(ctx, lib, scan, 1, 1, 0, nil); err != nil {
				t.Fatal(err)
			}
			_, scan, err = s.StartScan(ctx, "movies", "/movies")
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "rotation addition" {
				addFeaturedTestMovie(t, s, lib, scan, "New Movie", 8, nil)
			}
			if _, err := s.db.Exec(tc.sql); err != nil {
				t.Fatal(err)
			}
			if err := s.FinishScan(ctx, lib, scan, 0, 0, 0, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("finish scan = %v, want %s", err, tc.want)
			}
			var available int
			if err := s.db.QueryRow(`SELECT available FROM items WHERE id = ?`, id).Scan(&available); err != nil {
				t.Fatal(err)
			}
			if available != 1 {
				t.Fatal("failed scan marked movie unavailable")
			}
			var status string
			if err := s.db.QueryRow(`SELECT status FROM scan_runs WHERE id = ?`, scan).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "running" {
				t.Fatalf("failed transaction recorded status %q", status)
			}
		})
	}
}
