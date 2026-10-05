package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/laterna-project/laterna/internal/api"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/buildinfo"
	"github.com/laterna-project/laterna/internal/config"
	"github.com/laterna-project/laterna/internal/discovery"
	"github.com/laterna-project/laterna/internal/logging"
	"github.com/laterna-project/laterna/internal/platform"
	"github.com/laterna-project/laterna/internal/proc"
	"github.com/laterna-project/laterna/internal/store"
	"github.com/laterna-project/laterna/internal/telemetry"
)

// shutdownTimeout caps the graceful shutdown: past it, remaining connections are cut.
const shutdownTimeout = 15 * time.Second

func loadConfig(fs *flag.FlagSet, args []string) (config.Config, platform.Dirs, error) {
	cfgFile := fs.String("config", "", "TOML configuration file")
	address := fs.String("address", "", "listen address")
	if err := fs.Parse(args); err != nil {
		return config.Config{}, platform.Dirs{}, err
	}
	cfg, err := config.Load(configPath(*cfgFile), os.LookupEnv)
	if err != nil {
		return config.Config{}, platform.Dirs{}, err
	}
	if *address != "" {
		cfg.Server.Address = *address
	}
	defaults, err := platform.DefaultDirs()
	if err != nil {
		return config.Config{}, platform.Dirs{}, err
	}
	dirs := cfg.Dirs(defaults)
	if err := dirs.Ensure(); err != nil {
		return config.Config{}, platform.Dirs{}, fmt.Errorf("creating folders: %w", err)
	}
	return cfg, dirs, nil
}

// executableDir is the folder of the running binary, symbolic links resolved; empty if unknown.
func executableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

// recentLogs is the number of log messages kept in memory for the admin API.
const recentLogs = 2000

func newLogger(cfg config.Config, dirs platform.Dirs, stdout io.Writer, ring *logging.Ring) (*slog.Logger, io.Closer, error) {
	level, err := logging.ParseLevel(cfg.Log.Level)
	if err != nil {
		return nil, nil, err
	}
	return logging.New(stdout, logging.Options{Level: level, JSON: cfg.Log.Format == "json", Dir: dirs.Log(), Ring: ring})
}

func serveCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg, dirs, err := loadConfig(fs, args)
	if err != nil {
		return err
	}
	ring := logging.NewRing(recentLogs)
	log, closer, err := newLogger(cfg, dirs, stdout, ring)
	if err != nil {
		return err
	}
	defer func() { _ = closer.Close() }()
	// FFmpeg and ffprobe die with the server, even if it is killed abruptly.
	if err := proc.Bind(); err != nil {
		log.Warn("child processes may outlive a hard stop of the server", "err", err)
	}
	return serve(ctx, cfg, dirs, log, ring, nil)
}

func migrateCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	_, dirs, err := loadConfig(fs, args)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, filepath.Join(dirs.Data, store.FileName), store.WithBackupDir(dirs.BackupDir()))
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "database up to date:", filepath.Join(dirs.Data, store.FileName))
	return st.Close()
}

