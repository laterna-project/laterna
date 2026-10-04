package subtitles

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ASSToVTT converts an ASS (or SSA) subtitle to WebVTT, for clients without an ASS renderer. A
// fansub mixes dialogue, translated signs and karaoke effects (hundreds of lines per song):
// converted as is, it would cover the screen with text. So only dialogue is kept. Commented lines,
// drawings (\p), text placed or animated at a precise spot (\pos, \move, \clip, \org: signs,
// generated effects) and empty lines are dropped. Italics, bold, underline and line breaks are
// kept, and text at the top of the screen (\an8, a top-aligned style) stays there.
func ASSToVTT(ass []byte) []byte {
	var (
		section     string
		styleCols   []string
		eventCols   []string
		topStyles   = map[string]bool{}
		cues        []cue
		legacyAlign bool
	)
	for raw := range strings.Lines(string(bytes.TrimPrefix(ass, utf8BOM))) {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(line)
			legacyAlign = section == "[v4 styles]"
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch {
		case strings.HasSuffix(section, "styles]") && key == "Format":
			styleCols = columns(value)
		case strings.HasSuffix(section, "styles]") && key == "Style":
			f := fields(value, len(styleCols))
			name := field(f, styleCols, "name")
			if a, err := strconv.Atoi(field(f, styleCols, "alignment")); err == nil && isTop(a, legacyAlign) {
				topStyles[strings.ToLower(name)] = true
			}
		case section == "[events]" && key == "Format":
			eventCols = columns(value)
		case section == "[events]" && key == "Dialogue":
			f := fields(value, len(eventCols))
			start, err1 := assTime(field(f, eventCols, "start"))
			end, err2 := assTime(field(f, eventCols, "end"))
			if err1 != nil || err2 != nil || end <= start {
				continue
			}
			text, top, keep := assText(field(f, eventCols, "text"))
			if !keep || strings.TrimSpace(stripTags(text)) == "" {
				continue
			}
			top = top || topStyles[strings.ToLower(field(f, eventCols, "style"))]
			cues = append(cues, cue{start: start, end: end, text: text, top: top})
		}
	}
	slices.SortStableFunc(cues, func(a, b cue) int { return int(a.start - b.start) })
	cues = merge(cues)

	var out bytes.Buffer
	out.WriteString("WEBVTT\n")
	for _, c := range cues {
		fmt.Fprintf(&out, "\n%s --> %s", vttTime(c.start), vttTime(c.end))
		if c.top {
			out.WriteString(" line:0")
		}
		out.WriteString("\n" + c.text + "\n")
	}
	return out.Bytes()
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

type cue struct {
	start, end time.Duration
	text       string
	top        bool
}

// merge joins one text that was split into consecutive events (common in ASS files), even when
// other lines come in between: it looks among the last eight.
func merge(cues []cue) []cue {
	out := make([]cue, 0, len(cues))
next:
	for _, c := range cues {
		for j := len(out) - 1; j >= max(0, len(out)-8); j-- {
			if p := &out[j]; p.text == c.text && p.top == c.top && c.start <= p.end+10*time.Millisecond {
				p.end = max(p.end, c.end)
				continue next
			}
		}
		out = append(out, c)
	}
	return out
}

// isTop reports an alignment at the top of the screen (7 to 9; 5 to 7 in SSA).
func isTop(a int, legacy bool) bool {
	if legacy {
		return a >= 5 && a <= 7
	}
	return a >= 7 && a <= 9
}

func columns(format string) []string {
	var cols []string
	for c := range strings.SplitSeq(format, ",") {
		cols = append(cols, strings.ToLower(strings.TrimSpace(c)))
	}
	return cols
}

// fields splits a line into n fields. The last one (the text) keeps its commas.
func fields(value string, n int) []string {
	if n <= 0 {
		return nil
	}
	return strings.SplitN(value, ",", n)
}

func field(f, cols []string, name string) string {
	i := slices.Index(cols, name)
	if i < 0 || i >= len(f) {
		return ""
	}
	return strings.TrimSpace(f[i])
}

// assTime reads a time written as "H:MM:SS.cc".
func assTime(s string) (time.Duration, error) {
	h, rest, ok1 := strings.Cut(s, ":")
	m, sec, ok2 := strings.Cut(rest, ":")
	if !ok1 || !ok2 {
		return 0, fmt.Errorf("invalid ASS time %q", s)
	}
	hours, err1 := strconv.Atoi(h)
	minutes, err2 := strconv.Atoi(m)
	seconds, err3 := strconv.ParseFloat(sec, 64)
	if err1 != nil || err2 != nil || err3 != nil || hours < 0 || minutes < 0 || seconds < 0 {
		return 0, fmt.Errorf("invalid ASS time %q", s)
	}
	return time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute +
		(time.Duration(seconds*1000+0.5) * time.Millisecond), nil
}

func vttTime(d time.Duration) string {
	ms := d.Milliseconds()
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3_600_000, ms/60_000%60, ms/1000%60, ms%1000)
}

