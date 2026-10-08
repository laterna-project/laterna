package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/jobs"
	"github.com/laterna-project/laterna/internal/media/fmp4"
	"github.com/laterna-project/laterna/internal/media/keyframes"
	"github.com/laterna-project/laterna/internal/media/remux"
	"github.com/laterna-project/laterna/internal/media/subtitles"
	"github.com/laterna-project/laterna/internal/media/transcode"
	"github.com/laterna-project/laterna/internal/naming"
	"github.com/laterna-project/laterna/internal/playback"
	"github.com/laterna-project/laterna/internal/store"
	"github.com/laterna-project/laterna/internal/telemetry"
)

// Playback settings.
const (
	// segmentWait caps the wait for a requested segment.
	segmentWait = 30 * time.Second
	// aheadLimit: a run does not produce more segments than this ahead of the last one requested.
	// Beyond that FFmpeg waits (full pipe) without burning CPU.
	aheadLimit = 8
	// reachAhead: a segment requested further than this ahead of the current run is a seek, and a
	// new run starts from it.
	reachAhead = 3
	// idleRun: with no segment request for this long, the FFmpeg run is stopped.
	idleRun = 30 * time.Second
	// idleSession: with no news from the player for this long, the session is closed.
	idleSession = 30 * time.Minute
	// progressEvery is the time between two writes of the playback position to the database.
	progressEvery = 30 * time.Second
	// watchEvery is how often the watchdog runs.
	watchEvery = 5 * time.Second
	// maxTranscodeHeight caps the height of a re-encoded video.
	maxTranscodeHeight = 1080
)

// PlayRequest asks to play a movie or an episode on a device.
type PlayRequest struct {
	ItemID domain.ID
	// FileID picks a version; nil means the first present one.
	FileID *domain.ID
	// Audio is the index of the wanted audio stream; negative for the default one.
	Audio  int
	Device playback.DeviceProfile
	// Subtitle is the subtitle shown from the start (domain.Subtitle.Position); nil for none. It
	// only changes playback if the device cannot render it (burn-in).
	Subtitle *int
	// ProfileSubtitle asks, when Subtitle is nil, for the subtitle the profile's preferences pick.
	ProfileSubtitle bool
	// Language is the device's language (Accept-Language): the subtitles' when the profile names
	// none.
	Language string
}

// PlayInfo describes an open playback.
type PlayInfo struct {
	SessionID domain.ID
	// Token protects the stream URLs (a <video> element sends no header).
	Token    string
	Method   playback.Method
	File     domain.MediaFile
	Audio    int
	Duration time.Duration
	// Resume is the profile's resume point (0 means from the start).
	Resume time.Duration
	// Reasons say why playback is not direct.
	Reasons []domain.Text
	// CopyVideo and CopyAudio tell which streams are copied (re-encoded otherwise). Encoder is the
	// video encoder, ToneMap the HDR to SDR conversion (empty if none), GPU says the picture is
	// decoded and processed on the card, Decoder names the hardware decoder used otherwise (empty
	// if none).
	CopyVideo, CopyAudio bool
	Encoder, ToneMap     string
	GPU                  bool
	Decoder              string
	// Subtitles and Fonts are the subtitles of the file, served on the side, and the fonts of its
	// ASS subtitles. SubtitlesReady is false until they are extracted (their URLs wait for it).
	// Subtitle is the one shown from the start (requested, or picked by the profile's
	// preferences), nil for none. Burned is the subtitle burned into the picture, nil otherwise.
	Subtitles      []domain.Subtitle
	Fonts          []domain.Font
	SubtitlesReady bool
	Subtitle       *int
	Burned         *int
}

// playSession is a playback in progress.
type playSession struct {
	id      domain.ID
	token   string
	profile domain.ID
	item    domain.ID
	// who says who is watching what, for the admin API and the activity log.
	who     playViewer
	file    domain.MediaFile
	plan    playback.Plan
	encoder transcode.Encoder
	// running tracks the runs in progress, including those a seek canceled and that are finishing.
	running sync.WaitGroup
	// burn is the burned-in subtitle (last resort), nil otherwise. toneMap is the HDR to SDR
	// conversion, nil otherwise.
	burn    *transcode.Burn
	toneMap *transcode.ToneMapper
	// gpu means the picture is decoded and processed on the card; otherwise decoder, if set,
	// decodes it there. Both are given up if the card fails (s.mu).
	gpu     bool
	decoder *transcode.Decoder
	segs    []playback.Segment
	dir     string
	// conv is the audio conversion of a converted playback, nil otherwise. music marks a music
	// track (never a resume point).
	conv  *conversion
	music bool

	mu       sync.Mutex
	changed  chan struct{} // closed (then replaced) on every change: wakes up waiters
	ready    map[int]bool
	init     bool
	run      *ffmpegRun
	wanted   int
	lastSeen time.Time
	// Progress: last reported position, not saved yet if dirty.
	position time.Duration
	dirty    bool
	savedAt  time.Time
	finished bool
	closed   bool
	// History: the play, and the time actually watched (how far playback moved between two reported
	// positions, seeks excluded) since position lastPos reported at lastAt (at first: the suggested
	// resume point). reported is set after the first report, recorded once the play is kept.
	play     domain.Play
	watched  time.Duration
	lastPos  time.Duration
	lastAt   time.Time
	reported bool
	recorded bool
}

