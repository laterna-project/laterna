package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/laterna-project/laterna/internal/arr"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// Requests for movies and series (docs/design/requests.md). A profile searches Sonarr or Radarr and
// asks for a title; an administrator approves it, unless the account's requests are approved at
// once. The server then hands the title to the instance (request.submit) and follows it until the
// catalog has it (requests.refresh).

const (
	jobSubmitRequest   = "request.submit"
	jobRefreshRequests = "requests.refresh"
	// requestsEvery is how often requests on their way are followed.
	requestsEvery = 5 * time.Minute
	// requestsAfterSubmit and requestsAfterImport leave time for the instance to grab the title,
	// and after an import the webhook reports, for the scan (30 s later) and the analysis.
	requestsAfterSubmit = time.Minute
	requestsAfterImport = 2 * time.Minute
	// requestEpisodesFor is how long an available series keeps counting the episodes that arrive.
	requestEpisodesFor = 90 * 24 * time.Hour

	maxRequestResults  = 20
	minRequestQuery    = 2
	maxRequestQuery    = 200
	maxDeclineReason   = 500
	maxDestinationName = 100
	maxDestinations    = 50
	maxRequestSeasons  = 100
	maxSeasonNumber    = 10_000
	requestPageSize    = 50
	maxRequestPageSize = 200
)

// requestArr is the instance that handles a kind of request.
func requestArr(k domain.RequestKind) arr.Kind {
	if k == domain.RequestMovie {
		return arr.Radarr
	}
	return arr.Sonarr
}

func validRequestKind(k domain.RequestKind) error {
	if k != domain.RequestSeries && k != domain.RequestMovie {
		return domain.Invalid("media_request.invalid_kind")
	}
	return nil
}

// requestClient returns the client of the instance for a kind. Without it, that kind cannot be
// requested.
func (a *App) requestClient(ctx context.Context, k domain.RequestKind) (*arr.Client, arr.Kind, error) {
	ak := requestArr(k)
	s, ok, err := a.loadIntegration(ctx, ak)
	if err != nil {
		return nil, ak, err
	}
	if !ok {
		return nil, ak, domain.Precondition("media_request.unavailable", "name", ak.Name())
	}
	return a.arrClient(ak, s), ak, nil
}

// instanceError explains a failed call to Sonarr or Radarr: in full to an administrator, in short
// to anyone else (the details go to the log).
func (a *App) instanceError(ctx context.Context, p domain.Principal, k arr.Kind, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	a.log.WarnContext(ctx, "requests: instance call failed", "kind", k, "err", err)
	if p.CanAdminister() {
		return domain.FromText(domain.ErrPrecondition, arrProblem(k, err))
	}
	return domain.Precondition("media_request.unavailable", "name", k.Name())
}

// Search.

// SearchRequestable looks a title up on the instance for that kind and says, for each result, what
// it is to the profile.
func (a *App) SearchRequestable(ctx context.Context, p domain.Principal, kind domain.RequestKind, query string) ([]domain.RequestableTitle, error) {
	if err := validRequestKind(kind); err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if n := utf8.RuneCountInString(query); n < minRequestQuery || n > maxRequestQuery {
		return nil, domain.Invalid("media_request.invalid_query", "min", minRequestQuery, "max", maxRequestQuery)
	}
	client, ak, err := a.requestClient(ctx, kind)
	if err != nil {
		return nil, err
	}
	found, err := client.Search(ctx, query)
	if err != nil {
		return nil, a.instanceError(ctx, p, ak, err)
	}
	// A result without an ID (not on TVDB or TMDB yet) cannot be requested.
	found = slices.DeleteFunc(found, func(t arr.Title) bool { return t.ExternalID <= 0 })
	found = found[:min(len(found), maxRequestResults)]
	ids := make([]int64, len(found))
	for i, t := range found {
		ids[i] = t.ExternalID
	}
	visible, present, err := a.inCatalog(ctx, p, kind, ids)
	if err != nil {
		return nil, err
	}
	open, err := a.store.Read().OpenRequests(ctx, kind, ids)
	if err != nil {
		return nil, err
	}
	out := make([]domain.RequestableTitle, 0, len(found))
	for _, t := range found {
		r := domain.RequestableTitle{
			Kind: kind, ExternalID: t.ExternalID, Title: t.Title, Year: t.Year, Overview: t.Overview, Poster: t.Poster,
			Network: t.Network, SeasonCount: len(t.Seasons), State: domain.Requestable,
		}
		if item, ok := visible[t.ExternalID]; ok {
			r.State, r.ItemID = domain.RequestableAvailable, &item
		} else if req, ok := open[t.ExternalID]; ok {
			r.State, r.RequestID = domain.RequestableRequested, &req
		} else if present[t.ExternalID] || (t.ArrID > 0 && t.Monitored) {
			r.State = domain.RequestableTracked
		}
		a.rememberPoster(t.Poster)
		out = append(out, r)
	}
	return out, nil
}

