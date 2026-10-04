package rpc

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/store"
	"github.com/laterna-project/laterna/internal/testfixtures"
)

// fixtureLibrary is a library made from a fixture folder.
type fixtureLibrary struct {
	name string
	kind laternav1.LibraryKind
}

// mediaServer starts a full server (background work included) whose "Films" and "Séries" fixture
// libraries are scanned, and returns an administrator token.
func mediaServer(t *testing.T) (*testServer, laternav1connect.CatalogServiceClient, string) {
	t.Helper()
	return mediaServerWith(t, fixtureLibrary{"Films", laternav1.LibraryKind_LIBRARY_KIND_MOVIES},
		fixtureLibrary{"Séries", laternav1.LibraryKind_LIBRARY_KIND_SHOWS})
}

// mediaServerWith starts a full server whose given libraries are scanned.
func mediaServerWith(t *testing.T, libs ...fixtureLibrary) (*testServer, laternav1connect.CatalogServiceClient, string) {
	t.Helper()
	testfixtures.Library(t)
	ffmpeg, ffprobe, _ := testfixtures.FFmpeg()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(ctx, st, app.Options{
		ServerName: "Test", FFmpeg: ffmpeg, FFprobe: ffprobe, NoAutoScans: true, CacheDir: t.TempDir(),
		SegmentMin: 2 * time.Second, // fixture intros are 3 s long
	})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	if err := a.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Mount(mux, a, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.Close()
		cancel()
		a.Wait()
		_ = st.Close()
	})
	c := srv.Client()
	s := &testServer{
		url: srv.URL, server: laternav1connect.NewServerServiceClient(c, srv.URL),
		auth: laternav1connect.NewAuthServiceClient(c, srv.URL), profile: laternav1connect.NewProfileServiceClient(c, srv.URL),
		library: laternav1connect.NewLibraryServiceClient(c, srv.URL),
	}
	token := setup(t, s)
	for _, l := range libs {
		_, err := s.library.CreateLibrary(ctx, withToken(&laternav1.CreateLibraryRequest{
			Name: l.name, Kind: l.kind, Paths: []string{filepath.Join(testfixtures.Root(), l.name)},
		}, token))
		if err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		counts, err := st.Read().CountJobs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(counts) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job queue never empty: %+v", counts)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return s, laternav1connect.NewCatalogServiceClient(c, srv.URL), token
}

