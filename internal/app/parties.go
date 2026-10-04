package app

import (
	"context"
	"crypto/rand"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/party"
	"github.com/laterna-project/laterna/internal/store"
)

// Watch parties. Groups live in memory: a server restart wipes them (creating one again takes a
// moment). A group is joined with its code. Each member plays on their own device and the group
// only shares state (internal/party).

const (
	// Group code: letters and digits that cannot be mixed up (no 0/O, no 1/I/L).
	partyCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	partyCodeLen      = 6
	maxParties        = 100
	maxPartyQueue     = 500
	maxPartyMessage   = 500
	maxPartyReaction  = 32
	// partyGrace: a member with no open stream for this long (device off, network lost) leaves the
	// group.
	partyGrace = 30 * time.Second
	// partyTickEvery is how often the group loop runs (waits that last too long, absent members).
	partyTickEvery = time.Second
	// partyBuffer is how many updates can wait per subscriber; beyond that it gets the current
	// state.
	partyBuffer = 32
)

// PartyMessage is a message from a member to the group: text, or a reaction (emoji).
type PartyMessage struct {
	MemberID domain.ID
	Name     string
	Text     string
	Reaction bool
	At       time.Time
}

// PartyUpdate is an update sent to members: the state, a message, or the end of the group for this
// member (Ended holds the reason, a "party.ended...." text).
type PartyUpdate struct {
	State   *party.State
	Message *PartyMessage
	Ended   domain.Text
}

// PartyView is a group as one of its members sees it.
type PartyView struct {
	ID   domain.ID
	Code string
	// MemberID is the caller, within the group.
	MemberID domain.ID
	State    party.State
	// Items are the queue items (those the caller can see, in order).
	Items []domain.ItemView
}

// PartyCommand is a command from a member; only one field is set.
type PartyCommand struct {
	Play, Pause bool
	Seek        *time.Duration
	Select      *int
	// Queue replaces the queue (movies, episodes, tracks, seasons, series, albums...) and jumps to
	// item QueueIndex.
	Queue      []domain.ID
	QueueIndex int
	HostOnly   *bool
}

// room is a group and its subscribers.
type room struct {
	id    domain.ID
	code  string
	title string
	now   func() time.Time

	mu    sync.Mutex
	group *party.Group
	// bySession maps each device to its member; viewers holds what each member can see.
	bySession map[domain.ID]domain.ID
	viewers   map[domain.ID]domain.Viewer
	subs      map[*PartySubscription]bool
	// seen is the last time a member without an open stream was present.
	seen map[domain.ID]time.Time
	// kicked holds the devices the host removed. They cannot come back with the code.
	kicked map[domain.ID]bool
}

// partyRooms holds the groups in progress.
type partyRooms struct {
	mu     sync.Mutex
	byID   map[domain.ID]*room
	byCode map[string]*room
}

// PartySubscription receives the updates of a group for one member.
type PartySubscription struct {
	r      *room
	member domain.ID
	ch     chan PartyUpdate
	// lagged means an update could not be delivered; the next thing sent is the current state.
	lagged bool
	closed bool
}

func newPartyCode() string {
	b := make([]byte, partyCodeLen)
	_, _ = rand.Read(b) // never returns an error (crypto/rand)
	for i := range b {
		b[i] = partyCodeAlphabet[int(b[i])%len(partyCodeAlphabet)]
	}
	return string(b)
}

// CreateParty creates a group with the caller as host, paused at the start of the first item. The
// queue follows playlist rules (a season stands for its episodes...).
func (a *App) CreateParty(ctx context.Context, p domain.Principal, itemIDs []domain.ID, hostOnly bool) (PartyView, error) {
	v, err := viewerOf(p)
	if err != nil {
		return PartyView{}, err
	}
	queue, err := a.partyQueue(ctx, v, itemIDs)
	if err != nil {
		return PartyView{}, err
	}
	first, err := a.store.Read().View(ctx, v, queue[0])
	if err != nil {
		return PartyView{}, err
	}
	now := a.now()
	member := party.Member{ID: domain.NewID(), ProfileID: v.ProfileID, Name: p.Profile.Name}
	r := &room{
		id: domain.NewID(), title: itemTitle(first), now: a.now, group: party.New(member, queue, hostOnly, now),
		bySession: map[domain.ID]domain.ID{p.SessionID: member.ID}, viewers: map[domain.ID]domain.Viewer{member.ID: v},
		subs: map[*PartySubscription]bool{}, seen: map[domain.ID]time.Time{member.ID: now}, kicked: map[domain.ID]bool{},
	}
	a.rooms.mu.Lock()
	if len(a.rooms.byID) >= maxParties {
		a.rooms.mu.Unlock()
		return PartyView{}, domain.Precondition("party.too_many", "max", maxParties)
	}
	for r.code == "" || a.rooms.byCode[r.code] != nil {
		r.code = newPartyCode()
	}
	a.rooms.byID[r.id], a.rooms.byCode[r.code] = r, r
	a.rooms.mu.Unlock()
	a.log.InfoContext(ctx, "watch party created", "party", r.id, "title", r.title, "items", len(queue))
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityPartyStarted, AccountID: &p.Account.ID, ProfileID: idPtr(v.ProfileID), ItemID: idPtr(queue[0]),
		Text: domain.T("activity.party_started", "profile", p.Profile.Name, "title", r.title),
	})
	return a.partyView(ctx, r, member.ID, v)
}

