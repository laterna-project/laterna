package app

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/jobs"
	"github.com/laterna-project/laterna/internal/store"
)

// Job kinds and resource classes.
const (
	jobScanLibrary  = "library.scan"
	jobAnalyzeFile  = "file.analyze"
	jobItemMetadata = "item.metadata"
	jobAnalyzeImage = "image.analyze"
	jobIndexFile    = "file.keyframes"
	// Images that only an NFO gives a URL for, and the purge of those no longer used.
	jobDownloadImage = "image.download"
	jobPurgeMetadata = "metadata.purge"
	// Refresh asked of Sonarr or Radarr, followed by a scan.
	jobArrRefresh = "arr.refresh"
	// Scrubbing thumbnails of a video file.
	jobTrickplay = "file.trickplay"
	// Subtitles and fonts, and the purge of what is no longer used.
	jobExtractSubtitles = "file.subtitles"
	jobPurgeSubtitles   = "subtitles.purge"
	// Preparation of an offline download: a video file, or music to convert (kept apart: a few
	// seconds each, never stuck behind a movie).
	jobPrepareDownload = "download.prepare"
	jobConvertDownload = "download.convert"
	// SQLite planner statistics, after the analyses a scan triggered.
	jobOptimizeStore = "store.optimize"
	// jobBackupStore is the nightly database backup and rotation.
	jobBackupStore = "store.backup"
	// Intro and credits of a file: chapters, then audio compared with the season's other episodes.
	jobDetectSegments = "file.segments"
	// Requests (jobSubmitRequest, jobRefreshRequests) are in requests.go, the calendars
	// (jobRefreshUpcoming) in upcoming.go.

	// One scan at a time: it walks folders, often over the network.
	classScan = "scan"
	// File reads (ffprobe, NFO, images): two at a time go easy on an SMB share.
	classIO = "io"
	// CPU work (images).
	classCPU = "cpu"
	// Network: image downloads, paced per site.
	classNet = "net"
	// Keyframe index: instant with Matroska Cues, but a file without an index is read in full
	// (several GB). Kept apart so that it never holds up file analysis.
	classIndex = "index"
	// Subtitle extraction reads each file in full (subtitles are interleaved with the video). One
	// file at a time, kept apart so that it holds up neither analysis nor indexing.
	classExtract = "extract"
	// Sonarr and Radarr refreshes: mostly waiting, kept apart so that they hold nothing up.
	classArr = "arr"
	// Scrubbing thumbnails: decoding the keyframes of the whole file. One at a time, kept apart so
	// that it never holds up the rest.
	classTrickplay = "trickplay"
	// Download preparation: a whole file re-encoded. One at a time, kept apart.
	classPrepare = "prepare"
	// Music conversions for downloads: short, two at a time.
	classConvert = "convert"
	// Intro and credits: several minutes of audio to decode for each episode. One at a time, kept
	// apart.
	classSegments = "segments"
	// Requests handed to Sonarr and Radarr and followed: short calls, kept apart from refreshes,
	// which wait for the end of a command.
	classRequests = "requests"

	// Priorities: what the user asks for goes before background work. priorityIdle goes after every
	// waiting job of its class.
	priorityUser       = 10
	priorityBackground = 0
	priorityIdle       = -1
)

