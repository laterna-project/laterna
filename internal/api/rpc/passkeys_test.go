package rpc

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/auth/webauthn/webauthntest"
)

// Passkeys through the API: public URL set by the administrator, registration, login without a
// token or a username, removal.
func TestPasskeysOverHTTP(t *testing.T) {
	s := newTestServer(t)
	token := setup(t, s)
	ctx := context.Background()
	system := laternav1connect.NewSystemServiceClient(http.DefaultClient, s.url)

	if _, err := s.auth.BeginPasskeyLogin(ctx, connect.NewRequest(&laternav1.BeginPasskeyLoginRequest{})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("without a public URL: %v", err)
	}
	public := "https://media.example.org"
	settings, err := system.UpdateSettings(ctx, withToken(&laternav1.UpdateSettingsRequest{
		PublicUrl: &public, PasskeyOrigins: &laternav1.StringList{Values: []string{"https://app.example.org"}},
	}, token))
	if err != nil || settings.Msg.GetSettings().GetPublicUrl() != public || len(settings.Msg.GetSettings().GetPasskeyOrigins()) != 1 {
		t.Fatalf("settings: %v %v", settings, err)
	}
	info, err := s.server.GetServerInfo(ctx, connect.NewRequest(&laternav1.GetServerInfoRequest{}))
	if err != nil || !info.Msg.GetPasskeys() || info.Msg.GetPublicUrl() != public {
		t.Fatalf("passkeys and public URL announced (QR code for TVs): %v %v", info, err)
	}

	// Registered from the web client hosted elsewhere (an extra allowed origin).
	phone := webauthntest.New("https://app.example.org")
	begin, err := s.auth.BeginPasskeyRegistration(ctx, withToken(&laternav1.BeginPasskeyRegistrationRequest{}, token))
	if err != nil {
		t.Fatal(err)
	}
	cred, err := phone.Create([]byte(begin.Msg.GetOptionsJson()))
	if err != nil {
		t.Fatal(err)
	}
	finish, err := s.auth.FinishPasskeyRegistration(ctx, withToken(&laternav1.FinishPasskeyRegistrationRequest{
		RegistrationId: begin.Msg.GetRegistrationId(), CredentialJson: string(cred), Name: "Phone",
	}, token))
	if err != nil || finish.Msg.GetPasskey().GetName() != "Phone" {
		t.Fatalf("registration: %v %v", finish, err)
	}

	start, err := s.auth.BeginPasskeyLogin(ctx, connect.NewRequest(&laternav1.BeginPasskeyLoginRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	assertion, err := phone.Get([]byte(start.Msg.GetOptionsJson()))
	if err != nil {
		t.Fatal(err)
	}
	login, err := s.auth.FinishPasskeyLogin(ctx, connect.NewRequest(&laternav1.FinishPasskeyLoginRequest{
		LoginId: start.Msg.GetLoginId(), CredentialJson: string(assertion),
		Device: &laternav1.Device{Name: "Phone", Client: "Tests", ClientVersion: "1", Platform: "Go"},
	}))
	if err != nil || login.Msg.GetToken() == "" || login.Msg.GetSession().GetAccount().GetUsername() != "Chloé" {
		t.Fatalf("login: %v %v", login, err)
	}

	list, err := s.auth.ListPasskeys(ctx, withToken(&laternav1.ListPasskeysRequest{}, login.Msg.GetToken()))
	if err != nil || len(list.Msg.GetPasskeys()) != 1 || list.Msg.GetPasskeys()[0].GetLastUsedAt() == nil {
		t.Fatalf("passkeys: %v %v", list, err)
	}
	if _, err := s.auth.DeletePasskey(ctx, withToken(&laternav1.DeletePasskeyRequest{PasskeyId: list.Msg.GetPasskeys()[0].GetId()}, token)); err != nil {
		t.Error(err)
	}
	if _, err := s.auth.ListPasskeys(ctx, connect.NewRequest(&laternav1.ListPasskeysRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Errorf("list without a token: %v", err)
	}
}