// inCatalog says which titles the catalog has (present), and the item of each that the profile
// sees (visible).
func (a *App) inCatalog(ctx context.Context, p domain.Principal, kind domain.RequestKind, ids []int64) (visible map[int64]domain.ID, present map[int64]bool, err error) {
	read := a.store.Read()
	items, err := read.ItemsWithExternalIDs(ctx, kind.ExternalProvider(), ids)
	if err != nil {
		return nil, nil, err
	}
	visible, present = map[int64]domain.ID{}, map[int64]bool{}
	var itemIDs []domain.ID
	for ext, list := range items {
		present[ext] = true
		for _, it := range list {
			itemIDs = append(itemIDs, it.ItemID)
		}
	}
	v, ok := p.Viewer()
	if !ok || len(itemIDs) == 0 {
		return visible, present, nil
	}
	views, err := read.ViewsByID(ctx, v, itemIDs)
	if err != nil {
		return nil, nil, err
	}
	for ext, list := range items {
		for _, it := range list {
			if _, ok := views[it.ItemID]; ok {
				visible[ext] = it.ItemID
				break
			}
		}
	}
	return visible, present, nil
}

// Requests.

// NewRequest is a request a profile makes.
type NewRequest struct {
	Kind       domain.RequestKind
	ExternalID int64
	// DestinationID nil takes the only destination of that kind open to the profile.
	DestinationID *domain.ID
	Seasons       domain.RequestSeasons
	SeasonNumbers []int
}