func TestCatalogServiceOverHTTP(t *testing.T) {
	_, catalog, token := mediaServer(t)
	ctx := context.Background()

	libs, err := catalog.ListCatalogLibraries(ctx, withToken(&laternav1.ListCatalogLibrariesRequest{}, token))
	if err != nil || len(libs.Msg.GetLibraries()) != 2 {
		t.Fatalf("libraries: %v %v", libs, err)
	}

	var movies []*laternav1.MovieSummary
	req := &laternav1.ListMoviesRequest{PageSize: 2, Sort: laternav1.ItemSort_ITEM_SORT_TITLE}
	for {
		page, err := catalog.ListMovies(ctx, withToken(req, token))
		if err != nil {
			t.Fatal(err)
		}
		if page.Msg.GetTotalSize() != 5 {
			t.Fatalf("total: %d", page.Msg.GetTotalSize())
		}
		movies = append(movies, page.Msg.GetMovies()...)
		if page.Msg.GetNextPageToken() == "" {
			break
		}
		req.PageToken = page.Msg.GetNextPageToken()
	}
	if len(movies) != 5 || movies[0].GetTitle() != "Big Test Movie" || movies[0].GetRuntime().AsDuration() != time.Minute {
		t.Fatalf("movies: %v", movies)
	}
	if _, err := catalog.ListMovies(ctx, withToken(&laternav1.ListMoviesRequest{PageToken: "not-a-token"}, token)); code(err) != connect.CodeInvalidArgument {
		t.Errorf("invalid token: %v", err)
	}

	movie, err := catalog.GetMovie(ctx, withToken(&laternav1.GetMovieRequest{MovieId: movies[0].GetId()}, token))
	if err != nil {
		t.Fatal(err)
	}
	m := movie.Msg
	if m.GetMovie().GetOverview() == "" || len(m.GetMovie().GetImages()) != 2 || len(m.GetFiles()) != 1 || !m.GetFiles()[0].GetAvailable() || len(m.GetFiles()[0].GetStreams()) == 0 {
		t.Errorf("movie details: %v", m)
	}
	if _, err := catalog.GetMovie(ctx, withToken(&laternav1.GetMovieRequest{MovieId: libs.Msg.GetLibraries()[0].GetId()}, token)); code(err) != connect.CodeNotFound {
		t.Errorf("unknown movie: %v", err)
	}

	series, err := catalog.ListSeries(ctx, withToken(&laternav1.ListSeriesRequest{}, token))
	if err != nil || len(series.Msg.GetSeries()) != 1 {
		t.Fatalf("series: %v %v", series, err)
	}
	seriesID := series.Msg.GetSeries()[0].GetId()
	if _, err := catalog.SetPlayed(ctx, withToken(&laternav1.SetPlayedRequest{ItemId: seriesID, Played: true}, token)); err != nil {
		t.Fatal(err)
	}
	detail, err := catalog.GetSeries(ctx, withToken(&laternav1.GetSeriesRequest{SeriesId: seriesID}, token))
	if err != nil || !detail.Msg.GetSeries().GetUserData().GetPlayed() || len(detail.Msg.GetSeasons()) != 2 || detail.Msg.GetSeasons()[1].GetNumber() != 2 {
		t.Fatalf("series played: %v %v", detail, err)
	}
	eps, err := catalog.ListEpisodes(ctx, withToken(&laternav1.ListEpisodesRequest{SeriesId: seriesID, SeasonId: detail.Msg.GetSeasons()[0].GetId()}, token))
	if err != nil || len(eps.Msg.GetEpisodes()) != 3 || eps.Msg.GetEpisodes()[0].GetNumber() != 1 || eps.Msg.GetEpisodes()[0].GetUserData().GetLastPlayedAt() == nil {
		t.Fatalf("episodes: %v %v", eps, err)
	}
	ep, err := catalog.GetEpisode(ctx, withToken(&laternav1.GetEpisodeRequest{EpisodeId: eps.Msg.GetEpisodes()[0].GetId()}, token))
	if err != nil || ep.Msg.GetEpisode().GetSeriesTitle() != "Série Test" || len(ep.Msg.GetFiles()) != 1 {
		t.Fatalf("episode details: %v %v", ep, err)
	}
	// Similar titles: an episode stands for its series.
	if _, err := catalog.ListSimilar(ctx, withToken(&laternav1.ListSimilarRequest{ItemId: ep.Msg.GetEpisode().GetId()}, token)); err != nil {
		t.Errorf("titles similar to an episode: %v", err)
	}
	if _, err := catalog.ListSimilar(ctx, withToken(&laternav1.ListSimilarRequest{ItemId: "01a0edcc-0000-7000-8000-000000000000"}, token)); code(err) != connect.CodeNotFound {
		t.Errorf("titles similar to an unknown item: %v", err)
	}
	// Intro found by audio in the other episodes.
	if segs := ep.Msg.GetFiles()[0].GetSegments(); len(segs) != 1 || segs[0].GetKind() != laternav1.MediaSegmentKind_MEDIA_SEGMENT_KIND_INTRO ||
		segs[0].GetEnd().AsDuration() < 2500*time.Millisecond {
		t.Errorf("segments of the episode: %v", segs)
	}

	if _, err := catalog.SetFavorite(ctx, withToken(&laternav1.SetFavoriteRequest{ItemId: movies[1].GetId(), Favorite: true}, token)); err != nil {
		t.Fatal(err)
	}
	favs, err := catalog.ListMovies(ctx, withToken(&laternav1.ListMoviesRequest{FavoritesOnly: true}, token))
	if err != nil || len(favs.Msg.GetMovies()) != 1 || !favs.Msg.GetMovies()[0].GetUserData().GetFavorite() {
		t.Errorf("favorites: %v %v", favs, err)
	}

	found, err := catalog.Search(ctx, withToken(&laternav1.SearchRequest{Query: "serie"}, token))
	if err != nil || len(found.Msg.GetResults()) == 0 || found.Msg.GetResults()[0].GetSeries().GetId() != seriesID {
		t.Errorf("search: %v %v", found, err)
	}
	genres, err := catalog.ListGenres(ctx, withToken(&laternav1.ListGenresRequest{}, token))
	if err != nil || len(genres.Msg.GetGenres()) == 0 {
		t.Errorf("genres: %v %v", genres, err)
	}

	// The catalog requires a session.
	if _, err := catalog.ListMovies(ctx, withToken(&laternav1.ListMoviesRequest{}, "")); code(err) != connect.CodeUnauthenticated {
		t.Errorf("without a token: %v", err)
	}
}
