package images

import (
	"image"
	"image/draw"

	"github.com/laterna-project/laterna/internal/media/exif"
)

// Orientation returns the EXIF orientation of an image (1 to 8), or 1 if it does not say. A photo
// taken with the phone held upright is often stored sideways with orientation 6.
func Orientation(path string) int {
	info, err := exif.ReadFile(path)
	if err != nil || info.Orientation == 0 {
		return 1
	}
	return info.Orientation
}

// swaps reports an orientation that swaps width and height (quarter turn).
func swaps(o int) bool { return o >= 5 && o <= 8 }

// orient applies an EXIF orientation and returns the image as displayed.
func orient(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	src, ok := img.(*image.RGBA)
	if !ok {
		b := img.Bounds()
		src = image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if swaps(o) {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range h {
		for x := range w {
			var dx, dy int
			switch o {
			case 2: // horizontal flip
				dx, dy = w-1-x, y
			case 3: // half turn
				dx, dy = w-1-x, h-1-y
			case 4: // vertical flip
				dx, dy = x, h-1-y
			case 5: // transpose
				dx, dy = y, x
			case 6: // quarter turn clockwise
				dx, dy = h-1-y, x
			case 7: // transverse
				dx, dy = h-1-y, w-1-x
			case 8: // quarter turn counterclockwise
				dx, dy = y, w-1-x
			}
			dst.SetRGBA(dx, dy, src.RGBAAt(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