func (a *App) registerJobs() {
	a.jobs.OnFailure(a.jobFailed)
	a.jobs.OnRun(a.traceJob)
	a.jobs.Class(classScan, 1)
	a.jobs.Class(classIO, 2)
	a.jobs.Class(classCPU, max(1, runtime.NumCPU()/2))
	a.jobs.Class(classNet, 2)
	a.jobs.Class(classIndex, 1)
	a.jobs.Class(classExtract, 1)
	a.jobs.Class(classArr, 1)
	a.jobs.Class(classTrickplay, 1)
	a.jobs.Class(classPrepare, 1)
	a.jobs.Class(classConvert, 2)
	a.jobs.Class(classSegments, 1)
	a.jobs.Class(classRequests, 1)
	a.jobs.Class(classPush, 2)
	a.jobs.Register(jobScanLibrary, classScan, a.scanLibrary, jobs.Timeout(2*time.Hour))
	// In the analysis class, at idle priority: after the analyses a scan just asked for, which
	// create the items.
	a.jobs.Register(jobOptimizeStore, classIO, func(ctx context.Context, _ string) error { return a.store.Optimize(ctx) },
		jobs.Timeout(10*time.Minute))
	a.jobs.Register(jobAnalyzeFile, classIO, a.analyzeFile, jobs.Timeout(5*time.Minute))
	a.jobs.Register(jobItemMetadata, classIO, a.refreshMetadata, jobs.Timeout(5*time.Minute))
	a.jobs.Register(jobAnalyzeImage, classCPU, a.analyzeImage, jobs.Timeout(time.Minute))
	a.jobs.Register(jobDownloadImage, classNet, a.downloadImage, jobs.Timeout(5*time.Minute))
	a.jobs.Register(jobPurgeMetadata, classIO, a.purgeMetadata, jobs.Timeout(10*time.Minute))
	a.jobs.Register(jobBackupStore, classIO, a.autoBackup, jobs.Timeout(30*time.Minute))
	a.jobs.Register(jobArrRefresh, classArr, a.refreshArr, jobs.Timeout(2*time.Hour))
	a.jobs.Register(jobSubmitRequest, classRequests, a.submitRequest, jobs.Timeout(2*time.Minute))
	a.jobs.Register(jobRefreshRequests, classRequests, a.refreshRequests, jobs.Timeout(5*time.Minute))
	a.jobs.Register(jobPushNotification, classPush, a.pushNotification, jobs.Timeout(5*time.Minute), jobs.MaxAttempts(1))
	a.jobs.Register(jobRefreshUpcoming, classRequests, a.refreshUpcoming, jobs.Timeout(5*time.Minute))
	a.jobs.Register(jobTrickplay, classTrickplay, a.generateTrickplay, jobs.Timeout(2*time.Hour))
	a.jobs.Register(jobDetectSegments, classSegments, a.detectSegments, jobs.Timeout(30*time.Minute))
	a.jobs.Register(jobPrepareDownload, classPrepare, a.prepareDownload, jobs.Timeout(12*time.Hour))
	a.jobs.Register(jobConvertDownload, classConvert, a.prepareDownload, jobs.Timeout(2*time.Hour))
	a.jobs.Register(jobIndexFile, classIndex, a.indexFile, jobs.Timeout(30*time.Minute))
	a.jobs.Register(jobExtractSubtitles, classExtract, a.extractSubtitles, jobs.Timeout(2*time.Hour))
	a.jobs.Register(jobPurgeSubtitles, classExtract, a.purgeSubtitles, jobs.Timeout(10*time.Minute))
}

// jobFailed writes a job that failed for good to the activity log. A request that could not be
// handed to its instance fails.
func (a *App) jobFailed(ctx context.Context, kind, target string, err error) {
	if kind == jobSubmitRequest {
		a.requestFailed(ctx, target, err)
	}
	msg := err.Error()
	if len(msg) > 300 {
		msg = strings.ToValidUTF8(msg[:300], "") + "…"
	}
	text := domain.T("activity.job_failed", "job", kind, "reason", msg)
	if label := a.jobLabel(ctx, kind, target); label != "" {
		text = domain.T("activity.job_failed_on", "job", kind, "target", label, "reason", msg)
	}
	a.record(ctx, domain.Activity{Kind: domain.ActivityJobFailed, Warning: true, Text: text})
}

// Start launches background work: the job queue, periodic library scans, the purge of expired
// sessions, batched item change events, the playback watchdog. Everything stops with ctx; Wait
// waits for it to end.
func (a *App) Start(ctx context.Context) error {
	if err := a.prober.Check(ctx); err != nil {
		// The server is still useful (accounts, the catalog it already knows). Analyses will fail
		// until FFmpeg is installed or configured.
		a.log.WarnContext(ctx, "FFmpeg not found: new files will not be analysed (setting ffmpeg.ffprobe or LATERNA_FFPROBE)", "err", err)
	}
	a.runCtx = ctx
	if err := a.jobs.Start(ctx); err != nil {
		return err
	}
	a.background.Go(func() { a.watchPlayback(ctx) })
	a.background.Go(func() { a.watchParties(ctx) })
	a.background.Go(func() { a.watchLibraries(ctx) })
	// Detect encoders right at startup, so the first transcoded playback does not wait.
	a.background.Go(func() { _, _ = a.encoders() })
	a.background.Go(func() { a.periodic(ctx) })
	a.background.Go(func() { a.publishChanges(ctx) })
	a.background.Go(func() { a.watchNewEpisodes(ctx) })
	return nil
}

