package domain

import "time"

// NotificationKind says what a notification is about.
type NotificationKind string

// Kinds of notifications.
const (
	// A request waits for an administrator: sent to the administrators.
	NotificationRequestPending NotificationKind = "request_pending"
	// What became of a request: sent to the profile that made it.
	NotificationRequestApproved  NotificationKind = "request_approved"
	NotificationRequestDeclined  NotificationKind = "request_declined"
	NotificationRequestAvailable NotificationKind = "request_available"
	// A request could not be handed to its source: sent to its profile and to the administrators.
	NotificationRequestFailed NotificationKind = "request_failed"
	// Episodes arrived for a series the profile follows.
	NotificationNewEpisodes NotificationKind = "new_episodes"
)

// Notification tells a profile about something that happened while it was not looking.
type Notification struct {
	ID        ID
	ProfileID ID
	Kind      NotificationKind
	// Text is a "notification...." text.
	Text Text
	// ItemID is what to open: the episode or the series that arrived, the title a request got.
	ItemID *ID
	// RequestID is the request it is about.
	RequestID *ID
	CreatedAt time.Time
	// ReadAt is nil until the profile has seen it.
	ReadAt *time.Time
}

// PushSubscription is where to reach a device that asked for notifications while the app is
// closed: the address its browser's push service gave it, and the keys to encrypt for it.
type PushSubscription struct {
	// SessionID is the device.
	SessionID ID
	Endpoint  string
	// P256DH is the public key of the device, Auth its authentication secret.
	P256DH []byte
	Auth   []byte
}
