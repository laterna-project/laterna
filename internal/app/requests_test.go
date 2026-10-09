package app

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/arr"
	"github.com/laterna-project/laterna/internal/arr/arrtest"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/media/images"
	"github.com/laterna-project/laterna/internal/store"
)

// requestApp is an application with Sonarr and Radarr linked, a series library and a movie library,
// and no background work: jobs are run by hand.
func requestApp(t *testing.T) (a *App, clk *clock, admin domain.Principal, sonarr, radarr *arrtest.Server, shows, movies domain.Library) {
	t.Helper()
	a, clk = newTestApp(t)
	a.cacheDir = t.TempDir()
	_, admin = setupAdmin(t, a)
	ctx := context.Background()
	sonarr, radarr = arrtest.New(t, arr.Sonarr), arrtest.New(t, arr.Radarr)
	a.http = sonarr.Client()
	_, err := a.SetIntegration(ctx, admin, domain.IntegrationSonarr, sonarr.URL, arrtest.Key)
	mustNil(t, err)
	_, err = a.SetIntegration(ctx, admin, domain.IntegrationRadarr, radarr.URL, arrtest.Key)
	mustNil(t, err)
	now := clk.now()
	shows = domain.Library{ID: domain.NewID(), Name: "Anime", Kind: domain.LibraryShows, Paths: []string{t.TempDir()}, CreatedAt: now, UpdatedAt: now}
	movies = domain.Library{ID: domain.NewID(), Name: "Movies", Kind: domain.LibraryMovies, Paths: []string{t.TempDir()}, CreatedAt: now, UpdatedAt: now}
	mustNil(t, a.store.Write(ctx, func(q store.Q) error { return errors.Join(q.CreateLibrary(ctx, shows), q.CreateLibrary(ctx, movies)) }))
	return a, clk, admin, sonarr, radarr, shows, movies
}

// inCatalog puts a movie, or a series with episodes, in the catalog, carrying a TVDB or TMDB ID.
func putInCatalog(t *testing.T, a *App, lib domain.Library, title string, provider string, id int64, episodes int) domain.ID {
	t.Helper()
	ctx := context.Background()
	now := a.now()
	item := domain.Item{ID: domain.NewID(), LibraryID: lib.ID, Kind: domain.ItemMovie, GroupKey: title, Title: title, SortTitle: strings.ToLower(title), AddedAt: now, UpdatedAt: now}
	if episodes > 0 {
		item.Kind = domain.ItemSeries
	}
	mustNil(t, a.store.Write(ctx, func(q store.Q) error {
		steps := []error{q.CreateItem(ctx, item)}
		file := func(owner domain.ID, n int) {
			f := domain.MediaFile{ID: domain.NewID(), LibraryID: lib.ID, Path: lib.Paths[0] + "/" + title + strconv.Itoa(n) + ".mkv", Size: 1, ModTime: now, Fingerprint: title + strconv.Itoa(n)}
			steps = append(steps, q.CreateFile(ctx, f, now), q.LinkFile(ctx, owner, f.ID, "", 0))
		}
		if episodes == 0 {
			file(item.ID, 0)
		} else {
			season := domain.Item{ID: domain.NewID(), LibraryID: lib.ID, Kind: domain.ItemSeason, ParentID: &item.ID, GroupKey: title + ":1", Title: "Season 1", SortTitle: "0001", AddedAt: now, UpdatedAt: now}
			steps = append(steps, q.CreateItem(ctx, season), q.CreateSeason(ctx, domain.Season{ItemID: season.ID, SeriesID: item.ID, Number: 1}))
			for n := 1; n <= episodes; n++ {
				ep := domain.Item{ID: domain.NewID(), LibraryID: lib.ID, Kind: domain.ItemEpisode, ParentID: &season.ID, GroupKey: title + ":1:" + strconv.Itoa(n), Title: "Episode", SortTitle: strconv.Itoa(n), AddedAt: now, UpdatedAt: now}
				steps = append(steps, q.CreateItem(ctx, ep), q.CreateEpisode(ctx, domain.Episode{ItemID: ep.ID, SeriesID: item.ID, SeasonID: season.ID, SeasonNumber: 1, Number: n}))
				file(ep.ID, n)
			}
		}
		_, err := q.SetMetadata(ctx, item.ID, domain.Metadata{Title: title, SortTitle: strings.ToLower(title), ProviderIDs: map[string]string{provider: strconv.FormatInt(id, 10)}}, now)
		return errors.Join(append(steps, err)...)
	}))
	return item.ID
}

