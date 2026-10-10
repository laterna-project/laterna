package rpc

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// RequestService implements laterna.v1.RequestService.
type RequestService struct {
	app *app.App
}

var requestKinds = map[domain.RequestKind]laternav1.RequestKind{
	domain.RequestSeries: laternav1.RequestKind_REQUEST_KIND_SERIES,
	domain.RequestMovie:  laternav1.RequestKind_REQUEST_KIND_MOVIE,
	domain.RequestMusic:  laternav1.RequestKind_REQUEST_KIND_MUSIC,
	domain.RequestArtist: laternav1.RequestKind_REQUEST_KIND_ARTIST,
	domain.RequestAlbum:  laternav1.RequestKind_REQUEST_KIND_ALBUM,
	domain.RequestBook:   laternav1.RequestKind_REQUEST_KIND_BOOK,
}

var requestableStates = map[domain.RequestableState]laternav1.RequestableState{
	domain.Requestable:          laternav1.RequestableState_REQUESTABLE_STATE_REQUESTABLE,
	domain.RequestableAvailable: laternav1.RequestableState_REQUESTABLE_STATE_AVAILABLE,
	domain.RequestableRequested: laternav1.RequestableState_REQUESTABLE_STATE_REQUESTED,
	domain.RequestableTracked:   laternav1.RequestableState_REQUESTABLE_STATE_TRACKED,
}

var requestStatuses = map[domain.RequestStatus]laternav1.RequestStatus{
	domain.RequestPending:     laternav1.RequestStatus_REQUEST_STATUS_PENDING,
	domain.RequestApproved:    laternav1.RequestStatus_REQUEST_STATUS_APPROVED,
	domain.RequestDownloading: laternav1.RequestStatus_REQUEST_STATUS_DOWNLOADING,
	domain.RequestAvailable:   laternav1.RequestStatus_REQUEST_STATUS_AVAILABLE,
	domain.RequestDeclined:    laternav1.RequestStatus_REQUEST_STATUS_DECLINED,
	domain.RequestFailed:      laternav1.RequestStatus_REQUEST_STATUS_FAILED,
}

var requestSeasons = map[domain.RequestSeasons]laternav1.RequestSeasons{
	domain.SeasonsAll:    laternav1.RequestSeasons_REQUEST_SEASONS_ALL,
	domain.SeasonsFirst:  laternav1.RequestSeasons_REQUEST_SEASONS_FIRST,
	domain.SeasonsLatest: laternav1.RequestSeasons_REQUEST_SEASONS_LATEST,
	domain.SeasonsChosen: laternav1.RequestSeasons_REQUEST_SEASONS_CHOSEN,
}

var seriesTypes = map[domain.SeriesType]laternav1.RequestSeriesType{
	domain.SeriesStandard: laternav1.RequestSeriesType_REQUEST_SERIES_TYPE_STANDARD,
	domain.SeriesAnime:    laternav1.RequestSeriesType_REQUEST_SERIES_TYPE_ANIME,
	domain.SeriesDaily:    laternav1.RequestSeriesType_REQUEST_SERIES_TYPE_DAILY,
}

// fromMsg finds the domain value of a contract enum; zero for UNSPECIFIED or an unknown value.
func fromMsg[K comparable, V comparable](m map[K]V, v V) K {
	for k, mv := range m {
		if mv == v {
			return k
		}
	}
	var zero K
	return zero
}

// requestKindFromMsg reads a kind; an unknown one is left for the application to refuse.
func requestKindFromMsg(k laternav1.RequestKind) domain.RequestKind {
	if kind := fromMsg(requestKinds, k); kind != "" {
		return kind
	}
	return domain.RequestKind(k.String())
}

func seasonsFromMsg(s laternav1.RequestSeasons) domain.RequestSeasons {
	if s == laternav1.RequestSeasons_REQUEST_SEASONS_UNSPECIFIED {
		return ""
	}
	if v := fromMsg(requestSeasons, s); v != "" {
		return v
	}
	return domain.RequestSeasons(s.String())
}

func seriesTypeFromMsg(t laternav1.RequestSeriesType) domain.SeriesType {
	if t == laternav1.RequestSeriesType_REQUEST_SERIES_TYPE_UNSPECIFIED {
		return ""
	}
	if v := fromMsg(seriesTypes, t); v != "" {
		return v
	}
	return domain.SeriesType(t.String())
}

func intsFromMsg(n []int32) []int {
	out := make([]int, len(n))
	for i, v := range n {
		out[i] = int(v)
	}
	return out
}

