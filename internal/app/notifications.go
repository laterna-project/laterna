package app

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// Notifications (docs/design/notifications.md): what a profile is told about while it is not
// looking. Each one is kept in the profile's list and announced to its devices.
const (
	// maxNotifications is how many a profile keeps; notificationRetention how long.
	maxNotifications      = 200
	notificationRetention = 60 * 24 * time.Hour
	// Page sizes of the list.
	notificationPageSize    = 30
	maxNotificationPageSize = 100
	// Episodes of a series are announced together: once nothing more arrived for
	// newEpisodesQuiet, or newEpisodesLatest after the first one.
	newEpisodesQuiet  = 2 * time.Minute
	newEpisodesLatest = 15 * time.Minute
	newEpisodesEvery  = 30 * time.Second
)

// NotificationView is a notification with what a client shows next to it.
type NotificationView struct {
	domain.Notification
	// Item is what it is about, when the profile still sees it.
	Item *domain.ItemView
	// Poster is the poster of its request: an address on TVDB, TMDB..., served by the server.
	Poster string
}

// NotificationPage is a page of a profile's notifications, newest first.
type NotificationPage struct {
	Notifications []NotificationView
	// Unread counts all the notifications the profile has not read, not only those of the page.
	Unread        int
	NextPageToken string
}

// Notifications lists the notifications of the profile, newest first.
func (a *App) Notifications(ctx context.Context, p domain.Principal, pageToken string, pageSize int) (NotificationPage, error) {
	v, err := viewerOf(p)
	if err != nil {
		return NotificationPage{}, err
	}
	switch {
	case pageSize == 0:
		pageSize = notificationPageSize
	case pageSize < 0 || pageSize > maxNotificationPageSize:
		return NotificationPage{}, domain.Invalid("request.invalid_page_size", "max", maxNotificationPageSize)
	}
	var after *domain.ID
	if pageToken != "" {
		id, err := domain.ParseID(pageToken)
		if err != nil {
			return NotificationPage{}, domain.Invalid("request.invalid_page_token")
		}
		after = &id
	}
	read := a.store.Read()
	list, err := read.Notifications(ctx, v.ProfileID, after, pageSize+1)
	if err != nil {
		return NotificationPage{}, err
	}
	var page NotificationPage
	if len(list) > pageSize {
		list = list[:pageSize]
		page.NextPageToken = list[pageSize-1].ID.String()
	}
	if page.Unread, err = read.UnreadNotifications(ctx, v.ProfileID); err != nil {
		return NotificationPage{}, err
	}

	var itemIDs, requestIDs []domain.ID
	for _, n := range list {
		if n.ItemID != nil {
			itemIDs = append(itemIDs, *n.ItemID)
		}
		if n.RequestID != nil {
			requestIDs = append(requestIDs, *n.RequestID)
		}
	}
	items, err := read.ViewsByID(ctx, v, itemIDs)
	if err != nil {
		return NotificationPage{}, err
	}
	posters := map[domain.ID]string{}
	if len(requestIDs) > 0 {
		reqs, err := read.Requests(ctx, store.RequestQuery{IDs: requestIDs})
		if err != nil {
			return NotificationPage{}, err
		}
		for _, r := range reqs {
			posters[r.ID] = r.Poster
		}
	}
	page.Notifications = make([]NotificationView, 0, len(list))
	for _, n := range list {
		view := NotificationView{Notification: n}
		if n.ItemID != nil {
			if item, ok := items[*n.ItemID]; ok {
				view.Item = &item
			}
		}
		if n.RequestID != nil {
			view.Poster = posters[*n.RequestID]
		}
		page.Notifications = append(page.Notifications, view)
	}
	return page, nil
}

// MarkNotificationsRead marks notifications of the profile as read: those named, or all of them.
func (a *App) MarkNotificationsRead(ctx context.Context, p domain.Principal, ids []domain.ID, all bool) error {
	return a.changeNotifications(ctx, p, ids, all, func(q store.Q, profileID domain.ID, ids []domain.ID) (int64, error) {
		return q.MarkNotificationsRead(ctx, profileID, ids, a.now())
	})
}

// DeleteNotifications removes notifications of the profile: those named, or all of them.
func (a *App) DeleteNotifications(ctx context.Context, p domain.Principal, ids []domain.ID, all bool) error {
	return a.changeNotifications(ctx, p, ids, all, func(q store.Q, profileID domain.ID, ids []domain.ID) (int64, error) {
		return q.DeleteNotifications(ctx, profileID, ids)
	})
}

