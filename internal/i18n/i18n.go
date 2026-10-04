// Package i18n renders the texts the server has to write itself: the fallback message of errors,
// home row titles, the activity log, the OpenID Connect confirmation page. Clients translate with
// their own catalogs from the key and params of each text (domain.Text). This package is for those
// that do not, in the languages the server knows.
//
// It is pure logic: the catalogs (locales/*.json) are embedded.
package i18n

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/laterna-project/laterna/internal/domain"
)

// Lang is a language the server has a catalog for.
type Lang string

// Server languages. English is the fallback: its catalog is complete by definition and the others
// are checked against it.
const (
	English Lang = "en"
	French  Lang = "fr"

	Default = English
)

//go:embed locales/*.json
var files embed.FS

var catalogs = load()

func load() map[Lang]map[string]string {
	out := map[Lang]map[string]string{}
	for _, lang := range []Lang{English, French} {
		data, err := files.ReadFile("locales/" + string(lang) + ".json")
		if err != nil {
			panic("i18n: " + err.Error())
		}
		var messages map[string]string
		if err := json.Unmarshal(data, &messages); err != nil {
			panic(fmt.Sprintf("i18n: locales/%s.json: %v", lang, err))
		}
		out[lang] = messages
	}
	return out
}

// Languages returns the server languages, English first.
func Languages() []Lang { return []Lang{English, French} }

// Keys returns the catalog keys, sorted.
func Keys() []string { return slices.Sorted(maps.Keys(catalogs[English])) }

// Has reports whether the catalog has the key.
func Has(key string) bool {
	_, ok := catalogs[English][key]
	return ok
}

// Template returns the template of a message in a language, or the English one if it is missing.
func Template(lang Lang, key string) (string, bool) {
	if s, ok := catalogs[lang][key]; ok {
		return s, true
	}
	s, ok := catalogs[English][key]
	return s, ok
}

