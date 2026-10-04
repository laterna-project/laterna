package naming

import "testing"

func TestIsArtwork(t *testing.T) {
	for name, want := range map[string]bool{
		"poster.jpg": true, "Fanart.png": true, "fanart1.jpg": true, "folder.jpg": true, "cover.webp": true,
		"season01-poster.jpg": true, "season-specials-banner.jpg": true, "Bleach - S01E01 (001)-thumb.jpg": true,
		"clearlogo.png": true, "Film (2020)-landscape.jpg": true,
		"IMG_2041.jpg": false, "vacances.png": false, "poster.mkv": false, "plage-coucher.jpg": false,
	} {
		if got := IsArtwork(name); got != want {
			t.Errorf("%s: %v", name, got)
		}
	}
}
