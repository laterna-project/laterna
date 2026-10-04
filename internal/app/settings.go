package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/i18n"
	"github.com/laterna-project/laterna/internal/store"
)

// Runtime settings: stored in the database, loaded at startup, changed through the admin API. A
// change applies without a restart.

// Keys of settings in the database.
const (
	keyLanguage       = "server.language"
	keyDownloadImages = "metadata.download_images"
	keyScanInterval   = "library.scan_interval"
	keyMissingGrace   = "library.missing_grace"
	keyTrickplay      = "playback.trickplay"
	keyWatchLibraries = "library.watch"
	keyDetectSegments = "playback.segments"
	keyMaxTranscodes  = "playback.max_transcodes"
	keyBackupKeep     = "backup.keep"
	keyPublicURL      = "server.public_url"
	keyWebURL         = "server.web_url"
	keyPasskeyRPID    = "passkeys.rp_id" //nolint:gosec // name of a setting, not a secret
	keyPasskeyOrigins = "passkeys.origins"
)

// Bounds for settings.
const (
	maxServerNameLen = 64
	minScanInterval  = 15 * time.Minute
	maxScanInterval  = 7 * 24 * time.Hour
	minMissingGrace  = time.Hour
	maxMissingGrace  = 90 * 24 * time.Hour
	// maxTranscodesSetting is the highest value of the concurrent transcodes setting.
	maxTranscodesSetting = 64
)

// defaultSettings is what a setting is worth when it was never changed.
func defaultSettings(serverName string) domain.Settings {
	if serverName == "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			serverName = h
		} else {
			serverName = "Laterna"
		}
	}
	return domain.Settings{ServerName: serverName, Language: string(i18n.Default), DownloadImages: true, ScanInterval: 6 * time.Hour, MissingGrace: 72 * time.Hour, Trickplay: true, WatchLibraries: true, DetectSegments: true, BackupKeep: 7}
}

// loadSettings reads the settings from the database, on top of the defaults.
func (a *App) loadSettings(ctx context.Context, defaults domain.Settings) (domain.Settings, error) {
	s := defaults
	read := a.store.Read()
	get := func(key string, apply func(string) error) error {
		v, ok, err := read.Setting(ctx, key)
		if err != nil || !ok {
			return err
		}
		if err := apply(v); err != nil {
			return fmt.Errorf("setting %s: unreadable value %q: %w", key, v, err)
		}
		return nil
	}
	duration := func(dst *time.Duration) func(string) error {
		return func(v string) (err error) { *dst, err = time.ParseDuration(v); return err }
	}
	for _, step := range []error{
		get(keyServerName, func(v string) error { s.ServerName = v; return nil }),
		get(keyLanguage, func(v string) error {
			// A language this binary no longer has: use the default one rather than refuse to
			// start.
			if lang, ok := i18n.Parse(v); ok {
				s.Language = string(lang)
			}
			return nil
		}),
		get(keyDownloadImages, func(v string) (err error) { s.DownloadImages, err = strconv.ParseBool(v); return err }),
		get(keyScanInterval, duration(&s.ScanInterval)),
		get(keyMissingGrace, duration(&s.MissingGrace)),
		get(keyTrickplay, func(v string) (err error) { s.Trickplay, err = strconv.ParseBool(v); return err }),
		get(keyWatchLibraries, func(v string) (err error) { s.WatchLibraries, err = strconv.ParseBool(v); return err }),
		get(keyDetectSegments, func(v string) (err error) { s.DetectSegments, err = strconv.ParseBool(v); return err }),
		get(keyMaxTranscodes, func(v string) (err error) { s.MaxTranscodes, err = strconv.Atoi(v); return err }),
		get(keyBackupKeep, func(v string) (err error) { s.BackupKeep, err = strconv.Atoi(v); return err }),
		get(keyPublicURL, func(v string) error { s.PublicURL = v; return nil }),
		get(keyWebURL, func(v string) error { s.WebURL = v; return nil }),
		get(keyPasskeyRPID, func(v string) error { s.PasskeyRPID = v; return nil }),
		get(keyPasskeyOrigins, func(v string) error { return json.Unmarshal([]byte(v), &s.PasskeyOrigins) }),
	} {
		if step != nil {
			return s, step
		}
	}
	return s, nil
}

// Settings returns the settings in effect.
func (a *App) Settings() domain.Settings {
	a.settingsMu.RLock()
	defer a.settingsMu.RUnlock()
	return a.settings
}

// Language returns the server language: the one used for the texts it writes when neither the
// request nor the profile asks for one it knows.
func (a *App) Language() i18n.Lang {
	if lang, ok := i18n.Parse(a.Settings().Language); ok {
		return lang
	}
	return i18n.Default
}

