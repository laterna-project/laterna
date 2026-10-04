package app

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/media/fmp4"
	"github.com/laterna-project/laterna/internal/media/transcode"
	"github.com/laterna-project/laterna/internal/playback"
	"github.com/laterna-project/laterna/internal/testfixtures"
)

// browser is the profile of a typical browser: no MKV, no HEVC, no E-AC-3 (a test example).
var browser = playback.DeviceProfile{
	Containers:  []string{"mp4", "webm"},
	Video:       []playback.VideoSupport{{Codec: "h264"}, {Codec: "vp9", MaxBitDepth: 10}},
	AudioCodecs: []string{"aac", "mp3", "opus", "flac"},
	HLS:         true,
}

// moviesByTitle scans the fixture movies and returns their items by title.
func moviesByTitle(t *testing.T) (*App, *clock, domain.Principal, map[string]domain.Item) {
	t.Helper()
	a, c := startMediaApp(t)
	_, p := setupAdmin(t, a)
	ctx := context.Background()
	_, err := a.CreateLibrary(ctx, "Films", domain.LibraryMovies, []string{testRoot("Films")}, "")
	mustNil(t, err)
	waitIdle(t, a)
	page, err := a.ListMovies(ctx, p, ListQuery{PageSize: 50})
	mustNil(t, err)
	out := map[string]domain.Item{}
	for _, v := range page.Items {
		out[v.Item.Title] = v.Item
	}
	return a, c, p, out
}

func TestPlaybackDecisions(t *testing.T) {
	a, _, p, movies := moviesByTitle(t)
	ctx := context.Background()

	// MP4 with H.264 and AAC: the file is played directly.
	direct, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: movies["Big Test Movie"].ID, Audio: -1, Device: browser})
	mustNil(t, err)
	if direct.Method != playback.Direct {
		t.Fatalf("MP4: %s", direct.Method)
	}
	if path, err := a.DirectFile(ctx, direct.SessionID, direct.Token); err != nil || !strings.HasSuffix(path, ".mp4") {
		t.Errorf("direct file: %q %v", path, err)
	}
	if _, err := a.DirectFile(ctx, direct.SessionID, "mauvais-secret"); !isKind(err, domain.ErrNotFound) {
		t.Errorf("wrong secret: %v", err)
	}
	// 10-bit HDR HEVC on this browser without HDR: video re-encoded and converted to BT.709 SDR.
	hdr, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: movies["HDR Test"].ID, Audio: -1, Device: browser})
	mustNil(t, err)
	if hdr.Method != playback.Transcode || hdr.CopyVideo || hdr.ToneMap == "" {
		t.Fatalf("HDR: %+v", hdr)
	}
	sdr := join(t, hlsSegments(t, a, hdr)...)
	decodes(t, sdr)
	if got := ffprobeCSV(t, sdr, "-select_streams", "v:0", "-show_entries", "stream=codec_name,pix_fmt,color_transfer,color_primaries"); strings.TrimRight(got, ",") != "h264,yuv420p,bt709,bt709" {
		t.Errorf("HDR converted (%s): %s", hdr.ToneMap, got)
	}
	mustNil(t, a.StopPlayback(ctx, p, hdr.SessionID, 0))
	// A device without HLS and without the container: no way to play.
	noHLS := playback.DeviceProfile{Containers: []string{"mp4"}, Video: browser.Video, AudioCodecs: browser.AudioCodecs}
	if _, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: movies["Deux Pistes"].ID, Audio: -1, Device: noHLS}); !isKind(err, domain.ErrPrecondition) ||
		domain.CodeOf(err) != "playback.unplayable" || !strings.Contains(err.Error(), "reason.no_hls") {
		t.Errorf("no HLS: %v", err)
	}
	// The keyframe index was computed at import time (background job).
	files, err := a.store.Read().ItemFiles(ctx, movies["Deux Pistes"].ID)
	mustNil(t, err)
	if len(files) != 1 {
		t.Fatalf("%d files", len(files))
	}
	if keys, ok, err := a.store.Read().Keyframes(ctx, files[0].File.ID, files[0].File.Fingerprint); !ok || err != nil || len(keys) != 6 {
		t.Errorf("index at import time: %v %v %v", keys, ok, err)
	}
}

