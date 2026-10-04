package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/media/images"
	"github.com/laterna-project/laterna/internal/store"
)

// Themes. A theme is a set of design tokens (dark and light palettes, radius, density, font) that
// each client maps to its own UI; never CSS. Built-in themes are written at startup. An
// administrator creates their own, exports and imports them, and picks the server's theme. Each
// profile picks its own theme and mode. A theme that cannot be read (insufficient contrast, WCAG
// AA) is rejected.

const (
	keyDefaultTheme = "theme.default"
	maxThemes       = 100
	maxThemeName    = 64
	// maxThemeImage is the maximum size of an uploaded logo or background.
	maxThemeImage = 8 << 20
	// themeFormat is the format name in a theme export file.
	themeFormat  = "laterna-theme"
	themeVersion = 1
)

// mustThemeID builds the fixed ID of a built-in theme.
func mustThemeID(s string) domain.ID {
	id, err := domain.ParseID(s)
	if err != nil {
		panic(err)
	}
	return id
}

// ThemeLaterna is the default built-in theme.
var ThemeLaterna = mustThemeID("0192f1c0000070008000000000000001")

// builtinNames holds the name of each built-in theme, to be translated.
var builtinNames = map[domain.ID]domain.Text{
	ThemeLaterna: domain.T("theme.name.laterna"),
	mustThemeID("0192f1c0000070008000000000000002"): domain.T("theme.name.ocean"),
	mustThemeID("0192f1c0000070008000000000000003"): domain.T("theme.name.forest"),
	mustThemeID("0192f1c0000070008000000000000004"): domain.T("theme.name.high_contrast"),
}

// ThemeName returns the name of a built-in theme, to be translated; false for a theme created by an
// administrator, whose name is its own.
func ThemeName(t domain.Theme) (domain.Text, bool) {
	name, ok := builtinNames[t.ID]
	return name, ok && t.BuiltIn
}

// builtinThemes are the themes that ship with Laterna, all checked like any other (test).
var builtinThemes = []domain.Theme{
	{
		ID: ThemeLaterna, Name: "Laterna",
		Tokens: domain.ThemeTokens{
			Dark: domain.Palette{
				Background: "#0f1115", Surface: "#181b21", SurfaceRaised: "#232730", Text: "#eef0f3", TextMuted: "#a3a9b4",
				Accent: "#e8b04a", OnAccent: "#1c1400", Outline: "#2f343d", Error: "#ff6b6b", OnError: "#1f0505",
				Success: "#4cc38a", Warning: "#f2c94c",
			},
			Light: domain.Palette{
				Background: "#f6f5f2", Surface: "#ffffff", SurfaceRaised: "#eceae5", Text: "#16181d", TextMuted: "#545a65",
				Accent: "#8a5a00", OnAccent: "#ffffff", Outline: "#d5d2cb", Error: "#b3261e", OnError: "#ffffff",
				Success: "#1b7a4a", Warning: "#8f5d00",
			},
			Radius: 12, Density: domain.DensityComfortable, Font: domain.FontInter,
		},
	},
	{
		ID: mustThemeID("0192f1c0000070008000000000000002"), Name: "Ocean",
		Tokens: domain.ThemeTokens{
			Dark: domain.Palette{
				Background: "#0b1220", Surface: "#121b2d", SurfaceRaised: "#1b2740", Text: "#e8eef8", TextMuted: "#9fb0c8",
				Accent: "#5aa9ff", OnAccent: "#04121f", Outline: "#26344f", Error: "#ff7a7a", OnError: "#1f0505",
				Success: "#46c79a", Warning: "#f0c35a",
			},
			Light: domain.Palette{
				Background: "#f3f6fb", Surface: "#ffffff", SurfaceRaised: "#e6ecf5", Text: "#0f1a2b", TextMuted: "#4b5a70",
				Accent: "#1259b8", OnAccent: "#ffffff", Outline: "#cdd6e4", Error: "#b3261e", OnError: "#ffffff",
				Success: "#17775a", Warning: "#8a5a00",
			},
			Radius: 16, Density: domain.DensityComfortable, Font: domain.FontInter,
		},
	},
	{
		ID: mustThemeID("0192f1c0000070008000000000000003"), Name: "Forest",
		Tokens: domain.ThemeTokens{
			Dark: domain.Palette{
				Background: "#0e1411", Surface: "#151d18", SurfaceRaised: "#1f2a23", Text: "#e9f1ec", TextMuted: "#a0b3a7",
				Accent: "#6fcf8f", OnAccent: "#062312", Outline: "#2a382f", Error: "#ff7a7a", OnError: "#1f0505",
				Success: "#6fcf8f", Warning: "#e9c46a",
			},
			Light: domain.Palette{
				Background: "#f4f7f3", Surface: "#ffffff", SurfaceRaised: "#e7eee6", Text: "#14201a", TextMuted: "#4d5f53",
				Accent: "#1f6b3a", OnAccent: "#ffffff", Outline: "#cfdccf", Error: "#b3261e", OnError: "#ffffff",
				Success: "#1f6b3a", Warning: "#8a5a00",
			},
			Radius: 8, Density: domain.DensityComfortable, Font: domain.FontLexend,
		},
	},
	{
		ID: mustThemeID("0192f1c0000070008000000000000004"), Name: "High contrast",
		Tokens: domain.ThemeTokens{
			Dark: domain.Palette{
				Background: "#000000", Surface: "#000000", SurfaceRaised: "#1a1a1a", Text: "#ffffff", TextMuted: "#e0e0e0",
				Accent: "#ffd60a", OnAccent: "#000000", Outline: "#ffffff", Error: "#ff8a80", OnError: "#000000",
				Success: "#69f0ae", Warning: "#ffd60a",
			},
			Light: domain.Palette{
				Background: "#ffffff", Surface: "#ffffff", SurfaceRaised: "#ececec", Text: "#000000", TextMuted: "#262626",
				Accent: "#0038a8", OnAccent: "#ffffff", Outline: "#000000", Error: "#9b0000", OnError: "#ffffff",
				Success: "#005c2b", Warning: "#6b4300",
			},
			Radius: 4, Density: domain.DensitySpacious, Font: domain.FontAtkinson,
		},
	},
}

