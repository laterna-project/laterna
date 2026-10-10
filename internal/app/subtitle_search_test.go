package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/bazarr"
	"github.com/laterna-project/laterna/internal/bazarr/bazarrtest"
	"github.com/laterna-project/laterna/internal/domain"
)

// searchEnd waits for the searches of a file to end and returns them.
func searchEnd(t *testing.T, a *App, p domain.Principal, fileID domain.ID) []domain.SubtitleSearch {
	t.Helper()
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		info, err := a.SubtitleSearch(context.Background(), p, fileID)
		mustNil(t, err)
		if !slices.ContainsFunc(info.Searches, func(s domain.SubtitleSearch) bool { return s.State == domain.SubtitleSearching }) {
			return info.Searches
		}
		if time.Now().After(deadline) {
			t.Fatalf("searches still under way: %+v", info.Searches)
		}
	}
}

// A profile asks for a subtitle its movie does not have: Bazarr finds it, saves it next to the
// video, and the subtitles of the file are read again.
func TestSubtitleSearch(t *testing.T) {
	a, p, _, file, dir := subtitledMovie(t)
	ctx := context.Background()
	a.subtitleSearchWait, a.subtitleSearchPoll = 400*time.Millisecond, 10*time.Millisecond

	// Without Bazarr there is nothing to offer.
	if info, err := a.SubtitleSearch(ctx, p, file.ID); err != nil || info.Available || len(info.Languages) != 0 {
		t.Errorf("without Bazarr: %+v %v", info, err)
	}
	if _, err := a.SearchSubtitle(ctx, p, file.ID, domain.SubtitleWanted{Language: "es"}); domain.CodeOf(err) != "subtitle.search_unavailable" {
		t.Errorf("search without Bazarr: %v", err)
	}

	// Bazarr sees the same files under another folder.
	fake := bazarrtest.New(t)
	seen := "/data/media/movies/Fonts (2022)/"
	fake.Set(func(s *bazarrtest.Server) {
		s.Languages = []bazarr.Language{{Code: "en", Name: "English"}, {Code: "es", Name: "Spanish"}}
		s.Disabled = []bazarr.Language{{Code: "de", Name: "German"}}
		s.Movies = []*bazarr.Video{{ID: 3, Title: "Fonts", Path: seen + filepath.Base(file.Path)}}
		s.Find = func(_ bazarr.Video, w bazarr.Wanted) string {
			if w.Language != "es" {
				return ""
			}
			name := strings.TrimSuffix(filepath.Base(file.Path), filepath.Ext(file.Path)) + ".es.srt"
			if err := os.WriteFile(filepath.Join(dir, name), []byte("1\n00:00:01,000 --> 00:00:02,000\nHola\n"), 0o600); err != nil {
				t.Error(err)
			}
			return seen + name
		}
	})
	if _, err := a.SetIntegration(ctx, p, domain.IntegrationBazarr, fake.URL, "wrong"); !isKind(err, domain.ErrInvalid) {
		t.Errorf("wrong key: %v", err)
	}
	st, err := a.SetIntegration(ctx, p, domain.IntegrationBazarr, fake.URL, bazarrtest.Key)
	if err != nil || !st.Reachable || st.Version != "9.9.9" || st.ManagesMetadata {
		t.Fatalf("Bazarr linked: %+v %v", st, err)
	}
	if _, err := a.ConfigureIntegration(ctx, p, domain.IntegrationBazarr, domain.IntegrationSetup{KodiMetadata: true}); domain.CodeOf(err) != "integration.not_configurable" {
		t.Errorf("nothing to set up on Bazarr: %v", err)
	}
	if list, err := a.Integrations(ctx); err != nil || len(list) != 5 || list[4].Kind != domain.IntegrationBazarr || !list[4].Reachable {
		t.Errorf("integrations: %+v %v", list, err)
	}
	info, err := a.SubtitleSearch(ctx, p, file.ID)
	if err != nil || !info.Available || !slices.Equal(info.Languages, []domain.SubtitleLanguage{{Code: "en", Name: "English"}, {Code: "es", Name: "Spanish"}}) || len(info.Searches) != 0 {
		t.Fatalf("with Bazarr: %+v %v", info, err)
	}

	// What cannot be asked.
	for code, ask := range map[string]func() error{
		"subtitle.language_unavailable": func() error {
			_, err := a.SearchSubtitle(ctx, p, file.ID, domain.SubtitleWanted{Language: "de"})
			return err
		},
		"subtitle.file_not_found": func() error {
			_, err := a.SearchSubtitle(ctx, p, domain.NewID(), domain.SubtitleWanted{Language: "es"})
			return err
		},
	} {
		if err := ask(); domain.CodeOf(err) != code {
			t.Errorf("%s: %v", code, err)
		}
	}
	_, err = a.CreateAccount(ctx, p, NewAccount{Username: "Léa", Password: "a-password", Libraries: &domain.LibraryAccess{}})
	mustNil(t, err)
	_, lea := login(t, a, "Léa", "a-password")
	if _, err := a.SearchSubtitle(ctx, lea, file.ID, domain.SubtitleWanted{Language: "es"}); domain.CodeOf(err) != "subtitle.file_not_found" {
		t.Errorf("a file the profile does not see: %v", err)
	}

	// Found: the profile hears of it, and the file has one more subtitle.
	before, _, err := a.store.Read().SubtitleSet(ctx, file.ID)
	mustNil(t, err)
	sub := a.Subscribe(p)
	defer sub.Close()
	search, err := a.SearchSubtitle(ctx, p, file.ID, domain.SubtitleWanted{Language: " ES "})
	if err != nil || search.State != domain.SubtitleSearching || search.Language != "es" || search.FileID != file.ID {
		t.Fatalf("search: %+v %v", search, err)
	}
	searches := searchEnd(t, a, p, file.ID)
	if len(searches) != 1 || searches[0].State != domain.SubtitleFound || searches[0].Error != nil {
		t.Fatalf("after the search: %+v", searches)
	}
	heard := 0
	for heard < 2 {
		wait, cancel := context.WithTimeout(ctx, 5*time.Second)
		e, err := sub.Next(wait)
		cancel()
		if err != nil {
			t.Fatalf("%d events about the search: %v", heard, err)
		}
		if c, ok := e.(domain.SubtitleSearchChanged); ok && c.FileID == file.ID {
			heard++
		}
	}
	after, _, err := a.store.Read().SubtitleSet(ctx, file.ID)
	mustNil(t, err)
	if len(after.Subtitles) != len(before.Subtitles)+1 || !slices.ContainsFunc(after.Subtitles, func(s domain.Subtitle) bool {
		return s.External() && s.Language == "spa" && slices.Contains(s.Formats, "vtt")
	}) {
		t.Errorf("subtitles after the search: %+v", after.Subtitles)
	}

	// Nothing found: said so, and not asked again right away.
	wanted := domain.SubtitleWanted{Language: "en", HearingImpaired: true}
	_, err = a.SearchSubtitle(ctx, p, file.ID, wanted)
	mustNil(t, err)
	searches = searchEnd(t, a, p, file.ID)
	if len(searches) != 2 || searches[1].State != domain.SubtitleNotFound || !searches[1].HearingImpaired {
		t.Fatalf("nothing found: %+v", searches)
	}
	again, err := a.SearchSubtitle(ctx, p, file.ID, wanted)
	if err != nil || again.State != domain.SubtitleNotFound {
		t.Errorf("asked again: %+v %v", again, err)
	}
	var got []bazarrtest.Search
	fake.Set(func(s *bazarrtest.Server) { got = slices.Clone(s.Searches) })
	if len(got) != 2 || got[0] != (bazarrtest.Search{VideoID: 3, Wanted: bazarr.Wanted{Language: "es"}}) || !got[1].Wanted.HearingImpaired {
		t.Errorf("searches Bazarr received: %+v", got)
	}

	// A title Bazarr does not follow: the search fails and says why.
	fake.Set(func(s *bazarrtest.Server) { s.Movies = nil })
	_, err = a.SearchSubtitle(ctx, p, file.ID, domain.SubtitleWanted{Language: "en"})
	mustNil(t, err)
	searches = searchEnd(t, a, p, file.ID)
	if last := searches[len(searches)-1]; last.State != domain.SubtitleSearchFailed || last.Error == nil || last.Error.Key != "subtitle_search.not_followed" {
		t.Errorf("not followed: %+v", last)
	}

	// Unlinked: nothing to offer again.
	mustNil(t, a.DeleteIntegration(ctx, p, domain.IntegrationBazarr))
	if info, err := a.SubtitleSearch(ctx, p, file.ID); err != nil || info.Available {
		t.Errorf("after unlinking: %+v %v", info, err)
	}
}

