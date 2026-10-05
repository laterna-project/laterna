package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
	"github.com/laterna-project/laterna/internal/testfixtures"
)

// playbackServer starts a full server whose fixture movies (and the movie with subtitles) are
// scanned and indexed, with their subtitles extracted. It returns its address and an administrator
// token. The cap on concurrent transcodes is set as strict as it gets (one, as on a four-core
// machine without a hardware encoder), so that tests behave the same on every machine.
func playbackServer(t *testing.T) (string, string) {
	t.Helper()
	testfixtures.Library(t)
	ffmpeg, ffprobe, _ := testfixtures.FFmpeg()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(ctx, st, app.Options{ServerName: "Test", FFmpeg: ffmpeg, FFprobe: ffprobe, NoAutoScans: true, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	if err := a.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(a, Options{}))
	t.Cleanup(func() { srv.Close(); cancel(); a.Wait(); _ = st.Close() })
	login, err := a.Setup(ctx, "admin", "a-strong-password", domain.Device{Name: "Test", Client: "Test", ClientVersion: "1", Platform: "Go"}, "")
	if err != nil {
		t.Fatal(err)
	}
	roots := []string{filepath.Join(testfixtures.Root(), "Movies"), filepath.Join(testfixtures.Root(), "Subtitles")}
	if _, err := a.CreateLibrary(ctx, "Movies", domain.LibraryMovies, roots, ""); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(90 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if counts, err := st.Read().CountJobs(ctx); err != nil || len(counts) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job queue never empty")
		}
	}
	one := int32(1)
	system := laternav1connect.NewSystemServiceClient(srv.Client(), srv.URL)
	if _, err := system.UpdateSettings(ctx, authed(&laternav1.UpdateSettingsRequest{MaxTranscodes: &one}, login.Token)); err != nil {
		t.Fatal(err)
	}
	return srv.URL, login.Token
}

func authed[T any](msg *T, token string) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Authorization", "Bearer "+token)
	return req
}

// errorDetail returns the detail of an API error (laterna.v1.ErrorDetail): its code, params and
// causes; nil if the error carries none.
func errorDetail(err error) *laternav1.ErrorDetail {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return nil
	}
	for _, d := range ce.Details() {
		if msg, err := d.Value(); err == nil {
			if detail, ok := msg.(*laternav1.ErrorDetail); ok {
				return detail
			}
		}
	}
	return nil
}

// errorCode returns the code of an API error ("library.not_found"), or an empty string.
func errorCode(err error) string { return errorDetail(err).GetCode() }

