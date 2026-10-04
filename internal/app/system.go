package app

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/buildinfo"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/logging"
	"github.com/laterna-project/laterna/internal/media/fmp4"
	"github.com/laterna-project/laterna/internal/proc"
	"github.com/laterna-project/laterna/internal/store"
)

// Server administration: status, job queue, scheduled tasks, logs, devices and playbacks in
// progress. Rights are checked by the API layer from the contract.

// SystemStatus is the state of the server, for the dashboard.
type SystemStatus struct {
	Version, Commit     string
	StartedAt           time.Time
	OS, Arch, GoVersion string
	// FFmpeg is the first line of "ffmpeg -version"; empty if FFmpeg does not start.
	FFmpeg      string
	Encoders    []string
	ToneMappers []string
	GPU         bool
	// Server folders.
	DataDir, CacheDir, MetadataDir, LogDir string
	// Playbacks counts the playbacks in progress, FFmpegRunning the playback FFmpeg processes
	// running.
	Playbacks, FFmpegRunning int
	// Transcodes counts the active video transcodes; TranscodeLimit is the cap in effect.
	Transcodes, TranscodeLimit int
	// Jobs waiting, running and failed.
	JobsPending, JobsRunning, JobsFailed int
}

// SystemStatus describes the state of the server.
func (a *App) SystemStatus(ctx context.Context) (SystemStatus, error) {
	st := SystemStatus{
		Version: buildinfo.Version, Commit: buildinfo.Commit(), StartedAt: a.startedAt,
		OS: runtime.GOOS, Arch: runtime.GOARCH, GoVersion: runtime.Version(),
		FFmpeg:  a.ffmpegVersion(ctx),
		DataDir: a.dataDir, CacheDir: a.cacheDir, MetadataDir: a.metadataDir, LogDir: a.logDir,
		FFmpegRunning: fmp4.Running(), Transcodes: a.activeTranscodes(), TranscodeLimit: a.transcodeLimit(),
	}
	if caps, err := a.encoders(); err == nil {
		st.Encoders, st.GPU = caps.Names(), caps.GPU
		for _, tm := range caps.ToneMappers {
			st.ToneMappers = append(st.ToneMappers, tm.Name)
		}
	}
	a.plays.mu.Lock()
	st.Playbacks = len(a.plays.byID)
	a.plays.mu.Unlock()
	counts, err := a.store.Read().CountJobs(ctx)
	if err != nil {
		return st, err
	}
	for _, c := range counts {
		switch c.State {
		case "pending":
			st.JobsPending += c.N
		case "running":
			st.JobsRunning += c.N
		case "failed":
			st.JobsFailed += c.N
		}
	}
	return st, nil
}

// ffmpegVersion reads FFmpeg's version (once: it does not change without a restart).
func (a *App) ffmpegVersion(ctx context.Context) string {
	a.ffmpegVersionOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		bin := a.ffmpeg
		if bin == "" {
			bin = "ffmpeg"
		}
		out, err := proc.Command(ctx, bin, "-hide_banner", "-version").Output()
		if err != nil {
			a.log.WarnContext(ctx, "cannot read FFmpeg version", "err", err)
			return
		}
		line, _, _ := bytes.Cut(out, []byte("\n"))
		a.ffmpegVersionText = strings.TrimSpace(strings.ToValidUTF8(string(line), "�"))
	})
	return a.ffmpegVersionText
}

// Job queue.

// maxFailedJobs caps the list of failed jobs.
const maxFailedJobs = 200

// FailedJob is a failed job, with a readable description of its target.
type FailedJob struct {
	store.FailedJob
	// Label is the file, item, library or image it was about ("" if unknown).
	Label string
}

// JobsOverview is the state of the job queue.
type JobsOverview struct {
	Counts []store.JobCount
	Failed []FailedJob
}

// Jobs describes the job queue: counts by kind and state, failed jobs.
func (a *App) Jobs(ctx context.Context) (JobsOverview, error) {
	read := a.store.Read()
	counts, err := read.CountJobs(ctx)
	if err != nil {
		return JobsOverview{}, err
	}
	failed, err := read.FailedJobs(ctx, maxFailedJobs)
	if err != nil {
		return JobsOverview{}, err
	}
	out := JobsOverview{Counts: counts}
	for _, f := range failed {
		out.Failed = append(out.Failed, FailedJob{FailedJob: f, Label: a.jobLabel(ctx, f.Kind, f.Target)})
	}
	return out, nil
}

