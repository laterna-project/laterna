// Package app holds the server's use cases. The API layers only talk to it (and to the domain),
// never directly to the database or the media tools.
package app

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/laterna-project/laterna/internal/auth"
	"github.com/laterna-project/laterna/internal/buildinfo"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/events"
	"github.com/laterna-project/laterna/internal/jobs"
	"github.com/laterna-project/laterna/internal/logging"
	"github.com/laterna-project/laterna/internal/media/probe"
	"github.com/laterna-project/laterna/internal/media/transcode"
	"github.com/laterna-project/laterna/internal/metadata/download"
	"github.com/laterna-project/laterna/internal/segments"
	"github.com/laterna-project/laterna/internal/store"
	"github.com/laterna-project/laterna/internal/telemetry"
)

// Keys of settings in the database.
const (
	keyServerID   = "server.id"
	keyServerName = "server.name"
)

// Options configures the application at startup.
type Options struct {
	// ServerName is the name announced as long as none is set in the database (tests); empty means
	// the host name.
	ServerName string
	Logger     *slog.Logger
	// Logs keeps the latest log messages for the admin API; nil for none.
	Logs *logging.Ring
	// DataDir and LogDir are shown by the admin API. LogDir is also where log files are read from.
	DataDir string
	LogDir  string
	// FFmpeg and FFprobe are the executables ("" for those in PATH).
	FFmpeg  string
	FFprobe string
	// Encoder forces the H.264 encoder used for transcoding; empty means the best one that works. A
	// forced encoder that cannot be used here gives way to the best one.
	Encoder string
	// NoAutoScans turns off automatic scans (at startup, periodic, and on folder changes) whatever
	// the setting says (tests).
	NoAutoScans bool
	// WatchQuiet is how long folders must stay quiet after a change before scanning; 0 means 10 s.
	WatchQuiet time.Duration
	// SegmentMin is the shortest intro or credits accepted from audio; 0 means 15 s. Fixtures have
	// 3 s ones.
	SegmentMin time.Duration
	// CacheDir gets whatever can be rebuilt at any time (resized images); empty means the OS
	// temporary folder.
	CacheDir string
	// MetadataDir gets downloaded images (from URLs found in NFO files); empty means under
	// CacheDir.
	MetadataDir string
	// BackupDir gets the database backups; empty means no backups.
	BackupDir string
	// Tracer traces requests and background work; nil turns it off.
	Tracer *telemetry.Tracer
	// HTTPClient replaces the client used for downloads and for Sonarr and Radarr calls (tests);
	// nil means the default client.
	HTTPClient *http.Client
}

