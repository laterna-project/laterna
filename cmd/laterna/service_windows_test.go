package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/laterna-project/laterna/internal/config"
)

// The configuration written for the service names its folders, under %ProgramData%\Laterna, and
// one written earlier stays as it is.
func TestServiceConfig(t *testing.T) {
	for _, base := range []string{t.TempDir(), filepath.Join(t.TempDir(), "Kévin's data")} {
		t.Setenv("ProgramData", base)
		file := filepath.Join(base, "Laterna", "laterna.toml")
		if err := writeServiceConfig(file); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load(file, func(string) (string, bool) { return "", false })
		if err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(base, "Laterna")
		if cfg.Paths.Data != filepath.Join(root, "data") || cfg.Paths.Cache != filepath.Join(root, "cache") ||
			cfg.Paths.Metadata != filepath.Join(root, "metadata") || cfg.Server.Address != ":8096" {
			t.Errorf("%s: %+v", base, cfg)
		}
		if err := os.WriteFile(file, []byte("[server]\naddress = \":9000\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeServiceConfig(file); err != nil {
			t.Fatal(err)
		}
		if raw, _ := os.ReadFile(file); !strings.Contains(string(raw), ":9000") {
			t.Errorf("configuration written again: %s", raw)
		}
	}
}

// A short name ("RUNNER~1") comes back long: the firewall resolves no other.
func TestLongPath(t *testing.T) {
	long := filepath.Join(t.TempDir(), "Laterna media server")
	if err := os.Mkdir(long, 0o750); err != nil {
		t.Fatal(err)
	}
	p, err := windows.UTF16PtrFromString(long)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, windows.MAX_PATH)
	n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil {
		t.Fatal(err)
	}
	short := windows.UTF16ToString(buf[:n])
	if got := longPath(short); !strings.EqualFold(got, longPath(long)) || !strings.HasSuffix(got, "Laterna media server") {
		t.Errorf("long path of %s: %s", short, got)
	}
	if got := longPath(`C:\no\such\folder`); got != `C:\no\such\folder` {
		t.Errorf("missing path: %s", got)
	}
}

func TestServiceCommand(t *testing.T) {
	ctx := context.Background()
	for _, args := range [][]string{nil, {"dance"}, {"start", "now"}} {
		var out bytes.Buffer
		if err := serviceCommand(ctx, args, &out, &out); err == nil {
			t.Errorf("service %v: no error", args)
		}
	}
	var out bytes.Buffer
	if err := serviceCommand(ctx, []string{"help"}, &out, &out); err != nil || !strings.Contains(out.String(), "uninstall") {
		t.Errorf("help: %v %q", err, out.String())
	}
	if err := serviceCommand(ctx, []string{"run"}, &out, &out); err == nil || !strings.Contains(err.Error(), "laterna serve") {
		t.Errorf("run from a console: %v", err)
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		if err := serviceCommand(ctx, []string{"stop"}, &out, &out); !errors.Is(err, errAdmin) {
			t.Errorf("stop without being an administrator: %v", err)
		}
	}
}

