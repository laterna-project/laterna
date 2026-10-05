package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
)

// Offline downloads over HTTP: the file is fetched with the token of the device that asked for it,
// in ranges, under a readable name.
func TestDownloadsOverHTTP(t *testing.T) {
	base, token := playbackServer(t)
	ctx := context.Background()
	catalog := laternav1connect.NewCatalogServiceClient(http.DefaultClient, base)
	downloads := laternav1connect.NewDownloadServiceClient(http.DefaultClient, base)
	movies, err := catalog.ListMovies(ctx, authed(&laternav1.ListMoviesRequest{}, token))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, m := range movies.Msg.GetMovies() {
		ids[m.GetTitle()] = m.GetId()
	}
	device := &laternav1.DeviceProfile{
		Containers: []string{"mp4"}, Video: []*laternav1.VideoSupport{{Codec: "h264"}}, AudioCodecs: []string{"aac"},
		SubtitleFormats: []string{"vtt"},
	}
	created, err := downloads.CreateDownloads(ctx, authed(&laternav1.CreateDownloadsRequest{
		ItemIds: []string{ids["Big Test Movie"], ids["Dual Audio"]}, Quality: laternav1.DownloadQuality_DOWNLOAD_QUALITY_HIGH, Device: device,
	}, token))
	if err != nil || len(created.Msg.GetDownloads()) != 2 {
		t.Fatalf("downloads: %v %v", created, err)
	}
	direct, remux := created.Msg.GetDownloads()[0], created.Msg.GetDownloads()[1]
	if direct.GetState() != laternav1.DownloadState_DOWNLOAD_STATE_READY || direct.GetMethod() != laternav1.DownloadMethod_DOWNLOAD_METHOD_ORIGINAL ||
		direct.GetMovie().GetTitle() != "Big Test Movie" || direct.GetUrl() == "" || direct.GetExpiresAt() == nil {
		t.Fatalf("direct download: %v", direct)
	}
	if remux.GetMethod() != laternav1.DownloadMethod_DOWNLOAD_METHOD_REMUX || remux.GetUrl() != "" || remux.GetEstimatedSize() == 0 {
		t.Fatalf("download to prepare: %v", remux)
	}
	// Prepared in the background.
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		got, err := downloads.GetDownload(ctx, authed(&laternav1.GetDownloadRequest{DownloadId: remux.GetId()}, token))
		if err != nil {
			t.Fatal(err)
		}
		if remux = got.Msg.GetDownload(); remux.GetState() == laternav1.DownloadState_DOWNLOAD_STATE_READY {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never ready: %v", remux)
		}
	}
	if len(remux.GetSubtitles()) != 2 || remux.GetFileName() != "Dual Audio (2019).mp4" {
		t.Errorf("ready: %v", remux)
	}

	get := func(path, tok string, header ...string) *http.Response {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}
	resp := get(remux.GetUrl(), token, "Range", "bytes=0-99")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent || len(body) != 100 || resp.Header.Get("Content-Type") != "video/mp4" ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "Dual Audio (2019).mp4") {
		t.Errorf("file: %d %d %v", resp.StatusCode, len(body), resp.Header)
	}
	if resp := get(remux.GetSubtitles()[0].GetFiles()[0].GetUrl(), token); resp.StatusCode != http.StatusOK ||
		!strings.HasPrefix(resp.Header.Get("Content-Type"), "text/vtt") {
		t.Errorf("subtitle: %d %v", resp.StatusCode, resp.Header)
	}
	if resp := get(remux.GetUrl(), ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("without a token: %d", resp.StatusCode)
	}
	// Another device of the same account does not see this download.
	auth := laternav1connect.NewAuthServiceClient(http.DefaultClient, base)
	other, err := auth.Login(ctx, connect.NewRequest(&laternav1.LoginRequest{
		Username: "admin", Password: "a-strong-password",
		Device: &laternav1.Device{Name: "Tablet", Client: "Test", ClientVersion: "1", Platform: "Go"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp := get(remux.GetUrl(), other.Msg.GetToken()); resp.StatusCode != http.StatusNotFound {
		t.Errorf("other device: %d", resp.StatusCode)
	}

	if _, err := downloads.DeleteDownloads(ctx, authed(&laternav1.DeleteDownloadsRequest{DownloadIds: []string{remux.GetId()}}, token)); err != nil {
		t.Fatal(err)
	}
	if resp := get(remux.GetUrl(), token); resp.StatusCode != http.StatusNotFound {
		t.Errorf("after removal: %d", resp.StatusCode)
	}
}
