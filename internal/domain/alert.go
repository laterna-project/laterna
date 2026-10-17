package domain

import (
	"slices"
	"time"
)

// AlertChannelKind is where alerts go: entries of the activity log told to the household outside
// the apps.
type AlertChannelKind string

// Alert channels.
const (
	AlertDiscord AlertChannelKind = "discord"
	AlertNtfy    AlertChannelKind = "ntfy"
	AlertWebhook AlertChannelKind = "webhook"
)

// AlertChannelKinds lists the channels, in the order they are shown.
var AlertChannelKinds = []AlertChannelKind{AlertDiscord, AlertNtfy, AlertWebhook}

// AlertCategory says what an alert is about.
type AlertCategory string

// Alert categories.
const (
	AlertRequests    AlertCategory = "requests"
	AlertNewEpisodes AlertCategory = "new_episodes"
	AlertPlayback    AlertCategory = "playback"
	AlertProblems    AlertCategory = "problems"
)

// AlertChannel is a channel an administrator set up. URL and Token are secrets: a Discord webhook
// URL is enough to post in the channel.
type AlertChannel struct {
	Kind    AlertChannelKind
	Enabled bool
	// URL is the Discord webhook, the ntfy topic or the address the webhook POSTs to.
	URL string
	// Token is the access token of ntfy, or the secret that signs the bodies of the webhook.
	Token      string
	Categories []AlertCategory
	// How the last messages went: the last one delivered, the last failure.
	LastSentAt  *time.Time
	LastError   *Text
	LastErrorAt *time.Time
}

// Wants reports whether the channel takes alerts of a category.
func (c AlertChannel) Wants(cat AlertCategory) bool {
	return c.Enabled && c.URL != "" && slices.Contains(c.Categories, cat)
}

// AlertOf gives the category an activity entry is alerted in; false if it is not one.
func AlertOf(e Activity) (AlertCategory, bool) {
	switch {
	case e.Kind == ActivityRequest:
		return AlertRequests, true
	case e.Kind == ActivityEpisodesArrived:
		return AlertNewEpisodes, true
	case e.Kind == ActivityPlaybackStarted:
		return AlertPlayback, true
	case e.Warning:
		return AlertProblems, true
	}
	return "", false
}
