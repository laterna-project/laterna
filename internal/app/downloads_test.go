package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/playback"
)

// Offline downloads for a phone that only plays H.264/AAC in MP4.
func TestDownloads(t *testing.T) {
	a, c := startMediaApp(t)
	_, p := setupAdmin(t, a)
	ctx := context.Background()
	for _, l := range []struct {
		name string
		kind domain.LibraryKind
	}{{"Films", domain.LibraryMovies}, {"Musique", domain.LibraryMusic}} {
		_, err := a.CreateLibrary(ctx, l.name, l.kind, []string{testRoot(l.name)}, "")
		mustNil(t, err)
	}
	waitIdle(t, a)
	sub := a.Subscribe(p)
	defer sub.Close()
	movies, err := a.ListMovies(ctx, p, ListQuery{PageSize: 50})
	mustNil(t, err)
	byTitle := map[string]domain.ID{}
	for _, m := range movies.Items {
		byTitle[m.Item.Title] = m.Item.ID
	}
	phone := playback.DeviceProfile{
		Containers: []string{"mp4", "m4a"}, Video: []playback.VideoSupport{{Codec: "h264"}},
		AudioCodecs: []string{"aac", "mp3"}, SubtitleFormats: []string{"vtt"},
	}
	one := func(id domain.ID, q domain.DownloadQuality) DownloadView {
		t.Helper()
		views, err := a.CreateDownloads(ctx, p, DownloadRequest{ItemIDs: []domain.ID{id}, Quality: q, Audio: -1, Device: phone})
		mustNil(t, err)
		if len(views) != 1 {
			t.Fatalf("downloads: %+v", views)
		}
		return views[0]
	}

	// Played as is: ready right away, and it is the original file.
	direct := one(byTitle["Big Test Movie"], domain.DownloadOriginal)
	if direct.Download.State != domain.DownloadReady || direct.Plan.Method != playback.Direct || direct.FileName != "Big Test Movie (2020).mp4" {
		t.Errorf("direct download: %+v", direct)
	}
	path, name, err := a.DownloadFile(ctx, p, direct.Download.ID)
	mustNil(t, err)
	if !strings.HasSuffix(path, "Big Test Movie (2020).mp4") || name != "Big Test Movie (2020).mp4" {
		t.Errorf("direct file: %s %s", path, name)
	}
	// Already requested: the same download.
	if again := one(byTitle["Big Test Movie"], domain.DownloadOriginal); again.Download.ID != direct.Download.ID {
		t.Errorf("requested twice: %s %s", again.Download.ID, direct.Download.ID)
	}

	// Playable MKV: MP4 without re-encoding, subtitles as WebVTT.
	remux := one(byTitle["Deux Pistes"], domain.DownloadHigh)
	// 10-bit HDR HEVC: SDR H.264, 480p at most, stereo audio.
	low := one(byTitle["HDR Test"], domain.DownloadLow)
	if remux.Plan.Method != playback.Remux || low.Plan.Method != playback.Transcode || !low.Plan.ToneMap || low.Download.State != domain.DownloadQueued {
		t.Fatalf("decisions: %+v / %+v", remux.Plan, low.Plan)
	}
	if _, _, err := a.DownloadFile(ctx, p, low.Download.ID); !isKind(err, domain.ErrNotFound) {
		t.Errorf("file not ready yet: %v", err)
	}
	waitIdle(t, a)

	for _, d := range []DownloadView{remux, low} {
		got, err := a.GetDownload(ctx, p, d.Download.ID)
		mustNil(t, err)
		if got.Download.State != domain.DownloadReady || got.Download.Progress != 1 || got.Download.Size == 0 || got.ExpiresAt == nil ||
			!strings.HasSuffix(got.Download.Path, "file.mp4") {
			t.Fatalf("prepared: %+v", got.Download)
		}
		info, err := a.prober.Probe(ctx, got.Download.Path)
		mustNil(t, err)
		var codecs []string
		for _, s := range info.Streams {
			codecs = append(codecs, s.Codec)
			if s.Kind == domain.StreamAudio && s.Channels > 2 {
				t.Errorf("%s: audio with %d channels", d.FileName, s.Channels)
			}
		}
		if info.Container != "mp4" || !slices.Equal(codecs, []string{"h264", "aac"}) {
			t.Errorf("%s: %s %v", d.FileName, info.Container, codecs)
		}
	}
	got, err := a.GetDownload(ctx, p, remux.Download.ID)
	mustNil(t, err)
	if len(got.Subtitles) != 2 || !slices.Equal(got.Subtitles[0].Formats, []string{"vtt"}) {
		t.Errorf("subtitles of the download: %+v", got.Subtitles)
	}
	sub0, err := a.DownloadSubtitle(ctx, p, remux.Download.ID, got.Subtitles[0].Position, "vtt")
	mustNil(t, err)
	if _, err := os.Stat(sub0); err != nil {
		t.Errorf("subtitle: %v", err)
	}

	// The preparation is announced to the device.
	seen := false
	for !seen {
		wait, cancel := context.WithTimeout(ctx, time.Second)
		e, err := sub.Next(wait)
		cancel()
		if err != nil {
			break
		}
		if dc, ok := e.(domain.DownloadsChanged); ok && slices.Contains(dc.DownloadIDs, low.Download.ID) {
			seen = true
		}
	}
	if !seen {
		t.Error("no DownloadsChanged event for the preparation")
	}

	// An album: its two tracks, converted to AAC at 96 kbit/s.
	albums, err := a.ListAlbums(ctx, p, ListQuery{})
	mustNil(t, err)
	var albumID domain.ID
	for _, al := range albums.Items {
		if al.Item.Title == "Album Test" {
			albumID = al.Item.ID
		}
	}
	tracks, err := a.CreateDownloads(ctx, p, DownloadRequest{ItemIDs: []domain.ID{albumID}, Quality: domain.DownloadLow, Audio: -1, Device: phone})
	mustNil(t, err)
	if len(tracks) != 2 || tracks[0].Plan.Method != playback.Convert || tracks[0].FileName != "01 - Piste Un.m4a" {
		t.Fatalf("album: %+v", tracks)
	}
	waitIdle(t, a)
	for _, tr := range tracks {
		got, err := a.GetDownload(ctx, p, tr.Download.ID)
		mustNil(t, err)
		if got.Download.State != domain.DownloadReady || !strings.HasSuffix(got.Download.Path, "-aac96.m4a") {
			t.Errorf("track: %+v", got.Download)
		}
	}
	list, err := a.ListDownloads(ctx, p)
	mustNil(t, err)
	if len(list) != 5 {
		t.Errorf("downloads of the device: %d", len(list))
	}

	// Removing deletes the prepared copy.
	mustNil(t, a.DeleteDownloads(ctx, p, []domain.ID{low.Download.ID}))
	if _, err := os.Stat(a.downloadDir(low.Download.ID)); !os.IsNotExist(err) {
		t.Errorf("copy kept: %v", err)
	}
	if _, err := a.GetDownload(ctx, p, low.Download.ID); !isKind(err, domain.ErrNotFound) {
		t.Errorf("removed download: %v", err)
	}

	// Account without the right to download.
	denied := p
	denied.Account.IsAdmin, denied.Account.DenyDownloads = false, true
	if _, err := a.CreateDownloads(ctx, denied, DownloadRequest{ItemIDs: []domain.ID{byTitle["Big Test Movie"]}, Device: phone}); !isKind(err, domain.ErrForbidden) {
		t.Errorf("download right taken from the account: %v", err)
	}

	// Played offline to the end: counted once, even if sent twice.
	at := c.now().Add(-time.Hour)
	play := []OfflinePlay{{ItemID: byTitle["Big Test Movie"], Position: time.Minute, At: at}}
	mustNil(t, a.SyncOfflinePlayback(ctx, p, play))
	mustNil(t, a.SyncOfflinePlayback(ctx, p, play))
	view, err := a.store.Read().View(ctx, domain.Viewer{ProfileID: p.Profile.ID}, byTitle["Big Test Movie"])
	mustNil(t, err)
	if !view.UserData.Played || view.UserData.PlayCount != 1 || view.UserData.LastPlayedAt == nil || !view.UserData.LastPlayedAt.Equal(at) {
		t.Errorf("offline playback: %+v", view.UserData)
	}

	// Seven days later: forgotten, copy deleted.
	dir := a.downloadDir(remux.Download.ID)
	old := time.Now().Add(-2 * time.Hour)
	mustNil(t, os.Chtimes(dir, old, old))
	c.advance(8 * 24 * time.Hour)
	a.purgeDownloads(ctx)
	if list, _ := a.ListDownloads(ctx, p); len(list) != 0 {
		t.Errorf("after seven days: %d downloads", len(list))
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("copy kept after seven days: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(a.cacheDir, "downloads")); len(entries) != 0 {
		t.Errorf("copies left: %d", len(entries))
	}
}

// The right to download: taken away or given back by an administrator, never taken from an
// administrator.
func TestAccountDenyDownloads(t *testing.T) {
	a, _ := newTestApp(t)
	ctx := context.Background()
	_, admin := setupAdmin(t, a)
	lea, err := a.CreateAccount(ctx, admin, NewAccount{Username: "Léa", Password: "a-password", DenyDownloads: true})
	mustNil(t, err)
	if !lea.DenyDownloads {
		t.Fatalf("account created without downloads: %+v", lea)
	}
	allow := false
	lea, err = a.UpdateAccount(ctx, admin, lea.ID, AccountChanges{DenyDownloads: &allow})
	mustNil(t, err)
	if lea.DenyDownloads {
		t.Errorf("downloads given back: %+v", lea)
	}
	deny := true
	if _, err := a.UpdateAccount(ctx, admin, admin.Account.ID, AccountChanges{DenyDownloads: &deny}); !isKind(err, domain.ErrInvalid) {
		t.Errorf("administrator without downloads: %v", err)
	}
	if _, err := a.CreateAccount(ctx, admin, NewAccount{Username: "Chef", Password: "a-password", IsAdmin: true, DenyDownloads: true}); !isKind(err, domain.ErrInvalid) {
		t.Errorf("administrator created without downloads: %v", err)
	}
}
