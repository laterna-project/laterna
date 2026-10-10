package app

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/arr/arrtest"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// notificationsOf lists the notifications of a profile as "kind: text key" lines, newest first.
func notificationsOf(t *testing.T, a *App, p domain.Principal) ([]string, NotificationPage) {
	t.Helper()
	page, err := a.Notifications(context.Background(), p, "", 0)
	mustNil(t, err)
	out := []string{}
	for _, n := range page.Notifications {
		out = append(out, string(n.Kind)+": "+n.Text.Key)
	}
	return out, page
}

func TestRequestNotifications(t *testing.T) {
	a, _, admin, sonarr, _, shows, _ := requestApp(t)
	ctx := context.Background()
	sonarr.Set(func(s *arrtest.Server) {
		s.Catalog = []arrtest.Entry{
			{ExternalID: 101, Title: "Frieren", Year: 2023, Seasons: []int{1}, Poster: "https://artworks.example/frieren.jpg"},
			{ExternalID: 102, Title: "Mushishi", Year: 2005, Seasons: []int{1}},
			{ExternalID: 103, Title: "Monster", Year: 2004, Seasons: []int{1}},
		}
	})
	_, err := a.CreateAccount(ctx, admin, NewAccount{Username: "Léa", Password: "a-password"})
	mustNil(t, err)
	_, lea := login(t, a, "Léa", "a-password")
	name, root, profile := "Anime", "/data/media/shows", 7
	_, err = a.CreateRequestDestination(ctx, admin, DestinationChanges{Name: &name, Kind: domain.RequestSeries, LibraryID: &shows.ID, RootFolder: &root, QualityProfileID: &profile})
	mustNil(t, err)

	// A request that waits is announced to the administrators, not to whoever made it.
	subAdmin := a.Subscribe(admin)
	defer subAdmin.Close()
	frieren, err := a.CreateRequest(ctx, lea, NewRequest{Kind: domain.RequestSeries, ExternalID: 101})
	mustNil(t, err)
	if got, _ := notificationsOf(t, a, lea); len(got) != 0 {
		t.Errorf("requester, right after asking: %v", got)
	}
	got, page := notificationsOf(t, a, admin)
	if !slices.Equal(got, []string{"request_pending: notification.request_pending"}) || page.Unread != 1 {
		t.Fatalf("administrator: %v, %d unread", got, page.Unread)
	}
	n := page.Notifications[0]
	if n.RequestID == nil || *n.RequestID != frieren.ID || n.ReadAt != nil || n.Poster != "https://artworks.example/frieren.jpg" ||
		n.Text.Params["profile"] != "Léa" || n.Text.Params["title"] != "Frieren" {
		t.Errorf("notification: %+v", n)
	}
	// The administrator's devices hear of it.
	heard := false
	for !heard {
		wait, cancel := context.WithTimeout(ctx, 5*time.Second)
		e, err := subAdmin.Next(wait)
		cancel()
		if err != nil {
			t.Fatalf("no event: %v", err)
		}
		nc, ok := e.(domain.NotificationsChanged)
		heard = ok && nc.ProfileID == admin.Profile.ID
	}

	// An administrator's own request is approved at once: nobody is told what they just did.
	_, err = a.CreateRequest(ctx, admin, NewRequest{Kind: domain.RequestSeries, ExternalID: 103})
	mustNil(t, err)
	if got, _ := notificationsOf(t, a, admin); len(got) != 1 {
		t.Errorf("after the administrator's own request: %v", got)
	}

	// Approved by someone else: the requester is told. Then available, with the item to open.
	_, err = a.ApproveRequest(ctx, admin, frieren.ID, Approval{})
	mustNil(t, err)
	mustNil(t, a.submitRequest(ctx, frieren.ID.String()))
	item := putInCatalog(t, a, shows, "Frieren", "tvdb", 101, 2)
	mustNil(t, a.refreshRequests(ctx, ""))
	got, page = notificationsOf(t, a, lea)
	if !slices.Equal(got, []string{"request_available: notification.request_available", "request_approved: notification.request_approved"}) || page.Unread != 2 {
		t.Fatalf("requester: %v, %d unread", got, page.Unread)
	}
	if n := page.Notifications[0]; n.Item == nil || n.Item.Item.ID != item || n.ItemID == nil {
		t.Errorf("available: %+v", n)
	}
	// Following the request again does not announce it twice.
	mustNil(t, a.refreshRequests(ctx, ""))
	if got, _ := notificationsOf(t, a, lea); len(got) != 2 {
		t.Errorf("announced twice: %v", got)
	}

	// Declined, with the reason; then one that fails is told to its profile and to the
	// administrators.
	mushishi, err := a.CreateRequest(ctx, lea, NewRequest{Kind: domain.RequestSeries, ExternalID: 102})
	mustNil(t, err)
	_, err = a.DeclineRequest(ctx, admin, mushishi.ID, "Too long")
	mustNil(t, err)
	got, page = notificationsOf(t, a, lea)
	if got[0] != "request_declined: notification.request_declined_reason" || page.Notifications[0].Text.Params["reason"] != "Too long" {
		t.Errorf("declined: %v %+v", got, page.Notifications[0].Text)
	}
	failing, err := a.store.Read().Request(ctx, frieren.ID)
	mustNil(t, err)
	failing.Status = domain.RequestApproved
	mustNil(t, a.store.Write(ctx, func(q store.Q) error { return q.UpdateRequest(ctx, failing) }))
	mustNil(t, a.failRequest(ctx, failing, domain.Literal("Sonarr said no")))
	if got, _ := notificationsOf(t, a, lea); got[0] != "request_failed: notification.request_failed" {
		t.Errorf("failed, requester: %v", got)
	}
	if got, _ := notificationsOf(t, a, admin); got[0] != "request_failed: notification.request_failed_admin" {
		t.Errorf("failed, administrator: %v", got)
	}
}

