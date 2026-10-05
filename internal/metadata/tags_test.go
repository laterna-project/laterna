package metadata

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseTags(t *testing.T) {
	gain := func(f float64) *float64 { return &f }
	tests := []struct {
		name string
		tags map[string]string
		want Tags
	}{
		{"FLAC tagged by Picard", map[string]string{
			"title": "Harder, Better, Faster, Stronger", "artist": "Daft Punk", "album_artist": "Daft Punk",
			"album": "Discovery", "track": "4", "disc": "1", "date": "2001-03-12", "genre": "Electronic; House;electronic",
			"replaygain_track_gain": "-6.44 dB", "replaygain_track_peak": "1", "replaygain_album_gain": "-6.83 dB",
			"musicbrainz_albumid": "a", "musicbrainz_releasegroupid": "b", "musicbrainz_albumartistid": "c;d", "musicbrainz_trackid": "e",
		}, Tags{
			Title: "Harder, Better, Faster, Stronger", Artist: "Daft Punk", AlbumArtist: "Daft Punk", Album: "Discovery",
			Disc: 1, Number: 4, Year: 2001, Date: "2001-03-12", Genres: []string{"Electronic", "House"},
			TrackGain: gain(-6.44), TrackPeak: gain(1), AlbumGain: gain(-6.83),
			IDs: map[string]string{"musicbrainz_release": "a", "musicbrainz_releasegroup": "b", "musicbrainz_artist": "c", "musicbrainz_recording": "e"},
		}},
		{"MP3 (ID3): track over total, year only, compilation", map[string]string{
			"title": " Title ", "artist": "A feat. B", "album": "Café", "track": "03/12", "disc": "2/2", "date": "1999",
			"compilation": "1", "album_artist-sort": "Artists, Various",
		}, Tags{Title: "Title", Artist: "A feat. B", Album: "Café", Disc: 2, Number: 3, Year: 1999, Compilation: true, ArtistSort: "Artists, Various"}},
		{"original year when there is no date", map[string]string{"originalyear": "1977", "replaygain_track_gain": "n/a"}, Tags{Year: 1977}},
		{"nothing", map[string]string{"encoder": "Lavf"}, Tags{}},
	}
	for _, tc := range tests {
		if got := ParseTags(tc.tags); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n  %+v\nwant %+v", tc.name, got, tc.want)
		}
	}
}

func TestParseMusicNFO(t *testing.T) {
	album, err := ParseNFO(strings.NewReader(`<?xml version="1.0" encoding="utf-8" standalone="yes"?>
<album>
  <review>Second album.</review>
  <title>Discovery</title>
  <year>2001</year>
  <releasedate>2001-03-12</releasedate>
  <genre>Electronic</genre>
  <musicbrainzalbumid>release-id</musicbrainzalbumid>
  <musicbrainzreleasegroupid>group-id</musicbrainzreleasegroupid>
  <art><poster>/media/music/Discovery/cover.jpg</poster></art>
  <artist>Daft Punk</artist>
  <track><position>1</position><title>One More Time</title></track>
</album>`))
	if err != nil {
		t.Fatal(err)
	}
	if album.Kind != "album" || album.Title != "Discovery" || album.Year != 2001 || album.Premiered != "2001-03-12" ||
		album.Plot != "Second album." || !reflect.DeepEqual(album.Genres, []string{"Electronic"}) ||
		!reflect.DeepEqual(album.IDs, map[string]string{"musicbrainz_release": "release-id", "musicbrainz_releasegroup": "group-id"}) {
		t.Errorf("album: %+v", album)
	}
	artist, err := ParseNFO(strings.NewReader(`<artist><name>The Beatles</name><sortname>Beatles, The</sortname>
<biography>A band from Liverpool.</biography><musicbrainzartistid>artist-id</musicbrainzartistid></artist>`))
	if err != nil {
		t.Fatal(err)
	}
	if artist.Kind != "artist" || artist.Title != "The Beatles" || artist.SortTitle != "Beatles, The" ||
		artist.Plot != "A band from Liverpool." || artist.IDs["musicbrainz_artist"] != "artist-id" {
		t.Errorf("artist: %+v", artist)
	}
}
