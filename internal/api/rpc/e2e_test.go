package rpc

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/durationpb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// testServer starts the services on a real HTTP server and provides generated clients.
type testServer struct {
	url     string
	server  laternav1connect.ServerServiceClient
	auth    laternav1connect.AuthServiceClient
	profile laternav1connect.ProfileServiceClient
	library laternav1connect.LibraryServiceClient
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a, err := app.New(ctx, st, app.Options{ServerName: "Test"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Mount(mux, a, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := srv.Client()
	return &testServer{
		url:     srv.URL,
		server:  laternav1connect.NewServerServiceClient(c, srv.URL),
		auth:    laternav1connect.NewAuthServiceClient(c, srv.URL),
		profile: laternav1connect.NewProfileServiceClient(c, srv.URL),
		library: laternav1connect.NewLibraryServiceClient(c, srv.URL),
	}
}

// withToken adds the token to a request.
func withToken[T any](msg *T, token string) *connect.Request[T] {
	req := connect.NewRequest(msg)
	if token != "" {
		req.Header().Set("Authorization", "Bearer "+token)
	}
	return req
}

func code(err error) connect.Code { return connect.CodeOf(err) }

// errorDetail returns the detail of an API error (laterna.v1.ErrorDetail): its code, params and
// causes; nil if the error carries none.
func errorDetail(err error) *laternav1.ErrorDetail {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return nil
	}
	for _, d := range ce.Details() {
		if msg, err := d.Value(); err == nil {
			if detail, ok := msg.(*laternav1.ErrorDetail); ok {
				return detail
			}
		}
	}
	return nil
}

// errorCode returns the code of an API error ("library.not_found"), or an empty string.
func errorCode(err error) string { return errorDetail(err).GetCode() }

func setup(t *testing.T, s *testServer) string {
	t.Helper()
	resp, err := s.auth.Setup(context.Background(), connect.NewRequest(&laternav1.SetupRequest{
		Username: "Chloé", Password: "a-strong-password",
		Device: &laternav1.Device{Name: "Office PC", Client: "Tests", ClientVersion: "1", Platform: "Go"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return resp.Msg.GetToken()
}

// Every method of the contract must be served: a service left out of Mount would be an API that is
// documented but missing.
func TestEveryProcedureIsMounted(t *testing.T) {
	s := newTestServer(t)
	var procedures []string
	protoregistry.GlobalFiles.RangeFilesByPackage("laterna.v1", func(fd protoreflect.FileDescriptor) bool {
		for i := range fd.Services().Len() {
			svc := fd.Services().Get(i)
			for j := range svc.Methods().Len() {
				procedures = append(procedures, "/"+string(svc.FullName())+"/"+string(svc.Methods().Get(j).Name()))
			}
		}
		return true
	})
	if len(procedures) < 13 {
		t.Fatalf("only %d methods found in the contract", len(procedures))
	}
	for _, proc := range procedures {
		resp, err := http.Post(s.url+proc, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound && resp.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s is not served", proc)
		}
	}
}

func TestAccessLevelsFromContract(t *testing.T) {
	cases := map[string]laternav1.Access{
		laternav1connect.ServerServiceGetServerInfoProcedure:  laternav1.Access_ACCESS_PUBLIC,
		laternav1connect.AuthServiceSetupProcedure:            laternav1.Access_ACCESS_PUBLIC,
		laternav1connect.AuthServiceGetSessionProcedure:       laternav1.Access_ACCESS_ACCOUNT,
		laternav1connect.ProfileServiceSelectProfileProcedure: laternav1.Access_ACCESS_ACCOUNT,
		laternav1connect.LibraryServiceListLibrariesProcedure: laternav1.Access_ACCESS_ADMIN,
		laternav1connect.CatalogServiceListMoviesProcedure:    laternav1.Access_ACCESS_PROFILE, // no option
	}
	for proc, want := range cases {
		parts := strings.Split(strings.TrimPrefix(proc, "/"), "/")
		d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(parts[0]))
		if err != nil {
			t.Fatal(err)
		}
		md := d.(protoreflect.ServiceDescriptor).Methods().ByName(protoreflect.Name(parts[1]))
		if got := accessOf(connect.Spec{Schema: md}); got != want {
			t.Errorf("%s: %v, want %v", proc, got, want)
		}
	}
	if got := accessOf(connect.Spec{}); got != laternav1.Access_ACCESS_PROFILE {
		t.Errorf("without a schema, want the strictest level (profile), got %v", got)
	}
}

func TestAllowed(t *testing.T) {
	kid := domain.Profile{Kid: true}
	admin := domain.Principal{Account: domain.Account{IsAdmin: true}}
	user := domain.Principal{Account: domain.Account{}, Profile: &kid}
	tests := []struct {
		level laternav1.Access
		p     domain.Principal
		kind  error
	}{
		{laternav1.Access_ACCESS_ACCOUNT, domain.Principal{}, nil},
		{laternav1.Access_ACCESS_PROFILE, admin, domain.ErrPrecondition},
		{laternav1.Access_ACCESS_UNSPECIFIED, admin, domain.ErrPrecondition},
		{laternav1.Access_ACCESS_PROFILE, user, nil},
		{laternav1.Access_ACCESS_ADMIN, user, domain.ErrForbidden},
		{laternav1.Access_ACCESS_ADMIN, admin, nil},
		// A kid on the administrator's account administers nothing.
		{laternav1.Access_ACCESS_ADMIN, domain.Principal{Account: domain.Account{IsAdmin: true}, Profile: &kid}, domain.ErrForbidden},
	}
	for _, tt := range tests {
		err := allowed(tt.level, tt.p)
		if (tt.kind == nil) != (err == nil) || (tt.kind != nil && !errors.Is(err, tt.kind)) {
			t.Errorf("level %v: %v, want %v", tt.level, err, tt.kind)
		}
	}
}

func TestAuthFlowOverHTTP(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	info, err := s.server.GetServerInfo(ctx, connect.NewRequest(&laternav1.GetServerInfoRequest{}))
	if err != nil || !info.Msg.GetSetupRequired() || info.Msg.GetPublicUrl() != "" {
		t.Fatalf("setup not announced, or a public URL without the setting: %v %v", info, err)
	}

	if _, err := s.auth.GetSession(ctx, withToken(&laternav1.GetSessionRequest{}, "")); code(err) != connect.CodeUnauthenticated {
		t.Errorf("without a token: %v", err)
	}
	if _, err := s.auth.GetSession(ctx, withToken(&laternav1.GetSessionRequest{}, "lat_faux")); code(err) != connect.CodeUnauthenticated {
		t.Errorf("unknown token: %v", err)
	}

	token := setup(t, s)
	if _, err := s.auth.Setup(ctx, connect.NewRequest(&laternav1.SetupRequest{Username: "x", Password: "a-strong-password"})); code(err) != connect.CodeFailedPrecondition {
		t.Errorf("second setup: %v", err)
	}

	sess, err := s.auth.GetSession(ctx, withToken(&laternav1.GetSessionRequest{}, token))
	if err != nil {
		t.Fatal(err)
	}
	m := sess.Msg.GetSession()
	if !m.GetCurrent() || !m.GetAccount().GetIsAdmin() || m.GetProfile().GetName() != "Chloé" || m.GetDevice().GetName() != "Office PC" {
		t.Errorf("session: %v", m)
	}

	// Second profile: at the next login one has to be picked.
	kid, err := s.profile.CreateProfile(ctx, withToken(&laternav1.CreateProfileRequest{Name: "Lou", Kid: true}, token))
	if err != nil {
		t.Fatal(err)
	}
	login, err := s.auth.Login(ctx, connect.NewRequest(&laternav1.LoginRequest{Username: "chloé", Password: "a-strong-password"}))
	if err != nil {
		t.Fatal(err)
	}
	tvToken := login.Msg.GetToken()
	if login.Msg.GetSession().Profile != nil {
		t.Error("profile picked automatically despite two profiles")
	}
	if _, err := s.profile.SelectProfile(ctx, withToken(&laternav1.SelectProfileRequest{ProfileId: kid.Msg.GetProfile().GetId()}, tvToken)); err != nil {
		t.Fatal(err)
	}

	// A kid cannot sign devices out; the parent can.
	sessions, err := s.auth.ListSessions(ctx, withToken(&laternav1.ListSessionsRequest{}, token))
	if err != nil || len(sessions.Msg.GetSessions()) != 2 {
		t.Fatalf("sessions: %v %v", sessions, err)
	}
	if _, err := s.auth.RevokeSession(ctx, withToken(&laternav1.RevokeSessionRequest{SessionId: m.GetId()}, tvToken)); code(err) != connect.CodePermissionDenied {
		t.Errorf("kid revoking: %v", err)
	}
	if _, err := s.auth.RevokeSession(ctx, withToken(&laternav1.RevokeSessionRequest{SessionId: "not-an-id"}, token)); code(err) != connect.CodeInvalidArgument {
		t.Errorf("invalid ID: %v", err)
	}
	if _, err := s.auth.RevokeSession(ctx, withToken(&laternav1.RevokeSessionRequest{SessionId: login.Msg.GetSession().GetId()}, token)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.auth.GetSession(ctx, withToken(&laternav1.GetSessionRequest{}, tvToken)); code(err) != connect.CodeUnauthenticated {
		t.Errorf("revoked device: %v", err)
	}

	// Wrong password: a clear code and message, with no internal detail.
	_, err = s.auth.Login(ctx, connect.NewRequest(&laternav1.LoginRequest{Username: "chloé", Password: "faux"}))
	if code(err) != connect.CodeUnauthenticated || errorCode(err) != "auth.invalid_credentials" || !strings.Contains(err.Error(), "Incorrect username or password") {
		t.Errorf("wrong password: %v", err)
	}

	if _, err := s.auth.Logout(ctx, withToken(&laternav1.LogoutRequest{}, token)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.auth.GetSession(ctx, withToken(&laternav1.GetSessionRequest{}, token)); code(err) != connect.CodeUnauthenticated {
		t.Errorf("after logout: %v", err)
	}
}

// An administrator manages accounts; their kid, on the same account, cannot.
func TestAccountServiceOverHTTP(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	token := setup(t, s)
	accounts := laternav1connect.NewAccountServiceClient(http.DefaultClient, s.url)
	twelve := int32(12)
	created, err := accounts.CreateAccount(ctx, withToken(&laternav1.CreateAccountRequest{
		Username: "Léa", Password: "a-password", Parental: &laternav1.ParentalControl{MaxAge: &twelve, BlockUnrated: true},
	}, token))
	if err != nil {
		t.Fatal(err)
	}
	a := created.Msg.GetAccount()
	if a.GetIsAdmin() || !a.GetLibraries().GetAll() || a.GetParental().GetMaxAge() != 12 || !a.GetParental().GetBlockUnrated() {
		t.Errorf("account created: %v", a)
	}
	disabled := true
	if _, err := accounts.UpdateAccount(ctx, withToken(&laternav1.UpdateAccountRequest{
		AccountId: a.GetId(), Disabled: &disabled, Libraries: &laternav1.LibraryAccess{LibraryIds: []string{"not-an-id"}},
	}, token)); code(err) != connect.CodeInvalidArgument {
		t.Errorf("invalid library: %v", err)
	}
	list, err := accounts.ListAccounts(ctx, withToken(&laternav1.ListAccountsRequest{}, token))
	if err != nil || len(list.Msg.GetAccounts()) != 2 || list.Msg.GetAccounts()[0].GetLastActiveAt() == nil || list.Msg.GetAccounts()[1].GetProfileCount() != 1 {
		t.Fatalf("accounts: %v %v", list, err)
	}

	// On a kid profile the same administrator no longer administers.
	kid, err := s.profile.CreateProfile(ctx, withToken(&laternav1.CreateProfileRequest{Name: "Lou", Kid: true}, token))
	if err != nil {
		t.Fatal(err)
	}
	if kid.Msg.GetProfile().GetParental().GetMaxAge() != 10 {
		t.Errorf("kid profile: %v", kid.Msg.GetProfile())
	}
	if _, err := s.profile.SelectProfile(ctx, withToken(&laternav1.SelectProfileRequest{ProfileId: kid.Msg.GetProfile().GetId()}, token)); err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.ListAccounts(ctx, withToken(&laternav1.ListAccountsRequest{}, token)); code(err) != connect.CodePermissionDenied {
		t.Errorf("administration from a kid profile: %v", err)
	}
}

// Dashboard, settings, jobs, activity log and devices, over HTTP.
func TestAdministrationOverHTTP(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	token := setup(t, s)
	system := laternav1connect.NewSystemServiceClient(http.DefaultClient, s.url)
	activity := laternav1connect.NewActivityServiceClient(http.DefaultClient, s.url)

	status, err := system.GetSystemStatus(ctx, withToken(&laternav1.GetSystemStatusRequest{}, token))
	if err != nil || status.Msg.GetStatus().GetVersion() == "" || status.Msg.GetStatus().GetOs() == "" {
		t.Fatalf("status: %v %v", status, err)
	}
	name := "Grenier"
	updated, err := system.UpdateSettings(ctx, withToken(&laternav1.UpdateSettingsRequest{
		ServerName: &name, ScanInterval: durationpb.New(12 * time.Hour),
	}, token))
	if err != nil || updated.Msg.GetSettings().GetServerName() != "Grenier" || updated.Msg.GetSettings().GetScanInterval().AsDuration() != 12*time.Hour {
		t.Fatalf("settings: %v %v", updated, err)
	}
	if _, err := system.UpdateSettings(ctx, withToken(&laternav1.UpdateSettingsRequest{ScanInterval: durationpb.New(time.Second)}, token)); code(err) != connect.CodeInvalidArgument {
		t.Errorf("interval too short: %v", err)
	}
	info, err := s.server.GetServerInfo(ctx, connect.NewRequest(&laternav1.GetServerInfoRequest{}))
	if err != nil || info.Msg.GetName() != "Grenier" {
		t.Errorf("announced name: %v %v", info, err)
	}
	if _, err := system.RunTask(ctx, withToken(&laternav1.RunTaskRequest{Task: laternav1.SystemTask_SYSTEM_TASK_SCAN_LIBRARIES}, token)); err != nil {
		t.Errorf("task: %v", err)
	}
	if _, err := system.RunTask(ctx, withToken(&laternav1.RunTaskRequest{}, token)); code(err) != connect.CodeInvalidArgument {
		t.Errorf("unspecified task: %v", err)
	}
	if jobs, err := system.ListJobs(ctx, withToken(&laternav1.ListJobsRequest{}, token)); err != nil || len(jobs.Msg.GetFailed()) != 0 {
		t.Errorf("jobs: %v %v", jobs, err)
	}
	if logs, err := system.ListLogs(ctx, withToken(&laternav1.ListLogsRequest{MinLevel: laternav1.LogLevel_LOG_LEVEL_WARN}, token)); err != nil || len(logs.Msg.GetEntries()) != 0 {
		t.Errorf("log (no ring): %v %v", logs, err)
	}

	act, err := activity.ListActivity(ctx, withToken(&laternav1.ListActivityRequest{PageSize: 1}, token))
	if err != nil || len(act.Msg.GetEntries()) != 1 || act.Msg.GetEntries()[0].GetKind() != laternav1.ActivityKind_ACTIVITY_KIND_SETTINGS_UPDATED ||
		act.Msg.GetNextPageToken() == "" {
		t.Errorf("activity: %v %v", act, err)
	}
	devices, err := activity.ListDevices(ctx, withToken(&laternav1.ListDevicesRequest{}, token))
	if err != nil || len(devices.Msg.GetDevices()) != 1 || !devices.Msg.GetDevices()[0].GetCurrent() || devices.Msg.GetDevices()[0].GetUsername() != "Chloé" {
		t.Errorf("devices: %v %v", devices, err)
	}
	if plays, err := activity.ListPlaybacks(ctx, withToken(&laternav1.ListPlaybacksRequest{}, token)); err != nil || len(plays.Msg.GetPlaybacks()) != 0 {
		t.Errorf("playbacks: %v %v", plays, err)
	}
	if _, err := activity.EndPlayback(ctx, withToken(&laternav1.EndPlaybackRequest{PlaybackId: domain.NewID().String()}, token)); code(err) != connect.CodeNotFound {
		t.Errorf("unknown playback: %v", err)
	}
}

// Playlists of the profile, and collections that only administrators can write.
func TestCollectionsAndPlaylistsOverHTTP(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	token := setup(t, s)
	collections := laternav1connect.NewCollectionServiceClient(http.DefaultClient, s.url)
	playlists := laternav1connect.NewPlaylistServiceClient(http.DefaultClient, s.url)

	created, err := collections.CreateCollection(ctx, withToken(&laternav1.CreateCollectionRequest{Name: "Sélection"}, token))
	if err != nil || !created.Msg.GetCollection().GetManual() || created.Msg.GetCollection().GetItemCount() != 0 {
		t.Fatalf("collection: %v %v", created, err)
	}
	list, err := collections.ListCollections(ctx, withToken(&laternav1.ListCollectionsRequest{}, token))
	if err != nil || len(list.Msg.GetCollections()) != 1 {
		t.Errorf("collections (empty, visible to the administrator): %v %v", list, err)
	}
	if _, err := collections.CreateCollection(ctx, withToken(&laternav1.CreateCollectionRequest{Name: " "}, token)); code(err) != connect.CodeInvalidArgument {
		t.Errorf("empty name: %v", err)
	}
	if _, err := collections.DeleteCollection(ctx, withToken(&laternav1.DeleteCollectionRequest{CollectionId: created.Msg.GetCollection().GetId()}, token)); err != nil {
		t.Errorf("deletion: %v", err)
	}

	pl, err := playlists.CreatePlaylist(ctx, withToken(&laternav1.CreatePlaylistRequest{Name: "Soirée"}, token))
	if err != nil || pl.Msg.GetPlaylist().GetName() != "Soirée" || pl.Msg.GetPlaylist().GetEntryCount() != 0 {
		t.Fatalf("playlist: %v %v", pl, err)
	}
	id := pl.Msg.GetPlaylist().GetId()
	if _, err := playlists.AddToPlaylist(ctx, withToken(&laternav1.AddToPlaylistRequest{PlaylistId: id, ItemIds: []string{domain.NewID().String()}}, token)); code(err) != connect.CodeNotFound {
		t.Errorf("unknown item: %v", err)
	}
	renamed, err := playlists.RenamePlaylist(ctx, withToken(&laternav1.RenamePlaylistRequest{PlaylistId: id, Name: "Vendredi"}, token))
	if err != nil || renamed.Msg.GetPlaylist().GetName() != "Vendredi" {
		t.Errorf("rename: %v %v", renamed, err)
	}
	got, err := playlists.GetPlaylist(ctx, withToken(&laternav1.GetPlaylistRequest{PlaylistId: id}, token))
	if err != nil || got.Msg.GetPlaylist().GetName() != "Vendredi" || len(got.Msg.GetEntries()) != 0 {
		t.Errorf("playlist read back: %v %v", got, err)
	}
	all, err := playlists.ListPlaylists(ctx, withToken(&laternav1.ListPlaylistsRequest{}, token))
	if err != nil || len(all.Msg.GetPlaylists()) != 1 {
		t.Errorf("playlists: %v %v", all, err)
	}
	if _, err := playlists.DeletePlaylist(ctx, withToken(&laternav1.DeletePlaylistRequest{PlaylistId: id}, token)); err != nil {
		t.Errorf("deletion: %v", err)
	}

	// A non-administrator account reads collections but does not create any.
	accounts := laternav1connect.NewAccountServiceClient(http.DefaultClient, s.url)
	if _, err := accounts.CreateAccount(ctx, withToken(&laternav1.CreateAccountRequest{Username: "Léa", Password: "a-password"}, token)); err != nil {
		t.Fatal(err)
	}
	login, err := s.auth.Login(ctx, connect.NewRequest(&laternav1.LoginRequest{Username: "Léa", Password: "a-password"}))
	if err != nil {
		t.Fatal(err)
	}
	lea := login.Msg.GetToken()
	if _, err := collections.ListCollections(ctx, withToken(&laternav1.ListCollectionsRequest{}, lea)); err != nil {
		t.Errorf("reading collections: %v", err)
	}
	if _, err := collections.CreateCollection(ctx, withToken(&laternav1.CreateCollectionRequest{Name: "À moi"}, lea)); code(err) != connect.CodePermissionDenied {
		t.Errorf("creation by a non-administrator: %v", err)
	}
}
