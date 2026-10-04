package rpc

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
)

// Watch party over HTTP: two devices, WatchParty streams, common start, message, end of the group.
func TestPartyServiceOverHTTP(t *testing.T) {
	s, catalog, token := mediaServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	parties := laternav1connect.NewPartyServiceClient(http.DefaultClient, s.url)
	movies, err := catalog.ListMovies(ctx, withToken(&laternav1.ListMoviesRequest{}, token))
	if err != nil {
		t.Fatal(err)
	}
	// Second device of the same account: another member.
	other, err := s.auth.Login(ctx, connect.NewRequest(&laternav1.LoginRequest{
		Username: "Chloé", Password: "a-strong-password",
		Device: &laternav1.Device{Name: "Tablette", Client: "Tests", ClientVersion: "1", Platform: "Go"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	token2 := other.Msg.GetToken()

	created, err := parties.CreateParty(ctx, withToken(&laternav1.CreatePartyRequest{ItemIds: []string{movies.Msg.GetMovies()[0].GetId()}}, token))
	if err != nil {
		t.Fatal(err)
	}
	p := created.Msg.GetParty()
	if len(p.GetCode()) != 6 || len(p.GetItems()) != 1 || p.GetItems()[0].GetMovie() == nil || p.GetState().GetStatus() != laternav1.PartyStatus_PARTY_STATUS_PAUSED {
		t.Fatalf("group: %v", p)
	}
	joined, err := parties.JoinParty(ctx, withToken(&laternav1.JoinPartyRequest{Code: p.GetCode()}, token2))
	if err != nil || len(joined.Msg.GetParty().GetState().GetMembers()) != 2 {
		t.Fatalf("join: %v %v", joined, err)
	}

	watch := func(tok string) *connect.ServerStreamForClient[laternav1.WatchPartyResponse] {
		t.Helper()
		stream, err := parties.WatchParty(ctx, withToken(&laternav1.WatchPartyRequest{PartyId: p.GetId()}, tok))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = stream.Close() })
		return stream
	}
	// until reads the stream up to a response that satisfies ok.
	until := func(stream *connect.ServerStreamForClient[laternav1.WatchPartyResponse], ok func(*laternav1.WatchPartyResponse) bool) *laternav1.WatchPartyResponse {
		t.Helper()
		for stream.Receive() {
			if msg := stream.Msg(); ok(msg) {
				if msg.GetServerTime() == nil {
					t.Error("response without the server time")
				}
				return msg
			}
		}
		t.Fatalf("stream ended: %v", stream.Err())
		return nil
	}
	first, second := watch(token), watch(token2)
	until(first, func(r *laternav1.WatchPartyResponse) bool { return r.GetState() != nil })
	until(second, func(r *laternav1.WatchPartyResponse) bool { return r.GetState() != nil })

	if _, err := parties.ControlParty(ctx, withToken(&laternav1.ControlPartyRequest{
		PartyId: p.GetId(), Command: &laternav1.ControlPartyRequest_Play{Play: true},
	}, token2)); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{token, token2} {
		if _, err := parties.ReportPartyStatus(ctx, withToken(&laternav1.ReportPartyStatusRequest{PartyId: p.GetId(), Ready: true}, tok)); err != nil {
			t.Fatal(err)
		}
	}
	playing := func(r *laternav1.WatchPartyResponse) bool {
		return r.GetState().GetStatus() == laternav1.PartyStatus_PARTY_STATUS_PLAYING
	}
	a, b := until(first, playing).GetState(), until(second, playing).GetState()
	if !a.GetAt().AsTime().Equal(b.GetAt().AsTime()) || a.GetVersion() != b.GetVersion() {
		t.Errorf("different start: %v / %v", a, b)
	}

	if _, err := parties.SendPartyMessage(ctx, withToken(&laternav1.SendPartyMessageRequest{PartyId: p.GetId(), Text: "🍿", Reaction: true}, token2)); err != nil {
		t.Fatal(err)
	}
	if m := until(first, func(r *laternav1.WatchPartyResponse) bool { return r.GetMessage() != nil }).GetMessage(); m.GetText() != "🍿" || !m.GetReaction() {
		t.Errorf("reaction: %v", m)
	}
	now, err := parties.GetServerTime(ctx, withToken(&laternav1.GetServerTimeRequest{}, token))
	if err != nil || time.Since(now.Msg.GetNow().AsTime()).Abs() > time.Minute {
		t.Errorf("server time: %v %v", now, err)
	}

	// Only the host ends the group; both streams get the end.
	if _, err := parties.EndParty(ctx, withToken(&laternav1.EndPartyRequest{PartyId: p.GetId()}, token2)); code(err) != connect.CodePermissionDenied {
		t.Errorf("ended by a member: %v", err)
	}
	if _, err := parties.EndParty(ctx, withToken(&laternav1.EndPartyRequest{PartyId: p.GetId()}, token)); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []*connect.ServerStreamForClient[laternav1.WatchPartyResponse]{first, second} {
		if r := until(stream, func(r *laternav1.WatchPartyResponse) bool { return r.GetEnded() != "" }); r.GetEnded() == "" {
			t.Error("end not received")
		}
	}
}
