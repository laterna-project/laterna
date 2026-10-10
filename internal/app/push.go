package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/i18n"
	"github.com/laterna-project/laterna/internal/jobs"
	"github.com/laterna-project/laterna/internal/store"
	"github.com/laterna-project/laterna/internal/webpush"
)

// Web push (docs/design/notifications.md): a notification also goes to the devices that asked to be
// told when the app is closed. Each message is encrypted for its device; the push service of the
// browser only carries it.
const (
	// keyPushKey holds the key pair the server names itself with to push services. It is created
	// once: browsers subscribe for it.
	keyPushKey = "push.vapid_key"
	// jobPushNotification sends a notification to the devices of its profile.
	jobPushNotification = "notification.push"
	// classPush: short calls to push services, two at a time.
	classPush = "push"
	// pushTTL is how long a push service keeps a message for a device that is offline.
	pushTTL = 24 * time.Hour
	// pushSubject is who push services may contact about this server when it has no public address.
	pushSubject = "https://github.com/laterna-project/laterna"
)

// PushConfig is what a device needs to subscribe, and where it stands.
type PushConfig struct {
	// PublicKey is the server's key as browsers take it (applicationServerKey).
	PublicKey string
	// Endpoint is the endpoint this device is subscribed with; empty if it is not.
	Endpoint string
}

// loadPushKeys reads the server's push key pair, created at the first start.
func (a *App) loadPushKeys(ctx context.Context) error {
	fresh, err := webpush.NewKeys()
	if err != nil {
		return err
	}
	if err := a.store.Write(ctx, func(q store.Q) error { return q.InitSetting(ctx, keyPushKey, fresh.String()) }); err != nil {
		return err
	}
	raw, _, err := a.store.Read().Setting(ctx, keyPushKey)
	if err != nil {
		return err
	}
	if a.pushKeys, err = webpush.ParseKeys(raw); err != nil {
		return fmt.Errorf("corrupt push key: %w", err)
	}
	return nil
}

// publicOnly refuses to connect anywhere but a public address. Push endpoints come from devices:
// without this, a signed-in user could make the server post to the local network. It runs on the
// address actually dialed, so a name that resolves to a private address is refused too.
func publicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	ip = ip.Unmap()
	shared := netip.MustParsePrefix("100.64.0.0/10") // carrier-grade NAT, VPN meshes
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || shared.Contains(ip) {
		return fmt.Errorf("push: %s is not a public address", ip)
	}
	return nil
}

// newPushClient is the HTTP client for push services: public addresses only, no redirect, no proxy
// (its address could not be checked).
func newPushClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: publicOnly}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second, MaxIdleConns: 8, IdleConnTimeout: time.Minute,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// PushConfig returns the server's push key and the subscription of the caller's device.
func (a *App) PushConfig(ctx context.Context, p domain.Principal) (PushConfig, error) {
	endpoint, _, err := a.store.Read().PushEndpoint(ctx, p.SessionID)
	if err != nil {
		return PushConfig{}, err
	}
	return PushConfig{PublicKey: a.pushKeys.PublicKey(), Endpoint: endpoint}, nil
}

// SubscribePush stores the push subscription of the caller's device, in place of the one it had.
// The device then gets the notifications of the profile picked on it.
func (a *App) SubscribePush(ctx context.Context, p domain.Principal, endpoint string, key, auth []byte) error {
	if p.Profile == nil {
		return domain.Precondition("profile.required")
	}
	sub := webpush.Subscription{Endpoint: strings.TrimSpace(endpoint), P256DH: key, Auth: auth}
	if err := sub.Valid(); err != nil {
		return domain.Invalid("notification.invalid_subscription")
	}
	return a.store.Write(ctx, func(q store.Q) error {
		return q.SetPushSubscription(ctx, domain.PushSubscription{
			SessionID: p.SessionID, Endpoint: sub.Endpoint, P256DH: sub.P256DH, Auth: sub.Auth,
		}, a.now())
	})
}

// UnsubscribePush forgets the push subscription of the caller's device.
func (a *App) UnsubscribePush(ctx context.Context, p domain.Principal) error {
	return a.store.Write(ctx, func(q store.Q) error {
		_, err := q.DeletePushSubscription(ctx, p.SessionID)
		return err
	})
}

// pushMessage is what a device receives: enough to show a notification and to open what it is
// about. The text is written in the language of the profile.
type pushMessage struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Body  string `json:"body"`
	// ItemID and RequestID say what to open; empty when there is none.
	ItemID    string `json:"item_id,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// pushNotification sends a notification to the devices its profile is picked on (job
// notification.push). Push is a courtesy on top of the list: a device that cannot be reached is
// logged and not tried again, and one the push service no longer knows is forgotten.
func (a *App) pushNotification(ctx context.Context, target string) error {
	id, err := domain.ParseID(target)
	if err != nil {
		return jobs.Permanent(err)
	}
	read := a.store.Read()
	n, err := read.Notification(ctx, id)
	if store.IsNotFound(err) {
		return nil // deleted in the meantime
	}
	if err != nil {
		return err
	}
	subs, err := read.PushSubscriptions(ctx, n.ProfileID, a.now())
	if err != nil || len(subs) == 0 {
		return err
	}
	lang := a.Language()
	if profile, _, err := read.Profile(ctx, n.ProfileID); err == nil {
		if l, ok := i18n.Parse(profile.Language); ok {
			lang = l
		}
	}
	settings := a.Settings()
	msg := pushMessage{ID: n.ID.String(), Kind: string(n.Kind), Title: settings.ServerName, Body: i18n.Render(lang, n.Text)}
	if n.ItemID != nil {
		msg.ItemID = n.ItemID.String()
	}
	if n.RequestID != nil {
		msg.RequestID = n.RequestID.String()
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return jobs.Permanent(err)
	}
	client := webpush.Client{HTTP: a.pushHTTP, Keys: a.pushKeys, Subject: pushSubject, Now: a.now}
	if strings.HasPrefix(settings.PublicURL, "https://") {
		client.Subject = settings.PublicURL
	}
	for _, s := range subs {
		err := client.Send(ctx, webpush.Subscription{Endpoint: s.Endpoint, P256DH: s.P256DH, Auth: s.Auth}, payload, webpush.Options{TTL: pushTTL})
		switch {
		case err == nil:
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, webpush.ErrGone):
			if err := a.store.Write(ctx, func(q store.Q) error { return q.DeletePushEndpoint(ctx, s.Endpoint) }); err != nil {
				return err
			}
			a.log.InfoContext(ctx, "push: subscription gone, forgotten", "session", s.SessionID)
		default:
			a.log.WarnContext(ctx, "push: message not delivered", "session", s.SessionID, "err", err)
		}
	}
	return nil
}
