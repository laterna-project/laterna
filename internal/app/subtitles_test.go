package app

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/playback"
	"github.com/laterna-project/laterna/internal/testfixtures"
)

// subtitledMovie copies the fixture movie with subtitles into a temporary folder (the test may add
// files there), imports it and returns its file.
func subtitledMovie(t *testing.T) (*App, domain.Principal, domain.Item, domain.MediaFile, string) {
	t.Helper()
	src := testfixtures.Path(t, "Subtitles/Fonts (2022)")
	root := t.TempDir()
	dir := filepath.Join(root, "Fonts (2022)")
	mustNil(t, os.MkdirAll(dir, 0o750))
	entries, err := os.ReadDir(src)
	mustNil(t, err)
	for _, e := range entries {
		mustNil(t, copyFile(filepath.Join(src, e.Name()), filepath.Join(dir, e.Name())))
	}
	a, _ := startMediaApp(t)
	_, p := setupAdmin(t, a)
	ctx := context.Background()
	_, err = a.CreateLibrary(ctx, "Movies", domain.LibraryMovies, []string{root}, "")
	mustNil(t, err)
	waitIdle(t, a)
	page, err := a.ListMovies(ctx, p, ListQuery{PageSize: 10})
	mustNil(t, err)
	if len(page.Items) != 1 {
		t.Fatalf("%d movies", len(page.Items))
	}
	files, err := a.store.Read().ItemFiles(ctx, page.Items[0].Item.ID)
	mustNil(t, err)
	if len(files) != 1 {
		t.Fatalf("%d files", len(files))
	}
	return a, p, page.Items[0].Item, files[0].File, dir
}

func readText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	mustNil(t, err)
	return string(b)
}