// loadThemes writes the built-in themes (at startup: a new version updates them).
func (a *App) loadThemes(ctx context.Context) error {
	now := a.now()
	return a.store.Write(ctx, func(q store.Q) error {
		for _, t := range builtinThemes {
			t.BuiltIn = true
			if err := q.UpsertBuiltinTheme(ctx, t, now); err != nil {
				return fmt.Errorf("built-in theme %s: %w", t.Name, err)
			}
		}
		return nil
	})
}

// Themes lists the themes, built-in first.
func (a *App) Themes(ctx context.Context) ([]domain.Theme, error) {
	return a.store.Read().Themes(ctx)
}

// DefaultThemeID returns the server's theme.
func (a *App) DefaultThemeID(ctx context.Context) (domain.ID, error) {
	raw, ok, err := a.store.Read().Setting(ctx, keyDefaultTheme)
	if err != nil || !ok {
		return ThemeLaterna, err
	}
	id, err := domain.ParseID(raw)
	if err != nil {
		return ThemeLaterna, nil //nolint:nilerr // unreadable setting: the built-in theme
	}
	return id, nil
}

// DefaultTheme returns the server's theme (the one of the sign-in screen).
func (a *App) DefaultTheme(ctx context.Context) (domain.Theme, error) {
	id, err := a.DefaultThemeID(ctx)
	if err != nil {
		return domain.Theme{}, err
	}
	return a.themeOrDefault(ctx, &id)
}

// themeOrDefault reads a theme. If it is gone (or id is nil) it returns the server's theme, and as
// a last resort the built-in one.
func (a *App) themeOrDefault(ctx context.Context, id *domain.ID) (domain.Theme, error) {
	read := a.store.Read()
	if id != nil {
		t, err := read.Theme(ctx, *id)
		if err == nil || !store.IsNotFound(err) {
			return t, err
		}
	}
	def, err := a.DefaultThemeID(ctx)
	if err != nil {
		return domain.Theme{}, err
	}
	if id == nil || *id != def {
		t, err := read.Theme(ctx, def)
		if err == nil || !store.IsNotFound(err) {
			return t, err
		}
	}
	return read.Theme(ctx, ThemeLaterna)
}

