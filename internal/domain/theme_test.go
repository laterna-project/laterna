package domain

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestContrast(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want float64
	}{
		{"#000000", "#ffffff", 21},
		{"#ffffff", "#000000", 21},
		{"#777777", "#777777", 1},
		{"#767676", "#ffffff", 4.54}, // lightest gray that is readable on white
		{"#zzzzzz", "#ffffff", 0},
		{"#fff", "#000000", 0},
	} {
		if got := Contrast(c.a, c.b); math.Abs(got-c.want) > 0.01 {
			t.Errorf("%s / %s: %.3f, want %.2f", c.a, c.b, got, c.want)
		}
	}
}

func TestThemeProblems(t *testing.T) {
	good := Palette{
		Background: "#0f1115", Surface: "#181b21", SurfaceRaised: "#232730", Text: "#eef0f3", TextMuted: "#a3a9b4",
		Accent: "#e8b04a", OnAccent: "#1c1400", Outline: "#2f343d", Error: "#ff6b6b", OnError: "#1f0505",
		Success: "#4cc38a", Warning: "#f2c94c",
	}
	tokens := ThemeTokens{Dark: good, Light: good, Radius: 12, Density: DensityComfortable, Font: FontInter}
	if p := tokens.Problems(); len(p) != 0 {
		t.Fatalf("valid theme rejected: %v", p)
	}
	short := good
	short.Text = "#EEF"
	if n := short.Normalize(); n.Text != "#eeeeff" || n.Background != "#0f1115" {
		t.Errorf("normalize: %+v", n)
	}
	for name, c := range map[string]struct {
		change func(*ThemeTokens)
		want   string
	}{
		"gray text on gray": {func(t *ThemeTokens) { t.Dark.Text = "#3a3f48" }, "theme.problem.low_contrast (background=background, foreground=text, min=4.5, palette=dark"},
		"accent invisible":  {func(t *ThemeTokens) { t.Light.Accent = "#181b21" }, "theme.problem.low_contrast (background=background, foreground=accent, min=3.0, palette=light"},
		"unreadable color":  {func(t *ThemeTokens) { t.Dark.Outline = "blue-ish" }, "theme.problem.invalid_color (color=blue-ish, palette=dark, role=outline)"},
		"radius":            {func(t *ThemeTokens) { t.Radius = 40 }, "theme.problem.radius (max=24, radius=40)"},
		"density":           {func(t *ThemeTokens) { t.Density = "" }, "theme.problem.density (density=)"},
		"font":              {func(t *ThemeTokens) { t.Font = "comic" }, "theme.problem.font (font=comic)"},
	} {
		tt := tokens
		c.change(&tt)
		p := tt.Problems()
		if len(p) == 0 || !strings.Contains(fmt.Sprint(p), c.want) {
			t.Errorf("%s: %v", name, p)
		}
	}
}