func TestNotificationList(t *testing.T) {
	a, clk := newTestApp(t)
	_, admin := setupAdmin(t, a)
	ctx := context.Background()
	_, err := a.CreateAccount(ctx, admin, NewAccount{Username: "Léa", Password: "a-password"})
	mustNil(t, err)
	_, lea := login(t, a, "Léa", "a-password")
	tell := func(p domain.Principal, title string) {
		a.notify(ctx, domain.Notification{ProfileID: p.Profile.ID, Kind: domain.NotificationRequestAvailable, Text: domain.T("notification.request_available", "title", title)})
		clk.advance(time.Second)
	}
	for _, title := range []string{"One", "Two", "Three", "Four", "Five"} {
		tell(admin, title)
	}
	tell(lea, "Hers")
	titles := func(page NotificationPage) []string {
		var out []string
		for _, n := range page.Notifications {
			out = append(out, n.Text.Params["title"])
		}
		return out
	}

	// Pages, newest first, with the count of what is unread on every page.
	page, err := a.Notifications(ctx, admin, "", 2)
	mustNil(t, err)
	if !slices.Equal(titles(page), []string{"Five", "Four"}) || page.Unread != 5 || page.NextPageToken == "" {
		t.Fatalf("first page: %v %+v", titles(page), page)
	}
	second, err := a.Notifications(ctx, admin, page.NextPageToken, 2)
	mustNil(t, err)
	last, err := a.Notifications(ctx, admin, second.NextPageToken, 2)
	mustNil(t, err)
	if !slices.Equal(titles(second), []string{"Three", "Two"}) || !slices.Equal(titles(last), []string{"One"}) || last.NextPageToken != "" {
		t.Errorf("next pages: %v %v", titles(second), titles(last))
	}
	for _, bad := range []func() error{
		func() error { _, err := a.Notifications(ctx, admin, "nope", 0); return err },
		func() error { _, err := a.Notifications(ctx, admin, "", 101); return err },
		func() error { return a.MarkNotificationsRead(ctx, admin, []domain.ID{domain.NewID()}, true) },
		func() error { return a.DeleteNotifications(ctx, admin, make([]domain.ID, maxNotifications+1), false) },
	} {
		if err := bad(); !isKind(err, domain.ErrInvalid) {
			t.Errorf("invalid call: %v", err)
		}
	}

	// Read: some, then all. Someone else's are left alone.
	hers, err := a.Notifications(ctx, lea, "", 0)
	mustNil(t, err)
	mustNil(t, a.MarkNotificationsRead(ctx, admin, []domain.ID{page.Notifications[0].ID, hers.Notifications[0].ID}, false))
	page, _ = a.Notifications(ctx, admin, "", 0)
	if page.Unread != 4 || page.Notifications[0].ReadAt == nil || page.Notifications[1].ReadAt != nil {
		t.Errorf("one read: %d unread", page.Unread)
	}
	if hers, _ = a.Notifications(ctx, lea, "", 0); hers.Unread != 1 {
		t.Errorf("read by someone else: %+v", hers)
	}
	mustNil(t, a.MarkNotificationsRead(ctx, admin, nil, true))
	if page, _ = a.Notifications(ctx, admin, "", 0); page.Unread != 0 || len(page.Notifications) != 5 {
		t.Errorf("all read: %+v", page)
	}

	// Deleted: some, then all.
	mustNil(t, a.DeleteNotifications(ctx, admin, []domain.ID{page.Notifications[0].ID, hers.Notifications[0].ID}, false))
	if page, _ = a.Notifications(ctx, admin, "", 0); !slices.Equal(titles(page), []string{"Four", "Three", "Two", "One"}) {
		t.Errorf("one deleted: %v", titles(page))
	}
	mustNil(t, a.DeleteNotifications(ctx, admin, nil, true))
	if page, _ = a.Notifications(ctx, admin, "", 0); len(page.Notifications) != 0 {
		t.Errorf("all deleted: %v", titles(page))
	}
	if hers, _ = a.Notifications(ctx, lea, "", 0); len(hers.Notifications) != 1 {
		t.Errorf("deleted by someone else: %+v", hers)
	}

	// A profile keeps its newest ones, and none past their time.
	for range maxNotifications + 3 {
		a.notify(ctx, domain.Notification{ProfileID: admin.Profile.ID, Kind: domain.NotificationRequestAvailable, Text: domain.T("notification.request_available", "title", "Many")})
	}
	tell(admin, "Newest")
	all := 0
	for token := ""; ; {
		page, err := a.Notifications(ctx, admin, token, maxNotificationPageSize)
		mustNil(t, err)
		if token == "" && page.Notifications[0].Text.Params["title"] != "Newest" {
			t.Errorf("newest first: %+v", page.Notifications[0].Text)
		}
		all += len(page.Notifications)
		if token = page.NextPageToken; token == "" {
			break
		}
	}
	if all != maxNotifications {
		t.Errorf("%d notifications kept, want %d", all, maxNotifications)
	}
	clk.advance(notificationRetention + time.Hour)
	tell(lea, "Fresh")
	mustNil(t, a.purgeNotifications(ctx))
	if page, _ = a.Notifications(ctx, admin, "", 0); len(page.Notifications) != 0 {
		t.Errorf("old notifications kept: %d", len(page.Notifications))
	}
	if hers, _ = a.Notifications(ctx, lea, "", 0); !slices.Equal(titles(hers), []string{"Fresh"}) {
		t.Errorf("after the purge: %v", titles(hers))
	}
}

