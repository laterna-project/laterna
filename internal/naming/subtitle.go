package naming

import (
	"path"
	"strings"
	"unicode"
)

// SubtitleExtensions lists the external subtitle extensions we know (text: SubRip, ASS/SSA, WebVTT,
// MicroDVD; image: PGS, VobSub).
var SubtitleExtensions = map[string]bool{
	".srt": true, ".ass": true, ".ssa": true, ".vtt": true, ".sub": true, ".sup": true, ".idx": true,
}

// IsSubtitle reports a subtitle file by its extension.
func IsSubtitle(p string) bool { return SubtitleExtensions[strings.ToLower(path.Ext(p))] }

// Sidecar describes an external subtitle file of a video, from its name.
type Sidecar struct {
	// Language is an ISO 639-2/B code ("fre", "eng"), empty if the name does not say.
	Language string
	// Title is whatever else the name says ("Commentary", "Signs").
	Title                            string
	Forced, HearingImpaired, Default bool
}

// subtitleDirs are the names of subtitle subfolders.
var subtitleDirs = map[string]bool{"subs": true, "subtitles": true, "sub": true, "sous-titres": true}

// IsSubtitleDir reports a subtitle folder ("Subs", "Subtitles"...).
func IsSubtitleDir(name string) bool { return subtitleDirs[strings.ToLower(name)] }

// SidecarFor reports whether sub is an external subtitle of video (both relative to the same root,
// with forward slashes) and describes it. Three layouts are recognized:
//   - same folder, name starting with the video's name then a dot:
//     "Movie (2020).fr.forced.srt";
//   - a "Subs" (or "Subtitles") subfolder of a video that is alone in its folder (alone):
//     "Subs/2_English.srt";
//   - a "Subs/<video name>" subfolder (series): "Subs/Show.S01E01/3_French.srt".
func SidecarFor(video, sub string, alone bool) (Sidecar, bool) {
	vdir, vname := path.Split(video)
	stem := strings.TrimSuffix(vname, path.Ext(vname))
	sdir, sname := path.Split(sub)
	sstem := strings.TrimSuffix(sname, path.Ext(sname))
	if !IsSubtitle(sub) || stem == "" {
		return Sidecar{}, false
	}
	var tokens []string
	switch {
	case sdir == vdir:
		if len(sstem) < len(stem) || !strings.EqualFold(sstem[:len(stem)], stem) {
			return Sidecar{}, false
		}
		rest := sstem[len(stem):]
		if rest != "" && rest[0] != '.' {
			return Sidecar{}, false // "Episode 10.srt" does not belong to "Episode 1.mkv"
		}
		tokens = strings.Split(strings.TrimPrefix(rest, "."), ".")
	case strings.HasPrefix(sdir, vdir):
		inner := strings.Split(strings.TrimSuffix(sdir[len(vdir):], "/"), "/")
		switch {
		case len(inner) == 1 && subtitleDirs[strings.ToLower(inner[0])] && alone:
		case len(inner) == 2 && subtitleDirs[strings.ToLower(inner[0])] && strings.EqualFold(inner[1], stem):
		default:
			return Sidecar{}, false
		}
		tokens = strings.FieldsFunc(sstem, func(r rune) bool { return r == '.' || r == '_' || r == ' ' })
	default:
		return Sidecar{}, false
	}
	return describeSidecar(tokens), true
}

func describeSidecar(tokens []string) Sidecar {
	var s Sidecar
	var title []string
	for _, tok := range tokens {
		low := strings.ToLower(strings.TrimSpace(tok))
		switch {
		case low == "":
		case low == "forced" || low == "foreign":
			s.Forced = true
		case low == "sdh" || low == "cc" || low == "hi" || low == "hoh":
			s.HearingImpaired = true
		case low == "default":
			s.Default = true
		case strings.IndexFunc(low, func(r rune) bool { return !unicode.IsDigit(r) }) < 0:
			// ordering number ("2_English.srt")
		case s.Language == "" && language(low) != "":
			s.Language = language(low)
		default:
			title = append(title, strings.TrimSpace(tok))
		}
	}
	s.Title = strings.Join(title, " ")
	return s
}

// Language brings a language code (ISO 639-1 or 639-2, B or T, region included: "pt-BR", "fra")
// or a language name in English or French to its ISO 639-2/B code; "" if it does not know it.
func Language(tag string) string { return language(strings.ToLower(strings.TrimSpace(tag))) }

// LanguageTag gives the BCP 47 tag of a language ("fr" for "fre", "fra", "fr-CA" or "French"),
// for what names a language to a browser or a player (HLS renditions); "" if it does not know it.
func LanguageTag(tag string) string { return names[Language(tag)].tag }

// LanguageName gives the name of a language in that language ("Français" for "fre" or "fr"), as a
// player lists it when nothing else names a subtitle; "" if it does not know it.
func LanguageName(tag string) string { return names[Language(tag)].name }

