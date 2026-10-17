package app

import (
	"context"
	"fmt"
	"testing"

	"github.com/laterna-project/laterna/internal/domain"
)

// An episode starts with the audio and the subtitle last chosen in its series, before the
// profile's preferences; the profile's audio language applies otherwise. Each profile has its own
// choices.
func TestSeriesChoices(t *testing.T) {
	a, _ := startMediaApp(t)
	_, p := setupAdmin(t, a)
	ctx := context.Background()
	lib, err := a.CreateLibrary(ctx, "Anime", domain.LibraryShows, []string{testRoot("Anime")}, "")
	mustNil(t, err)
	waitIdle(t, a)
	anime := itemByKey(t, a, lib, "series:Anime Test")
	episode := func(n int) domain.ID { return itemByKey(t, a, lib, fmt.Sprintf("episode:%s:1:%d", anime.ID, n)).ID }
	// Each episode: Japanese audio (the default), French audio, then one French ASS subtitle.
	const japanese, french = 1, 2
	play := func(p domain.Principal, ep, audio int) PlayInfo {
		t.Helper()
		info, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: episode(ep), Audio: audio, Device: browser, ProfileSubtitle: true})
		mustNil(t, err)
		return info
	}
	check := func(name string, info PlayInfo, audio, subtitle int) {
		t.Helper()
		got := -1
		if info.Subtitle != nil {
			got = *info.Subtitle
		}
		if info.Audio != audio || got != subtitle {
			t.Errorf("%s: audio %d and subtitle %d, want %d and %d", name, info.Audio, got, audio, subtitle)
		}
	}
	stop := func(p domain.Principal, info PlayInfo) {
		t.Helper()
		mustNil(t, a.StopPlayback(ctx, p, info.SessionID, 0))
	}

	// Nothing chosen: the file's default audio, and French subtitles under audio in another
	// language.
	prof, err := a.SetSubtitlePreferences(ctx, p, domain.SubtitleAuto, "fr")
	mustNil(t, err)
	p.Profile = &prof
	info := play(p, 1, -1)
	check("default audio", info, japanese, 0)
	stop(p, info)

	// The profile prefers French audio: French, and no subtitle under audio in its language.
	prof, err = a.SetAudioPreferences(ctx, p, "fr-FR")
	mustNil(t, err)
	if prof.AudioLanguage != "fr-FR" {
		t.Errorf("audio language: %q", prof.AudioLanguage)
	}
	p.Profile = &prof
	info = play(p, 1, -1)
	check("profile's audio language", info, french, -1)
	stop(p, info)
	if _, err := a.SetAudioPreferences(ctx, p, "not a language!"); !isKind(err, domain.ErrInvalid) {
		t.Errorf("invalid language: %v", err)
	}

	// The viewer picks Japanese in episode 1 and turns the subtitle off: episode 2 starts that way.
	info = play(p, 1, japanese)
	check("Japanese picked", info, japanese, 0)
	mustNil(t, a.NoteSubtitle(p, info.SessionID, -1))
	stop(p, info)
	info = play(p, 2, -1)
	check("as chosen in episode 1", info, japanese, -1)
	// The subtitle back on in episode 2: episode 3 has it.
	mustNil(t, a.NoteSubtitle(p, info.SessionID, 0))
	if err := a.NoteSubtitle(p, info.SessionID, 4); !isKind(err, domain.ErrInvalid) {
		t.Errorf("unknown subtitle: %v", err)
	}
	stop(p, info)
	info = play(p, 3, -1)
	check("as chosen in episode 2", info, japanese, 0)
	stop(p, info)

	// Another profile does not inherit them: its own preferences (none) apply.
	other, err := a.CreateProfile(ctx, p, "Tom", "", false, nil, "")
	mustNil(t, err)
	tom := p
	tom.Profile = &other
	info = play(tom, 2, -1)
	check("another profile", info, japanese, -1)
	stop(tom, info)
}