func TestPlaybackHLS(t *testing.T) {
	a, _, p, movies := moviesByTitle(t)
	ctx := context.Background()
	info, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: movies["Deux Pistes"].ID, Audio: -1, Device: browser})
	mustNil(t, err)
	if info.Method != playback.Remux {
		t.Fatalf("H.264 MKV in a browser: %s", info.Method)
	}
	pl, err := a.Playlist(info.SessionID, info.Token)
	mustNil(t, err)
	if !strings.Contains(pl, `#EXT-X-MAP:URI="init.mp4"`) || strings.Count(pl, ".m4s") != 2 {
		t.Fatalf("playlist:\n%s", pl)
	}

	// A seek first (last segment), then the start: two runs, one consistent stream.
	last, err := a.MediaSegment(ctx, info.SessionID, info.Token, 1)
	mustNil(t, err)
	first, err := a.MediaSegment(ctx, info.SessionID, info.Token, 0)
	mustNil(t, err)
	init, err := a.InitSegment(ctx, info.SessionID, info.Token)
	mustNil(t, err)
	if _, err := a.MediaSegment(ctx, info.SessionID, info.Token, 2); !isKind(err, domain.ErrNotFound) {
		t.Errorf("segment outside the playlist: %v", err)
	}

	// The stitched stream decodes in full, with as many frames as the source, and lasts as long as
	// the movie.
	joined := join(t, init, first, last)
	decodes(t, joined)
	if src, got := ffprobeCSV(t, testfixtures.Path(t, "Films/Deux Pistes (2019)/Deux Pistes (2019).mkv"), "-count_frames", "-select_streams", "v:0", "-show_entries", "stream=nb_read_frames"),
		ffprobeCSV(t, joined, "-count_frames", "-select_streams", "v:0", "-show_entries", "stream=nb_read_frames"); src != got {
		t.Errorf("%s frames decoded, %s in the source", got, src)
	}
	sameDuration(t, joined, info.Duration)

	// End: position saved, segments deleted, session closed.
	mustNil(t, a.StopPlayback(ctx, p, info.SessionID, 5*time.Second))
	if _, err := os.Stat(filepath.Dir(init)); !os.IsNotExist(err) {
		t.Errorf("segments still there: %v", err)
	}
	if _, err := a.Playlist(info.SessionID, info.Token); !isKind(err, domain.ErrNotFound) {
		t.Errorf("session still open: %v", err)
	}
}

func TestPlaybackWatchdogAndProgress(t *testing.T) {
	a, c, p, movies := moviesByTitle(t)
	ctx := context.Background()
	movie := movies["Big Test Movie"] // one minute according to the NFO, a twelve-second file
	info, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: movie.ID, Audio: -1, Device: browser})
	mustNil(t, err)

	// Reported position: saved after the delay, not on every report.
	mustNil(t, a.ReportProgress(p, info.SessionID, 11*time.Second))
	a.tickPlayback(ctx)
	if v, _, _ := a.Movie(ctx, p, movie.ID); v.UserData.Played {
		t.Error("saved too early")
	}
	c.advance(progressEvery)
	a.tickPlayback(ctx)
	if v, _, _ := a.Movie(ctx, p, movie.ID); !v.UserData.Played || v.UserData.PlayCount != 1 {
		t.Errorf("end of playback (11 s out of 12): %+v", v.UserData)
	}
	// Another report past 90% does not count a second play.
	mustNil(t, a.ReportProgress(p, info.SessionID, 11500*time.Millisecond))
	c.advance(progressEvery)
	a.tickPlayback(ctx)
	if v, _, _ := a.Movie(ctx, p, movie.ID); v.UserData.PlayCount != 1 {
		t.Errorf("playback counted twice: %d", v.UserData.PlayCount)
	}

	// Another profile cannot control this playback.
	other := domain.Principal{Account: p.Account, Profile: &domain.Profile{ID: domain.NewID()}}
	if err := a.ReportProgress(other, info.SessionID, time.Second); !isKind(err, domain.ErrNotFound) {
		t.Errorf("other profile: %v", err)
	}
	// Abandoned: closed by the watchdog.
	c.advance(idleSession + time.Minute)
	a.tickPlayback(ctx)
	if _, err := a.DirectFile(ctx, info.SessionID, info.Token); !isKind(err, domain.ErrNotFound) {
		t.Errorf("abandoned session still open: %v", err)
	}
}

// join stitches the init segment and segments into one file.
func join(t *testing.T, parts ...string) string {
	t.Helper()
	var data []byte
	for _, p := range parts {
		b, err := os.ReadFile(p)
		mustNil(t, err)
		data = append(data, b...)
	}
	path := filepath.Join(t.TempDir(), "flux.mp4")
	mustNil(t, os.WriteFile(path, data, 0o600))
	return path
}

