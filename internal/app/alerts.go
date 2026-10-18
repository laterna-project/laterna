package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/i18n"
	"github.com/laterna-project/laterna/internal/jobs"
	"github.com/laterna-project/laterna/internal/store"
)

// Alerts: entries of the activity log told to a Discord channel, an ntfy topic or a JSON webhook,
// for the whole household. Each channel is a setting (alerts.<kind>) kept in memory too, so that
// writing an entry does not read the settings. An entry worth an alert queues one job per channel
// that wants it, in the transaction that writes it; the job sends it in the server's language and
// notes how it went on the channel. A failure is tried again by the job queue, then given up; the
// failure of the job itself makes no alert, or a broken channel would feed itself.

// jobSendAlert sends an entry of the activity log to a channel ("<entry>:<channel>").
const jobSendAlert = "alert.send"

// keyAlert prefixes the setting of each channel ("alerts.discord").
const keyAlert = "alerts."

// Alert limits.
const (
	// discordMax is the most Discord takes in a message.
	discordMax = 2000
	// alertErrorBody is how much of a refusal is kept to tell why.
	alertErrorBody = 200
)

// alertChannels holds the channels in memory.
type alertChannels struct {
	mu       sync.RWMutex
	channels map[domain.AlertChannelKind]domain.AlertChannel
}

func (c *alertChannels) get(kind domain.AlertChannelKind) domain.AlertChannel {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if ch, ok := c.channels[kind]; ok {
		return ch
	}
	return domain.AlertChannel{Kind: kind}
}

func (c *alertChannels) set(ch domain.AlertChannel) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.channels == nil {
		c.channels = map[domain.AlertChannelKind]domain.AlertChannel{}
	}
	c.channels[ch.Kind] = ch
}

// loadAlerts reads the channels at startup.
func (a *App) loadAlerts(ctx context.Context) error {
	read := a.store.Read()
	for _, kind := range domain.AlertChannelKinds {
		raw, ok, err := read.Setting(ctx, keyAlert+string(kind))
		if err != nil {
			return err
		}
		ch := domain.AlertChannel{Kind: kind}
		if ok {
			if err := json.Unmarshal([]byte(raw), &ch); err != nil {
				return fmt.Errorf("setting %s%s: %w", keyAlert, kind, err)
			}
			ch.Kind = kind
		}
		a.alerts.set(ch)
	}
	return nil
}

// saveAlert writes a channel, in memory and in the settings.
func (a *App) saveAlert(ctx context.Context, ch domain.AlertChannel) error {
	raw, err := json.Marshal(ch)
	if err != nil {
		return err
	}
	if err := a.store.Write(ctx, func(q store.Q) error { return q.SetSetting(ctx, keyAlert+string(ch.Kind), string(raw)) }); err != nil {
		return err
	}
	a.alerts.set(ch)
	return nil
}

// queueAlerts queues the alerts an entry of the activity log makes, in the transaction that writes
// it. It reports whether it queued any.
func (a *App) queueAlerts(ctx context.Context, q store.Q, id int64, e domain.Activity) (bool, error) {
	cat, ok := domain.AlertOf(e)
	if !ok {
		return false, nil
	}
	queued := false
	for _, kind := range domain.AlertChannelKinds {
		if !a.alerts.get(kind).Wants(cat) {
			continue
		}
		if err := a.jobs.Enqueue(ctx, q, jobSendAlert, strconv.FormatInt(id, 10)+":"+string(kind), priorityUser); err != nil {
			return queued, err
		}
		queued = true
	}
	return queued, nil
}

// AlertChannels lists the channels, without their secrets for the caller to see (alertTarget).
func (a *App) AlertChannels() []domain.AlertChannel {
	out := make([]domain.AlertChannel, len(domain.AlertChannelKinds))
	for i, kind := range domain.AlertChannelKinds {
		out[i] = a.alerts.get(kind)
	}
	return out
}

// AlertChanges describes a change to a channel; a nil URL or token is left as it is.
type AlertChanges struct {
	Enabled    bool
	URL        *string
	Token      *string
	Categories []domain.AlertCategory
}

