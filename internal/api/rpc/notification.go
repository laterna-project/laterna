package rpc

import (
	"context"
	"encoding/base64"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// NotificationService implements laterna.v1.NotificationService.
type NotificationService struct {
	app *app.App
}

var notificationKinds = map[domain.NotificationKind]laternav1.NotificationKind{
	domain.NotificationRequestPending:   laternav1.NotificationKind_NOTIFICATION_KIND_REQUEST_PENDING,
	domain.NotificationRequestApproved:  laternav1.NotificationKind_NOTIFICATION_KIND_REQUEST_APPROVED,
	domain.NotificationRequestDeclined:  laternav1.NotificationKind_NOTIFICATION_KIND_REQUEST_DECLINED,
	domain.NotificationRequestAvailable: laternav1.NotificationKind_NOTIFICATION_KIND_REQUEST_AVAILABLE,
	domain.NotificationRequestFailed:    laternav1.NotificationKind_NOTIFICATION_KIND_REQUEST_FAILED,
	domain.NotificationNewEpisodes:      laternav1.NotificationKind_NOTIFICATION_KIND_NEW_EPISODES,
	domain.NotificationRequestChoose:    laternav1.NotificationKind_NOTIFICATION_KIND_REQUEST_CHOOSE,
}

func notificationMsg(ctx context.Context, n app.NotificationView) *laternav1.Notification {
	msg := &laternav1.Notification{
		Id: n.ID.String(), Kind: notificationKinds[n.Kind], Summary: render(ctx, n.Text), Text: textMsg(ctx, n.Text),
		CreatedAt: timestamppb.New(n.CreatedAt), Read: n.ReadAt != nil,
		RequestId: idString(n.RequestID), PosterUrl: app.RequestPosterPath(n.Poster),
	}
	if n.Item != nil {
		msg.Item = searchResultMsg(ctx, *n.Item)
	}
	return msg
}

// ListNotifications lists the notifications of the profile.
func (s *NotificationService) ListNotifications(ctx context.Context, req *connect.Request[laternav1.ListNotificationsRequest]) (*connect.Response[laternav1.ListNotificationsResponse], error) {
	page, err := s.app.Notifications(ctx, principal(ctx), req.Msg.GetPageToken(), int(req.Msg.GetPageSize()))
	if err != nil {
		return nil, err
	}
	out := &laternav1.ListNotificationsResponse{NextPageToken: page.NextPageToken, UnreadCount: clampInt32(page.Unread)}
	for _, n := range page.Notifications {
		out.Notifications = append(out.Notifications, notificationMsg(ctx, n))
	}
	return connect.NewResponse(out), nil
}

// MarkNotificationsRead marks notifications of the profile as read.
func (s *NotificationService) MarkNotificationsRead(ctx context.Context, req *connect.Request[laternav1.MarkNotificationsReadRequest]) (*connect.Response[laternav1.MarkNotificationsReadResponse], error) {
	ids, err := parseIDs(req.Msg.GetNotificationIds(), "notification_ids")
	if err != nil {
		return nil, err
	}
	if err := s.app.MarkNotificationsRead(ctx, principal(ctx), ids, req.Msg.GetAll()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.MarkNotificationsReadResponse{}), nil
}

// DeleteNotifications removes notifications of the profile.
func (s *NotificationService) DeleteNotifications(ctx context.Context, req *connect.Request[laternav1.DeleteNotificationsRequest]) (*connect.Response[laternav1.DeleteNotificationsResponse], error) {
	ids, err := parseIDs(req.Msg.GetNotificationIds(), "notification_ids")
	if err != nil {
		return nil, err
	}
	if err := s.app.DeleteNotifications(ctx, principal(ctx), ids, req.Msg.GetAll()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.DeleteNotificationsResponse{}), nil
}

// GetPushConfig returns the server's push key and the subscription of this device.
func (s *NotificationService) GetPushConfig(ctx context.Context, _ *connect.Request[laternav1.GetPushConfigRequest]) (*connect.Response[laternav1.GetPushConfigResponse], error) {
	cfg, err := s.app.PushConfig(ctx, principal(ctx))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.GetPushConfigResponse{PublicKey: cfg.PublicKey, Endpoint: cfg.Endpoint}), nil
}

