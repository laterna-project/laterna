package rpc

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
)

func TestMusicServiceOverHTTP(t *testing.T) {
	s, catalog, token := mediaServerWith(t, fixtureLibrary{"Music", laternav1.LibraryKind_LIBRARY_KIND_MUSIC})
	ctx := context.Background()
	c := http.DefaultClient
	music := laternav1connect.NewMusicServiceClient(c, s.url)

	libs, err := catalog.ListCatalogLibraries(ctx, withToken(&laternav1.ListCatalogLibrariesRequest{}, token))
	if err != nil || len(libs.Msg.GetLibraries()) != 1 {
		t.Fatalf("libraries: %v %v", libs, err)
	}
	if l := libs.Msg.GetLibraries()[0]; l.GetKind() != laternav1.LibraryKind_LIBRARY_KIND_MUSIC ||
		l.GetCounts().GetArtists() != 2 || l.GetCounts().GetAlbums() != 3 || l.GetCounts().GetTracks() != 5 {
		t.Errorf("music library: %v", l)
	}

	artists, err := music.ListArtists(ctx, withToken(&laternav1.ListArtistsRequest{}, token))
	if err != nil || len(artists.Msg.GetArtists()) != 2 || artists.Msg.GetTotalSize() != 2 {
		t.Fatalf("artists: %v %v", artists, err)
	}
	// The artist of untagged tracks has a name made up by the server: written in the language of
	// the request (English here, since none is asked for) and given for translation. A real name
	// has no text to translate.
	if unknown := artists.Msg.GetArtists()[1]; unknown.GetName() != "Unknown artist" || unknown.GetNameText().GetKey() != "name.unknown_artist" ||
		artists.Msg.GetArtists()[0].GetNameText() != nil {
		t.Errorf("unknown artist: %v", artists.Msg.GetArtists())
	}
	artist := artists.Msg.GetArtists()[0]
	full, err := music.GetArtist(ctx, withToken(&laternav1.GetArtistRequest{ArtistId: artist.GetId()}, token))
	if err != nil || full.Msg.GetArtist().GetName() != "Artist Test" || len(full.Msg.GetAlbums()) != 2 ||
		full.Msg.GetArtist().GetOverview() == "" || len(full.Msg.GetArtist().GetImages()) == 0 {
		t.Fatalf("artist: %v %v", full, err)
	}
	albums, err := music.ListAlbums(ctx, withToken(&laternav1.ListAlbumsRequest{ArtistId: artist.GetId(), Sort: laternav1.ItemSort_ITEM_SORT_TITLE}, token))
	if err != nil || len(albums.Msg.GetAlbums()) != 2 || albums.Msg.GetAlbums()[0].GetTitle() != "Album Test" {
		t.Fatalf("albums of the artist: %v %v", albums, err)
	}
	second := full.Msg.GetAlbums()[0]
	album, err := music.GetAlbum(ctx, withToken(&laternav1.GetAlbumRequest{AlbumId: second.GetId()}, token))
	if err != nil || len(album.Msg.GetTracks()) != 2 || album.Msg.GetAlbum().GetArtistName() != "Artist Test" ||
		album.Msg.GetAlbum().GetPremiereDate() != "2022-05-13" {
		t.Fatalf("album: %v %v", album, err)
	}
	track := album.Msg.GetTracks()[1]
	if track.GetDisc() != 2 || track.GetArtists() != "Artist Test feat. Guest" || track.GetReplayGain().GetTrackGain() != -4.1 ||
		track.GetReplayGain().AlbumGain != nil || len(track.GetImages()) == 0 || track.GetAlbumId() != second.GetId() {
		t.Errorf("track: %v", track)
	}
	got, err := music.GetTrack(ctx, withToken(&laternav1.GetTrackRequest{TrackId: track.GetId()}, token))
	if err != nil || len(got.Msg.GetFiles()) != 1 || got.Msg.GetFiles()[0].GetVersion() != "FLAC" {
		t.Errorf("track details: %v %v", got, err)
	}
	all, err := music.ListArtistTracks(ctx, withToken(&laternav1.ListArtistTracksRequest{ArtistId: artist.GetId()}, token))
	if err != nil || len(all.Msg.GetTracks()) != 4 {
		t.Errorf("tracks of the artist: %v %v", all, err)
	}
	// An album is not an artist.
	if _, err := music.ListArtistTracks(ctx, withToken(&laternav1.ListArtistTracksRequest{ArtistId: second.GetId()}, token)); code(err) != connect.CodeNotFound {
		t.Errorf("album taken for an artist: %v", err)
	}

	found, err := catalog.Search(ctx, withToken(&laternav1.SearchRequest{Query: "second"}, token))
	if err != nil || len(found.Msg.GetResults()) != 1 || found.Msg.GetResults()[0].GetTrack().GetTitle() != "Second" {
		t.Errorf("search for a track: %v %v", found, err)
	}

	// Converted playback: the URL is that of a file, not of an HLS playlist.
	playback := laternav1connect.NewPlaybackServiceClient(c, s.url)
	play, err := playback.StartPlayback(ctx, withToken(&laternav1.StartPlaybackRequest{
		ItemId: track.GetId(), Device: &laternav1.DeviceProfile{Containers: []string{"m4a"}, AudioCodecs: []string{"aac"}},
	}, token))
	if err != nil || play.Msg.GetMethod() != laternav1.PlaybackMethod_PLAYBACK_METHOD_CONVERTED ||
		!strings.HasSuffix(play.Msg.GetUrl(), "/direct") || !play.Msg.GetAudioTranscoded() {
		t.Fatalf("converted playback: %v %v", play, err)
	}
	if _, err := playback.StopPlayback(ctx, withToken(&laternav1.StopPlaybackRequest{SessionId: play.Msg.GetSessionId()}, token)); err != nil {
		t.Error(err)
	}
}
