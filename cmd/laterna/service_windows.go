package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/laterna-project/laterna/internal/config"
	"github.com/laterna-project/laterna/internal/platform"
)

// serviceName names the Windows service, its account, its event log source and its firewall rule.
const serviceName = "Laterna"

// serviceAccount is the virtual account the service runs as: an account of its own, without a
// password, with the rights of an ordinary user and access to its own folders only.
const serviceAccount = `NT SERVICE\` + serviceName

const (
	// startWait caps how long laterna service start waits: a schema migration backs the database up
	// first.
	startWait = 2 * time.Minute
	// stopWait caps how long laterna service stop waits: the server stops within shutdownTimeout.
	stopWait = shutdownTimeout + 15*time.Second
)

const serviceUsage = `Usage: laterna service <command>

Runs Laterna as a Windows service, started with Windows. From an administrator prompt.

Commands:
  install    register the service (it does not start it)
  uninstall  stop and remove the service; its data stays
  start      start the service
  stop       stop the service

Options of install:
  -config <file>  TOML file the service reads (default: laterna.toml in %ProgramData%\Laterna,
                  written the first time)
`

var (
	errAdmin        = errors.New("the Windows service is managed from an administrator prompt")
	errNotInstalled = errors.New("the Laterna service is not installed (laterna service install)")
)

func serviceCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, serviceUsage)
		return errors.New("service: missing command")
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "run":
		return runService(args)
	case "help", "-h", "--help":
		_, _ = io.WriteString(stdout, serviceUsage)
		return nil
	case "install", "uninstall", "start", "stop":
	default:
		_, _ = io.WriteString(stderr, serviceUsage)
		return fmt.Errorf("service: unknown command %q", cmd)
	}
	if cmd != "install" && len(args) > 0 {
		return fmt.Errorf("service %s takes no argument", cmd)
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errAdmin
	}
	switch cmd {
	case "install":
		return installService(ctx, args, stdout, stderr)
	case "uninstall":
		return uninstallService(ctx, stdout)
	case "start":
		return startService(ctx, stdout)
	default:
		return stopService(ctx, stdout)
	}
}

// installService registers the service: account, command line, automatic start, restart after a
// failure, access to its folders, event log source, firewall rule. Everything or nothing: a
// failure removes the service again.
func installService(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("service install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgFile := fs.String("config", "", "TOML file the service reads")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("service install: unexpected argument %q", fs.Arg(0))
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// The firewall cannot resolve a short name ("C:\Users\RUNNER~1\..."): the full one, for the rule
	// and the service alike.
	exe = longPath(exe)
	root, err := platform.ServiceRoot()
	if err != nil {
		return err
	}
	file := *cfgFile
	if file == "" {
		file = filepath.Join(root, "laterna.toml")
		if err := writeServiceConfig(file); err != nil {
			return err
		}
	} else if file, err = filepath.Abs(file); err != nil {
		return err
	}
	// Read as the service will read it, here rather than at its first start. The environment of an
	// administrator prompt is not the one of the service: the configuration alone.
	cfg, err := config.Load(file, func(string) (string, bool) { return "", false })
	if err != nil {
		return err
	}
	defaults, err := platform.ServiceDirs()
	if err != nil {
		return err
	}
	dirs := cfg.Dirs(defaults)
	if err := dirs.Ensure(); err != nil {
		return fmt.Errorf("creating folders: %w", err)
	}

	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	if s, err := m.OpenService(serviceName); err == nil {
		_ = s.Close()
		return errors.New("the Laterna service is already installed (laterna service uninstall first)")
	}
	s, err := m.CreateService(serviceName, exe, mgr.Config{
		DisplayName:      "Laterna",
		Description:      "Laterna media server",
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
		ErrorControl:     mgr.ErrorNormal,
		ServiceStartName: serviceAccount,
		SidType:          windows.SERVICE_SID_TYPE_UNRESTRICTED,
	}, "service", "run", "-config", file)
	if err != nil {
		return fmt.Errorf("creating the service: %w", err)
	}
	defer func() { _ = s.Close() }()
	installed := false
	defer func() {
		if !installed {
			_ = s.Delete()
		}
	}()
	// Restarted after a failure: after 10 s, 1 min, then 5 min, counted again after a day.
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: time.Minute},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Minute},
	}, uint32((24 * time.Hour).Seconds())); err != nil {
		return fmt.Errorf("setting the restart after a failure: %w", err)
	}
	// The server ends on an error without crashing: that is a failure too.
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("setting the restart after a failure: %w", err)
	}
	if err := grantService(exe, file, cfg, dirs); err != nil {
		return err
	}
	_ = eventlog.Remove(serviceName)
	if err := eventlog.InstallAsEventCreate(serviceName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil {
		return fmt.Errorf("registering the event log source: %w", err)
	}
	installed = true

	_, _ = fmt.Fprintf(stdout, "Laterna service installed: it starts with Windows, as %s.\n", serviceAccount)
	_, _ = fmt.Fprintf(stdout, "  configuration  %s\n  data           %s\n  logs           %s\n", file, dirs.Data, dirs.Log())
	// The network reaches the server without Windows asking anyone: a service has no desktop to
	// ask on.
	_ = firewall(ctx, "delete", "rule", "name="+serviceName)
	if err := firewall(ctx, "add", "rule", "name="+serviceName, "dir=in", "action=allow", "program="+exe,
		"enable=yes", "profile=private,domain", "description=Laterna media server"); err != nil {
		_, _ = fmt.Fprintf(stdout, "No firewall rule (%v): allow %s in the firewall for other devices to reach the server.\n", err, exe)
	}
	if mine, err := platform.DefaultDirs(); err == nil && mine.Data != dirs.Data && exists(filepath.Join(mine.Data, "laterna.db")) && !exists(filepath.Join(dirs.Data, "laterna.db")) {
		_, _ = fmt.Fprintf(stdout, "The server already ran from this account. To keep its library, copy %s to %s and %s to %s before the first start.\n",
			mine.Data, dirs.Data, mine.Metadata, dirs.Metadata)
	}
	_, _ = fmt.Fprintln(stdout, "Start it now: laterna service start")
	return nil
}

// serviceConfig is the laterna.toml written for the service, as the Linux packages install theirs:
// the folders are named, so that laterna backup -config finds the database of the service.
const serviceConfig = `# Laterna startup configuration, read by the Windows service.
# Only what the server needs before it opens its database lives here. Everything else (server
# name, scans, public address...) is a setting changed from a client's administration screens.
# After a change: laterna service stop, then laterna service start.

