package rpc

import (
	"context"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/bazarr"
	"github.com/laterna-project/laterna/internal/bazarr/bazarrtest"
	"github.com/laterna-project/laterna/internal/testfixtures"
)

// Subtitle searches over HTTP: Bazarr linked, a search asked for a movie, and its end read back.
func TestSubtitleSearchOverHTTP(t *testing.T) {
	s, catalog, token := mediaServer(t)
	ctx := context.Background()
	movies, err := catalog.ListMovies(ctx, withToken(&laternav1.ListMoviesRequest{PageSize: 1}, token))
	if err != nil || len(movies.Msg.GetMovies()) != 1 {
		t.Fatalf("movies: %v %v", movies, err)
	}
	movie, err := catalog.GetMovie(ctx, withToken(&laternav1.GetMovieRequest{MovieId: movies.Msg.GetMovies()[0].GetId()}, token))
	if err != nil || len(movie.Msg.GetFiles()) == 0 {
		t.Fatalf("movie: %v %v", movie, err)
	}
	file := movie.Msg.GetFiles()[0]

	subtitles := laternav1connect.NewSubtitleServiceClient(http.DefaultClient, s.url)
	status := func() *laternav1.GetSubtitleSearchResponse {
		t.Helper()
		resp, err := subtitles.GetSubtitleSearch(ctx, withToken(&laternav1.GetSubtitleSearchRequest{FileId: file.GetId()}, token))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg
	}
	if got := status(); got.GetAvailable() || len(got.GetLanguages()) != 0 {
		t.Errorf("without Bazarr: %v", got)
	}

	// Bazarr follows the movies, and finds subtitles that it saves where Laterna does not look.
	var followed []*bazarr.Video
	err = filepath.WalkDir(filepath.Join(testfixtures.Root(), "Movies"), func(p string, d fs.DirEntry, err error) error {
		if ext := strings.ToLower(filepath.Ext(p)); err == nil && !d.IsDir() && (ext == ".mkv" || ext == ".mp4") {
			followed = append(followed, &bazarr.Video{ID: len(followed) + 1, Path: "/elsewhere/" + filepath.Base(p)})
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	fake := bazarrtest.New(t)
	fake.Set(func(b *bazarrtest.Server) {
		b.Movies = followed
		b.Find = func(v bazarr.Video, _ bazarr.Wanted) string { return v.Path + ".fr.srt" }
	})
	integrations := laternav1connect.NewIntegrationServiceClient(http.DefaultClient, s.url)
	set, err := integrations.SetIntegration(ctx, withToken(&laternav1.SetIntegrationRequest{
		Kind: laternav1.IntegrationKind_INTEGRATION_KIND_BAZARR, Url: fake.URL, ApiKey: bazarrtest.Key,
	}, token))
	if err != nil || !set.Msg.GetIntegration().GetReachable() || set.Msg.GetIntegration().GetManagesMetadata() {
		t.Fatalf("Bazarr linked: %v %v", set, err)
	}
	got := status()
	if !got.GetAvailable() || len(got.GetLanguages()) != 2 || got.GetLanguages()[1].GetCode() != "fr" || got.GetLanguages()[1].GetName() != "French" {
		t.Fatalf("with Bazarr: %v", got)
	}

	for want, req := range map[connect.Code]*laternav1.SearchSubtitleRequest{
		connect.CodeInvalidArgument: {FileId: file.GetId(), Language: "de"},
		connect.CodeNotFound:        {FileId: "0190f0f0-0000-7000-8000-000000000000", Language: "fr"},
	} {
		if _, err := subtitles.SearchSubtitle(ctx, withToken(req, token)); code(err) != want {
			t.Errorf("%v: %v", want, err)
		}
	}
	started, err := subtitles.SearchSubtitle(ctx, withToken(&laternav1.SearchSubtitleRequest{FileId: file.GetId(), Language: "fr", Forced: true}, token))
	if err != nil {
		t.Fatal(err)
	}
	if st := started.Msg.GetSearch(); st.GetState() != laternav1.SubtitleSearchState_SUBTITLE_SEARCH_STATE_SEARCHING ||
		st.GetFileId() != file.GetId() || st.GetLanguage() != "fr" || !st.GetForced() || st.GetHearingImpaired() || st.GetStartedAt() == nil {
		t.Errorf("search: %v", st)
	}
	var end *laternav1.SubtitleSearch
	for deadline := time.Now().Add(30 * time.Second); end == nil; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the search did not end")
		}
		if list := status().GetSearches(); len(list) == 1 && list[0].GetState() != laternav1.SubtitleSearchState_SUBTITLE_SEARCH_STATE_SEARCHING {
			end = list[0]
		}
	}
	if end.GetState() != laternav1.SubtitleSearchState_SUBTITLE_SEARCH_STATE_FAILED || end.GetErrorText().GetKey() != "subtitle_search.not_visible" ||
		end.GetError() != "Bazarr found a subtitle, but saved it where Laterna does not see it" {
		t.Errorf("end of the search: %v", end)
	}
}