// Wait waits for background work to stop (after the context given to Start is canceled).
func (a *App) Wait() {
	a.background.Wait()
	a.jobs.Wait()
}

// periodic schedules recurring work: library scans at the configured interval (read again whenever
// settings change) and daily purges. A scan of each library is requested at startup, since files
// may have changed while the server was down.
func (a *App) periodic(ctx context.Context) {
	if !a.noAutoScans {
		a.enqueueAllScans(ctx, priorityBackground)
	}
	purge := time.NewTicker(24 * time.Hour)
	defer purge.Stop()
	requests := time.NewTicker(requestsEvery)
	defer requests.Stop()
	a.followUpcoming(ctx, 0)
	upcoming := time.NewTicker(upcomingEvery)
	defer upcoming.Stop()
	var timer *time.Timer
	arm := func() <-chan time.Time {
		if timer != nil {
			timer.Stop()
		}
		d := a.Settings().ScanInterval
		if a.noAutoScans || d <= 0 {
			return nil // never
		}
		timer = time.NewTimer(d)
		return timer.C
	}
	scans := arm()
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.settingsChanged:
			scans = arm()
		case <-scans:
			a.enqueueAllScans(ctx, priorityBackground)
			scans = arm()
		case <-purge.C:
			a.runPurges(ctx)
		case <-requests.C:
			a.followRequests(ctx, 0)
		case <-upcoming.C:
			a.followUpcoming(ctx, 0)
		}
	}
}

// runPurges forgets expired sessions and old activity, and asks for the purge of subtitles, fonts,
// people and images that are no longer used.
func (a *App) runPurges(ctx context.Context) {
	if _, err := a.PurgeExpiredSessions(ctx); err != nil {
		a.log.WarnContext(ctx, "cannot purge expired sessions", "err", err)
	}
	if err := a.store.Write(ctx, func(q store.Q) error {
		_, err := q.DeleteActivityBefore(ctx, a.now().Add(-activityRetention))
		return err
	}); err != nil {
		a.log.WarnContext(ctx, "cannot purge the activity log", "err", err)
	}
	a.purgeConverted(ctx)
	a.purgeDownloads(ctx)
	if err := a.store.Optimize(ctx); err != nil {
		a.log.WarnContext(ctx, "database statistics: update failed", "err", err)
	}
	if err := a.purgeBookCache(ctx); err != nil {
		a.log.WarnContext(ctx, "cannot purge cached book pages", "err", err)
	}
	if err := a.purgePrints(ctx); err != nil {
		a.log.WarnContext(ctx, "cannot purge cached audio fingerprints", "err", err)
	}
	if err := a.purgePosters(ctx); err != nil {
		a.log.WarnContext(ctx, "cannot purge cached request posters", "err", err)
	}
	if err := a.purgeNotifications(ctx); err != nil {
		a.log.WarnContext(ctx, "cannot purge old notifications", "err", err)
	}
	a.enqueuePurges(ctx)
}

// enqueuePurges asks for the purge of subtitles, fonts, people and images that are no longer used.
func (a *App) enqueuePurges(ctx context.Context) {
	if err := a.store.Write(ctx, func(q store.Q) error {
		return errors.Join(
			a.jobs.Enqueue(ctx, q, jobPurgeSubtitles, "", priorityBackground),
			a.jobs.Enqueue(ctx, q, jobPurgeMetadata, "", priorityBackground),
			a.jobs.Enqueue(ctx, q, jobBackupStore, "", priorityIdle),
		)
	}); err != nil {
		a.log.WarnContext(ctx, "purges: cannot enqueue", "err", err)
		return
	}
	a.jobs.Kick()
}

// enqueueAllScans asks for a scan of each library.
func (a *App) enqueueAllScans(ctx context.Context, priority int) {
	libs, err := a.store.Read().Libraries(ctx)
	if err != nil {
		a.log.WarnContext(ctx, "periodic scan: cannot read libraries", "err", err)
		return
	}
	err = a.store.Write(ctx, func(q store.Q) error {
		for _, l := range libs {
			if err := a.jobs.Enqueue(ctx, q, jobScanLibrary, l.ID.String(), priority); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		a.log.WarnContext(ctx, "periodic scan: cannot enqueue", "err", err)
		return
	}
	a.jobs.Kick()
}
