package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/jobs"
	"github.com/laterna-project/laterna/internal/store"
)

// received is a request a fake channel got.
type received struct {
	host, path string
	header     http.Header
	raw        []byte
	body       map[string]any
}

// toServer sends every request to a test server, whatever its host (Discord, ntfy.example.org).
type toServer struct{ url *url.URL }

func (t toServer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Host", r.URL.Host)
	r.URL.Scheme, r.URL.Host = t.url.Scheme, t.url.Host
	return http.DefaultTransport.RoundTrip(r)
}

// Alerts: the entries of the activity log a channel wants go to Discord, ntfy or a webhook, in the
// form each one takes; secrets stay out of what is shown, and a channel that refuses says so
// without feeding another alert.
func TestAlerts(t *testing.T) {
	var mu sync.Mutex
	var got []received
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		got = append(got, received{host: r.Header.Get("X-Host"), path: r.URL.Path, header: r.Header.Clone(), raw: raw, body: body})
		mu.Unlock()
		if r.URL.Path == "/refuse" {
			http.Error(w, "unknown webhook", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), store.FileName)
	st := openStore(t, path)
	t.Cleanup(func() { _ = st.Close() })
	opts := Options{ServerName: "Living room", HTTPClient: &http.Client{Transport: toServer{target}}}
	a, err := New(ctx, st, opts)
	if err != nil {
		t.Fatal(err)
	}
	_, p := setupAdmin(t, a)
	take := func() []received {
		mu.Lock()
		defer mu.Unlock()
		out := got
		got = nil
		return out
	}
	set := func(kind domain.AlertChannelKind, u, token string, cats ...domain.AlertCategory) (domain.AlertChannel, error) {
		return a.SetAlertChannel(ctx, p, kind, AlertChanges{Enabled: true, URL: &u, Token: &token, Categories: cats})
	}

	// Addresses are checked; a channel enabled needs a category.
	for _, c := range []struct {
		kind domain.AlertChannelKind
		url  string
		cats []domain.AlertCategory
		code string
	}{
		{domain.AlertDiscord, "https://example.org/api/webhooks/1/x", []domain.AlertCategory{domain.AlertRequests}, "alert.invalid_discord_url"},
		{domain.AlertNtfy, "https://ntfy.sh/", []domain.AlertCategory{domain.AlertRequests}, "alert.invalid_ntfy_url"},
		{domain.AlertWebhook, "ftp://example.org/x", []domain.AlertCategory{domain.AlertRequests}, "alert.invalid_url"},
		{domain.AlertWebhook, "http://example.org/x", nil, "alert.no_category"},
	} {
		if _, err := set(c.kind, c.url, "", c.cats...); domain.CodeOf(err) != c.code {
			t.Errorf("%s %s: %v", c.kind, c.url, err)
		}
	}
	_, err = set(domain.AlertDiscord, "https://discord.com/api/webhooks/123/secret-token", "ignored", domain.AlertRequests, domain.AlertProblems)
	mustNil(t, err)
	_, err = set(domain.AlertNtfy, "https://ntfy.example.org/laterna/", "tk_123", domain.AlertNewEpisodes, domain.AlertProblems)
	mustNil(t, err)
	_, err = set(domain.AlertWebhook, "http://homeassistant.local:8123/api/webhook/laterna-abc", "s3cret", domain.AlertPlayback)
	mustNil(t, err)
	shown := a.AlertChannels()
	for i, want := range []string{"discord.com/api/webhooks/123/…", "ntfy.example.org/laterna", "homeassistant.local:8123/…"} {
		if AlertTarget(shown[i]) != want || (shown[i].Token != "") != (i > 0) {
			t.Errorf("channel %s shown as %q (token %q)", shown[i].Kind, AlertTarget(shown[i]), shown[i].Token)
		}
	}

	// sendQueued sends what the last entry queued, as the job queue would.
	sendQueued := func() int {
		t.Helper()
		page, err := a.Activity(ctx, ActivityQuery{PageSize: 1})
		mustNil(t, err)
		n := 0
		for _, kind := range domain.AlertChannelKinds {
			if a.alerts.get(kind).Wants(mustCategory(t, page.Entries[0])) {
				mustNil(t, a.sendAlert(ctx, strconv.FormatInt(page.Entries[0].ID, 10)+":"+string(kind)))
				n++
			}
		}
		return n
	}
	queued := func() int {
		t.Helper()
		counts, err := a.store.Read().CountJobs(ctx)
		mustNil(t, err)
		n := 0
		for _, c := range counts {
			if c.Kind == jobSendAlert {
				n += c.N
			}
		}
		return n
	}

	// A problem goes to Discord and ntfy.
	before := queued()
	a.record(ctx, domain.Activity{Kind: domain.ActivityLoginFailed, Warning: true, Text: domain.T("activity.login_failed", "username", "@everyone", "device", "TV", "ip", "203.0.113.7")})
	if queued()-before != 2 || sendQueued() != 2 {
		t.Fatalf("alerts queued for a problem: %d", queued()-before)
	}
	msgs := take()
	if len(msgs) != 2 {
		t.Fatalf("messages: %+v", msgs)
	}
	discord, ntfy := msgs[0], msgs[1]
	if discord.host != "discord.com" || discord.path != "/api/webhooks/123/secret-token" || discord.body["username"] != "Living room" ||
		discord.body["content"] == "" || discord.body["allowed_mentions"].(map[string]any)["parse"] == nil {
		t.Errorf("Discord: %+v", discord)
	}
	if ntfy.host != "ntfy.example.org" || ntfy.path != "" && ntfy.path != "/" || ntfy.body["topic"] != "laterna" || ntfy.body["priority"] != 4.0 ||
		ntfy.header.Get("Authorization") != "Bearer tk_123" || ntfy.body["title"] != "Living room" {
		t.Errorf("ntfy: %+v", ntfy)
	}

	// A playback goes to the webhook only, signed.
	a.record(ctx, domain.Activity{Kind: domain.ActivityPlaybackStarted, Text: domain.T("activity.playback_started", "profile", "Tom", "title", "Suzume", "device", "TV")})
	if sendQueued() != 1 {
		t.Fatal("no alert for a playback")
	}
	msgs = take()
	if len(msgs) != 1 {
		t.Fatalf("messages: %+v", msgs)
	}
	hook := msgs[0]
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(hook.raw)
	if hook.path != "/api/webhook/laterna-abc" || hook.body["category"] != "playback" || hook.body["kind"] != "playback.started" ||
		hook.body["key"] != "activity.playback_started" || hook.header.Get("X-Laterna-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) ||
		hook.body["server"].(map[string]any)["name"] != "Living room" {
		t.Errorf("webhook: %+v %s", hook, hook.raw)
	}

	// An entry that is no alert queues nothing.
	before = queued()
	a.record(ctx, domain.Activity{Kind: domain.ActivityLibraryScanned, Text: domain.T("activity.library_scanned", "library", "Movies")})
	if queued() != before {
		t.Error("alert for a scan")
	}

	// A channel that refuses: the reason is noted, no retry, and its failure makes no alert.
	refuse := "http://homeassistant.local:8123/refuse"
	_, err = a.SetAlertChannel(ctx, p, domain.AlertWebhook, AlertChanges{Enabled: true, URL: &refuse, Categories: []domain.AlertCategory{domain.AlertPlayback}})
	mustNil(t, err)
	a.record(ctx, domain.Activity{Kind: domain.ActivityPlaybackStarted, Text: domain.T("activity.playback_started", "profile", "Tom", "title", "Suzume", "device", "TV")})
	page, err := a.Activity(ctx, ActivityQuery{PageSize: 1})
	mustNil(t, err)
	err = a.sendAlert(ctx, strconv.FormatInt(page.Entries[0].ID, 10)+":webhook")
	if !errors.Is(err, jobs.ErrPermanent) {
		t.Errorf("refused: %v", err)
	}
	if c := a.alerts.get(domain.AlertWebhook); c.LastError == nil || c.LastError.Key != "error.alert.refused" || c.LastError.Params["status"] != "404" {
		t.Errorf("reason noted: %+v", c.LastError)
	}
	before = queued()
	a.jobFailed(ctx, jobSendAlert, "1:webhook", errors.New("refused"))
	if queued() != before {
		t.Error("the failure of an alert made an alert")
	}
	if err := a.TestAlertChannel(ctx, domain.AlertWebhook); domain.CodeOf(err) != "alert.delivery_failed" {
		t.Errorf("test of a channel that refuses: %v", err)
	}
	if err := a.TestAlertChannel(ctx, domain.AlertDiscord); err != nil {
		t.Errorf("test: %v", err)
	}
	take()

	// Kept across a restart; an empty address removes a channel.
	again, err := New(ctx, st, opts)
	if err != nil {
		t.Fatal(err)
	}
	if c := again.alerts.get(domain.AlertNtfy); c.URL != "https://ntfy.example.org/laterna" || c.Token != "tk_123" || !c.Enabled {
		t.Errorf("after a restart: %+v", c)
	}
	empty := ""
	if c, err := a.SetAlertChannel(ctx, p, domain.AlertDiscord, AlertChanges{URL: &empty}); err != nil || c.URL != "" || c.Enabled {
		t.Errorf("removed: %+v %v", c, err)
	}
}

func mustCategory(t *testing.T, e domain.Activity) domain.AlertCategory {
	t.Helper()
	cat, ok := domain.AlertOf(e)
	if !ok {
		t.Fatalf("%s is no alert", e.Kind)
	}
	return cat
}
