// Command perfcheck measures the server's performance budgets on a synthetic catalog of about
// 10,000 items: startup time (until /health answers), idle memory (before and after calls), latency
// of lists through the API (p50, p95, p99).
//
//	go run ./devtools/perfcheck [-bin laterna.exe] [-n 200] [-keep]
//
// The catalog is written straight into the database (movies, series, seasons, episodes, artists,
// albums, tracks, with genres, credits, analyzed images, user data). Its files do not exist: the
// library roots are empty, which the scan takes for an unplugged disk and so marks nothing as
// missing.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/auth"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// Catalog size: 3,000 movies, 150 series of 4 seasons of 10 episodes, 20 artists with 3 albums of
// 10 tracks; 10,430 items.
const (
	movies          = 3000
	series          = 150
	seasonsPer      = 4
	episodesPer     = 10
	artists         = 20
	albumsPer       = 3
	tracksPer       = 10
	perfUser        = "perf"
	perfPassword    = "a-password-perf"
	playedMovies    = 400
	resumedMovies   = 30
	watchedEpisodes = 3 // first episodes played in each series
)

var genres = []string{"Action", "Adventure", "Animation", "Comedy", "Drama", "Fantasy", "Horror", "Romance", "Science Fiction", "Thriller"}

func main() {
	bin := flag.String("bin", "", "server binary (empty: build it)")
	n := flag.Int("n", 200, "calls measured per list")
	keep := flag.Bool("keep", false, "keep the data folder")
	rest := flag.Duration("rest", 70*time.Second, "wait before measuring idle memory")
	flag.Parse()
	log.SetFlags(0)
	if err := run(context.Background(), *bin, *n, *keep, *rest); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, bin string, n int, keep bool, rest time.Duration) error {
	dir, err := os.MkdirTemp("", "laterna-perf-")
	if err != nil {
		return err
	}
	if !keep {
		defer func() { _ = os.RemoveAll(dir) }()
	}
	if bin == "" {
		bin = filepath.Join(dir, "laterna"+exeSuffix())
		build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", bin, "./cmd/laterna") //nolint:gosec // path chosen here
		build.Stdout, build.Stderr = os.Stderr, os.Stderr
		if err := build.Run(); err != nil {
			return fmt.Errorf("build: %w", err)
		}
	}

	start := time.Now()
	ids, err := populate(ctx, dir)
	if err != nil {
		return fmt.Errorf("catalog: %w", err)
	}
	log.Printf("synthetic catalog written in %s", time.Since(start).Round(time.Millisecond))

	addr, err := freeAddr(ctx)
	if err != nil {
		return err
	}
	logFile, err := os.Create(filepath.Join(dir, "serve.log"))
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, "serve")
	cmd.Env = append(os.Environ(), "LATERNA_DATA_DIR="+filepath.Join(dir, "data"), "LATERNA_CACHE_DIR="+filepath.Join(dir, "cache"),
		"LATERNA_METADATA_DIR="+filepath.Join(dir, "meta"), "LATERNA_ADDRESS="+addr, "LATERNA_LOG_LEVEL=warn")
	cmd.Stdout, cmd.Stderr = logFile, logFile
	base := "http://" + addr
	started := time.Now()
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
		if err != nil {
			return err
		}
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Since(started) > 30*time.Second {
			return errors.New("the server does not answer")
		}
		time.Sleep(2 * time.Millisecond)
	}
	startup := time.Since(started)

	c := http.DefaultClient
	authC := laternav1connect.NewAuthServiceClient(c, base)
	login, err := authC.Login(ctx, connect.NewRequest(&laternav1.LoginRequest{
		Username: perfUser, Password: perfPassword, Device: &laternav1.Device{Name: "perfcheck", Client: "perfcheck", ClientVersion: "1", Platform: runtime.GOOS},
	}))
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	token := login.Msg.GetToken()
	system := laternav1connect.NewSystemServiceClient(c, base)
	// Idle: no job left (startup scans, encoder detection), then some quiet time (-rest).
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		st, err := system.GetSystemStatus(ctx, withToken(&laternav1.GetSystemStatusRequest{}, token))
		if err == nil && st.Msg.GetStatus().GetJobsPending() == 0 && st.Msg.GetStatus().GetJobsRunning() == 0 {
			break
		}
	}
	time.Sleep(rest)
	idle := memory(cmd.Process.Pid)

	catalog := laternav1connect.NewCatalogServiceClient(c, base)
	home := laternav1connect.NewHomeServiceClient(c, base)
	music := laternav1connect.NewMusicServiceClient(c, base)
	history := laternav1connect.NewHistoryServiceClient(c, base)
	page, err := catalog.ListMovies(ctx, withToken(&laternav1.ListMoviesRequest{PageSize: 50}, token))
	if err != nil {
		return err
	}
	token2 := page.Msg.GetNextPageToken()
	calls := []struct {
		name string
		call func() error
	}{
		{"ListMovies (title, 50)", func() error {
			_, err := catalog.ListMovies(ctx, withToken(&laternav1.ListMoviesRequest{PageSize: 50}, token))
			return err
		}},
		{"ListMovies (next page)", func() error {
			_, err := catalog.ListMovies(ctx, withToken(&laternav1.ListMoviesRequest{PageSize: 50, PageToken: token2}, token))
			return err
		}},
		{"ListMovies (added, 50)", func() error {
			_, err := catalog.ListMovies(ctx, withToken(&laternav1.ListMoviesRequest{PageSize: 50, Sort: laternav1.ItemSort_ITEM_SORT_ADDED}, token))
			return err
		}},
		{"ListMovies (genre, unplayed)", func() error {
			f := false
			_, err := catalog.ListMovies(ctx, withToken(&laternav1.ListMoviesRequest{PageSize: 50, Genre: "Drama", Played: &f}, token))
			return err
		}},
		{"ListSeries (title, 50)", func() error {
			_, err := catalog.ListSeries(ctx, withToken(&laternav1.ListSeriesRequest{PageSize: 50}, token))
			return err
		}},
		{"GetSeries", func() error {
			_, err := catalog.GetSeries(ctx, withToken(&laternav1.GetSeriesRequest{SeriesId: ids.series.String()}, token))
			return err
		}},
		{"ListEpisodes (series)", func() error {
			_, err := catalog.ListEpisodes(ctx, withToken(&laternav1.ListEpisodesRequest{SeriesId: ids.series.String()}, token))
			return err
		}},
		{"GetMovie", func() error {
			_, err := catalog.GetMovie(ctx, withToken(&laternav1.GetMovieRequest{MovieId: ids.movie.String()}, token))
			return err
		}},
		{"Search (\"the\")", func() error {
			_, err := catalog.Search(ctx, withToken(&laternav1.SearchRequest{Query: "the"}, token))
			return err
		}},
		{"GetHome", func() error {
			_, err := home.GetHome(ctx, withToken(&laternav1.GetHomeRequest{}, token))
			return err
		}},
		{"ListAlbums (50)", func() error {
			_, err := music.ListAlbums(ctx, withToken(&laternav1.ListAlbumsRequest{PageSize: 50}, token))
			return err
		}},
		{"ListSimilar (movie)", func() error {
			_, err := catalog.ListSimilar(ctx, withToken(&laternav1.ListSimilarRequest{ItemId: ids.movie.String()}, token))
			return err
		}},
		{"ListHistory (50)", func() error {
			_, err := history.ListHistory(ctx, withToken(&laternav1.ListHistoryRequest{}, token))
			return err
		}},
		{"GetStats (year)", func() error {
			_, err := history.GetStats(ctx, withToken(&laternav1.GetStatsRequest{Year: int32(time.Now().Year()), TimeZone: "Europe/Paris"}, token)) //nolint:gosec // a year
			return err
		}},
		{"GetAlbum", func() error {
			_, err := music.GetAlbum(ctx, withToken(&laternav1.GetAlbumRequest{AlbumId: ids.album.String()}, token))
			return err
		}},
	}
	fmt.Printf("\nStartup (until /health answers): %s\n", startup.Round(time.Millisecond))
	fmt.Printf("Idle memory: %s\n\n", idle)
	fmt.Printf("%-30s %8s %8s %8s %8s\n", "Call", "p50", "p95", "p99", "max")
	worst := time.Duration(0)
	for _, cl := range calls {
		for range 20 { // warm-up (SQLite caches, prepared statements)
			if err := cl.call(); err != nil {
				return fmt.Errorf("%s: %w", cl.name, err)
			}
		}
		durs := make([]time.Duration, n)
		for i := range n {
			t0 := time.Now()
			if err := cl.call(); err != nil {
				return fmt.Errorf("%s: %w", cl.name, err)
			}
			durs[i] = time.Since(t0)
		}
		slices.Sort(durs)
		q := func(p float64) time.Duration { return durs[min(len(durs)-1, int(p*float64(len(durs))))] }
		fmt.Printf("%-30s %8s %8s %8s %8s\n", cl.name, ms(q(0.5)), ms(q(0.95)), ms(q(0.99)), ms(durs[len(durs)-1]))
		worst = max(worst, q(0.95))
	}
	after := memory(cmd.Process.Pid)
	time.Sleep(rest)
	calm := memory(cmd.Process.Pid)
	fmt.Printf("\nWorst p95: %s\n", ms(worst))
	fmt.Printf("Memory right after the calls: %s\n", after)
	fmt.Printf("Idle memory after use (%s): %s\n", rest, calm)
	if keep {
		fmt.Printf("\nData kept in %s\n", dir)
	}
	return nil
}

