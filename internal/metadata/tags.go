package metadata

import (
	"strconv"
	"strings"
)

// Tags is what the tags of an audio file say (Vorbis, ID3, MP4), as ffprobe reports them, with
// lower-case keys.
type Tags struct {
	Title string
	// Artist is the track artists as written. AlbumArtist is the album's artist, empty if the tags
	// do not set it apart.
	Artist, AlbumArtist string
	Album               string
	// Disc and Number ("4/14" gives 4); 0 if missing.
	Disc, Number int
	Year         int
	// Date as YYYY-MM-DD, when the tags give a full date.
	Date   string
	Genres []string
	// Compilation marks an album by several artists (iTunes "cpil", Picard "compilation").
	Compilation bool
	// Sort keys ("Beatles, The"), empty if missing.
	ArtistSort, AlbumSort, TitleSort string
	// ReplayGain in dB and peaks; nil if missing or unreadable.
	TrackGain, TrackPeak, AlbumGain, AlbumPeak *float64
	// IDs are MusicBrainz IDs: musicbrainz_recording (track), musicbrainz_release and
	// musicbrainz_releasegroup (album), musicbrainz_artist (album artist).
	IDs map[string]string
}

// ParseTags reads the tags of an audio file. The same thing often goes by several names depending
// on the format; the first one present wins.
func ParseTags(tags map[string]string) Tags {
	get := func(keys ...string) string {
		for _, k := range keys {
			if v := text(tags[k]); v != "" {
				return v
			}
		}
		return ""
	}
	t := Tags{
		Title:       get("title"),
		Artist:      get("artist", "artists"),
		AlbumArtist: get("album_artist", "albumartist", "album artist", "album-artist"),
		Album:       get("album"),
		Disc:        leadingNumber(get("disc", "discnumber", "disk")),
		Number:      leadingNumber(get("track", "tracknumber")),
		ArtistSort:  get("albumartistsort", "album_artist-sort", "album-artist-sort", "artistsort", "artist-sort"),
		AlbumSort:   get("albumsort", "album-sort"),
		TitleSort:   get("titlesort", "title-sort"),
		TrackGain:   replayGain(get("replaygain_track_gain")),
		TrackPeak:   replayGain(get("replaygain_track_peak")),
		AlbumGain:   replayGain(get("replaygain_album_gain")),
		AlbumPeak:   replayGain(get("replaygain_album_peak")),
	}
	switch strings.ToLower(get("compilation", "itunescompilation", "cpil", "tcmp")) {
	case "1", "true", "yes":
		t.Compilation = true
	}
	// Release date of this edition, otherwise the original date.
	for _, d := range []string{get("date", "year", "tdrc", "tyer"), get("originaldate", "originalyear", "tdor")} {
		if y := leadingNumber(d); y >= 1000 && y <= 9999 {
			t.Year = y
			t.Date = date(d)
			break
		}
	}
	seen := map[string]bool{}
	for g := range strings.SplitSeq(get("genre"), ";") {
		if g = strings.TrimSpace(g); g != "" && !seen[strings.ToLower(g)] {
			seen[strings.ToLower(g)] = true
			t.Genres = append(t.Genres, g)
		}
	}
	for key, names := range map[string][]string{
		"musicbrainz_recording":    {"musicbrainz_trackid", "musicbrainz track id"},
		"musicbrainz_release":      {"musicbrainz_albumid", "musicbrainz album id"},
		"musicbrainz_releasegroup": {"musicbrainz_releasegroupid", "musicbrainz release group id"},
		"musicbrainz_artist":       {"musicbrainz_albumartistid", "musicbrainz album artist id"},
	} {
		if v := get(names...); v != "" {
			if t.IDs == nil {
				t.IDs = map[string]string{}
			}
			// Several artists: take the first ID.
			t.IDs[key], _, _ = strings.Cut(v, ";")
		}
	}
	return t
}

// leadingNumber reads the number at the start of a value ("4/14", "2005-03-01"); 0 otherwise.
func leadingNumber(s string) int {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, _ := strconv.Atoi(s[:end])
	return n
}

// replayGain reads a ReplayGain value ("-6.44 dB", "0.988525"); nil if unreadable.
func replayGain(s string) *float64 {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.ToLower(s)), "db"))
	if s == "" {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &f
}