// advance counts the time watched up to the reported position: the progress since the previous one
// if it is plausible (at most twice the elapsed time, plus 5 s). A seek, forward or back, does not
// count. On the first report, a playback that did not start from the suggested resume point started
// from the beginning. Call it with s.mu held.
func (s *playSession) advance(position time.Duration, now time.Time) {
	limit := 2*now.Sub(s.lastAt) + 5*time.Second
	d := position - s.lastPos
	if !s.reported && (d <= 0 || d > limit) {
		d = position
	}
	if d > 0 && d <= limit {
		s.watched += d
	}
	s.reported = true
	s.lastPos, s.lastAt = position, now
}

// playViewer says who is watching a playback, and what.
type playViewer struct {
	accountID                     domain.ID
	username, profileName, device string
	title                         string
	startedAt                     time.Time
}

// itemTitle is the title of an item as the log shows it (an episode with its series, a track with
// its artists).
func itemTitle(v domain.ItemView) string {
	switch {
	case v.Episode != nil && v.SeriesTitle != "":
		return fmt.Sprintf("%s — S%02dE%02d %s", v.SeriesTitle, v.Episode.SeasonNumber, v.Episode.Number, v.Item.Title)
	case v.Track != nil && v.Track.Artists != "":
		return v.Track.Artists + " — " + v.Item.Title
	}
	return v.Item.Title
}

// methodText describes a playback method, for the activity log.
func methodText(plan playback.Plan) domain.Text {
	switch {
	case plan.Method == playback.Direct:
		return domain.T("activity.method.direct")
	case plan.Method == playback.Convert:
		return domain.T("activity.method.convert")
	case plan.CopyVideo && plan.CopyAudio:
		return domain.T("activity.method.remux")
	case plan.CopyVideo:
		return domain.T("activity.method.transcode_audio")
	default:
		return domain.T("activity.method.transcode")
	}
}

// ffmpegRun is an FFmpeg run that produces the segments from start on.
type ffmpegRun struct {
	// next is the first segment the run has not finished yet. Earlier ones are ready, or skipped
	// (no keyframe at their start) and will not come from this run.
	start, next int
	cancel      context.CancelFunc
	done        bool
	err         error
}

// broadcast wakes up waiters (s.mu held).
func (s *playSession) broadcast() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *playSession) segmentPath(n int) string {
	return filepath.Join(s.dir, strconv.Itoa(n)+".m4s")
}

