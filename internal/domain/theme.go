package domain

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Themes are design tokens that each client maps to its own UI (CSS variables, Compose, SwiftUI).
// There is no CSS on purpose: a theme works on every client, survives their updates, and its
// legibility can be checked.

// Theme is a named set of tokens with optional images.
type Theme struct {
	ID   ID
	Name string
	// BuiltIn themes ship with Laterna and cannot be edited or deleted.
	BuiltIn bool
	Tokens  ThemeTokens
	// Logo and Background are optional (server logo, home background).
	Logo, Background *Image
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// ThemeTokens are the tokens of a theme.
type ThemeTokens struct {
	Dark  Palette `json:"dark"`
	Light Palette `json:"light"`
	// Radius is the corner radius in points (0 to 24).
	Radius  int          `json:"radius"`
	Density ThemeDensity `json:"density"`
	Font    ThemeFont    `json:"font"`
}

// Palette gives each role a color, as "#rrggbb".
type Palette struct {
	// Background is the app background, Surface is for cards and panels, SurfaceRaised for menus,
	// dialogs and hovered items.
	Background    string `json:"background"`
	Surface       string `json:"surface"`
	SurfaceRaised string `json:"surface_raised"`
	// Text is the main text color, TextMuted the secondary one. Both must stay readable.
	Text      string `json:"text"`
	TextMuted string `json:"text_muted"`
	// Accent is for buttons, selection and progress; OnAccent is the text drawn on it.
	Accent   string `json:"accent"`
	OnAccent string `json:"on_accent"`
	// Outline is for borders and dividers. Decorative, so no contrast requirement.
	Outline string `json:"outline"`
	// Error and OnError, then Success and Warning, are the status colors.
	Error   string `json:"error"`
	OnError string `json:"on_error"`
	Success string `json:"success"`
	Warning string `json:"warning"`
}

// ThemeDensity sets how much spacing the UI uses.
type ThemeDensity string

// Densities.
const (
	DensityCompact     ThemeDensity = "compact"
	DensityComfortable ThemeDensity = "comfortable"
	DensitySpacious    ThemeDensity = "spacious"
)

// ThemeFont is a font from a fixed list. Each client bundles them, or falls back to the system
// font.
type ThemeFont string

// Fonts.
const (
	FontSystem ThemeFont = "system"
	FontInter  ThemeFont = "inter"
	// FontAtkinson is Atkinson Hyperlegible, designed for low-vision readers.
	FontAtkinson ThemeFont = "atkinson"
	FontLexend   ThemeFont = "lexend"
	FontSerif    ThemeFont = "serif"
)

// ThemeMode is the mode picked by a profile.
type ThemeMode string

// Modes.
const (
	// ThemeAuto follows the device.
	ThemeAuto  ThemeMode = "auto"
	ThemeDark  ThemeMode = "dark"
	ThemeLight ThemeMode = "light"
)

// Valid reports a known mode.
func (m ThemeMode) Valid() bool { return m == ThemeAuto || m == ThemeDark || m == ThemeLight }

// MaxThemeRadius is the largest corner radius.
const MaxThemeRadius = 24

// Minimum contrast ratios (WCAG 2.2, level AA): normal text, UI components.
const (
	ContrastText = 4.5
	ContrastUI   = 3.0
)

// contrastRules lists role pairs (foreground, background) and the contrast they need.
var contrastRules = []struct {
	fg, bg string
	min    float64
}{
	{"text", "background", ContrastText},
	{"text", "surface", ContrastText},
	{"text", "surface_raised", ContrastText},
	{"text_muted", "background", ContrastText},
	{"text_muted", "surface", ContrastText},
	{"on_accent", "accent", ContrastText},
	{"on_error", "error", ContrastText},
	{"accent", "background", ContrastUI},
	{"error", "background", ContrastUI},
	{"success", "background", ContrastUI},
	{"warning", "background", ContrastUI},
}

func (p Palette) roles() map[string]string {
	return map[string]string{
		"background": p.Background, "surface": p.Surface, "surface_raised": p.SurfaceRaised,
		"text": p.Text, "text_muted": p.TextMuted, "accent": p.Accent, "on_accent": p.OnAccent,
		"outline": p.Outline, "error": p.Error, "on_error": p.OnError, "success": p.Success, "warning": p.Warning,
	}
}

// Normalize rewrites the colors as lower-case "#rrggbb" ("#ABC" becomes "#aabbcc").
func (p Palette) Normalize() Palette {
	n := func(c string) string {
		c = strings.ToLower(strings.TrimSpace(c))
		if len(c) == 4 && c[0] == '#' {
			c = "#" + strings.Repeat(c[1:2], 2) + strings.Repeat(c[2:3], 2) + strings.Repeat(c[3:4], 2)
		}
		return c
	}
	return Palette{
		Background: n(p.Background), Surface: n(p.Surface), SurfaceRaised: n(p.SurfaceRaised), Text: n(p.Text),
		TextMuted: n(p.TextMuted), Accent: n(p.Accent), OnAccent: n(p.OnAccent), Outline: n(p.Outline),
		Error: n(p.Error), OnError: n(p.OnError), Success: n(p.Success), Warning: n(p.Warning),
	}
}

// Problems lists what makes the tokens invalid or hard to read; empty if they are fine. Palettes
// must be normalized first.
func (t ThemeTokens) Problems() []Text {
	var out []Text
	for _, m := range []struct {
		name string
		p    Palette
	}{{"dark", t.Dark}, {"light", t.Light}} {
		roles := m.p.roles()
		bad := false
		for _, role := range roleOrder {
			if _, err := luminance(roles[role]); err != nil {
				out = append(out, T("theme.problem.invalid_color", "palette", m.name, "role", role, "color", roles[role]))
				bad = true
			}
		}
		if bad {
			continue
		}
		for _, r := range contrastRules {
			if c := Contrast(roles[r.fg], roles[r.bg]); c < r.min {
				out = append(out, T("theme.problem.low_contrast", "palette", m.name, "foreground", r.fg, "background", r.bg,
					"ratio", strconv.FormatFloat(c, 'f', 2, 64), "min", strconv.FormatFloat(r.min, 'f', 1, 64)))
			}
		}
	}
	if t.Radius < 0 || t.Radius > MaxThemeRadius {
		out = append(out, T("theme.problem.radius", "radius", t.Radius, "max", MaxThemeRadius))
	}
	switch t.Density {
	case DensityCompact, DensityComfortable, DensitySpacious:
	default:
		out = append(out, T("theme.problem.density", "density", t.Density))
	}
	switch t.Font {
	case FontSystem, FontInter, FontAtkinson, FontLexend, FontSerif:
	default:
		out = append(out, T("theme.problem.font", "font", t.Font))
	}
	return out
}

// roleOrder is the order in which problems are reported.
var roleOrder = []string{
	"background", "surface", "surface_raised", "text", "text_muted", "accent", "on_accent", "outline", "error", "on_error", "success", "warning",
}

// Contrast returns the contrast ratio of two "#rrggbb" colors (WCAG: 1 to 21), or 0 if one cannot
// be parsed.
func Contrast(a, b string) float64 {
	la, errA := luminance(a)
	lb, errB := luminance(b)
	if errA != nil || errB != nil {
		return 0
	}
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// luminance returns the relative luminance of a "#rrggbb" color (WCAG).
func luminance(c string) (float64, error) {
	if len(c) != 7 || c[0] != '#' {
		return 0, fmt.Errorf("invalid colour %q (\"#rrggbb\" expected)", c)
	}
	v, err := strconv.ParseUint(c[1:], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid colour %q (\"#rrggbb\" expected)", c)
	}
	channel := func(x uint64) float64 {
		s := float64(x) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(v>>16&0xff) + 0.7152*channel(v>>8&0xff) + 0.0722*channel(v&0xff), nil
}