func optInt(n *int32) *int {
	if n == nil {
		return nil
	}
	v := int(*n)
	return &v
}

func optTimestamp(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

func requestableMsg(t domain.RequestableTitle) *laternav1.RequestableTitle {
	return &laternav1.RequestableTitle{
		Kind: requestKinds[t.Kind], ExternalId: t.ExternalID, Title: t.Title, Year: clampInt32(t.Year), Overview: t.Overview,
		PosterUrl: app.RequestPosterPath(t.Poster), State: requestableStates[t.State], ItemId: idString(t.ItemID),
		RequestId: idString(t.RequestID), SeasonCount: clampInt32(t.SeasonCount), Network: t.Network,
		ExternalKey: t.ExternalKey,
	}
}

func destinationMsg(d domain.RequestDestination) *laternav1.RequestDestination {
	return &laternav1.RequestDestination{
		Id: d.ID.String(), Name: d.Name, Kind: requestKinds[d.Kind], LibraryId: d.LibraryID.String(), LibraryName: d.LibraryName,
		RootFolder: d.RootFolder, QualityProfileId: clampInt32(d.QualityProfileID), QualityProfileName: d.QualityProfileName,
		SeriesType: seriesTypes[d.SeriesType], MetadataProfileId: clampInt32(d.MetadataProfileID),
		MetadataProfileName: d.MetadataProfileName,
	}
}

func requestMsg(ctx context.Context, r domain.MediaRequest) *laternav1.MediaRequest {
	msg := &laternav1.MediaRequest{
		Id: r.ID.String(), Kind: requestKinds[r.Kind], ExternalId: r.ExternalID, Title: r.Title, Year: clampInt32(r.Year),
		PosterUrl: app.RequestPosterPath(r.Poster), Status: requestStatuses[r.Status], Seasons: requestSeasons[r.Seasons],
		AccountId: r.AccountID.String(), Username: r.Username, ProfileId: r.ProfileID.String(), ProfileName: r.ProfileName,
		CreatedAt: timestamppb.New(r.CreatedAt), DecidedAt: optTimestamp(r.DecidedAt), DecidedBy: r.DecidedBy,
		DeclineReason: r.DeclineReason, Progress: r.Progress, ItemId: idString(r.ItemID),
		EpisodesAvailable: clampInt32(r.EpisodesAvailable), EpisodesWanted: clampInt32(r.EpisodesWanted),
		AvailableAt: optTimestamp(r.AvailableAt), ExternalKey: r.ExternalKey, Subtitle: r.Subtitle,
	}
	for _, n := range r.SeasonNumbers {
		msg.SeasonNumbers = append(msg.SeasonNumbers, clampInt32(n))
	}
	if r.Destination != nil {
		msg.Destination = destinationMsg(*r.Destination)
	}
	if r.Error != nil {
		msg.Error, msg.ErrorText = render(ctx, *r.Error), textMsg(ctx, *r.Error)
	}
	return msg
}

func requestsMsg(ctx context.Context, list []domain.MediaRequest) []*laternav1.MediaRequest {
	out := make([]*laternav1.MediaRequest, len(list))
	for i, r := range list {
		out[i] = requestMsg(ctx, r)
	}
	return out
}

// SearchRequestable looks a title up on Sonarr or Radarr.
func (s *RequestService) SearchRequestable(ctx context.Context, req *connect.Request[laternav1.SearchRequestableRequest]) (*connect.Response[laternav1.SearchRequestableResponse], error) {
	found, err := s.app.SearchRequestable(ctx, principal(ctx), requestKindFromMsg(req.Msg.GetKind()), req.Msg.GetQuery())
	if err != nil {
		return nil, err
	}
	resp := &laternav1.SearchRequestableResponse{}
	for _, t := range found {
		resp.Results = append(resp.Results, requestableMsg(t))
	}
	return connect.NewResponse(resp), nil
}

// ListRequestDestinations lists the destinations open to the profile.
func (s *RequestService) ListRequestDestinations(ctx context.Context, req *connect.Request[laternav1.ListRequestDestinationsRequest]) (*connect.Response[laternav1.ListRequestDestinationsResponse], error) {
	var kind domain.RequestKind
	if k := req.Msg.GetKind(); k != laternav1.RequestKind_REQUEST_KIND_UNSPECIFIED {
		kind = requestKindFromMsg(k)
	}
	list, err := s.app.RequestDestinations(ctx, principal(ctx), kind)
	if err != nil {
		return nil, err
	}
	resp := &laternav1.ListRequestDestinationsResponse{}
	for _, d := range list {
		resp.Destinations = append(resp.Destinations, destinationMsg(d))
	}
	return connect.NewResponse(resp), nil
}

// CreateRequest asks for a title.
func (s *RequestService) CreateRequest(ctx context.Context, req *connect.Request[laternav1.CreateRequestRequest]) (*connect.Response[laternav1.CreateRequestResponse], error) {
	m := req.Msg
	dest, err := parseOptionalID(m.GetDestinationId(), "destination_id")
	if err != nil {
		return nil, err
	}
	r, err := s.app.CreateRequest(ctx, principal(ctx), app.NewRequest{
		Kind: requestKindFromMsg(m.GetKind()), ExternalID: m.GetExternalId(), ExternalKey: m.GetExternalKey(), DestinationID: dest,
		Seasons: seasonsFromMsg(m.GetSeasons()), SeasonNumbers: intsFromMsg(m.GetSeasonNumbers()),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.CreateRequestResponse{Request: requestMsg(ctx, r)}), nil
}

// ListMyRequests lists the profile's requests.
func (s *RequestService) ListMyRequests(ctx context.Context, req *connect.Request[laternav1.ListMyRequestsRequest]) (*connect.Response[laternav1.ListMyRequestsResponse], error) {
	page, err := s.app.MyRequests(ctx, principal(ctx), req.Msg.GetPageToken(), int(req.Msg.GetPageSize()))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.ListMyRequestsResponse{
		Requests: requestsMsg(ctx, page.Requests), NextPageToken: page.NextPageToken,
	}), nil
}

// GetRequest returns a request.
func (s *RequestService) GetRequest(ctx context.Context, req *connect.Request[laternav1.GetRequestRequest]) (*connect.Response[laternav1.GetRequestResponse], error) {
	id, err := parseID(req.Msg.GetRequestId(), "request_id")
	if err != nil {
		return nil, err
	}
	r, err := s.app.Request(ctx, principal(ctx), id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.GetRequestResponse{Request: requestMsg(ctx, r)}), nil
}

// CancelRequest withdraws a pending request of the profile.
func (s *RequestService) CancelRequest(ctx context.Context, req *connect.Request[laternav1.CancelRequestRequest]) (*connect.Response[laternav1.CancelRequestResponse], error) {
	id, err := parseID(req.Msg.GetRequestId(), "request_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.CancelRequest(ctx, principal(ctx), id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.CancelRequestResponse{}), nil
}

// ListRequests lists the requests of every account.
func (s *RequestService) ListRequests(ctx context.Context, req *connect.Request[laternav1.ListRequestsRequest]) (*connect.Response[laternav1.ListRequestsResponse], error) {
	var statuses []domain.RequestStatus
	for _, st := range req.Msg.GetStatuses() {
		if v := fromMsg(requestStatuses, st); v != "" {
			statuses = append(statuses, v)
		}
	}
	page, err := s.app.Requests(ctx, principal(ctx), statuses, req.Msg.GetPageToken(), int(req.Msg.GetPageSize()))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.ListRequestsResponse{
		Requests: requestsMsg(ctx, page.Requests), NextPageToken: page.NextPageToken, PendingCount: clampInt32(page.Pending),
	}), nil
}

// ApproveRequest approves a request.
func (s *RequestService) ApproveRequest(ctx context.Context, req *connect.Request[laternav1.ApproveRequestRequest]) (*connect.Response[laternav1.ApproveRequestResponse], error) {
	m := req.Msg
	id, err := parseID(m.GetRequestId(), "request_id")
	if err != nil {
		return nil, err
	}
	var ap app.Approval
	if m.DestinationId != nil {
		dest, err := parseID(m.GetDestinationId(), "destination_id")
		if err != nil {
			return nil, err
		}
		ap.DestinationID = &dest
	}
	ap.Seasons, ap.SeasonNumbers = seasonsFromMsg(m.GetSeasons()), intsFromMsg(m.GetSeasonNumbers())
	r, err := s.app.ApproveRequest(ctx, principal(ctx), id, ap)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.ApproveRequestResponse{Request: requestMsg(ctx, r)}), nil
}