func ms(d time.Duration) string {
	return strconv.FormatFloat(float64(d.Microseconds())/1000, 'f', 1, 64) + " ms"
}

func withToken[T any](msg *T, token string) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Authorization", "Bearer "+token)
	return req
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func freeAddr(ctx context.Context) (string, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String(), nil
}

// memory returns the memory of the process: resident memory (working set on Windows) and private
// memory.
func memory(pid int) string {
	if runtime.GOOS == "windows" {
		out, err := exec.Command("powershell", "-NoProfile", "-Command", //nolint:gosec,noctx // one-off measurement, integer pid
			fmt.Sprintf("$p = Get-Process -Id %d; \"$($p.WorkingSet64) $($p.PrivateMemorySize64)\"", pid)).Output()
		if err != nil {
			return "inconnue (" + err.Error() + ")"
		}
		f := strings.Fields(string(out))
		if len(f) != 2 {
			return "inconnue"
		}
		ws, _ := strconv.ParseInt(f[0], 10, 64)
		priv, _ := strconv.ParseInt(f[1], 10, 64)
		return fmt.Sprintf("%.1f MB resident, %.1f MB private", float64(ws)/1e6, float64(priv)/1e6)
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return "inconnue (" + err.Error() + ")"
	}
	var rss, anon string
	for line := range bytes.Lines(raw) {
		s := string(line)
		if v, ok := strings.CutPrefix(s, "VmRSS:"); ok {
			rss = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(s, "RssAnon:"); ok {
			anon = strings.TrimSpace(v)
		}
	}
	return rss + " resident, of which " + anon + " anonymous"
}

type sampleIDs struct {
	movie, series, album domain.ID
}

// populate writes the synthetic catalog into dir/data.
func populate(ctx context.Context, dir string) (sampleIDs, error) {
	var ids sampleIDs
	data := filepath.Join(dir, "data")
	if err := os.MkdirAll(data, 0o750); err != nil {
		return ids, err
	}
	st, err := store.Open(ctx, filepath.Join(data, store.FileName))
	if err != nil {
		return ids, err
	}
	defer func() { _ = st.Close() }()
	now := time.Now().Add(-24 * time.Hour)
	hash, err := auth.HashPassword(perfPassword)
	if err != nil {
		return ids, err
	}
	account := domain.Account{ID: domain.NewID(), Username: perfUser, IsAdmin: true, Libraries: domain.AllLibraries(), CreatedAt: now, UpdatedAt: now}
	profile := domain.Profile{ID: domain.NewID(), AccountID: account.ID, Name: perfUser, CreatedAt: now, UpdatedAt: now}
	libs := map[domain.LibraryKind]domain.Library{}
	for _, k := range []domain.LibraryKind{domain.LibraryMovies, domain.LibraryShows, domain.LibraryMusic} {
		root := filepath.Join(dir, "medias", string(k))
		if err := os.MkdirAll(root, 0o750); err != nil {
			return ids, err
		}
		libs[k] = domain.Library{ID: domain.NewID(), Name: string(k), Kind: k, Paths: []string{root}, Language: "en-US", CreatedAt: now, UpdatedAt: now}
	}
	err = st.Write(ctx, func(q store.Q) error {
		if err := q.CreateAccount(ctx, account, hash); err != nil {
			return err
		}
		if err := q.CreateProfile(ctx, profile, ""); err != nil {
			return err
		}
		for _, l := range libs {
			if err := q.CreateLibrary(ctx, l); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ids, err
	}
	video := domain.MediaInfo{Container: "mkv", Duration: 95 * time.Minute, Bitrate: 8_000_000, Streams: []domain.Stream{
		{Index: 0, Kind: domain.StreamVideo, Codec: "hevc", Profile: "Main 10", Width: 1920, Height: 1080, BitDepth: 10, FrameRate: 23.976, DynamicRange: domain.SDR},
		{Index: 1, Kind: domain.StreamAudio, Codec: "eac3", Language: "fre", Channels: 6, ChannelLayout: "5.1", SampleRate: 48000, Default: true},
		{Index: 2, Kind: domain.StreamAudio, Codec: "aac", Language: "jpn", Channels: 2, ChannelLayout: "stereo", SampleRate: 48000},
		{Index: 3, Kind: domain.StreamSubtitle, Codec: "subrip", Language: "fre"},
	}}
	audio := domain.MediaInfo{Container: "flac", Duration: 4 * time.Minute, Streams: []domain.Stream{
		{Index: 0, Kind: domain.StreamAudio, Codec: "flac", Channels: 2, SampleRate: 44100},
	}}
	overview := strings.Repeat("An overview of ordinary length, like the ones NFO files carry. ", 6)
	seq := 0
	file := func(q store.Q, lib domain.Library, info domain.MediaInfo) (domain.ID, error) {
		seq++
		f := domain.MediaFile{
			ID: domain.NewID(), LibraryID: lib.ID, Path: filepath.Join(lib.Paths[0], fmt.Sprintf("f%06d.%s", seq, info.Container)),
			Size: 2 << 30, ModTime: now, Fingerprint: fmt.Sprintf("%064x", seq),
		}
		if err := q.CreateFile(ctx, f, now); err != nil {
			return f.ID, err
		}
		return f.ID, q.SetFileAnalysis(ctx, f.ID, info, now)
	}
	item := func(q store.Q, it domain.Item, meta domain.Metadata, poster bool) (domain.Item, error) {
		it.ID, it.SortTitle, it.AddedAt, it.UpdatedAt = domain.NewID(), strings.ToLower(it.Title), now.Add(time.Duration(seq)*time.Second), now
		if err := q.CreateItem(ctx, it); err != nil {
			return it, err
		}
		meta.Title, meta.SortTitle = it.Title, it.SortTitle
		if _, err := q.SetMetadata(ctx, it.ID, meta, now); err != nil {
			return it, err
		}
		if poster {
			img, _, err := q.SetItemImage(ctx, it.ID, domain.ImagePoster, domain.ImageLocal, filepath.Join(dir, "poster.jpg"), "", now)
			if err != nil {
				return it, err
			}
			if err := q.SetImageAnalysis(ctx, img.ID, 1000, 1500, "LKO2?U%2Tw=w]~RBVZRi};RPxuwH", fmt.Sprintf("%016x", seq), now); err != nil {
				return it, err
			}
		}
		return it, nil
	}
	credits := func(i int) []domain.Credit {
		var out []domain.Credit
		for j := range 8 {
			out = append(out, domain.Credit{Name: fmt.Sprintf("Actor %d", (i*7+j)%900), Role: domain.RoleActor, Character: "Role", Order: j})
		}
		return append(out, domain.Credit{Name: fmt.Sprintf("Director %d", i%300), Role: domain.RoleDirector})
	}
	// In batches of 500: one transaction per batch.
	batch := func(n int, fn func(q store.Q, i int) error) error {
		for startAt := 0; startAt < n; startAt += 500 {
			if err := st.Write(ctx, func(q store.Q) error {
				for i := startAt; i < min(n, startAt+500); i++ {
					if err := fn(q, i); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	}
	movieIDs := make([]domain.ID, 0, movies)
	err = batch(movies, func(q store.Q, i int) error {
		fid, err := file(q, libs[domain.LibraryMovies], video)
		if err != nil {
			return err
		}
		m, err := item(q, domain.Item{
			LibraryID: libs[domain.LibraryMovies].ID, Kind: domain.ItemMovie, GroupKey: fmt.Sprintf("movie:%d", i),
			Title: fmt.Sprintf("The Movie %04d", i), Year: 1970 + i%55,
		}, domain.Metadata{
			Year: 1970 + i%55, PremiereDate: fmt.Sprintf("%04d-%02d-15", 1970+i%55, 1+i%12), Overview: overview, OfficialRating: "PG-13",
			CommunityRating: float64(i%90) / 10, Runtime: 95 * time.Minute, Genres: []string{genres[i%len(genres)], genres[(i+3)%len(genres)]},
			Studios: []string{"Studio"}, ProviderIDs: map[string]string{"tmdb": strconv.Itoa(1000 + i)}, Credits: credits(i),
		}, true)
		if err != nil {
			return err
		}
		movieIDs = append(movieIDs, m.ID)
		return q.LinkFile(ctx, m.ID, fid, "", 0)
	})
	if err != nil {
		return ids, err
	}
	ids.movie = movieIDs[len(movieIDs)/2]
	var watched, watchedSeries []domain.ID
	err = batch(series, func(q store.Q, i int) error {
		lib := libs[domain.LibraryShows]
		s, err := item(q, domain.Item{
			LibraryID: lib.ID, Kind: domain.ItemSeries, GroupKey: fmt.Sprintf("series:%d", i),
			Title: fmt.Sprintf("The Series %03d", i), Year: 2000 + i%25,
		}, domain.Metadata{
			Year: 2000 + i%25, Overview: overview, Genres: []string{genres[i%len(genres)]}, Credits: credits(i),
		}, true)
		if err != nil {
			return err
		}
		if i == series/2 {
			ids.series = s.ID
		}
		for sn := 1; sn <= seasonsPer; sn++ {
			season, err := item(q, domain.Item{
				LibraryID: lib.ID, Kind: domain.ItemSeason, ParentID: &s.ID,
				GroupKey: fmt.Sprintf("season:%s:%d", s.ID, sn), Title: domain.SeasonTitle(sn),
			}, domain.Metadata{}, true)
			if err != nil {
				return err
			}
			if err := q.CreateSeason(ctx, domain.Season{ItemID: season.ID, SeriesID: s.ID, Number: sn}); err != nil {
				return err
			}
			for en := 1; en <= episodesPer; en++ {
				fid, err := file(q, lib, video)
				if err != nil {
					return err
				}
				ep, err := item(q, domain.Item{
					LibraryID: lib.ID, Kind: domain.ItemEpisode, ParentID: &season.ID,
					GroupKey: fmt.Sprintf("episode:%s:%d:%d", s.ID, sn, en), Title: domain.EpisodeTitle(en),
				}, domain.Metadata{
					Overview: overview, Runtime: 24 * time.Minute, PremiereDate: fmt.Sprintf("2020-%02d-%02d", sn, en),
				}, false)
				if err != nil {
					return err
				}
				if err := q.CreateEpisode(ctx, domain.Episode{ItemID: ep.ID, SeriesID: s.ID, SeasonID: season.ID, SeasonNumber: sn, Number: en}); err != nil {
					return err
				}
				if err := q.LinkFile(ctx, ep.ID, fid, "", 0); err != nil {
					return err
				}
				if sn == 1 && en <= watchedEpisodes {
					watched = append(watched, ep.ID)
					watchedSeries = append(watchedSeries, s.ID)
				}
			}
		}
		return nil
	})
	if err != nil {
		return ids, err
	}
	err = batch(artists, func(q store.Q, i int) error {
		lib := libs[domain.LibraryMusic]
		ar, err := item(q, domain.Item{
			LibraryID: lib.ID, Kind: domain.ItemArtist, GroupKey: fmt.Sprintf("artist:%d", i),
			Title: fmt.Sprintf("Artist %02d", i),
		}, domain.Metadata{}, true)
		if err != nil {
			return err
		}
		for an := range albumsPer {
			al, err := item(q, domain.Item{
				LibraryID: lib.ID, Kind: domain.ItemAlbum, ParentID: &ar.ID,
				GroupKey: fmt.Sprintf("album:%s:%d", ar.ID, an), Title: fmt.Sprintf("Album %d", an), Year: 1990 + an,
			}, domain.Metadata{
				Year: 1990 + an, Genres: []string{"Electronic"},
			}, true)
			if err != nil {
				return err
			}
			if i == 0 && an == 0 {
				ids.album = al.ID
			}
			for tn := 1; tn <= tracksPer; tn++ {
				fid, err := file(q, lib, audio)
				if err != nil {
					return err
				}
				tr, err := item(q, domain.Item{
					LibraryID: lib.ID, Kind: domain.ItemTrack, ParentID: &al.ID,
					GroupKey: fmt.Sprintf("track:%s:1:%d", al.ID, tn), Title: fmt.Sprintf("Track %d", tn),
				}, domain.Metadata{Runtime: 4 * time.Minute}, false)
				if err != nil {
					return err
				}
				if err := q.SetTrack(ctx, domain.Track{ItemID: tr.ID, AlbumID: al.ID, ArtistID: ar.ID, Disc: 1, Number: tn, Artists: ar.Title}); err != nil {
					return err
				}
				if err := q.LinkFile(ctx, tr.ID, fid, "FLAC", 0); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return ids, err
	}
	// User data: movies played, movies started, first episodes of series; one history play per
	// movie and episode played, spread over the past year.
	err = st.Write(ctx, func(q store.Q) error {
		if err := q.SetPlayed(ctx, profile.ID, movieIDs[:playedMovies], true, now); err != nil {
			return err
		}
		n := 0
		addPlay := func(item domain.ID, kind domain.ItemKind, series *domain.ID, length time.Duration) error {
			n++
			start := now.Add(-time.Duration(n) * 9 * time.Hour)
			pl := domain.Play{
				ID: domain.NewID(), ProfileID: profile.ID, ItemID: &item, Kind: kind, SeriesID: series, Title: "Title",
				StartedAt: start, EndedAt: start.Add(length), Watched: length, Position: length, Duration: length, Completed: true, Device: "TV",
			}
			if series != nil {
				pl.Subtitle = "Series"
			}
			return q.AddPlay(ctx, pl)
		}
		for _, id := range movieIDs[:playedMovies] {
			if err := addPlay(id, domain.ItemMovie, nil, 95*time.Minute); err != nil {
				return err
			}
		}
		for i, id := range watched {
			if err := addPlay(id, domain.ItemEpisode, &watchedSeries[i], 24*time.Minute); err != nil {
				return err
			}
		}
		if err := q.SetPlayed(ctx, profile.ID, watched, true, now); err != nil {
			return err
		}
		for i, id := range movieIDs[playedMovies : playedMovies+resumedMovies] {
			if err := q.SaveProgress(ctx, profile.ID, id, time.Duration(10+i)*time.Minute, false, now.Add(time.Duration(i)*time.Minute)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ids, err
	}
	if ids.series.IsZero() || ids.album.IsZero() {
		return ids, errors.New("samples not found")
	}
	return ids, nil
}
