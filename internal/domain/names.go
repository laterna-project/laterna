package domain

import (
	"strconv"
	"strings"
)

// Names the server makes up when it has nothing better: a season almost never has a title, an
// episode sometimes does not, an untagged track has no artist or album. They are stored in English,
// the way files already spell them ("Various Artists" is the compilation artist on MusicBrainz,
// "Episode 5" is what TheTVDB calls an episode it has no title for yet), so a made-up name and the
// same name read from a file are one and the same. Whoever displays it recognizes it (SeasonName,
// EpisodeName, ArtistName, AlbumName, BookName) and translates it.
const (
	UnknownArtist  = "Unknown Artist"
	VariousArtists = "Various Artists"
	UnknownAlbum   = "Unknown Album"
	specials       = "Specials"
)

// SeasonTitle is the title of an untitled season: "Season 2", or "Specials" for season 0.
func SeasonTitle(number int) string {
	if number == 0 {
		return specials
	}
	return "Season " + strconv.Itoa(number)
}

// EpisodeTitle is the title of an untitled episode: "Episode 5".
func EpisodeTitle(number int) string { return "Episode " + strconv.Itoa(number) }

// SeasonName reports whether title is a made-up season title and returns the text to translate.
func SeasonName(title string, number int) (Text, bool) {
	if !strings.EqualFold(title, SeasonTitle(number)) {
		return Text{}, false
	}
	if number == 0 {
		return T("name.specials"), true
	}
	return T("name.season", "number", number), true
}

// EpisodeName reports whether title is a made-up episode title and returns the text to translate.
func EpisodeName(title string, number int) (Text, bool) {
	if !strings.EqualFold(title, EpisodeTitle(number)) {
		return Text{}, false
	}
	return T("name.episode", "number", number), true
}

// ArtistName reports whether name is one of the artist names given for lack of tags and returns the
// text to translate.
func ArtistName(name string) (Text, bool) {
	switch {
	case strings.EqualFold(name, UnknownArtist):
		return T("name.unknown_artist"), true
	case strings.EqualFold(name, VariousArtists):
		return T("name.various_artists"), true
	}
	return Text{}, false
}

// AlbumName reports whether title is the album title given for lack of tags and returns the text to
// translate.
func AlbumName(title string) (Text, bool) {
	if strings.EqualFold(title, UnknownAlbum) {
		return T("name.unknown_album"), true
	}
	return Text{}, false
}

// BookTitle is the title of an untitled volume: "Series – Volume 3".
func BookTitle(series string, number float64) string {
	return series + " – Volume " + strconv.FormatFloat(number, 'f', -1, 64)
}

// BookName reports whether title is a made-up volume title and returns the text to translate.
func BookName(title, series string, number float64) (Text, bool) {
	if series == "" || title != BookTitle(series, number) {
		return Text{}, false
	}
	return T("name.book_volume", "series", series, "number", strconv.FormatFloat(number, 'f', -1, 64)), true
}