// DeclineRequest declines a request.
func (s *RequestService) DeclineRequest(ctx context.Context, req *connect.Request[laternav1.DeclineRequestRequest]) (*connect.Response[laternav1.DeclineRequestResponse], error) {
	id, err := parseID(req.Msg.GetRequestId(), "request_id")
	if err != nil {
		return nil, err
	}
	r, err := s.app.DeclineRequest(ctx, principal(ctx), id, req.Msg.GetReason())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.DeclineRequestResponse{Request: requestMsg(ctx, r)}), nil
}

// DeleteRequest forgets a request.
func (s *RequestService) DeleteRequest(ctx context.Context, req *connect.Request[laternav1.DeleteRequestRequest]) (*connect.Response[laternav1.DeleteRequestResponse], error) {
	id, err := parseID(req.Msg.GetRequestId(), "request_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.DeleteRequest(ctx, principal(ctx), id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.DeleteRequestResponse{}), nil
}

// GetRequestOptions reads the root folders and quality profiles of the instance.
func (s *RequestService) GetRequestOptions(ctx context.Context, req *connect.Request[laternav1.GetRequestOptionsRequest]) (*connect.Response[laternav1.GetRequestOptionsResponse], error) {
	opts, err := s.app.RequestOptions(ctx, principal(ctx), requestKindFromMsg(req.Msg.GetKind()))
	if err != nil {
		return nil, err
	}
	resp := &laternav1.GetRequestOptionsResponse{}
	for _, f := range opts.RootFolders {
		resp.RootFolders = append(resp.RootFolders, &laternav1.RequestRootFolder{Path: f.Path, FreeSpace: f.FreeSpace})
	}
	for _, q := range opts.QualityProfiles {
		resp.QualityProfiles = append(resp.QualityProfiles, &laternav1.RequestQualityProfile{Id: clampInt32(q.ID), Name: q.Name})
	}
	for _, q := range opts.MetadataProfiles {
		resp.MetadataProfiles = append(resp.MetadataProfiles, &laternav1.RequestQualityProfile{Id: clampInt32(q.ID), Name: q.Name})
	}
	return connect.NewResponse(resp), nil
}

// CreateRequestDestination adds a destination.
func (s *RequestService) CreateRequestDestination(ctx context.Context, req *connect.Request[laternav1.CreateRequestDestinationRequest]) (*connect.Response[laternav1.CreateRequestDestinationResponse], error) {
	m := req.Msg
	lib, err := parseID(m.GetLibraryId(), "library_id")
	if err != nil {
		return nil, err
	}
	name, root, profile, meta := m.GetName(), m.GetRootFolder(), int(m.GetQualityProfileId()), int(m.GetMetadataProfileId())
	d, err := s.app.CreateRequestDestination(ctx, principal(ctx), app.DestinationChanges{
		Name: &name, Kind: requestKindFromMsg(m.GetKind()), LibraryID: &lib, RootFolder: &root, QualityProfileID: &profile,
		SeriesType: seriesTypeFromMsg(m.GetSeriesType()), MetadataProfileID: &meta,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.CreateRequestDestinationResponse{Destination: destinationMsg(d)}), nil
}

// UpdateRequestDestination changes a destination.
func (s *RequestService) UpdateRequestDestination(ctx context.Context, req *connect.Request[laternav1.UpdateRequestDestinationRequest]) (*connect.Response[laternav1.UpdateRequestDestinationResponse], error) {
	m := req.Msg
	id, err := parseID(m.GetDestinationId(), "destination_id")
	if err != nil {
		return nil, err
	}
	ch := app.DestinationChanges{
		Name: m.Name, RootFolder: m.RootFolder, QualityProfileID: optInt(m.QualityProfileId),
		SeriesType: seriesTypeFromMsg(m.GetSeriesType()), MetadataProfileID: optInt(m.MetadataProfileId),
	}
	if m.LibraryId != nil {
		lib, err := parseID(m.GetLibraryId(), "library_id")
		if err != nil {
			return nil, err
		}
		ch.LibraryID = &lib
	}
	d, err := s.app.UpdateRequestDestination(ctx, principal(ctx), id, ch)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.UpdateRequestDestinationResponse{Destination: destinationMsg(d)}), nil
}

// DeleteRequestDestination removes a destination.
func (s *RequestService) DeleteRequestDestination(ctx context.Context, req *connect.Request[laternav1.DeleteRequestDestinationRequest]) (*connect.Response[laternav1.DeleteRequestDestinationResponse], error) {
	id, err := parseID(req.Msg.GetDestinationId(), "destination_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.DeleteRequestDestination(ctx, principal(ctx), id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.DeleteRequestDestinationResponse{}), nil
}
