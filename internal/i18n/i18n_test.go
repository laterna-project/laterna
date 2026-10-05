package i18n

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// Catalogs must say the same thing in every language: same keys, same params, no empty message.
func TestCatalogs(t *testing.T) {
	keyPattern := regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)*$`)
	en := catalogs[English]
	for _, lang := range Languages() {
		messages := catalogs[lang]
		for key, tmpl := range en {
			got, ok := messages[key]
			switch {
			case !ok:
				t.Errorf("%s: %s is missing", lang, key)
			case strings.TrimSpace(got) == "" && key != "list.separator":
				t.Errorf("%s: %s is empty", lang, key)
			case !slices.Equal(Placeholders(got), Placeholders(tmpl)):
				t.Errorf("%s: %s expects %v, English %v", lang, key, Placeholders(got), Placeholders(tmpl))
			}
			if strings.Count(got, "{") != len(placeholder.FindAllString(got, -1)) {
				t.Errorf("%s: %s has a malformed param: %q", lang, key, got)
			}
		}
		for key := range messages {
			if _, ok := en[key]; !ok {
				t.Errorf("%s: %s does not exist in English", lang, key)
			}
			if !keyPattern.MatchString(key) {
				t.Errorf("%s: malformed key %q", lang, key)
			}
		}
	}
}

func TestRender(t *testing.T) {
	for _, c := range []struct {
		lang Lang
		text domain.Text
		want string
	}{
		{English, domain.T("error.library.not_found"), "Library not found"},
		{French, domain.T("error.library.not_found"), "Bibliothèque introuvable"},
		{English, domain.T("error.auth.username_too_long", "max", 64), "The username is longer than 64 characters"},
		{French, domain.T("error.auth.username_too_long", "max", 64), "Le nom d'utilisateur dépasse 64 caractères"},
		// A language the server does not have falls back to English.
		{"de", domain.T("error.library.not_found"), "Library not found"},
		// Durations: largest unit that divides evenly, otherwise minutes rounded up.
		{French, domain.T("error.auth.too_many_attempts", "retry_after_seconds", 14*time.Minute+7*time.Second), "Trop de tentatives, réessayez dans 15 min"},
		{English, domain.T("error.auth.too_many_attempts", "retry_after_seconds", 45*time.Second), "Too many attempts, try again in 45 s"},
		{
			French, domain.T("error.settings.invalid_missing_grace", "min_seconds", time.Hour, "max_seconds", 90*24*time.Hour),
			"Le délai avant d'oublier un fichier disparu doit être compris entre 1 h et 90 j",
		},
		{
			English, domain.T("error.settings.invalid_scan_interval", "min_seconds", 15*time.Minute, "max_seconds", 7*24*time.Hour),
			"The scan interval must be zero (never) or between 15 min and 7 d",
		},
		{French, domain.T("error.theme.image_too_large", "max_bytes", 8<<20), "Image trop lourde (8 Mio au plus)"},
		{
			English, domain.T("activity.playback_stopped", "profile", "Léa", "title", "Suzume", "position_seconds", 83*time.Minute+45*time.Second),
			"Léa stopped \"Suzume\" at 1:23:45",
		},
		{French, domain.T("activity.setting.trickplay", "value", true), "vignettes de défilement : activé"},
		{English, domain.T("activity.setting.trickplay", "value", false), "preview thumbnails: off"},
		// A list: each text is rendered, then they are joined.
		{French, domain.T("error.playback.unplayable", []domain.Text{
			domain.T("reason.audio_unsupported", "codec", "eac3"), domain.T("reason.no_hls"),
		}), "Ce fichier ne peut pas être lu sur cet appareil : L'appareil ne lit pas ce son (eac3) ; L'appareil ne lit pas le HLS"},
		{English, domain.T("activity.account_changed", "actor", "admin", "username", "lea", []domain.Text{
			domain.T("activity.change.password"), domain.T("activity.change.disabled"),
		}), "admin updated the account lea: password; disabled"},
		// A sentence stored before texts were keyed stays as it was.
		{French, domain.Literal("Léa signed in on Living room"), "Léa signed in on Living room"},
		// An unknown key stays readable.
		{French, domain.T("no.such_key", "a", 1), "no.such_key (a=1)"},
		// A missing param is left empty, without braces.
		{English, domain.T("error.auth.username_too_long"), "The username is longer than  characters"},
	} {
		if got := Render(c.lang, c.text); got != c.want {
			t.Errorf("%s %v:\n  %q\n  want %q", c.lang, c.text, got, c.want)
		}
	}
	if got := Text(French, "name.season", "number", 3); got != "Saison 3" {
		t.Errorf("Text: %q", got)
	}
}

func TestFormats(t *testing.T) {
	for _, c := range []struct{ format, value, en, fr string }{
		{formatDuration, "59", "59 s", "59 s"},
		{formatDuration, "60", "1 min", "1 min"},
		{formatDuration, "61", "2 min", "2 min"},
		{formatDuration, "5400", "90 min", "90 min"},
		{formatDuration, "21600", "6 h", "6 h"},
		{formatDuration, "259200", "3 d", "3 j"},
		{formatBytes, "512", "512 B", "512 o"},
		{formatBytes, "1536", "2 KiB", "2 Kio"},
		{formatBytes, "8388608", "8 MiB", "8 Mio"},
		{formatBytes, "2147483648", "2 GiB", "2 Gio"},
		{formatClock, "65", "1:05", "1:05"},
		{formatClock, "5025", "1:23:45", "1:23:45"},
		{formatOnOff, "true", "on", "activé"},
		{formatOnOff, "false", "off", "désactivé"},
		// Anything that is not a number goes through untouched.
		{formatDuration, "soon", "soon", "soon"},
		{"", "42", "42", "42"},
	} {
		if en, fr := formatParam(English, c.format, c.value), formatParam(French, c.format, c.value); en != c.en || fr != c.fr {
			t.Errorf("%s(%s) = %q, %q; want %q, %q", c.format, c.value, en, fr, c.en, c.fr)
		}
	}
}

func TestAccept(t *testing.T) {
	for header, want := range map[string]Lang{
		"fr":                                  French,
		"fr-FR,fr;q=0.9,en-US;q=0.8,en;q=0.7": French,
		"en-GB,en;q=0.9,fr;q=0.8":             English,
		"de-DE,de;q=0.9,fr;q=0.5,en;q=0.4":    French,
		"de, en;q=0.2, fr;q=0.1":              English,
		"FR-ca":                               French,
		" en ; q=0.5 , fr ; q=0.9":            French,
		"fr;q=0,en":                           English,
		"de":                                  "",
		"*":                                   "",
		"":                                    "",
		"fr;q=abc":                            "",
	} {
		got, ok := Accept(header)
		if got != want || ok != (want != "") {
			t.Errorf("Accept(%q) = %q, %v; want %q", header, got, ok, want)
		}
	}
	for tag, want := range map[string]bool{
		"fr": true, "en-GB": true, "pt-BR": true, "zh-Hant-TW": true, "fil": true,
		"": false, "f": false, "français": false, "fr_FR": false, "fr-": false, strings.Repeat("ab-", 12) + "ab": false,
	} {
		if ValidTag(tag) != want {
			t.Errorf("ValidTag(%q) = %v", tag, !want)
		}
	}
}

// The language of a request is the one it asks for, then the profile's, then the server's.
func TestContext(t *testing.T) {
	if FromContext(context.Background()) != Default {
		t.Error("outside a request: want the fallback language")
	}
	asked := NewContext(context.Background(), French, true)
	Prefer(asked, "en")
	if FromContext(asked) != French {
		t.Error("a language asked by the request must not yield to the profile's")
	}
	server := NewContext(context.Background(), English, false)
	Prefer(server, "de") // a language the server does not have: the server's stays
	if FromContext(server) != English {
		t.Errorf("unknown language: %s", FromContext(server))
	}
	Prefer(server, "fr-CA")
	if FromContext(server) != French {
		t.Errorf("profile language: %s", FromContext(server))
	}
}