func TestNewEpisodeNotifications(t *testing.T) {
	a, clk, admin, _, _, shows, movies := requestApp(t)
	ctx := context.Background()
	series := putInCatalog(t, a, shows, "Frieren", "tvdb", 101, 4)
	other := putInCatalog(t, a, shows, "Monster", "tvdb", 103, 1)
	eps, err := a.Episodes(ctx, admin, series, nil)
	mustNil(t, err)
	episode := func(n int) domain.ID { return eps[n-1].Item.ID }

	// Three profiles: one watches the series, one has it as a favorite but may not browse its
	// library, one does not follow it.
	_, err = a.CreateAccount(ctx, admin, NewAccount{Username: "Léa", Password: "a-password"})
	mustNil(t, err)
	_, lea := login(t, a, "Léa", "a-password")
	mustNil(t, a.SetPlayed(ctx, lea, episode(1), true))
	_, err = a.CreateAccount(ctx, admin, NewAccount{Username: "Tom", Password: "a-password"})
	mustNil(t, err)
	_, tom := login(t, a, "Tom", "a-password")
	mustNil(t, a.SetFavorite(ctx, tom, series, true))
	_, err = a.UpdateAccount(ctx, admin, tom.Account.ID, AccountChanges{Libraries: &domain.LibraryAccess{IDs: []domain.ID{movies.ID}}})
	mustNil(t, err)
	_, tom = login(t, a, "Tom", "a-password")
	mustNil(t, a.SetFavorite(ctx, admin, other, true))

	// One episode arrives: nothing is said while more may follow, then it is named.
	a.episodeArrived(series, episode(2))
	a.announceEpisodes(ctx)
	if got, _ := notificationsOf(t, a, lea); len(got) != 0 {
		t.Errorf("announced at once: %v", got)
	}
	clk.advance(newEpisodesQuiet)
	a.announceEpisodes(ctx)
	got, page := notificationsOf(t, a, lea)
	if !slices.Equal(got, []string{"new_episodes: notification.new_episode"}) {
		t.Fatalf("one episode: %v", got)
	}
	n := page.Notifications[0]
	if n.Item == nil || n.Item.Item.ID != episode(2) || n.Text.Params["series"] != "Frieren" || n.Text.Params["season"] != "1" || n.Text.Params["episode"] != "2" {
		t.Errorf("one episode: %+v %+v", n.Text, n.Item)
	}
	for who, p := range map[string]domain.Principal{"no access to the library": tom, "does not follow it": admin} {
		if got, _ := notificationsOf(t, a, p); len(got) != 0 {
			t.Errorf("%s: %v", who, got)
		}
	}
	a.announceEpisodes(ctx)
	if got, _ := notificationsOf(t, a, lea); len(got) != 1 {
		t.Errorf("announced twice: %v", got)
	}

	// Episodes that keep arriving are announced together, at the latest a quarter of an hour after
	// the first; they open on the series.
	a.episodeArrived(series, episode(3))
	clk.advance(newEpisodesQuiet - time.Second)
	a.episodeArrived(series, episode(4))
	a.announceEpisodes(ctx)
	if got, _ := notificationsOf(t, a, lea); len(got) != 1 {
		t.Errorf("announced while episodes arrive: %v", got)
	}
	clk.advance(newEpisodesLatest)
	a.announceEpisodes(ctx)
	got, page = notificationsOf(t, a, lea)
	if len(got) != 2 || got[0] != "new_episodes: notification.new_episodes" {
		t.Fatalf("two episodes: %v", got)
	}
	if n := page.Notifications[0]; n.Item == nil || n.Item.Item.ID != series || n.Text.Params["count"] != "2" {
		t.Errorf("two episodes: %+v", n.Text)
	}
}
