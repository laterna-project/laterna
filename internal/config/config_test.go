package config

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/laterna-project/laterna/internal/platform"
)

func envMap(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "laterna.toml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Address != ":8096" || cfg.Log.Level != "info" || cfg.Log.Format != "text" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if !slices.Equal(cfg.Server.CORSOrigins, []string{"*"}) {
		t.Errorf("default CORS: %v", cfg.Server.CORSOrigins)
	}
}

// Runtime settings live in the database: their old config keys are rejected like typos.
func TestHotSettingsNotInConfig(t *testing.T) {
	for _, toml := range []string{"[metadata]\ndownload_images = false\n", "[metadata]\nonline = true\n", "[server]\nname = \"Living room\"\n"} {
		if _, err := Load(writeFile(t, toml), envMap(nil)); err == nil {
			t.Errorf("%q accepted", toml)
		}
	}
}

func TestLoadFileThenEnv(t *testing.T) {
	path := writeFile(t, `
[server]
address = ":9000"

[log]
level = "debug"

[ffmpeg]
encoder = "h264_nvenc"
`)
	cfg, err := Load(path, envMap(map[string]string{
		"LATERNA_ADDRESS":        "127.0.0.1:8096",
		"LATERNA_CORS_ORIGINS":   "http://a.test, http://b.test ,",
		"LATERNA_FFMPEG_ENCODER": " libx264 ",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Address != "127.0.0.1:8096" {
		t.Errorf("the environment must win over the file: %q", cfg.Server.Address)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("values from the file lost: %+v", cfg)
	}
	if !slices.Equal(cfg.Server.CORSOrigins, []string{"http://a.test", "http://b.test"}) {
		t.Errorf("CORS origins: %v", cfg.Server.CORSOrigins)
	}
	if cfg.FFmpeg.Encoder != "libx264" {
		t.Errorf("forced encoder: %q", cfg.FFmpeg.Encoder)
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	path := writeFile(t, "[server]\nadress = \":9000\"\n")
	if _, err := Load(path, envMap(nil)); err == nil {
		t.Fatal("an unknown key (typo) must be rejected")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.toml"), envMap(nil)); err == nil {
		t.Fatal("a file that is named but missing must be an error")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"empty address", func(c *Config) { c.Server.Address = "" }, "server.address"},
		{"unknown level", func(c *Config) { c.Log.Level = "verbose" }, "log.level"},
		{"unknown format", func(c *Config) { c.Log.Format = "xml" }, "log.format"},
		{"invalid proxy", func(c *Config) { c.Server.TrustedProxies = []string{"10.0.0.0/8", "proxy.local"} }, "server.trusted_proxies"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want an error mentioning %q, got %v", tt.want, err)
			}
		})
	}
}

func TestDirsOverride(t *testing.T) {
	defaults := platform.Dirs{Data: "d", Cache: "c", Metadata: "m"}
	cfg := Default()
	cfg.Paths.Cache = "other"
	got := cfg.Dirs(defaults)
	if got != (platform.Dirs{Data: "d", Cache: "other", Metadata: "m"}) {
		t.Errorf("got %+v", got)
	}
}

func TestFFmpegNextTo(t *testing.T) {
	dir := t.TempDir()
	name := func(tool string) string {
		if runtime.GOOS == "windows" {
			tool += ".exe"
		}
		return filepath.Join(dir, tool)
	}
	if err := os.WriteFile(name("ffmpeg"), []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Only ffmpeg is shipped: ffprobe is left to PATH.
	if got := (FFmpeg{}).NextTo(dir); got.FFmpeg != name("ffmpeg") || got.FFprobe != "" {
		t.Errorf("next to the binary: %+v", got)
	}
	// What is configured wins.
	set := FFmpeg{FFmpeg: "/opt/ffmpeg", FFprobe: "/opt/ffprobe", Encoder: "libx264"}
	if got := set.NextTo(dir); got != set {
		t.Errorf("configured: %+v", got)
	}
	// Nothing there, or no folder known: PATH.
	if got := (FFmpeg{}).NextTo(t.TempDir()); got != (FFmpeg{}) {
		t.Errorf("empty folder: %+v", got)
	}
	if got := (FFmpeg{}).NextTo(""); got != (FFmpeg{}) {
		t.Errorf("unknown folder: %+v", got)
	}
}

func TestTrustedProxies(t *testing.T) {
	s := Server{TrustedProxies: []string{"127.0.0.1", " 172.16.0.0/12 ", "::1", "10.1.2.3/8"}}
	var got []string
	for _, p := range s.Proxies() {
		got = append(got, p.String())
	}
	if strings.Join(got, " ") != "127.0.0.1/32 172.16.0.0/12 ::1/128 10.0.0.0/8" {
		t.Errorf("proxies: %v", got)
	}
}

func TestDiscovery(t *testing.T) {
	cfg, err := Load("", envMap(nil))
	if err != nil || !cfg.Server.Discovery {
		t.Fatalf("discovery should be on by default: %v, %v", cfg.Server.Discovery, err)
	}
	cfg, err = Load(writeFile(t, "[server]\ndiscovery = false\n"), envMap(nil))
	if err != nil || cfg.Server.Discovery {
		t.Fatalf("turned off by the file: %v, %v", cfg.Server.Discovery, err)
	}
	cfg, err = Load(writeFile(t, "[server]\ndiscovery = false\n"), envMap(map[string]string{"LATERNA_DISCOVERY": " true "}))
	if err != nil || !cfg.Server.Discovery {
		t.Fatalf("the environment wins: %v, %v", cfg.Server.Discovery, err)
	}
	if _, err := Load("", envMap(map[string]string{"LATERNA_DISCOVERY": "yes"})); err == nil {
		t.Fatal("a value that is not a boolean must be rejected")
	}
}