[server]
address = ":8096"
# Reverse proxies whose X-Forwarded-For header is trusted (addresses or CIDR ranges).
# trusted_proxies = ["127.0.0.1"]
# Announce the server on the local network (mDNS).
# discovery = true

[paths]
data = {data}
metadata = {metadata}
cache = {cache}
# Database backups. Another disk protects against losing the first one.
# backups = 'E:\Laterna backups'
# A web client to serve next to the API: an unpacked release of
# https://github.com/laterna-project/laterna-web (see docs/install.md).
# web = 'C:\Program Files\Laterna Web'

[ffmpeg]
# FFmpeg next to laterna.exe is used when it is there, else the one in the PATH of the system.
# ffmpeg = 'C:\ffmpeg\bin\ffmpeg.exe'
# ffprobe = 'C:\ffmpeg\bin\ffprobe.exe'
# Force the H.264 encoder instead of the best one detected at startup.
# encoder = "libx264"
`

// writeServiceConfig writes the configuration of the service unless it exists: a configuration
// kept from an earlier install stays as it was.
func writeServiceConfig(path string) error {
	dirs, err := platform.ServiceDirs()
	if err != nil {
		return err
	}
	text := strings.NewReplacer("{data}", tomlString(dirs.Data), "{metadata}", tomlString(dirs.Metadata), "{cache}", tomlString(dirs.Cache)).Replace(serviceConfig)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}

// tomlString writes a path as a TOML string: literal (backslashes as they are) unless it holds a
// quote.
func tomlString(s string) string {
	if !strings.ContainsAny(s, "'\n") {
		return "'" + s + "'"
	}
	return strconv.Quote(s)
}

// longPath gives the long form of a path whose folders may have a short name; the path itself if
// Windows cannot.
func longPath(path string) string {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return path
	}
	var buf [windows.MAX_LONG_PATH]uint16
	n, err := windows.GetLongPathName(p, &buf[0], windows.MAX_LONG_PATH)
	if err != nil || n == 0 || int(n) > len(buf) {
		return path
	}
	return windows.UTF16ToString(buf[:n])
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// grantService gives the account of the service what it needs: its folders in full, read access to
// its program, its configuration, the web client and FFmpeg. Media folders are not touched: an
// ordinary user can read them, or an administrator gives access (docs/install.md).
func grantService(exe, cfgFile string, cfg config.Config, dirs platform.Dirs) error {
	sid, _, _, err := windows.LookupSID("", serviceAccount)
	if err != nil {
		return fmt.Errorf("finding the account %s: %w", serviceAccount, err)
	}
	const (
		read   = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE
		modify = read | windows.FILE_GENERIC_WRITE | windows.DELETE
	)
	grants := map[string]windows.ACCESS_MASK{filepath.Dir(exe): read, cfgFile: read}
	for _, p := range []string{cfg.Paths.Web, cfg.FFmpeg.FFmpeg, cfg.FFmpeg.FFprobe} {
		if filepath.IsAbs(p) {
			grants[p] = read
		}
	}
	for _, p := range []string{dirs.Data, dirs.Cache, dirs.Metadata, dirs.BackupDir()} {
		grants[p] = modify
	}
	paths := make([]string, 0, len(grants))
	for p := range grants {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		if err := grant(p, sid, grants[p]); err != nil {
			return fmt.Errorf("giving %s access to %s: %w", serviceAccount, p, err)
		}
	}
	return nil
}

// grant adds access for sid to the ACL of a file, or of a folder and everything inside it.
func grant(path string, sid *windows.SID, mask windows.ACCESS_MASK) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) || err == nil && dacl == nil {
		// No ACL (FAT, exFAT): everyone has access.
		return nil
	}
	if err != nil {
		return err
	}
	inheritance := uint32(windows.NO_INHERITANCE)
	if info.IsDir() {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: mask,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, dacl)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

// firewall runs netsh advfirewall firewall with args.
func firewall(ctx context.Context, args ...string) error {
	sys, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	//nolint:gosec // netsh of the system, arguments as a slice, never a shell
	out, err := exec.CommandContext(ctx, filepath.Join(sys, "netsh.exe"), append([]string{"advfirewall", "firewall"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh: %w: %s", err, strings.ToValidUTF8(strings.TrimSpace(string(out)), "?"))
	}
	return nil
}

// uninstallService stops and removes the service, its event log source and its firewall rule. Its
// configuration and data stay.
func uninstallService(ctx context.Context, stdout io.Writer) error {
	m, s, err := openService()
	if err != nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	defer func() { _ = s.Close() }()
	if st, err := s.Query(); err == nil && st.State != svc.Stopped {
		if err := stop(ctx, s); err != nil {
			return err
		}
	}
	if err := s.Delete(); err != nil {
		return fmt.Errorf("removing the service: %w", err)
	}
	_ = eventlog.Remove(serviceName)
	_ = firewall(ctx, "delete", "rule", "name="+serviceName)
	root, _ := platform.ServiceRoot()
	_, _ = fmt.Fprintf(stdout, "Laterna service removed. Its configuration and data stay where they are (by default in %s).\n", root)
	return nil
}

func startService(ctx context.Context, stdout io.Writer) error {
	m, s, err := openService()
	if err != nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	defer func() { _ = s.Close() }()
	if err := s.Start(); errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		_, _ = fmt.Fprintln(stdout, "Laterna is already running.")
		return nil
	} else if err != nil {
		return fmt.Errorf("starting the service: %w", err)
	}
	st, err := waitFor(ctx, s, svc.Running, startWait)
	if err != nil {
		return err
	}
	if st.State != svc.Running {
		return errors.New("the service stopped while starting: the event log says why (Windows Logs > Application, source Laterna)")
	}
	_, _ = fmt.Fprintln(stdout, "Laterna started.")
	return nil
}

func stopService(ctx context.Context, stdout io.Writer) error {
	m, s, err := openService()
	if err != nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	defer func() { _ = s.Close() }()
	if st, err := s.Query(); err == nil && st.State == svc.Stopped {
		_, _ = fmt.Fprintln(stdout, "Laterna is not running.")
		return nil
	}
	if err := stop(ctx, s); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "Laterna stopped.")
	return nil
}

// openService opens the service; m is to disconnect, s to close.
func openService() (*mgr.Mgr, *mgr.Service, error) {
	m, err := mgr.Connect()
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return nil, nil, errAdmin
	}
	if err != nil {
		return nil, nil, err
	}
	s, err := m.OpenService(serviceName)
	if err != nil {
		_ = m.Disconnect()
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil, nil, errNotInstalled
		}
		return nil, nil, err
	}
	return m, s, nil
}

// stop asks the service to stop and waits until it has.
func stop(ctx context.Context, s *mgr.Service) error {
	if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return fmt.Errorf("stopping the service: %w", err)
	}
	_, err := waitFor(ctx, s, svc.Stopped, stopWait)
	return err
}

// waitFor waits until the service is in state want, or stopped.
func waitFor(ctx context.Context, s *mgr.Service, want svc.State, limit time.Duration) (svc.Status, error) {
	deadline := time.Now().Add(limit)
	for {
		st, err := s.Query()
		if err != nil {
			return st, err
		}
		if st.State == want || st.State == svc.Stopped {
			return st, nil
		}
		if time.Now().After(deadline) {
			return st, fmt.Errorf("the service is still %s after %s", stateName(st.State), limit)
		}
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func stateName(s svc.State) string {
	switch s {
	case svc.StartPending:
		return "starting"
	case svc.StopPending:
		return "stopping"
	case svc.Running:
		return "running"
	case svc.Stopped:
		return "stopped"
	default:
		return "in state " + strconv.Itoa(int(s))
	}
}

// runService is the command line of the service, run by the service manager: the server runs
// until it is told to stop.
func runService(args []string) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !isService {
		return errors.New("laterna service run is started by the Windows service manager: from a console, use laterna serve")
	}
	return svc.Run(serviceName, service{args: args})
}

// service runs the server for the service manager.
type service struct{ args []string }

// Execute starts the server, says the service is running once it listens, and stops it when asked.
// A server that ends on an error leaves its reason in the event log and a failure exit code: the
// service manager restarts it.
func (h service) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listening := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- h.serve(ctx, func(string) { close(listening) }) }()
	ready := listening

	// While it starts (a migration can take a while), the service manager sees it progress.
	current := svc.Status{State: svc.StartPending, WaitHint: 10_000}
	status <- current
	progress := time.NewTicker(2 * time.Second)
	defer progress.Stop()
	for {
		select {
		case <-ready:
			ready = nil
			progress.Stop()
			current = svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPreShutdown}
			status <- current
		case <-progress.C:
			if current.State == svc.StartPending {
				current.CheckPoint++
				status <- current
			}
		case r := <-requests:
			switch r.Cmd {
			case svc.Interrogate:
				status <- current
			case svc.Stop, svc.Shutdown, svc.PreShutdown:
				current = svc.Status{State: svc.StopPending, WaitHint: uint32(stopWait / time.Millisecond)}
				status <- current
				cancel()
			default:
			}
		case err := <-done:
			if err != nil {
				reportFailure(err)
				return true, 1
			}
			return false, 0
		}
	}
}

// serve reads the configuration of the service and runs the server. The folders not named by the
// configuration are those of the service, never the profile of its account.
func (h service) serve(ctx context.Context, ready func(addr string)) error {
	root, err := platform.ServiceRoot()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("service run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg, dirs, err := loadConfig(fs, h.args, where{dirs: platform.ServiceDirs, config: filepath.Join(root, "laterna.toml")})
	if err != nil {
		return err
	}
	return runServer(ctx, cfg, dirs, io.Discard, ready)
}

// reportFailure writes why the server stopped in the event log: a service has no console, and the
// error may come before the logs are open.
func reportFailure(err error) {
	l, e := eventlog.Open(serviceName)
	if e != nil {
		return
	}
	defer func() { _ = l.Close() }()
	_ = l.Error(1, "Laterna stopped on an error: "+err.Error())
}
