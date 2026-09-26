package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditReportsIntegrityAndMetadataCheckFailures(t *testing.T) {
	for _, tc := range []struct{ name, drop, want string }{
		{"integrity", "media_files", "audit items without a media file"},
		{"metadata", "item_genres", "audit movies without genres"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "loom.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			if _, err := s.db.Exec("DROP TABLE " + tc.drop); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Audit(context.Background()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("audit after dropping %s = %v", tc.drop, err)
			}
		})
	}
}