// An episode is found on Bazarr by the TVDB ID of its series, then by its numbers; without an ID,
// by the name of the series folder.
func TestBazarrVideo(t *testing.T) {
	a, _, admin, _, _, shows, movies := requestApp(t)
	ctx := context.Background()
	series := putInCatalog(t, a, shows, "Frieren", "tvdb", 101, 2)
	movie := putInCatalog(t, a, movies, "Suzume", "imdb", 0, 0)
	eps, err := a.Episodes(ctx, admin, series, nil)
	if err != nil || len(eps) != 2 {
		t.Fatalf("episodes: %d %v", len(eps), err)
	}
	fileOf := func(item domain.ID) domain.MediaFile {
		files, err := a.store.Read().ItemFiles(ctx, item)
		mustNil(t, err)
		return files[0].File
	}
	fake := bazarrtest.New(t)
	fake.Set(func(s *bazarrtest.Server) {
		s.Series = []bazarr.Series{
			{ID: 6, TvdbID: 999, Title: "Other", Path: "/tv/Other"},
			{ID: 7, TvdbID: 101, Title: "Frieren", Path: "/tv/Sousou no Frieren"},
		}
		s.Episodes = []*bazarr.Video{
			{ID: 61, SeriesID: 6, Season: 1, Episode: 2, Path: "/tv/Other/Other S01E02.mkv"},
			{ID: 71, SeriesID: 7, Season: 1, Episode: 1, Path: "/tv/Sousou no Frieren/S01E01.mkv"},
			{ID: 72, SeriesID: 7, Season: 1, Episode: 2, Path: "/tv/Sousou no Frieren/S01E02.mkv"},
		}
		s.Movies = []*bazarr.Video{
			{ID: 3, ImdbID: "tt0000001", Path: "/movies/Other (2020)/Other (2020).mkv"},
			{ID: 4, Path: `D:\movies\Suzume (2022)\` + filepath.Base(fileOf(movie).Path)},
		}
	})
	c := bazarr.New(fake.URL, bazarrtest.Key, nil)

	v, ok, err := a.bazarrVideo(ctx, c, fileOf(eps[1].Item.ID), eps[1].Item.ID)
	if err != nil || !ok || v.ID != 72 || v.SeriesID != 7 {
		t.Errorf("episode by TVDB ID and numbers: %+v %v %v", v, ok, err)
	}
	v, ok, err = a.bazarrVideo(ctx, c, fileOf(movie), movie)
	if err != nil || !ok || v.ID != 4 || v.SeriesID != 0 {
		t.Errorf("movie by file name: %+v %v %v", v, ok, err)
	}
	// Bazarr knows the series under another ID: nothing to go by.
	fake.Set(func(s *bazarrtest.Server) { s.Series[1].TvdbID = 555 })
	if v, ok, err := a.bazarrVideo(ctx, c, fileOf(eps[0].Item.ID), eps[0].Item.ID); err != nil || ok {
		t.Errorf("unknown series: %+v %v %v", v, ok, err)
	}
	// The folder of the series has the same name on both sides.
	folder := filepath.Base(shows.Paths[0])
	fake.Set(func(s *bazarrtest.Server) { s.Series[1].Path = "/tv/" + strings.ToUpper(folder) })
	if v, ok, err := a.bazarrVideo(ctx, c, fileOf(eps[0].Item.ID), eps[0].Item.ID); err != nil || !ok || v.ID != 71 {
		t.Errorf("series by folder name: %+v %v %v", v, ok, err)
	}
}