// SettingsChanges describes a change to the settings; a nil field is left alone.
type SettingsChanges struct {
	ServerName     *string
	Language       *string
	DownloadImages *bool
	ScanInterval   *time.Duration
	MissingGrace   *time.Duration
	Trickplay      *bool
	WatchLibraries *bool
	DetectSegments *bool
	MaxTranscodes  *int
	BackupKeep     *int
	PublicURL      *string
	WebURL         *string
	PasskeyRPID    *string
	PasskeyOrigins *[]string
}

// UpdateSettings changes settings and applies them right away.
func (a *App) UpdateSettings(ctx context.Context, p domain.Principal, ch SettingsChanges) (domain.Settings, error) {
	s := a.Settings()
	wasTrickplay, wasSegments := s.Trickplay, s.DetectSegments
	values := map[string]string{}
	var changed []domain.Text
	if ch.ServerName != nil {
		name := strings.TrimSpace(*ch.ServerName)
		switch {
		case name == "":
			return s, domain.Invalid("settings.server_name_required")
		case utf8.RuneCountInString(name) > maxServerNameLen:
			return s, domain.Invalid("settings.server_name_too_long", "max", maxServerNameLen)
		case strings.ContainsFunc(name, unicode.IsControl):
			return s, domain.Invalid("settings.server_name_invalid")
		}
		s.ServerName, values[keyServerName] = name, name
		changed = append(changed, domain.T("activity.setting.server_name", "value", name))
	}
	if ch.Language != nil {
		lang, ok := i18n.Parse(*ch.Language)
		if !ok || !strings.EqualFold(strings.TrimSpace(*ch.Language), string(lang)) {
			return s, domain.Invalid("settings.invalid_language", "language", *ch.Language, "supported", serverLanguages())
		}
		s.Language, values[keyLanguage] = string(lang), string(lang)
		changed = append(changed, domain.T("activity.setting.language", "value", lang))
	}
	if ch.DownloadImages != nil {
		s.DownloadImages, values[keyDownloadImages] = *ch.DownloadImages, strconv.FormatBool(*ch.DownloadImages)
		changed = append(changed, domain.T("activity.setting.download_images", "value", *ch.DownloadImages))
	}
	if ch.ScanInterval != nil {
		d := *ch.ScanInterval
		if d != 0 && (d < minScanInterval || d > maxScanInterval) {
			return s, domain.Invalid("settings.invalid_scan_interval", "min_seconds", minScanInterval, "max_seconds", maxScanInterval)
		}
		s.ScanInterval, values[keyScanInterval] = d, d.String()
		if d == 0 {
			changed = append(changed, domain.T("activity.setting.scan_interval_off"))
		} else {
			changed = append(changed, domain.T("activity.setting.scan_interval", "value_seconds", d))
		}
	}
	if ch.MissingGrace != nil {
		d := *ch.MissingGrace
		if d < minMissingGrace || d > maxMissingGrace {
			return s, domain.Invalid("settings.invalid_missing_grace", "min_seconds", minMissingGrace, "max_seconds", maxMissingGrace)
		}
		s.MissingGrace, values[keyMissingGrace] = d, d.String()
		changed = append(changed, domain.T("activity.setting.missing_grace", "value_seconds", d))
	}
	if ch.Trickplay != nil {
		s.Trickplay, values[keyTrickplay] = *ch.Trickplay, strconv.FormatBool(*ch.Trickplay)
		changed = append(changed, domain.T("activity.setting.trickplay", "value", *ch.Trickplay))
	}
	if ch.WatchLibraries != nil {
		s.WatchLibraries, values[keyWatchLibraries] = *ch.WatchLibraries, strconv.FormatBool(*ch.WatchLibraries)
		changed = append(changed, domain.T("activity.setting.watch_libraries", "value", *ch.WatchLibraries))
	}
	if ch.DetectSegments != nil {
		s.DetectSegments, values[keyDetectSegments] = *ch.DetectSegments, strconv.FormatBool(*ch.DetectSegments)
		changed = append(changed, domain.T("activity.setting.detect_segments", "value", *ch.DetectSegments))
	}
	if ch.MaxTranscodes != nil {
		n := *ch.MaxTranscodes
		if n < 0 || n > maxTranscodesSetting {
			return s, domain.Invalid("settings.invalid_max_transcodes", "max", maxTranscodesSetting)
		}
		s.MaxTranscodes, values[keyMaxTranscodes] = n, strconv.Itoa(n)
		if n == 0 {
			changed = append(changed, domain.T("activity.setting.max_transcodes_auto"))
		} else {
			changed = append(changed, domain.T("activity.setting.max_transcodes", "value", n))
		}
	}
	if ch.BackupKeep != nil {
		n := *ch.BackupKeep
		if n < 0 || n > maxBackupKeep {
			return s, domain.Invalid("settings.invalid_backup_keep", "max", maxBackupKeep)
		}
		s.BackupKeep, values[keyBackupKeep] = n, strconv.Itoa(n)
		if n == 0 {
			changed = append(changed, domain.T("activity.setting.backup_off"))
		} else {
			changed = append(changed, domain.T("activity.setting.backup_keep", "value", n))
		}
	}
	if ch.PublicURL != nil {
		u, err := publicURL(*ch.PublicURL)
		if err != nil {
			return s, err
		}
		s.PublicURL, values[keyPublicURL] = u, u
		changed = append(changed, domain.T("activity.setting.public_url", "value", u))
	}
	if ch.WebURL != nil {
		u, ok := siteURL(*ch.WebURL)
		if !ok {
			return s, domain.Invalid("settings.invalid_web_url")
		}
		s.WebURL, values[keyWebURL] = u, u
		changed = append(changed, domain.T("activity.setting.web_url", "value", u))
	}
	if ch.PasskeyRPID != nil {
		id := strings.ToLower(strings.TrimSpace(*ch.PasskeyRPID))
		if id != "" && !validHost(id) {
			return s, domain.Invalid("settings.invalid_passkey_rp_id")
		}
		s.PasskeyRPID, values[keyPasskeyRPID] = id, id
		changed = append(changed, domain.T("activity.setting.passkey_rp_id", "value", id))
	}
	if ch.PasskeyOrigins != nil {
		origins, err := passkeyOrigins(*ch.PasskeyOrigins)
		if err != nil {
			return s, err
		}
		b, _ := json.Marshal(origins) // strings: cannot fail
		s.PasskeyOrigins, values[keyPasskeyOrigins] = origins, string(b)
		changed = append(changed, domain.T("activity.setting.passkey_origins", "value", strings.Join(origins, ", ")))
	}
	if len(values) == 0 {
		return s, nil
	}
	if err := a.store.Write(ctx, func(q store.Q) error {
		for k, v := range values {
			if err := q.SetSetting(ctx, k, v); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return a.Settings(), err
	}
	a.settingsMu.Lock()
	a.settings = s
	a.settingsMu.Unlock()
	// The scheduler reads the scan interval again.
	select {
	case a.settingsChanged <- struct{}{}:
	default:
	}
	// Folder watching follows its setting.
	a.resyncWatches()
	// Thumbnails or segment detection turned back on: the scan queues what is missing for the files
	// that arrived in the meantime.
	if ch.Trickplay != nil && *ch.Trickplay && !wasTrickplay || ch.DetectSegments != nil && *ch.DetectSegments && !wasSegments {
		a.enqueueAllScans(ctx, priorityBackground)
	}
	a.log.InfoContext(ctx, "settings updated", "changes", changed)
	a.record(ctx, domain.Activity{
		Kind: domain.ActivitySettingsUpdated, AccountID: &p.Account.ID,
		Text: domain.T("activity.settings_updated", "actor", p.Account.Username, changed),
	})
	return s, nil
}

// serverLanguages lists the server languages for a message ("en, fr").
func serverLanguages() string {
	langs := i18n.Languages()
	names := make([]string, len(langs))
	for i, l := range langs {
		names[i] = string(l)
	}
	return strings.Join(names, ", ")
}

// publicURL validates the server's public URL: http(s), a host, optionally a path (server behind a
// proxy), no query or fragment; empty means unknown.
func publicURL(raw string) (string, error) {
	u, ok := siteURL(raw)
	if !ok {
		return "", domain.Invalid("settings.invalid_public_url")
	}
	return u, nil
}

// siteURL validates the address of a site (the server, the web client): http(s), a host, optionally
// a path, no query or fragment. It is returned without a trailing slash. Empty means unknown, and
// is valid.
func siteURL(raw string) (string, bool) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", true
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || !validHost(u.Hostname()) {
		return "", false
	}
	return raw, true
}

// passkeyOrigins validates the allowed passkey origins: "https://host[:port]" (http only for
// localhost) or "android:apk-key-hash:..."; ten at most.
func passkeyOrigins(list []string) ([]string, error) {
	if len(list) > 10 {
		return nil, domain.Invalid("settings.too_many_passkey_origins")
	}
	out := []string{}
	for _, o := range list {
		o = strings.TrimRight(strings.TrimSpace(o), "/")
		if strings.HasPrefix(o, "android:apk-key-hash:") && len(o) > len("android:apk-key-hash:") {
			out = append(out, o)
			continue
		}
		u, err := url.Parse(o)
		local := u != nil && u.Scheme == "http" && u.Hostname() == "localhost"
		if err != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.User != nil || !validHost(u.Hostname()) ||
			(u.Scheme != "https" && !local) {
			return nil, domain.Invalid("settings.invalid_passkey_origin", "origin", o)
		}
		out = append(out, u.Scheme+"://"+u.Host)
	}
	return out, nil
}

// validHost accepts a host name (letters, digits, dashes, dots) or an IP address.
func validHost(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	return !strings.ContainsFunc(h, func(r rune) bool {
		letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		return !letter && r != '-' && r != '.' && r != ':'
	})
}
