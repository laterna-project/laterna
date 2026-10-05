// Package config loads the startup configuration: defaults, then an optional TOML file, then
// LATERNA_* environment variables. Settings that can change at runtime (server name, image
// downloads, scans...) live in the database and are edited from the admin API, never here.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/laterna-project/laterna/internal/platform"
)

// DefaultAddress uses the port people know from Jellyfin, which clients suggest by default.
const DefaultAddress = ":8096"

// Config is the server's startup configuration.
type Config struct {
	Server Server `toml:"server"`
	Paths  Paths  `toml:"paths"`
	Log    Log    `toml:"log"`
	FFmpeg FFmpeg `toml:"ffmpeg"`
}

// Server configures the HTTP listener.
type Server struct {
	// Address to listen on (":8096", "127.0.0.1:8096").
	Address string `toml:"address"`
	// CORSOrigins lists the allowed origins. "*" allows all of them, which is the default: tokens
	// travel in a header, never in a cookie.
	CORSOrigins []string `toml:"cors_origins"`
	// TrustedProxies lists the reverse proxies we trust (addresses or CIDR ranges: "127.0.0.1",
	// "172.16.0.0/12"). Behind them the client address is read from X-Forwarded-For. Empty (the
	// default) means the address is the connection's and X-Forwarded-For is ignored.
	TrustedProxies []string `toml:"trusted_proxies"`
	// Discovery announces the server on the local network (mDNS, service "_laterna._tcp") so TVs
	// and apps find it without typing its address. On by default.
	Discovery bool `toml:"discovery"`
}

// Proxies returns the trusted proxies, already checked by Validate.
func (s Server) Proxies() []netip.Prefix {
	out := make([]netip.Prefix, 0, len(s.TrustedProxies))
	for _, p := range s.TrustedProxies {
		if prefix, err := parseProxy(p); err == nil {
			out = append(out, prefix)
		}
	}
	return out
}

// parseProxy reads an address ("10.0.0.2") or a range ("10.0.0.0/8").
func parseProxy(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		return p.Masked(), err
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// Paths overrides the platform's default folders.
type Paths struct {
	Data     string `toml:"data"`
	Cache    string `toml:"cache"`
	Metadata string `toml:"metadata"`
	// Backups is the folder for database backups (default: "backups" under data). Put it on another
	// disk to survive the loss of the first one.
	Backups string `toml:"backups"`
}

// Log configures logging.
type Log struct {
	// Level is debug, info, warn or error.
	Level string `toml:"level"`
	// Format of standard output: text or json. Files are always text.
	Format string `toml:"format"`
}

// FFmpeg names the executables to use; empty means look them up in PATH.
type FFmpeg struct {
	FFmpeg  string `toml:"ffmpeg"`
	FFprobe string `toml:"ffprobe"`
	// Encoder forces the H.264 encoder used for transcoding ("libx264", "h264_nvenc"...). Empty
	// means the best one that works on this machine, detected at startup.
	Encoder string `toml:"encoder"`
}

// NextTo fills in the executables that are not configured with the ones found in dir, the folder
// of the server binary. Release archives ship FFmpeg there, so they work without anything in
// PATH. What is configured always wins, and a tool that is not in dir is left to PATH.
func (f FFmpeg) NextTo(dir string) FFmpeg {
	pick := func(configured, name string) string {
		if configured != "" || dir == "" {
			return configured
		}
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err != nil || info.IsDir() {
			return ""
		}
		return p
	}
	f.FFmpeg = pick(f.FFmpeg, "ffmpeg")
	f.FFprobe = pick(f.FFprobe, "ffprobe")
	return f
}

// Dirs returns the folders in effect: the configured ones, otherwise the platform's.
func (c Config) Dirs(defaults platform.Dirs) platform.Dirs {
	d := defaults
	if c.Paths.Data != "" {
		d.Data = c.Paths.Data
	}
	if c.Paths.Cache != "" {
		d.Cache = c.Paths.Cache
	}
	if c.Paths.Metadata != "" {
		d.Metadata = c.Paths.Metadata
	}
	if c.Paths.Backups != "" {
		d.Backups = c.Paths.Backups
	}
	return d
}

// Default returns the default configuration.
func Default() Config {
	return Config{
		Server: Server{Address: DefaultAddress, CORSOrigins: []string{"*"}, Discovery: true},
		Log:    Log{Level: "info", Format: "text"},
	}
}

// Load builds the configuration. path may be empty (no file); a file that is named but missing is
// an error. getenv reads environment variables.
func Load(path string, getenv func(string) (string, bool)) (Config, error) {
	cfg := Default()
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("config: reading %s: %w", path, err)
		}
		dec := toml.NewDecoder(bytes.NewReader(raw))
		// An unknown key is a typo: reject it rather than ignore it.
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("config: %s: %w", path, err)
		}
	}
	if err := applyEnv(&cfg, getenv); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Environment variables we read (documented in the README).
func applyEnv(cfg *Config, getenv func(string) (string, bool)) error {
	str := func(name string, dst *string) {
		if v, ok := getenv(name); ok {
			*dst = strings.TrimSpace(v)
		}
	}
	str("LATERNA_ADDRESS", &cfg.Server.Address)
	str("LATERNA_DATA_DIR", &cfg.Paths.Data)
	str("LATERNA_CACHE_DIR", &cfg.Paths.Cache)
	str("LATERNA_METADATA_DIR", &cfg.Paths.Metadata)
	str("LATERNA_BACKUP_DIR", &cfg.Paths.Backups)
	str("LATERNA_LOG_LEVEL", &cfg.Log.Level)
	str("LATERNA_LOG_FORMAT", &cfg.Log.Format)
	str("LATERNA_FFMPEG", &cfg.FFmpeg.FFmpeg)
	str("LATERNA_FFPROBE", &cfg.FFmpeg.FFprobe)
	str("LATERNA_FFMPEG_ENCODER", &cfg.FFmpeg.Encoder)
	if v, ok := getenv("LATERNA_CORS_ORIGINS"); ok {
		cfg.Server.CORSOrigins = splitList(v)
	}
	if v, ok := getenv("LATERNA_TRUSTED_PROXIES"); ok {
		cfg.Server.TrustedProxies = splitList(v)
	}
	if v, ok := getenv("LATERNA_DISCOVERY"); ok {
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("invalid config: LATERNA_DISCOVERY=%q is not a boolean", v)
		}
		cfg.Server.Discovery = b
	}
	return nil
}

func splitList(v string) []string {
	var out []string
	for part := range strings.SplitSeq(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Validate checks that the configuration makes sense.
func (c Config) Validate() error {
	var errs []error
	if c.Server.Address == "" {
		errs = append(errs, errors.New("server.address is empty"))
	}
	if !slices.Contains([]string{"debug", "info", "warn", "error"}, c.Log.Level) {
		errs = append(errs, fmt.Errorf("invalid log.level %q (debug, info, warn, error)", c.Log.Level))
	}
	if !slices.Contains([]string{"text", "json"}, c.Log.Format) {
		errs = append(errs, fmt.Errorf("invalid log.format %q (text, json)", c.Log.Format))
	}
	for _, p := range c.Server.TrustedProxies {
		if _, err := parseProxy(p); err != nil {
			errs = append(errs, fmt.Errorf("server.trusted_proxies: %q is neither an address nor a CIDR network", p))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	return nil
}