// CreateRequest asks for a title: pending, or approved and handed to the instance when the
// account's requests are approved at once.
func (a *App) CreateRequest(ctx context.Context, p domain.Principal, n NewRequest) (domain.MediaRequest, error) {
	if !p.CanRequest() {
		return domain.MediaRequest{}, domain.Forbidden("media_request.denied")
	}
	if p.Profile == nil {
		return domain.MediaRequest{}, domain.Precondition("profile.required")
	}
	if err := validRequestKind(n.Kind); err != nil {
		return domain.MediaRequest{}, err
	}
	if n.ExternalID <= 0 {
		return domain.MediaRequest{}, domain.Invalid("media_request.invalid_external_id")
	}
	seasons, numbers, err := requestSeasons(n.Kind, n.Seasons, n.SeasonNumbers)
	if err != nil {
		return domain.MediaRequest{}, err
	}
	dest, err := a.destinationFor(ctx, p, n.Kind, n.DestinationID)
	if err != nil {
		return domain.MediaRequest{}, err
	}
	client, ak, err := a.requestClient(ctx, n.Kind)
	if err != nil {
		return domain.MediaRequest{}, err
	}
	title, ok, err := client.Find(ctx, n.ExternalID)
	if err != nil {
		return domain.MediaRequest{}, a.instanceError(ctx, p, ak, err)
	}
	if !ok {
		return domain.MediaRequest{}, domain.NotFound("media_request.title_not_found")
	}
	visible, present, err := a.inCatalog(ctx, p, n.Kind, []int64{n.ExternalID})
	if err != nil {
		return domain.MediaRequest{}, err
	}
	if _, ok := visible[n.ExternalID]; ok {
		return domain.MediaRequest{}, domain.Conflict("media_request.available")
	}
	if present[n.ExternalID] || (title.ArrID > 0 && title.Monitored) {
		return domain.MediaRequest{}, domain.Conflict("media_request.tracked")
	}
	now := a.now()
	r := domain.MediaRequest{
		ID: domain.NewID(), Kind: n.Kind, ExternalID: n.ExternalID, Title: title.Title, Year: title.Year, Poster: title.Poster,
		Status: domain.RequestPending, Seasons: seasons, SeasonNumbers: numbers, Destination: &dest,
		AccountID: p.Account.ID, Username: p.Account.Username, ProfileID: p.Profile.ID, ProfileName: p.Profile.Name,
		CreatedAt: now, UpdatedAt: now,
	}
	approved := p.RequestsApproved()
	if approved {
		r.Status, r.DecidedAt, r.DecidedBy = domain.RequestApproved, &now, p.Account.Username
	}
	err = a.store.Write(ctx, func(q store.Q) error {
		if quota := p.Account.RequestQuota; !p.Account.IsAdmin && quota > 0 {
			made, err := q.CountRequestsSince(ctx, p.Account.ID, now.Add(-domain.RequestQuotaWindow))
			if err != nil {
				return err
			}
			if made >= quota {
				return domain.Precondition("media_request.quota_reached", "quota", quota, "days", int(domain.RequestQuotaWindow/(24*time.Hour)))
			}
		}
		if err := q.CreateRequest(ctx, r); errors.Is(err, store.ErrDuplicate) {
			return domain.Conflict("media_request.already_requested")
		} else if err != nil {
			return err
		}
		if approved {
			return a.jobs.Enqueue(ctx, q, jobSubmitRequest, r.ID.String(), priorityUser)
		}
		return nil
	})
	if err != nil {
		return domain.MediaRequest{}, err
	}
	if approved {
		a.jobs.Kick()
	}
	a.rememberPoster(r.Poster)
	a.log.InfoContext(ctx, "request created", "request", r.ID, "kind", r.Kind, "title", r.Title, "approved", approved)
	text := domain.T("activity.request_created", "profile", p.Profile.Name, "title", r.Title)
	if approved {
		text = domain.T("activity.request_created_approved", "profile", p.Profile.Name, "title", r.Title)
	}
	a.record(ctx, domain.Activity{Kind: domain.ActivityRequest, AccountID: &p.Account.ID, ProfileID: &p.Profile.ID, Text: text})
	a.requestsChanged(r.ProfileID, r.ID)
	return requestFor(p, r), nil
}

// requestSeasons checks the seasons asked for: a movie has none; chosen seasons come sorted and
// once each.
func requestSeasons(kind domain.RequestKind, s domain.RequestSeasons, numbers []int) (domain.RequestSeasons, []int, error) {
	if s == "" {
		s = domain.SeasonsAll
	}
	if kind == domain.RequestMovie {
		return domain.SeasonsAll, nil, nil
	}
	switch s {
	case domain.SeasonsAll, domain.SeasonsFirst, domain.SeasonsLatest:
		return s, nil, nil
	case domain.SeasonsChosen:
		out := slices.Compact(slices.Sorted(slices.Values(numbers)))
		if len(out) == 0 || len(out) > maxRequestSeasons || out[0] < 0 || out[len(out)-1] > maxSeasonNumber {
			return "", nil, domain.Invalid("media_request.invalid_seasons")
		}
		return s, out, nil
	}
	return "", nil, domain.Invalid("media_request.invalid_seasons")
}