// StartPlayback opens the playback of a movie, an episode or a track for the described device:
// direct play if the device plays the file, otherwise HLS with or without re-encoding or, for
// music, a converted file.
func (a *App) StartPlayback(ctx context.Context, p domain.Principal, req PlayRequest) (PlayInfo, error) {
	v, err := viewerOf(p)
	if err != nil {
		return PlayInfo{}, err
	}
	profile := v.ProfileID
	read := a.store.Read()
	view, err := read.View(ctx, v, req.ItemID)
	it := view.Item
	if store.IsNotFound(err) || (err == nil && it.Kind != domain.ItemMovie && it.Kind != domain.ItemEpisode && it.Kind != domain.ItemTrack) {
		return PlayInfo{}, domain.NotFound("catalog.item_not_found")
	}
	if err != nil {
		return PlayInfo{}, err
	}
	files, err := read.ItemFiles(ctx, it.ID)
	if err != nil {
		return PlayInfo{}, err
	}
	var file *domain.MediaFile
	for i := range files {
		f := &files[i].File
		if f.MissingSince == nil && f.AnalyzedAt != nil && (req.FileID == nil || f.ID == *req.FileID) {
			file = f
			break
		}
	}
	if file == nil {
		return PlayInfo{}, domain.NotFound("playback.no_readable_file")
	}
	music := it.Kind == domain.ItemTrack
	var subs []domain.Subtitle
	var fonts []domain.Font
	subsReady := true
	if !music {
		if subs, fonts, subsReady, err = a.subtitlesOf(ctx, *file); err != nil {
			return PlayInfo{}, err
		}
	}
	var chosen *domain.Subtitle
	if req.Subtitle != nil {
		if *req.Subtitle < 0 || *req.Subtitle >= len(subs) {
			return PlayInfo{}, domain.Invalid("playback.unknown_subtitle", "index", *req.Subtitle)
		}
		chosen = &subs[*req.Subtitle]
	}
	if chosen == nil && req.ProfileSubtitle && !music && p.Profile != nil {
		chosen = profileSubtitle(*p.Profile, req.Language, file.Info, req.Audio, subs)
	}
	plan := playback.Decide(file.Info, req.Device, req.Audio, chosen)
	var encoder transcode.Encoder
	var toneMap *transcode.ToneMapper
	gpu := false
	var decoder *transcode.Decoder
	switch {
	case plan.Method == playback.Unplayable:
		return PlayInfo{}, domain.Precondition("playback.unplayable", plan.Reasons)
	case plan.Method == playback.Transcode && !plan.CopyVideo:
		if n, limit := a.activeTranscodes(), a.transcodeLimit(); n >= limit {
			a.log.InfoContext(ctx, "playback refused: simultaneous transcodes at the limit", "item", it.ID, "active", n, "limit", limit)
			return PlayInfo{}, domain.Busy("playback.transcode_limit", "limit", limit)
		}
		caps, err := a.encoders()
		best, ok := caps.Best()
		if !ok {
			a.log.WarnContext(ctx, "playback: no encoder to transcode the video", "item", it.ID, "err", err)
			return PlayInfo{}, domain.Precondition("playback.no_encoder")
		}
		encoder = best
		// On the card, unless a subtitle has to be burned in (overlay in memory). The CPU
		// conversion stays ready as a fallback. Without that chain, the card may still decode.
		gpu = caps.GPU && !plan.Burn
		if !gpu {
			decoder = caps.Decoder
		}
		if plan.ToneMap {
			tm, ok := caps.BestToneMapper()
			if !ok && !gpu {
				return PlayInfo{}, domain.Precondition("playback.no_tone_mapping", plan.Reasons)
			}
			toneMap = &tm
		}
	}
	s := &playSession{
		id: domain.NewID(), token: newToken(), profile: profile, item: it.ID, file: *file, plan: plan, encoder: encoder, toneMap: toneMap,
		gpu: gpu, decoder: decoder,
		changed: make(chan struct{}), ready: map[int]bool{}, lastSeen: a.now(), savedAt: a.now(),
		position: view.UserData.Position, music: music,
		play: playOf(profile, view, file.Info.Duration, a.now()), lastPos: view.UserData.Position, lastAt: a.now(),
		who: playViewer{
			accountID: p.Account.ID, username: p.Account.Username, profileName: p.Profile.Name,
			device: "unknown device", title: itemTitle(view), startedAt: a.now(),
		},
	}
	if sess, err := read.Session(ctx, p.SessionID); err == nil {
		s.who.device = sess.Device.Name
	}
	if plan.Method == playback.Convert {
		// Started right away: often ready before the device asks for the file.
		channels := 2
		for _, st := range file.Info.Streams {
			if st.Index == plan.Audio {
				channels = st.Channels
			}
		}
		s.conv = a.convertAudio(*file, plan.Audio, channels, 0) //nolint:contextcheck // the conversion outlives the request: background context
	}
	if s.hls() {
		// Video copied: segments on the source keyframes. Video re-encoded: fixed segments with
		// forced keyframes.
		if plan.CopyVideo {
			keys, err := a.keyframesOf(ctx, *file)
			if err != nil {
				return PlayInfo{}, err
			}
			s.segs = playback.Segments(keys, file.Info.Duration, playback.TargetSegment)
		} else {
			s.segs = playback.FixedSegments(file.Info.Duration, playback.TargetSegment)
		}
		if len(s.segs) == 0 {
			return PlayInfo{}, domain.Precondition("playback.unknown_duration")
		}
		s.wanted = playback.Find(s.segs, s.position)
		s.dir = filepath.Join(a.cacheDir, "playback", s.id.String())
		if err := os.MkdirAll(s.dir, 0o750); err != nil {
			return PlayInfo{}, err
		}
	}
	var burned *int
	if plan.Burn && chosen != nil {
		if s.burn, err = a.prepareBurn(ctx, s, chosen.Position); err != nil {
			_ = os.RemoveAll(s.dir)
			return PlayInfo{}, err
		}
		burned = &chosen.Position
	}
	a.plays.mu.Lock()
	a.plays.byID[s.id] = s
	a.plays.mu.Unlock()
	a.log.InfoContext(ctx, "playback started", "session", s.id, "item", it.ID, "title", it.Title,
		"method", plan.Method, "copy_video", plan.CopyVideo, "copy_audio", plan.CopyAudio,
		"encoder", encoder.Name, "gpu", gpu, "decoder", decoderName(decoder), "tonemap", toneMapName(toneMap, gpu), "reasons", plan.Reasons, "segments", len(s.segs),
		"subtitles", len(subs), "subtitles_ready", subsReady, "subtitle", subtitleLog(chosen), "burn", plan.Burn)
	started := domain.T("activity.playback_started", "profile", s.who.profileName, "title", s.who.title, "device", s.who.device, []domain.Text{methodText(plan)})
	if music {
		started.Key = "activity.listening_started"
	}
	a.metrics.playbacks.Inc(string(plan.Method))
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityPlaybackStarted, AccountID: &p.Account.ID, ProfileID: idPtr(profile), ItemID: idPtr(it.ID),
		Text: started,
	})
	return PlayInfo{
		SessionID: s.id, Token: s.token, Method: plan.Method, File: *file, Audio: plan.Audio,
		Duration: file.Info.Duration, Resume: view.UserData.Position,
		Reasons: plan.Reasons, CopyVideo: plan.CopyVideo, CopyAudio: plan.CopyAudio, Encoder: encoder.Name, ToneMap: toneMapName(toneMap, gpu),
		GPU: gpu, Decoder: decoderName(decoder),
		Subtitles: subs, Fonts: fonts, SubtitlesReady: subsReady, Subtitle: subtitlePosition(chosen), Burned: burned,
	}, nil
}

