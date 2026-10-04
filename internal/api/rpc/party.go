package rpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/party"
)

// PartyService implements laterna.v1.PartyService.
type PartyService struct {
	app *app.App
}

var partyStatuses = map[party.Status]laternav1.PartyStatus{
	party.Paused:  laternav1.PartyStatus_PARTY_STATUS_PAUSED,
	party.Playing: laternav1.PartyStatus_PARTY_STATUS_PLAYING,
	party.Waiting: laternav1.PartyStatus_PARTY_STATUS_WAITING,
}

func partyStateMsg(st party.State) *laternav1.PartyState {
	msg := &laternav1.PartyState{
		Queue: idsMsg(st.Queue), Index: clampInt32(st.Index), Status: partyStatuses[st.Status], Resume: partyStatuses[st.Resume],
		Position: durationpb.New(st.Position), At: timestamppb.New(st.At), HostOnly: st.HostOnly, Version: st.Version,
	}
	for _, m := range st.Members {
		msg.Members = append(msg.Members, &laternav1.PartyMember{
			Id: m.ID.String(), Name: m.Name, Host: m.Host, Ready: m.Ready, Buffering: m.Buffering, Synced: m.Synced,
		})
	}
	return msg
}

func partyMsg(ctx context.Context, v app.PartyView) *laternav1.Party {
	msg := &laternav1.Party{Id: v.ID.String(), Code: v.Code, MemberId: v.MemberID.String(), State: partyStateMsg(v.State)}
	for _, it := range v.Items {
		switch it.Item.Kind {
		case domain.ItemMovie:
			msg.Items = append(msg.Items, &laternav1.PartyItem{Item: &laternav1.PartyItem_Movie{Movie: movieSummaryMsg(it)}})
		case domain.ItemEpisode:
			msg.Items = append(msg.Items, &laternav1.PartyItem{Item: &laternav1.PartyItem_Episode{Episode: episodeMsg(ctx, it)}})
		case domain.ItemTrack:
			msg.Items = append(msg.Items, &laternav1.PartyItem{Item: &laternav1.PartyItem_Track{Track: trackMsg(ctx, it)}})
		case domain.ItemSeries, domain.ItemSeason, domain.ItemArtist, domain.ItemAlbum, domain.ItemBookSeries, domain.ItemBook,
			domain.ItemPhotoAlbum, domain.ItemPhoto:
		}
	}
	return msg
}

// CreateParty creates a group.
func (s *PartyService) CreateParty(ctx context.Context, req *connect.Request[laternav1.CreatePartyRequest]) (*connect.Response[laternav1.CreatePartyResponse], error) {
	ids, err := parseIDs(req.Msg.GetItemIds(), "item_ids")
	if err != nil {
		return nil, err
	}
	v, err := s.app.CreateParty(ctx, principal(ctx), ids, req.Msg.GetHostOnly())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.CreatePartyResponse{Party: partyMsg(ctx, v)}), nil
}

// JoinParty lets the caller into a group.
func (s *PartyService) JoinParty(ctx context.Context, req *connect.Request[laternav1.JoinPartyRequest]) (*connect.Response[laternav1.JoinPartyResponse], error) {
	v, err := s.app.JoinParty(ctx, principal(ctx), req.Msg.GetCode())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.JoinPartyResponse{Party: partyMsg(ctx, v)}), nil
}

// GetParty returns a group.
func (s *PartyService) GetParty(ctx context.Context, req *connect.Request[laternav1.GetPartyRequest]) (*connect.Response[laternav1.GetPartyResponse], error) {
	id, err := parseID(req.Msg.GetPartyId(), "party_id")
	if err != nil {
		return nil, err
	}
	v, err := s.app.GetParty(ctx, principal(ctx), id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.GetPartyResponse{Party: partyMsg(ctx, v)}), nil
}

// WatchParty sends the updates of a group until the stream is closed or the group ends.
func (s *PartyService) WatchParty(ctx context.Context, req *connect.Request[laternav1.WatchPartyRequest], stream *connect.ServerStream[laternav1.WatchPartyResponse]) error {
	id, err := parseID(req.Msg.GetPartyId(), "party_id")
	if err != nil {
		return err
	}
	sub, err := s.app.WatchParty(principal(ctx), id)
	if err != nil {
		return err
	}
	defer sub.Close()
	for {
		wait, cancel := context.WithTimeout(ctx, heartbeatEvery)
		u, err := sub.Next(wait)
		cancel()
		resp := &laternav1.WatchPartyResponse{}
		switch {
		case ctx.Err() != nil, errors.Is(err, app.ErrSubscriptionClosed):
			return nil
		case errors.Is(err, context.DeadlineExceeded):
			resp.Update = &laternav1.WatchPartyResponse_Heartbeat{Heartbeat: true}
		case err != nil:
			return err
		case u.State != nil:
			resp.Update = &laternav1.WatchPartyResponse_State{State: partyStateMsg(*u.State)}
		case u.Message != nil:
			m := u.Message
			resp.Update = &laternav1.WatchPartyResponse_Message{Message: &laternav1.PartyMessage{
				MemberId: m.MemberID.String(), Name: m.Name, Text: m.Text, Reaction: m.Reaction, At: timestamppb.New(m.At),
			}}
		default:
			resp.Update = &laternav1.WatchPartyResponse_Ended{Ended: render(ctx, u.Ended)}
			resp.EndedText = textMsg(ctx, u.Ended)
		}
		resp.ServerTime = timestamppb.Now()
		if err := stream.Send(resp); err != nil {
			return err
		}
		if !u.Ended.IsZero() {
			return nil
		}
	}
}