// destinationFor picks where a request lands: the destination asked for, or the only one of that
// kind open to the profile (its account may browse the library).
func (a *App) destinationFor(ctx context.Context, p domain.Principal, kind domain.RequestKind, id *domain.ID) (domain.RequestDestination, error) {
	all, err := a.store.Read().RequestDestinations(ctx)
	if err != nil {
		return domain.RequestDestination{}, err
	}
	open := slices.DeleteFunc(all, func(d domain.RequestDestination) bool { return d.Kind != kind || !p.AllowsLibrary(d.LibraryID) })
	if id != nil {
		for _, d := range open {
			if d.ID == *id {
				return d, nil
			}
		}
		return domain.RequestDestination{}, domain.NotFound("media_request.destination_not_found")
	}
	switch len(open) {
	case 0:
		return domain.RequestDestination{}, domain.Precondition("media_request.no_destination")
	case 1:
		return open[0], nil
	}
	return domain.RequestDestination{}, domain.Invalid("media_request.destination_required")
}

// requestFor is a request as the caller may see it: how a destination is set on the instance is
// for administrators.
func requestFor(p domain.Principal, r domain.MediaRequest) domain.MediaRequest {
	if r.Destination != nil {
		d := destinationView(p, *r.Destination)
		r.Destination = &d
	}
	return r
}

func destinationView(p domain.Principal, d domain.RequestDestination) domain.RequestDestination {
	if !p.CanAdminister() {
		d.RootFolder, d.QualityProfileID, d.QualityProfileName, d.SeriesType = "", 0, "", ""
	}
	return d
}

// RequestPage is a page of requests.
type RequestPage struct {
	Requests      []domain.MediaRequest
	NextPageToken string
	// Pending counts the requests waiting for an administrator (ListRequests only).
	Pending int
}

type requestToken struct {
	At int64     `json:"at"`
	ID domain.ID `json:"id"`
}

// MyRequests lists the profile's requests, newest first.
func (a *App) MyRequests(ctx context.Context, p domain.Principal, pageToken string, pageSize int) (RequestPage, error) {
	if p.Profile == nil {
		return RequestPage{}, domain.Precondition("profile.required")
	}
	return a.requestPage(ctx, p, store.RequestQuery{ProfileID: &p.Profile.ID}, pageToken, pageSize)
}

// Requests lists the requests of every account, newest first, with the number waiting for an
// administrator.
func (a *App) Requests(ctx context.Context, p domain.Principal, statuses []domain.RequestStatus, pageToken string, pageSize int) (RequestPage, error) {
	page, err := a.requestPage(ctx, p, store.RequestQuery{Statuses: statuses}, pageToken, pageSize)
	if err != nil {
		return RequestPage{}, err
	}
	page.Pending, err = a.store.Read().CountPendingRequests(ctx)
	return page, err
}

func (a *App) requestPage(ctx context.Context, p domain.Principal, rq store.RequestQuery, pageToken string, pageSize int) (RequestPage, error) {
	if pageSize < 0 || pageSize > maxRequestPageSize {
		return RequestPage{}, domain.Invalid("request.invalid_page_size", "max", maxRequestPageSize)
	}
	rq.Limit = requestPageSize
	if pageSize > 0 {
		rq.Limit = pageSize
	}
	if pageToken != "" {
		var t requestToken
		b, err := base64.RawURLEncoding.DecodeString(pageToken)
		if err == nil {
			err = json.Unmarshal(b, &t)
		}
		if err != nil {
			return RequestPage{}, domain.Invalid("request.invalid_page_token")
		}
		rq.After = store.RequestCursor{At: time.UnixMilli(t.At), ID: t.ID}
	}
	limit := rq.Limit
	rq.Limit++
	list, err := a.store.Read().Requests(ctx, rq)
	if err != nil {
		return RequestPage{}, err
	}
	var page RequestPage
	if len(list) > limit {
		list = list[:limit]
		last := list[limit-1]
		b, _ := json.Marshal(requestToken{At: last.CreatedAt.UnixMilli(), ID: last.ID}) // a number and an ID: cannot fail
		page.NextPageToken = base64.RawURLEncoding.EncodeToString(b)
	}
	page.Requests = make([]domain.MediaRequest, len(list))
	for i, r := range list {
		page.Requests[i] = requestFor(p, r)
	}
	return page, nil
}

