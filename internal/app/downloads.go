package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/jobs"
	"github.com/laterna-project/laterna/internal/media/transcode"
	"github.com/laterna-project/laterna/internal/playback"
	"github.com/laterna-project/laterna/internal/proc"
	"github.com/laterna-project/laterna/internal/store"
)

// Offline downloads: a device asks for movies, episodes or tracks to play without a network. The
// server hands over the file as it is when it can. Otherwise it prepares, in the background, an MP4
// (an M4A for music) that the device can play, and serves it until the device has fetched it. When
// the network is back, the device reports what it played.

const (
	// downloadKeep: a download that is ready (or failed) is forgotten after this long and its
	// prepared copy deleted. The device has fetched it, or will ask again.
	downloadKeep = 7 * 24 * time.Hour
	// maxSessionDownloads caps the downloads of one device.
	maxSessionDownloads = 1000
	// maxOfflinePlays caps one report of offline plays. offlinePlaysKeep is how long a replayed
	// report is recognized.
	maxOfflinePlays  = 1000
	offlinePlaysKeep = 90 * 24 * time.Hour
	// downloadProgressEvery is the time between two progress events of a preparation.
	downloadProgressEvery = 2 * time.Second
)

// DownloadRequest asks for downloads for the caller's device.
type DownloadRequest struct {
	// ItemIDs are movies, episodes and tracks. A season, a series, an album or an artist stands for
	// its episodes or tracks, as in a playlist.
	ItemIDs []domain.ID
	// FileID picks a version (when a single item is requested); nil means the first present one.
	FileID  *domain.ID
	Quality domain.DownloadQuality
	// Audio is the index of the wanted audio stream; negative for the default one.
	Audio  int
	Device playback.DeviceProfile
}

// DownloadView is a download as the device sees it: the item, the preparation decision, and the
// subtitles (in the formats the device renders) to fetch with it.
type DownloadView struct {
	Download  domain.Download
	Item      domain.ItemView
	Plan      playback.DownloadPlan
	Subtitles []domain.Subtitle
	Fonts     []domain.Font
	// FileName is the suggested name to save it under. ExpiresAt is when the server will forget it
	// (nil until it is ready or failed).
	FileName  string
	ExpiresAt *time.Time
}

// downloadPlan is stored with the download: what the preparation must do, and the device's subtitle
// formats.
type downloadPlan struct {
	playback.DownloadPlan
	SubtitleFormats []string `json:"subtitleFormats,omitempty"`
}

// preparations holds the preparations in progress. Removing a download stops its preparation.
type preparations struct {
	mu     sync.Mutex
	byID   map[domain.ID]context.CancelFunc
	lastAt map[domain.ID]time.Time
}

// downloadDir is the folder of the prepared copy of a download.
func (a *App) downloadDir(id domain.ID) string {
	return filepath.Join(a.cacheDir, "downloads", id.String())
}