// profileSubtitle is the subtitle a playback starts with by the profile's preferences, nil for
// none (playback.PickSubtitle). Its language is the one the profile names for subtitles, then the
// profile's, then the device's. Every language is brought to its ISO 639-2/B code: the file says
// "fra" or "fre", the profile "fr" or "fr-CA".
func profileSubtitle(profile domain.Profile, device string, info domain.MediaInfo, audio int, subs []domain.Subtitle) *domain.Subtitle {
	lang := ""
	for _, tag := range []string{profile.SubtitleLanguage, profile.Language, device} {
		if lang = naming.Language(tag); lang != "" {
			break
		}
	}
	audioLang := ""
	if s := playback.AudioStream(info, audio); s != nil {
		audioLang = naming.Language(s.Language)
	}
	candidates := make([]domain.Subtitle, len(subs))
	for i, s := range subs {
		s.Language = naming.Language(s.Language)
		candidates[i] = s
	}
	pos, ok := playback.PickSubtitle(candidates, profile.SubtitleMode, lang, audioLang)
	if !ok || pos >= len(subs) {
		return nil
	}
	return &subs[pos]
}

func subtitlePosition(s *domain.Subtitle) *int {
	if s == nil {
		return nil
	}
	return &s.Position
}

func subtitleLog(s *domain.Subtitle) int {
	if s == nil {
		return -1
	}
	return s.Position
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // never returns an error (crypto/rand)
	return hex.EncodeToString(b)
}

// playSessions holds the playbacks in progress.
type playSessions struct {
	mu   sync.Mutex
	byID map[domain.ID]*playSession
}

// session finds a playback by its ID and the secret of its URLs.
func (a *App) session(id domain.ID, token string) (*playSession, error) {
	a.plays.mu.Lock()
	s := a.plays.byID[id]
	a.plays.mu.Unlock()
	if s == nil || subtle.ConstantTimeCompare([]byte(s.token), []byte(token)) != 1 {
		return nil, domain.NotFound("playback.session_not_found")
	}
	return s, nil
}

// ownSession finds a playback of the caller's profile.
func (a *App) ownSession(p domain.Principal, id domain.ID) (*playSession, error) {
	v, err := viewerOf(p)
	if err != nil {
		return nil, err
	}
	profile := v.ProfileID
	a.plays.mu.Lock()
	s := a.plays.byID[id]
	a.plays.mu.Unlock()
	if s == nil || s.profile != profile {
		return nil, domain.NotFound("playback.session_not_found")
	}
	return s, nil
}

// convertWait caps the wait for an audio conversion (a 4 min song converts in 2 s, a one-hour
// recording in half a minute).
const convertWait = 5 * time.Minute

// DirectFile returns the file of a direct playback, or the converted file of a converted one
// (waiting for the conversion to finish).
func (a *App) DirectFile(ctx context.Context, id domain.ID, token string) (string, error) {
	s, err := a.session(id, token)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.lastSeen = a.now()
	s.mu.Unlock()
	switch {
	case s.plan.Method == playback.Direct:
		return s.file.Path, nil
	case s.conv != nil:
		return awaitConversion(ctx, s.conv, convertWait)
	}
	return "", domain.NotFound("playback.not_direct")
}

// Playlist returns the HLS playlist of a playback.
func (a *App) Playlist(id domain.ID, token string) (string, error) {
	s, err := a.session(id, token)
	if err != nil {
		return "", err
	}
	if !s.hls() {
		return "", domain.NotFound("playback.not_hls")
	}
	return playback.Playlist(s.segs, "init.mp4", func(i int) string { return strconv.Itoa(i) + ".m4s" }), nil
}

// hls reports an HLS playback (remux or transcode), the only kind that has segments.
func (s *playSession) hls() bool {
	return s.plan.Method == playback.Remux || s.plan.Method == playback.Transcode
}

// InitSegment returns the HLS init segment (ftyp + moov) of a playback, producing it if needed.
func (a *App) InitSegment(ctx context.Context, id domain.ID, token string) (string, error) {
	s, err := a.session(id, token)
	if err != nil {
		return "", err
	}
	if !s.hls() {
		return "", domain.NotFound("playback.not_hls")
	}
	path := filepath.Join(s.dir, "init.mp4")
	return path, a.await(ctx, s, func() (bool, error) {
		if s.init {
			return true, nil
		}
		if s.run == nil || s.run.done {
			a.startRun(ctx, s, s.wanted)
		}
		return false, nil
	})
}