func titleStates(found []domain.RequestableTitle) map[string]domain.RequestableState {
	out := map[string]domain.RequestableState{}
	for _, f := range found {
		out[f.Title] = f.State
	}
	return out
}

func lastActivity(t *testing.T, a *App) domain.Activity {
	t.Helper()
	list, err := a.store.Read().Activity(context.Background(), store.ActivityQuery{Limit: 1})
	mustNil(t, err)
	if len(list) == 0 {
		t.Fatal("empty activity log")
	}
	return list[0]
}

func TestRequests(t *testing.T) {
	a, clk, admin, sonarr, _, shows, movies := requestApp(t)
	ctx := context.Background()
	sonarr.Set(func(s *arrtest.Server) {
		s.Catalog = []arrtest.Entry{
			{ExternalID: 101, Title: "Frieren", Year: 2023, Seasons: []int{1, 2}},
			{ExternalID: 102, Title: "Mushishi", Year: 2005, Seasons: []int{1, 2}},
			{ExternalID: 103, Title: "Monster", Year: 2004, Seasons: []int{1}},
			{ExternalID: 104, Title: "Mononoke", Year: 2007, Seasons: []int{1}},
			{ExternalID: 105, Title: "Mob Psycho", Year: 2016, Seasons: []int{1, 2, 3}},
		}
		// Sonarr already follows Mononoke.
		s.Titles[1] = &arrtest.Title{Entry: s.Catalog[3], ID: 1, Monitored: true}
	})
	putInCatalog(t, a, shows, "Monster", "tvdb", 103, 3)

	two := 2
	lea, err := a.CreateAccount(ctx, admin, NewAccount{Username: "Léa", Password: "a-password", RequestQuota: &two})
	mustNil(t, err)
	_, user := login(t, a, "Léa", "a-password")
	if user.Account.RequestQuota != 2 || user.Account.DenyRequests || user.Account.AutoApproveRequests {
		t.Fatalf("account rights: %+v", user.Account)
	}

	// Without a destination, nothing can be requested.
	if _, err := a.CreateRequest(ctx, user, NewRequest{Kind: domain.RequestSeries, ExternalID: 101}); domain.CodeOf(err) != "media_request.no_destination" {
		t.Errorf("no destination: %v", err)
	}

	// Destinations: a library of the right kind, a root folder and a profile Sonarr has.
	opts, err := a.RequestOptions(ctx, admin, domain.RequestSeries)
	if err != nil || len(opts.RootFolders) != 1 || len(opts.QualityProfiles) != 2 {
		t.Fatalf("options: %+v %v", opts, err)
	}
	name, root, hd, wrongRoot := "Anime", "/data/media/shows", 7, "/elsewhere"
	for code, ch := range map[string]DestinationChanges{
		"media_request.library_kind":            {Name: &name, Kind: domain.RequestSeries, LibraryID: &movies.ID, RootFolder: &root, QualityProfileID: &hd},
		"media_request.unknown_root_folder":     {Name: &name, Kind: domain.RequestSeries, LibraryID: &shows.ID, RootFolder: &wrongRoot, QualityProfileID: &hd},
		"media_request.destination_incomplete":  {Name: &name, Kind: domain.RequestSeries, LibraryID: &shows.ID},
		"media_request.invalid_series_type":     {Name: &name, Kind: domain.RequestSeries, LibraryID: &shows.ID, RootFolder: &root, QualityProfileID: &hd, SeriesType: "soap"},
		"media_request.unknown_quality_profile": {Name: &name, Kind: domain.RequestSeries, LibraryID: &shows.ID, RootFolder: &root, QualityProfileID: &two},
	} {
		if _, err := a.CreateRequestDestination(ctx, admin, ch); domain.CodeOf(err) != code {
			t.Errorf("%s: %v", code, err)
		}
	}
	dest, err := a.CreateRequestDestination(ctx, admin, DestinationChanges{
		Name: &name, Kind: domain.RequestSeries, LibraryID: &shows.ID, RootFolder: &root, QualityProfileID: &hd, SeriesType: domain.SeriesAnime,
	})
	mustNil(t, err)
	if dest.QualityProfileName != "HD Bluray + WEB" || dest.LibraryName != "Anime" {
		t.Errorf("destination: %+v", dest)
	}
	// A profile does not see how a destination is set on the instance.
	if list, err := a.RequestDestinations(ctx, user, ""); err != nil || len(list) != 1 || list[0].RootFolder != "" || list[0].Name != "Anime" {
		t.Errorf("destinations of a user: %+v %v", list, err)
	}

	// Search: requestable, available (in the catalog), tracked (Sonarr follows it).
	if _, err := a.SearchRequestable(ctx, user, domain.RequestSeries, " m "); !isKind(err, domain.ErrInvalid) {
		t.Errorf("short query: %v", err)
	}
	found, err := a.SearchRequestable(ctx, user, domain.RequestSeries, "mo")
	mustNil(t, err)
	if got := titleStates(found); got["Monster"] != domain.RequestableAvailable || got["Mononoke"] != domain.RequestableTracked || got["Mob Psycho"] != domain.Requestable {
		t.Errorf("states: %v", got)
	}

	// A request waits for an administrator; the requester and the administrators hear of it.
	subUser, subAdmin := a.Subscribe(user), a.Subscribe(admin)
	defer subUser.Close()
	defer subAdmin.Close()
	frieren, err := a.CreateRequest(ctx, user, NewRequest{Kind: domain.RequestSeries, ExternalID: 101})
	mustNil(t, err)
	if frieren.Status != domain.RequestPending || frieren.Seasons != domain.SeasonsAll || frieren.Destination == nil || frieren.Destination.RootFolder != "" || frieren.Username != "Léa" {
		t.Errorf("request: %+v", frieren)
	}
	for _, sub := range []*Subscription{subUser, subAdmin} {
		wait, cancel := context.WithTimeout(ctx, 5*time.Second)
		e, err := sub.Next(wait)
		cancel()
		if rc, ok := e.(domain.RequestsChanged); err != nil || !ok || !slices.Equal(rc.RequestIDs, []domain.ID{frieren.ID}) {
			t.Errorf("event: %#v %v", e, err)
		}
	}
	if act := lastActivity(t, a); act.Kind != domain.ActivityRequest || act.Text.Key != "activity.request_created" {
		t.Errorf("activity: %+v", act)
	}
	if _, err := a.CreateRequest(ctx, admin, NewRequest{Kind: domain.RequestSeries, ExternalID: 101}); domain.CodeOf(err) != "media_request.already_requested" {
		t.Errorf("requested twice: %v", err)
	}
	for id, code := range map[int64]string{103: "media_request.available", 104: "media_request.tracked", 999: "media_request.title_not_found"} {
		if _, err := a.CreateRequest(ctx, user, NewRequest{Kind: domain.RequestSeries, ExternalID: id}); domain.CodeOf(err) != code {
			t.Errorf("%d: %v", id, err)
		}
	}
	if found, _ := a.SearchRequestable(ctx, user, domain.RequestSeries, "frie"); len(found) != 1 || found[0].State != domain.RequestableRequested || *found[0].RequestID != frieren.ID {
		t.Errorf("requested: %+v", found)
	}

	// Quota: two requests in seven days, whatever becomes of them; withdrawing one frees it.
	mushishi, err := a.CreateRequest(ctx, user, NewRequest{Kind: domain.RequestSeries, ExternalID: 102, Seasons: domain.SeasonsChosen, SeasonNumbers: []int{2, 1, 2}})
	mustNil(t, err)
	if !slices.Equal(mushishi.SeasonNumbers, []int{1, 2}) {
		t.Errorf("chosen seasons: %v", mushishi.SeasonNumbers)
	}
	if _, err := a.CreateRequest(ctx, user, NewRequest{Kind: domain.RequestSeries, ExternalID: 105}); domain.CodeOf(err) != "media_request.quota_reached" {
		t.Errorf("quota: %v", err)
	}
	mustNil(t, a.CancelRequest(ctx, user, mushishi.ID))
	if err := a.CancelRequest(ctx, admin, frieren.ID); !isKind(err, domain.ErrNotFound) {
		t.Errorf("someone else's request withdrawn: %v", err)
	}
	mob, err := a.CreateRequest(ctx, user, NewRequest{Kind: domain.RequestSeries, ExternalID: 105})
	mustNil(t, err)
	clk.advance(domain.RequestQuotaWindow + time.Minute)

	// Declined, with a reason the requester reads.
	mob, err = a.DeclineRequest(ctx, admin, mob.ID, "  Already on Blu-ray at home  ")
	mustNil(t, err)
	if mob.Status != domain.RequestDeclined || mob.DeclineReason != "Already on Blu-ray at home" || mob.DecidedBy != "Chloé" {
		t.Errorf("declined: %+v", mob)
	}
	if _, err := a.ApproveRequest(ctx, admin, mob.ID, Approval{}); domain.CodeOf(err) != "media_request.already_decided" {
		t.Errorf("approved after a decline: %v", err)
	}

	// Approved for its first season only, then handed to Sonarr.
	frieren, err = a.ApproveRequest(ctx, admin, frieren.ID, Approval{Seasons: domain.SeasonsFirst})
	mustNil(t, err)
	if frieren.Status != domain.RequestApproved || frieren.Seasons != domain.SeasonsFirst || pendingJobs(t, a, jobSubmitRequest) != 1 {
		t.Errorf("approved: %+v", frieren)
	}
	mustNil(t, a.submitRequest(ctx, frieren.ID.String()))
	var arrID int
	sonarr.Get(func(s *arrtest.Server) {
		for id, tt := range s.Titles {
			if tt.ExternalID == 101 {
				arrID = id
				if !tt.Monitored || tt.SeriesType != "anime" || tt.RootFolder != root || !slices.Equal(tt.MonitoredSeasons, []int{1}) {
					t.Errorf("added: %+v", tt)
				}
			}
		}
	})
	got, err := a.Request(ctx, user, frieren.ID)
	if err != nil || got.ArrID != arrID || arrID == 0 {
		t.Fatalf("handed over: %+v %v", got, err)
	}
	if _, err := a.Request(ctx, admin, frieren.ID); err != nil {
		t.Errorf("read by an administrator: %v", err)
	}

	// Followed: downloading, then available from its first episode, then counting episodes.
	sonarr.Set(func(s *arrtest.Server) {
		s.Queue = []arr.Download{{ArrID: arrID, Size: 1000, Left: 400}}
		s.Titles[arrID].EpisodesWanted = 4
	})
	mustNil(t, a.refreshRequests(ctx, ""))
	if got, _ := a.Request(ctx, user, frieren.ID); got.Status != domain.RequestDownloading || got.Progress != 0.6 || got.EpisodesWanted != 4 {
		t.Errorf("downloading: %+v", got)
	}
	item := putInCatalog(t, a, shows, "Frieren", "tvdb", 101, 2)
	mustNil(t, a.refreshRequests(ctx, ""))
	got, _ = a.Request(ctx, user, frieren.ID)
	if got.Status != domain.RequestAvailable || got.ItemID == nil || *got.ItemID != item || got.EpisodesAvailable != 2 || got.AvailableAt == nil {
		t.Errorf("available: %+v", got)
	}
	if act := lastActivity(t, a); act.Text.Key != "activity.request_available" || act.ItemID == nil || *act.ItemID != item {
		t.Errorf("activity: %+v", act)
	}
	if ids, _ := a.store.Read().RequestsOnTheirWay(ctx, a.now().Add(-requestEpisodesFor)); !slices.Contains(ids, frieren.ID) {
		t.Errorf("a series still waiting for episodes is no longer followed: %v", ids)
	}

	// Lists: the profile's own, and all of them for an administrator.
	mine, err := a.MyRequests(ctx, user, "", 1)
	mustNil(t, err)
	if len(mine.Requests) != 1 || mine.Requests[0].ID != mob.ID || mine.NextPageToken == "" {
		t.Fatalf("first page: %+v", mine)
	}
	if next, err := a.MyRequests(ctx, user, mine.NextPageToken, 1); err != nil || len(next.Requests) != 1 || next.Requests[0].ID != frieren.ID || next.NextPageToken != "" {
		t.Errorf("second page: %+v %v", next, err)
	}
	all, err := a.Requests(ctx, admin, []domain.RequestStatus{domain.RequestDeclined}, "", 0)
	if err != nil || len(all.Requests) != 1 || all.Pending != 0 || all.Requests[0].Destination.RootFolder != root {
		t.Errorf("all: %+v %v", all, err)
	}

	// Rights: requests taken away, approved at once (never from a restricted profile).
	yes := true
	_, err = a.UpdateAccount(ctx, admin, lea.ID, AccountChanges{DenyRequests: &yes})
	mustNil(t, err)
	_, user = login(t, a, "Léa", "a-password")
	if _, err := a.CreateRequest(ctx, user, NewRequest{Kind: domain.RequestSeries, ExternalID: 105}); domain.CodeOf(err) != "media_request.denied" {
		t.Errorf("denied: %v", err)
	}
	if _, err := a.UpdateAccount(ctx, admin, admin.Account.ID, AccountChanges{DenyRequests: &yes}); domain.CodeOf(err) != "account.admin_always_requests" {
		t.Errorf("administrator denied: %v", err)
	}
	no, many := false, 1001
	if _, err := a.UpdateAccount(ctx, admin, lea.ID, AccountChanges{RequestQuota: &many}); domain.CodeOf(err) != "account.invalid_quota" {
		t.Errorf("quota out of range: %v", err)
	}
	_, err = a.UpdateAccount(ctx, admin, lea.ID, AccountChanges{DenyRequests: &no, AutoApproveRequests: &yes})
	mustNil(t, err)
	_, user = login(t, a, "Léa", "a-password")
	kid := user
	kid.Profile = &domain.Profile{ID: user.Profile.ID, AccountID: lea.ID, Name: "Tom", Kid: true}
	mob2, err := a.CreateRequest(ctx, kid, NewRequest{Kind: domain.RequestSeries, ExternalID: 105})
	mustNil(t, err)
	if mob2.Status != domain.RequestPending {
		t.Errorf("restricted profile approved at once: %+v", mob2)
	}
	mustNil(t, a.DeleteRequest(ctx, admin, mob2.ID))
	mustNil(t, a.DeleteRequest(ctx, admin, mob2.ID)) // idempotent
	mob2, err = a.CreateRequest(ctx, user, NewRequest{Kind: domain.RequestSeries, ExternalID: 105})
	mustNil(t, err)
	if mob2.Status != domain.RequestApproved || mob2.DecidedBy != "Léa" {
		t.Errorf("approved at once: %+v", mob2)
	}

	// Sonarr refuses: the request fails with the reason, and can be approved again.
	sonarr.Set(func(s *arrtest.Server) { s.Roots = []arr.RootFolder{{Path: "/other"}} })
	mustNil(t, a.submitRequest(ctx, mob2.ID.String()))
	got, _ = a.Request(ctx, admin, mob2.ID)
	if got.Status != domain.RequestFailed || got.Error == nil || got.Error.Key != "error.integration.refused" {
		t.Errorf("failed: %+v", got)
	}
	if act := lastActivity(t, a); act.Text.Key != "activity.request_failed" || !act.Warning {
		t.Errorf("activity: %+v", act)
	}
	sonarr.Set(func(s *arrtest.Server) { s.Roots = []arr.RootFolder{{Path: root}} })
	_, err = a.ApproveRequest(ctx, admin, mob2.ID, Approval{})
	mustNil(t, err)
	mustNil(t, a.submitRequest(ctx, mob2.ID.String()))
	if got, _ := a.Request(ctx, admin, mob2.ID); got.Status != domain.RequestApproved || got.ArrID == 0 || got.Error != nil {
		t.Errorf("approved again: %+v", got)
	}

	// The hand-over keeps failing (instance unreachable): the last attempt fails the request.
	mush2, err := a.CreateRequest(ctx, admin, NewRequest{Kind: domain.RequestSeries, ExternalID: 102})
	mustNil(t, err)
	a.jobFailed(ctx, jobSubmitRequest, mush2.ID.String(), context.DeadlineExceeded)
	if got, _ := a.Request(ctx, admin, mush2.ID); got.Status != domain.RequestFailed || got.Error.Key != "error.integration.timeout" {
		t.Errorf("failed for good: %+v", got)
	}

	// Removing a destination declines the requests waiting for it.
	_, err = a.UpdateAccount(ctx, admin, lea.ID, AccountChanges{AutoApproveRequests: &no})
	mustNil(t, err)
	_, user = login(t, a, "Léa", "a-password")
	mustNil(t, a.DeleteRequest(ctx, admin, mush2.ID))
	waiting, err := a.CreateRequest(ctx, user, NewRequest{Kind: domain.RequestSeries, ExternalID: 102})
	mustNil(t, err)
	mustNil(t, a.DeleteRequestDestination(ctx, admin, dest.ID))
	if got, _ := a.Request(ctx, user, waiting.ID); got.Status != domain.RequestDeclined || got.Destination != nil {
		t.Errorf("declined with its destination: %+v", got)
	}
}

