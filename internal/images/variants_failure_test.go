package images

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVariantRejectsUnreadableAndInvalidOriginals(t *testing.T) {
	root := t.TempDir()
	if _, err := Variant(filepath.Join(root, "missing.jpg"), 240); err == nil || !strings.Contains(err.Error(), "open original image") {
		t.Fatalf("missing original = %v", err)
	}
	path := filepath.Join(root, "broken.png")
	if err := os.WriteFile(path, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Variant(path, 240); err == nil || !strings.Contains(err.Error(), "decode original image") {
		t.Fatalf("bad original = %v", err)
	}
}

func TestRemoveWithVariantsReportsUndeletableDirectories(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "poster.png")
	cached := variantPath(original, 240)
	for _, path := range []string{original, cached} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "child"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveWithVariants(original); err == nil {
		t.Fatal("nonempty image directories silently removed")
	}
	for _, path := range []string{original, cached} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("directory %q removed: %v", path, err)
		}
	}
}
