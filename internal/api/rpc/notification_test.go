package rpc

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/arr"
	"github.com/laterna-project/laterna/internal/arr/arrtest"
)

// Notifications over HTTP: a request that waits is announced to the administrator, who reads it and
// deletes it.
func TestNotificationsOverHTTP(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	token := setup(t, s)
	sonarr := arrtest.New(t, arr.Sonarr)
	sonarr.Set(func(srv *arrtest.Server) {
		srv.Catalog = []arrtest.Entry{{ExternalID: 101, Title: "Frieren", Year: 2023, Seasons: []int{1}, Poster: "https://artworks.example/frieren.jpg"}}
	})
	integrations := laternav1connect.NewIntegrationServiceClient(http.DefaultClient, s.url)
	if _, err := integrations.SetIntegration(ctx, withToken(&laternav1.SetIntegrationRequest{
		Kind: laternav1.IntegrationKind_INTEGRATION_KIND_SONARR, Url: sonarr.URL, ApiKey: arrtest.Key,
	}, token)); err != nil {
		t.Fatal(err)
	}
	lib, err := s.library.CreateLibrary(ctx, withToken(&laternav1.CreateLibraryRequest{
		Name: "Anime", Kind: laternav1.LibraryKind_LIBRARY_KIND_SHOWS, Paths: []string{t.TempDir()},
	}, token))
	if err != nil {
		t.Fatal(err)
	}
	requests := laternav1connect.NewRequestServiceClient(http.DefaultClient, s.url)
	if _, err := requests.CreateRequestDestination(ctx, withToken(&laternav1.CreateRequestDestinationRequest{
		Name: "Anime", Kind: laternav1.RequestKind_REQUEST_KIND_SERIES, LibraryId: lib.Msg.GetLibrary().GetId(),
		RootFolder: "/data/media/shows", QualityProfileId: 7,
	}, token)); err != nil {
		t.Fatal(err)
	}
	accounts := laternav1connect.NewAccountServiceClient(http.DefaultClient, s.url)
	if _, err := accounts.CreateAccount(ctx, withToken(&laternav1.CreateAccountRequest{Username: "Léa", Password: "a-password"}, token)); err != nil {
		t.Fatal(err)
	}
	login, err := s.auth.Login(ctx, connect.NewRequest(&laternav1.LoginRequest{
		Username: "Léa", Password: "a-password", Device: &laternav1.Device{Name: "Phone", Client: "Tests", ClientVersion: "1", Platform: "Go"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	user := login.Msg.GetToken()
	made, err := requests.CreateRequest(ctx, withToken(&laternav1.CreateRequestRequest{Kind: laternav1.RequestKind_REQUEST_KIND_SERIES, ExternalId: 101}, user))
	if err != nil {
		t.Fatal(err)
	}

	notifications := laternav1connect.NewNotificationServiceClient(http.DefaultClient, s.url)
	list := func(token string) *laternav1.ListNotificationsResponse {
		t.Helper()
		resp, err := notifications.ListNotifications(ctx, withToken(&laternav1.ListNotificationsRequest{}, token))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg
	}
	if _, err := notifications.ListNotifications(ctx, connect.NewRequest(&laternav1.ListNotificationsRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Errorf("without a token: %v", err)
	}
	if got := list(user); len(got.GetNotifications()) != 0 || got.GetUnreadCount() != 0 {
		t.Errorf("requester: %v", got)
	}
	got := list(token)
	if len(got.GetNotifications()) != 1 || got.GetUnreadCount() != 1 || got.GetNextPageToken() != "" {
		t.Fatalf("administrator: %v", got)
	}
	n := got.GetNotifications()[0]
	if n.GetKind() != laternav1.NotificationKind_NOTIFICATION_KIND_REQUEST_PENDING || n.GetSummary() != "Léa asks for Frieren" ||
		n.GetText().GetKey() != "notification.request_pending" || n.GetText().GetParams()["title"] != "Frieren" || n.GetRead() ||
		n.GetRequestId() != made.Msg.GetRequest().GetId() || !strings.HasPrefix(n.GetPosterUrl(), "/requests/posters/") ||
		n.GetItem() != nil || n.GetCreatedAt() == nil {
		t.Errorf("notification: %v", n)
	}

	// Read, then deleted. An ID that is not one is refused.
	if _, err := notifications.MarkNotificationsRead(ctx, withToken(&laternav1.MarkNotificationsReadRequest{All: true}, token)); err != nil {
		t.Fatal(err)
	}
	if got := list(token); got.GetUnreadCount() != 0 || !got.GetNotifications()[0].GetRead() {
		t.Errorf("after reading: %v", got)
	}
	if _, err := notifications.DeleteNotifications(ctx, withToken(&laternav1.DeleteNotificationsRequest{NotificationIds: []string{"nope"}}, token)); code(err) != connect.CodeInvalidArgument {
		t.Errorf("invalid ID: %v", err)
	}
	if _, err := notifications.DeleteNotifications(ctx, withToken(&laternav1.DeleteNotificationsRequest{NotificationIds: []string{n.GetId()}}, token)); err != nil {
		t.Fatal(err)
	}
	if got := list(token); len(got.GetNotifications()) != 0 {
		t.Errorf("after deleting: %v", got)
	}
}
