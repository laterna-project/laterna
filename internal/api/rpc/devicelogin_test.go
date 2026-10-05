package rpc

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
)

// Device login over HTTP: the TV asks, the phone approves, the TV gets a token that works. A poll
// that comes too soon is refused.
func TestDeviceLoginOverHTTP(t *testing.T) {
	s := newTestServer(t)
	phone := setup(t, s)
	ctx := context.Background()

	start, err := s.auth.StartDeviceLogin(ctx, connect.NewRequest(&laternav1.StartDeviceLoginRequest{
		Device: &laternav1.Device{Name: "Living room", Client: "Laterna TV", ClientVersion: "1", Platform: "Android TV"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	st := start.Msg
	if len(st.GetUserCode()) != 9 || st.GetInterval().AsDuration().Seconds() != 5 || st.GetDeviceCode() == "" ||
		st.GetVerificationUrl() != "" || st.GetVerificationUrlComplete() != "" {
		t.Fatalf("request: %v", st)
	}
	// With the web client's address set, the server says where to approve, code included.
	web := "https://app.example.org"
	system := laternav1connect.NewSystemServiceClient(http.DefaultClient, s.url)
	if _, err := system.UpdateSettings(ctx, withToken(&laternav1.UpdateSettingsRequest{WebUrl: &web}, phone)); err != nil {
		t.Fatal(err)
	}
	withURL, err := s.auth.StartDeviceLogin(ctx, connect.NewRequest(&laternav1.StartDeviceLoginRequest{
		Device: &laternav1.Device{Name: "Bedroom", Client: "Laterna TV", ClientVersion: "1", Platform: "Android TV"},
	}))
	if err != nil || withURL.Msg.GetVerificationUrl() != "https://app.example.org/device" ||
		withURL.Msg.GetVerificationUrlComplete() != "https://app.example.org/device?code="+withURL.Msg.GetUserCode() {
		t.Fatalf("verification URL: %v %v", withURL, err)
	}
	poll := func(deviceCode string) (*laternav1.PollDeviceLoginResponse, error) {
		r, err := s.auth.PollDeviceLogin(ctx, connect.NewRequest(&laternav1.PollDeviceLoginRequest{DeviceCode: deviceCode}))
		if err != nil {
			return nil, err
		}
		return r.Msg, nil
	}

	got, err := s.auth.GetDeviceLogin(ctx, withToken(&laternav1.GetDeviceLoginRequest{UserCode: st.GetUserCode()}, phone))
	if err != nil || got.Msg.GetDevice().GetPlatform() != "Android TV" {
		t.Fatalf("device: %v %v", got, err)
	}
	// Without a session: refused.
	if _, err := s.auth.ApproveDeviceLogin(ctx, connect.NewRequest(&laternav1.ApproveDeviceLoginRequest{UserCode: st.GetUserCode()})); code(err) != connect.CodeUnauthenticated {
		t.Errorf("anonymous approval: %v", err)
	}
	if _, err := s.auth.ApproveDeviceLogin(ctx, withToken(&laternav1.ApproveDeviceLoginRequest{UserCode: st.GetUserCode(), SelectProfile: true}, phone)); err != nil {
		t.Fatal(err)
	}
	// First poll: the session, with a token that works.
	r, err := poll(st.GetDeviceCode())
	if err != nil || r.GetState() != laternav1.DeviceLoginState_DEVICE_LOGIN_STATE_APPROVED || r.GetToken() == "" ||
		r.GetSession().GetDevice().GetName() != "Living room" {
		t.Fatalf("approved: %v %v", r, err)
	}
	session, err := s.auth.GetSession(ctx, withToken(&laternav1.GetSessionRequest{}, r.GetToken()))
	if err != nil || session.Msg.GetSession().GetProfile() == nil {
		t.Errorf("TV session: %v %v", session, err)
	}
	if again, err := poll(st.GetDeviceCode()); err != nil || again.GetState() != laternav1.DeviceLoginState_DEVICE_LOGIN_STATE_EXPIRED {
		t.Errorf("handed over only once: %v %v", again, err)
	}

	// Polling too fast: RESOURCE_EXHAUSTED.
	other, err := s.auth.StartDeviceLogin(ctx, connect.NewRequest(&laternav1.StartDeviceLoginRequest{
		Device: &laternav1.Device{Name: "Bedroom", Client: "Laterna TV", ClientVersion: "1", Platform: "tvOS"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if r, err := poll(other.Msg.GetDeviceCode()); err != nil || r.GetState() != laternav1.DeviceLoginState_DEVICE_LOGIN_STATE_PENDING {
		t.Fatalf("pending: %v %v", r, err)
	}
	if _, err := poll(other.Msg.GetDeviceCode()); code(err) != connect.CodeResourceExhausted {
		t.Errorf("poll too soon: %v", err)
	}
}