func (a *App) changeNotifications(ctx context.Context, p domain.Principal, ids []domain.ID, all bool, change func(store.Q, domain.ID, []domain.ID) (int64, error)) error {
	if p.Profile == nil {
		return domain.Precondition("profile.required")
	}
	switch {
	case all && len(ids) > 0:
		return domain.Invalid("notification.all_or_some")
	case all:
		ids = nil
	case len(ids) == 0:
		return nil
	case len(ids) > maxNotifications:
		return domain.Invalid("notification.too_many", "max", maxNotifications)
	default:
		ids = slices.Clone(ids)
	}
	var changed int64
	if err := a.store.Write(ctx, func(q store.Q) (err error) {
		changed, err = change(q, p.Profile.ID, ids)
		return err
	}); err != nil {
		return err
	}
	if changed > 0 {
		a.bus.Publish(domain.NotificationsChanged{ProfileID: p.Profile.ID})
	}
	return nil
}

// notify stores notifications and announces them to the devices of their profiles. Like the
// activity log, it never fails what it reports on.
func (a *App) notify(ctx context.Context, list ...domain.Notification) {
	if len(list) == 0 {
		return
	}
	now := a.now()
	for i := range list {
		list[i].ID, list[i].CreatedAt = domain.NewID(), now
	}
	// What is reported already happened: it is written even if the request that caused it is gone.
	ctx = context.WithoutCancel(ctx)
	pushed := false
	if err := a.store.Write(ctx, func(q store.Q) error {
		for _, n := range list {
			if err := q.AddNotification(ctx, n, maxNotifications); err != nil {
				return err
			}
			// Devices that asked for it are told even when the app is closed.
			subs, err := q.PushSubscriptions(ctx, n.ProfileID, now)
			if err != nil {
				return err
			}
			if len(subs) > 0 {
				if err := a.jobs.Enqueue(ctx, q, jobPushNotification, n.ID.String(), priorityUser); err != nil {
					return err
				}
				pushed = true
			}
		}
		return nil
	}); err != nil {
		a.log.WarnContext(ctx, "notifications: cannot store", "count", len(list), "err", err)
		return
	}
	if pushed {
		a.jobs.Kick()
	}
	told := map[domain.ID]bool{}
	for _, n := range list {
		if !told[n.ProfileID] {
			told[n.ProfileID] = true
			a.bus.Publish(domain.NotificationsChanged{ProfileID: n.ProfileID})
		}
	}
}

// purgeNotifications forgets old notifications.
func (a *App) purgeNotifications(ctx context.Context) error {
	return a.store.Write(ctx, func(q store.Q) error {
		_, err := q.DeleteNotificationsBefore(ctx, a.now().Add(-notificationRetention))
		return err
	})
}

// Requests.

// notifyRequest tells the profile of a request what became of it. actor is the profile whose
// action it reports, nil for the server: nobody is told about what they just did themselves.
func (a *App) notifyRequest(ctx context.Context, r domain.MediaRequest, kind domain.NotificationKind, actor *domain.ID) {
	if actor != nil && *actor == r.ProfileID {
		return
	}
	n := domain.Notification{ProfileID: r.ProfileID, Kind: kind, RequestID: &r.ID, ItemID: r.ItemID}
	switch kind {
	case domain.NotificationRequestApproved:
		n.Text = domain.T("notification.request_approved", "title", r.Title)
	case domain.NotificationRequestDeclined:
		n.Text = domain.T("notification.request_declined", "title", r.Title)
		if r.DeclineReason != "" {
			n.Text = domain.T("notification.request_declined_reason", "title", r.Title, "reason", r.DeclineReason)
		}
	case domain.NotificationRequestAvailable:
		n.Text = domain.T("notification.request_available", "title", r.Title)
	case domain.NotificationRequestFailed:
		n.Text = domain.T("notification.request_failed", "title", r.Title)
	case domain.NotificationRequestPending, domain.NotificationNewEpisodes:
		return
	}
	a.notify(ctx, n)
}

// notifyAdministrators tells the administrators about a request: one that waits for them, or one
// that failed. skip is a profile that already knows.
func (a *App) notifyAdministrators(ctx context.Context, r domain.MediaRequest, kind domain.NotificationKind, skip *domain.ID) {
	text := domain.T("notification.request_pending", "profile", r.ProfileName, "title", r.Title)
	if kind == domain.NotificationRequestFailed {
		text = domain.T("notification.request_failed_admin", "profile", r.ProfileName, "title", r.Title)
	}
	admins, err := a.store.Read().AdministratorProfiles(ctx)
	if err != nil {
		a.log.WarnContext(ctx, "notifications: cannot list administrators", "err", err)
		return
	}
	var list []domain.Notification
	for _, id := range admins {
		if skip != nil && *skip == id {
			continue
		}
		list = append(list, domain.Notification{ProfileID: id, Kind: kind, Text: text, RequestID: &r.ID})
	}
	a.notify(ctx, list...)
}

