package rpc

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/arr"
	"github.com/laterna-project/laterna/internal/arr/arrtest"
)

// What is coming, over HTTP: linking Sonarr reads its calendar, and the home page lists it for a
// client that asks.
func TestUpcomingOverHTTP(t *testing.T) {
	s, _, token := mediaServer(t)
	ctx := context.Background()
	airs := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	sonarr := arrtest.New(t, arr.Sonarr)
	sonarr.Set(func(srv *arrtest.Server) {
		srv.Calendar = []map[string]any{{
			"title": "The Journey's End", "seasonNumber": 2, "episodeNumber": 5, "airDateUtc": airs.Format(time.RFC3339),
			"series": map[string]any{
				"title": "Frieren", "tvdbId": 101, "rootFolderPath": "/data/media/shows",
				"images": []any{map[string]any{"coverType": "poster", "remoteUrl": "https://artworks.example/frieren.jpg"}},
			},
		}}
	})
	integrations := laternav1connect.NewIntegrationServiceClient(http.DefaultClient, s.url)
	if _, err := integrations.SetIntegration(ctx, withToken(&laternav1.SetIntegrationRequest{
		Kind: laternav1.IntegrationKind_INTEGRATION_KIND_SONARR, Url: sonarr.URL, ApiKey: arrtest.Key,
	}, token)); err != nil {
		t.Fatal(err)
	}

	home := laternav1connect.NewHomeServiceClient(http.DefaultClient, s.url)
	upcoming := func(ask bool) *laternav1.HomeRow {
		resp, err := home.GetHome(ctx, withToken(&laternav1.GetHomeRequest{Upcoming: ask}, token))
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range resp.Msg.GetRows() {
			if r.GetKind() == laternav1.HomeRowKind_HOME_ROW_KIND_UPCOMING {
				return r
			}
		}
		return nil
	}
	var row *laternav1.HomeRow
	for deadline := time.Now().Add(20 * time.Second); row == nil; row = upcoming(true) {
		if time.Now().After(deadline) {
			t.Fatal("the calendar was not read")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if row.GetTitle() != "Coming soon" || row.GetTitleText().GetKey() != "home.upcoming" || len(row.GetItems()) != 0 || len(row.GetUpcoming()) != 1 {
		t.Fatalf("row: %v", row)
	}
	u := row.GetUpcoming()[0]
	if u.GetKind() != laternav1.UpcomingKind_UPCOMING_KIND_EPISODE || u.GetTitle() != "The Journey's End" || u.GetParentTitle() != "Frieren" ||
		u.GetSeasonNumber() != 2 || u.GetEpisodeNumber() != 5 || !u.GetReleaseTime().AsTime().Equal(airs) || u.GetAllDay() ||
		u.GetItemId() != "" || !strings.HasPrefix(u.GetPosterUrl(), "/requests/posters/") {
		t.Errorf("release: %v", u)
	}
	if upcoming(false) != nil {
		t.Error("the row was sent to a client that did not ask for it")
	}
}
