// Package images serves resized variants of stored artwork. TMDB originals
// are up to 4K; clients request bucketed widths so a phone never decodes a
// multi-megapixel file for a small card.
package images

import (
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/image/draw"
)

// Fixed buckets keep the on-disk cache to at most four variants per image
// and let every client slot share the same cached file.
var widthBuckets = []int{240, 480, 960, 1440}

const jpegQuality = 85

// Serializes variant generation; contention is rare (single user) and this
// keeps two requests from resizing the same image concurrently.
var generateMu sync.Mutex

// SnapWidth maps a requested width to the smallest bucket that covers it,
// or the largest bucket for oversized requests.
func SnapWidth(requested int) int {
	for _, bucket := range widthBuckets {
		if requested <= bucket {
			return bucket
		}
	}
	return widthBuckets[len(widthBuckets)-1]
}

// Variant returns the path of a cached resized copy of originalPath at the
// given bucketed width, generating it on first use. It returns originalPath
// unchanged when resizing would not shrink the image.
func Variant(originalPath string, width int) (string, error) {
	generateMu.Lock()
	defer generateMu.Unlock()

	path := variantPath(originalPath, width)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	source, err := os.Open(originalPath)
	if err != nil {
		return "", fmt.Errorf("open original image: %w", err)
	}
	defer func() { _ = source.Close() }()
	decoded, format, err := image.Decode(source)
	if err != nil {
		return "", fmt.Errorf("decode original image: %w", err)
	}
	bounds := decoded.Bounds()
	if width >= bounds.Dx() {
		return originalPath, nil
	}
	height := (bounds.Dy()*width + bounds.Dx()/2) / bounds.Dx()
	if height < 1 {
		height = 1
	}
	resized := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(resized, resized.Bounds(), decoded, bounds, draw.Src, nil)

	temporary, err := os.CreateTemp(filepath.Dir(originalPath), ".variant-*")
	if err != nil {
		return "", fmt.Errorf("create temporary variant: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	var encodeErr error
	if format == "png" {
		encodeErr = png.Encode(temporary, resized)
	} else {
		encodeErr = jpeg.Encode(temporary, resized, &jpeg.Options{Quality: jpegQuality})
	}
	closeErr := temporary.Close()
	if encodeErr != nil || closeErr != nil {
		return "", fmt.Errorf("encode variant: %w", errors.Join(encodeErr, closeErr))
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return "", fmt.Errorf("install variant: %w", err)
	}
	return path, nil
}

// RemoveWithVariants deletes an image file and any cached resized variants.
// Missing files are not an error.
func RemoveWithVariants(path string) error {
	var failures []error
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		failures = append(failures, err)
	}
	extension := filepath.Ext(path)
	matches, _ := filepath.Glob(strings.TrimSuffix(path, extension) + "-w*" + extension)
	for _, match := range matches {
		if err := os.Remove(match); err != nil && !os.IsNotExist(err) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Originals are named kind-<tag>.ext, so variants inherit cache invalidation:
// a new original gets a new tag and therefore fresh variant paths.
func variantPath(originalPath string, width int) string {
	extension := filepath.Ext(originalPath)
	return fmt.Sprintf("%s-w%d%s", strings.TrimSuffix(originalPath, extension), width, extension)
}

