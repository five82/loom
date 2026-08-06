package images

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapWidth(t *testing.T) {
	tests := []struct{ requested, want int }{
		{1, 240}, {240, 240}, {241, 480}, {480, 480}, {700, 960},
		{960, 960}, {1440, 1440}, {5000, 1440},
	}
	for _, test := range tests {
		if got := SnapWidth(test.requested); got != test.want {
			t.Errorf("SnapWidth(%d) = %d, want %d", test.requested, got, test.want)
		}
	}
}

func TestVariantResizesAndCaches(t *testing.T) {
	path := writeTestImage(t, "poster-abcdef1234567890.jpg", 600, 400)

	variant, err := Variant(path, 240)
	if err != nil {
		t.Fatal(err)
	}
	if variant == path {
		t.Fatal("variant path should differ from the original")
	}
	decoded := decodeFile(t, variant)
	if decoded.Bounds().Dx() != 240 || decoded.Bounds().Dy() != 160 {
		t.Fatalf("variant size = %dx%d, want 240x160", decoded.Bounds().Dx(), decoded.Bounds().Dy())
	}

	// Overwrite the cached variant with sentinel bytes: a second request must
	// serve the cache, not regenerate.
	sentinel := []byte("cached")
	if err := os.WriteFile(variant, sentinel, 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := Variant(path, 240)
	if err != nil {
		t.Fatal(err)
	}
	if again != variant {
		t.Fatalf("second variant path = %q, want %q", again, variant)
	}
	if data, err := os.ReadFile(again); err != nil || !bytes.Equal(data, sentinel) {
		t.Fatalf("cached variant was regenerated: %v", err)
	}
}

func TestVariantKeepsPNGFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logo-abcdef1234567890.png")
	picture := image.NewRGBA(image.Rect(0, 0, 600, 200))
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, picture); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	variant, err := Variant(path, 240)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(variant)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	_, format, err := image.Decode(file)
	if err != nil || format != "png" {
		t.Fatalf("variant format = %q (%v), want png", format, err)
	}
}

func TestVariantDoesNotUpscale(t *testing.T) {
	path := writeTestImage(t, "poster-abcdef1234567890.jpg", 200, 133)
	variant, err := Variant(path, 240)
	if err != nil {
		t.Fatal(err)
	}
	if variant != path {
		t.Fatalf("small image variant = %q, want the original %q", variant, path)
	}
}

func TestVariantMissingOriginal(t *testing.T) {
	if _, err := Variant(filepath.Join(t.TempDir(), "missing.jpg"), 240); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing original error = %v, want not-exist", err)
	}
}

func TestRemoveWithVariants(t *testing.T) {
	path := writeTestImage(t, "backdrop-abcdef1234567890.jpg", 600, 400)
	variant, err := Variant(path, 240)
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveWithVariants(path); err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{path, variant} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Fatalf("%q was not removed: %v", removed, err)
		}
	}
	if err := RemoveWithVariants(path); err != nil {
		t.Fatalf("removing missing files should not fail: %v", err)
	}
}

func decodeFile(t *testing.T, path string) image.Image {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	decoded, _, err := image.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func writeTestImage(t *testing.T, name string, width, height int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			picture.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, picture, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
