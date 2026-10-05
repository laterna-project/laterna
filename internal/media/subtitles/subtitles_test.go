package subtitles

import (
	"slices"
	"strings"
	"testing"

	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
)

// A fansub ASS: dialogue, a positioned sign, a generated karaoke effect, a drawing, a comment.
const fansub = "\ufeff[Script Info]\r\nScriptType: v4.00+\r\n\r\n" +
	"[V4+ Styles]\r\n" +
	"Format: Name, Fontname, Fontsize, PrimaryColour, Bold, Italic, Alignment, MarginV\r\n" +
	"Style: Default,Go,48,&H00FFFFFF,0,0,2,20\r\n" +
	"Style: Top,Go,48,&H00FFFFFF,0,0,8,20\r\n" +
	"Style: Sign,Go,30,&H00FFFFFF,0,0,5,20\r\n\r\n" +
	"[Events]\r\n" +
	"Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\r\n" +
	"Comment: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,commented text\r\n" +
	"Dialogue: 0,0:00:01.00,0:00:03.50,Default,,0,0,0,,Hello, {\\i1}you{\\i0}!\\NAll good & well?\r\n" +
	"Dialogue: 0,0:00:03.50,0:00:05.00,Default,,0,0,0,,Hello, {\\i1}you{\\i0}!\\NAll good & well?\r\n" +
	"Dialogue: 0,0:00:02.00,0:00:04.00,Top,,0,0,0,,On top<3\r\n" +
	"Dialogue: 0,0:00:04.00,0:00:06.00,Default,,0,0,0,,{\\an8\\b1}Also on top\r\n" +
	"Dialogue: 0,0:00:01.00,0:00:09.00,Sign,,0,0,0,,{\\pos(320,50)\\fnArial}RAMEN ICHIRAKU\r\n" +
	"Dialogue: 1,0:00:07.00,0:00:07.20,Default,,0,0,0,fx,{\\move(10,10,20,20)\\k20}ka\r\n" +
	"Dialogue: 0,0:00:07.00,0:00:08.00,Default,,0,0,0,,{\\p1}m 0 0 l 10 0 10 10\r\n" +
	"Dialogue: 0,0:00:08.00,0:00:10.00,Default,,0,0,0,,{\\k50}Ka{\\k50}ra{\\k50}o{\\k50}ke\r\n" +
	"Dialogue: 0,0:00:10.00,0:00:10.00,Default,,0,0,0,,zero duration\r\n" +
	"Dialogue: 0,1:02:03.45,1:02:04.00,Default,,0,0,0,,{ comment }End\\hof film\r\n"

func TestASSToVTT(t *testing.T) {
	got := string(ASSToVTT([]byte(fansub)))
	want := "WEBVTT\n" +
		"\n00:00:01.000 --> 00:00:05.000\nHello, <i>you</i>!\nAll good &amp; well?\n" +
		"\n00:00:02.000 --> 00:00:04.000 line:0\nOn top&lt;3\n" +
		"\n00:00:04.000 --> 00:00:06.000 line:0\n<b>Also on top</b>\n" +
		"\n00:00:08.000 --> 00:00:10.000\nKaraoke\n" +
		"\n01:02:03.450 --> 01:02:04.000\nEnd\u00a0of film\n"
	if got != want {
		t.Errorf("WebVTT:\n%s\nwant:\n%s", got, want)
	}
}

// SSA (V4) aligns to the top with 5 to 7.
func TestASSToVTTLegacyAlignment(t *testing.T) {
	ssa := "[V4 Styles]\nFormat: Name, Alignment\nStyle: Top,6\n\n[Events]\nFormat: Marked, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		"Dialogue: Marked=0,0:00:01.00,0:00:02.00,Top,,0,0,0,,Text\n"
	if got := string(ASSToVTT([]byte(ssa))); !strings.Contains(got, "00:00:01.000 --> 00:00:02.000 line:0\nText") {
		t.Errorf("SSA: %q", got)
	}
}

func TestToUTF8(t *testing.T) {
	for name, c := range map[string]struct{ in, want string }{
		"UTF-8":        {"Café", "Café"},
		"UTF-8 BOM":    {"\ufeffCafé", "Café"},
		"Windows-1252": {"Caf\xe9 \x80 \x9cuvre", "Café € œuvre"},
		"UTF-16 LE":    {"\xff\xfeC\x00a\x00f\x00\xe9\x00", "Café"},
		"UTF-16 BE":    {"\xfe\xff\x00C\x00a\x00f\x00\xe9", "Café"},
	} {
		if got := string(ToUTF8([]byte(c.in))); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

func TestFontNames(t *testing.T) {
	names, ok := FontNames(goregular.TTF)
	if !ok || !slices.Contains(names, "go") || !slices.Contains(names, "goregular") {
		t.Errorf("Go Regular: %v %v", names, ok)
	}
	if names, _ := FontNames(gobold.TTF); !slices.Contains(names, "go bold") {
		t.Errorf("Go Bold: %v", names)
	}
	for _, bad := range [][]byte{nil, []byte("not a font"), goregular.TTF[:40]} {
		if names, ok := FontNames(bad); ok {
			t.Errorf("false: %v", names)
		}
	}
	if !IsFont("x.bin", "application/x-truetype-font") || !IsFont("Font.TTF", "unknown") || IsFont("Cover", "image/png") {
		t.Error("IsFont")
	}
	if FontExt("a.OTF") != ".otf" || FontExt("no-extension") != ".ttf" {
		t.Error("FontExt")
	}
}

func TestFormatsAndArgs(t *testing.T) {
	for codec, want := range map[string][]string{
		"ass": {ASS, VTT}, "ssa": {ASS, VTT}, "subrip": {VTT}, "mov_text": {VTT},
		"hdmv_pgs_subtitle": {SUP}, "dvd_subtitle": {MKS}, "eia_608": nil,
	} {
		if got := Formats(codec); !slices.Equal(got, want) {
			t.Errorf("Formats(%s) = %v", codec, got)
		}
	}
	args := strings.Join(ExtractArgs("a.mkv", []Track{{Index: 2, Codec: "ass", Name: "0"}, {Index: 3, Codec: "subrip", Name: "1"}, {Index: 4, Codec: "hdmv_pgs_subtitle", Name: "2"}}, "out"), " ")
	for _, want := range []string{
		"-copyts -i file:a.mkv", "-map 0:2 -c:s copy -f ass file:out/0.ass", "-map 0:3 -c:s webvtt -f webvtt file:out/1.vtt",
		"-map 0:4 -c:s copy -f sup file:out/2.sup",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("extraction without %q: %s", want, args)
		}
	}
	if a := strings.Join(ConvertArgs("x.idx", "out/3.mks"), " "); !strings.Contains(a, "-c:s copy -f matroska file:out/3.mks") {
		t.Errorf("VobSub: %s", a)
	}
}