// Request returns a request of the profile; an administrator may read any.
func (a *App) Request(ctx context.Context, p domain.Principal, id domain.ID) (domain.MediaRequest, error) {
	r, err := a.store.Read().Request(ctx, id)
	if store.IsNotFound(err) || (err == nil && !p.CanAdminister() && (p.Profile == nil || r.ProfileID != p.Profile.ID)) {
		return domain.MediaRequest{}, domain.NotFound("media_request.not_found")
	}
	if err != nil {
		return domain.MediaRequest{}, err
	}
	return requestFor(p, r), nil
}

// CancelRequest withdraws a pending request of the profile; it no longer counts in the quota.
func (a *App) CancelRequest(ctx context.Context, p domain.Principal, id domain.ID) error {
	if p.Profile == nil {
		return domain.Precondition("profile.required")
	}
	var r domain.MediaRequest
	err := a.store.Write(ctx, func(q store.Q) error {
		var err error
		r, err = q.Request(ctx, id)
		if store.IsNotFound(err) || (err == nil && r.ProfileID != p.Profile.ID) {
			return domain.NotFound("media_request.not_found")
		}
		if err != nil {
			return err
		}
		if r.Status != domain.RequestPending {
			return domain.Precondition("media_request.not_pending")
		}
		return q.DeleteRequest(ctx, id)
	})
	if err != nil {
		return err
	}
	a.log.InfoContext(ctx, "request canceled", "request", id, "title", r.Title)
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityRequest, AccountID: &p.Account.ID, ProfileID: &p.Profile.ID,
		Text: domain.T("activity.request_canceled", "profile", p.Profile.Name, "title", r.Title),
	})
	a.requestsChanged(r.ProfileID, r.ID)
	return nil
}

// Approval is what an administrator may change when approving a request: nil or empty keeps what
// was requested.
type Approval struct {
	DestinationID *domain.ID
	Seasons       domain.RequestSeasons
	SeasonNumbers []int
}

// ApproveRequest approves a pending or failed request and hands it to the instance.
func (a *App) ApproveRequest(ctx context.Context, p domain.Principal, id domain.ID, ap Approval) (domain.MediaRequest, error) {
	var r domain.MediaRequest
	err := a.store.Write(ctx, func(q store.Q) error {
		var err error
		if r, err = decidable(ctx, q, id); err != nil {
			return err
		}
		if ap.Seasons != "" {
			if r.Seasons, r.SeasonNumbers, err = requestSeasons(r.Kind, ap.Seasons, ap.SeasonNumbers); err != nil {
				return err
			}
		}
		if ap.DestinationID != nil {
			d, err := q.RequestDestination(ctx, *ap.DestinationID)
			if store.IsNotFound(err) || (err == nil && d.Kind != r.Kind) {
				return domain.NotFound("media_request.destination_not_found")
			}
			if err != nil {
				return err
			}
			r.Destination = &d
		}
		if r.Destination == nil {
			return domain.Precondition("media_request.destination_required")
		}
		now := a.now()
		r.Status, r.DecidedAt, r.DecidedBy, r.UpdatedAt = domain.RequestApproved, &now, p.Account.Username, now
		r.DeclineReason, r.Error, r.Progress = "", nil, 0
		if err := q.UpdateRequest(ctx, r); errors.Is(err, store.ErrDuplicate) {
			return domain.Conflict("media_request.already_requested")
		} else if err != nil {
			return err
		}
		return a.jobs.Enqueue(ctx, q, jobSubmitRequest, r.ID.String(), priorityUser)
	})
	if err != nil {
		return domain.MediaRequest{}, err
	}
	a.jobs.Kick()
	a.log.InfoContext(ctx, "request approved", "request", id, "title", r.Title)
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityRequest, AccountID: &p.Account.ID, ProfileID: &r.ProfileID,
		Text: domain.T("activity.request_approved", "actor", p.Account.Username, "profile", r.ProfileName, "title", r.Title),
	})
	a.requestsChanged(r.ProfileID, r.ID)
	return r, nil
}

