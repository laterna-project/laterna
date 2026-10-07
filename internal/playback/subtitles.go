package playback

import "github.com/laterna-project/laterna/internal/domain"

// PickSubtitle returns the subtitle a playback starts with (domain.Subtitle.Position) for a profile
// that wants its subtitles in lang under mode, the audio played being in audioLang. Languages are
// compared as given: the caller brings them all to one form. false means none.
//
//   - Automatic: a whole subtitle when the audio is in another language; when it is in lang, or
//     says nothing, only a forced one (signs, lines in a foreign language), so that a film in the
//     viewer's own language does not start covered in text.
//   - Always: a subtitle whenever the file has one in lang.
//   - Forced: only a forced one.
//
// Among the candidates: a whole subtitle before a forced one when a whole one is wanted, then
// one without hearing-impaired notes, the one the file marks as default, text before images
// (images are burned in on a device that cannot draw them), and finally the file's order.
func PickSubtitle(subs []domain.Subtitle, mode domain.SubtitleMode, lang, audioLang string) (int, bool) {
	if lang == "" || mode == domain.SubtitleOff {
		return 0, false
	}
	whole := mode == domain.SubtitleAlways || (mode == domain.SubtitleAuto && audioLang != "" && audioLang != lang)
	var best domain.Subtitle
	found := false
	for _, s := range subs {
		if s.Language != lang || (!whole && !s.Forced) {
			continue
		}
		if !found || rank(s, whole) < rank(best, whole) {
			best, found = s, true
		}
	}
	return best.Position, found
}

// rank orders the candidates, lowest first (see PickSubtitle).
func rank(s domain.Subtitle, whole bool) int {
	r := 0
	if whole && s.Forced {
		r += 8
	}
	if s.HearingImpaired {
		r += 4
	}
	if !s.Default {
		r += 2
	}
	if s.Image() {
		r++
	}
	return r
}
