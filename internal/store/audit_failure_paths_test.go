package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditReportsQueryAndRowFailures(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.runAuditCheck(ctx, auditCheck{name: "missing table", query: `SELECT path FROM absent`}, true); err == nil || !strings.Contains(err.Error(), "audit missing table") {
		t.Fatalf("query failure = %v", err)
	}
	if _, err := s.runAuditCheck(ctx, auditCheck{name: "extra columns", query: `SELECT 1, 2`}, true); err == nil || !strings.Contains(err.Error(), "scan audit extra columns") {
		t.Fatalf("malformed check result = %v", err)
	}
	if _, err := s.db.Exec(`DROP TABLE playback_state`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Audit(ctx); err == nil || !strings.Contains(err.Error(), "count preserved state") {
		t.Fatalf("audit after missing state = %v", err)
	}
	if _, err := s.db.Exec(`DROP TABLE images`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.missingArtworkFiles(ctx); err == nil || !strings.Contains(err.Error(), "audit artwork files") {
		t.Fatalf("audit after missing images = %v", err)
	}
}
