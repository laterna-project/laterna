package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/laterna-project/laterna/internal/arr"
	"github.com/laterna-project/laterna/internal/arr/arrtest"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/lazylibrarian/lazylibrariantest"
	"github.com/laterna-project/laterna/internal/store"
)

// newLibrary stores a library of a kind on a temporary folder, without scanning it.
func newLibrary(t *testing.T, a *App, name string, kind domain.LibraryKind) domain.Library {
	t.Helper()
	ctx := context.Background()
	now := a.now()
	lib := domain.Library{ID: domain.NewID(), Name: name, Kind: kind, Paths: []string{t.TempDir()}, CreatedAt: now, UpdatedAt: now}
	mustNil(t, a.store.Write(ctx, func(q store.Q) error { return q.CreateLibrary(ctx, lib) }))
	return lib
}

// putAlbum puts an artist and one of its albums, with a track file, in the catalog, carrying their
// MusicBrainz IDs.
func putAlbum(t *testing.T, a *App, lib domain.Library, artist, artistMBID, album, albumMBID string) domain.ID {
	t.Helper()
	ctx := context.Background()
	now := a.now()
	var albumID domain.ID
	mustNil(t, a.store.Write(ctx, func(q store.Q) error {
		ar, err := q.ItemByGroupKey(ctx, lib.ID, "artist:"+artist)
		if store.IsNotFound(err) {
			ar = domain.Item{ID: domain.NewID(), LibraryID: lib.ID, Kind: domain.ItemArtist, GroupKey: "artist:" + artist, Title: artist, SortTitle: strings.ToLower(artist), AddedAt: now, UpdatedAt: now}
			if err := q.CreateItem(ctx, ar); err != nil {
				return err
			}
			if _, err := q.SetMetadata(ctx, ar.ID, domain.Metadata{Title: artist, SortTitle: strings.ToLower(artist), ProviderIDs: map[string]string{"musicbrainz_artist": artistMBID}}, now); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		al := domain.Item{ID: domain.NewID(), LibraryID: lib.ID, Kind: domain.ItemAlbum, ParentID: &ar.ID, GroupKey: "album:" + album, Title: album, SortTitle: strings.ToLower(album), AddedAt: now, UpdatedAt: now}
		tr := domain.Item{ID: domain.NewID(), LibraryID: lib.ID, Kind: domain.ItemTrack, ParentID: &al.ID, GroupKey: "track:" + album, Title: "One", SortTitle: "one", AddedAt: now, UpdatedAt: now}
		f := domain.MediaFile{ID: domain.NewID(), LibraryID: lib.ID, Path: lib.Paths[0] + "/" + album + ".flac", Size: 1, ModTime: now, Fingerprint: album}
		albumID = al.ID
		if err := errors.Join(q.CreateItem(ctx, al), q.CreateItem(ctx, tr), q.SetTrack(ctx, domain.Track{ItemID: tr.ID, AlbumID: al.ID, ArtistID: ar.ID, Number: 1}),
			q.CreateFile(ctx, f, now), q.LinkFile(ctx, tr.ID, f.ID, "", 0)); err != nil {
			return err
		}
		_, err = q.SetMetadata(ctx, al.ID, domain.Metadata{Title: album, SortTitle: strings.ToLower(album), ProviderIDs: map[string]string{"musicbrainz_releasegroup": albumMBID}}, now)
		return err
	}))
	return albumID
}

// putBook puts a book with a file in the catalog.
func putBook(t *testing.T, a *App, lib domain.Library, title, author, isbn string) domain.ID {
	t.Helper()
	ctx := context.Background()
	now := a.now()
	b := domain.Item{ID: domain.NewID(), LibraryID: lib.ID, Kind: domain.ItemBook, GroupKey: "book:" + title, Title: title, SortTitle: strings.ToLower(title), AddedAt: now, UpdatedAt: now}
	f := domain.MediaFile{ID: domain.NewID(), LibraryID: lib.ID, Path: lib.Paths[0] + "/" + title + ".epub", Size: 1, ModTime: now, Fingerprint: title}
	mustNil(t, a.store.Write(ctx, func(q store.Q) error {
		if err := q.CreateItem(ctx, b); err != nil {
			return err
		}
		_, err := q.SetMetadata(ctx, b.ID, domain.Metadata{Title: title, SortTitle: strings.ToLower(title), Credits: []domain.Credit{{Name: author, Role: domain.RoleWriter}}}, now)
		return errors.Join(err, q.SetBook(ctx, domain.Book{ItemID: b.ID, ISBN: isbn}), q.CreateFile(ctx, f, now), q.LinkFile(ctx, b.ID, f.ID, "", 0))
	}))
	return b.ID
}

func TestLidarrAndLazyLibrarianIntegrations(t *testing.T) {
	a, _ := startApp(t)
	_, admin := setupAdmin(t, a)
	ctx := context.Background()
	lidarr, ll := arrtest.New(t, arr.Lidarr), lazylibrariantest.New(t)
	a.http = lidarr.Client()

	// Lidarr: the music libraries, artist.nfo, the webhook that scans them.
	music := newLibrary(t, a, "Music", domain.LibraryMusic)
	mustNil(t, os.MkdirAll(filepath.Join(music.Paths[0], "Daft Punk"), 0o750))
	writeText(t, filepath.Join(music.Paths[0], "Daft Punk", "artist.nfo"), "<artist><name>Daft Punk</name></artist>")
	mustNil(t, os.MkdirAll(filepath.Join(music.Paths[0], "Justice"), 0o750))
	lidarr.Set(func(s *arrtest.Server) {
		s.Folders = []arr.Folder{{Title: "Daft Punk", Path: "/music/Daft Punk", HasFiles: true}, {Title: "Justice", Path: "/music/Justice", HasFiles: true}}
	})
	st, err := a.SetIntegration(ctx, admin, domain.IntegrationLidarr, lidarr.URL, arrtest.Key)
	mustNil(t, err)
	if !st.Reachable || !st.ManagesMetadata || len(st.MissingOptions) != 5 || st.Folders != 2 || st.Unmapped != 0 || !slices.Equal(st.WithoutNFOTitles, []string{"Justice"}) {
		t.Errorf("Lidarr: %+v", st)
	}
	hooks := hookServer(t, a)
	st, err = a.ConfigureIntegration(ctx, admin, domain.IntegrationLidarr, domain.IntegrationSetup{KodiMetadata: true, WebhookURL: hooks.URL})
	mustNil(t, err)
	if !st.KodiMetadata || !st.Webhook {
		t.Errorf("Lidarr set up: %+v", st)
	}
	before := pendingJobs(t, a, jobScanLibrary)
	mustNil(t, lidarr.Send(map[string]any{"eventType": "Download", "artist": map[string]any{"path": "/music/Daft Punk"}}))
	if pendingJobs(t, a, jobScanLibrary) != before+1 {
		t.Error("no scan of the music library after an import")
	}

	// LazyLibrarian: only the connection; nothing to set up.
	if _, err := a.SetIntegration(ctx, admin, domain.IntegrationLazyLibrarian, ll.URL, "wrong"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("wrong key: %v", err)
	}
	st, err = a.SetIntegration(ctx, admin, domain.IntegrationLazyLibrarian, ll.URL, lazylibrariantest.Key)
	mustNil(t, err)
	if !st.Reachable || st.ManagesMetadata || st.Version != "8006a0fc" {
		t.Errorf("LazyLibrarian: %+v", st)
	}
	if _, err := a.ConfigureIntegration(ctx, admin, domain.IntegrationLazyLibrarian, domain.IntegrationSetup{Refresh: true}); domain.CodeOf(err) != "integration.not_configurable" {
		t.Errorf("set up: %v", err)
	}
	list, err := a.Integrations(ctx)
	mustNil(t, err)
	if len(list) != 4 || !list[3].Reachable || list[3].URL != ll.URL {
		t.Errorf("integrations: %+v", list)
	}
	mustNil(t, a.DeleteIntegration(ctx, admin, domain.IntegrationLazyLibrarian))
	if list, _ := a.Integrations(ctx); len(list) != 4 || list[3].URL != "" {
		t.Errorf("forgotten: %+v", list)
	}
}

func TestMusicRequests(t *testing.T) {
	a, _, admin, _, _, _, _ := requestApp(t)
	ctx := context.Background()
	lidarr := arrtest.New(t, arr.Lidarr)
	lidarr.Set(func(s *arrtest.Server) {
		s.Music = []arrtest.MusicEntry{
			{MBID: "mb-daft", Name: "Daft Punk", Albums: []arrtest.AlbumEntry{
				{MBID: "rg-homework", Title: "Homework", Date: "1997-01-20"},
				{MBID: "rg-discovery", Title: "Discovery", Date: "2001-03-12", Cover: "https://images.example/discovery.jpg"},
			}},
			{MBID: "mb-justice", Name: "Justice", Albums: []arrtest.AlbumEntry{
				{MBID: "rg-cross", Title: "Cross", Date: "2007-06-11"},
				{MBID: "rg-audio", Title: "Audio, Video, Disco", Date: "2011-10-24"},
			}},
		}
	})
	_, err := a.SetIntegration(ctx, admin, domain.IntegrationLidarr, lidarr.URL, arrtest.Key)
	mustNil(t, err)
	music := newLibrary(t, a, "Music", domain.LibraryMusic)
	putAlbum(t, a, music, "Daft Punk", "mb-daft", "Homework", "rg-homework")

	// A music destination needs Lidarr's metadata profile.
	opts, err := a.RequestOptions(ctx, admin, domain.RequestMusic)
	if err != nil || len(opts.MetadataProfiles) != 2 || opts.RootFolders[0].Path != "/data/media/music" {
		t.Fatalf("options: %+v %v", opts, err)
	}
	name, root, hd, standard, unknown := "Music", "/data/media/music", 7, 1, 9
	for code, ch := range map[string]DestinationChanges{
		"media_request.destination_incomplete":   {Name: &name, Kind: domain.RequestMusic, LibraryID: &music.ID, RootFolder: &root, QualityProfileID: &hd},
		"media_request.unknown_metadata_profile": {Name: &name, Kind: domain.RequestMusic, LibraryID: &music.ID, RootFolder: &root, QualityProfileID: &hd, MetadataProfileID: &unknown},
	} {
		if _, err := a.CreateRequestDestination(ctx, admin, ch); domain.CodeOf(err) != code {
			t.Errorf("%s: %v", code, err)
		}
	}
	dest, err := a.CreateRequestDestination(ctx, admin, DestinationChanges{
		Name: &name, Kind: domain.RequestMusic, LibraryID: &music.ID, RootFolder: &root, QualityProfileID: &hd, MetadataProfileID: &standard,
	})
	mustNil(t, err)
	if dest.MetadataProfileName != "Standard" || dest.Kind != domain.RequestMusic {
		t.Errorf("destination: %+v", dest)
	}

	// Searching music finds artists and albums; what the catalog has is available.
	states := map[string]domain.RequestableState{}
	for _, q := range []string{"homework", "disc", "cross"} {
		found, err := a.SearchRequestable(ctx, admin, domain.RequestMusic, q)
		mustNil(t, err)
		for _, f := range found {
			states[string(f.Kind)+" "+f.Title] = f.State
		}
	}
	if states["album Homework"] != domain.RequestableAvailable || states["album Discovery"] != domain.Requestable ||
		states["album Cross"] != domain.Requestable || states["album Audio, Video, Disco"] != domain.Requestable {
		t.Errorf("states: %v", states)
	}
	if found, _ := a.SearchRequestable(ctx, admin, domain.RequestMusic, "daft"); len(found) != 1 || found[0].Kind != domain.RequestArtist || found[0].State != domain.RequestableAvailable {
		t.Errorf("artist in the catalog: %+v", found)
	}
	if _, err := a.SearchRequestable(ctx, admin, domain.RequestArtist, "daft"); domain.CodeOf(err) != "media_request.invalid_kind" {
		t.Errorf("search by request kind: %v", err)
	}

	// An album from a user: pending, then approved and handed to Lidarr.
	two := 2
	_, err = a.CreateAccount(ctx, admin, NewAccount{Username: "Léa", Password: "a-password", RequestQuota: &two})
	mustNil(t, err)
	_, user := login(t, a, "Léa", "a-password")
	for key, code := range map[string]string{"": "media_request.invalid_external_id", "rg-homework": "media_request.available", "rg-unknown": "media_request.title_not_found"} {
		if _, err := a.CreateRequest(ctx, user, NewRequest{Kind: domain.RequestAlbum, ExternalKey: key}); domain.CodeOf(err) != code {
			t.Errorf("%q: %v", key, err)
		}
	}
	disco, err := a.CreateRequest(ctx, user, NewRequest{Kind: domain.RequestAlbum, ExternalKey: "rg-discovery"})
	mustNil(t, err)
	if disco.Status != domain.RequestPending || disco.Subtitle != "Daft Punk" || disco.Poster != "https://images.example/discovery.jpg" || disco.ExternalID != 0 {
		t.Errorf("album request: %+v", disco)
	}
	if found, _ := a.SearchRequestable(ctx, user, domain.RequestMusic, "disco"); len(found) == 0 || found[0].State != domain.RequestableRequested {
		t.Errorf("requested: %+v", found)
	}
	_, err = a.ApproveRequest(ctx, admin, disco.ID, Approval{})
	mustNil(t, err)
	mustNil(t, a.submitRequest(ctx, disco.ID.String()))
	disco, _ = a.Request(ctx, admin, disco.ID)
	var artistID int
	lidarr.Get(func(s *arrtest.Server) {
		al := s.Albums[disco.ArrID]
		if al == nil || al.Title != "Discovery" || !al.Monitored {
			t.Fatalf("album on Lidarr: %+v", al)
		}
		artistID = al.ArtistID
		if a := s.Artists[artistID]; a.MonitorNewItems != "none" || a.MetadataProfileID != 1 || a.RootFolder != root {
			t.Errorf("artist on Lidarr: %+v", a)
		}
	})

	// Followed album by album: downloading, then in the catalog.
	lidarr.Set(func(s *arrtest.Server) {
		s.Queue = []arr.Download{{ArrID: artistID, AlbumID: disco.ArrID, Size: 100, Left: 50}, {ArrID: artistID, AlbumID: 999, Size: 100, Left: 0}}
		s.Albums[disco.ArrID].TrackFiles = 4
	})
	mustNil(t, a.refreshRequests(ctx, ""))
	if got, _ := a.Request(ctx, user, disco.ID); got.Status != domain.RequestDownloading || got.Progress != 0.5 || got.EpisodesWanted != 10 {
		t.Errorf("downloading: %+v", got)
	}
	item := putAlbum(t, a, music, "Daft Punk", "mb-daft", "Discovery", "rg-discovery")
	lidarr.Set(func(s *arrtest.Server) { s.Queue = nil })
	mustNil(t, a.refreshRequests(ctx, ""))
	if got, _ := a.Request(ctx, user, disco.ID); got.Status != domain.RequestAvailable || *got.ItemID != item || got.EpisodesAvailable != 4 {
		t.Errorf("available: %+v", got)
	}

	// An artist for its latest album, from an administrator: approved at once.
	if _, err := a.CreateRequest(ctx, admin, NewRequest{Kind: domain.RequestArtist, ExternalKey: "mb-justice", Seasons: domain.SeasonsChosen}); domain.CodeOf(err) != "media_request.invalid_seasons" {
		t.Errorf("chosen albums: %v", err)
	}
	justice, err := a.CreateRequest(ctx, admin, NewRequest{Kind: domain.RequestArtist, ExternalKey: "mb-justice", Seasons: domain.SeasonsLatest})
	mustNil(t, err)
	if justice.Status != domain.RequestApproved || justice.Seasons != domain.SeasonsLatest {
		t.Errorf("artist request: %+v", justice)
	}
	mustNil(t, a.submitRequest(ctx, justice.ID.String()))
	lidarr.Get(func(s *arrtest.Server) {
		var monitored []string
		for _, al := range s.Albums {
			if al.Monitored && strings.HasPrefix(al.MBID, "rg-") && al.ArtistID != artistID {
				monitored = append(monitored, al.Title)
			}
		}
		if !slices.Equal(monitored, []string{"Audio, Video, Disco"}) {
			t.Errorf("albums of Justice: %v", monitored)
		}
	})
}

func TestBookRequests(t *testing.T) {
	a, _, admin, _, _, _, _ := requestApp(t)
	ctx := context.Background()
	ll := lazylibrariantest.New(t)
	ll.Set(func(s *lazylibrariantest.Server) {
		s.Catalog = []lazylibrariantest.Entry{
			{ID: "OL893414W", Title: "Dune", Author: "Frank Herbert", Year: 1965, Cover: "http://covers.openlibrary.org/b/id/1-S.jpg"},
			{ID: "OL45804W", Title: "Fantastic Mr Fox", Author: "Roald Dahl", ISBN: "9780140328721"},
			{ID: "OL27448W", Title: "The Lord of the Rings", Author: "J.R.R. Tolkien"},
		}
		s.Status["OL27448W"] = "Wanted"
	})
	books := newLibrary(t, a, "Books", domain.LibraryBooks)
	putBook(t, a, books, "Fantastic Mr. Fox", "Roald Dahl", "978-0-14-032872-1")

	// Without LazyLibrarian, books cannot be requested.
	name := "Books"
	if _, err := a.CreateRequestDestination(ctx, admin, DestinationChanges{Name: &name, Kind: domain.RequestBook, LibraryID: &books.ID}); domain.CodeOf(err) != "media_request.unavailable" {
		t.Errorf("without LazyLibrarian: %v", err)
	}
	_, err := a.SetIntegration(ctx, admin, domain.IntegrationLazyLibrarian, ll.URL, lazylibrariantest.Key)
	mustNil(t, err)
	if opts, err := a.RequestOptions(ctx, admin, domain.RequestBook); err != nil || len(opts.RootFolders) != 0 {
		t.Errorf("options: %+v %v", opts, err)
	}
	dest, err := a.CreateRequestDestination(ctx, admin, DestinationChanges{Name: &name, Kind: domain.RequestBook, LibraryID: &books.ID})
	mustNil(t, err)
	if dest.RootFolder != "" || dest.Kind != domain.RequestBook {
		t.Errorf("destination: %+v", dest)
	}

	// Search: requestable, available by ISBN, on its way at LazyLibrarian.
	states := map[string]domain.RequestableState{}
	for _, q := range []string{"dune", "fox", "lord"} {
		found, err := a.SearchRequestable(ctx, admin, domain.RequestBook, q)
		mustNil(t, err)
		for _, f := range found {
			states[f.Title] = f.State
		}
	}
	if states["Dune"] != domain.Requestable || states["Fantastic Mr Fox"] != domain.RequestableAvailable || states["The Lord of the Rings"] != domain.RequestableTracked {
		t.Errorf("states: %v", states)
	}

	// Requested, approved at once, handed to LazyLibrarian as a wanted ebook.
	dune, err := a.CreateRequest(ctx, admin, NewRequest{Kind: domain.RequestBook, ExternalKey: "OL893414W"})
	mustNil(t, err)
	if dune.Subtitle != "Frank Herbert" || dune.Year != 1965 || dune.Poster != "https://covers.openlibrary.org/b/id/1-L.jpg" {
		t.Errorf("request: %+v", dune)
	}
	if _, err := a.CreateRequest(ctx, admin, NewRequest{Kind: domain.RequestBook, ExternalKey: "OL0W"}); domain.CodeOf(err) != "media_request.title_not_found" {
		t.Errorf("never searched: %v", err)
	}
	mustNil(t, a.submitRequest(ctx, dune.ID.String()))
	ll.Get(func(s *lazylibrariantest.Server) {
		if s.Status["OL893414W"] != "Wanted" || !slices.Contains(s.Commands, "searchBook OL893414W eBook") {
			t.Errorf("LazyLibrarian: %v %v", s.Status, s.Commands)
		}
	})

	// Followed: snatched, filed, then in the catalog by title and author.
	mustNil(t, a.refreshRequests(ctx, ""))
	if got, _ := a.Request(ctx, admin, dune.ID); got.Status != domain.RequestApproved {
		t.Errorf("wanted: %+v", got)
	}
	ll.Set(func(s *lazylibrariantest.Server) { s.Status["OL893414W"] = "Snatched" })
	mustNil(t, a.refreshRequests(ctx, ""))
	if got, _ := a.Request(ctx, admin, dune.ID); got.Status != domain.RequestDownloading || got.Progress != 0 {
		t.Errorf("snatched: %+v", got)
	}
	ll.Set(func(s *lazylibrariantest.Server) { s.Status["OL893414W"] = "Have" })
	mustNil(t, a.refreshRequests(ctx, ""))
	if got, _ := a.Request(ctx, admin, dune.ID); got.Status != domain.RequestDownloading || got.Progress != 1 {
		t.Errorf("filed: %+v", got)
	}
	item := putBook(t, a, books, "Dune", "Frank Herbert", "")
	mustNil(t, a.refreshRequests(ctx, ""))
	if got, _ := a.Request(ctx, admin, dune.ID); got.Status != domain.RequestAvailable || *got.ItemID != item {
		t.Errorf("available: %+v", got)
	}
}
