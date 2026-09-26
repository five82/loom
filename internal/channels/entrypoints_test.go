package channels

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/five82/loom/internal/store"
)

func TestDefaultGeneratorAndEmptyExtend(t *testing.T) {
	catalog, err := store.Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	generator := New(catalog)
	if len(generator.lineup) != len(Lineup) || generator.location != time.Local {
		t.Fatal("default generator did not use the local lineup")
	}
	pruned, added, err := generator.Extend(context.Background(), time.Now())
	if err != nil || pruned != 0 || added != 0 {
		t.Fatalf("empty Extend: %d, %d, %v", pruned, added, err)
	}
}
