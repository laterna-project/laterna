package subtitles

import (
	"encoding/binary"
	"path"
	"slices"
	"strings"
	"unicode/utf16"
)

// fontMimeTypes are the types of fonts attached to Matroska files (old and new names).
var fontMimeTypes = []string{
	"font/ttf", "font/otf", "font/sfnt", "font/collection", "application/x-truetype-font",
	"application/x-font-ttf", "application/x-font-otf", "application/x-font-opentype",
	"application/vnd.ms-opentype", "application/font-sfnt", "application/x-font",
}

// fontExtensions are font extensions, for an attachment whose type is wrong (ffprobe then says
// "unknown").
var fontExtensions = []string{".ttf", ".otf", ".ttc", ".otc"}

// IsFont reports whether an attachment is a font, from its type or its name.
func IsFont(name, mimeType string) bool {
	return slices.Contains(fontMimeTypes, strings.ToLower(mimeType)) ||
		slices.Contains(fontExtensions, strings.ToLower(path.Ext(name)))
}

// FontExt returns the extension to store a font under: the one in its name if it is known, ".ttf"
// otherwise.
func FontExt(name string) string {
	if ext := strings.ToLower(path.Ext(name)); slices.Contains(fontExtensions, ext) {
		return ext
	}
	return ".ttf"
}

// Names read from the "name" table of a font.
const (
	nameFamily            = 1
	nameFull              = 4
	namePostScript        = 6
	nameTypographicFamily = 16
)

// FontNames returns the names an ASS subtitle may use for a font: family, typographic family, full
// name and PostScript name, for each font of a collection. They are lower-cased (libass compares
// them ignoring case) and deduplicated. It returns false if the data is not a readable TrueType or
// OpenType font.
func FontNames(data []byte) ([]string, bool) {
	var fonts []uint32 // start of each font
	switch {
	case len(data) < 12:
		return nil, false
	case string(data[:4]) == "ttcf":
		n := binary.BigEndian.Uint32(data[8:12])
		for i := range min(n, 256) {
			at := 12 + 4*int(i)
			if at+4 > len(data) {
				return nil, false
			}
			fonts = append(fonts, binary.BigEndian.Uint32(data[at:]))
		}
	default:
		fonts = []uint32{0}
	}
	var names []string
	for _, start := range fonts {
		names = append(names, sfntNames(data, int(start))...)
	}
	slices.Sort(names)
	names = slices.Compact(names)
	return names, len(names) > 0
}

// sfntNames reads the useful names of the font that starts at start.
func sfntNames(data []byte, start int) []string {
	if start < 0 || start+12 > len(data) {
		return nil
	}
	switch binary.BigEndian.Uint32(data[start:]) {
	case 0x00010000, 0x4F54544F, 0x74727565, 0x74797031: // TrueType, "OTTO", "true", "typ1"
	default:
		return nil
	}
	numTables := int(binary.BigEndian.Uint16(data[start+4:]))
	var table []byte
	for i := range numTables {
		rec := start + 12 + 16*i
		if rec+16 > len(data) {
			return nil
		}
		if string(data[rec:rec+4]) == "name" {
			off, length := int(binary.BigEndian.Uint32(data[rec+8:])), int(binary.BigEndian.Uint32(data[rec+12:]))
			if off < 0 || length < 0 || off+length > len(data) || off+length < off {
				return nil
			}
			table = data[off : off+length]
			break
		}
	}
	if len(table) < 6 {
		return nil
	}
	count, strings0 := int(binary.BigEndian.Uint16(table[2:])), int(binary.BigEndian.Uint16(table[4:]))
	var out []string
	for i := range count {
		rec := 6 + 12*i
		if rec+12 > len(table) {
			break
		}
		platform, encoding := binary.BigEndian.Uint16(table[rec:]), binary.BigEndian.Uint16(table[rec+2:])
		id := binary.BigEndian.Uint16(table[rec+6:])
		length, off := int(binary.BigEndian.Uint16(table[rec+8:])), int(binary.BigEndian.Uint16(table[rec+10:]))
		if id != nameFamily && id != nameFull && id != namePostScript && id != nameTypographicFamily {
			continue
		}
		at := strings0 + off
		if at+length > len(table) {
			continue
		}
		raw := table[at : at+length]
		var s string
		switch {
		case platform == 0 || (platform == 3 && (encoding == 1 || encoding == 10)):
			s = utf16BE(raw)
		case platform == 1 && encoding == 0 && isASCII(raw): // Macintosh Roman: ASCII is enough
			s = string(raw)
		default:
			continue
		}
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func utf16BE(b []byte) string {
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = binary.BigEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(units))
}

func isASCII(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return false
		}
	}
	return true
}
