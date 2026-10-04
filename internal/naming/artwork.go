package naming

import (
	"path"
	"regexp"
	"strings"
)

// artworkName matches the images Kodi, Sonarr, Radarr or Jellyfin put next to media: posters,
// backdrops, episode thumbs, season images, logos.
var artworkName = regexp.MustCompile(`(?i)^(poster|fanart\d*|folder|cover|backdrop\d*|banner|logo|clearlogo|clearart|landscape|thumb|disc|discart|keyart|characterart|artist|season[\w-]*)$` +
	`|-(poster|fanart|thumb|banner|landscape|clearlogo|clearart|disc|logo|keyart)$`)

// IsArtwork reports an artwork image (poster, backdrop, thumb...) as opposed to a photo.
func IsArtwork(p string) bool {
	if !IsPhoto(p) {
		return false
	}
	base := path.Base(strings.ReplaceAll(p, `\`, "/"))
	return artworkName.MatchString(strings.TrimSuffix(base, path.Ext(base)))
}