// MediaSegment returns segment n of an HLS playback: right away if it is already there, otherwise
// when the current run reaches it, or after a new run if it is far away (seek).
func (a *App) MediaSegment(ctx context.Context, id domain.ID, token string, n int) (string, error) {
	s, err := a.session(id, token)
	if err != nil {
		return "", err
	}
	if n < 0 || n >= len(s.segs) {
		return "", domain.NotFound("playback.segment_not_found")
	}
	return s.segmentPath(n), a.await(ctx, s, func() (bool, error) {
		if s.wanted != n {
			s.wanted = n
			s.broadcast() // a run held back for being ahead may go on
		}
		if s.ready[n] {
			return true, nil
		}
		c := s.run
		if c != nil && c.done && c.err != nil && n >= c.start && n <= c.next {
			// The run failed: the error is returned and the next request will start another one.
			s.run = nil
			return false, c.err
		}
		if c != nil && n >= c.start && n < c.next {
			// The run went past n without producing it (no keyframe at its start, or the stream
			// ended before). A run that starts at n opens it with a keyframe. If that was already
			// the case, n does not exist in the stream: return an error rather than restart in a
			// loop.
			if c.start == n {
				return false, fmt.Errorf("playback: segment %d missing from the FFmpeg stream", n)
			}
			a.log.Warn("playback: segment skipped by the run, restarting at this segment", "session", s.id, "segment", n, "run", c.start)
			a.startRun(ctx, s, n)
			return false, nil
		}
		if c == nil || c.done || n < c.start || n > c.next+reachAhead {
			a.startRun(ctx, s, n)
		}
		return false, nil
	})
}

// await waits for check (called with s.mu held) to return true, checking again on every change.
func (a *App) await(ctx context.Context, s *playSession, check func() (bool, error)) error {
	timer := time.NewTimer(segmentWait)
	defer timer.Stop()
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return domain.NotFound("playback.session_not_found")
		}
		s.lastSeen = a.now()
		ok, err := check()
		ch := s.changed
		s.mu.Unlock()
		if ok || err != nil {
			return err
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New("playback: segment still not ready")
		}
	}
}

// startRun starts an FFmpeg run at segment k, stopping the previous one (s.mu held). The run
// outlives the request that asked for it (trigger): it runs in the background context, but its span
// is a child of the request's.
//
//nolint:contextcheck // the run outlives the request: background context, span child of trigger
func (a *App) startRun(trigger context.Context, s *playSession, k int) {
	if s.run != nil && !s.run.done {
		s.run.cancel()
	}
	parent := a.runCtx
	if parent == nil {
		parent = context.Background()
	}
	_, span := a.tracer.Start(trigger, "ffmpeg", telemetry.Internal,
		telemetry.Int("playback.segment", k), telemetry.String("playback.method", string(s.plan.Method)),
		telemetry.Bool("playback.copy_video", s.plan.CopyVideo), telemetry.String("playback.encoder", s.encoder.Name),
		telemetry.Bool("playback.gpu", s.gpu))
	ctx, cancel := context.WithCancel(telemetry.ContextWithSpan(parent, span))
	c := &ffmpegRun{start: k, next: k, cancel: cancel}
	s.run = c
	s.running.Add(1)
	a.background.Go(func() {
		defer s.running.Done()
		a.produceRun(ctx, s, c)
		s.mu.Lock()
		err := c.err
		s.mu.Unlock()
		if err != nil {
			span.Fail(err.Error())
		}
		span.End()
	})
}

