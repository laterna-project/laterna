package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/webpush/pushtest"
)

// A device that subscribed gets the notifications of the profile picked on it, encrypted for it and
// written in the language of the profile.
func TestPushNotifications(t *testing.T) {
	a, _ := newTestApp(t)
	_, admin := setupAdmin(t, a)
	ctx := context.Background()
	push := pushtest.New(t)
	a.pushHTTP = push.Client()
	_, err := a.CreateAccount(ctx, admin, NewAccount{Username: "Léa", Password: "a-password"})
	mustNil(t, err)
	_, lea := login(t, a, "Léa", "a-password")

	// The key browsers subscribe for is created once and kept.
	cfg, err := a.PushConfig(ctx, admin)
	if err != nil || len(cfg.PublicKey) != 87 || cfg.Endpoint != "" {
		t.Fatalf("configuration: %+v %v", cfg, err)
	}
	mustNil(t, a.loadPushKeys(ctx))
	if again, _ := a.PushConfig(ctx, admin); again.PublicKey != cfg.PublicKey {
		t.Error("the push key changed at the next start")
	}

	phone, tablet := push.NewDevice(t), push.NewDevice(t)
	sub := phone.Subscription
	for name, bad := range map[string]func() error{
		"plain http": func() error { return a.SubscribePush(ctx, admin, "http://push.example.net/x", sub.P256DH, sub.Auth) },
		"short key":  func() error { return a.SubscribePush(ctx, admin, sub.Endpoint, sub.P256DH[:10], sub.Auth) },
		"no secret":  func() error { return a.SubscribePush(ctx, admin, sub.Endpoint, sub.P256DH, nil) },
	} {
		if err := bad(); domain.CodeOf(err) != "notification.invalid_subscription" {
			t.Errorf("%s: %v", name, err)
		}
	}
	mustNil(t, a.SubscribePush(ctx, admin, sub.Endpoint, sub.P256DH, sub.Auth))
	mustNil(t, a.SubscribePush(ctx, lea, tablet.Subscription.Endpoint, tablet.Subscription.P256DH, tablet.Subscription.Auth))
	if cfg, _ := a.PushConfig(ctx, admin); cfg.Endpoint != sub.Endpoint {
		t.Errorf("subscribed: %+v", cfg)
	}

	// send tells a profile something and runs the push the notification asked for.
	send := func(p domain.Principal, title string) {
		t.Helper()
		before := pendingJobs(t, a, jobPushNotification)
		a.notify(ctx, domain.Notification{
			ProfileID: p.Profile.ID, Kind: domain.NotificationRequestAvailable, Text: domain.T("notification.request_available", "title", title),
		})
		page, err := a.Notifications(ctx, p, "", 1)
		mustNil(t, err)
		if pendingJobs(t, a, jobPushNotification) != before+1 {
			t.Fatalf("no push asked for %q", title)
		}
		mustNil(t, a.pushNotification(ctx, page.Notifications[0].ID.String()))
	}
	send(admin, "Frieren")
	got := push.Messages(phone)
	if len(got) != 1 || len(push.Messages(tablet)) != 0 || push.Refused() != 0 {
		t.Fatalf("messages: %v, to the other device %v, refused %d", got, push.Messages(tablet), push.Refused())
	}
	var msg pushMessage
	mustNil(t, json.Unmarshal([]byte(got[0]), &msg))
	if msg.Kind != "request_available" || msg.Title != "Test" || msg.Body != "Frieren is available" || msg.ID == "" || msg.ItemID != "" {
		t.Errorf("message: %+v", msg)
	}
	if ttls := push.TTLs(phone); len(ttls) != 1 || ttls[0] != 86400 {
		t.Errorf("TTL: %v", ttls)
	}

	// Each profile reads in its own language, on its own devices.
	_, err = a.SetLanguage(ctx, lea, "fr")
	mustNil(t, err)
	send(lea, "Suzume")
	if got := push.Messages(tablet); len(got) != 1 || json.Unmarshal([]byte(got[0]), &msg) != nil || msg.Body != "Suzume est disponible" {
		t.Errorf("in French: %v", got)
	}
	if len(push.Messages(phone)) != 1 {
		t.Error("a notification reached the device of another profile")
	}

	// A subscription the push service no longer knows is forgotten; nothing is asked for a profile
	// without a device.
	push.Unsubscribe(phone)
	send(admin, "Monster")
	if cfg, _ := a.PushConfig(ctx, admin); cfg.Endpoint != "" {
		t.Errorf("a gone subscription was kept: %+v", cfg)
	}
	before := pendingJobs(t, a, jobPushNotification)
	a.notify(ctx, domain.Notification{ProfileID: admin.Profile.ID, Kind: domain.NotificationRequestAvailable, Text: domain.T("notification.request_available", "title", "Mushishi")})
	if pendingJobs(t, a, jobPushNotification) != before {
		t.Error("a push was asked for a profile without a subscribed device")
	}

	// Unsubscribing, or signing out, ends it.
	mustNil(t, a.UnsubscribePush(ctx, lea))
	if cfg, _ := a.PushConfig(ctx, lea); cfg.Endpoint != "" {
		t.Errorf("after unsubscribing: %+v", cfg)
	}
	mustNil(t, a.SubscribePush(ctx, lea, tablet.Subscription.Endpoint, tablet.Subscription.P256DH, tablet.Subscription.Auth))
	mustNil(t, a.Logout(ctx, lea))
	if subs, err := a.store.Read().PushSubscriptions(ctx, lea.Profile.ID, a.now()); err != nil || len(subs) != 0 {
		t.Errorf("after signing out: %+v %v", subs, err)
	}
}

// Push endpoints come from devices: the server only connects to public addresses.
func TestPushPublicOnly(t *testing.T) {
	for addr, public := range map[string]bool{
		"8.8.8.8:443":                true,
		"[2606:4700:4700::1111]:443": true,
		"127.0.0.1:443":              false,
		"10.0.0.5:443":               false,
		"192.168.1.12:8096":          false,
		"172.16.0.1:443":             false,
		"169.254.169.254:80":         false,
		"100.100.100.100:443":        false,
		"0.0.0.0:443":                false,
		"224.0.0.251:5353":           false,
		"[::1]:443":                  false,
		"[fe80::1]:443":              false,
		"[fd00::1]:443":              false,
		"[::ffff:10.0.0.5]:443":      false,
		"push.example.net:443":       false,
		"nonsense":                   false,
	} {
		if err := publicOnly("tcp", addr, nil); (err == nil) != public {
			t.Errorf("%s: %v", addr, err)
		}
	}
	// The real client refuses before anything is sent.
	if _, err := newPushClient().Get("https://127.0.0.1:1/x"); err == nil {
		t.Error("the push client connected to the loopback")
	}
}
