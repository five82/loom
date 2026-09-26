package channels

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/five82/loom/internal/store"
)

func TestGeneratorReportsCatalogFailures(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	g := NewWith(s, nil, time.UTC)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := g.Update(ctx, testNow); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("update = %v", err)
	}
	if _, err := g.Reconcile(ctx, testNow); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("reconcile = %v", err)
	}
	if _, _, err := g.Extend(ctx, testNow); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("extend = %v", err)
	}
}

func TestNewWithRejectsInvalidLineup(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("invalid lineup was accepted")
		}
	}()
	NewWith(nil, []Channel{{Key: "invalid", Name: "Invalid"}}, time.UTC)
}
