// Package images analyzes and resizes catalog images.
package images

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/jpeg" // decoders registered with image.Decode
	_ "image/png"
	"io"
	"os"

	"github.com/buckket/go-blurhash"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// Analysis is what we keep from an image: its dimensions (as displayed, EXIF orientation applied),
// a blurred preview and a hash of its content.
type Analysis struct {
	Width, Height int
	// BlurHash is a compact blurred preview (4x3 components) shown while the image loads.
	BlurHash string
	// Hash is the first 16 characters of the file's SHA-256. It changes with the content.
	Hash string
}

// maxDecodePixels caps the size of decoded images, as a guard against a malicious image.
const maxDecodePixels = 80_000_000

// Analyze reads an image (JPEG, PNG or WebP) and analyzes it.
func Analyze(path string) (Analysis, error) { return AnalyzeWith(path, nil) }

// AnalyzeWith does what Analyze does and writes resized versions of the image at the same time,
// decoding it only once. thumbs is called with the analysis and returns the file for each wanted
// width. Photo thumbnails are made this way, so they are ready before a grid asks for them.
func AnalyzeWith(path string, thumbs func(Analysis) map[int]string) (Analysis, error) {
	f, err := os.Open(path)
	if err != nil {
		return Analysis{}, err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	cfg, _, err := image.DecodeConfig(io.TeeReader(f, h))
	if err != nil {
		return Analysis{}, fmt.Errorf("unreadable image %s: %w", path, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxDecodePixels {
		return Analysis{}, fmt.Errorf("image %s: dimensions %d×%d not accepted", path, cfg.Width, cfg.Height)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Analysis{}, err
	}
	h.Reset()
	img, _, err := image.Decode(io.TeeReader(f, h))
	if err != nil {
		return Analysis{}, fmt.Errorf("unreadable image %s: %w", path, err)
	}
	// The decoder may not read everything: finish the hash on the rest of the file.
	if _, err := io.Copy(h, f); err != nil {
		return Analysis{}, err
	}
	o := Orientation(path)
	hash, err := blurhash.Encode(4, 3, orient(thumbnail(img, 32), o))
	if err != nil {
		return Analysis{}, err
	}
	w, ht := cfg.Width, cfg.Height
	if swaps(o) {
		w, ht = ht, w
	}
	a := Analysis{Width: w, Height: ht, BlurHash: hash, Hash: hex.EncodeToString(h.Sum(nil))[:16]}
	if thumbs != nil {
		for width, dst := range thumbs(a) {
			if err := resize(img, o, path, dst, width); err != nil {
				return Analysis{}, err
			}
		}
	}
	return a, nil
}

// thumbnail scales an image down to width pixels. The blurhash only needs a few pixels and
// computing it on the original would be needlessly slow.
func thumbnail(img image.Image, width int) image.Image {
	b := img.Bounds()
	if b.Dx() <= width {
		return img
	}
	height := max(1, b.Dy()*width/b.Dx())
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}