// ProfileTheme returns the theme to show for the caller's profile (its choice, otherwise the
// server's), the chosen mode, and whether the theme is the server's for lack of a choice.
func (a *App) ProfileTheme(ctx context.Context, p domain.Principal) (domain.Theme, domain.ThemeMode, error) {
	if p.Profile == nil {
		return domain.Theme{}, "", domain.Precondition("profile.required")
	}
	profile, _, err := a.store.Read().Profile(ctx, p.Profile.ID)
	if err != nil {
		return domain.Theme{}, "", err
	}
	t, err := a.themeOrDefault(ctx, profile.ThemeID)
	mode := profile.ThemeMode
	if !mode.Valid() {
		mode = domain.ThemeAuto
	}
	return t, mode, err
}

// SetProfileTheme sets the theme of the caller's profile (nil for the server's) and its mode. Any
// profile can, even a restricted one: it only chooses among the server's themes.
func (a *App) SetProfileTheme(ctx context.Context, p domain.Principal, themeID *domain.ID, mode domain.ThemeMode) error {
	if p.Profile == nil {
		return domain.Precondition("profile.required")
	}
	if mode == "" {
		mode = domain.ThemeAuto
	}
	if !mode.Valid() {
		return domain.Invalid("theme.unknown_mode", "mode", mode)
	}
	err := a.store.Write(ctx, func(q store.Q) error {
		if themeID != nil {
			if _, err := q.Theme(ctx, *themeID); store.IsNotFound(err) {
				return domain.NotFound("theme.not_found")
			} else if err != nil {
				return err
			}
		}
		return q.SetProfileTheme(ctx, p.Profile.ID, themeID, mode, a.now())
	})
	if err != nil {
		return err
	}
	profile := p.Profile.ID
	a.bus.Publish(domain.ThemesChanged{ProfileID: &profile})
	return nil
}

// checkTheme validates the name and tokens of a theme and normalizes the palettes.
func checkTheme(name string, tokens domain.ThemeTokens) (string, domain.ThemeTokens, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxThemeName || strings.ContainsFunc(name, func(r rune) bool { return r < ' ' }) {
		return "", tokens, domain.Invalid("theme.invalid_name", "max", maxThemeName)
	}
	tokens.Dark, tokens.Light = tokens.Dark.Normalize(), tokens.Light.Normalize()
	if problems := tokens.Problems(); len(problems) > 0 {
		return "", tokens, domain.Invalid("theme.rejected", problems)
	}
	return name, tokens, nil
}

func themeWriteError(err error, name string) error {
	if errors.Is(err, store.ErrDuplicate) {
		return domain.Conflict("theme.name_taken", "name", name)
	}
	return err
}

// CreateTheme creates a theme.
func (a *App) CreateTheme(ctx context.Context, p domain.Principal, name string, tokens domain.ThemeTokens) (domain.Theme, error) {
	name, tokens, err := checkTheme(name, tokens)
	if err != nil {
		return domain.Theme{}, err
	}
	now := a.now()
	t := domain.Theme{ID: domain.NewID(), Name: name, Tokens: tokens, CreatedAt: now, UpdatedAt: now}
	err = a.store.Write(ctx, func(q store.Q) error {
		all, err := q.Themes(ctx)
		if err != nil {
			return err
		}
		if len(all) >= maxThemes {
			return domain.Precondition("theme.too_many", "max", maxThemes)
		}
		return themeWriteError(q.CreateTheme(ctx, t), name)
	})
	if err != nil {
		return domain.Theme{}, err
	}
	a.record(ctx, domain.Activity{Kind: domain.ActivitySettingsUpdated, AccountID: &p.Account.ID, Text: domain.T("activity.theme_created", "name", name)})
	return t, nil
}

// UpdateTheme replaces the name and tokens of a theme (not a built-in one).
func (a *App) UpdateTheme(ctx context.Context, p domain.Principal, id domain.ID, name string, tokens domain.ThemeTokens) (domain.Theme, error) {
	name, tokens, err := checkTheme(name, tokens)
	if err != nil {
		return domain.Theme{}, err
	}
	var t domain.Theme
	err = a.store.Write(ctx, func(q store.Q) error {
		var err error
		if t, err = a.editableTheme(ctx, q, id); err != nil {
			return err
		}
		t.Name, t.Tokens, t.UpdatedAt = name, tokens, a.now()
		return themeWriteError(q.UpdateTheme(ctx, t), name)
	})
	if err != nil {
		return domain.Theme{}, err
	}
	a.bus.Publish(domain.ThemesChanged{})
	a.record(ctx, domain.Activity{Kind: domain.ActivitySettingsUpdated, AccountID: &p.Account.ID, Text: domain.T("activity.theme_updated", "name", name)})
	return t, nil
}