var tagPattern = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8})*$`)

// ValidTag reports whether a language tag is well formed ("fr", "pt-BR", "zh-Hant-TW"). A profile
// may pick a language the server does not have.
func ValidTag(tag string) bool { return len(tag) <= 35 && tagPattern.MatchString(tag) }

// Parse returns the server language that serves a tag ("fr-CA" gives French).
func Parse(tag string) (Lang, bool) {
	primary, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
	for _, lang := range Languages() {
		if string(lang) == primary {
			return lang, true
		}
	}
	return "", false
}

// Accept picks the server language an Accept-Language header prefers ("fr-FR,fr;q=0.9,en;q=0.8");
// false if none fits.
func Accept(header string) (Lang, bool) {
	var best Lang
	bestQ := 0.0
	for part := range strings.SplitSeq(header, ",") {
		tag, params, _ := strings.Cut(part, ";")
		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			parsed, err := strconv.ParseFloat(v, 64)
			if err != nil {
				continue
			}
			q = parsed
		}
		// On a tie the first one listed wins.
		if lang, ok := Parse(tag); ok && q > bestQ {
			best, bestQ = lang, q
		}
	}
	return best, bestQ > 0
}

// Render writes a text in a language. A missing param is left empty. A key the catalog does not
// know is written as is with its params, so it stays readable.
func Render(lang Lang, t domain.Text) string {
	tmpl, ok := Template(lang, t.Key)
	if !ok {
		return t.String()
	}
	if !strings.Contains(tmpl, "{") {
		return tmpl
	}
	var b strings.Builder
	for {
		open := strings.IndexByte(tmpl, '{')
		if open < 0 {
			break
		}
		end := strings.IndexByte(tmpl[open:], '}')
		if end < 0 {
			break
		}
		b.WriteString(tmpl[:open])
		name, format, _ := strings.Cut(tmpl[open+1:open+end], ":")
		if name == listParam {
			b.WriteString(renderList(lang, t.List))
		} else {
			b.WriteString(formatParam(lang, format, t.Params[name]))
		}
		tmpl = tmpl[open+end+1:]
	}
	b.WriteString(tmpl)
	return b.String()
}

// Text is shorthand for Render(lang, domain.T(key, kv...)).
func Text(lang Lang, key string, kv ...any) string { return Render(lang, domain.T(key, kv...)) }

// listParam is the reserved placeholder for the list of a text.
const listParam = "list"

func renderList(lang Lang, list []domain.Text) string {
	parts := make([]string, len(list))
	for i, sub := range list {
		parts[i] = Render(lang, sub)
	}
	sep, _ := Template(lang, "list.separator")
	return strings.Join(parts, sep)
}

// Param formats in a template: {retry_after_seconds:duration} ("15 min"), {max_bytes:bytes} ("8
// MiB"), {position_seconds:clock} ("1:23:45"), {value:onoff} ("on").
const (
	formatDuration = "duration"
	formatBytes    = "bytes"
	formatClock    = "clock"
	formatOnOff    = "onoff"
)

func formatParam(lang Lang, format, value string) string {
	if format == formatOnOff {
		word := "word.off"
		if value == "true" {
			word = "word.on"
		}
		s, _ := Template(lang, word)
		return s
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || format == "" {
		return value
	}
	unit := func(name string) string {
		u, _ := Template(lang, "unit."+name)
		return strconv.FormatInt(n, 10) + " " + u
	}
	switch format {
	case formatDuration:
		// Largest unit that divides evenly, otherwise minutes rounded up.
		switch {
		case n >= 86400 && n%86400 == 0:
			n /= 86400
			return unit("day")
		case n >= 3600 && n%3600 == 0:
			n /= 3600
			return unit("hour")
		case n >= 60:
			n = (n + 59) / 60
			return unit("minute")
		}
		return unit("second")
	case formatBytes:
		switch {
		case n >= 1<<30 && n%(1<<30) == 0:
			n >>= 30
			return unit("gib")
		case n >= 1<<20:
			n = (n + 1<<19) >> 20
			return unit("mib")
		case n >= 1<<10:
			n = (n + 1<<9) >> 10
			return unit("kib")
		}
		return unit("byte")
	case formatClock:
		if n >= 3600 {
			return fmt.Sprintf("%d:%02d:%02d", n/3600, n/60%60, n%60)
		}
		return fmt.Sprintf("%d:%02d", n/60, n%60)
	}
	return value
}

// Placeholders returns the param names a template expects, sorted and deduplicated, {list}
// included.
func Placeholders(tmpl string) []string {
	var names []string
	for _, m := range placeholder.FindAllStringSubmatch(tmpl, -1) {
		if !slices.Contains(names, m[1]) {
			names = append(names, m[1])
		}
	}
	slices.Sort(names)
	return names
}

var placeholder = regexp.MustCompile(`\{([a-z0-9_]+)(?::[a-z]+)?\}`)

type langKey struct{}

// request holds the language of a request. It starts as the server's and is replaced by the
// caller's once we know it (authenticated profile), unless the request asked for one itself.
type request struct {
	lang     Lang
	explicit bool
}

// NewContext attaches the language of a request to the context. explicit means the request asked
// for it (Accept-Language); otherwise Prefer may still replace it.
func NewContext(ctx context.Context, lang Lang, explicit bool) context.Context {
	return context.WithValue(ctx, langKey{}, &request{lang: lang, explicit: explicit})
}

// Prefer switches to the language of a tag (the caller's profile language) if the request did not
// ask for one and the server has it.
func Prefer(ctx context.Context, tag string) {
	r, ok := ctx.Value(langKey{}).(*request)
	if !ok || r.explicit {
		return
	}
	if lang, ok := Parse(tag); ok {
		r.lang = lang
	}
}

// FromContext returns the language of the request, or the fallback language outside of one.
func FromContext(ctx context.Context) Lang {
	if r, ok := ctx.Value(langKey{}).(*request); ok {
		return r.lang
	}
	return Default
}
