package playback

import (
	"strings"

	"github.com/laterna-project/laterna/internal/domain"
)

// PickAudio returns the audio stream a playback starts with when the viewer wants lang (compared as
// given: the caller brings the languages to one form). Among the streams in lang: a main one before
// a commentary, the one the file marks as default, the one with the most channels, then the file's
// order. false means none in lang.
func PickAudio(streams []domain.Stream, lang string) (int, bool) {
	score := func(s domain.Stream) int {
		r := -min(s.Channels, 8)
		if !s.Default {
			r += 10
		}
		if commentary(s) {
			r += 20
		}
		return r
	}
	var best domain.Stream
	found := false
	for _, s := range streams {
		if s.Kind != domain.StreamAudio || lang == "" || s.Language != lang {
			continue
		}
		if !found || score(s) < score(best) {
			best, found = s, true
		}
	}
	return best.Index, found
}

// commentary reports an audio stream that is a commentary, as its title says ("Commentary",
// "Commentaire", "Kommentar").
func commentary(s domain.Stream) bool {
	t := strings.ToLower(s.Title)
	return strings.Contains(t, "comment") || strings.Contains(t, "kommentar")
}