// decodes checks that a stream decodes in full without errors. (It decodes to a raw file: FFmpeg's
// "null" muxer wrongly complains about the timestamps of a stream without an edit list.)
func decodes(t *testing.T, path string) {
	t.Helper()
	ffmpeg, _, _ := testfixtures.FFmpeg()
	out, err := exec.Command(ffmpeg, "-v", "error", "-i", path, "-f", "rawvideo", "-y", os.DevNull).CombinedOutput()
	if err != nil || len(out) > 0 {
		t.Errorf("decoding %s: %v %s", filepath.Base(path), err, out)
	}
}

// ffprobeCSV returns ffprobe's CSV output (without headers) for a file.
func ffprobeCSV(t *testing.T, path string, args ...string) string {
	t.Helper()
	_, ffprobe, _ := testfixtures.FFmpeg()
	args = append(append([]string{"-v", "error"}, args...), "-of", "csv=p=0", path)
	out, err := exec.Command(ffprobe, args...).Output()
	mustNil(t, err)
	return strings.TrimSpace(string(out))
}

// sameDuration checks that a stream lasts as long as the source. Tolerance: FFmpeg's MP4 demuxer
// shifts the reading of composition offsets (B-frames) by a frame or two.
func sameDuration(t *testing.T, path string, want time.Duration) {
	t.Helper()
	got, err := strconv.ParseFloat(ffprobeCSV(t, path, "-show_entries", "format=duration"), 64)
	if err != nil || math.Abs(got-want.Seconds()) > 0.15 {
		t.Errorf("stream lasts %v, source %v (%v)", got, want, err)
	}
}

// hlsSegments requests every segment of an HLS playback, in the order given by order (missing
// indexes are requested afterwards, in order), and returns the init segment followed by the
// segments in playback order.
func hlsSegments(t *testing.T, a *App, info PlayInfo, order ...int) []string {
	t.Helper()
	ctx := context.Background()
	pl, err := a.Playlist(info.SessionID, info.Token)
	mustNil(t, err)
	n := strings.Count(pl, ".m4s")
	paths := make([]string, n)
	for i := range n {
		order = append(order, i)
	}
	for _, i := range order {
		if paths[i] != "" {
			continue
		}
		paths[i], err = a.MediaSegment(ctx, info.SessionID, info.Token, i)
		mustNil(t, err)
	}
	init, err := a.InitSegment(ctx, info.SessionID, info.Token)
	mustNil(t, err)
	return append([]string{init}, paths...)
}

// Audio the device cannot play (E-AC-3 5.1) under video it can (10-bit HDR HEVC on an HDR TV): the
// video is copied and only the audio is re-encoded, to AAC 5.1.
func TestPlaybackTranscodeAudio(t *testing.T) {
	a, _, p, movies := moviesByTitle(t)
	ctx := context.Background()
	tv := playback.DeviceProfile{
		Containers: []string{"mp4"}, Video: []playback.VideoSupport{{Codec: "h264"}, {Codec: "hevc", MaxBitDepth: 10, HDR: true}},
		AudioCodecs: []string{"aac"}, HLS: true,
	}
	info, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: movies["HDR Test"].ID, Audio: -1, Device: tv})
	mustNil(t, err)
	if info.Method != playback.Transcode || !info.CopyVideo || info.CopyAudio || info.Encoder != "" {
		t.Fatalf("audio only re-encoded: %+v", info)
	}
	parts := hlsSegments(t, a, info, 1, 0)
	joined := join(t, parts...)
	decodes(t, joined)
	if got := ffprobeCSV(t, joined, "-select_streams", "v:0", "-show_entries", "stream=codec_name,codec_tag_string"); got != "hevc,hvc1" {
		t.Errorf("video %q", got)
	}
	if got := ffprobeCSV(t, joined, "-select_streams", "a:0", "-show_entries", "stream=codec_name,channels"); got != "aac,6" {
		t.Errorf("audio %q", got)
	}
	sameDuration(t, joined, info.Duration)
	mustNil(t, a.StopPlayback(ctx, p, info.SessionID, 0))
}