// ControlParty controls a group.
func (s *PartyService) ControlParty(ctx context.Context, req *connect.Request[laternav1.ControlPartyRequest]) (*connect.Response[laternav1.ControlPartyResponse], error) {
	m := req.Msg
	id, err := parseID(m.GetPartyId(), "party_id")
	if err != nil {
		return nil, err
	}
	var cmd app.PartyCommand
	switch c := m.GetCommand().(type) {
	case *laternav1.ControlPartyRequest_Play:
		cmd.Play = c.Play
	case *laternav1.ControlPartyRequest_Pause:
		cmd.Pause = c.Pause
	case *laternav1.ControlPartyRequest_Seek:
		pos := c.Seek.AsDuration()
		cmd.Seek = &pos
	case *laternav1.ControlPartyRequest_Select:
		i := int(c.Select)
		cmd.Select = &i
	case *laternav1.ControlPartyRequest_Queue:
		if cmd.Queue, err = parseIDs(c.Queue.GetItemIds(), "queue.item_ids"); err != nil {
			return nil, err
		}
		if cmd.Queue == nil {
			cmd.Queue = []domain.ID{}
		}
		cmd.QueueIndex = int(c.Queue.GetIndex())
	case *laternav1.ControlPartyRequest_HostOnly:
		cmd.HostOnly = &c.HostOnly
	}
	st, err := s.app.ControlParty(ctx, principal(ctx), id, cmd)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.ControlPartyResponse{State: partyStateMsg(st)}), nil
}

// ReportPartyStatus records the state of the device.
func (s *PartyService) ReportPartyStatus(ctx context.Context, req *connect.Request[laternav1.ReportPartyStatusRequest]) (*connect.Response[laternav1.ReportPartyStatusResponse], error) {
	m := req.Msg
	id, err := parseID(m.GetPartyId(), "party_id")
	if err != nil {
		return nil, err
	}
	var ended *int
	if m.EndedIndex != nil {
		i := int(m.GetEndedIndex())
		ended = &i
	}
	if err := s.app.ReportPartyStatus(principal(ctx), id, m.GetReady(), m.GetBuffering(), ended); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.ReportPartyStatusResponse{}), nil
}

// SendPartyMessage sends a message to the group.
func (s *PartyService) SendPartyMessage(ctx context.Context, req *connect.Request[laternav1.SendPartyMessageRequest]) (*connect.Response[laternav1.SendPartyMessageResponse], error) {
	id, err := parseID(req.Msg.GetPartyId(), "party_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.SendPartyMessage(principal(ctx), id, req.Msg.GetText(), req.Msg.GetReaction()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.SendPartyMessageResponse{}), nil
}

// LeaveParty takes the caller out of the group.
func (s *PartyService) LeaveParty(ctx context.Context, req *connect.Request[laternav1.LeavePartyRequest]) (*connect.Response[laternav1.LeavePartyResponse], error) {
	id, err := parseID(req.Msg.GetPartyId(), "party_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.LeaveParty(principal(ctx), id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.LeavePartyResponse{}), nil
}

// KickPartyMember removes a member.
func (s *PartyService) KickPartyMember(ctx context.Context, req *connect.Request[laternav1.KickPartyMemberRequest]) (*connect.Response[laternav1.KickPartyMemberResponse], error) {
	id, err := parseID(req.Msg.GetPartyId(), "party_id")
	if err != nil {
		return nil, err
	}
	member, err := parseID(req.Msg.GetMemberId(), "member_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.KickPartyMember(principal(ctx), id, member); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.KickPartyMemberResponse{}), nil
}

// EndParty ends a group.
func (s *PartyService) EndParty(ctx context.Context, req *connect.Request[laternav1.EndPartyRequest]) (*connect.Response[laternav1.EndPartyResponse], error) {
	id, err := parseID(req.Msg.GetPartyId(), "party_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.EndParty(principal(ctx), id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.EndPartyResponse{}), nil
}

// GetServerTime returns the server's time.
func (s *PartyService) GetServerTime(context.Context, *connect.Request[laternav1.GetServerTimeRequest]) (*connect.Response[laternav1.GetServerTimeResponse], error) {
	return connect.NewResponse(&laternav1.GetServerTimeResponse{Now: timestamppb.Now()}), nil
}
