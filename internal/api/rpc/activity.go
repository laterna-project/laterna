package rpc

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// ActivityService implements laterna.v1.ActivityService.
type ActivityService struct {
	app *app.App
}

var activityKinds = map[domain.ActivityKind]laternav1.ActivityKind{
	domain.ActivityLogin:           laternav1.ActivityKind_ACTIVITY_KIND_LOGIN,
	domain.ActivityLoginFailed:     laternav1.ActivityKind_ACTIVITY_KIND_LOGIN_FAILED,
	domain.ActivityAccountCreated:  laternav1.ActivityKind_ACTIVITY_KIND_ACCOUNT_CREATED,
	domain.ActivityAccountUpdated:  laternav1.ActivityKind_ACTIVITY_KIND_ACCOUNT_UPDATED,
	domain.ActivityAccountDeleted:  laternav1.ActivityKind_ACTIVITY_KIND_ACCOUNT_DELETED,
	domain.ActivitySessionRevoked:  laternav1.ActivityKind_ACTIVITY_KIND_SESSION_REVOKED,
	domain.ActivityLibraryCreated:  laternav1.ActivityKind_ACTIVITY_KIND_LIBRARY_CREATED,
	domain.ActivityLibraryUpdated:  laternav1.ActivityKind_ACTIVITY_KIND_LIBRARY_UPDATED,
	domain.ActivityLibraryDeleted:  laternav1.ActivityKind_ACTIVITY_KIND_LIBRARY_DELETED,
	domain.ActivityLibraryScanned:  laternav1.ActivityKind_ACTIVITY_KIND_LIBRARY_SCANNED,
	domain.ActivityPlaybackStarted: laternav1.ActivityKind_ACTIVITY_KIND_PLAYBACK_STARTED,
	domain.ActivityPlaybackStopped: laternav1.ActivityKind_ACTIVITY_KIND_PLAYBACK_STOPPED,
	domain.ActivitySettingsUpdated: laternav1.ActivityKind_ACTIVITY_KIND_SETTINGS_UPDATED,
	domain.ActivityIntegration:     laternav1.ActivityKind_ACTIVITY_KIND_INTEGRATION,
	domain.ActivityWebhook:         laternav1.ActivityKind_ACTIVITY_KIND_WEBHOOK,
	domain.ActivityJobFailed:       laternav1.ActivityKind_ACTIVITY_KIND_JOB_FAILED,
	domain.ActivityPartyStarted:    laternav1.ActivityKind_ACTIVITY_KIND_PARTY_STARTED,
	domain.ActivityImport:          laternav1.ActivityKind_ACTIVITY_KIND_IMPORT,
}

func optID(id *domain.ID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// ListActivity reads the activity log.
func (s *ActivityService) ListActivity(ctx context.Context, req *connect.Request[laternav1.ListActivityRequest]) (*connect.Response[laternav1.ListActivityResponse], error) {
	m := req.Msg
	account, err := parseOptionalID(m.GetAccountId(), "account_id")
	if err != nil {
		return nil, err
	}
	page, err := s.app.Activity(ctx, app.ActivityQuery{
		PageToken: m.GetPageToken(), PageSize: int(m.GetPageSize()), WarningsOnly: m.GetWarningsOnly(), AccountID: account,
	})
	if err != nil {
		return nil, err
	}
	resp := &laternav1.ListActivityResponse{NextPageToken: page.NextPageToken}
	for _, e := range page.Entries {
		resp.Entries = append(resp.Entries, &laternav1.ActivityEntry{
			Id: e.ID, At: timestamppb.New(e.At), Kind: activityKinds[e.Kind], Warning: e.Warning,
			AccountId: optID(e.AccountID), ProfileId: optID(e.ProfileID), ItemId: optID(e.ItemID), Summary: render(ctx, e.Text), Text: textMsg(ctx, e.Text),
		})
	}
	return connect.NewResponse(resp), nil
}

// ListDevices lists the signed-in devices of every account.
func (s *ActivityService) ListDevices(ctx context.Context, _ *connect.Request[laternav1.ListDevicesRequest]) (*connect.Response[laternav1.ListDevicesResponse], error) {
	devices, err := s.app.Devices(ctx)
	if err != nil {
		return nil, err
	}
	current := principal(ctx).SessionID
	resp := &laternav1.ListDevicesResponse{}
	for _, d := range devices {
		se := d.Session
		resp.Devices = append(resp.Devices, &laternav1.DeviceSession{
			SessionId: se.ID.String(), AccountId: se.AccountID.String(), Username: d.Username, ProfileName: d.ProfileName,
			Device: &laternav1.Device{
				Name: se.Device.Name, Client: se.Device.Client, ClientVersion: se.Device.ClientVersion, Platform: se.Device.Platform,
			},
			CreatedAt: timestamppb.New(se.CreatedAt), LastUsedAt: timestamppb.New(se.LastUsedAt), LastIp: se.LastIP,
			Current: se.ID == current,
		})
	}
	return connect.NewResponse(resp), nil
}

// RevokeDevice signs a device out.
func (s *ActivityService) RevokeDevice(ctx context.Context, req *connect.Request[laternav1.RevokeDeviceRequest]) (*connect.Response[laternav1.RevokeDeviceResponse], error) {
	id, err := parseID(req.Msg.GetSessionId(), "session_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.RevokeDevice(ctx, principal(ctx), id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.RevokeDeviceResponse{}), nil
}

// ListPlaybacks lists the playbacks in progress.
func (s *ActivityService) ListPlaybacks(context.Context, *connect.Request[laternav1.ListPlaybacksRequest]) (*connect.Response[laternav1.ListPlaybacksResponse], error) {
	resp := &laternav1.ListPlaybacksResponse{}
	for _, p := range s.app.Playbacks() {
		resp.Playbacks = append(resp.Playbacks, &laternav1.ActivePlayback{
			Id: p.ID.String(), AccountId: p.AccountID.String(), Username: p.Username, ProfileId: p.ProfileID.String(),
			ProfileName: p.ProfileName, Device: p.Device, ItemId: p.ItemID.String(), Title: p.Title, Method: p.Method,
			CopyVideo: p.CopyVideo, CopyAudio: p.CopyAudio, Encoder: p.Encoder, ToneMap: p.ToneMap, Gpu: p.GPU, Decoder: p.Decoder,
			StartedAt: timestamppb.New(p.StartedAt), Position: durationpb.New(p.Position), Duration: durationpb.New(p.Duration),
			Transcoding: p.Transcoding,
		})
	}
	return connect.NewResponse(resp), nil
}

// EndPlayback stops a playback in progress.
func (s *ActivityService) EndPlayback(ctx context.Context, req *connect.Request[laternav1.EndPlaybackRequest]) (*connect.Response[laternav1.EndPlaybackResponse], error) {
	id, err := parseID(req.Msg.GetPlaybackId(), "playback_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.EndPlayback(ctx, principal(ctx), id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.EndPlaybackResponse{}), nil
}