// Old codec (MPEG-4 Part 2 in an AVI, a "DivX"): video re-encoded to H.264, fixed 6 s segments each
// opened by a keyframe, MP3 audio copied. Run with every encoder usable on the machine (forced
// through the configuration), not only the preferred one.
func TestPlaybackTranscodeVideo(t *testing.T) {
	root, src := oldMovie(t, 16)
	ffmpeg, _, _ := testfixtures.FFmpeg()
	// Source frames per 6 s segment. The AVI has irregular timestamps (FFmpeg shifts the second
	// frame by one frame and adds one at the end): each frame must show up once, and only once, in
	// the segment that covers its timestamp.
	want := make([]int, 3)
	for line := range strings.Lines(ffprobeCSV(t, src, "-select_streams", "v:0", "-show_entries", "frame=pts_time")) {
		pts, err := strconv.ParseFloat(strings.TrimSpace(line), 64)
		mustNil(t, err)
		want[min(int(pts/6), len(want)-1)]++
	}
	caps, err := transcode.Detect(context.Background(), ffmpeg)
	mustNil(t, err)
	for _, enc := range caps.Names() {
		t.Run(enc, func(t *testing.T) {
			a, _ := startMediaApp(t, func(o *Options) { o.Encoder = enc })
			_, p := setupAdmin(t, a)
			ctx := context.Background()
			_, err := a.CreateLibrary(ctx, "Films", domain.LibraryMovies, []string{root}, "")
			mustNil(t, err)
			waitIdle(t, a)
			page, err := a.ListMovies(ctx, p, ListQuery{PageSize: 10})
			mustNil(t, err)
			if len(page.Items) != 1 {
				t.Fatalf("%d movies", len(page.Items))
			}
			info, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: page.Items[0].Item.ID, Audio: -1, Device: browser})
			mustNil(t, err)
			if info.Method != playback.Transcode || info.CopyVideo || !info.CopyAudio || info.Encoder != enc {
				t.Fatalf("video re-encoded: %+v", info)
			}
			pl, err := a.Playlist(info.SessionID, info.Token)
			mustNil(t, err)
			// 16 s of video (and a few hundredths more of audio): two 6 s segments and a 4 s one.
			if !strings.Contains(pl, "#EXTINF:6.000000,\n0.m4s") || !strings.Contains(pl, "#EXTINF:6.000000,\n1.m4s") || strings.Count(pl, ".m4s") != 3 {
				t.Fatalf("want fixed 6 s segments:\n%s", pl)
			}
			// A seek first: segments from different runs fit together.
			parts := hlsSegments(t, a, info, 1, 2, 0)
			joined := join(t, parts...)
			decodes(t, joined)
			if got := ffprobeCSV(t, joined, "-select_streams", "v:0", "-show_entries", "stream=codec_name"); got != "h264" {
				t.Errorf("video %q", got)
			}
			if got := ffprobeCSV(t, joined, "-select_streams", "a:0", "-show_entries", "stream=codec_name"); got != "mp3" {
				t.Errorf("audio %q", got)
			}
			frames := func(path string) int {
				n, err := strconv.Atoi(ffprobeCSV(t, path, "-count_frames", "-select_streams", "v:0", "-show_entries", "stream=nb_read_frames"))
				mustNil(t, err)
				return n
			}
			total := 0
			for i, seg := range parts[1:] {
				total += want[i]
				// Each segment decodes on its own (init + segment): it starts with a keyframe.
				if got := frames(join(t, parts[0], seg)); got != want[i] {
					t.Errorf("segment %d alone: %d frames, want %d", i, got, want[i])
				}
			}
			// Junctions between runs included: no frame is lost (without B-frames, decode
			// timestamps never go backwards).
			if got := frames(joined); got != total {
				t.Errorf("%d frames decoded, %d in the source", got, total)
			}
			sameDuration(t, joined, info.Duration)
			mustNil(t, a.StopPlayback(ctx, p, info.SessionID, 0))
		})
	}
}

// oldMovie writes a movie of seconds seconds in the format of an old "DivX" (MPEG-4 Part 2 and MP3
// in an AVI): the video has to be re-encoded for a browser. It returns the library folder and the
// file.
func oldMovie(t *testing.T, seconds int) (root, src string) {
	t.Helper()
	testfixtures.Library(t) // skips the test without FFmpeg
	root = t.TempDir()
	src = filepath.Join(root, "Vieux Film (2003)", "Vieux Film (2003).avi")
	mustNil(t, os.MkdirAll(filepath.Dir(src), 0o750))
	ffmpeg, _, _ := testfixtures.FFmpeg()
	d := strconv.Itoa(seconds)
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=640x360:r=25:d="+d,
		"-f", "lavfi", "-i", "sine=f=330:d="+d, "-c:v", "mpeg4", "-q:v", "6", "-c:a", "libmp3lame", "-b:a", "96k",
		"file:"+src).CombinedOutput(); err != nil {
		t.Fatalf("source: %v %s", err, out)
	}
	return root, src
}