// App is the entry point of the use cases.
type App struct {
	store     *store.Store
	log       *slog.Logger
	serverID  domain.ID
	startedAt time.Time
	now       func() time.Time
	// Slows down repeated password and profile PIN attempts.
	limiter *auth.Limiter
	// publicRate slows down, per address, the public endpoints that are expensive (logins).
	publicRate *auth.Rate

	prober      *probe.Prober
	jobs        *jobs.Runner
	noAutoScans bool
	background  sync.WaitGroup

	// settings are the settings in effect; settingsChanged wakes the scheduler.
	settingsMu      sync.RWMutex
	settings        domain.Settings
	settingsChanged chan struct{}

	logs    *logging.Ring
	dataDir string
	logDir  string
	// ffmpegVersion is FFmpeg's version, read once.
	ffmpegVersionOnce sync.Once
	ffmpegVersionText string

	cacheDir    string
	metadataDir string
	ffmpeg      string
	ffprobe     string
	// plays are the playbacks in progress; runCtx is the context of background work (FFmpeg runs).
	plays  playSessions
	runCtx context.Context
	// convs are the audio conversions in progress, preps the download preparations.
	convs conversions
	preps preparations
	// rooms are the watch parties in progress.
	rooms partyRooms
	// deviceLogins are the device login requests in progress.
	deviceLogins deviceLogins
	// watchResync wakes the folder watcher; watchQuiet is the quiet time before scanning.
	watchResync chan struct{}
	watchQuiet  time.Duration
	// segmentMin is the shortest intro or credits accepted from audio.
	segmentMin time.Duration
	// recs is the recommendation index.
	recs recIndex
	// passkeys are the challenges of passkey registrations and logins in progress.
	passkeys passkeyChallenges
	// oidc is the OpenID Connect provider and the logins in progress.
	oidc oidcState
	// backupDir is the folder for database backups; empty means none.
	backupDir string
	// metrics are the server metrics.
	metrics appMetrics
	// tracer traces requests and background work.
	tracer *telemetry.Tracer
	// encoders are the usable H.264 encoders, detected once by really trying them.
	encoders func() (transcode.Caps, error)
	// download fetches the images NFO files point to (if the setting allows it).
	download *download.Client
	// http calls Sonarr and Radarr; arrPoll is the delay between two reads of a command's state.
	http    *http.Client
	arrPoll time.Duration
	// resizing caps the number of images resized at the same time (CPU).
	resizing chan struct{}
	// warming holds the book pages being prepared ahead (cache path).
	warming sync.Map

	// posters are the posters of recent request searches.
	posters posterURLs
	// books are the books of recent request searches.
	books bookCache
	// subSearches are the subtitle searches handed to Bazarr; the two durations are how long it
	// gets to find one and how often it is asked.
	subSearches        subtitleSearches
	subtitleSearchWait time.Duration
	subtitleSearchPoll time.Duration
	// arrived are the episodes that arrived and were not announced yet.
	arrived newEpisodes
	// upcoming is what Sonarr, Radarr and Lidarr expect in the coming days.
	upcoming upcomingCache

	// bus pushes events to subscribed clients; changes batches the item ones.
	bus     *events.Bus[domain.Event]
	changes changes
}