// SetAlertChannel sets up a channel. An empty URL removes it.
func (a *App) SetAlertChannel(ctx context.Context, p domain.Principal, kind domain.AlertChannelKind, ch AlertChanges) (domain.AlertChannel, error) {
	if !slices.Contains(domain.AlertChannelKinds, kind) {
		return domain.AlertChannel{}, domain.Invalid("alert.unknown_channel")
	}
	c := a.alerts.get(kind)
	if ch.URL != nil {
		u, err := alertURL(kind, *ch.URL)
		if err != nil {
			return domain.AlertChannel{}, err
		}
		if u != c.URL {
			// Another address: what went wrong with the previous one no longer tells anything.
			c.LastSentAt, c.LastError, c.LastErrorAt = nil, nil, nil
		}
		c.URL = u
	}
	if ch.Token != nil {
		c.Token = strings.TrimSpace(*ch.Token)
		if kind == domain.AlertDiscord {
			c.Token = ""
		}
	}
	categories := []domain.AlertCategory{}
	for _, cat := range ch.Categories {
		switch cat {
		case domain.AlertRequests, domain.AlertNewEpisodes, domain.AlertPlayback, domain.AlertProblems:
		default:
			return domain.AlertChannel{}, domain.Invalid("alert.unknown_category")
		}
		if !slices.Contains(categories, cat) {
			categories = append(categories, cat)
		}
	}
	c.Categories, c.Enabled = categories, ch.Enabled && c.URL != ""
	if c.Enabled && len(c.Categories) == 0 {
		return domain.AlertChannel{}, domain.Invalid("alert.no_category")
	}
	if c.URL == "" {
		c = domain.AlertChannel{Kind: kind}
	}
	if err := a.saveAlert(ctx, c); err != nil {
		return domain.AlertChannel{}, err
	}
	text := domain.T("activity.alert_channel_set", "actor", p.Account.Username, "channel", string(kind))
	if c.URL == "" {
		text = domain.T("activity.alert_channel_removed", "actor", p.Account.Username, "channel", string(kind))
	}
	a.log.InfoContext(ctx, "alert channel set", "channel", kind, "enabled", c.Enabled, "categories", c.Categories)
	a.record(ctx, domain.Activity{Kind: domain.ActivitySettingsUpdated, AccountID: &p.Account.ID, Text: text})
	return c, nil
}

// TestAlertChannel sends a test message to a channel and waits for it to arrive.
func (a *App) TestAlertChannel(ctx context.Context, kind domain.AlertChannelKind) error {
	if !slices.Contains(domain.AlertChannelKinds, kind) {
		return domain.Invalid("alert.unknown_channel")
	}
	c := a.alerts.get(kind)
	if c.URL == "" {
		return domain.Precondition("alert.not_set_up")
	}
	err := a.deliver(ctx, c, alertMessage{
		Category: domain.AlertProblems, Kind: "test", Text: domain.T("alert.test", "server", a.Settings().ServerName), At: a.now(),
	})
	a.noteDelivery(ctx, kind, err)
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) {
			return domain.Precondition("alert.delivery_failed", []domain.Text{de.Text()})
		}
		return err
	}
	return nil
}

// alertMessage is what a channel is sent.
type alertMessage struct {
	Category domain.AlertCategory
	Kind     string
	Text     domain.Text
	Warning  bool
	At       time.Time
	ItemID   *domain.ID
}

// sendAlert sends an entry of the activity log to a channel (job alert.send).
func (a *App) sendAlert(ctx context.Context, target string) error {
	rawID, rawKind, ok := strings.Cut(target, ":")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if !ok || err != nil {
		return jobs.Permanent(fmt.Errorf("alert target %q", target))
	}
	kind := domain.AlertChannelKind(rawKind)
	e, err := a.store.Read().ActivityEntry(ctx, id)
	if store.IsNotFound(err) {
		return nil // purged in the meantime
	}
	if err != nil {
		return err
	}
	cat, ok := domain.AlertOf(e)
	c := a.alerts.get(kind)
	if !ok || !c.Wants(cat) {
		return nil // changed in the meantime
	}
	err = a.deliver(ctx, c, alertMessage{Category: cat, Kind: string(e.Kind), Text: e.Text, Warning: e.Warning, At: e.At, ItemID: e.ItemID})
	a.noteDelivery(ctx, kind, err)
	var refused *refusedError
	if errors.As(err, &refused) && refused.permanent() {
		return jobs.Permanent(err)
	}
	return err
}

// noteDelivery notes on the channel how a message went.
func (a *App) noteDelivery(ctx context.Context, kind domain.AlertChannelKind, err error) {
	c := a.alerts.get(kind)
	now := a.now()
	if err == nil {
		c.LastSentAt, c.LastError, c.LastErrorAt = &now, nil, nil
	} else {
		text := domain.T("error.alert.unreachable")
		var de *domain.Error
		if errors.As(err, &de) {
			text = de.Text()
		}
		c.LastError, c.LastErrorAt = &text, &now
	}
	if err := a.saveAlert(context.WithoutCancel(ctx), c); err != nil {
		a.log.WarnContext(ctx, "alerts: cannot note the delivery", "channel", kind, "err", err)
	}
}

// refusedError is a channel that answered with an error status.
type refusedError struct {
	status int
	body   string
}

func (e *refusedError) Error() string {
	return fmt.Sprintf("alert refused with HTTP %d: %s", e.status, e.body)
}

// permanent reports a refusal that trying again will not change: the address or the token is
// wrong.
func (e *refusedError) permanent() bool {
	return e.status >= 400 && e.status < 500 && e.status != http.StatusTooManyRequests && e.status != http.StatusRequestTimeout
}