// An encoder that ignored forced keyframes would make its runs skip segments (simulated: 3 s
// segments, keyframes every 6 s). Requesting a skipped segment starts a run at that segment, which
// necessarily opens with a keyframe, so nothing waits forever. A segment past the end of the stream
// is an immediate error.
func TestPlaybackHealsSkippedSegments(t *testing.T) {
	root, _ := oldMovie(t, 16)
	a, _ := startMediaApp(t)
	_, p := setupAdmin(t, a)
	ctx := context.Background()
	_, err := a.CreateLibrary(ctx, "Films", domain.LibraryMovies, []string{root}, "")
	mustNil(t, err)
	waitIdle(t, a)
	page, err := a.ListMovies(ctx, p, ListQuery{PageSize: 10})
	mustNil(t, err)
	info, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: page.Items[0].Item.ID, Audio: -1, Device: browser})
	mustNil(t, err)
	// Six 3 s segments in the stream (the last one up to the end) and a seventh past it.
	resegment(t, a, info.SessionID, playback.FixedSegments(info.Duration+6*time.Second, 3*time.Second))

	begin := time.Now()
	for n := range 6 {
		path, err := a.MediaSegment(ctx, info.SessionID, info.Token, n)
		mustNil(t, err)
		if st, err := os.Stat(path); err != nil || st.Size() == 0 {
			t.Errorf("segment %d: %v", n, err)
		}
	}
	if _, err := a.MediaSegment(ctx, info.SessionID, info.Token, 6); err == nil {
		t.Error("segment past the end of the stream was served")
	}
	if d := time.Since(begin); d > segmentWait/2 {
		t.Errorf("restarts too slow: %v", d)
	}
	mustNil(t, a.StopPlayback(ctx, p, info.SessionID, 0))
}

// Without re-encoding a run can only start on a keyframe: a segment without one at its start
// (simulated: 1 s segments, keyframes every 2 s) does not exist in the stream. Immediate error, no
// restart loop.
func TestPlaybackSegmentMissingFromStream(t *testing.T) {
	a, _, p, movies := moviesByTitle(t)
	ctx := context.Background()
	info, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: movies["Deux Pistes"].ID, Audio: -1, Device: browser})
	mustNil(t, err)
	resegment(t, a, info.SessionID, playback.FixedSegments(info.Duration, time.Second))
	begin := time.Now()
	if _, err := a.MediaSegment(ctx, info.SessionID, info.Token, 1); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("segment without a keyframe: %v", err)
	}
	if _, err := a.MediaSegment(ctx, info.SessionID, info.Token, 2); err != nil {
		t.Errorf("next segment: %v", err)
	}
	if d := time.Since(begin); d > segmentWait/2 {
		t.Errorf("too slow: %v", d)
	}
	mustNil(t, a.StopPlayback(ctx, p, info.SessionID, 0))
}

// resegment replaces the segmenting of a playback that has not produced anything yet (simulated
// edge cases).
func resegment(t *testing.T, a *App, id domain.ID, segs []playback.Segment) {
	t.Helper()
	a.plays.mu.Lock()
	s, ok := a.plays.byID[id]
	a.plays.mu.Unlock()
	if !ok || s == nil {
		t.Fatal("playback not found")
		return
	}
	s.mu.Lock()
	s.segs = segs
	s.mu.Unlock()
}

