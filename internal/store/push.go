package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Web push subscriptions (docs/design/notifications.md): one per device, kept with its session.
// Keys are stored as base64url (only the server's own IDs are BLOBs).

// SetPushSubscription stores the subscription of a device, in place of the one it had. An endpoint
// belongs to one device: a browser that signs in again takes it with it.
func (q Q) SetPushSubscription(ctx context.Context, s domain.PushSubscription, now time.Time) error {
	if _, err := q.q.DeletePushEndpoint(ctx, s.Endpoint); err != nil {
		return err
	}
	return q.q.UpsertPushSubscription(ctx, sqlc.UpsertPushSubscriptionParams{
		SessionID: s.SessionID, Endpoint: s.Endpoint, P256dh: b64url.EncodeToString(s.P256DH),
		Auth: b64url.EncodeToString(s.Auth), CreatedAt: toMillis(now),
	})
}

// DeletePushSubscription forgets the subscription of a device; false if it had none.
func (q Q) DeletePushSubscription(ctx context.Context, sessionID domain.ID) (bool, error) {
	n, err := q.q.DeletePushSubscription(ctx, sessionID)
	return n > 0, err
}

// DeletePushEndpoint forgets a subscription by its endpoint (the push service no longer knows it).
func (q Q) DeletePushEndpoint(ctx context.Context, endpoint string) error {
	_, err := q.q.DeletePushEndpoint(ctx, endpoint)
	return err
}

// PushEndpoint returns the endpoint a device is subscribed with; ok is false if it is not.
func (q Q) PushEndpoint(ctx context.Context, sessionID domain.ID) (endpoint string, ok bool, err error) {
	endpoint, err = q.q.GetPushEndpoint(ctx, sessionID)
	if IsNotFound(err) {
		return "", false, nil
	}
	return endpoint, err == nil, err
}

// PushSubscriptions lists the subscriptions of the devices a profile is picked on, whose session
// has not expired.
func (q Q) PushSubscriptions(ctx context.Context, profileID domain.ID, now time.Time) ([]domain.PushSubscription, error) {
	rows, err := q.q.ProfilePushSubscriptions(ctx, sqlc.ProfilePushSubscriptionsParams{ProfileID: &profileID, ExpiresAt: toMillis(now)})
	if err != nil {
		return nil, err
	}
	out := make([]domain.PushSubscription, 0, len(rows))
	for _, r := range rows {
		key, err := b64url.DecodeString(r.P256dh)
		if err != nil {
			return nil, fmt.Errorf("push subscription of %s: %w", r.SessionID, err)
		}
		auth, err := b64url.DecodeString(r.Auth)
		if err != nil {
			return nil, fmt.Errorf("push subscription of %s: %w", r.SessionID, err)
		}
		out = append(out, domain.PushSubscription{SessionID: r.SessionID, Endpoint: r.Endpoint, P256DH: key, Auth: auth})
	}
	return out, nil
}

// Notification reads a notification.
func (q Q) Notification(ctx context.Context, id domain.ID) (domain.Notification, error) {
	r, err := q.q.GetNotification(ctx, id)
	if err != nil {
		return domain.Notification{}, err
	}
	n := domain.Notification{
		ID: r.ID, ProfileID: r.ProfileID, Kind: domain.NotificationKind(r.Kind), ItemID: r.ItemID, RequestID: r.RequestID,
		CreatedAt: fromMillis(r.CreatedAt),
	}
	if err := json.Unmarshal([]byte(r.Text), &n.Text); err != nil {
		return domain.Notification{}, fmt.Errorf("notification %s: %w", n.ID, err)
	}
	if r.ReadAt.Valid {
		at := fromMillis(r.ReadAt.Int64)
		n.ReadAt = &at
	}
	return n, nil
}
