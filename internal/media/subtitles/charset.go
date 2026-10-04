package subtitles

import (
	"bytes"
	"unicode/utf16"
	"unicode/utf8"
)

// ToUTF8 turns a text subtitle file into UTF-8 without a BOM. It recognizes UTF-8 (with or without
// a BOM) and UTF-16 with a BOM. Anything else is read as Windows-1252, the encoding of old Western
// European .srt files (FFmpeg would read them as UTF-8 and lose the accents).
func ToUTF8(b []byte) []byte {
	switch {
	case bytes.HasPrefix(b, utf8BOM):
		return b[len(utf8BOM):]
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		return fromUTF16(b[2:], false)
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		return fromUTF16(b[2:], true)
	case utf8.Valid(b):
		return b
	}
	out := make([]byte, 0, len(b)+len(b)/8)
	for _, c := range b {
		r := rune(c)
		if c >= 0x80 && c < 0xA0 {
			r = cp1252[c-0x80]
		}
		out = utf8.AppendRune(out, r)
	}
	return out
}

func fromUTF16(b []byte, bigEndian bool) []byte {
	units := make([]uint16, len(b)/2)
	for i := range units {
		if bigEndian {
			units[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
		} else {
			units[i] = uint16(b[2*i+1])<<8 | uint16(b[2*i])
		}
	}
	var out []byte
	for _, r := range utf16.Decode(units) {
		out = utf8.AppendRune(out, r)
	}
	return out
}

// cp1252 holds the characters from 0x80 to 0x9F in Windows-1252. The rest is ISO 8859-1.
var cp1252 = [32]rune{
	'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0x8D, 'Ž', 0x8F,
	0x90, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0x9D, 'ž', 'Ÿ',
}