// DeclineRequest declines a pending or failed request, with a reason for the requester.
func (a *App) DeclineRequest(ctx context.Context, p domain.Principal, id domain.ID, reason string) (domain.MediaRequest, error) {
	reason = strings.TrimSpace(strings.ToValidUTF8(reason, ""))
	if utf8.RuneCountInString(reason) > maxDeclineReason {
		return domain.MediaRequest{}, domain.Invalid("media_request.reason_too_long", "max", maxDeclineReason)
	}
	var r domain.MediaRequest
	err := a.store.Write(ctx, func(q store.Q) error {
		var err error
		if r, err = decidable(ctx, q, id); err != nil {
			return err
		}
		now := a.now()
		r.Status, r.DecidedAt, r.DecidedBy, r.UpdatedAt, r.DeclineReason = domain.RequestDeclined, &now, p.Account.Username, now, reason
		return q.UpdateRequest(ctx, r)
	})
	if err != nil {
		return domain.MediaRequest{}, err
	}
	a.log.InfoContext(ctx, "request declined", "request", id, "title", r.Title)
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityRequest, AccountID: &p.Account.ID, ProfileID: &r.ProfileID,
		Text: domain.T("activity.request_declined", "actor", p.Account.Username, "profile", r.ProfileName, "title", r.Title),
	})
	a.requestsChanged(r.ProfileID, r.ID)
	return r, nil
}

// decidable reads a request an administrator can still approve or decline.
func decidable(ctx context.Context, q store.Q, id domain.ID) (domain.MediaRequest, error) {
	r, err := q.Request(ctx, id)
	if store.IsNotFound(err) {
		return r, domain.NotFound("media_request.not_found")
	}
	if err != nil {
		return r, err
	}
	if r.Status != domain.RequestPending && r.Status != domain.RequestFailed {
		return r, domain.Precondition("media_request.already_decided")
	}
	return r, nil
}

// DeleteRequest forgets a request, whatever its state; what the instance has stays there.
func (a *App) DeleteRequest(ctx context.Context, p domain.Principal, id domain.ID) error {
	var (
		r     domain.MediaRequest
		found bool
	)
	err := a.store.Write(ctx, func(q store.Q) error {
		var err error
		r, err = q.Request(ctx, id)
		if store.IsNotFound(err) {
			return nil // already gone
		}
		if err != nil {
			return err
		}
		found = true
		return q.DeleteRequest(ctx, id)
	})
	if err != nil || !found {
		return err
	}
	a.log.InfoContext(ctx, "request deleted", "request", id, "title", r.Title)
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityRequest, AccountID: &p.Account.ID,
		Text: domain.T("activity.request_deleted", "actor", p.Account.Username, "profile", r.ProfileName, "title", r.Title),
	})
	a.requestsChanged(r.ProfileID, r.ID)
	return nil
}

// requestsChanged announces changed requests to the profile that made them and to the
// administrators.
func (a *App) requestsChanged(profileID domain.ID, ids ...domain.ID) {
	a.bus.Publish(domain.RequestsChanged{ProfileID: profileID, RequestIDs: slices.Clone(ids)})
}

// Destinations.

// RequestDestinations lists the destinations the caller may request into (all of them for an
// administrator, with how each is set on the instance); kind "" lists both kinds.
func (a *App) RequestDestinations(ctx context.Context, p domain.Principal, kind domain.RequestKind) ([]domain.RequestDestination, error) {
	if kind != "" {
		if err := validRequestKind(kind); err != nil {
			return nil, err
		}
	}
	all, err := a.store.Read().RequestDestinations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.RequestDestination, 0, len(all))
	for _, d := range all {
		if (kind == "" || d.Kind == kind) && (p.CanAdminister() || p.AllowsLibrary(d.LibraryID)) {
			out = append(out, destinationView(p, d))
		}
	}
	return out, nil
}