// Soak test: seeks in every direction (each one starts a run and stops the previous one), then a
// stop in the middle of a run. No FFmpeg is left behind and the playback's folder is deleted.
func TestPlaybackEndurance(t *testing.T) {
	root, _ := oldMovie(t, 60)
	a, _ := startMediaApp(t)
	_, p := setupAdmin(t, a)
	ctx := context.Background()
	_, err := a.CreateLibrary(ctx, "Films", domain.LibraryMovies, []string{root}, "")
	mustNil(t, err)
	waitIdle(t, a)
	page, err := a.ListMovies(ctx, p, ListQuery{PageSize: 10})
	mustNil(t, err)
	if len(page.Items) != 1 {
		t.Fatalf("%d movies", len(page.Items))
	}
	info, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: page.Items[0].Item.ID, Audio: -1, Device: browser})
	mustNil(t, err)
	if info.Method != playback.Transcode {
		t.Fatalf("playback: %+v", info)
	}
	begin := time.Now()
	for _, n := range []int{9, 1, 7, 3, 9, 0, 5, 8, 2, 6, 4} {
		_, err := a.MediaSegment(ctx, info.SessionID, info.Token, n)
		mustNil(t, err)
	}
	t.Logf("11 seeks in %v", time.Since(begin).Round(time.Millisecond))
	// One last seek while segments are being produced, then stop.
	go func() { _, _ = a.MediaSegment(ctx, info.SessionID, info.Token, 0) }()
	time.Sleep(20 * time.Millisecond)
	a.plays.mu.Lock()
	session, ok := a.plays.byID[info.SessionID]
	a.plays.mu.Unlock()
	if !ok || session == nil {
		t.Fatal("playback not found")
		return
	}
	dir := session.dir
	mustNil(t, a.StopPlayback(ctx, p, info.SessionID, 0))
	if n := fmp4.Running(); n != 0 {
		t.Errorf("%d FFmpeg still running after the stop", n)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("playback folder still there: %v", err)
	}
}

// Cap on concurrent video transcodes: at the cap a new playback that needs transcoding is refused
// ("server busy"); a playback without transcoding always goes through; a playback that is stopped,
// or has been paused for a minute, gives its slot back.
func TestTranscodeLimit(t *testing.T) {
	root, _ := oldMovie(t, 16)
	a, clk := startMediaApp(t, func(o *Options) { o.Encoder = "libx264" })
	_, p := setupAdmin(t, a)
	ctx := context.Background()
	_, err := a.CreateLibrary(ctx, "Films", domain.LibraryMovies, []string{root}, "")
	mustNil(t, err)
	waitIdle(t, a)
	page, err := a.ListMovies(ctx, p, ListQuery{PageSize: 10})
	mustNil(t, err)
	movie := page.Items[0].Item.ID
	one := 1
	_, err = a.UpdateSettings(ctx, p, SettingsChanges{MaxTranscodes: &one})
	mustNil(t, err)
	tooMany := 65
	if _, err := a.UpdateSettings(ctx, p, SettingsChanges{MaxTranscodes: &tooMany}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("cap of 65 accepted: %v", err)
	}

	first, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: movie, Audio: -1, Device: browser})
	mustNil(t, err)
	if first.Method != playback.Transcode || first.CopyVideo {
		t.Fatalf("first playback: %+v", first)
	}
	if _, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: movie, Audio: -1, Device: browser}); !errors.Is(err, domain.ErrBusy) {
		t.Fatalf("second transcode at the cap: %v", err)
	}
	st, err := a.SystemStatus(ctx)
	mustNil(t, err)
	if st.Transcodes != 1 || st.TranscodeLimit != 1 {
		t.Errorf("state: %d transcodes, cap %d", st.Transcodes, st.TranscodeLimit)
	}
	// A device that plays the file as is is not affected.
	everything := playback.DeviceProfile{
		Containers: []string{"avi"}, Video: []playback.VideoSupport{{Codec: "mpeg4"}}, AudioCodecs: []string{"mp3"},
	}
	direct, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: movie, Audio: -1, Device: everything})
	mustNil(t, err)
	if direct.Method != playback.Direct {
		t.Errorf("direct play: %+v", direct)
	}

	// Paused for a minute, without an FFmpeg run: the slot is given back.
	clk.advance(transcodeIdle + time.Second)
	second, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: movie, Audio: -1, Device: browser})
	mustNil(t, err)
	// Playback stopped: the slot is given back right away.
	if _, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: movie, Audio: -1, Device: browser}); !errors.Is(err, domain.ErrBusy) {
		t.Fatalf("third transcode at the cap: %v", err)
	}
	mustNil(t, a.StopPlayback(ctx, p, second.SessionID, 0))
	_, err = a.StartPlayback(ctx, p, PlayRequest{ItemID: movie, Audio: -1, Device: browser})
	mustNil(t, err)

	// Automatic: at least one.
	zero := 0
	_, err = a.UpdateSettings(ctx, p, SettingsChanges{MaxTranscodes: &zero})
	mustNil(t, err)
	if limit := a.transcodeLimit(); limit < 1 {
		t.Errorf("automatic cap: %d", limit)
	}
}
