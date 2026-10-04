package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/media/images"
	"github.com/laterna-project/laterna/internal/store"
)

// imageWidths are the widths we serve. A requested width is rounded up to the next tier, which caps
// the number of cached versions of each image.
var imageWidths = []int{160, 240, 320, 480, 640, 960, 1280, 1920}

var imageTypes = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".webp": "image/webp", ".gif": "image/gif",
}

// imageCachePath is the resized version of an image, stored by hash (two identical images share
// their resized versions).
func (a *App) imageCachePath(hash string, width int) string {
	return filepath.Join(a.cacheDir, "images", hash[:2], fmt.Sprintf("%s-%d", hash, width))
}

// photoThumbs are the widths prepared as soon as a photo is analyzed (thumbnail grids, on a regular
// and on a very dense screen).
var photoThumbs = []int{240, 480}

// ImageFile is an image file ready to serve.
type ImageFile struct {
	Path string
	// ContentType is empty for a resized version (JPEG or PNG): the type is read from the file.
	ContentType string
	// ETag identifies this exact content (hash and width).
	ETag string
}

// Image returns the file to serve for an image. hash must be the current hash: a stale URL serves
// nothing anymore (the client reloads the item). width = 0 asks for the original; otherwise the
// version scaled down to the next tier, made on first request and cached. An image is never
// upscaled.
func (a *App) Image(ctx context.Context, id domain.ID, hash string, width int) (ImageFile, error) {
	if width < 0 || width > 10_000 {
		return ImageFile{}, domain.Invalid("request.invalid_width")
	}
	img, err := a.store.Read().Image(ctx, id)
	if store.IsNotFound(err) || (err == nil && (img.Hash == "" || img.Hash != hash)) {
		return ImageFile{}, domain.NotFound("image.not_found")
	}
	if err != nil {
		return ImageFile{}, err
	}
	original := ImageFile{Path: img.Path, ContentType: imageTypes[strings.ToLower(filepath.Ext(img.Path))], ETag: `"` + img.Hash + `"`}
	i, _ := slices.BinarySearch(imageWidths, width)
	if width == 0 || i == len(imageWidths) || imageWidths[i] >= img.Width {
		return original, nil
	}
	w := imageWidths[i]
	path := a.imageCachePath(img.Hash, w)
	resized := ImageFile{Path: path, ETag: fmt.Sprintf(`"%s-%d"`, img.Hash, w)}
	if _, err := os.Stat(path); err == nil {
		return resized, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ImageFile{}, err
	}
	select {
	case a.resizing <- struct{}{}:
		defer func() { <-a.resizing }()
	case <-ctx.Done():
		return ImageFile{}, ctx.Err()
	}
	if err := images.Resize(img.Path, path, w); err != nil {
		return ImageFile{}, err
	}
	return resized, nil
}