// RequestOptions reads from the instance for a kind its root folders and quality profiles.
func (a *App) RequestOptions(ctx context.Context, p domain.Principal, kind domain.RequestKind) (domain.RequestOptions, error) {
	if err := validRequestKind(kind); err != nil {
		return domain.RequestOptions{}, err
	}
	client, ak, err := a.requestClient(ctx, kind)
	if err != nil {
		return domain.RequestOptions{}, err
	}
	roots, err := client.RootFolders(ctx)
	if err != nil {
		return domain.RequestOptions{}, a.instanceError(ctx, p, ak, err)
	}
	profiles, err := client.QualityProfiles(ctx)
	if err != nil {
		return domain.RequestOptions{}, a.instanceError(ctx, p, ak, err)
	}
	var out domain.RequestOptions
	for _, r := range roots {
		out.RootFolders = append(out.RootFolders, domain.RootFolder{Path: r.Path, FreeSpace: r.FreeSpace})
	}
	for _, q := range profiles {
		out.QualityProfiles = append(out.QualityProfiles, domain.QualityProfile{ID: q.ID, Name: q.Name})
	}
	return out, nil
}

// DestinationChanges describes a destination to create, or a change to one (nil or empty fields
// are left alone; the kind never changes).
type DestinationChanges struct {
	Name             *string
	Kind             domain.RequestKind
	LibraryID        *domain.ID
	RootFolder       *string
	QualityProfileID *int
	SeriesType       domain.SeriesType
}

// CreateRequestDestination adds a destination, after checking its library and its settings on
// the instance.
func (a *App) CreateRequestDestination(ctx context.Context, p domain.Principal, ch DestinationChanges) (domain.RequestDestination, error) {
	if err := validRequestKind(ch.Kind); err != nil {
		return domain.RequestDestination{}, err
	}
	if ch.Name == nil || ch.LibraryID == nil || ch.RootFolder == nil || ch.QualityProfileID == nil {
		return domain.RequestDestination{}, domain.Invalid("media_request.destination_incomplete")
	}
	now := a.now()
	d := domain.RequestDestination{ID: domain.NewID(), Kind: ch.Kind, SeriesType: domain.SeriesStandard, CreatedAt: now, UpdatedAt: now}
	if err := a.applyDestination(ctx, p, &d, ch); err != nil {
		return domain.RequestDestination{}, err
	}
	err := a.store.Write(ctx, func(q store.Q) error {
		all, err := q.RequestDestinations(ctx)
		if err != nil {
			return err
		}
		if len(all) >= maxDestinations {
			return domain.Precondition("media_request.too_many_destinations", "max", maxDestinations)
		}
		return destinationWriteError(q.CreateRequestDestination(ctx, d), d.Name)
	})
	if err != nil {
		return domain.RequestDestination{}, err
	}
	a.log.InfoContext(ctx, "request destination created", "destination", d.ID, "name", d.Name, "kind", d.Kind)
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityRequest, AccountID: &p.Account.ID,
		Text: domain.T("activity.request_destination_created", "actor", p.Account.Username, "name", d.Name),
	})
	return d, nil
}

// UpdateRequestDestination changes a destination. Requests already approved keep where they went.
func (a *App) UpdateRequestDestination(ctx context.Context, p domain.Principal, id domain.ID, ch DestinationChanges) (domain.RequestDestination, error) {
	d, err := a.store.Read().RequestDestination(ctx, id)
	if store.IsNotFound(err) {
		return domain.RequestDestination{}, domain.NotFound("media_request.destination_not_found")
	}
	if err != nil {
		return domain.RequestDestination{}, err
	}
	ch.Kind = d.Kind
	if err := a.applyDestination(ctx, p, &d, ch); err != nil {
		return domain.RequestDestination{}, err
	}
	d.UpdatedAt = a.now()
	if err := a.store.Write(ctx, func(q store.Q) error {
		return destinationWriteError(q.UpdateRequestDestination(ctx, d), d.Name)
	}); err != nil {
		return domain.RequestDestination{}, err
	}
	a.log.InfoContext(ctx, "request destination updated", "destination", d.ID, "name", d.Name)
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityRequest, AccountID: &p.Account.ID,
		Text: domain.T("activity.request_destination_updated", "actor", p.Account.Username, "name", d.Name),
	})
	return d, nil
}

