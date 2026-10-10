package rpc

import (
	"context"

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