// assText converts the text of an event: WebVTT tags for italics, bold and underline, line breaks,
// non-breaking spaces. keep is false for a drawing or positioned text (sign, effect); top is true
// for top-aligned text.
func assText(s string) (text string, top, keep bool) {
	var b strings.Builder
	var italic, bold, underline bool
	open := func() {
		if italic {
			b.WriteString("<i>")
		}
		if bold {
			b.WriteString("<b>")
		}
		if underline {
			b.WriteString("<u>")
		}
	}
	closeAll := func() {
		if underline {
			b.WriteString("</u>")
		}
		if bold {
			b.WriteString("</b>")
		}
		if italic {
			b.WriteString("</i>")
		}
	}
	for s != "" {
		if s[0] == '{' {
			end := strings.IndexByte(s, '}')
			if end < 0 {
				end = len(s) - 1
			}
			block := s[1:end]
			s = s[end+1:]
			if !strings.Contains(block, `\`) {
				continue // comment
			}
			ni, nb, nu := italic, bold, underline
			for tag := range strings.SplitSeq(block, `\`) {
				switch {
				case tag == "":
				case hasAny(tag, "pos(", "move(", "clip(", "iclip(", "org("):
					return "", false, false
				case len(tag) >= 2 && tag[0] == 'p' && tag[1] >= '1' && tag[1] <= '9':
					return "", false, false // drawing
				case tag == "an7" || tag == "an8" || tag == "an9" || tag == "a5" || tag == "a6" || tag == "a7":
					top = true
				case tag == "i1":
					ni = true
				case tag == "i0" || tag == "i":
					ni = false
				case strings.HasPrefix(tag, "b") && len(tag) > 1 && isDigits(tag[1:]):
					w, _ := strconv.Atoi(tag[1:])
					nb = w == 1 || w >= 600
				case tag == "u1":
					nu = true
				case tag == "u0" || tag == "u":
					nu = false
				case tag == "r":
					ni, nb, nu = false, false, false
				}
			}
			if ni != italic || nb != bold || nu != underline {
				closeAll()
				italic, bold, underline = ni, nb, nu
				open()
			}
			continue
		}
		next := strings.IndexByte(s, '{')
		if next < 0 {
			next = len(s)
		}
		b.WriteString(plain(s[:next]))
		s = s[next:]
	}
	closeAll()
	lines := strings.Split(b.String(), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n"), top, true
}

// plain converts text outside tags: \N and \n become line breaks, \h a non-breaking space, and
// WebVTT special characters are escaped.
func plain(s string) string {
	s = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
	return strings.NewReplacer(`\N`, "\n", `\n`, "\n", `\h`, " ").Replace(s)
}

// stripTags removes WebVTT tags, to see whether any text is left.
func stripTags(s string) string {
	return strings.NewReplacer("<i>", "", "</i>", "", "<b>", "", "</b>", "", "<u>", "", "</u>", "", " ", " ").Replace(s)
}

func hasAny(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func isDigits(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) < 0
}