// New episodes.

// newEpisodes gathers the episodes that arrive, series by series, until they are announced.
type newEpisodes struct {
	mu     sync.Mutex
	series map[domain.ID]*arrivals
}

// newEpisode is an episode the catalog just got, and its series.
type newEpisode struct {
	series, episode domain.ID
}

// arrivals are the episodes of a series that arrived and were not announced yet.
type arrivals struct {
	episodes    []domain.ID
	first, last time.Time
}

// episodeArrived notes an episode the catalog just got.
func (a *App) episodeArrived(seriesID, episodeID domain.ID) {
	now := a.now()
	a.arrived.mu.Lock()
	defer a.arrived.mu.Unlock()
	if a.arrived.series == nil {
		a.arrived.series = map[domain.ID]*arrivals{}
	}
	s := a.arrived.series[seriesID]
	if s == nil {
		s = &arrivals{first: now}
		a.arrived.series[seriesID] = s
	}
	s.episodes, s.last = append(s.episodes, episodeID), now
}

// watchNewEpisodes announces the episodes that arrived, until shutdown.
func (a *App) watchNewEpisodes(ctx context.Context) {
	t := time.NewTicker(newEpisodesEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.announceEpisodes(ctx)
		}
	}
}

// announceEpisodes tells the profiles that follow a series about its episodes that arrived, once
// the series has been quiet for a while: a season that lands is one notification, not twelve.
func (a *App) announceEpisodes(ctx context.Context) {
	now := a.now()
	due := map[domain.ID][]domain.ID{}
	a.arrived.mu.Lock()
	for id, s := range a.arrived.series {
		if now.Sub(s.last) >= newEpisodesQuiet || now.Sub(s.first) >= newEpisodesLatest {
			due[id] = s.episodes
			delete(a.arrived.series, id)
		}
	}
	a.arrived.mu.Unlock()
	for seriesID, episodes := range due {
		if err := a.announceSeries(ctx, seriesID, episodes); err != nil {
			a.log.WarnContext(ctx, "notifications: cannot announce new episodes", "series", seriesID, "err", err)
		}
	}
}

// announceSeries notifies the followers of a series who can see it. One episode is named and opens
// on itself; several are counted and open on the series.
func (a *App) announceSeries(ctx context.Context, seriesID domain.ID, episodes []domain.ID) error {
	read := a.store.Read()
	followers, err := read.SeriesFollowers(ctx, seriesID)
	if err != nil || len(followers) == 0 {
		return err
	}
	var list []domain.Notification
	for _, profileID := range followers {
		v, ok, err := a.viewerOfProfile(ctx, profileID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		// Only what the profile sees: an episode that left in the meantime is not announced.
		views, err := read.ViewsByID(ctx, v, append([]domain.ID{seriesID}, episodes...))
		if err != nil {
			return err
		}
		series, ok := views[seriesID]
		if !ok {
			continue
		}
		var seen []domain.ItemView
		for _, id := range episodes {
			if ep, ok := views[id]; ok && ep.Episode != nil {
				seen = append(seen, ep)
			}
		}
		n := domain.Notification{ProfileID: profileID, Kind: domain.NotificationNewEpisodes}
		switch len(seen) {
		case 0:
			continue
		case 1:
			n.ItemID = &seen[0].Item.ID
			n.Text = domain.T("notification.new_episode", "series", series.Item.Title, "season", seen[0].Episode.SeasonNumber, "episode", seen[0].Episode.Number)
		default:
			n.ItemID = &seriesID
			n.Text = domain.T("notification.new_episodes", "series", series.Item.Title, "count", len(seen))
		}
		list = append(list, n)
	}
	a.notify(ctx, list...)
	return nil
}

// viewerOfProfile is what a profile may see, read from the database; ok is false if the profile is
// gone or its account is disabled.
func (a *App) viewerOfProfile(ctx context.Context, profileID domain.ID) (domain.Viewer, bool, error) {
	read := a.store.Read()
	profile, _, err := read.Profile(ctx, profileID)
	if store.IsNotFound(err) {
		return domain.Viewer{}, false, nil
	}
	if err != nil {
		return domain.Viewer{}, false, err
	}
	account, err := read.Account(ctx, profile.AccountID)
	if store.IsNotFound(err) {
		return domain.Viewer{}, false, nil
	}
	if err != nil || account.Disabled {
		return domain.Viewer{}, false, err
	}
	v, ok := domain.Principal{Account: account, Profile: &profile}.Viewer()
	return v, ok, nil
}