// editableTheme reads a theme that can be edited.
func (a *App) editableTheme(ctx context.Context, q store.Q, id domain.ID) (domain.Theme, error) {
	t, err := q.Theme(ctx, id)
	if store.IsNotFound(err) {
		return domain.Theme{}, domain.NotFound("theme.not_found")
	}
	if err != nil {
		return domain.Theme{}, err
	}
	if t.BuiltIn {
		return domain.Theme{}, domain.Precondition("theme.builtin_read_only")
	}
	return t, nil
}

// DeleteTheme deletes a theme (not a built-in one). Profiles that had picked it go back to the
// server's theme; if it was the server's, the built-in theme replaces it.
func (a *App) DeleteTheme(ctx context.Context, p domain.Principal, id domain.ID) error {
	var t domain.Theme
	err := a.store.Write(ctx, func(q store.Q) error {
		var err error
		if t, err = a.editableTheme(ctx, q, id); err != nil {
			return err
		}
		if _, err := q.DeleteTheme(ctx, id); err != nil {
			return err
		}
		if raw, ok, err := q.Setting(ctx, keyDefaultTheme); err != nil {
			return err
		} else if ok && raw == id.String() {
			return q.DeleteSetting(ctx, keyDefaultTheme)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, img := range []*domain.Image{t.Logo, t.Background} {
		if img != nil {
			_ = os.Remove(img.Path)
		}
	}
	_ = os.Remove(a.themeDir(id))
	a.bus.Publish(domain.ThemesChanged{})
	a.record(ctx, domain.Activity{Kind: domain.ActivitySettingsUpdated, AccountID: &p.Account.ID, Text: domain.T("activity.theme_deleted", "name", t.Name)})
	return nil
}

// SetDefaultTheme picks the server's theme.
func (a *App) SetDefaultTheme(ctx context.Context, p domain.Principal, id domain.ID) error {
	var t domain.Theme
	err := a.store.Write(ctx, func(q store.Q) error {
		var err error
		if t, err = q.Theme(ctx, id); store.IsNotFound(err) {
			return domain.NotFound("theme.not_found")
		} else if err != nil {
			return err
		}
		return q.SetSetting(ctx, keyDefaultTheme, id.String())
	})
	if err != nil {
		return err
	}
	a.bus.Publish(domain.ThemesChanged{})
	a.record(ctx, domain.Activity{Kind: domain.ActivitySettingsUpdated, AccountID: &p.Account.ID, Text: domain.T("activity.theme_default", "name", t.Name)})
	return nil
}

func (a *App) themeDir(id domain.ID) string {
	return filepath.Join(a.metadataDir, "themes", id.String())
}

// SetThemeImage replaces the logo (domain.ImageLogo) or the background (domain.ImageBackdrop) of a
// theme; empty data removes it. JPEG, PNG or WebP, 8 MiB at most.
func (a *App) SetThemeImage(ctx context.Context, p domain.Principal, id domain.ID, kind domain.ImageKind, data []byte) (domain.Theme, error) {
	if kind != domain.ImageLogo && kind != domain.ImageBackdrop {
		return domain.Theme{}, domain.Invalid("theme.unknown_image_kind", "kind", kind)
	}
	if _, err := a.editableTheme(ctx, a.store.Read(), id); err != nil {
		return domain.Theme{}, err
	}
	var img *domain.Image
	if len(data) > 0 {
		var err error
		if img, err = a.writeThemeImage(id, kind, data); err != nil {
			return domain.Theme{}, err
		}
	}
	var old *domain.Image
	err := a.store.Write(ctx, func(q store.Q) error {
		prev, err := q.ThemeImage(ctx, id, kind)
		switch {
		case err == nil:
			old = &prev
			if err := q.DeleteImage(ctx, prev.ID); err != nil {
				return err
			}
		case !store.IsNotFound(err):
			return err
		}
		if img == nil {
			return nil
		}
		return q.AddThemeImage(ctx, *img)
	})
	if err != nil {
		if img != nil {
			_ = os.Remove(img.Path)
		}
		return domain.Theme{}, err
	}
	if old != nil && (img == nil || old.Path != img.Path) {
		_ = os.Remove(old.Path)
	}
	a.bus.Publish(domain.ThemesChanged{})
	a.record(ctx, domain.Activity{Kind: domain.ActivitySettingsUpdated, AccountID: &p.Account.ID, Text: domain.T("activity.theme_image_changed")})
	return a.store.Read().Theme(ctx, id)
}

// writeThemeImage checks and stores a theme image, then analyzes it.
func (a *App) writeThemeImage(id domain.ID, kind domain.ImageKind, data []byte) (*domain.Image, error) {
	if len(data) > maxThemeImage {
		return nil, domain.Invalid("theme.image_too_large", "max_bytes", maxThemeImage)
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	ext := map[string]string{"jpeg": ".jpg", "png": ".png", "webp": ".webp"}[format]
	if err != nil || ext == "" {
		return nil, domain.Invalid("theme.image_unreadable")
	}
	dir := a.themeDir(id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, string(kind)+"-*"+ext)
	if err != nil {
		return nil, err
	}
	path := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	an, err := images.Analyze(path)
	if err != nil {
		_ = os.Remove(path)
		return nil, domain.Invalid("theme.image_unreadable", "reason", err)
	}
	return &domain.Image{
		ID: domain.NewID(), ThemeID: &id, Kind: kind, Source: domain.ImageUpload, Path: path,
		Width: an.Width, Height: an.Height, BlurHash: an.BlurHash, Hash: an.Hash, UpdatedAt: a.now(),
	}, nil
}

// themeFile is an exported theme: its tokens and images in a single JSON file.
type themeFile struct {
	Format     string             `json:"format"`
	Version    int                `json:"version"`
	Name       string             `json:"name"`
	Tokens     domain.ThemeTokens `json:"tokens"`
	Logo       string             `json:"logo,omitempty"`
	Background string             `json:"background,omitempty"`
}

// ExportTheme returns a theme as a JSON file that can be imported on another server, and its name.
func (a *App) ExportTheme(ctx context.Context, id domain.ID) ([]byte, string, error) {
	t, err := a.store.Read().Theme(ctx, id)
	if store.IsNotFound(err) {
		return nil, "", domain.NotFound("theme.not_found")
	}
	if err != nil {
		return nil, "", err
	}
	f := themeFile{Format: themeFormat, Version: themeVersion, Name: t.Name, Tokens: t.Tokens}
	for _, img := range []struct {
		src *domain.Image
		dst *string
	}{{t.Logo, &f.Logo}, {t.Background, &f.Background}} {
		if img.src == nil {
			continue
		}
		data, err := os.ReadFile(img.src.Path)
		if err != nil {
			return nil, "", fmt.Errorf("theme image: %w", err)
		}
		*img.dst = base64.StdEncoding.EncodeToString(data)
	}
	data, err := json.MarshalIndent(f, "", "  ")
	return data, t.Name, err
}

// ImportTheme creates a theme from an exported file. A name that is already taken gets a number.
func (a *App) ImportTheme(ctx context.Context, p domain.Principal, data []byte) (domain.Theme, error) {
	var f themeFile
	if err := json.Unmarshal(data, &f); err != nil || f.Format != themeFormat {
		return domain.Theme{}, domain.Invalid("theme.file_unreadable")
	}
	if f.Version > themeVersion {
		return domain.Theme{}, domain.Invalid("theme.file_too_recent", "version", f.Version)
	}
	pictures := map[domain.ImageKind][]byte{}
	for kind, raw := range map[domain.ImageKind]string{domain.ImageLogo: f.Logo, domain.ImageBackdrop: f.Background} {
		if raw == "" {
			continue
		}
		img, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return domain.Theme{}, domain.Invalid("theme.image_unreadable")
		}
		pictures[kind] = img
	}
	existing, err := a.Themes(ctx)
	if err != nil {
		return domain.Theme{}, err
	}
	name := strings.TrimSpace(f.Name)
	for n := 2; nameTaken(existing, name); n++ {
		name = fmt.Sprintf("%s (%d)", strings.TrimSpace(f.Name), n)
	}
	t, err := a.CreateTheme(ctx, p, name, f.Tokens)
	if err != nil {
		return domain.Theme{}, err
	}
	for kind, img := range pictures {
		if t, err = a.SetThemeImage(ctx, p, t.ID, kind, img); err != nil {
			_ = a.DeleteTheme(ctx, p, t.ID)
			return domain.Theme{}, err
		}
	}
	return t, nil
}

func nameTaken(themes []domain.Theme, name string) bool {
	for _, t := range themes {
		if store.NameKey(t.Name) == store.NameKey(name) {
			return true
		}
	}
	return false
}