// jobLabel describes the target of a job.
func (a *App) jobLabel(ctx context.Context, kind, target string) string {
	read := a.store.Read()
	id, err := domain.ParseID(target)
	if err != nil {
		return target
	}
	switch {
	case strings.HasPrefix(kind, "file."):
		if f, err := read.File(ctx, id); err == nil {
			return f.Path
		}
	case kind == jobItemMetadata:
		if it, err := read.Item(ctx, id); err == nil {
			return it.Title
		}
	case kind == jobScanLibrary:
		if l, err := read.Library(ctx, id); err == nil {
			return l.Name
		}
	case strings.HasPrefix(kind, "image."):
		if img, err := read.Image(ctx, id); err == nil {
			if img.RemoteURL != "" {
				return img.RemoteURL
			}
			return img.Path
		}
	}
	return ""
}

// RetryJobs retries failed jobs (all of them if ids is empty) and returns how many.
func (a *App) RetryJobs(ctx context.Context, ids []int64) (int, error) {
	n, err := a.eachFailedJob(ctx, ids, func(q store.Q, j store.FailedJob) error {
		if err := q.DeleteFailedJob(ctx, j.ID); err != nil {
			return err
		}
		return a.jobs.Enqueue(ctx, q, j.Kind, j.Target, priorityUser)
	})
	if n > 0 {
		a.jobs.Kick()
	}
	return n, err
}

// DeleteJobs forgets failed jobs (all of them if ids is empty) and returns how many.
func (a *App) DeleteJobs(ctx context.Context, ids []int64) (int, error) {
	return a.eachFailedJob(ctx, ids, func(q store.Q, j store.FailedJob) error { return q.DeleteFailedJob(ctx, j.ID) })
}

func (a *App) eachFailedJob(ctx context.Context, ids []int64, fn func(q store.Q, j store.FailedJob) error) (int, error) {
	n := 0
	err := a.store.Write(ctx, func(q store.Q) error {
		var jobs []store.FailedJob
		if len(ids) == 0 {
			all, err := q.FailedJobs(ctx, maxFailedJobs)
			if err != nil {
				return err
			}
			jobs = all
		}
		for _, id := range ids {
			j, err := q.FailedJob(ctx, id)
			if store.IsNotFound(err) {
				return domain.NotFound("system.job_not_found", "job_id", id)
			}
			if err != nil {
				return err
			}
			jobs = append(jobs, j)
		}
		for _, j := range jobs {
			if err := fn(q, j); err != nil {
				return err
			}
		}
		n = len(jobs)
		return nil
	})
	return n, err
}

// Task is a scheduled task an administrator can run without waiting.
type Task string

// Scheduled tasks.
const (
	// TaskScanLibraries scans every library (normally every 6 h).
	TaskScanLibraries Task = "scan"
	// TaskPurge forgets expired sessions and old activity, and deletes subtitles, fonts, people and
	// images that are no longer used (normally every day).
	TaskPurge Task = "purge"
)

// RunTask runs a scheduled task right now.
func (a *App) RunTask(ctx context.Context, task Task) error {
	switch task {
	case TaskScanLibraries:
		a.enqueueAllScans(ctx, priorityUser)
	case TaskPurge:
		a.runPurges(ctx)
	default:
		return domain.Invalid("system.unknown_task", "task", task)
	}
	a.log.InfoContext(ctx, "scheduled task run on demand", "task", task)
	return nil
}

// Logs.

// LogEntry is a log message.
type LogEntry = logging.Entry

const (
	defaultLogLen = 200
	maxLogLen     = 1000
)

// RecentLogs returns the latest log messages at or above minLevel that contain text, newest first.
func (a *App) RecentLogs(minLevel slog.Level, text string, limit int) ([]LogEntry, error) {
	switch {
	case limit == 0:
		limit = defaultLogLen
	case limit < 0 || limit > maxLogLen:
		return nil, domain.Invalid("request.invalid_limit", "max", maxLogLen)
	}
	if a.logs == nil {
		return nil, nil
	}
	return a.logs.Entries(minLevel, text, limit), nil
}

// LogFile is a daily log file.
type LogFile struct {
	Name    string
	Size    int64
	ModTime time.Time
}

