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

func TestHomeAndEventsOverHTTP(t *testing.T) {
	s, catalog, token := mediaServer(t)
	ctx := context.Background()
	home := laternav1connect.NewHomeServiceClient(http.DefaultClient, s.url)
	events := laternav1connect.NewEventServiceClient(http.DefaultClient, s.url)

	resp, err := home.GetHome(ctx, withToken(&laternav1.GetHomeRequest{RowSize: 2}, token))
	if err != nil {
		t.Fatal(err)
	}
	rows := resp.Msg.GetRows()
	if len(rows) != 2 || rows[0].GetKind() != laternav1.HomeRowKind_HOME_ROW_KIND_LATEST_MOVIES ||
		len(rows[0].GetItems()) != 2 || rows[0].GetItems()[0].GetMovie() == nil || rows[1].GetItems()[0].GetSeries() == nil {
		t.Fatalf("home: %v", rows)
	}

	streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream, err := events.Subscribe(streamCtx, withToken(&laternav1.SubscribeRequest{}, token))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if !stream.Receive() || stream.Msg().GetEvent().GetHeartbeat() == nil || stream.Msg().GetEvent().GetTime() == nil {
		t.Fatalf("first message: %v %v", stream.Msg(), stream.Err())
	}

	// A favorite set elsewhere shows up in the stream.
	movieID := rows[0].GetItems()[0].GetMovie().GetId()
	if _, err := catalog.SetFavorite(ctx, withToken(&laternav1.SetFavoriteRequest{ItemId: movieID, Favorite: true}, token)); err != nil {
		t.Fatal(err)
	}
	for stream.Receive() {
		if changed := stream.Msg().GetEvent().GetUserDataChanged(); changed != nil {
			if ids := changed.GetItemIds(); len(ids) != 1 || ids[0] != movieID {
				t.Errorf("items: %v", ids)
			}
			break
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}

	// Without a token: refused with a clear code, in streaming too.
	bad, err := events.Subscribe(ctx, withToken(&laternav1.SubscribeRequest{}, ""))
	if err == nil {
		for bad.Receive() {
			t.Error("message received without a token")
		}
		err = bad.Err()
		_ = bad.Close()
	}
	if code(err) != connect.CodeUnauthenticated {
		t.Errorf("stream without a token: %v", err)
	}
}