func TestMovieRequest(t *testing.T) {
	a, _, admin, _, radarr, _, movies := requestApp(t)
	ctx := context.Background()
	radarr.Set(func(s *arrtest.Server) {
		s.Catalog = []arrtest.Entry{{ExternalID: 550, Title: "Perfect Blue", Year: 1997}}
	})
	name, root, anyQuality := "Movies", "/data/media/movies", 1
	_, err := a.CreateRequestDestination(ctx, admin, DestinationChanges{
		Name: &name, Kind: domain.RequestMovie, LibraryID: &movies.ID, RootFolder: &root, QualityProfileID: &anyQuality, SeriesType: domain.SeriesAnime,
	})
	mustNil(t, err)
	// An administrator's request is approved at once; a movie has no seasons.
	r, err := a.CreateRequest(ctx, admin, NewRequest{Kind: domain.RequestMovie, ExternalID: 550, Seasons: domain.SeasonsChosen, SeasonNumbers: []int{1}})
	mustNil(t, err)
	if r.Status != domain.RequestApproved || r.Seasons != domain.SeasonsAll || r.SeasonNumbers != nil || r.Destination.SeriesType != domain.SeriesStandard {
		t.Errorf("request: %+v", r)
	}
	mustNil(t, a.submitRequest(ctx, r.ID.String()))
	got, _ := a.Request(ctx, admin, r.ID)
	radarr.Set(func(s *arrtest.Server) { s.Queue = []arr.Download{{ArrID: got.ArrID, Size: 100, Left: 100}} })
	mustNil(t, a.refreshRequests(ctx, ""))
	if got, _ := a.Request(ctx, admin, r.ID); got.Status != domain.RequestDownloading || got.Progress != 0 {
		t.Errorf("downloading: %+v", got)
	}
	// Out of the queue, not in the catalog yet: being imported.
	radarr.Set(func(s *arrtest.Server) { s.Queue = nil })
	mustNil(t, a.refreshRequests(ctx, ""))
	if got, _ := a.Request(ctx, admin, r.ID); got.Status != domain.RequestDownloading || got.Progress != 1 {
		t.Errorf("imported: %+v", got)
	}
	item := putInCatalog(t, a, movies, "Perfect Blue", "tmdb", 550, 0)
	mustNil(t, a.refreshRequests(ctx, ""))
	if got, _ := a.Request(ctx, admin, r.ID); got.Status != domain.RequestAvailable || *got.ItemID != item {
		t.Errorf("available: %+v", got)
	}
	if ids, _ := a.store.Read().RequestsOnTheirWay(ctx, a.now().Add(-requestEpisodesFor)); len(ids) != 0 {
		t.Errorf("an available movie is still followed: %v", ids)
	}
	if found, _ := a.SearchRequestable(ctx, admin, domain.RequestMovie, "blue"); len(found) != 1 || found[0].State != domain.RequestableAvailable {
		t.Errorf("search: %+v", found)
	}
}