// SubscribePush stores the push subscription of this device.
func (s *NotificationService) SubscribePush(ctx context.Context, req *connect.Request[laternav1.SubscribePushRequest]) (*connect.Response[laternav1.SubscribePushResponse], error) {
	m := req.Msg
	// Browsers write these with or without padding.
	key, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(m.GetP256Dh(), "="))
	if err != nil {
		return nil, domain.Invalid("notification.invalid_subscription")
	}
	auth, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(m.GetAuth(), "="))
	if err != nil {
		return nil, domain.Invalid("notification.invalid_subscription")
	}
	if err := s.app.SubscribePush(ctx, principal(ctx), m.GetEndpoint(), key, auth); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.SubscribePushResponse{}), nil
}

// UnsubscribePush forgets the push subscription of this device.
func (s *NotificationService) UnsubscribePush(ctx context.Context, _ *connect.Request[laternav1.UnsubscribePushRequest]) (*connect.Response[laternav1.UnsubscribePushResponse], error) {
	if err := s.app.UnsubscribePush(ctx, principal(ctx)); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.UnsubscribePushResponse{}), nil
}

var alertChannelKinds = map[domain.AlertChannelKind]laternav1.AlertChannelKind{
	domain.AlertDiscord: laternav1.AlertChannelKind_ALERT_CHANNEL_KIND_DISCORD,
	domain.AlertNtfy:    laternav1.AlertChannelKind_ALERT_CHANNEL_KIND_NTFY,
	domain.AlertWebhook: laternav1.AlertChannelKind_ALERT_CHANNEL_KIND_WEBHOOK,
}

var alertCategories = map[domain.AlertCategory]laternav1.AlertCategory{
	domain.AlertRequests:    laternav1.AlertCategory_ALERT_CATEGORY_REQUESTS,
	domain.AlertNewEpisodes: laternav1.AlertCategory_ALERT_CATEGORY_NEW_EPISODES,
	domain.AlertPlayback:    laternav1.AlertCategory_ALERT_CATEGORY_PLAYBACK,
	domain.AlertProblems:    laternav1.AlertCategory_ALERT_CATEGORY_PROBLEMS,
}

func alertKindFromMsg(k laternav1.AlertChannelKind) domain.AlertChannelKind {
	for d, m := range alertChannelKinds {
		if m == k {
			return d
		}
	}
	return ""
}

func alertChannelMsg(ctx context.Context, c domain.AlertChannel) *laternav1.AlertChannel {
	msg := &laternav1.AlertChannel{
		Kind: alertChannelKinds[c.Kind], Enabled: c.Enabled, Target: app.AlertTarget(c), HasToken: c.Token != "",
	}
	for _, cat := range c.Categories {
		msg.Categories = append(msg.Categories, alertCategories[cat])
	}
	if c.LastSentAt != nil {
		msg.LastSentAt = timestamppb.New(*c.LastSentAt)
	}
	if c.LastError != nil {
		msg.LastError = textMsg(ctx, *c.LastError)
	}
	if c.LastErrorAt != nil {
		msg.LastErrorAt = timestamppb.New(*c.LastErrorAt)
	}
	return msg
}

// ListAlertChannels lists the alert channels.
func (s *NotificationService) ListAlertChannels(ctx context.Context, _ *connect.Request[laternav1.ListAlertChannelsRequest]) (*connect.Response[laternav1.ListAlertChannelsResponse], error) {
	resp := &laternav1.ListAlertChannelsResponse{}
	for _, c := range s.app.AlertChannels() {
		resp.Channels = append(resp.Channels, alertChannelMsg(ctx, c))
	}
	return connect.NewResponse(resp), nil
}

// SetAlertChannel sets up an alert channel.
func (s *NotificationService) SetAlertChannel(ctx context.Context, req *connect.Request[laternav1.SetAlertChannelRequest]) (*connect.Response[laternav1.SetAlertChannelResponse], error) {
	m := req.Msg
	ch := app.AlertChanges{Enabled: m.GetEnabled(), URL: m.Url, Token: m.Token}
	for _, cat := range m.GetCategories() {
		found := domain.AlertCategory("unknown")
		for d, v := range alertCategories {
			if v == cat {
				found = d
			}
		}
		ch.Categories = append(ch.Categories, found)
	}
	c, err := s.app.SetAlertChannel(ctx, principal(ctx), alertKindFromMsg(m.GetKind()), ch)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.SetAlertChannelResponse{Channel: alertChannelMsg(ctx, c)}), nil
}

// TestAlertChannel sends a test message to an alert channel.
func (s *NotificationService) TestAlertChannel(ctx context.Context, req *connect.Request[laternav1.TestAlertChannelRequest]) (*connect.Response[laternav1.TestAlertChannelResponse], error) {
	if err := s.app.TestAlertChannel(ctx, alertKindFromMsg(req.Msg.GetKind())); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.TestAlertChannelResponse{}), nil
}
