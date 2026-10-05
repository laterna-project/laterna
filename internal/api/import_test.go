package api

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha512"
	"encoding/hex"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/jellyfin/jellyfintest"
	"github.com/laterna-project/laterna/internal/store"
	"github.com/laterna-project/laterna/internal/testfixtures"
)

// jellyfinHash returns the hash of a password in Jellyfin's format.
func jellyfinHash(t *testing.T, password string) string {
	t.Helper()
	salt := []byte("sel-de-jellyfin-0123456789abcdef")
	key, err := pbkdf2.Key(sha512.New, password, salt, 1000, 64)
	if err != nil {
		t.Fatal(err)
	}
	return "$PBKDF2-SHA512$iterations=1000$" + strings.ToUpper(hex.EncodeToString(salt)) + "$" + strings.ToUpper(hex.EncodeToString(key))
}

// Jellyfin import end to end: a Jellyfin server whose files are the fixtures, seen under /media as
// in its container. Preview, then import: one user joins the account of the same name, another
// becomes an account (Jellyfin password, library and parental control carried over), a third
// becomes a profile of an existing account. Played state, positions and favorites are matched by
// file path, and sessions from the log go into the history. A second import changes nothing.
func TestJellyfinImport(t *testing.T) {
	testfixtures.Library(t)
	ffmpeg, ffprobe, _ := testfixtures.FFmpeg()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(ctx, st, app.Options{ServerName: "Test", FFmpeg: ffmpeg, FFprobe: ffprobe, NoAutoScans: true, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	if err := a.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(a, Options{}))
	t.Cleanup(func() { srv.Close(); cancel(); a.Wait(); _ = st.Close() })
	dev := domain.Device{Name: "Test", Client: "Test", ClientVersion: "1", Platform: "Go"}
	admin, err := a.Setup(ctx, "Chloé", "a-strong-password", dev, "")
	if err != nil {
		t.Fatal(err)
	}
	films, err := a.CreateLibrary(ctx, "Movies", domain.LibraryMovies, []string{filepath.Join(testfixtures.Root(), "Movies")}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateLibrary(ctx, "Shows", domain.LibraryShows, []string{filepath.Join(testfixtures.Root(), "Shows")}, ""); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(120 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if counts, err := st.Read().CountJobs(ctx); err != nil || len(counts) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job queue never empty")
		}
	}

	// The Jellyfin server.
	chloe, lea, zoe := jellyfintest.NewID(), jellyfintest.NewID(), jellyfintest.NewID()
	jfFilms, jfSeries := jellyfintest.NewID(), jellyfintest.NewID()
	big, absent, series, s1e1 := jellyfintest.NewID(), jellyfintest.NewID(), jellyfintest.NewID(), jellyfintest.NewID()
	twelve := 12
	t0 := time.Date(2026, 6, 1, 19, 0, 0, 123456700, time.UTC) // down to a tenth of a microsecond, like Jellyfin
	dir := t.TempDir()
	err = jellyfintest.Write(dir, jellyfintest.Server{
		Users: []jellyfintest.User{
			{ID: chloe, Name: "chloé", Password: jellyfinHash(t, "another-password"), Admin: true, AllLibraries: true},
			{ID: lea, Name: "Léa", Password: jellyfinHash(t, "leas-secret"), Libraries: []string{jfFilms}, MaxScore: &twelve},
			{ID: zoe, Name: "Zoé", AllLibraries: true},
		},
		Libraries: []jellyfintest.Library{{ID: jfFilms, Name: "Movies"}, {ID: jfSeries, Name: "Shows"}},
		Items: []jellyfintest.Item{
			{ID: big, Type: "Movie", Path: "/media/movies/Big Test Movie (2020)/Big Test Movie (2020).mp4", Name: "Big Test Movie", Runtime: 100 * time.Minute, Libraries: []string{jfFilms}},
			{ID: absent, Type: "Movie", Path: "/media/movies/Absent (1999)/Absent (1999).mkv", Name: "Absent", Runtime: 90 * time.Minute, Libraries: []string{jfFilms}},
			{ID: series, Type: "Series", Path: "/media/tv/Café Stories (2022)", Name: "Café Stories", Libraries: []string{jfSeries}},
			{
				ID: s1e1, Type: "Episode", Path: "/media/tv/Café Stories (2022)/Season 01/Café Stories (2022) S01E01.mkv", Name: "Pilot",
				SeriesName: "Café Stories", Series: series, Runtime: 24 * time.Minute, Libraries: []string{jfSeries},
			},
		},
		UserData: []jellyfintest.UserData{
			{User: chloe, Item: big, Key: "tt1", Played: true, PlayCount: 3, LastPlayed: t0},
			{User: chloe, Item: s1e1, Key: "e1", Position: 5 * time.Minute, LastPlayed: t0.Add(time.Hour)},
			{User: chloe, Item: series, Key: "s", Favorite: true},
			{User: chloe, Item: absent, Key: "tt2", Played: true, LastPlayed: t0},
			{User: lea, Item: big, Key: "tt1", Position: 20 * time.Minute, LastPlayed: t0},
		},
		Log: []jellyfintest.Event{
			{Type: "VideoPlayback", User: chloe, Item: big, At: t0.Add(-2 * time.Hour)},
			{Type: "VideoPlaybackStopped", User: chloe, Item: big, At: t0.Add(-15 * time.Minute)}, // longer than the movie: capped
			{Type: "VideoPlayback", User: chloe, Item: absent, At: t0.Add(-48 * time.Hour)},
			{Type: "VideoPlaybackStopped", User: chloe, Item: absent, At: t0.Add(-47 * time.Hour)},
			{Type: "VideoPlayback", User: chloe, Item: s1e1, At: t0.Add(time.Hour)},
			{Type: "VideoPlaybackStopped", User: chloe, Item: s1e1, At: t0.Add(time.Hour + 30*time.Second)}, // too short
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	imports := laternav1connect.NewImportServiceClient(srv.Client(), srv.URL)
	token := admin.Token
	if _, err := imports.PreviewJellyfinImport(ctx, authed(&laternav1.PreviewJellyfinImportRequest{Path: filepath.Join(dir, "nowhere")}, token)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("missing folder: %v", err)
	}
	preview, err := imports.PreviewJellyfinImport(ctx, authed(&laternav1.PreviewJellyfinImportRequest{Path: dir}, token))
	if err != nil {
		t.Fatal(err)
	}
	p := preview.Msg
	users := map[string]*laternav1.JellyfinUser{}
	for _, u := range p.GetUsers() {
		users[u.GetName()] = u
	}
	if u := users["chloé"]; u.GetTarget().GetKind() != laternav1.JellyfinTargetKind_JELLYFIN_TARGET_KIND_PROFILE || u.GetProfileName() != "Chloé" ||
		u.GetUserData() != 4 || u.GetUserDataMatched() != 3 || u.GetSessions() != 2 || u.GetSessionsMatched() != 1 {
		t.Errorf("chloé: %v", u)
	}
	if u := users["Léa"]; u.GetTarget().GetKind() != laternav1.JellyfinTargetKind_JELLYFIN_TARGET_KIND_NEW_ACCOUNT || !u.GetHasPassword() {
		t.Errorf("Léa: %v", u)
	}
	if u := users["Zoé"]; u.GetTarget().GetKind() != laternav1.JellyfinTargetKind_JELLYFIN_TARGET_KIND_NEW_ACCOUNT || u.GetHasPassword() {
		t.Errorf("Zoé: %v", u)
	}
	if p.GetUnmatchedFiles() != 1 || len(p.GetUnmatchedSamples()) != 1 || !strings.Contains(p.GetUnmatchedSamples()[0], "Absent") {
		t.Errorf("missing files: %d %v", p.GetUnmatchedFiles(), p.GetUnmatchedSamples())
	}

	// Import: Zoé becomes a profile of Chloé's account.
	run := func() map[string]*laternav1.JellyfinImport {
		t.Helper()
		resp, err := imports.ImportJellyfin(ctx, authed(&laternav1.ImportJellyfinRequest{Path: dir, Targets: map[string]*laternav1.JellyfinTarget{
			users["Zoé"].GetId(): {Kind: laternav1.JellyfinTargetKind_JELLYFIN_TARGET_KIND_NEW_PROFILE, AccountId: admin.Session.Account.ID.String()},
		}}, token))
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]*laternav1.JellyfinImport{}
		for _, u := range resp.Msg.GetUsers() {
			out[u.GetName()] = u
		}
		return out
	}
	done := run()
	if d := done["chloé"]; d.GetAccountCreated() || d.GetUserData() != 3 || d.GetHistory() != 2 {
		t.Errorf("chloé imported: %v", d)
	}
	if d := done["Léa"]; !d.GetAccountCreated() || d.GetUserData() != 1 {
		t.Errorf("Léa imported: %v", d)
	}
	if d := done["Zoé"]; d.GetAccountCreated() || !d.GetProfileCreated() || d.GetProfileName() != "Zoé" {
		t.Errorf("Zoé imported: %v", d)
	}

	// Chloé's data: a movie played three times, an episode to resume, a series as favorite.
	catalog := laternav1connect.NewCatalogServiceClient(srv.Client(), srv.URL)
	movies, err := catalog.ListMovies(ctx, authed(&laternav1.ListMoviesRequest{}, token))
	if err != nil {
		t.Fatal(err)
	}
	idx := slices.IndexFunc(movies.Msg.GetMovies(), func(m *laternav1.MovieSummary) bool { return m.GetTitle() == "Big Test Movie" })
	if idx < 0 {
		t.Fatalf("movies: %v", movies.Msg.GetMovies())
	}
	if ud := movies.Msg.GetMovies()[idx].GetUserData(); !ud.GetPlayed() || ud.GetPlayCount() != 3 {
		t.Errorf("movie: %v", ud)
	}
	shows, err := catalog.ListSeries(ctx, authed(&laternav1.ListSeriesRequest{}, token))
	if err != nil || len(shows.Msg.GetSeries()) != 1 || !shows.Msg.GetSeries()[0].GetUserData().GetFavorite() {
		t.Fatalf("series: %v %v", shows, err)
	}
	episodes, err := catalog.ListEpisodes(ctx, authed(&laternav1.ListEpisodesRequest{SeriesId: shows.Msg.GetSeries()[0].GetId()}, token))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range episodes.Msg.GetEpisodes() {
		want := time.Duration(0)
		if e.GetSeasonNumber() == 1 && e.GetNumber() == 1 {
			want = 5 * time.Minute
		}
		if got := e.GetUserData().GetPosition().AsDuration(); got != want {
			t.Errorf("S%02dE%02d: position %v, want %v", e.GetSeasonNumber(), e.GetNumber(), got, want)
		}
	}

	// History: the movie (capped at its runtime) and the movie Laterna does not have (title only).
	history := laternav1connect.NewHistoryServiceClient(srv.Client(), srv.URL)
	hist, err := history.ListHistory(ctx, authed(&laternav1.ListHistoryRequest{}, token))
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, e := range hist.Msg.GetEntries() {
		titles = append(titles, e.GetTitle())
		if e.GetTitle() == "Big Test Movie" && (e.GetWatched().AsDuration() != 100*time.Minute || e.GetDevice() != "Jellyfin" || !e.GetCompleted()) {
			t.Errorf("play of the movie: %v", e)
		}
	}
	if !slices.Equal(titles, []string{"Big Test Movie", "Absent"}) {
		t.Errorf("history: %v", titles)
	}

	// Léa: her Jellyfin password still works, then becomes an argon2id hash; movies only, age 12 at
	// most.
	auth := laternav1connect.NewAuthServiceClient(srv.Client(), srv.URL)
	if _, err := auth.Login(ctx, connect.NewRequest(&laternav1.LoginRequest{Username: "Léa", Password: "leas-secret", Device: &laternav1.Device{Name: "TV", Client: "Test", ClientVersion: "1", Platform: "Go"}})); err != nil {
		t.Fatalf("Léa's login: %v", err)
	}
	leaID, err := domain.ParseID(done["Léa"].GetAccountId())
	if err != nil {
		t.Fatal(err)
	}
	if hash, err := st.Read().PasswordHash(ctx, leaID); err != nil || !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("Léa's hash after login: %.12s %v", hash, err)
	}
	account, err := st.Read().Account(ctx, leaID)
	if err != nil || account.Libraries.All || !slices.Equal(account.Libraries.IDs, []domain.ID{films.ID}) ||
		account.Parental.MaxAge == nil || *account.Parental.MaxAge != 12 || account.IsAdmin {
		t.Errorf("Léa's account: %+v %v", account, err)
	}

	// A second import changes nothing.
	again := run()
	for name, d := range again {
		if d.GetAccountCreated() || d.GetProfileCreated() || d.GetUserData() != 0 || d.GetHistory() != 0 {
			t.Errorf("second import, %s: %v", name, d)
		}
	}
}