// produceRun produces the segments of a run: FFmpeg's fragments go into the segment that the
// presentation time of their first frame points to.
func (a *App) produceRun(ctx context.Context, s *playSession, c *ffmpegRun) {
	cur := -1
	var buf []byte
	var seq uint32
	finish := func() error {
		if cur < 0 {
			return nil
		}
		s.mu.Lock()
		already := s.ready[cur]
		s.mu.Unlock()
		if !already {
			if err := writeAtomic(s.segmentPath(cur), buf); err != nil {
				return err
			}
		}
		s.mu.Lock()
		s.ready[cur] = true
		c.next = max(c.next, cur+1)
		s.broadcast()
		s.mu.Unlock()
		buf = nil
		return nil
	}
	produced := false // the run produced something (otherwise a fallback is possible)
	onInit := func(init []byte) error {
		produced = true
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.init {
			return nil
		}
		if err := writeAtomic(filepath.Join(s.dir, "init.mp4"), init); err != nil {
			return err
		}
		s.init = true
		s.broadcast()
		return nil
	}
	onFragment := func(f fmp4.Fragment) error {
		// The one millisecond margin absorbs index rounding (Matroska rounds to the ms).
		idx := playback.Find(s.segs, f.Start+time.Millisecond)
		if idx < c.start {
			return nil // FFmpeg starts from the index point before: fragments to throw away
		}
		if idx != cur {
			if err := finish(); err != nil {
				return err
			}
			// Skipped segments (no keyframe at their start): requests for them start a new run
			// (MediaSegment).
			s.mu.Lock()
			c.next = max(c.next, idx)
			s.broadcast()
			s.mu.Unlock()
			if err := a.holdRun(ctx, s, idx); err != nil {
				return err
			}
			cur, seq = idx, 0
		}
		seq++
		f.Renumber(uint32(cur)*1000 + seq) //nolint:gosec // segment index is bounded
		buf = append(buf, f.Data...)
		return nil
	}
	err := fmp4.Run(ctx, a.runCommand(s, c.start), onInit, onFragment)
	if err != nil && !produced && ctx.Err() == nil && a.dropGPU(s) {
		// The card failed before the first frame (driver, unusual stream): the run restarts on the
		// CPU and the playback stays there.
		a.log.Warn("playback: GPU pipeline failed, falling back to the CPU", "session", s.id, "err", err)
		err = fmp4.Run(ctx, a.runCommand(s, c.start), onInit, onFragment)
	}
	if err == nil && ctx.Err() == nil {
		err = finish() // end of file: the last segment is complete
	}
	s.mu.Lock()
	if err == nil && ctx.Err() == nil {
		c.next = len(s.segs) // end of stream: the remaining segments are not part of it
	}
	c.done = true
	if ctx.Err() == nil && err != nil {
		c.err = err
		a.log.Warn("playback: FFmpeg run failed", "session", s.id, "segment", c.start, "err", err)
	}
	s.broadcast()
	s.mu.Unlock()
}

// dropGPU gives up the card for the session, chain or decoder; false if it was not using it.
func (a *App) dropGPU(s *playSession) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	was := s.gpu || s.decoder != nil
	s.gpu, s.decoder = false, nil
	return was
}

// runCommand builds the FFmpeg command of a run starting at segment k.
func (a *App) runCommand(s *playSession, k int) fmp4.Command {
	var videoTag string
	channels := 2
	for _, st := range s.file.Info.Streams {
		if st.Index == s.plan.Video && st.Codec == "hevc" {
			videoTag = "hvc1" // required by Apple devices
		}
		if st.Index == s.plan.Audio {
			channels = st.Channels
		}
	}
	start := s.segs[k].Start
	if s.plan.Method == playback.Remux {
		return fmp4.Command{Bin: a.ffmpeg, Args: remux.Args(remux.Options{Path: s.file.Path, Start: start, Audio: s.plan.Audio, VideoTag: videoTag})}
	}
	s.mu.Lock()
	gpu, decoder := s.gpu, s.decoder
	s.mu.Unlock()
	cmd := fmp4.Command{Bin: a.ffmpeg, Args: transcode.Args(transcode.Options{
		Path: s.file.Path, Start: start, Audio: s.plan.Audio,
		CopyVideo: s.plan.CopyVideo, VideoTag: videoTag, Encoder: s.encoder, MaxHeight: maxTranscodeHeight,
		Segment: playback.TargetSegment, CopyAudio: s.plan.CopyAudio, Channels: channels, Burn: s.burn, ToneMap: s.toneMap,
		GPU: gpu, Decoder: decoder,
	})}
	if s.burn != nil && s.burn.Text != "" {
		cmd.Dir = s.dir // the subtitle and its fonts are copied there (relative paths)
	}
	return cmd
}

// decoderName is the name of the hardware decoder, empty for none.
func decoderName(d *transcode.Decoder) string {
	if d == nil {
		return ""
	}
	return d.Name
}

// toneMapName is the HDR to SDR conversion applied (libplacebo on the card).
func toneMapName(tm *transcode.ToneMapper, gpu bool) string {
	switch {
	case tm == nil:
		return ""
	case gpu:
		return "libplacebo"
	}
	return tm.Name
}

// burnWait caps the wait for the extraction of a subtitle to burn in.
const burnWait = 20 * time.Second

