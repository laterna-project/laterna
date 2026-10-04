// Package subtitles prepares the subtitles of a file so that they show up instantly: all streams
// are extracted in a single read of the file (FFmpeg), converted to WebVTT for clients without an
// ASS renderer, external files are normalized to UTF-8, and the names of attached fonts are read.
// Nothing is done at playback time: everything is ready as soon as the file is imported.
package subtitles

import (
	"slices"
	"strconv"
)

// Formats served to clients.
const (
	// ASS is the original, with its styles and fonts (rendered by libass or JASSUB).
	ASS = "ass"
	// VTT is WebVTT, which plays everywhere. For ASS sources it holds dialogue only (see ASSToVTT).
	VTT = "vtt"
	// SUP is PGS (Blu-ray), an image subtitle.
	SUP = "sup"
	// MKS holds other image subtitles (VobSub, DVB) in a Matroska file. They are used for burn-in.
	MKS = "mks"
)

// textCodecs are the text subtitle codecs FFmpeg can convert to WebVTT.
var textCodecs = []string{"ass", "ssa", "subrip", "srt", "webvtt", "mov_text", "text", "microdvd", "subviewer", "subviewer1", "sami", "realtext", "jacosub", "mpl2", "pjs", "vplayer", "stl"}

// imageCodecs are the image subtitle codecs.
var imageCodecs = []string{"hdmv_pgs_subtitle", "dvd_subtitle", "dvb_subtitle", "xsub"}

// IsText reports a text subtitle, rendered by the client.
func IsText(codec string) bool { return slices.Contains(textCodecs, codec) }

// IsImage reports an image (bitmap) subtitle: rendered by a client that can show PGS, burned into
// the picture otherwise.
func IsImage(codec string) bool { return slices.Contains(imageCodecs, codec) }

// Formats returns the formats produced for a codec: the original ASS and its WebVTT version, WebVTT
// for other text, PGS as is, other image formats in Matroska. None for an unknown codec.
func Formats(codec string) []string {
	switch {
	case codec == "ass" || codec == "ssa":
		return []string{ASS, VTT}
	case IsText(codec):
		return []string{VTT}
	case codec == "hdmv_pgs_subtitle":
		return []string{SUP}
	case IsImage(codec):
		return []string{MKS}
	}
	return nil
}

// Track is a subtitle stream of a file to extract.
type Track struct {
	// Index is the stream number in the file.
	Index int
	Codec string
	// Name of the files produced, without extension ("3" gives "3.ass" and "3.vtt").
	Name string
}

// ExtractArgs builds the FFmpeg command that extracts all streams into dir in a single read of the
// file. Times stay those of the source (-copyts), as in HLS playback, so a subtitle shows at the
// same instant whatever the playback method. ASS is copied as is (the WebVTT is derived from it
// afterwards, see ASSToVTT) and other text formats are converted to WebVTT by FFmpeg.
func ExtractArgs(path string, tracks []Track, dir string) []string {
	args := []string{"-nostdin", "-hide_banner", "-v", "error", "-copyts", "-i", "file:" + path}
	for _, t := range tracks {
		m := []string{"-map", "0:" + strconv.Itoa(t.Index)}
		out := "file:" + dir + "/" + t.Name
		switch {
		case t.Codec == "ass":
			args = append(append(args, m...), "-c:s", "copy", "-f", "ass", out+".ass")
		case t.Codec == "ssa":
			args = append(append(args, m...), "-c:s", "ass", "-f", "ass", out+".ass")
		case IsText(t.Codec):
			args = append(append(args, m...), "-c:s", "webvtt", "-f", "webvtt", out+".vtt")
		case t.Codec == "hdmv_pgs_subtitle":
			args = append(append(args, m...), "-c:s", "copy", "-f", "sup", out+".sup")
		case IsImage(t.Codec):
			args = append(append(args, m...), "-c:s", "copy", "-f", "matroska", out+".mks")
		}
	}
	return args
}

// ConvertArgs builds the FFmpeg command that converts an external subtitle file (UTF-8 text, or a
// VobSub .idx) to the format we serve: WebVTT for text, Matroska for VobSub.
func ConvertArgs(src, dst string) []string {
	args := []string{"-nostdin", "-hide_banner", "-v", "error", "-i", "file:" + src, "-map", "0:s:0"}
	if len(dst) > 4 && dst[len(dst)-4:] == ".mks" {
		return append(args, "-c:s", "copy", "-f", "matroska", "file:"+dst)
	}
	return append(args, "-c:s", "webvtt", "-f", "webvtt", "file:"+dst)
}