// serve starts the server and blocks until ctx is canceled, then shuts down gracefully: no new
// connections, requests in progress finished, database closed. ring (optional) keeps the latest log
// messages for the admin API; ready (optional) receives the actual listen address.
func serve(ctx context.Context, cfg config.Config, dirs platform.Dirs, log *slog.Logger, ring *logging.Ring, ready func(addr string)) error {
	// Prepared restore (laterna restore, or SystemService.RestoreBackup): applied before the
	// database is opened. If it is rejected it is moved aside and the server starts on its own
	// database.
	switch restored, kept, err := store.ApplyRestore(ctx, dirs.Data, dirs.BackupDir(), time.Now()); {
	case err != nil:
		log.Error("database restore failed: starting on the current database", "err", err)
	case restored:
		log.Warn("database restored from a backup", "replaced", kept)
	}
	st, err := store.Open(ctx, filepath.Join(dirs.Data, store.FileName), store.WithBackupDir(dirs.BackupDir()))
	if err != nil {
		return err
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Error("closing the database", "err", err)
		}
	}()

	// OpenTelemetry tracing, configured with the standard OTEL_* variables: off without a
	// collector. The last spans are sent at shutdown.
	tracer, err := telemetry.FromEnv(ctx, os.Getenv, buildinfo.Version, log)
	if err != nil {
		return err
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		tracer.Shutdown(flushCtx)
	}()

	// FFmpeg shipped next to the server (release archives) is used unless the configuration
	// names another one.
	tools := cfg.FFmpeg.NextTo(executableDir())
	opts := app.Options{
		Logger: log, Logs: ring, FFmpeg: tools.FFmpeg, FFprobe: tools.FFprobe, Encoder: cfg.FFmpeg.Encoder,
		DataDir: dirs.Data, CacheDir: dirs.Cache, MetadataDir: dirs.Metadata, LogDir: dirs.Log(), BackupDir: dirs.BackupDir(), Tracer: tracer,
	}
	a, err := app.New(ctx, st, opts)
	if err != nil {
		return err
	}
	// Background work (job queue, periodic scans): stopped and waited for before the database is
	// closed, including when startup fails further down.
	bgCtx, stopBackground := context.WithCancel(ctx)
	defer func() {
		stopBackground()
		a.Wait()
	}()
	if err := a.Start(bgCtx); err != nil {
		return err
	}
	if n, err := a.PurgeExpiredSessions(ctx); err != nil {
		log.Warn("cannot purge expired sessions", "err", err)
	} else if n > 0 {
		log.Info("expired sessions deleted", "count", n)
	}

	// HTTP/1.1 for browsers and the Connect protocol, cleartext HTTP/2 for native gRPC clients
	// (Android, iOS). Behind a TLS proxy, the proxy negotiates HTTP/2.
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	srv := &http.Server{
		Handler: api.NewHandler(a, api.Options{
			CORSOrigins:    cfg.Server.CORSOrigins,
			TrustedProxies: cfg.Server.Proxies(),
			Logger:         log,
		}),
		Protocols:         &protocols,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: a video stream lasts as long as the movie.
		IdleTimeout: 2 * time.Minute,
		ErrorLog:    slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Server.Address)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", cfg.Server.Address, err)
	}
	log.Info("Laterna started",
		"address", ln.Addr().String(), "version", buildinfo.Version, "server_id", a.Server().ID.String(),
		"server_name", a.Server().Name, "data", dirs.Data, "cache", dirs.Cache)
	if ready != nil {
		ready(ln.Addr().String())
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	// Local network announcement: stopped before the HTTP server, it tells clients the server is
	// going away.
	discoveryCtx, stopDiscovery := context.WithCancel(ctx)
	discoveryDone := startDiscovery(discoveryCtx, cfg.Server.Discovery, ln.Addr(), a, log)

	select {
	case err := <-serveErr:
		stopDiscovery()
		<-discoveryDone
		return fmt.Errorf("HTTP server: %w", err)
	case <-ctx.Done():
	}

	log.Info("shutting down...")
	stopDiscovery()
	<-discoveryDone
	// The parent context is canceled: shutdown has its own deadline.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("stopping the HTTP server: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	stopBackground()
	a.Wait()
	log.Info("Laterna stopped")
	return nil
}

// startDiscovery announces the server on the local network as long as ctx lives. The returned
// channel is closed when the announcement is over. It does nothing if discovery is off, if the
// server only listens on loopback (nobody else could reach it) or on an IPv6 address (the
// announcement is IPv4 only). An unavailable mDNS port never prevents the server from starting.
func startDiscovery(ctx context.Context, enabled bool, addr net.Addr, a *app.App, log *slog.Logger) <-chan struct{} {
	done := make(chan struct{})
	tcp, ok := addr.(*net.TCPAddr)
	if !enabled || !ok {
		close(done)
		return done
	}
	opts := discovery.Options{
		Port:   tcp.Port,
		Logger: log,
		Info: func() discovery.Info {
			srv := a.Server()
			return discovery.Info{ID: srv.ID.String(), Name: srv.Name, Version: buildinfo.Version}
		},
	}
	if ip, ok := netip.AddrFromSlice(tcp.IP); ok && !ip.Unmap().IsUnspecified() {
		ip = ip.Unmap()
		if ip.IsLoopback() || !ip.Is4() {
			log.Info("local network discovery off: the server does not listen on an IPv4 network address", "address", addr.String())
			close(done)
			return done
		}
		opts.Only = ip
	}
	r, err := discovery.Listen(opts)
	if err != nil {
		log.Warn("local network discovery unavailable", "err", err)
		close(done)
		return done
	}
	log.Info("announced on the local network", "service", discovery.ServiceType, "port", tcp.Port, "addresses", r.Addresses())
	if platform.InContainer() {
		// On a bridge network the announcement gives the container's address and does not leave the
		// host. Better say so than let people think the TVs in the living room will find the
		// server.
		log.Info("running in a container: the announcement reaches the local network only with host networking (--network host); with a bridge network, enter the server address on each device")
	}
	go func() {
		defer close(done)
		if err := r.Serve(ctx); err != nil {
			log.Warn("local network discovery stopped", "err", err)
		}
	}()
	return done
}
