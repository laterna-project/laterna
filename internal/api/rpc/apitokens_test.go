package rpc

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
)

// An API token acts as the profile that created it. A read-only one only calls what changes
// nothing, no token manages credentials or profiles, and a token goes away when it is revoked or
// when the password changes.
func TestAPITokens(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	session := setup(t, s)
	system := laternav1connect.NewSystemServiceClient(s.httpClient, s.url)
	events := laternav1connect.NewEventServiceClient(s.httpClient, s.url)
	create := func(name string, readOnly bool) (string, *laternav1.ApiToken) {
		t.Helper()
		resp, err := s.auth.CreateApiToken(ctx, withToken(&laternav1.CreateApiTokenRequest{Name: name, ReadOnly: readOnly}, session))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.GetToken(), resp.Msg.GetApiToken()
	}
	reader, readerInfo := create("  Home Assistant ", true)
	full, _ := create("Backup script", false)
	if readerInfo.GetName() != "Home Assistant" || !readerInfo.GetReadOnly() || readerInfo.GetProfile().GetName() != "Chloé" || reader == "" || reader == full {
		t.Fatalf("token: %v", readerInfo)
	}
	if _, err := s.auth.CreateApiToken(ctx, withToken(&laternav1.CreateApiTokenRequest{Name: " "}, session)); errorCode(err) != "api_token.name_required" {
		t.Errorf("token without a name: %v", err)
	}

	// A read-only token reads, even what needs an administrator, and follows the events.
	if resp, err := s.auth.GetSession(ctx, withToken(&laternav1.GetSessionRequest{}, reader)); err != nil || resp.Msg.GetSession().GetProfile().GetName() != "Chloé" {
		t.Errorf("session of the token: %v %v", resp, err)
	}
	if _, err := s.library.ListLibraries(ctx, withToken(&laternav1.ListLibrariesRequest{}, reader)); err != nil {
		t.Errorf("read-only token reading: %v", err)
	}
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := events.Subscribe(streamCtx, withToken(&laternav1.SubscribeRequest{}, reader))
	if err != nil || !stream.Receive() {
		t.Errorf("events with a read-only token: %v %v", err, stream.Err())
	}
	cancel()
	// It writes nothing; a full token does.
	name := "Attic"
	if _, err := system.UpdateSettings(ctx, withToken(&laternav1.UpdateSettingsRequest{ServerName: &name}, reader)); code(err) != connect.CodePermissionDenied || errorCode(err) != "auth.read_only_token" {
		t.Errorf("read-only token writing: %v", err)
	}
	if _, err := system.UpdateSettings(ctx, withToken(&laternav1.UpdateSettingsRequest{ServerName: &name}, full)); err != nil {
		t.Errorf("full token writing: %v", err)
	}
	// No token manages credentials or profiles.
	for what, err := range map[string]error{
		"password": func() error {
			_, err := s.auth.ChangePassword(ctx, withToken(&laternav1.ChangePasswordRequest{CurrentPassword: "a-strong-password", NewPassword: "another-password"}, full))
			return err
		}(),
		"tokens": func() error {
			_, err := s.auth.CreateApiToken(ctx, withToken(&laternav1.CreateApiTokenRequest{Name: "More"}, full))
			return err
		}(),
		"list of tokens": func() error {
			_, err := s.auth.ListApiTokens(ctx, withToken(&laternav1.ListApiTokensRequest{}, reader))
			return err
		}(),
		"profile": func() error {
			_, err := s.profile.SelectProfile(ctx, withToken(&laternav1.SelectProfileRequest{ProfileId: readerInfo.GetProfile().GetId()}, full))
			return err
		}(),
	} {
		if errorCode(err) != "auth.api_token_not_allowed" {
			t.Errorf("%s with a token: %v", what, err)
		}
	}

	// Listed apart from the devices; revoked one by one, or all with the password.
	tokens, err := s.auth.ListApiTokens(ctx, withToken(&laternav1.ListApiTokensRequest{}, session))
	if err != nil || len(tokens.Msg.GetApiTokens()) != 2 {
		t.Fatalf("tokens: %v %v", tokens, err)
	}
	devices, err := s.auth.ListSessions(ctx, withToken(&laternav1.ListSessionsRequest{}, session))
	if err != nil || len(devices.Msg.GetSessions()) != 1 {
		t.Fatalf("devices: %v %v", devices, err)
	}
	if _, err := s.auth.RevokeApiToken(ctx, withToken(&laternav1.RevokeApiTokenRequest{ApiTokenId: readerInfo.GetId()}, session)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.auth.GetSession(ctx, withToken(&laternav1.GetSessionRequest{}, reader)); code(err) != connect.CodeUnauthenticated {
		t.Errorf("revoked token: %v", err)
	}
	if _, err := s.auth.RevokeApiToken(ctx, withToken(&laternav1.RevokeApiTokenRequest{ApiTokenId: devices.Msg.GetSessions()[0].GetId()}, session)); errorCode(err) != "api_token.not_found" {
		t.Errorf("a device revoked as a token: %v", err)
	}
	if _, err := s.auth.ChangePassword(ctx, withToken(&laternav1.ChangePasswordRequest{CurrentPassword: "a-strong-password", NewPassword: "another-password"}, session)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.auth.GetSession(ctx, withToken(&laternav1.GetSessionRequest{}, full)); code(err) != connect.CodeUnauthenticated {
		t.Errorf("token after a new password: %v", err)
	}
	if _, err := s.auth.GetSession(ctx, withToken(&laternav1.GetSessionRequest{}, session)); err != nil {
		t.Errorf("the device that changed the password: %v", err)
	}
}