var logFileName = regexp.MustCompile(`^` + logging.FilePrefix + `\d{8}\` + logging.FileExt + `$`)

// LogFiles lists the log files, newest first.
func (a *App) LogFiles() ([]LogFile, error) {
	if a.logDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(a.logDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []LogFile
	for _, e := range entries {
		if e.IsDir() || !logFileName.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, LogFile{Name: e.Name(), Size: info.Size(), ModTime: info.ModTime()})
	}
	slices.SortFunc(out, func(x, y LogFile) int { return strings.Compare(y.Name, x.Name) })
	return out, nil
}

// OpenLogFile opens a log file by name, never leaving the log folder (any other name is not found).
// The caller closes it.
func (a *App) OpenLogFile(name string) (*os.File, error) {
	if a.logDir == "" || !logFileName.MatchString(name) {
		return nil, domain.NotFound("system.log_not_found")
	}
	root, err := os.OpenRoot(a.logDir)
	if err != nil {
		return nil, domain.NotFound("system.log_not_found")
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(name)
	if err != nil {
		return nil, domain.NotFound("system.log_not_found")
	}
	return f, nil
}

// Devices.

// Devices lists the signed-in devices of every account.
func (a *App) Devices(ctx context.Context) ([]store.Device, error) {
	return a.store.Read().AllSessions(ctx, a.now())
}

// RevokeDevice signs out a device of any account.
func (a *App) RevokeDevice(ctx context.Context, p domain.Principal, sessionID domain.ID) error {
	var owner domain.Account
	var dev domain.Device
	err := a.store.Write(ctx, func(q store.Q) error {
		s, err := q.Session(ctx, sessionID)
		if store.IsNotFound(err) {
			return domain.NotFound("auth.session_not_found")
		}
		if err != nil {
			return err
		}
		if owner, err = q.Account(ctx, s.AccountID); err != nil {
			return err
		}
		dev = s.Device
		return q.DeleteSession(ctx, sessionID)
	})
	if err != nil {
		return err
	}
	a.record(ctx, domain.Activity{
		Kind: domain.ActivitySessionRevoked, AccountID: &p.Account.ID,
		Text: domain.T("activity.device_revoked", "actor", p.Account.Username, "device", dev.Name, "username", owner.Username),
	})
	return nil
}

// Playbacks in progress.

// Playback is a playback in progress, as an administrator sees it.
type Playback struct {
	ID                    domain.ID
	AccountID, ProfileID  domain.ID
	Username, ProfileName string
	Device                string
	ItemID                domain.ID
	Title                 string
	Method                string
	CopyVideo, CopyAudio  bool
	Encoder, ToneMap      string
	GPU                   bool
	StartedAt             time.Time
	Position, Duration    time.Duration
	// Transcoding means an FFmpeg run is working for this playback right now.
	Transcoding bool
}

// Playbacks lists the playbacks in progress, oldest first.
func (a *App) Playbacks() []Playback {
	var out []Playback
	for _, s := range a.allSessions() {
		s.mu.Lock()
		p := Playback{
			ID: s.id, AccountID: s.who.accountID, ProfileID: s.profile, Username: s.who.username, ProfileName: s.who.profileName,
			Device: s.who.device, ItemID: s.item, Title: s.who.title, Method: string(s.plan.Method),
			CopyVideo: s.plan.CopyVideo, CopyAudio: s.plan.CopyAudio, Encoder: s.encoder.Name, ToneMap: toneMapName(s.toneMap, s.gpu),
			GPU: s.gpu, StartedAt: s.who.startedAt, Position: s.position, Duration: s.file.Info.Duration,
			Transcoding: s.run != nil && !s.run.done,
		}
		s.mu.Unlock()
		out = append(out, p)
	}
	slices.SortFunc(out, func(x, y Playback) int { return x.StartedAt.Compare(y.StartedAt) })
	return out
}

// EndPlayback stops a playback in progress of any profile.
func (a *App) EndPlayback(ctx context.Context, p domain.Principal, id domain.ID) error {
	a.plays.mu.Lock()
	s := a.plays.byID[id]
	a.plays.mu.Unlock()
	if s == nil {
		return domain.NotFound("playback.session_not_found")
	}
	a.log.InfoContext(ctx, "playback stopped by an administrator", "session", id, "admin", p.Account.Username)
	a.closeSession(ctx, s)
	return nil
}
