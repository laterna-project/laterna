package app

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/playback"
)

// imageSource returns the source of the image of a given kind of an item ("" without an image).
func imageSource(v domain.ItemView, kind domain.ImageKind) domain.ImageSource {
	for _, img := range v.Images {
		if img.Kind == kind {
			return img.Source
		}
	}
	return ""
}

// Music on the fixtures: an artist with an NFO and an image, an album with an NFO and a cover next
// to it, a two-disc album with an embedded cover, an untagged file.
func TestMusic(t *testing.T) {
	a, _ := startMediaApp(t)
	_, p := setupAdmin(t, a)
	ctx := context.Background()
	lib, err := a.CreateLibrary(ctx, "Musique", domain.LibraryMusic, []string{testRoot("Musique")}, "")
	mustNil(t, err)
	waitIdle(t, a)

	artists, err := a.ListArtists(ctx, p, ListQuery{})
	mustNil(t, err)
	if got := titles(artists.Items); !slices.Equal(got, []string{"Artiste Test", domain.UnknownArtist}) {
		t.Fatalf("artists: %v", got)
	}
	artistID := artists.Items[0].Item.ID
	artist, d, albums, err := a.Artist(ctx, p, artistID)
	mustNil(t, err)
	if artist.Item.Overview != "Artiste inventé pour les tests." || d.ProviderIDs["musicbrainz_artist"] != "00000000-0000-0000-0000-00000000a001" ||
		imageSource(artist, domain.ImagePoster) != domain.ImageLocal || artist.AlbumCount != 2 || artist.TrackCount != 4 {
		t.Errorf("artist: %+v %+v", artist, d)
	}
	if got := titles(albums); len(albums) != 2 || !slices.Equal(got, []string{"Deuxième Album", "Album Test"}) {
		t.Fatalf("albums, newest first: %v", got)
	}

	// First album: NFO and cover next to it, two tracks in order.
	first, d, tracks, err := a.Album(ctx, p, albums[1].Item.ID)
	mustNil(t, err)
	if first.Item.Year != 2020 || first.Item.Overview != "Premier album de l'artiste de test." || first.ArtistName != "Artiste Test" ||
		d.ProviderIDs["musicbrainz_release"] != "00000000-0000-0000-0000-00000000b001" || !slices.Equal(d.Genres, []string{"Électro"}) ||
		imageSource(first, domain.ImagePoster) != domain.ImageLocal || first.TrackCount != 2 || first.Item.Runtime < 20*time.Second {
		t.Errorf("first album: %+v %+v", first, d)
	}
	if got := titles(tracks); !slices.Equal(got, []string{"Piste Un", "Piste Deux"}) {
		t.Errorf("tracks: %v", got)
	}

	// Second album: two discs, cover taken from the files, a "feat." track, ReplayGain.
	second, d, tracks, err := a.Album(ctx, p, albums[0].Item.ID)
	mustNil(t, err)
	if second.Item.PremiereDate != "2022-05-13" || imageSource(second, domain.ImagePoster) != domain.ImageEmbedded ||
		!slices.Equal(d.Genres, []string{"Ambient", "Électro"}) {
		t.Errorf("second album: %+v %+v", second, d)
	}
	if len(tracks) != 2 || tracks[0].Track == nil || tracks[1].Track == nil {
		t.Fatalf("tracks of the second album: %+v", tracks)
	}
	if t2 := tracks[1].Track; tracks[0].Track.Disc != 1 || t2.Disc != 2 || t2.Number != 1 || t2.Artists != "Artiste Test feat. Invité" ||
		t2.TrackGain == nil || *t2.TrackGain != -4.10 || t2.TrackPeak == nil || *t2.TrackPeak != 0.95 || tracks[1].AlbumTitle != "Deuxième Album" {
		t.Errorf("track of the second disc: %+v %+v", tracks[1], t2)
	}
	// A track shows the cover of its album.
	if imageSource(tracks[1], domain.ImagePoster) != domain.ImageEmbedded {
		t.Errorf("cover of a track: %+v", tracks[1].Images)
	}

	// Untagged file at the root.
	loose, err := a.ArtistTracks(ctx, p, artists.Items[1].Item.ID)
	mustNil(t, err)
	if len(loose) != 1 || loose[0].Item.Title != "Sans étiquette" || loose[0].AlbumTitle != domain.UnknownAlbum {
		t.Errorf("untagged file: %+v", loose)
	}
	all, err := a.ArtistTracks(ctx, p, artistID)
	mustNil(t, err)
	if got := titles(all); !slices.Equal(got, []string{"Piste Un", "Piste Deux", "Premier", "Second"}) {
		t.Errorf("all tracks of the artist: %v", got)
	}

	// Search, library counts, genres.
	found, err := a.Search(ctx, p, "deuxieme", 10)
	mustNil(t, err)
	if len(found) != 1 || found[0].Item.Kind != domain.ItemAlbum {
		t.Errorf("search for an album: %+v", titles(found))
	}
	libs, err := a.CatalogLibraries(ctx, p)
	mustNil(t, err)
	if len(libs) != 1 || libs[0].Library.ID != lib.ID || libs[0].Counts[domain.ItemArtist] != 2 ||
		libs[0].Counts[domain.ItemAlbum] != 3 || libs[0].Counts[domain.ItemTrack] != 5 {
		t.Errorf("counts: %+v", libs)
	}

	// Playback: direct if the device plays FLAC, otherwise converted to AAC.
	web := playback.DeviceProfile{Containers: []string{"flac", "mp3", "mp4"}, AudioCodecs: []string{"flac", "mp3", "aac"}}
	info, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: tracks[0].Item.ID, Audio: -1, Device: web})
	mustNil(t, err)
	if info.Method != playback.Direct {
		t.Errorf("want direct play: %+v", info)
	}
	mustNil(t, a.StopPlayback(ctx, p, info.SessionID, 0))
	phone := playback.DeviceProfile{Containers: []string{"m4a"}, AudioCodecs: []string{"aac"}}
	info, err = a.StartPlayback(ctx, p, PlayRequest{ItemID: tracks[0].Item.ID, Audio: -1, Device: phone})
	mustNil(t, err)
	if info.Method != playback.Convert || info.CopyAudio {
		t.Fatalf("want a conversion: %+v", info)
	}
	path, err := a.DirectFile(ctx, info.SessionID, info.Token)
	mustNil(t, err)
	converted, err := a.prober.Probe(ctx, path)
	mustNil(t, err)
	if !strings.HasSuffix(path, "-aac.m4a") || len(converted.Streams) != 1 || converted.Streams[0].Codec != "aac" ||
		converted.Duration < 11*time.Second {
		t.Errorf("converted file: %s %+v", path, converted)
	}
	// Played to the end: counted, without a resume point, and the album shows up on the home page.
	mustNil(t, a.StopPlayback(ctx, p, info.SessionID, 11*time.Second))
	played, err := a.store.Read().View(ctx, domain.Viewer{ProfileID: p.Profile.ID}, tracks[0].Item.ID)
	mustNil(t, err)
	if !played.UserData.Played || played.UserData.PlayCount != 1 || played.UserData.Position != 0 {
		t.Errorf("played track: %+v", played.UserData)
	}
	// A second playback reuses the same conversion.
	again, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: tracks[0].Item.ID, Audio: -1, Device: phone})
	mustNil(t, err)
	if again2, err := a.DirectFile(ctx, again.SessionID, again.Token); err != nil || again2 != path {
		t.Errorf("conversion served again: %s %v", again2, err)
	}
	mustNil(t, a.StopPlayback(ctx, p, again.SessionID, 0))
	if _, err := os.Stat(path); err != nil {
		t.Errorf("conversion kept: %v", err)
	}

	rows, err := a.Home(ctx, p, 0)
	mustNil(t, err)
	var kinds []HomeRowKind
	for _, r := range rows {
		kinds = append(kinds, r.Kind)
	}
	if len(rows) != 2 || !slices.Equal(kinds, []HomeRowKind{RowRecentAlbums, RowLatestAlbums}) ||
		!slices.Equal(titles(rows[0].Items), []string{"Deuxième Album"}) {
		t.Fatalf("home: %v", kinds)
	}

	// "Played" on an album means all its tracks; a playlist from an artist holds their tracks.
	mustNil(t, a.SetPlayed(ctx, p, first.Item.ID, true))
	firstTracks, err := a.store.Read().Tracks(ctx, domain.Viewer{ProfileID: p.Profile.ID}, first.Item)
	mustNil(t, err)
	for _, tr := range firstTracks {
		if !tr.UserData.Played {
			t.Errorf("track not marked as played: %s", tr.Item.Title)
		}
	}
	pl, err := a.CreatePlaylist(ctx, p, "Tout l'artiste", []domain.ID{artistID})
	mustNil(t, err)
	if pl.EntryCount != 4 || len(pl.Images) == 0 {
		t.Errorf("playlist: %+v", pl)
	}

	// A kid profile sees music: it has no rating.
	ten, prof := 10, *p.Profile
	prof.Parental = domain.ParentalControl{MaxAge: &ten, BlockUnrated: true}
	kid := p
	kid.Profile = &prof
	if page, err := a.ListAlbums(ctx, kid, ListQuery{}); err != nil || page.Total != 3 {
		t.Errorf("albums of a kid profile: %+v %v", page.Total, err)
	}
}