// At import time every stream and every external subtitle is extracted, in the formats we serve,
// along with the attached fonts: everything is ready before the first playback.
func TestSubtitlesExtractedAtImport(t *testing.T) {
	a, p, movie, file, dir := subtitledMovie(t)
	ctx := context.Background()
	set, ok, err := a.store.Read().SubtitleSet(ctx, file.ID)
	if err != nil || !ok || set.Fingerprint != file.Fingerprint || set.Sidecars == "" {
		t.Fatalf("extraction: %+v %v %v", set, ok, err)
	}
	type want struct {
		codec, lang string
		formats     []string
		external    bool
		forced      bool
	}
	wants := []want{
		{"ass", "fre", []string{"ass", "vtt"}, false, false},
		{"subrip", "eng", []string{"vtt"}, false, false},
		{"hdmv_pgs_subtitle", "fre", []string{"sup"}, false, false},
		{"hdmv_pgs_subtitle", "ger", []string{"sup"}, true, false},
		{"subrip", "eng", []string{"vtt"}, true, true},
		{"subrip", "fre", []string{"vtt"}, true, false},
	}
	if len(set.Subtitles) != len(wants) {
		t.Fatalf("%d subtitles: %+v", len(set.Subtitles), set.Subtitles)
	}
	for i, w := range wants {
		s := set.Subtitles[i]
		if s.Position != i || s.Codec != w.codec || s.Language != w.lang || !slices.Equal(s.Formats, w.formats) ||
			s.External() != w.external || s.Forced != w.forced {
			t.Errorf("subtitle %d: %+v, want %+v", i, s, w)
		}
	}
	if pgs := set.Subtitles[2]; pgs.Width != 640 || pgs.Height != 480 {
		t.Errorf("PGS size: %dx%d", pgs.Width, pgs.Height)
	}
	path := func(pos int, format string) string { return a.subtitlePath(file.ID, pos, format) }
	if ass := readText(t, path(0, "ass")); !strings.Contains(ass, "Style: Default,Go,40") || !strings.Contains(ass, `{\pos(320,60)}RAMEN`) {
		t.Errorf("original ASS:\n%s", ass)
	}
	// The WebVTT derived from the ASS: dialogue only, the top line at the top.
	vtt := readText(t, path(0, "vtt"))
	for _, want := range []string{"00:00:01.000 --> 00:00:03.000 line:0\nAt the top", "A line in <i>italics</i>.", "00:00:08.000 --> 00:00:11.000\nSecond line."} {
		if !strings.Contains(vtt, want) {
			t.Errorf("WebVTT from the ASS without %q:\n%s", want, vtt)
		}
	}
	if strings.Contains(vtt, "RAMEN") || strings.Contains(vtt, "ka") && !strings.Contains(vtt, "ka\n") && strings.Contains(vtt, ">ka") {
		t.Errorf("sign or effect in the WebVTT:\n%s", vtt)
	}
	if srt := readText(t, path(1, "vtt")); !strings.Contains(srt, "00:04.000 --> 00:07.000\nFirst line.") {
		t.Errorf("SRT:\n%s", srt)
	}
	if cp1252 := readText(t, path(4, "vtt")); !strings.Contains(cp1252, "Café!") {
		t.Errorf("Windows-1252 SRT:\n%q", cp1252)
	}
	if bom := readText(t, path(5, "vtt")); !strings.Contains(bom, "First line, é à ç.") || strings.Contains(bom, "\ufeff") {
		t.Errorf("SRT with a BOM:\n%q", bom)
	}
	if len(set.Fonts) != 1 || !slices.Contains(set.Fonts[0].Names, "go") {
		t.Fatalf("fonts: %+v", set.Fonts)
	}
	font, fontPath, err := a.FontFile(ctx, set.Fonts[0].SHA256)
	if err != nil || font.Ext != ".ttf" {
		t.Fatalf("font: %+v %v", font, err)
	}
	if st, err := os.Stat(fontPath); err != nil || st.Size() != font.Size {
		t.Errorf("font file: %v", err)
	}
	if _, _, err := a.FontFile(ctx, "../../etc/passwd"); !isKind(err, domain.ErrNotFound) {
		t.Errorf("invalid hash: %v", err)
	}

	// At playback: list and fonts ready, files served by position and format.
	dev := browser
	dev.SubtitleFormats = []string{"vtt", "ass"}
	info, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: movie.ID, Audio: -1, Device: dev})
	mustNil(t, err)
	if !info.SubtitlesReady || len(info.Subtitles) != 6 || len(info.Fonts) != 1 || info.Burned != nil || info.Method != playback.Remux {
		t.Fatalf("playback: %+v", info)
	}
	if got, err := a.SubtitleFile(ctx, info.SessionID, info.Token, 0, "ass"); err != nil || got != path(0, "ass") {
		t.Errorf("subtitle served: %q %v", got, err)
	}
	if _, err := a.SubtitleFile(ctx, info.SessionID, info.Token, 2, "vtt"); !isKind(err, domain.ErrNotFound) {
		t.Errorf("missing format: %v", err)
	}
	if _, err := a.SubtitleFile(ctx, info.SessionID, "wrong-secret", 0, "ass"); !isKind(err, domain.ErrNotFound) {
		t.Errorf("wrong secret: %v", err)
	}
	// Reading again (a client that opened the playback before the extraction finished).
	if again, err := a.PlaybackSubtitles(ctx, p, info.SessionID, true); err != nil || !again.SubtitlesReady || len(again.Subtitles) != 6 || len(again.Fonts) != 1 {
		t.Errorf("reading again: %+v %v", again, err)
	}
	other := domain.Principal{Account: p.Account, Profile: &domain.Profile{ID: domain.NewID()}}
	if _, err := a.PlaybackSubtitles(ctx, other, info.SessionID, false); !isKind(err, domain.ErrNotFound) {
		t.Errorf("other profile: %v", err)
	}
	mustNil(t, a.StopPlayback(ctx, p, info.SessionID, 0))

	// A scan with no change extracts nothing again; an added external subtitle does.
	scan := func() {
		t.Helper()
		lib, err := a.store.Read().Library(ctx, file.LibraryID)
		mustNil(t, err)
		mustNil(t, a.scanLibrary(ctx, lib.ID.String()))
		waitIdle(t, a)
	}
	scan()
	if again, _, _ := a.store.Read().SubtitleSet(ctx, file.ID); !again.ExtractedAt.Equal(set.ExtractedAt) {
		t.Error("extracted again without a change")
	}
	mustNil(t, os.WriteFile(filepath.Join(dir, "Fonts (2022).ja.srt"), []byte("1\n00:00:02,000 --> 00:00:03,000\nこんにちは\n"), 0o600))
	scan()
	added, _, _ := a.store.Read().SubtitleSet(ctx, file.ID)
	if len(added.Subtitles) != 7 || added.Subtitles[6].Language != "jpn" || !strings.Contains(readText(t, path(6, "vtt")), "こんにちは") {
		t.Errorf("external subtitle added: %+v", added.Subtitles)
	}
}

