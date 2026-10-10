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

// Requests over HTTP: a destination set by the administrator, a request from a user within their
// quota, then approved.
func TestRequestsOverHTTP(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	token := setup(t, s)
	sonarr := arrtest.New(t, arr.Sonarr)
	sonarr.Set(func(srv *arrtest.Server) {
		srv.Catalog = []arrtest.Entry{
			{ExternalID: 101, Title: "Frieren", Year: 2023, Seasons: []int{1, 2}, Poster: "https://artworks.example/frieren.jpg"},
			{ExternalID: 102, Title: "Mushishi", Year: 2005, Seasons: []int{1}},
		}
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
	opts, err := requests.GetRequestOptions(ctx, withToken(&laternav1.GetRequestOptionsRequest{Kind: laternav1.RequestKind_REQUEST_KIND_SERIES}, token))
	if err != nil || len(opts.Msg.GetRootFolders()) != 1 || len(opts.Msg.GetQualityProfiles()) != 2 {
		t.Fatalf("options: %v %v", opts, err)
	}
	dest, err := requests.CreateRequestDestination(ctx, withToken(&laternav1.CreateRequestDestinationRequest{
		Name: "Anime", Kind: laternav1.RequestKind_REQUEST_KIND_SERIES, LibraryId: lib.Msg.GetLibrary().GetId(),
		RootFolder: opts.Msg.GetRootFolders()[0].GetPath(), QualityProfileId: 7,
		SeriesType: laternav1.RequestSeriesType_REQUEST_SERIES_TYPE_ANIME,
	}, token))
	if err != nil {
		t.Fatal(err)
	}
	if d := dest.Msg.GetDestination(); d.GetQualityProfileName() != "HD Bluray + WEB" || d.GetSeriesType() != laternav1.RequestSeriesType_REQUEST_SERIES_TYPE_ANIME {
		t.Errorf("destination: %v", d)
	}

	// A user with one request a week.
	accounts := laternav1connect.NewAccountServiceClient(http.DefaultClient, s.url)
	one := int32(1)
	created, err := accounts.CreateAccount(ctx, withToken(&laternav1.CreateAccountRequest{Username: "Léa", Password: "a-password", RequestQuota: &one}, token))
	if err != nil {
		t.Fatal(err)
	}
	if a := created.Msg.GetAccount(); a.GetRequestQuota() != 1 || a.GetDenyRequests() || a.GetAutoApproveRequests() {
		t.Errorf("account: %v", a)
	}
	login, err := s.auth.Login(ctx, connect.NewRequest(&laternav1.LoginRequest{
		Username: "Léa", Password: "a-password", Device: &laternav1.Device{Name: "Phone", Client: "Tests", ClientVersion: "1", Platform: "Go"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	user := login.Msg.GetToken()

	found, err := requests.SearchRequestable(ctx, withToken(&laternav1.SearchRequestableRequest{Kind: laternav1.RequestKind_REQUEST_KIND_SERIES, Query: "frieren"}, user))
	if err != nil || len(found.Msg.GetResults()) != 1 {
		t.Fatalf("search: %v %v", found, err)
	}
	r := found.Msg.GetResults()[0]
	if r.GetState() != laternav1.RequestableState_REQUESTABLE_STATE_REQUESTABLE || r.GetSeasonCount() != 2 || !strings.HasPrefix(r.GetPosterUrl(), "/requests/posters/") {
		t.Errorf("result: %v", r)
	}
	made, err := requests.CreateRequest(ctx, withToken(&laternav1.CreateRequestRequest{Kind: laternav1.RequestKind_REQUEST_KIND_SERIES, ExternalId: 101}, user))
	if err != nil {
		t.Fatal(err)
	}
	req := made.Msg.GetRequest()
	if req.GetStatus() != laternav1.RequestStatus_REQUEST_STATUS_PENDING || req.GetSeasons() != laternav1.RequestSeasons_REQUEST_SEASONS_ALL ||
		req.GetDestination().GetName() != "Anime" || req.GetDestination().GetRootFolder() != "" || req.GetProfileName() != "Léa" {
		t.Errorf("request: %v", req)
	}
	if _, err := requests.CreateRequest(ctx, withToken(&laternav1.CreateRequestRequest{Kind: laternav1.RequestKind_REQUEST_KIND_SERIES, ExternalId: 102}, user)); code(err) != connect.CodeFailedPrecondition || errorCode(err) != "media_request.quota_reached" {
		t.Errorf("quota: %v", err)
	}
	if _, err := requests.CreateRequest(ctx, withToken(&laternav1.CreateRequestRequest{ExternalId: 102}, user)); code(err) != connect.CodeInvalidArgument {
		t.Errorf("no kind: %v", err)
	}
	if _, err := requests.ListRequests(ctx, withToken(&laternav1.ListRequestsRequest{}, user)); code(err) != connect.CodePermissionDenied {
		t.Errorf("queue read by a user: %v", err)
	}

	// The administrator's queue, then approval for the first season.
	queue, err := requests.ListRequests(ctx, withToken(&laternav1.ListRequestsRequest{Statuses: []laternav1.RequestStatus{laternav1.RequestStatus_REQUEST_STATUS_PENDING}}, token))
	if err != nil || len(queue.Msg.GetRequests()) != 1 || queue.Msg.GetPendingCount() != 1 || queue.Msg.GetRequests()[0].GetDestination().GetRootFolder() == "" {
		t.Fatalf("queue: %v %v", queue, err)
	}
	approved, err := requests.ApproveRequest(ctx, withToken(&laternav1.ApproveRequestRequest{
		RequestId: req.GetId(), Seasons: laternav1.RequestSeasons_REQUEST_SEASONS_CHOSEN, SeasonNumbers: []int32{2},
	}, token))
	if err != nil {
		t.Fatal(err)
	}
	if a := approved.Msg.GetRequest(); a.GetStatus() != laternav1.RequestStatus_REQUEST_STATUS_APPROVED || a.GetDecidedBy() != "Chloé" || len(a.GetSeasonNumbers()) != 1 {
		t.Errorf("approved: %v", a)
	}
	mine, err := requests.ListMyRequests(ctx, withToken(&laternav1.ListMyRequestsRequest{}, user))
	if err != nil || len(mine.Msg.GetRequests()) != 1 || mine.Msg.GetRequests()[0].GetStatus() != laternav1.RequestStatus_REQUEST_STATUS_APPROVED {
		t.Errorf("my requests: %v %v", mine, err)
	}
	if _, err := requests.CancelRequest(ctx, withToken(&laternav1.CancelRequestRequest{RequestId: req.GetId()}, user)); errorCode(err) != "media_request.not_pending" {
		t.Errorf("approved request withdrawn: %v", err)
	}

	// Music needs Lidarr, books LazyLibrarian; the integrations list them both.
	if _, err := requests.SearchRequestable(ctx, withToken(&laternav1.SearchRequestableRequest{Kind: laternav1.RequestKind_REQUEST_KIND_MUSIC, Query: "daft"}, user)); errorCode(err) != "media_request.unavailable" {
		t.Errorf("music without Lidarr: %v", err)
	}
	if _, err := requests.CreateRequest(ctx, withToken(&laternav1.CreateRequestRequest{Kind: laternav1.RequestKind_REQUEST_KIND_BOOK}, user)); errorCode(err) != "media_request.invalid_external_id" {
		t.Errorf("book without a key: %v", err)
	}
	list, err := integrations.ListIntegrations(ctx, withToken(&laternav1.ListIntegrationsRequest{}, token))
	if err != nil || len(list.Msg.GetIntegrations()) != 4 {
		t.Fatalf("integrations: %v %v", list, err)
	}
	if ll := list.Msg.GetIntegrations()[3]; ll.GetKind() != laternav1.IntegrationKind_INTEGRATION_KIND_LAZYLIBRARIAN || ll.GetManagesMetadata() {
		t.Errorf("LazyLibrarian: %v", ll)
	}
}