// prepareBurn prepares burning a subtitle in (last resort): an image subtitle is read from its
// extracted file; a text one is copied, with its fonts, into the playback's folder, where FFmpeg
// finds it through a relative path.
func (a *App) prepareBurn(ctx context.Context, s *playSession, position int) (*transcode.Burn, error) {
	set, err := a.awaitSubtitles(ctx, s.file, burnWait)
	if err != nil {
		return nil, err
	}
	if position >= len(set.Subtitles) {
		return nil, domain.NotFound("subtitle.not_found")
	}
	sub := set.Subtitles[position]
	var vw, vh int
	for _, st := range s.file.Info.Streams {
		if st.Index == s.plan.Video {
			vw, vh = st.Width, st.Height
		}
	}
	for _, format := range []string{subtitles.SUP, subtitles.MKS} {
		if slices.Contains(sub.Formats, format) {
			return &transcode.Burn{
				File: a.subtitlePath(s.file.ID, position, format), Width: sub.Width, Height: sub.Height,
				VideoWidth: vw, VideoHeight: vh,
			}, nil
		}
	}
	for _, format := range []string{subtitles.ASS, subtitles.VTT} {
		if !slices.Contains(sub.Formats, format) {
			continue
		}
		text := "burn." + format
		if err := copyFile(a.subtitlePath(s.file.ID, position, format), filepath.Join(s.dir, text)); err != nil {
			return nil, err
		}
		fonts := filepath.Join(s.dir, "fonts")
		if err := os.MkdirAll(fonts, 0o750); err != nil {
			return nil, err
		}
		for _, f := range set.Fonts {
			if err := copyFile(a.fontPath(f), filepath.Join(fonts, f.SHA256+f.Ext)); err != nil {
				a.log.WarnContext(ctx, "burn-in: missing font", "font", f.SHA256, "err", err)
			}
		}
		return &transcode.Burn{Text: text, FontsDir: "fonts"}, nil
	}
	return nil, domain.Precondition("subtitle.not_extracted")
}

// holdRun holds back a run that is too far ahead of the last segment requested.
func (a *App) holdRun(ctx context.Context, s *playSession, idx int) error {
	s.mu.Lock()
	for idx > s.wanted+aheadLimit && ctx.Err() == nil {
		ch := s.changed
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
		}
		s.mu.Lock()
	}
	s.mu.Unlock()
	return ctx.Err()
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReportProgress records the playback position reported by the player. It is saved at regular
// intervals, and on stop.
func (a *App) ReportProgress(p domain.Principal, id domain.ID, position time.Duration) error {
	s, err := a.ownSession(p, id)
	if err != nil {
		return err
	}
	if position < 0 {
		return domain.Invalid("request.negative_position")
	}
	s.mu.Lock()
	s.position, s.dirty, s.lastSeen = position, true, a.now()
	s.advance(position, a.now())
	s.mu.Unlock()
	return nil
}

// StopPlayback ends a playback: position saved, FFmpeg stopped, segments deleted.
func (a *App) StopPlayback(ctx context.Context, p domain.Principal, id domain.ID, position time.Duration) error {
	s, err := a.ownSession(p, id)
	if err != nil {
		return err
	}
	if position > 0 {
		s.mu.Lock()
		s.position, s.dirty = position, true
		s.advance(position, a.now())
		s.mu.Unlock()
	}
	a.closeSession(ctx, s)
	return nil
}

// saveSessionProgress saves the position if it changed. A finished playback is only counted once
// per session.
func (a *App) saveSessionProgress(ctx context.Context, s *playSession) {
	s.mu.Lock()
	if !s.dirty || s.finished {
		s.mu.Unlock()
		return
	}
	position := s.position
	s.dirty, s.savedAt = false, a.now()
	s.mu.Unlock()
	resume, finished := domain.Progress(position, s.file.Info.Duration)
	if s.music {
		resume = 0 // a track plays again from the start
	}
	if finished {
		s.mu.Lock()
		s.finished = true
		s.mu.Unlock()
	}
	err := a.store.Write(ctx, func(q store.Q) error {
		return q.SaveProgress(ctx, s.profile, s.item, resume, finished, a.now())
	})
	if err != nil {
		a.log.WarnContext(ctx, "playback: position not saved", "session", s.id, "err", err)
		return
	}
	a.userDataChanged(s.profile, []domain.ID{s.item})
}

// closeSession ends a session: position saved, run stopped, segments deleted.
func (a *App) closeSession(ctx context.Context, s *playSession) {
	a.saveSessionProgress(ctx, s)
	s.mu.Lock()
	position, finished := s.position, s.finished
	// Two closes may cross (stop requested, watchdog): only one play is recorded.
	play, record := s.play, !s.recorded
	s.recorded = true
	play.Watched, play.Position, play.Completed = s.watched, position, finished
	s.mu.Unlock()
	if record {
		play.EndedAt, play.Device = a.now(), s.who.device
		a.recordPlay(ctx, play)
	}
	stopped := domain.T("activity.playback_stopped", "profile", s.who.profileName, "title", s.who.title, "position_seconds", position)
	if finished {
		stopped = domain.T("activity.playback_finished", "profile", s.who.profileName, "title", s.who.title)
	}
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityPlaybackStopped, AccountID: idPtr(s.who.accountID), ProfileID: idPtr(s.profile), ItemID: idPtr(s.item),
		Text: stopped,
	})
	a.plays.mu.Lock()
	delete(a.plays.byID, s.id)
	a.plays.mu.Unlock()
	s.mu.Lock()
	s.closed = true
	if c := s.run; c != nil {
		c.cancel()
	}
	s.broadcast()
	s.mu.Unlock()
	// Stopped runs (FFmpeg killed) finish writing before their folder is deleted.
	if !awaitRuns(s, 5*time.Second) {
		a.log.WarnContext(ctx, "playback: run still active on close", "session", s.id)
	}
	if s.dir != "" {
		if err := os.RemoveAll(s.dir); err != nil {
			a.log.WarnContext(ctx, "playback: segments not removed", "dir", s.dir, "err", err)
		}
	}
	a.log.InfoContext(ctx, "playback ended", "session", s.id)
}