// language recognizes a code (ISO 639-1 or 639-2, region included: "pt-BR") or a language name in
// English or French, and returns the ISO 639-2/B code.
func language(tok string) string {
	if code, ok := languages[tok]; ok {
		return code
	}
	if base, _, ok := strings.Cut(tok, "-"); ok {
		return languages[base]
	}
	return ""
}

// languages lists the most common subtitle languages. "hi" is left out: in a subtitle name it means
// "hearing impaired" far more often than Hindi.
var languages = func() map[string]string {
	m := map[string]string{}
	for code, names := range map[string][]string{
		"fre": {"fr", "fra", "french", "français", "francais", "vf", "vff", "vfq", "vostfr"},
		"eng": {"en", "english", "anglais"},
		"jpn": {"ja", "jp", "japanese", "japonais"},
		"spa": {"es", "spanish", "español", "espanol", "espagnol", "castellano", "latino"},
		"ger": {"de", "deu", "german", "deutsch", "allemand"},
		"ita": {"it", "italian", "italiano", "italien"},
		"por": {"pt", "portuguese", "português", "portugues", "portugais", "brazilian", "ptbr"},
		"rus": {"ru", "russian", "russe"},
		"chi": {"zh", "zho", "chinese", "chinois", "chs", "cht"},
		"kor": {"ko", "korean", "coréen", "coreen"},
		"ara": {"ar", "arabic", "arabe"},
		"dut": {"nl", "nld", "dutch", "nederlands", "néerlandais", "neerlandais"},
		"pol": {"pl", "polish", "polski", "polonais"},
		"swe": {"sv", "swedish", "svenska", "suédois", "suedois"},
		"nor": {"no", "nb", "nob", "norwegian", "norsk", "norvégien", "norvegien"},
		"dan": {"da", "danish", "dansk", "danois"},
		"fin": {"fi", "finnish", "suomi", "finnois"},
		"tur": {"tr", "turkish", "türkçe", "turkce", "turc"},
		"heb": {"he", "hebrew", "hébreu", "hebreu"},
		"hin": {"hindi"},
		"tha": {"th", "thai", "thaï"},
		"vie": {"vi", "vietnamese", "vietnamien"},
		"ind": {"indonesian", "indonésien", "indonesien"},
		"may": {"ms", "msa", "malay", "malais"},
		"cze": {"cs", "ces", "czech", "tchèque", "tcheque"},
		"hun": {"hu", "hungarian", "hongrois"},
		"rum": {"ro", "ron", "romanian", "roumain"},
		"gre": {"el", "ell", "greek", "grec"},
		"ukr": {"ukrainian", "ukrainien"},
		"bul": {"bg", "bulgarian", "bulgare"},
		"hrv": {"hr", "croatian", "croate"},
		"srp": {"sr", "serbian", "serbe"},
		"slv": {"sl", "slovenian", "slovène", "slovene"},
		"slo": {"sk", "slk", "slovak", "slovaque"},
		"cat": {"ca", "catalan"},
	} {
		m[code] = code
		for _, n := range names {
			m[n] = code
		}
	}
	// ISO 639-2/T codes: same language as the /B code.
	for t, b := range map[string]string{"fra": "fre", "deu": "ger", "zho": "chi", "nld": "dut", "ces": "cze", "ron": "rum", "ell": "gre", "msa": "may", "slk": "slo"} {
		m[t] = b
	}
	return m
}()

// names gives the ISO 639-1 code of each language of the table, and its name in that language.
var names = map[string]struct{ tag, name string }{
	"fre": {"fr", "Français"}, "eng": {"en", "English"}, "jpn": {"ja", "日本語"}, "spa": {"es", "Español"},
	"ger": {"de", "Deutsch"}, "ita": {"it", "Italiano"}, "por": {"pt", "Português"}, "rus": {"ru", "Русский"},
	"chi": {"zh", "中文"}, "kor": {"ko", "한국어"}, "ara": {"ar", "العربية"}, "dut": {"nl", "Nederlands"},
	"pol": {"pl", "Polski"}, "swe": {"sv", "Svenska"}, "nor": {"no", "Norsk"}, "dan": {"da", "Dansk"},
	"fin": {"fi", "Suomi"}, "tur": {"tr", "Türkçe"}, "heb": {"he", "עברית"}, "hin": {"hi", "हिन्दी"},
	"tha": {"th", "ไทย"}, "vie": {"vi", "Tiếng Việt"}, "ind": {"id", "Bahasa Indonesia"},
	"may": {"ms", "Bahasa Melayu"}, "cze": {"cs", "Čeština"}, "hun": {"hu", "Magyar"}, "rum": {"ro", "Română"},
	"gre": {"el", "Ελληνικά"}, "ukr": {"uk", "Українська"}, "bul": {"bg", "Български"}, "hrv": {"hr", "Hrvatski"},
	"srp": {"sr", "Српски"}, "slv": {"sl", "Slovenščina"}, "slo": {"sk", "Slovenčina"}, "cat": {"ca", "Català"},
}