func TestPlaybackOverHTTP(t *testing.T) {
	base, token := playbackServer(t)
	ctx := context.Background()
	catalog := laternav1connect.NewCatalogServiceClient(http.DefaultClient, base)
	player := laternav1connect.NewPlaybackServiceClient(http.DefaultClient, base)
	movies, err := catalog.ListMovies(ctx, authed(&laternav1.ListMoviesRequest{}, token))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, m := range movies.Msg.GetMovies() {
		ids[m.GetTitle()] = m.GetId()
	}
	device := &laternav1.DeviceProfile{
		Containers: []string{"mp4"}, Video: []*laternav1.VideoSupport{{Codec: "h264"}},
		AudioCodecs: []string{"aac", "opus", "flac"}, Hls: true,
	}
	get := func(path string, header ...string) *http.Response {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	// HLS without re-encoding: playlist, init segment and segments, through all the middlewares.
	hls, err := player.StartPlayback(ctx, authed(&laternav1.StartPlaybackRequest{ItemId: ids["Dual Audio"], Device: device}, token))
	if err != nil {
		t.Fatal(err)
	}
	if hls.Msg.GetMethod() != laternav1.PlaybackMethod_PLAYBACK_METHOD_HLS_REMUX || !strings.HasSuffix(hls.Msg.GetUrl(), "/main.m3u8") ||
		hls.Msg.AudioStreamIndex == nil || hls.Msg.GetDuration().AsDuration() < 12*time.Second ||
		hls.Msg.GetVideoTranscoded() || hls.Msg.GetAudioTranscoded() || len(hls.Msg.GetReasons()) == 0 {
		t.Fatalf("HLS playback: %v", hls.Msg)
	}
	// Intro and credits given by the "OP" and "ED" chapters.
	if segs := hls.Msg.GetSegments(); len(segs) != 2 || segs[0].GetKind() != laternav1.MediaSegmentKind_MEDIA_SEGMENT_KIND_INTRO ||
		segs[1].GetKind() != laternav1.MediaSegmentKind_MEDIA_SEGMENT_KIND_CREDITS || segs[1].GetStart().AsDuration() != 9*time.Second {
		t.Errorf("segments: %v", segs)
	}
	resp := get(hls.Msg.GetUrl())
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/vnd.apple.mpegurl" {
		t.Fatalf("playlist: %d %v", resp.StatusCode, resp.Header)
	}
	dir := strings.TrimSuffix(hls.Msg.GetUrl(), "main.m3u8")
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasSuffix(line, ".m4s") {
			if r := get(dir + line); r.StatusCode != http.StatusOK || r.Header.Get("Content-Type") != "video/iso.segment" {
				t.Errorf("%s: %d", line, r.StatusCode)
			}
		}
	}
	if r := get(dir + "init.mp4"); r.StatusCode != http.StatusOK {
		t.Errorf("init: %d", r.StatusCode)
	}
	if r := get(strings.Replace(dir, hls.Msg.GetSessionId(), domain.NewID().String(), 1) + "0.m4s"); r.StatusCode != http.StatusNotFound {
		t.Errorf("unknown session: %d", r.StatusCode)
	}
	if _, err := player.StopPlayback(ctx, authed(&laternav1.StopPlaybackRequest{SessionId: hls.Msg.GetSessionId()}, token)); err != nil {
		t.Fatal(err)
	}
	if r := get(hls.Msg.GetUrl()); r.StatusCode != http.StatusNotFound {
		t.Errorf("playlist after the stop: %d", r.StatusCode)
	}

	// Direct play: the file, in ranges.
	direct, err := player.StartPlayback(ctx, authed(&laternav1.StartPlaybackRequest{ItemId: ids["Big Test Movie"], Device: device}, token))
	if err != nil || direct.Msg.GetMethod() != laternav1.PlaybackMethod_PLAYBACK_METHOD_DIRECT {
		t.Fatalf("direct play: %v %v", direct, err)
	}
	if r := get(direct.Msg.GetUrl(), "Range", "bytes=0-99"); r.StatusCode != http.StatusPartialContent || r.Header.Get("Content-Type") != "video/mp4" {
		t.Errorf("Range: %d %v", r.StatusCode, r.Header)
	}
	if _, err := player.ReportProgress(ctx, authed(&laternav1.ReportProgressRequest{SessionId: direct.Msg.GetSessionId()}, token)); err != nil {
		t.Error(err)
	}
	// Subtitles: served on the side, fonts included (URL without authentication, immutable).
	device.SubtitleFormats = []string{"vtt", "ass"}
	subs, err := player.StartPlayback(ctx, authed(&laternav1.StartPlaybackRequest{ItemId: ids["Fonts"], Device: device}, token))
	if err != nil {
		t.Fatal(err)
	}
	if !subs.Msg.GetSubtitlesReady() || len(subs.Msg.GetSubtitles()) != 6 || len(subs.Msg.GetFonts()) != 1 || subs.Msg.BurnedSubtitleIndex != nil {
		t.Fatalf("subtitles: %v", subs.Msg)
	}
	ass := subs.Msg.GetSubtitles()[0]
	if ass.GetCodec() != "ass" || ass.GetLanguage() != "fre" || len(ass.GetFiles()) != 2 || ass.GetFiles()[1].GetFormat() != "vtt" {
		t.Fatalf("ASS track: %v", ass)
	}
	if r := get(ass.GetFiles()[1].GetUrl()); r.StatusCode != http.StatusOK || r.Header.Get("Content-Type") != "text/vtt; charset=utf-8" {
		t.Errorf("WebVTT: %d %v", r.StatusCode, r.Header)
	} else if b, _ := io.ReadAll(r.Body); !strings.HasPrefix(string(b), "WEBVTT") {
		t.Errorf("WebVTT: %q", b)
	}
	if pgs := subs.Msg.GetSubtitles()[2]; !pgs.GetImage() || pgs.GetFiles()[0].GetFormat() != "sup" {
		t.Errorf("PGS track: %v", pgs)
	}
	if ext := subs.Msg.GetSubtitles()[4]; !ext.GetExternal() || !ext.GetForced() {
		t.Errorf("forced external subtitle: %v", ext)
	}
	font := subs.Msg.GetFonts()[0]
	if r := get(font.GetUrl()); r.StatusCode != http.StatusOK || r.Header.Get("Content-Type") != "font/ttf" ||
		!strings.Contains(r.Header.Get("Cache-Control"), "immutable") || r.ContentLength != font.GetSize() {
		t.Errorf("font: %d %v", r.StatusCode, r.Header)
	}
	for _, bad := range []string{strings.Replace(font.GetUrl(), ".ttf", ".otf", 1), "/fonts/abc.ttf", "/fonts/..%2F..%2Fsecret.ttf"} {
		if r := get(bad); r.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", bad, r.StatusCode)
		}
	}
	if r := get(strings.Replace(ass.GetFiles()[1].GetUrl(), "0.vtt", "0.sup", 1)); r.StatusCode != http.StatusNotFound {
		t.Errorf("missing format: %d", r.StatusCode)
	}
	// Image subtitle on a device that cannot render it: burned in.
	two := int32(2)
	burned, err := player.StartPlayback(ctx, authed(&laternav1.StartPlaybackRequest{ItemId: ids["Fonts"], Device: device, SubtitleIndex: &two}, token))
	if err != nil || burned.Msg.GetMethod() != laternav1.PlaybackMethod_PLAYBACK_METHOD_HLS_TRANSCODE || burned.Msg.GetBurnedSubtitleIndex() != 2 || !burned.Msg.GetVideoTranscoded() {
		t.Fatalf("burn-in: %v %v", burned, err)
	}
	device.SubtitleFormats = nil
	// This playback is closed before the next one: a device only has one at a time, and a small
	// machine only transcodes one video at a time (automatic cap: one transcode per four cores
	// without a hardware encoder).
	if _, err := player.StopPlayback(ctx, authed(&laternav1.StopPlaybackRequest{SessionId: burned.Msg.GetSessionId()}, token)); err != nil {
		t.Fatal(err)
	}

	// HDR video on a device without HDR: re-encoded and converted to SDR.
	if hdr, err := player.StartPlayback(ctx, authed(&laternav1.StartPlaybackRequest{ItemId: ids["HDR Test"], Device: device}, token)); err != nil ||
		hdr.Msg.GetMethod() != laternav1.PlaybackMethod_PLAYBACK_METHOD_HLS_TRANSCODE || hdr.Msg.GetToneMapping() == "" {
		t.Errorf("HDR: %v %v", hdr, err)
	}
	// On an HDR TV that cannot play E-AC-3: video copied, only the audio re-encoded.
	tv := &laternav1.DeviceProfile{
		Video:       []*laternav1.VideoSupport{{Codec: "h264"}, {Codec: "hevc", MaxBitDepth: 10, Hdr: true}},
		AudioCodecs: []string{"aac"}, Hls: true,
	}
	sound, err := player.StartPlayback(ctx, authed(&laternav1.StartPlaybackRequest{ItemId: ids["HDR Test"], Device: tv}, token))
	if err != nil || sound.Msg.GetMethod() != laternav1.PlaybackMethod_PLAYBACK_METHOD_HLS_TRANSCODE ||
		sound.Msg.GetVideoTranscoded() || !sound.Msg.GetAudioTranscoded() || sound.Msg.GetVideoEncoder() != "" {
		t.Errorf("audio only re-encoded: %v %v", sound, err)
	}
}