// awaitRuns waits for all the runs of a session to end, canceled ones included (limit at most);
// false if one is still running.
func awaitRuns(s *playSession, limit time.Duration) bool {
	done := make(chan struct{})
	go func() {
		s.running.Wait()
		close(done)
	}()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// watchPlayback is the playback watchdog: it stops runs nobody reads anymore, saves progress and
// closes abandoned sessions. On server shutdown every session is closed (position saved).
func (a *App) watchPlayback(ctx context.Context) {
	t := time.NewTicker(watchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			for _, s := range a.allSessions() {
				a.closeSession(context.WithoutCancel(ctx), s)
			}
			return
		case <-t.C:
			a.tickPlayback(ctx)
		}
	}
}

// tickPlayback is one round of the watchdog.
func (a *App) tickPlayback(ctx context.Context) {
	now := a.now()
	for _, s := range a.allSessions() {
		s.mu.Lock()
		idle := now.Sub(s.lastSeen)
		if s.run != nil && !s.run.done && idle > idleRun {
			s.run.cancel()
		}
		due := s.dirty && now.Sub(s.savedAt) >= progressEvery
		s.mu.Unlock()
		switch {
		case idle > idleSession:
			a.closeSession(ctx, s)
		case due:
			a.saveSessionProgress(ctx, s)
		}
	}
}

// transcodeIdle: with no request for this long a transcode no longer counts (playback paused or
// abandoned: its FFmpeg run has been stopped for 30 s).
const transcodeIdle = time.Minute

// activeTranscodes counts the active video transcodes: those with an FFmpeg run going, or whose
// device asked for a segment less than a minute ago. A paused playback does not keep its slot. It
// takes it back without a check, because a movie in progress is not cut off.
func (a *App) activeTranscodes() int {
	now := a.now()
	n := 0
	for _, s := range a.allSessions() {
		s.mu.Lock()
		heavy := s.plan.Method == playback.Transcode && !s.plan.CopyVideo
		if heavy && ((s.run != nil && !s.run.done) || now.Sub(s.lastSeen) < transcodeIdle) {
			n++
		}
		s.mu.Unlock()
	}
	return n
}

// transcodeLimit returns the cap on concurrent video transcodes: the setting, otherwise 4 with a
// hardware encoder and one per four cores without.
func (a *App) transcodeLimit() int {
	if n := a.Settings().MaxTranscodes; n > 0 {
		return n
	}
	if caps, err := a.encoders(); err == nil {
		if best, ok := caps.Best(); ok && best.Hardware {
			return 4
		}
	}
	return max(1, runtime.NumCPU()/4)
}

func (a *App) allSessions() []*playSession {
	a.plays.mu.Lock()
	defer a.plays.mu.Unlock()
	out := make([]*playSession, 0, len(a.plays.byID))
	for _, s := range a.plays.byID {
		out = append(out, s)
	}
	return out
}

// keyframesOf returns the keyframe index of a file: the stored one if it matches the fingerprint,
// otherwise computed right now (and stored).
func (a *App) keyframesOf(ctx context.Context, f domain.MediaFile) ([]time.Duration, error) {
	keys, ok, err := a.store.Read().Keyframes(ctx, f.ID, f.Fingerprint)
	if err != nil || ok {
		return keys, err
	}
	return a.indexKeyframes(ctx, f)
}

func (a *App) indexKeyframes(ctx context.Context, f domain.MediaFile) ([]time.Duration, error) {
	start := time.Now()
	keys, err := keyframes.Read(ctx, a.ffprobe, f.Path)
	if err != nil {
		return nil, fmt.Errorf("keyframe index of %s: %w", filepath.Base(f.Path), err)
	}
	if err := a.store.Write(ctx, func(q store.Q) error {
		return q.SetKeyframes(ctx, f.ID, f.Fingerprint, keys, a.now())
	}); err != nil {
		return nil, err
	}
	a.log.DebugContext(ctx, "keyframe index", "path", f.Path, "keyframes", len(keys), "duration", time.Since(start).Round(time.Millisecond))
	return keys, nil
}

// indexFile is the file.keyframes job: the index is ready before the first playback.
func (a *App) indexFile(ctx context.Context, target string) error {
	id, err := domain.ParseID(target)
	if err != nil {
		return jobs.Permanent(err)
	}
	f, err := a.store.Read().File(ctx, id)
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if f.MissingSince != nil {
		return nil
	}
	if _, ok, err := a.store.Read().Keyframes(ctx, f.ID, f.Fingerprint); err != nil || ok {
		return err
	}
	_, err = a.indexKeyframes(ctx, f)
	return err
}