// applyDestination checks and applies changes to a destination: a name, a library of the matching
// kind, a root folder and a quality profile the instance has.
func (a *App) applyDestination(ctx context.Context, p domain.Principal, d *domain.RequestDestination, ch DestinationChanges) error {
	if ch.Name != nil {
		name := strings.TrimSpace(*ch.Name)
		if name == "" {
			return domain.Invalid("media_request.destination_name_required")
		}
		if utf8.RuneCountInString(name) > maxDestinationName {
			return domain.Invalid("media_request.destination_name_too_long", "max", maxDestinationName)
		}
		d.Name = name
	}
	if ch.LibraryID != nil {
		lib, err := a.store.Read().Library(ctx, *ch.LibraryID)
		if store.IsNotFound(err) {
			return domain.NotFound("library.not_found", "library_id", *ch.LibraryID)
		}
		if err != nil {
			return err
		}
		if lib.Kind != d.Kind.LibraryKind() {
			return domain.Invalid("media_request.library_kind")
		}
		d.LibraryID, d.LibraryName = lib.ID, lib.Name
	}
	switch ch.SeriesType {
	case "":
	case domain.SeriesStandard, domain.SeriesAnime, domain.SeriesDaily:
		d.SeriesType = ch.SeriesType
	default:
		return domain.Invalid("media_request.invalid_series_type")
	}
	if d.Kind == domain.RequestMovie {
		d.SeriesType = domain.SeriesStandard
	}
	if ch.RootFolder == nil && ch.QualityProfileID == nil {
		return nil
	}
	opts, err := a.RequestOptions(ctx, p, d.Kind)
	if err != nil {
		return err
	}
	if ch.RootFolder != nil {
		root := strings.TrimSpace(*ch.RootFolder)
		if !slices.ContainsFunc(opts.RootFolders, func(f domain.RootFolder) bool { return f.Path == root }) {
			return domain.Invalid("media_request.unknown_root_folder", "path", root)
		}
		d.RootFolder = root
	}
	if ch.QualityProfileID != nil {
		i := slices.IndexFunc(opts.QualityProfiles, func(q domain.QualityProfile) bool { return q.ID == *ch.QualityProfileID })
		if i < 0 {
			return domain.Invalid("media_request.unknown_quality_profile", "id", *ch.QualityProfileID)
		}
		d.QualityProfileID, d.QualityProfileName = opts.QualityProfiles[i].ID, opts.QualityProfiles[i].Name
	}
	return nil
}

func destinationWriteError(err error, name string) error {
	if errors.Is(err, store.ErrDuplicate) {
		return domain.Conflict("media_request.destination_name_taken", "name", name)
	}
	return err
}

// DeleteRequestDestination removes a destination; the pending requests into it are declined.
func (a *App) DeleteRequestDestination(ctx context.Context, p domain.Principal, id domain.ID) error {
	var (
		d        domain.RequestDestination
		declined []store.DeclinedRequest
		found    bool
	)
	err := a.store.Write(ctx, func(q store.Q) error {
		var err error
		d, err = q.RequestDestination(ctx, id)
		if store.IsNotFound(err) {
			return nil // already gone
		}
		if err != nil {
			return err
		}
		found = true
		if declined, err = q.DeclinePendingRequestsTo(ctx, id, a.now()); err != nil {
			return err
		}
		return q.DeleteRequestDestination(ctx, id)
	})
	if err != nil || !found {
		return err
	}
	a.log.InfoContext(ctx, "request destination deleted", "destination", id, "name", d.Name, "declined", len(declined))
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityRequest, AccountID: &p.Account.ID,
		Text: domain.T("activity.request_destination_deleted", "actor", p.Account.Username, "name", d.Name),
	})
	for _, r := range declined {
		a.requestsChanged(r.ProfileID, r.ID)
	}
	return nil
}