// New sets up the application. The server ID is created on first start and kept, so that clients
// recognize the same server.
func New(ctx context.Context, st *store.Store, opts Options) (*App, error) {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	a := &App{
		store: st, log: log, now: time.Now, limiter: auth.NewLimiter(5, 15*time.Minute),
		publicRate: auth.NewRate(publicBurst, publicEvery),
		prober:     probe.New(opts.FFprobe), jobs: jobs.New(st, log), noAutoScans: opts.NoAutoScans, startedAt: time.Now(),
		settingsChanged: make(chan struct{}, 1), logs: opts.Logs, dataDir: opts.DataDir, logDir: opts.LogDir,
		watchResync: make(chan struct{}, 1), watchQuiet: cmp.Or(opts.WatchQuiet, defaultWatchQuiet),
		segmentMin:         cmp.Or(opts.SegmentMin, segments.DefaultMin),
		subtitleSearchWait: defaultSubtitleSearchWait, subtitleSearchPoll: defaultSubtitleSearchPoll,
		cacheDir: opts.CacheDir, resizing: make(chan struct{}, max(1, runtime.NumCPU()/2)),
		bus: events.New[domain.Event](), ffmpeg: opts.FFmpeg, ffprobe: opts.FFprobe,
		plays: playSessions{byID: map[domain.ID]*playSession{}}, convs: conversions{byPath: map[string]*conversion{}},
		preps:        preparations{byID: map[domain.ID]context.CancelFunc{}, lastAt: map[domain.ID]time.Time{}},
		rooms:        partyRooms{byID: map[domain.ID]*room{}, byCode: map[string]*room{}},
		deviceLogins: deviceLogins{byDevice: map[string]*deviceLogin{}, byUser: map[string]*deviceLogin{}},
	}
	if opts.Encoder != "" && !slices.Contains(transcode.Known(), opts.Encoder) {
		return nil, fmt.Errorf("unknown encoder %q (%s)", opts.Encoder, strings.Join(transcode.Known(), ", "))
	}
	// Detected once and shared by all playbacks. It does not depend on any caller, but it stops
	// with background work. Shutting down does not wait for the tests to finish (about ten seconds
	// on an idle machine, much more on a busy one): a server that is stopping no longer needs them.
	//nolint:contextcheck // detection has its own context
	a.encoders = sync.OnceValues(func() (transcode.Caps, error) {
		ctx := a.runCtx
		if ctx == nil {
			ctx = context.Background()
		}
		caps, err := transcode.Detect(ctx, a.ffmpeg)
		if ctx.Err() != nil {
			return transcode.Caps{}, ctx.Err()
		}
		if err != nil {
			a.log.Warn("transcoding unavailable", "err", err)
			return caps, err
		}
		if opts.Encoder != "" {
			forced, ok := caps.Prefer(opts.Encoder)
			if !ok {
				a.log.Warn("forced encoder unusable on this machine: falling back to the best one", "encoder", opts.Encoder)
			}
			caps = forced
		}
		best, _ := caps.Best()
		caps.ToneMappers = transcode.DetectToneMappers(ctx, a.ffmpeg, best)
		caps.GPU = transcode.DetectGPU(ctx, a.ffmpeg, best)
		caps.Decoder = transcode.DetectDecoder(ctx, a.ffmpeg, best)
		if ctx.Err() != nil {
			return transcode.Caps{}, ctx.Err()
		}
		var tms []string
		for _, tm := range caps.ToneMappers {
			tms = append(tms, tm.Name)
		}
		a.log.Info("usable H.264 encoders", "encoders", caps.Names(), "hdr_to_sdr", tms, "gpu", caps.GPU,
			"decoder", decoderName(caps.Decoder))
		return caps, nil
	})
	if a.cacheDir == "" {
		a.cacheDir = filepath.Join(os.TempDir(), "laterna-cache")
	}
	a.metadataDir = opts.MetadataDir
	if a.metadataDir == "" {
		a.metadataDir = filepath.Join(a.cacheDir, "metadata")
	}
	a.backupDir = opts.BackupDir
	a.tracer = opts.Tracer
	if a.tracer == nil {
		a.tracer = telemetry.Disabled()
	}
	a.http, a.arrPoll = &http.Client{Timeout: 30 * time.Second}, 5*time.Second
	if opts.HTTPClient != nil {
		client := *opts.HTTPClient
		a.http = &client
	}
	// Outgoing calls are traced (Sonarr, Radarr, NFO images, OpenID Connect).
	a.http.Transport = a.tracer.Transport(a.http.Transport)
	// One request every 250 ms per site: image CDNs (TVDB, TMDb) take that without complaining, and
	// 500 photos arrive in two minutes.
	a.download = download.New("Laterna/"+buildinfo.Version+" (+https://github.com/laterna-project/laterna)", 250*time.Millisecond, a.http)
	a.registerJobs()
	// Metrics first: everything after this may write to the activity log, which counts entries.
	if err := a.initMetrics(ctx); err != nil {
		return nil, fmt.Errorf("app: metrics: %w", err)
	}
	id, err := a.ensureServerID(ctx)
	if err != nil {
		return nil, err
	}
	a.serverID = id
	if a.settings, err = a.loadSettings(ctx, defaultSettings(opts.ServerName)); err != nil {
		return nil, fmt.Errorf("app: %w", err)
	}
	if err := a.loadThemes(ctx); err != nil {
		return nil, fmt.Errorf("app: %w", err)
	}
	if err := a.loadOIDC(ctx); err != nil {
		return nil, fmt.Errorf("app: %w", err)
	}
	return a, nil
}

func (a *App) ensureServerID(ctx context.Context) (domain.ID, error) {
	if err := a.store.Write(ctx, func(q store.Q) error {
		return q.InitSetting(ctx, keyServerID, domain.NewID().String())
	}); err != nil {
		return domain.ID{}, fmt.Errorf("app: server ID: %w", err)
	}
	raw, _, err := a.store.Read().Setting(ctx, keyServerID)
	if err != nil {
		return domain.ID{}, fmt.Errorf("app: server ID: %w", err)
	}
	id, err := domain.ParseID(raw)
	if err != nil {
		return domain.ID{}, fmt.Errorf("app: corrupt server ID %q: %w", raw, err)
	}
	return id, nil
}

// Healthy checks that the database answers.
func (a *App) Healthy(ctx context.Context) error {
	if err := a.store.Read().Ping(ctx); err != nil {
		return fmt.Errorf("database: %w", err)
	}
	return nil
}

// Server returns the server's identity.
func (a *App) Server() domain.Server {
	return domain.Server{ID: a.serverID, Name: a.Settings().ServerName}
}
