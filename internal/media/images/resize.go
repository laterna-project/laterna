package images

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/image/draw"
)

// Resize writes to dst the image src scaled down to width pixels (as displayed: EXIF orientation
// applied), keeping its proportions. The result is a JPEG, or a PNG if the image has transparency
// (logos). It is written to a temporary file that is then renamed, so dst never exists half
// written.
func Resize(src, dst string, width int) error {
	img, err := decode(src)
	if err != nil {
		return err
	}
	return resize(img, Orientation(src), src, dst, width)
}

// ResizeData does what Resize does for an image held in memory (a page from an archive). name is
// only used in error messages.
func ResizeData(data []byte, name, dst string, width int) error {
	img, err := decodeReader(bytes.NewReader(data), name)
	if err != nil {
		return err
	}
	return resize(img, 1, name, dst, width)
}

// resize scales down first, then rotates: turning the small image is much cheaper than the
// original.
func resize(img image.Image, orientation int, src, dst string, width int) error {
	b := img.Bounds()
	dw, dh := b.Dx(), b.Dy()
	if swaps(orientation) {
		dw, dh = dh, dw
	}
	if width <= 0 || width >= dw {
		return fmt.Errorf("image %s: width %d outside 1..%d", src, width, dw-1)
	}
	height := max(1, dh*width/dw)
	sw, sh := width, height
	if swaps(orientation) {
		sw, sh = height, width
	}
	scaled := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.CatmullRom.Scale(scaled, scaled.Bounds(), img, b, draw.Src, nil)
	out := orient(scaled, orientation)

	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".resize-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no effect once renamed
	if opaque(img) {
		err = jpeg.Encode(tmp, out, &jpeg.Options{Quality: 85})
	} else {
		err = png.Encode(tmp, out)
	}
	if err = errors.Join(err, tmp.Close()); err != nil {
		return fmt.Errorf("image %s: writing the resized version: %w", src, err)
	}
	return os.Rename(tmp.Name(), dst)
}

// decode reads an image, rejecting oversized dimensions before decoding it.
func decode(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return decodeReader(f, path)
}

func decodeReader(f io.ReadSeeker, path string) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, fmt.Errorf("unreadable image %s: %w", path, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxDecodePixels {
		return nil, fmt.Errorf("image %s: dimensions %d×%d not accepted", path, cfg.Width, cfg.Height)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("unreadable image %s: %w", path, err)
	}
	return img, nil
}

func opaque(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return o.Opaque()
	}
	return true
}