// grayFrame decodes the frame at time at (source time, -copyts) of a stream in grayscale and
// returns its pixels and width.
func grayFrame(t *testing.T, path string, at time.Duration) ([]byte, int) {
	t.Helper()
	ffmpeg, _, _ := testfixtures.FFmpeg()
	w, err := strconv.Atoi(ffprobeCSV(t, path, "-select_streams", "v:0", "-show_entries", "stream=width"))
	mustNil(t, err)
	cmd := exec.Command(ffmpeg, "-v", "error", "-copyts", "-i", path, "-vf",
		"select='gte(t,"+strconv.FormatFloat(at.Seconds(), 'f', 3, 64)+")'", "-frames:v", "1", "-pix_fmt", "gray", "-f", "rawvideo", "-")
	out, err := cmd.StdoutPipe()
	mustNil(t, err)
	mustNil(t, cmd.Start())
	b, err := io.ReadAll(out)
	mustNil(t, err)
	mustNil(t, cmd.Wait())
	return b, w
}

// mean is the average brightness of a rectangle (-1 if the picture is too small).
func mean(px []byte, width, x, y, w, h int) int {
	if len(px) < (y+h)*width {
		return -1
	}
	sum := 0
	for j := y; j < y+h; j++ {
		for i := x; i < x+w; i++ {
			sum += int(px[j*width+i])
		}
	}
	return sum / (w * h)
}

// Last resort: a subtitle the device cannot render is burned in. Image subtitle (PGS drawn on 640 x
// 480, video cropped to 640 x 360): the bars come back and the line that sits in them is visible,
// including one that started before the run did. Text (ASS): rendered by libass with its attached
// font.
func TestPlaybackBurnsSubtitles(t *testing.T) {
	a, p, movie, _, _ := subtitledMovie(t)
	ctx := context.Background()
	burnFirstSegment := func(dev playback.DeviceProfile, sub int) (PlayInfo, string) {
		t.Helper()
		info, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: movie.ID, Audio: -1, Device: dev, Subtitle: &sub})
		mustNil(t, err)
		if info.Method != playback.Transcode || info.CopyVideo || info.Burned == nil || *info.Burned != sub {
			t.Fatalf("burn-in: %+v", info)
		}
		parts := hlsSegments(t, a, info, 1, 0) // the run for segment 1 starts at 6 s, in the middle of a line
		joined := join(t, parts...)
		decodes(t, joined)
		return info, joined
	}

	web := browser
	web.SubtitleFormats = []string{"vtt"} // no PGS and no ASS
	info, joined := burnFirstSegment(web, 2)
	if size := ffprobeCSV(t, joined, "-select_streams", "v:0", "-show_entries", "stream=width,height"); size != "640,480" {
		t.Errorf("picture with its bars: %s", size)
	}
	// Line 1 (4 to 7 s) is in the bottom bar (y 425 to 465), line 2 (8 to 11 s) is inside the
	// picture.
	at5, w := grayFrame(t, joined, 5*time.Second)
	at65, _ := grayFrame(t, joined, 6500*time.Millisecond) // segment 1: run started at 6 s
	at2, _ := grayFrame(t, joined, 2*time.Second)
	if mean(at5, w, 240, 430, 160, 30) < 200 || mean(at65, w, 240, 430, 160, 30) < 200 || mean(at2, w, 240, 430, 160, 30) > 40 {
		t.Errorf("burned-in PGS: %d at 5 s, %d at 6.5 s, %d at 2 s (bottom bar)",
			mean(at5, w, 240, 430, 160, 30), mean(at65, w, 240, 430, 160, 30), mean(at2, w, 240, 430, 160, 30))
	}
	mustNil(t, a.StopPlayback(ctx, p, info.SessionID, 0))

	// No subtitle format at all: the ASS is burned in by libass ("At the top", 1 to 3 s).
	none := browser
	info, joined = burnFirstSegment(none, 0)
	burned, w := grayFrame(t, joined, 2*time.Second)
	clean, _ := grayFrame(t, joined, 3500*time.Millisecond)
	top := func(px []byte) int { return mean(px, w, 200, 5, 240, 50) }
	if size := ffprobeCSV(t, joined, "-select_streams", "v:0", "-show_entries", "stream=width,height"); size != "640,360" {
		t.Errorf("picture: %s", size)
	}
	if d := top(burned) - top(clean); d < 10 && d > -10 {
		t.Errorf("burned-in ASS: top of the picture %d with the line, %d without", top(burned), top(clean))
	}
	mustNil(t, a.StopPlayback(ctx, p, info.SessionID, 0))

	// Subtitle that does not exist: refused.
	bad := 42
	if _, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: movie.ID, Audio: -1, Device: web, Subtitle: &bad}); !isKind(err, domain.ErrInvalid) {
		t.Errorf("subtitle 42: %v", err)
	}
}