// deliver sends a message to a channel. Errors are domain errors that tell why (shown on the
// channel), wrapping what happened.
func (a *App) deliver(ctx context.Context, c domain.AlertChannel, m alertMessage) error {
	server := a.Server()
	text := i18n.Render(a.Language(), m.Text)
	var body []byte
	var err error
	header := http.Header{"Content-Type": {"application/json"}, "User-Agent": {"Laterna"}}
	address := c.URL
	switch c.Kind {
	case domain.AlertDiscord:
		if len([]rune(text)) > discordMax {
			text = string([]rune(text)[:discordMax-1]) + "…"
		}
		// No mention of anyone, whatever a title holds ("@everyone").
		body, err = json.Marshal(map[string]any{
			"username": server.Name, "content": text, "allowed_mentions": map[string]any{"parse": []string{}},
		})
	case domain.AlertNtfy:
		base, topic := ntfyTopic(c.URL)
		address = base
		priority := 3
		if m.Category == domain.AlertProblems {
			priority = 4
		}
		body, err = json.Marshal(map[string]any{
			"topic": topic, "title": server.Name, "message": text, "tags": []string{ntfyTags[m.Category]}, "priority": priority,
		})
		if c.Token != "" {
			header.Set("Authorization", "Bearer "+c.Token)
		}
	case domain.AlertWebhook:
		msg := map[string]any{
			"server": map[string]string{"id": server.ID.String(), "name": server.Name}, "category": string(m.Category), "kind": m.Kind,
			"text": text, "key": m.Text.Key, "params": m.Text.Params, "warning": m.Warning, "at": m.At.UTC().Format(time.RFC3339),
		}
		if m.ItemID != nil {
			msg["item_id"] = m.ItemID.String()
		}
		body, err = json.Marshal(msg)
		if c.Token != "" {
			mac := hmac.New(sha256.New, []byte(c.Token))
			mac.Write(body)
			header.Set("X-Laterna-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		}
		header.Set("X-Laterna-Event", string(m.Category))
	}
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header = header
	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", domain.Precondition("alert.unreachable"), err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, alertErrorBody))
		refused := &refusedError{status: resp.StatusCode, body: strings.TrimSpace(strings.ToValidUTF8(string(b), "�"))}
		return fmt.Errorf("%w: %w", domain.Precondition("alert.refused", "status", resp.StatusCode), refused)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return nil
}

// ntfyTags gives each category its ntfy tag, shown as an emoji.
var ntfyTags = map[domain.AlertCategory]string{
	domain.AlertRequests:    "inbox_tray",
	domain.AlertNewEpisodes: "tv",
	domain.AlertPlayback:    "arrow_forward",
	domain.AlertProblems:    "warning",
}

var (
	ntfyTopicName = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)
	discordHosts  = []string{"discord.com", "discordapp.com", "ptb.discord.com", "canary.discord.com"}
)

// ntfyTopic splits the URL of an ntfy topic into the server and the topic.
func ntfyTopic(raw string) (base, topic string) {
	i := strings.LastIndex(raw, "/")
	if i < 0 {
		return raw, ""
	}
	return raw[:i], raw[i+1:]
}

// alertURL checks the address of a channel: http or https and a host; a Discord webhook of Discord;
// an ntfy topic at the end of the path. Empty is valid: it removes the channel.
func alertURL(kind domain.AlertChannelKind, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", domain.Invalid("alert.invalid_url")
	}
	switch kind {
	case domain.AlertDiscord:
		if u.Scheme != "https" || !slices.Contains(discordHosts, strings.ToLower(u.Hostname())) || !strings.HasPrefix(u.Path, "/api/webhooks/") {
			return "", domain.Invalid("alert.invalid_discord_url")
		}
	case domain.AlertNtfy:
		_, topic := ntfyTopic(strings.TrimRight(u.Path, "/"))
		if u.RawQuery != "" || !ntfyTopicName.MatchString(topic) {
			return "", domain.Invalid("alert.invalid_ntfy_url")
		}
		u.Path = strings.TrimRight(u.Path, "/")
	case domain.AlertWebhook:
	}
	return u.String(), nil
}

// alertTarget shows where a channel goes without its secret: a Discord webhook up to its number,
// a webhook up to its host, the topic of ntfy (a topic is no secret with an access token, and on
// ntfy.sh its name is the only one).
func alertTarget(c domain.AlertChannel) string {
	u, err := url.Parse(c.URL)
	if err != nil || c.URL == "" {
		return ""
	}
	switch c.Kind {
	case domain.AlertDiscord:
		parts := strings.Split(strings.TrimPrefix(u.Path, "/api/webhooks/"), "/")
		return u.Host + "/api/webhooks/" + parts[0] + "/…"
	case domain.AlertNtfy:
		return u.Host + u.Path
	case domain.AlertWebhook:
		if u.Path == "" || u.Path == "/" {
			return u.Host
		}
		return u.Host + "/…"
	}
	return ""
}

// AlertTarget is alertTarget, for the API layer.
func AlertTarget(c domain.AlertChannel) string { return alertTarget(c) }
