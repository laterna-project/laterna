package app

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/logging"
	"github.com/laterna-project/laterna/internal/store"
)

func TestSettings(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), store.FileName)
	st := openStore(t, path)
	a, err := New(ctx, st, Options{ServerName: "Living room"})
	if err != nil {
		t.Fatal(err)
	}
	_, admin := setupAdmin(t, a)
	if s := a.Settings(); s.ServerName != "Living room" || !s.DownloadImages || s.ScanInterval != 6*time.Hour || s.MissingGrace != 72*time.Hour || !s.Trickplay || !s.DetectSegments {
		t.Errorf("defaults: %+v", s)
	}
	name, off, never, week := "  Attic  ", false, time.Duration(0), 7*24*time.Hour
	public, rpID := "https://media.example.org/laterna/", "Example.org"
	origins := []string{"https://app.example.org/", "android:apk-key-hash:abc"}
	s, err := a.UpdateSettings(ctx, admin, SettingsChanges{
		ServerName: &name, DownloadImages: &off, ScanInterval: &never, MissingGrace: &week, Trickplay: &off, DetectSegments: &off,
		PublicURL: &public, PasskeyRPID: &rpID, PasskeyOrigins: &origins,
	})
	mustNil(t, err)
	if s.ServerName != "Attic" || s.DownloadImages || s.ScanInterval != 0 || s.MissingGrace != week || s.Trickplay || s.DetectSegments || a.Server().Name != "Attic" ||
		s.PublicURL != "https://media.example.org/laterna" || s.PasskeyRPID != "example.org" ||
		!slices.Equal(s.PasskeyOrigins, []string{"https://app.example.org", "android:apk-key-hash:abc"}) {
		t.Errorf("after the update: %+v, server %q", s, a.Server().Name)
	}
	empty, tooShort, tooLong := " ", time.Minute, 100*24*time.Hour
	badURL, badRP, badOrigins := "ftp://media", "https://example.org", []string{"http://media.example.org"}
	for _, ch := range []SettingsChanges{
		{ServerName: &empty},
		{ScanInterval: &tooShort},
		{MissingGrace: &tooShort},
		{MissingGrace: &tooLong},
		{PublicURL: &badURL},
		{PasskeyRPID: &badRP},
		{PasskeyOrigins: &badOrigins},
	} {
		if _, err := a.UpdateSettings(ctx, admin, ch); !isKind(err, domain.ErrInvalid) {
			t.Errorf("%+v: %v", ch, err)
		}
	}
	// Kept across a restart, on top of the default name.
	_ = st.Close()
	st = openStore(t, path)
	defer func() { _ = st.Close() }()
	again, err := New(ctx, st, Options{ServerName: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Settings(); !reflect.DeepEqual(got, s) {
		t.Errorf("after the restart: %+v, want %+v", got, s)
	}
	page, err := again.Activity(ctx, ActivityQuery{})
	mustNil(t, err)
	if len(page.Entries) == 0 || page.Entries[0].Kind != domain.ActivitySettingsUpdated || !strings.Contains(page.Entries[0].Text.String(), "Attic") {
		t.Errorf("activity: %+v", page.Entries)
	}
}

func TestActivityOfLoginsAndAccounts(t *testing.T) {
	a, _ := newTestApp(t)
	ctx := context.Background()
	_, admin := setupAdmin(t, a)
	_, err := a.CreateAccount(ctx, admin, NewAccount{Username: "Léa", Password: "a-password"})
	mustNil(t, err)
	_, _ = a.Login(ctx, "Léa", "wrong-password", dev("TV"), "10.0.0.9")
	_, _ = a.Login(ctx, "Unknown", "wrong-password", dev("TV"), "10.0.0.9")
	login(t, a, "Léa", "a-password")

	page, err := a.Activity(ctx, ActivityQuery{PageSize: 2})
	mustNil(t, err)
	if len(page.Entries) != 2 || page.NextPageToken == "" || page.Entries[0].Kind != domain.ActivityLogin ||
		page.Entries[0].Text.Key != "activity.login" || page.Entries[0].Text.Params["username"] != "Léa" || page.Entries[0].Text.Params["ip"] != "10.0.0.2" {
		t.Fatalf("first page: %+v", page)
	}
	rest, err := a.Activity(ctx, ActivityQuery{PageToken: page.NextPageToken})
	mustNil(t, err)
	var kinds []domain.ActivityKind
	for _, e := range append(page.Entries, rest.Entries...) {
		kinds = append(kinds, e.Kind)
	}
	want := []domain.ActivityKind{
		domain.ActivityLogin, domain.ActivityLoginFailed, domain.ActivityLoginFailed, domain.ActivityAccountCreated, domain.ActivityAccountCreated,
	}
	if !slices.Equal(kinds, want) || rest.NextPageToken != "" {
		t.Errorf("activity: %v (next %q)", kinds, rest.NextPageToken)
	}
	warnings, err := a.Activity(ctx, ActivityQuery{WarningsOnly: true})
	mustNil(t, err)
	if len(warnings.Entries) != 2 || warnings.Entries[0].Text.Key != "activity.login_failed" || warnings.Entries[0].Text.Params["username"] != "Unknown" {
		t.Errorf("refused logins: %+v", warnings.Entries)
	}
	if _, err := a.Activity(ctx, ActivityQuery{PageToken: "!"}); !isKind(err, domain.ErrInvalid) {
		t.Errorf("invalid token: %v", err)
	}
}

func TestFailedJobsAdministration(t *testing.T) {
	a, _ := startApp(t)
	ctx := context.Background()
	// A metadata refresh on an invalid target fails for good.
	mustNil(t, a.store.Write(ctx, func(q store.Q) error { return a.jobs.Enqueue(ctx, q, jobItemMetadata, "not-an-id", priorityUser) }))
	a.jobs.Kick()
	var o JobsOverview
	deadline := time.Now().Add(10 * time.Second)
	for len(o.Failed) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		var err error
		o, err = a.Jobs(ctx)
		mustNil(t, err)
	}
	if len(o.Failed) != 1 || o.Failed[0].Kind != jobItemMetadata || o.Failed[0].Label != "not-an-id" || o.Failed[0].LastError == "" {
		t.Fatalf("failed jobs: %+v", o)
	}
	if st, _ := a.SystemStatus(ctx); st.JobsFailed != 1 || st.Version == "" || st.OS == "" {
		t.Errorf("status: %+v", st)
	}
	if page, _ := a.Activity(ctx, ActivityQuery{WarningsOnly: true}); len(page.Entries) != 1 || page.Entries[0].Kind != domain.ActivityJobFailed {
		t.Errorf("activity: %+v", page.Entries)
	}
	// Retried, it fails again; deleted, it is gone.
	n, err := a.RetryJobs(ctx, nil)
	mustNil(t, err)
	if n != 1 {
		t.Errorf("%d jobs retried", n)
	}
	if _, err := a.RetryJobs(ctx, []int64{424242}); !isKind(err, domain.ErrNotFound) {
		t.Errorf("unknown job: %v", err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if o, _ = a.Jobs(ctx); len(o.Failed) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	n, err = a.DeleteJobs(ctx, []int64{o.Failed[0].ID})
	mustNil(t, err)
	if o, _ = a.Jobs(ctx); n != 1 || len(o.Failed) != 0 {
		t.Errorf("after deleting: %d, %+v", n, o.Failed)
	}
	mustNil(t, a.RunTask(ctx, TaskPurge))
	if err := a.RunTask(ctx, "cleanup"); !isKind(err, domain.ErrInvalid) {
		t.Errorf("unknown task: %v", err)
	}
}

func TestLogsAndDevices(t *testing.T) {
	ctx := context.Background()
	ring := logging.NewRing(50)
	logDir := t.TempDir()
	for _, name := range []string{"laterna_20260929.log", "laterna_20260930.log", "other.txt"} {
		mustNil(t, os.WriteFile(filepath.Join(logDir, name), []byte("x"), 0o600))
	}
	st := openStore(t, filepath.Join(t.TempDir(), store.FileName))
	defer func() { _ = st.Close() }()
	log := slog.New(ring.Handler(slog.LevelInfo))
	a, err := New(ctx, st, Options{ServerName: "Test", Logger: log, Logs: ring, LogDir: logDir})
	if err != nil {
		t.Fatal(err)
	}
	_, admin := setupAdmin(t, a)
	name := "Attic"
	_, err = a.UpdateSettings(ctx, admin, SettingsChanges{ServerName: &name})
	mustNil(t, err)
	entries, err := a.RecentLogs(slog.LevelInfo, "attic", 0)
	mustNil(t, err)
	if len(entries) != 1 || entries[0].Message != "settings updated" {
		t.Errorf("log: %+v", entries)
	}
	if _, err := a.RecentLogs(slog.LevelInfo, "", 5000); !isKind(err, domain.ErrInvalid) {
		t.Errorf("limit: %v", err)
	}
	files, err := a.LogFiles()
	mustNil(t, err)
	if len(files) != 2 || files[0].Name != "laterna_20260930.log" {
		t.Errorf("files: %+v", files)
	}
	f, err := a.OpenLogFile("laterna_20260929.log")
	mustNil(t, err)
	_ = f.Close()
	for _, bad := range []string{"other.txt", "../laterna.db", "laterna_20260101.log"} {
		if _, err := a.OpenLogFile(bad); !isKind(err, domain.ErrNotFound) {
			t.Errorf("%q: %v", bad, err)
		}
	}

	// Devices of every account; an administrator signs one out.
	_, err = a.CreateAccount(ctx, admin, NewAccount{Username: "Léa", Password: "a-password"})
	mustNil(t, err)
	l, _ := login(t, a, "Léa", "a-password")
	devices, err := a.Devices(ctx)
	mustNil(t, err)
	if len(devices) != 2 || devices[0].Username != "Léa" || devices[0].ProfileName != "Léa" {
		t.Fatalf("devices: %+v", devices)
	}
	mustNil(t, a.RevokeDevice(ctx, admin, devices[0].Session.ID))
	if _, err := a.Authenticate(ctx, l.Token, ""); !isKind(err, domain.ErrUnauthenticated) {
		t.Errorf("signed-out device still valid: %v", err)
	}
	if err := a.RevokeDevice(ctx, admin, domain.NewID()); !isKind(err, domain.ErrNotFound) {
		t.Errorf("unknown device: %v", err)
	}
}

// Playbacks in progress, as an administrator sees them and can stop one. The activity log tells the
// start and the end.
func TestActivePlaybacks(t *testing.T) {
	a, _, p, movies := moviesByTitle(t)
	ctx := context.Background()
	info, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: movies["Dual Audio"].ID, Audio: -1, Device: browser})
	mustNil(t, err)
	plays := a.Playbacks()
	if len(plays) != 1 || plays[0].ID != info.SessionID || plays[0].Title != "Dual Audio" || plays[0].Method != string(info.Method) ||
		plays[0].ProfileName == "" || plays[0].Device != "PC" || plays[0].Duration == 0 {
		t.Fatalf("playbacks: %+v", plays)
	}
	if st, _ := a.SystemStatus(ctx); st.Playbacks != 1 {
		t.Errorf("status: %d playbacks", st.Playbacks)
	}
	mustNil(t, a.EndPlayback(ctx, p, info.SessionID))
	if len(a.Playbacks()) != 0 {
		t.Error("playback still in progress")
	}
	if err := a.EndPlayback(ctx, p, info.SessionID); !isKind(err, domain.ErrNotFound) {
		t.Errorf("second stop: %v", err)
	}
	page, err := a.Activity(ctx, ActivityQuery{PageSize: 2})
	mustNil(t, err)
	if len(page.Entries) != 2 || page.Entries[0].Kind != domain.ActivityPlaybackStopped || page.Entries[1].Kind != domain.ActivityPlaybackStarted ||
		page.Entries[1].Text.Params["title"] != "Dual Audio" || page.Entries[1].Text.Params["device"] != "PC" || page.Entries[1].ItemID == nil {
		t.Errorf("activity: %+v", page.Entries)
	}
}