// The profile's preferences pick the subtitle a playback starts with (Japanese audio; French ASS,
// English SRT, French PGS, then external German PGS, forced English SRT and French SRT).
func TestPlaybackPicksProfileSubtitle(t *testing.T) {
	a, p, movie, _, _ := subtitledMovie(t)
	ctx := context.Background()
	web := browser
	web.SubtitleFormats = []string{"vtt", "ass"}
	start := func(req PlayRequest) PlayInfo {
		t.Helper()
		req.ItemID, req.Audio, req.Device = movie.ID, -1, web
		info, err := a.StartPlayback(ctx, p, req)
		mustNil(t, err)
		mustNil(t, a.StopPlayback(ctx, p, info.SessionID, 0))
		return info
	}
	prefer := func(mode domain.SubtitleMode, lang string) {
		t.Helper()
		profile, err := a.SetSubtitlePreferences(ctx, p, mode, lang)
		mustNil(t, err)
		p.Profile = &profile
	}
	picked := func(info PlayInfo) int {
		if info.Subtitle == nil {
			return -1
		}
		return *info.Subtitle
	}

	// Nothing asked: nothing shown, as before.
	if got := picked(start(PlayRequest{Language: "fr"})); got != -1 {
		t.Errorf("without profile_subtitle: %d", got)
	}
	// Automatic, French device, Japanese audio: a whole French subtitle in text, not the PGS.
	info := start(PlayRequest{ProfileSubtitle: true, Language: "fr-FR"})
	if got := picked(info); got != 0 && got != 5 || info.Burned != nil {
		t.Errorf("automatic, French: %d (burned %v)", got, info.Burned)
	}
	// The profile's own subtitle language wins over the device's.
	prefer(domain.SubtitleAlways, "en")
	if got := picked(start(PlayRequest{ProfileSubtitle: true, Language: "fr"})); got != 1 {
		t.Errorf("always, English: %d", got)
	}
	prefer(domain.SubtitleForced, "en")
	if got := picked(start(PlayRequest{ProfileSubtitle: true})); got != 4 {
		t.Errorf("forced, English: %d", got)
	}
	prefer(domain.SubtitleOff, "en")
	if got := picked(start(PlayRequest{ProfileSubtitle: true})); got != -1 {
		t.Errorf("off: %d", got)
	}
	// A requested subtitle wins and comes back as is.
	sub := 1
	if got := picked(start(PlayRequest{ProfileSubtitle: true, Subtitle: &sub})); got != 1 {
		t.Errorf("requested: %d", got)
	}
	// German only exists as PGS: a device that cannot draw it gets it burned in.
	prefer(domain.SubtitleAuto, "de")
	info = start(PlayRequest{ProfileSubtitle: true})
	if picked(info) != 3 || info.Burned == nil || *info.Burned != 3 || info.Method != playback.Transcode {
		t.Errorf("German PGS: %d, burned %v, %s", picked(info), info.Burned, info.Method)
	}

	if _, err := a.SetSubtitlePreferences(ctx, p, "sometimes", ""); !isKind(err, domain.ErrInvalid) {
		t.Errorf("unknown mode: %v", err)
	}
	if _, err := a.SetSubtitlePreferences(ctx, p, domain.SubtitleAuto, "not a tag!"); !isKind(err, domain.ErrInvalid) {
		t.Errorf("bad language: %v", err)
	}
}
