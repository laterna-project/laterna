package rpc

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
)

// Web push over HTTP: the key to subscribe with, then the subscription of the device.
func TestPushSubscriptionOverHTTP(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	token := setup(t, s)
	notifications := laternav1connect.NewNotificationServiceClient(http.DefaultClient, s.url)
	config := func() *laternav1.GetPushConfigResponse {
		t.Helper()
		resp, err := notifications.GetPushConfig(ctx, withToken(&laternav1.GetPushConfigRequest{}, token))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg
	}
	if _, err := notifications.GetPushConfig(ctx, connect.NewRequest(&laternav1.GetPushConfigRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Errorf("without a token: %v", err)
	}
	cfg := config()
	if key, err := base64.RawURLEncoding.DecodeString(cfg.GetPublicKey()); err != nil || len(key) != 65 || key[0] != 4 || cfg.GetEndpoint() != "" {
		t.Fatalf("configuration: %v %v", cfg, err)
	}

	device, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Browsers write base64url, some with padding.
	key := base64.URLEncoding.EncodeToString(device.PublicKey().Bytes())
	auth := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef"))
	const endpoint = "https://push.example.net/push/abc"
	for name, req := range map[string]*laternav1.SubscribePushRequest{
		"plain http":    {Endpoint: "http://push.example.net/push/abc", P256Dh: key, Auth: auth},
		"not base64":    {Endpoint: endpoint, P256Dh: "***", Auth: auth},
		"not a key":     {Endpoint: endpoint, P256Dh: auth, Auth: auth},
		"short secret":  {Endpoint: endpoint, P256Dh: key, Auth: "AAAA"},
		"empty request": {},
	} {
		if _, err := notifications.SubscribePush(ctx, withToken(req, token)); code(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := notifications.SubscribePush(ctx, withToken(&laternav1.SubscribePushRequest{Endpoint: endpoint, P256Dh: key, Auth: auth}, token)); err != nil {
		t.Fatal(err)
	}
	if got := config(); got.GetEndpoint() != endpoint || got.GetPublicKey() != cfg.GetPublicKey() {
		t.Errorf("subscribed: %v", got)
	}
	if _, err := notifications.UnsubscribePush(ctx, withToken(&laternav1.UnsubscribePushRequest{}, token)); err != nil {
		t.Fatal(err)
	}
	if got := config(); got.GetEndpoint() != "" {
		t.Errorf("unsubscribed: %v", got)
	}
}