// partyQueue expands a queue like a playlist, for a profile.
func (a *App) partyQueue(ctx context.Context, v domain.Viewer, itemIDs []domain.ID) ([]domain.ID, error) {
	if len(itemIDs) == 0 {
		return nil, domain.Invalid("party.empty_queue")
	}
	queue, err := a.playable(ctx, v, itemIDs)
	if err != nil {
		return nil, err
	}
	switch {
	case len(queue) == 0:
		return nil, domain.Invalid("party.empty_queue")
	case len(queue) > maxPartyQueue:
		return nil, domain.Precondition("party.queue_too_long", "max", maxPartyQueue)
	}
	return queue, nil
}

// canSeeAll checks that a profile can see every item of a queue.
func (a *App) canSeeAll(ctx context.Context, v domain.Viewer, queue []domain.ID) error {
	read := a.store.Read()
	for _, id := range queue {
		if _, err := read.View(ctx, v, id); store.IsNotFound(err) {
			return domain.Forbidden("party.item_not_visible")
		} else if err != nil {
			return err
		}
	}
	return nil
}

// JoinParty lets the caller into a group by its code. They must be able to see everything the group
// watches. If they join during playback they catch up without stopping the others.
func (a *App) JoinParty(ctx context.Context, p domain.Principal, code string) (PartyView, error) {
	v, err := viewerOf(p)
	if err != nil {
		return PartyView{}, err
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	a.rooms.mu.Lock()
	r := a.rooms.byCode[code]
	a.rooms.mu.Unlock()
	if r == nil {
		return PartyView{}, domain.NotFound("party.unknown_code")
	}
	r.mu.Lock()
	queue := r.group.State().Queue
	id, member := r.bySession[p.SessionID]
	kicked := r.kicked[p.SessionID]
	r.mu.Unlock()
	switch {
	case member:
		return a.partyView(ctx, r, id, v)
	case kicked:
		return PartyView{}, domain.Forbidden("party.removed")
	}
	if err := a.canSeeAll(ctx, v, queue); err != nil {
		return PartyView{}, err
	}
	m := party.Member{ID: domain.NewID(), ProfileID: v.ProfileID, Name: p.Profile.Name}
	now := a.now()
	r.mu.Lock()
	if err := r.group.Join(m, now); err != nil {
		r.mu.Unlock()
		return PartyView{}, err
	}
	r.bySession[p.SessionID], r.viewers[m.ID], r.seen[m.ID] = m.ID, v, now
	r.broadcastState()
	r.mu.Unlock()
	a.log.InfoContext(ctx, "watch party joined", "party", r.id, "profile", v.ProfileID)
	return a.partyView(ctx, r, m.ID, v)
}

// memberRoom finds group id and the caller's member in it.
func (a *App) memberRoom(p domain.Principal, id domain.ID) (*room, domain.ID, error) {
	a.rooms.mu.Lock()
	r := a.rooms.byID[id]
	a.rooms.mu.Unlock()
	if r != nil {
		r.mu.Lock()
		m, ok := r.bySession[p.SessionID]
		r.mu.Unlock()
		if ok {
			return r, m, nil
		}
	}
	return nil, domain.ID{}, domain.NotFound("party.not_found")
}

// GetParty returns a group the caller belongs to.
func (a *App) GetParty(ctx context.Context, p domain.Principal, id domain.ID) (PartyView, error) {
	v, err := viewerOf(p)
	if err != nil {
		return PartyView{}, err
	}
	r, m, err := a.memberRoom(p, id)
	if err != nil {
		return PartyView{}, err
	}
	return a.partyView(ctx, r, m, v)
}

func (a *App) partyView(ctx context.Context, r *room, member domain.ID, v domain.Viewer) (PartyView, error) {
	r.mu.Lock()
	st := r.group.State()
	r.mu.Unlock()
	pv := PartyView{ID: r.id, Code: r.code, MemberID: member, State: st}
	read := a.store.Read()
	for _, id := range st.Queue {
		view, err := read.View(ctx, v, id)
		if store.IsNotFound(err) {
			continue
		}
		if err != nil {
			return PartyView{}, err
		}
		pv.Items = append(pv.Items, view)
	}
	return pv, nil
}

// WatchParty opens the update stream of a group for the caller. The first update is the group's
// state.
func (a *App) WatchParty(p domain.Principal, id domain.ID) (*PartySubscription, error) {
	r, m, err := a.memberRoom(p, id)
	if err != nil {
		return nil, err
	}
	s := &PartySubscription{r: r, member: m, ch: make(chan PartyUpdate, partyBuffer)}
	r.mu.Lock()
	st := r.group.State()
	s.ch <- PartyUpdate{State: &st}
	r.subs[s] = true
	r.mu.Unlock()
	return s, nil
}

// Next waits for the next update. After a loss (subscriber too slow) it is the current state.
func (s *PartySubscription) Next(ctx context.Context) (PartyUpdate, error) {
	s.r.mu.Lock()
	if s.lagged {
		s.lagged = false
		st := s.r.group.State()
		s.r.mu.Unlock()
		return PartyUpdate{State: &st}, nil
	}
	s.r.mu.Unlock()
	select {
	case <-ctx.Done():
		return PartyUpdate{}, ctx.Err()
	case u, ok := <-s.ch:
		if !ok {
			return PartyUpdate{}, ErrSubscriptionClosed
		}
		return u, nil
	}
}

// Close closes the stream. The member stays in the group long enough to come back (partyGrace).
func (s *PartySubscription) Close() {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	if !s.closed {
		delete(s.r.subs, s)
		s.closed = true
		close(s.ch)
	}
	if _, ok := s.r.viewers[s.member]; ok {
		s.r.seen[s.member] = s.r.now()
	}
}

// send delivers an update to a subscriber without ever blocking the group (r.mu held).
func (s *PartySubscription) send(u PartyUpdate) {
	if s.closed {
		return
	}
	select {
	case s.ch <- u:
	default:
		s.lagged = true
	}
}

// broadcastState sends the state to every subscriber (r.mu held).
func (r *room) broadcastState() {
	st := r.group.State()
	for s := range r.subs {
		s.send(PartyUpdate{State: &st})
	}
}

// endFor ends the stream of a member's subscribers, with a reason (r.mu held).
func (r *room) endFor(member domain.ID, reason domain.Text) {
	for s := range r.subs {
		if s.member == member {
			s.send(PartyUpdate{Ended: reason})
			delete(r.subs, s)
			s.closed = true
			close(s.ch)
		}
	}
}

// removeMember forgets a member of the group (r.mu held); true if the group is now empty.
func (r *room) removeMember(member domain.ID, now time.Time) bool {
	for session, m := range r.bySession {
		if m == member {
			delete(r.bySession, session)
		}
	}
	delete(r.viewers, member)
	delete(r.seen, member)
	return r.group.Leave(member, now)
}

// ControlParty applies a member's command and returns the new state.
func (a *App) ControlParty(ctx context.Context, p domain.Principal, id domain.ID, cmd PartyCommand) (party.State, error) {
	v, err := viewerOf(p)
	if err != nil {
		return party.State{}, err
	}
	r, m, err := a.memberRoom(p, id)
	if err != nil {
		return party.State{}, err
	}
	var queue []domain.ID
	if cmd.Queue != nil {
		if queue, err = a.partyQueue(ctx, v, cmd.Queue); err != nil {
			return party.State{}, err
		}
		// Everyone must be able to see the new queue.
		r.mu.Lock()
		viewers := make([]domain.Viewer, 0, len(r.viewers))
		for _, mv := range r.viewers {
			viewers = append(viewers, mv)
		}
		r.mu.Unlock()
		for _, mv := range viewers {
			if err := a.canSeeAll(ctx, mv, queue); err != nil {
				return party.State{}, domain.Forbidden("party.item_not_visible_to_member")
			}
		}
	}
	now := a.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	g := r.group
	switch {
	case cmd.Play:
		err = g.Play(m, now)
	case cmd.Pause:
		err = g.Pause(m, now)
	case cmd.Seek != nil:
		err = g.Seek(m, *cmd.Seek, now)
	case cmd.Select != nil:
		err = g.Select(m, *cmd.Select, now)
	case queue != nil:
		err = g.SetQueue(m, queue, cmd.QueueIndex, now)
	case cmd.HostOnly != nil:
		err = g.SetHostOnly(m, *cmd.HostOnly)
	default:
		err = domain.Invalid("party.command_required")
	}
	if err != nil {
		return party.State{}, err
	}
	r.broadcastState()
	return g.State(), nil
}

// ReportPartyStatus records the state of the caller's device: ready, buffering, or done with item
// ended (the group moves to the next one).
func (a *App) ReportPartyStatus(p domain.Principal, id domain.ID, ready, buffering bool, ended *int) error {
	r, m, err := a.memberRoom(p, id)
	if err != nil {
		return err
	}
	now := a.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	before := r.group.State().Version
	if ended != nil {
		err = r.group.Ended(m, *ended, now)
	} else {
		err = r.group.Report(m, ready, buffering, now)
	}
	if err == nil && r.group.State().Version != before {
		r.broadcastState()
	}
	return err
}

// SendPartyMessage sends a message (or a reaction) from the caller to the whole group. Nothing is
// stored.
func (a *App) SendPartyMessage(p domain.Principal, id domain.ID, text string, reaction bool) error {
	r, m, err := a.memberRoom(p, id)
	if err != nil {
		return err
	}
	text = strings.TrimSpace(strings.ToValidUTF8(text, "�"))
	limit := maxPartyMessage
	if reaction {
		limit = maxPartyReaction
	}
	switch {
	case text == "":
		return domain.Invalid("party.message_empty")
	case utf8.RuneCountInString(text) > limit:
		return domain.Invalid("party.message_too_long", "max", limit)
	case strings.ContainsFunc(text, func(r rune) bool { return unicode.IsControl(r) && r != '\n' }):
		return domain.Invalid("party.message_invalid")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var name string
	for _, mb := range r.group.State().Members {
		if mb.ID == m {
			name = mb.Name
		}
	}
	msg := &PartyMessage{MemberID: m, Name: name, Text: text, Reaction: reaction, At: a.now()}
	for s := range r.subs {
		s.send(PartyUpdate{Message: msg})
	}
	return nil
}

// LeaveParty takes the caller out of a group. The last one to leave ends it.
func (a *App) LeaveParty(p domain.Principal, id domain.ID) error {
	r, m, err := a.memberRoom(p, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.endFor(m, domain.T("party.ended.left"))
	empty := r.removeMember(m, a.now())
	if !empty {
		r.broadcastState()
	}
	r.mu.Unlock()
	if empty {
		a.closeRoom(r, domain.Text{})
	}
	return nil
}

// KickPartyMember removes a member from the group, at the host's request.
func (a *App) KickPartyMember(p domain.Principal, id, member domain.ID) error {
	r, m, err := a.memberRoom(p, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.group.Kick(m, member, a.now()); err != nil {
		return err
	}
	r.endFor(member, domain.T("party.ended.removed"))
	for session, mb := range r.bySession {
		if mb == member {
			delete(r.bySession, session)
			r.kicked[session] = true
		}
	}
	delete(r.viewers, member)
	delete(r.seen, member)
	r.broadcastState()
	return nil
}

// EndParty ends a group for everyone, at the host's request.
func (a *App) EndParty(p domain.Principal, id domain.ID) error {
	r, m, err := a.memberRoom(p, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	host := false
	for _, mb := range r.group.State().Members {
		if mb.ID == m {
			host = mb.Host
		}
	}
	r.mu.Unlock()
	if !host {
		return domain.Forbidden("party.host_only")
	}
	a.closeRoom(r, domain.T("party.ended.by_host"))
	return nil
}

// closeRoom forgets a group. Its subscribers get the reason (nothing is sent if it is empty).
func (a *App) closeRoom(r *room, reason domain.Text) {
	a.rooms.mu.Lock()
	delete(a.rooms.byID, r.id)
	delete(a.rooms.byCode, r.code)
	a.rooms.mu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	for s := range r.subs {
		if !reason.IsZero() {
			s.send(PartyUpdate{Ended: reason})
		}
		delete(r.subs, s)
		s.closed = true
		close(s.ch)
	}
	a.log.Info("watch party ended", "party", r.id, "title", r.title)
}

// watchParties moves groups forward: waits that last too long, members who left without saying so.
func (a *App) watchParties(ctx context.Context) {
	t := time.NewTicker(partyTickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.tickParties(a.now())
		}
	}
}

// tickParties is one round of the group loop.
func (a *App) tickParties(now time.Time) {
	a.rooms.mu.Lock()
	rooms := make([]*room, 0, len(a.rooms.byID))
	for _, r := range a.rooms.byID {
		rooms = append(rooms, r)
	}
	a.rooms.mu.Unlock()
	for _, r := range rooms {
		r.mu.Lock()
		watching := map[domain.ID]bool{}
		for s := range r.subs {
			watching[s.member] = true
		}
		changed, empty := false, false
		for member, seen := range r.seen {
			if !watching[member] && now.Sub(seen) >= partyGrace {
				empty = r.removeMember(member, now)
				changed = true
			} else if watching[member] {
				r.seen[member] = now
			}
		}
		if !empty && (r.group.Tick(now) || changed) {
			r.broadcastState()
		}
		r.mu.Unlock()
		if empty {
			a.closeRoom(r, domain.Text{})
		}
	}
}
