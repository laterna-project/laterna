package rpc

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
)

// History through the API: a playback that was watched enough becomes a play, summed up by the
// year's statistics.
func TestHistoryServiceOverHTTP(t *testing.T) {
	s, catalog, token := mediaServerWith(t, fixtureLibrary{"Films", laternav1.LibraryKind_LIBRARY_KIND_MOVIES})
	ctx := context.Background()
	c := http.DefaultClient
	player := laternav1connect.NewPlaybackServiceClient(c, s.url)
	history := laternav1connect.NewHistoryServiceClient(c, s.url)

	movies, err := catalog.ListMovies(ctx, withToken(&laternav1.ListMoviesRequest{PageSize: 50}, token))
	if err != nil {
		t.Fatal(err)
	}
	var movieID string
	for _, m := range movies.Msg.GetMovies() {
		if m.GetTitle() == "Big Test Movie" {
			movieID = m.GetId()
		}
	}
	play, err := player.StartPlayback(ctx, withToken(&laternav1.StartPlaybackRequest{
		ItemId: movieID, Device: &laternav1.DeviceProfile{
			Containers: []string{"mp4"}, Video: []*laternav1.VideoSupport{{Codec: "h264"}}, AudioCodecs: []string{"aac"}, Hls: true,
		},
	}, token))
	if err != nil {
		t.Fatal(err)
	}
	// Two 5 s steps: plausible even when reported right away (5 s margin), 10 s watched out of 12.
	for _, at := range []time.Duration{5 * time.Second, 10 * time.Second} {
		if _, err := player.ReportProgress(ctx, withToken(&laternav1.ReportProgressRequest{
			SessionId: play.Msg.GetSessionId(), Position: durationpb.New(at),
		}, token)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := player.StopPlayback(ctx, withToken(&laternav1.StopPlaybackRequest{SessionId: play.Msg.GetSessionId()}, token)); err != nil {
		t.Fatal(err)
	}

	list, err := history.ListHistory(ctx, withToken(&laternav1.ListHistoryRequest{}, token))
	if err != nil || len(list.Msg.GetEntries()) != 1 {
		t.Fatalf("history: %v %v", list, err)
	}
	e := list.Msg.GetEntries()[0]
	if e.GetKind() != laternav1.HistoryKind_HISTORY_KIND_MOVIE || e.GetMovie().GetId() != movieID || e.GetWatched().AsDuration() != 10*time.Second ||
		e.GetTitle() != "Big Test Movie" || e.GetStartedAt() == nil {
		t.Errorf("play: %v", e)
	}

	st, err := history.GetStats(ctx, withToken(&laternav1.GetStatsRequest{Year: int32(time.Now().Year()), TimeZone: "Europe/Paris"}, token))
	if err != nil {
		t.Fatal(err)
	}
	if m := st.Msg.GetStats(); m.GetPlays() != 1 || len(m.GetByHour()) != 168 || len(m.GetTimeline()) != 12 ||
		len(m.GetTopMovies()) != 1 || len(m.GetTopMovies()[0].GetImages()) == 0 || m.GetFirst().GetTitle() != "Big Test Movie" {
		t.Errorf("statistics: %v", m)
	}
	if _, err := history.GetStats(ctx, withToken(&laternav1.GetStatsRequest{TimeZone: "Nulle/Part"}, token)); code(err) != connect.CodeInvalidArgument {
		t.Errorf("unknown time zone: %v", err)
	}

	if _, err := history.DeleteHistoryEntry(ctx, withToken(&laternav1.DeleteHistoryEntryRequest{EntryId: e.GetId()}, token)); err != nil {
		t.Error(err)
	}
	cleared, err := history.ClearHistory(ctx, withToken(&laternav1.ClearHistoryRequest{}, token))
	if err != nil || cleared.Msg.GetDeleted() != 0 {
		t.Errorf("clear: %v %v", cleared, err)
	}
}