// CreateDownloads asks for downloads for the caller's device. What the device already plays and
// that fits the requested quality is ready right away. The rest is prepared in the background, one
// file at a time. An item already requested at the same quality is not requested twice.
func (a *App) CreateDownloads(ctx context.Context, p domain.Principal, req DownloadRequest) ([]DownloadView, error) {
	v, err := viewerOf(p)
	if err != nil {
		return nil, err
	}
	if !p.CanDownload() {
		return nil, domain.Forbidden("download.not_allowed")
	}
	q := req.Quality
	if q == "" {
		q = domain.DownloadOriginal
	}
	if !q.Valid() {
		return nil, domain.Invalid("download.unknown_quality")
	}
	if len(req.ItemIDs) == 0 {
		return nil, domain.Invalid("download.nothing_to_download")
	}
	ids, err := a.playable(ctx, v, req.ItemIDs)
	if err != nil {
		return nil, err
	}
	if req.FileID != nil && len(ids) != 1 {
		return nil, domain.Invalid("download.version_needs_single_item")
	}
	read := a.store.Read()
	existing, err := read.SessionDownloads(ctx, p.SessionID, v.ProfileID)
	if err != nil {
		return nil, err
	}
	key := func(item, file domain.ID, q domain.DownloadQuality) string {
		return item.String() + "|" + file.String() + "|" + string(q)
	}
	have := map[string]domain.ID{}
	for _, d := range existing {
		if d.State != domain.DownloadFailed {
			have[key(d.ItemID, d.FileID, d.Quality)] = d.ID
		}
	}
	count, err := read.CountSessionDownloads(ctx, p.SessionID)
	if err != nil {
		return nil, err
	}
	if int(count)+len(ids) > maxSessionDownloads {
		return nil, domain.Precondition("download.too_many", "max", maxSessionDownloads)
	}

	now := a.now()
	var created []domain.Download
	plans := map[domain.ID]string{}
	kinds := map[domain.ID]string{}
	out := make([]domain.ID, 0, len(ids))
	for _, id := range ids {
		it, err := read.Item(ctx, id)
		if err != nil {
			return nil, err
		}
		files, err := read.ItemFiles(ctx, id)
		if err != nil {
			return nil, err
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
			return nil, domain.NotFound("download.no_readable_file", "title", it.Title)
		}
		if known, ok := have[key(id, file.ID, q)]; ok {
			out = append(out, known)
			continue
		}
		plan := playback.DecideDownload(file.Info, file.Size, req.Device, req.Audio, q)
		if plan.Method == playback.Unplayable {
			return nil, domain.Precondition("download.unplayable", "title", it.Title, plan.Reasons)
		}
		raw, err := json.Marshal(downloadPlan{DownloadPlan: plan, SubtitleFormats: req.Device.SubtitleFormats})
		if err != nil {
			return nil, err
		}
		d := domain.Download{
			ID: domain.NewID(), AccountID: p.Account.ID, ProfileID: v.ProfileID, SessionID: p.SessionID,
			ItemID: id, FileID: file.ID, Quality: q, State: domain.DownloadQueued, Estimate: plan.Estimate,
			CreatedAt: now, UpdatedAt: now,
		}
		if plan.Method == playback.Direct {
			d.State, d.Progress, d.Size, d.Path, d.ReadyAt = domain.DownloadReady, 1, file.Size, file.Path, &now
		}
		created = append(created, d)
		plans[d.ID] = string(raw)
		kinds[d.ID] = jobPrepareDownload
		if plan.Method == playback.Convert {
			kinds[d.ID] = jobConvertDownload
		}
		have[key(id, file.ID, q)] = d.ID
		out = append(out, d.ID)
	}
	err = a.store.Write(ctx, func(q store.Q) error {
		for _, d := range created {
			if err := q.CreateDownload(ctx, d, plans[d.ID]); err != nil {
				return err
			}
			if d.State == domain.DownloadQueued {
				if err := a.jobs.Enqueue(ctx, q, kinds[d.ID], d.ID.String(), priorityUser); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(created) > 0 {
		a.jobs.Kick()
		newIDs := make([]domain.ID, len(created))
		for i, d := range created {
			newIDs[i] = d.ID
		}
		a.downloadsChanged(p.SessionID, newIDs...)
		a.log.InfoContext(ctx, "downloads requested", "count", len(created), "quality", q, "device", p.SessionID)
	}
	return a.downloadViews(ctx, p, v, out)
}

// ListDownloads lists the downloads of the caller's device, for their profile.
func (a *App) ListDownloads(ctx context.Context, p domain.Principal) ([]DownloadView, error) {
	v, err := viewerOf(p)
	if err != nil {
		return nil, err
	}
	list, err := a.store.Read().SessionDownloads(ctx, p.SessionID, v.ProfileID)
	if err != nil {
		return nil, err
	}
	ids := make([]domain.ID, len(list))
	for i, d := range list {
		ids[i] = d.ID
	}
	return a.downloadViews(ctx, p, v, ids)
}

// GetDownload returns a download of the caller's device.
func (a *App) GetDownload(ctx context.Context, p domain.Principal, id domain.ID) (DownloadView, error) {
	v, err := viewerOf(p)
	if err != nil {
		return DownloadView{}, err
	}
	views, err := a.downloadViews(ctx, p, v, []domain.ID{id})
	if err != nil {
		return DownloadView{}, err
	}
	if len(views) == 0 {
		return DownloadView{}, domain.NotFound("download.not_found")
	}
	return views[0], nil
}

// downloadViews reads downloads of the caller's device, in the given order. Those of another
// device, or whose item is no longer visible, are left out.
func (a *App) downloadViews(ctx context.Context, p domain.Principal, v domain.Viewer, ids []domain.ID) ([]DownloadView, error) {
	read := a.store.Read()
	out := make([]DownloadView, 0, len(ids))
	for _, id := range ids {
		d, raw, err := read.Download(ctx, id)
		if store.IsNotFound(err) || (err == nil && !ownDownload(d, p, v)) {
			continue
		}
		if err != nil {
			return nil, err
		}
		view, err := read.View(ctx, v, d.ItemID)
		if store.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var plan downloadPlan
		if err := json.Unmarshal([]byte(raw), &plan); err != nil {
			return nil, err
		}
		dv := DownloadView{Download: d, Item: view, Plan: plan.DownloadPlan}
		ext := filepath.Ext(d.Path)
		switch {
		case ext != "":
		case plan.Method == playback.Convert:
			ext = ".m4a"
		default:
			ext = ".mp4"
		}
		dv.FileName = downloadName(view, ext)
		switch d.State {
		case domain.DownloadReady:
			if d.ReadyAt != nil {
				t := d.ReadyAt.Add(downloadKeep)
				dv.ExpiresAt = &t
			}
		case domain.DownloadFailed:
			t := d.UpdatedAt.Add(downloadKeep)
			dv.ExpiresAt = &t
		case domain.DownloadQueued, domain.DownloadPreparing:
		}
		if view.Item.Kind != domain.ItemTrack {
			file, err := read.File(ctx, d.FileID)
			if err != nil {
				return nil, err
			}
			subs, fonts, ready, err := a.subtitlesOf(ctx, file)
			if err != nil {
				return nil, err
			}
			if ready {
				dv.Subtitles, dv.Fonts = shownSubtitles(subs, plan.SubtitleFormats), fonts
			}
		}
		out = append(out, dv)
	}
	return out, nil
}

// shownSubtitles keeps the subtitles a device renders, in its formats only. Position stays the one
// in the file (it is used in subtitle URLs).
func shownSubtitles(subs []domain.Subtitle, formats []string) []domain.Subtitle {
	var out []domain.Subtitle
	for _, s := range subs {
		var keep []string
		for _, f := range s.Formats {
			if slices.Contains(formats, f) {
				keep = append(keep, f)
			}
		}
		if len(keep) > 0 {
			s.Formats = keep
			out = append(out, s)
		}
	}
	return out
}

// ownDownload reports whether the download belongs to the caller's device and profile.
func ownDownload(d domain.Download, p domain.Principal, v domain.Viewer) bool {
	return d.SessionID == p.SessionID && d.ProfileID == v.ProfileID
}

// DeleteDownloads removes downloads from the caller's device: a preparation in progress stops and
// the prepared copy is deleted. An unknown download is ignored.
func (a *App) DeleteDownloads(ctx context.Context, p domain.Principal, ids []domain.ID) error {
	v, err := viewerOf(p)
	if err != nil {
		return err
	}
	var gone []domain.ID
	err = a.store.Write(ctx, func(q store.Q) error {
		for _, id := range ids {
			d, _, err := q.Download(ctx, id)
			if store.IsNotFound(err) || (err == nil && !ownDownload(d, p, v)) {
				continue
			}
			if err != nil {
				return err
			}
			if err := q.DeleteDownload(ctx, id); err != nil {
				return err
			}
			gone = append(gone, id)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, id := range gone {
		// A stopped preparation deletes its own copy; otherwise the purge takes care of it.
		if !a.cancelPreparation(id) {
			_ = os.RemoveAll(a.downloadDir(id))
		}
	}
	if len(gone) > 0 {
		a.downloadsChanged(p.SessionID, gone...)
	}
	return nil
}

// DownloadFile returns the file of a ready download of the caller's device, and the name to save it
// under.
func (a *App) DownloadFile(ctx context.Context, p domain.Principal, id domain.ID) (path, name string, err error) {
	v, err := viewerOf(p)
	if err != nil {
		return "", "", err
	}
	read := a.store.Read()
	d, _, err := read.Download(ctx, id)
	if store.IsNotFound(err) || (err == nil && !ownDownload(d, p, v)) {
		return "", "", domain.NotFound("download.not_found")
	}
	if err != nil {
		return "", "", err
	}
	if d.State != domain.DownloadReady {
		return "", "", domain.NotFound("download.not_ready")
	}
	view, err := read.View(ctx, v, d.ItemID)
	if store.IsNotFound(err) {
		return "", "", domain.NotFound("download.not_found")
	}
	if err != nil {
		return "", "", err
	}
	return d.Path, downloadName(view, filepath.Ext(d.Path)), nil
}

// DownloadSubtitle returns a subtitle of a download of the caller's device, in an extracted format
// (waiting for the extraction if it is under way).
func (a *App) DownloadSubtitle(ctx context.Context, p domain.Principal, id domain.ID, position int, format string) (string, error) {
	v, err := viewerOf(p)
	if err != nil {
		return "", err
	}
	read := a.store.Read()
	d, _, err := read.Download(ctx, id)
	if store.IsNotFound(err) || (err == nil && !ownDownload(d, p, v)) {
		return "", domain.NotFound("download.not_found")
	}
	if err != nil {
		return "", err
	}
	file, err := read.File(ctx, d.FileID)
	if err != nil {
		return "", err
	}
	set, err := a.awaitSubtitles(ctx, file, subtitleWait)
	if err != nil {
		return "", err
	}
	if position < 0 || position >= len(set.Subtitles) || !slices.Contains(set.Subtitles[position].Formats, format) {
		return "", domain.NotFound("subtitle.not_found")
	}
	return a.subtitlePath(file.ID, position, format), nil
}

// downloadName is the name to save a download under: "Movie (2020).mp4", "Show - S01E02 -
// Title.mp4", "04 - Title.m4a", without characters that are not allowed in file names.
func downloadName(v domain.ItemView, ext string) string {
	it := v.Item
	name := it.Title
	switch {
	case v.Episode != nil && v.SeriesTitle != "":
		name = fmt.Sprintf("%s - S%02dE%02d - %s", v.SeriesTitle, v.Episode.SeasonNumber, v.Episode.Number, it.Title)
	case v.Track != nil && v.Track.Number > 0:
		name = fmt.Sprintf("%02d - %s", v.Track.Number, it.Title)
	case it.Kind == domain.ItemMovie && it.Year > 0:
		name = fmt.Sprintf("%s (%d)", it.Title, it.Year)
	}
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if name == "" {
		name = "laterna"
	}
	return name + ext
}

// OfflinePlay is a playback that happened offline, reported when the network is back.
type OfflinePlay struct {
	ItemID   domain.ID
	Position time.Duration
	// Finished means played to the end (otherwise it is worked out from the position and the resume
	// thresholds).
	Finished bool
	// At is when playback stopped (zero or in the future means now).
	At time.Time
}

// SyncOfflinePlayback applies what a device played offline. A finished playback always counts; a
// resume point only replaces what the profile did earlier, elsewhere. An item that is gone or no
// longer visible is ignored, and replaying the same report does not count twice. Each playback goes
// into the history, with the position reached as time watched (the whole runtime if it was
// finished).
func (a *App) SyncOfflinePlayback(ctx context.Context, p domain.Principal, plays []OfflinePlay) error {
	v, err := viewerOf(p)
	if err != nil {
		return err
	}
	if len(plays) > maxOfflinePlays {
		return domain.Invalid("download.too_many_plays", "max", maxOfflinePlays)
	}
	now := a.now()
	device := "unknown device"
	if sess, err := a.store.Read().Session(ctx, p.SessionID); err == nil {
		device = sess.Device.Name
	}
	var ids []domain.ID
	err = a.store.Write(ctx, func(q store.Q) error {
		for _, pl := range plays {
			if pl.Position < 0 {
				return domain.Invalid("request.negative_position")
			}
			view, err := q.View(ctx, v, pl.ItemID)
			if store.IsNotFound(err) {
				continue
			}
			if err != nil {
				return err
			}
			kind := view.Item.Kind
			if kind != domain.ItemMovie && kind != domain.ItemEpisode && kind != domain.ItemTrack {
				continue
			}
			at := pl.At
			if at.IsZero() || at.After(now) {
				at = now
			}
			resume, finished := domain.Progress(pl.Position, view.Item.Runtime)
			if pl.Finished {
				resume, finished = 0, true
			}
			if kind == domain.ItemTrack {
				resume = 0
			}
			applied, err := q.SaveOfflineProgress(ctx, v.ProfileID, pl.ItemID, resume, finished, at, now)
			if err != nil {
				return err
			}
			if !applied {
				continue
			}
			ids = append(ids, pl.ItemID)
			watched := min(pl.Position, view.Item.Runtime)
			if pl.Finished || finished || view.Item.Runtime == 0 {
				watched = max(pl.Position, view.Item.Runtime)
			}
			play := playOf(v.ProfileID, view, view.Item.Runtime, at.Add(-watched))
			play.EndedAt, play.Watched, play.Position, play.Completed = at, watched, pl.Position, finished
			play.Device, play.Offline = device, true
			if domain.CountsAsPlay(play.Kind, play.Watched, play.Duration) {
				if err := q.AddPlay(ctx, play); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err == nil && len(ids) > 0 {
		a.userDataChanged(v.ProfileID, ids)
	}
	return err
}

// Preparation.

// prepareDownload prepares the copy of a download: an MP4, re-encoded or not ("download.prepare"
// job, one file at a time), or an audio conversion ("download.convert", kept apart so it never
// waits behind a movie).
func (a *App) prepareDownload(ctx context.Context, target string) error {
	id, err := domain.ParseID(target)
	if err != nil {
		return jobs.Permanent(err)
	}
	read := a.store.Read()
	d, raw, err := read.Download(ctx, id)
	if store.IsNotFound(err) {
		return nil // removed in the meantime
	}
	if err != nil {
		return err
	}
	if d.State != domain.DownloadQueued && d.State != domain.DownloadPreparing {
		return nil
	}
	var plan downloadPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return jobs.Permanent(err)
	}
	f, err := read.File(ctx, d.FileID)
	if err != nil && !store.IsNotFound(err) {
		return err
	}
	if err != nil || f.MissingSince != nil {
		return a.failDownload(ctx, d, errors.New("file is gone"))
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.preps.mu.Lock()
	a.preps.byID[id] = cancel
	a.preps.mu.Unlock()
	defer func() {
		a.preps.mu.Lock()
		delete(a.preps.byID, id)
		delete(a.preps.lastAt, id)
		a.preps.mu.Unlock()
	}()

	start := a.now()
	d.State, d.Progress, d.UpdatedAt = domain.DownloadPreparing, 0, start
	if err := a.store.Write(ctx, func(q store.Q) error { return q.SetDownloadState(ctx, d) }); err != nil {
		return err
	}
	a.downloadsChanged(d.SessionID, d.ID)

	var path string
	switch plan.Method {
	case playback.Convert:
		c := a.convertAudio(f, plan.Audio, streamChannels(f, plan.Audio), plan.AudioRate) //nolint:contextcheck // conversion shared with playback
		path, err = awaitConversion(ctx, c, 12*time.Hour)
	case playback.Remux, playback.Transcode:
		path, err = a.prepareVideo(ctx, d, f, plan.DownloadPlan)
	case playback.Direct, playback.Unplayable:
		err = jobs.Permanent(fmt.Errorf("nothing to prepare (%s)", plan.Method))
	}
	if err != nil {
		if _, _, gone := a.store.Read().Download(context.WithoutCancel(ctx), id); store.IsNotFound(gone) {
			_ = os.RemoveAll(a.downloadDir(id)) // removed during the preparation
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err() // server shutdown: resumed at the next start
		}
		return a.failDownload(ctx, d, err)
	}
	st, err := os.Stat(path)
	if err != nil {
		return a.failDownload(ctx, d, err)
	}
	now := a.now()
	d.State, d.Progress, d.Size, d.Path, d.ReadyAt, d.UpdatedAt = domain.DownloadReady, 1, st.Size(), path, &now, now
	if err := a.store.Write(ctx, func(q store.Q) error { return q.SetDownloadState(ctx, d) }); err != nil {
		return err
	}
	a.downloadsChanged(d.SessionID, d.ID)
	a.log.InfoContext(ctx, "download ready", "download", d.ID, "path", f.Path, "method", plan.Method,
		"size", st.Size(), "estimate", d.Estimate, "duration", now.Sub(start).Round(time.Second))
	return nil
}

// failDownload records that a preparation failed (for good: the device can ask again).
func (a *App) failDownload(ctx context.Context, d domain.Download, cause error) error {
	msg := strings.TrimSpace(cause.Error())
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	d.State, d.Error, d.UpdatedAt = domain.DownloadFailed, strings.ToValidUTF8(msg, "�"), a.now()
	ctx = context.WithoutCancel(ctx)
	if err := a.store.Write(ctx, func(q store.Q) error { return q.SetDownloadState(ctx, d) }); err != nil {
		return err
	}
	_ = os.RemoveAll(a.downloadDir(d.ID))
	a.downloadsChanged(d.SessionID, d.ID)
	a.log.WarnContext(ctx, "cannot prepare download", "download", d.ID, "err", cause)
	return jobs.Permanent(cause)
}

// prepareVideo makes the MP4 of a download. A preparation that fails on the GPU is restarted on the
// CPU.
func (a *App) prepareVideo(ctx context.Context, d domain.Download, f domain.MediaFile, plan playback.DownloadPlan) (string, error) {
	dir := a.downloadDir(d.ID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	out := filepath.Join(dir, "file.mp4")
	tmp := out + ".part"
	o := transcode.FileOptions{
		Path: f.Path, Output: tmp, Audio: plan.Audio, CopyVideo: plan.CopyVideo, CopyAudio: plan.CopyAudio,
		MaxHeight: plan.MaxHeight, VideoRate: plan.VideoRate, AudioRate: plan.AudioRate, Channels: streamChannels(f, plan.Audio),
	}
	for _, st := range f.Info.Streams {
		if st.Index == plan.Video && st.Codec == "hevc" {
			o.VideoTag = "hvc1" // required by Apple devices
		}
	}
	var caps transcode.Caps
	if !plan.CopyVideo {
		var err error
		caps, err = a.encoders()
		best, ok := caps.Best()
		if !ok {
			return "", jobs.Permanent(fmt.Errorf("no usable H.264 encoder: %w", err))
		}
		o.Encoder, o.GPU = best, caps.GPU
		if plan.ToneMap {
			tm, ok := caps.BestToneMapper()
			if !ok && !o.GPU {
				return "", jobs.Permanent(errors.New("no usable HDR to SDR conversion on this server"))
			}
			o.ToneMap = &tm // on the card libplacebo does it: all that matters is that a conversion is needed
		}
	}
	err := a.runFile(ctx, d, f, o)
	if err != nil && o.GPU && ctx.Err() == nil {
		a.log.WarnContext(ctx, "download: GPU failed, falling back to the CPU", "download", d.ID, "err", err)
		o.GPU = false
		if plan.ToneMap {
			tm, ok := caps.BestToneMapper()
			if !ok {
				return "", jobs.Permanent(errors.New("no HDR to SDR conversion on the CPU"))
			}
			o.ToneMap = &tm
		}
		err = a.runFile(ctx, d, f, o)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return out, os.Rename(tmp, out)
}

// runFile runs FFmpeg and follows its progress ("-progress" on standard output).
func (a *App) runFile(ctx context.Context, d domain.Download, f domain.MediaFile, o transcode.FileOptions) error {
	bin := a.ffmpeg
	if bin == "" {
		bin = "ffmpeg"
	}
	cmd := proc.Command(ctx, bin, transcode.FileArgs(o)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	total := f.Info.Duration.Microseconds()
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		v, ok := strings.CutPrefix(sc.Text(), "out_time_us=")
		us, convErr := strconv.ParseInt(v, 10, 64)
		if !ok || convErr != nil || total <= 0 {
			continue
		}
		a.downloadProgress(ctx, d, min(0.99, max(0, float64(us)/float64(total))))
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// downloadProgress stores and announces the progress of a preparation, at most every two seconds.
func (a *App) downloadProgress(ctx context.Context, d domain.Download, progress float64) {
	a.preps.mu.Lock()
	if last, ok := a.preps.lastAt[d.ID]; ok && time.Since(last) < downloadProgressEvery {
		a.preps.mu.Unlock()
		return
	}
	a.preps.lastAt[d.ID] = time.Now()
	a.preps.mu.Unlock()
	if err := a.store.Write(ctx, func(q store.Q) error { return q.SetDownloadProgress(ctx, d.ID, progress, a.now()) }); err != nil {
		return
	}
	a.downloadsChanged(d.SessionID, d.ID)
}

// cancelPreparation stops the preparation of a download if one is in progress; false if there is
// none.
func (a *App) cancelPreparation(id domain.ID) bool {
	a.preps.mu.Lock()
	defer a.preps.mu.Unlock()
	cancel, ok := a.preps.byID[id]
	if ok {
		cancel()
	}
	return ok
}

// streamChannels returns the channel count of an audio stream of a file (2 if unknown).
func streamChannels(f domain.MediaFile, index int) int {
	for _, st := range f.Info.Streams {
		if st.Index == index && st.Channels > 0 {
			return st.Channels
		}
	}
	return 2
}

// downloadsChanged tells a device that some of its downloads changed.
func (a *App) downloadsChanged(sessionID domain.ID, ids ...domain.ID) {
	a.bus.Publish(domain.DownloadsChanged{SessionID: sessionID, DownloadIDs: slices.Clone(ids)})
}

// purgeDownloads forgets the downloads that have been ready or failed for downloadKeep and the
// records of offline plays older than 90 days, and deletes the prepared copies that are no longer
// needed (recent folders are spared: a preparation may be under way).
func (a *App) purgeDownloads(ctx context.Context) {
	var forgotten int64
	if err := a.store.Write(ctx, func(q store.Q) error {
		var err error
		if forgotten, err = q.DeleteOldDownloads(ctx, a.now().Add(-downloadKeep)); err != nil {
			return err
		}
		return q.DeleteOfflinePlaysBefore(ctx, a.now().Add(-offlinePlaysKeep))
	}); err != nil {
		a.log.WarnContext(ctx, "cannot purge downloads", "err", err)
		return
	}
	ids, err := a.store.Read().DownloadIDs(ctx)
	if err != nil {
		a.log.WarnContext(ctx, "cannot purge downloads", "err", err)
		return
	}
	known := make(map[string]bool, len(ids))
	for _, id := range ids {
		known[id.String()] = true
	}
	root, err := os.OpenRoot(a.cacheDir)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		a.log.WarnContext(ctx, "downloads purge: unreadable cache", "err", err)
		return
	}
	defer func() { _ = root.Close() }()
	entries, err := fs.ReadDir(root.FS(), "downloads")
	if err != nil {
		return // no prepared download
	}
	cutoff := time.Now().Add(-time.Hour)
	removed := 0
	for _, e := range entries {
		info, err := e.Info()
		if !e.IsDir() || known[e.Name()] || err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if root.RemoveAll(filepath.Join("downloads", e.Name())) == nil {
			removed++
		}
	}
	if forgotten > 0 || removed > 0 {
		a.log.InfoContext(ctx, "downloads: purge", "forgotten", forgotten, "files", removed)
	}
}