// The Windows service end to end: installed, started, it answers on /health and writes its logs,
// then it stops, fails to start on a broken configuration, and goes away. It installs a real
// service and a firewall rule, so it only runs with LATERNA_SERVICE_TEST set, on a CI runner.
func TestWindowsService(t *testing.T) {
	if os.Getenv("LATERNA_SERVICE_TEST") == "" {
		t.Skip("installs a real service: set LATERNA_SERVICE_TEST=1, as an administrator")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("LATERNA_SERVICE_TEST needs an administrator")
	}
	m, err := mgr.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Disconnect() }()
	if s, err := m.OpenService(serviceName); err == nil {
		_ = s.Close()
		t.Fatal("a Laterna service is already installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	// A space in the path, as in Program Files.
	exe := filepath.Join(dir, "Laterna server", "laterna.exe")
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", exe, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	laterna := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, exe, args...).CombinedOutput()
		return string(out), err
	}
	t.Cleanup(func() { _, _ = laterna("service", "uninstall") })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := ln.Addr().String()
	_ = ln.Close()
	data := filepath.Join(dir, "data")
	cfgFile := filepath.Join(dir, "laterna.toml")
	cfg := "[server]\naddress = \"" + address + "\"\ndiscovery = false\n\n[paths]\ndata = " + tomlString(data) +
		"\ncache = " + tomlString(filepath.Join(dir, "cache")) + "\nmetadata = " + tomlString(filepath.Join(dir, "metadata")) + "\n"
	if err := os.WriteFile(cfgFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := laterna("service", "install", "-config", cfgFile)
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if out, err := laterna("service", "install", "-config", cfgFile); err == nil || !strings.Contains(out, "already installed") {
		t.Errorf("installed twice: %v %s", err, out)
	}
	s, err := m.OpenService(serviceName)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Config()
	if err != nil {
		t.Fatal(err)
	}
	actions, err := s.RecoveryActions()
	_ = s.Close()
	if err != nil {
		t.Fatal(err)
	}
	if c.ServiceStartName != serviceAccount || c.StartType != mgr.StartAutomatic || !c.DelayedAutoStart ||
		!strings.Contains(c.BinaryPathName, "service run -config") || len(actions) != 3 {
		t.Errorf("service: %+v, recovery %v", c, actions)
	}
	if rule, err := firewallRule(ctx); err != nil || !strings.Contains(strings.ToLower(rule), strings.ToLower(longPath(exe))) {
		t.Errorf("firewall rule: %v\n%s\ninstall said:\n%s", err, rule, out)
	}

	// Started: it answers and writes its logs where the configuration says.
	if out, err := laterna("service", "start"); err != nil {
		t.Fatalf("start: %v\n%s", err, out)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("health: %d", resp.StatusCode)
	}
	if logs, _ := filepath.Glob(filepath.Join(data, "log", "laterna_*.log")); len(logs) == 0 {
		t.Error("no log file")
	}
	if out, err := laterna("service", "stop"); err != nil {
		t.Fatalf("stop: %v\n%s", err, out)
	}
	if st := query(t, m); st.State != svc.Stopped {
		t.Errorf("after stop: %v", st.State)
	}
	if out, err := laterna("service", "stop"); err != nil || !strings.Contains(out, "not running") {
		t.Errorf("stopped twice: %v %s", err, out)
	}

	// A configuration the server refuses: start says so.
	if err := os.WriteFile(cfgFile, []byte(cfg+"typo = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := laterna("service", "start"); err == nil || !strings.Contains(out, "event log") {
		t.Errorf("start on a broken configuration: %v %s", err, out)
	}

	// Removed, its rule with it; its data stays.
	if out, err := laterna("service", "uninstall"); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if _, err := m.OpenService(serviceName); !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		t.Errorf("after uninstall: %v", err)
	}
	if _, err := firewallRule(ctx); err == nil {
		t.Error("firewall rule left")
	}
	if _, err := os.Stat(filepath.Join(data, "laterna.db")); err != nil {
		t.Errorf("data removed: %v", err)
	}
}

func query(t *testing.T, m *mgr.Mgr) svc.Status {
	t.Helper()
	s, err := m.OpenService(serviceName)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		st, err := s.Query()
		if err != nil {
			t.Fatal(err)
		}
		if st.State != svc.StopPending || time.Now().After(deadline) {
			return st
		}
	}
}

// firewallRule describes the firewall rule of the service; an error if there is none.
func firewallRule(ctx context.Context) (string, error) {
	sys, err := windows.GetSystemDirectory()
	if err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, filepath.Join(sys, "netsh.exe"), "advfirewall", "firewall", "show", "rule", "name="+serviceName, "verbose").CombinedOutput()
	if err != nil {
		return string(out), errors.New("netsh: " + strconv.Quote(strings.TrimSpace(string(out))))
	}
	return string(out), nil
}