func TestRequestPosters(t *testing.T) {
	a, clk, admin, sonarr, _, shows, _ := requestApp(t)
	ctx := context.Background()
	var hits atomic.Int32
	img := image.NewRGBA(image.Rect(0, 0, 600, 900))
	for y := range 900 {
		for x := range 600 {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 100, A: 255})
		}
	}
	var buf bytes.Buffer
	mustNil(t, png.Encode(&buf, img))
	posters := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(buf.Bytes())
	}))
	t.Cleanup(posters.Close)
	poster := posters.URL + "/frieren.png"
	sonarr.Set(func(s *arrtest.Server) {
		s.Catalog = []arrtest.Entry{{ExternalID: 101, Title: "Frieren", Seasons: []int{1}, Poster: poster}}
	})
	name, root, hd := "Anime", "/data/media/shows", 7
	_, err := a.CreateRequestDestination(ctx, admin, DestinationChanges{Name: &name, Kind: domain.RequestSeries, LibraryID: &shows.ID, RootFolder: &root, QualityProfileID: &hd})
	mustNil(t, err)

	// Only what a search returned can be fetched.
	key := posterKey(poster)
	if _, err := a.RequestPoster(ctx, key); !isKind(err, domain.ErrNotFound) {
		t.Errorf("poster never returned: %v", err)
	}
	for _, bad := range []string{"", "../../etc/passwd", strings.Repeat("g", 32)} {
		if _, err := a.RequestPoster(ctx, bad); !isKind(err, domain.ErrNotFound) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	found, err := a.SearchRequestable(ctx, admin, domain.RequestSeries, "frieren")
	mustNil(t, err)
	if len(found) != 1 || RequestPosterPath(found[0].Poster) != "/requests/posters/"+key {
		t.Fatalf("search: %+v", found)
	}
	path, err := a.RequestPoster(ctx, key)
	mustNil(t, err)
	if info, err := images.Analyze(path); err != nil || info.Width != posterWidth {
		t.Errorf("scaled down: %+v %v", info, err)
	}
	// Kept: served again without fetching it, even once the search is forgotten.
	clk.advance(2 * posterTTL)
	if again, err := a.RequestPoster(ctx, key); err != nil || again != path || hits.Load() != 1 {
		t.Errorf("cached: %v %d", err, hits.Load())
	}
	// A request keeps its poster beyond the hour; the purge keeps it too.
	_, err = a.CreateRequest(ctx, admin, NewRequest{Kind: domain.RequestSeries, ExternalID: 101})
	mustNil(t, err)
	clk.advance(48 * time.Hour)
	mustNil(t, a.purgePosters(ctx))
	if _, err := a.RequestPoster(ctx, key); err != nil || hits.Load() != 1 {
		t.Errorf("kept by the request: %v %d", err, hits.Load())
	}
	if smallerPoster("https://image.tmdb.org/t/p/original/abc.jpg") != "https://image.tmdb.org/t/p/w500/abc.jpg" || smallerPoster(poster) != poster {
		t.Error("smaller TMDB poster")
	}
}
