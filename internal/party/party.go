// Package party holds the state of a watch party: the queue, playing or paused, the reference
// position, and who is waiting for whom. Pure logic: the caller gives the time, nothing is read or
// written, and app broadcasts the state after every change.
//
// Each member plays on their own device, with their own playback session (direct or transcoded).
// The group only shares state. A playing state says "at time At (server clock) the position is
// Position, and it has been moving since": each device works out where it should be and adjusts.
package party

import (
	"slices"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// Status is what the group is doing.
type Status string

// Group statuses.
const (
	Paused  Status = "paused"
	Playing Status = "playing"
	// Waiting means the group waits for its members to be ready (item loaded, seek, a member
	// buffering). It then goes on as Resume says.
	Waiting Status = "waiting"
)

// Sync settings.
const (
	// Lead is the delay before playback starts. Everyone gets the state before the start time and
	// they all start at the same instant.
	Lead = 800 * time.Millisecond
	// WaitLimit is how long the group waits. After that it goes on without the members that are not
	// ready; they will catch up.
	WaitLimit = 15 * time.Second
	// MaxMembers caps the size of a group.
	MaxMembers = 20
)

// Member is a member of a group: a device (session) on a profile.
type Member struct {
	ID        domain.ID
	ProfileID domain.ID
	Name      string
	Host      bool
	// Ready means the member has loaded the current item at the requested position since the last
	// wait.
	Ready bool
	// Buffering means the member's playback has stalled.
	Buffering bool
	// Synced means the member has been ready at least once. A member who joins during playback
	// catches up without stopping the group; after that the group waits for them like anyone else.
	Synced   bool
	JoinedAt time.Time
}

// State is the state of a group as its members receive it.
type State struct {
	Queue []domain.ID
	Index int
	// Status is what the group is doing, Resume what it will do after a wait.
	Status Status
	Resume Status
	// Position at time At (server clock). While playing it moves on from At, which may be slightly
	// in the future (start delayed by Lead).
	Position time.Duration
	At       time.Time
	Members  []Member
	// HostOnly means only the host controls playback (play, pause, seek, queue).
	HostOnly bool
	// Version grows with every change. A device drops a state older than the one it has.
	Version int64
}

// Group is a watch party.
type Group struct {
	st        State
	waitSince time.Time
}

// New creates a group, paused at the start of the first queue item.
func New(host Member, queue []domain.ID, hostOnly bool, now time.Time) *Group {
	host.Host, host.Synced, host.JoinedAt = true, true, now
	return &Group{st: State{
		Queue: slices.Clone(queue), Status: Paused, Resume: Paused, At: now, Members: []Member{host},
		HostOnly: hostOnly, Version: 1,
	}}
}

// State returns a copy of the state.
func (g *Group) State() State {
	st := g.st
	st.Queue = slices.Clone(g.st.Queue)
	st.Members = slices.Clone(g.st.Members)
	return st
}

// Current returns the current item.
func (g *Group) Current() domain.ID { return g.st.Queue[g.st.Index] }

// Position returns the group's position at time now.
func (g *Group) Position(now time.Time) time.Duration {
	if g.st.Status == Playing && now.After(g.st.At) {
		return g.st.Position + now.Sub(g.st.At)
	}
	return g.st.Position
}

func (g *Group) member(id domain.ID) int {
	return slices.IndexFunc(g.st.Members, func(m Member) bool { return m.ID == id })
}

// allowed checks that a member may control the group.
func (g *Group) allowed(by domain.ID) error {
	i := g.member(by)
	switch {
	case i < 0:
		return domain.NotFound("party.not_found")
	case g.st.HostOnly && !g.st.Members[i].Host:
		return domain.Forbidden("party.host_only")
	}
	return nil
}

func (g *Group) changed() { g.st.Version++ }

// Join adds a member, or finds them again if they come back. A member who joins during playback
// catches up without stopping the group.
func (g *Group) Join(m Member, now time.Time) error {
	if i := g.member(m.ID); i >= 0 {
		return nil
	}
	if len(g.st.Members) >= MaxMembers {
		return domain.Precondition("party.full", "max", MaxMembers)
	}
	m.Host, m.Ready, m.Buffering, m.JoinedAt = false, false, false, now
	m.Synced = g.st.Status != Playing
	g.st.Members = append(g.st.Members, m)
	g.changed()
	g.settle(now)
	return nil
}

// Leave removes a member. If it was the host, the oldest member takes over. empty reports an empty
// group.
func (g *Group) Leave(id domain.ID, now time.Time) (empty bool) {
	i := g.member(id)
	if i < 0 {
		return len(g.st.Members) == 0
	}
	host := g.st.Members[i].Host
	g.st.Members = slices.Delete(g.st.Members, i, i+1)
	if len(g.st.Members) == 0 {
		g.changed()
		return true
	}
	if host {
		g.st.Members[0].Host = true // members are kept in join order
	}
	g.changed()
	g.settle(now)
	return false
}

// Kick removes a member on the host's request.
func (g *Group) Kick(by, id domain.ID, now time.Time) error {
	i := g.member(by)
	if i < 0 || !g.st.Members[i].Host {
		return domain.Forbidden("party.host_only")
	}
	if by == id {
		return domain.Invalid("party.host_cannot_remove_self")
	}
	if g.member(id) < 0 {
		return domain.NotFound("party.member_not_found")
	}
	g.Leave(id, now)
	return nil
}

// SetHostOnly restricts controls to the host, or opens them again.
func (g *Group) SetHostOnly(by domain.ID, hostOnly bool) error {
	i := g.member(by)
	if i < 0 || !g.st.Members[i].Host {
		return domain.Forbidden("party.host_only")
	}
	if g.st.HostOnly != hostOnly {
		g.st.HostOnly = hostOnly
		g.changed()
	}
	return nil
}

// Play resumes playback: once the members that are not ready have loaded, or right away (start
// delayed by Lead).
func (g *Group) Play(by domain.ID, now time.Time) error {
	if err := g.allowed(by); err != nil {
		return err
	}
	switch g.st.Status {
	case Playing:
		return nil
	case Waiting:
		g.st.Resume = Playing
		g.changed()
		g.settle(now)
	case Paused:
		g.wait(Playing, g.st.Position, now)
	}
	return nil
}

// Pause stops playback where it is.
func (g *Group) Pause(by domain.ID, now time.Time) error {
	if err := g.allowed(by); err != nil {
		return err
	}
	switch g.st.Status {
	case Paused:
		return nil
	case Waiting:
		g.st.Resume = Paused
	case Playing:
		g.st.Position, g.st.Status, g.st.Resume = g.Position(now), Paused, Paused
	}
	g.st.At = now
	g.changed()
	return nil
}

// Seek moves the group: everyone seeks, then the group goes on as before.
func (g *Group) Seek(by domain.ID, position time.Duration, now time.Time) error {
	if err := g.allowed(by); err != nil {
		return err
	}
	if position < 0 {
		return domain.Invalid("request.negative_position")
	}
	g.wait(g.intent(), position, now)
	return nil
}

// Select jumps to a queue item (next, previous or any other), from its start.
func (g *Group) Select(by domain.ID, index int, now time.Time) error {
	if err := g.allowed(by); err != nil {
		return err
	}
	if index < 0 || index >= len(g.st.Queue) {
		return domain.Invalid("party.index_out_of_queue", "index", index)
	}
	g.st.Index = index
	g.wait(g.intent(), 0, now)
	return nil
}

// SetQueue replaces the queue and jumps to item index, from its start.
func (g *Group) SetQueue(by domain.ID, queue []domain.ID, index int, now time.Time) error {
	if err := g.allowed(by); err != nil {
		return err
	}
	if len(queue) == 0 || index < 0 || index >= len(queue) {
		return domain.Invalid("party.index_out_of_queue", "index", index)
	}
	g.st.Queue, g.st.Index = slices.Clone(queue), index
	g.wait(g.intent(), 0, now)
	return nil
}

// Ended reports that item index finished for a member: the group moves to the next item, or stops
// at the end of the queue. A report for an item that is no longer current (several members finish
// around the same time) does nothing.
func (g *Group) Ended(by domain.ID, index int, now time.Time) error {
	if g.member(by) < 0 {
		return domain.NotFound("party.not_found")
	}
	if index != g.st.Index || g.st.Status != Playing {
		return nil
	}
	if index+1 < len(g.st.Queue) {
		g.st.Index++
		g.wait(Playing, 0, now)
		return nil
	}
	g.st.Position, g.st.Status, g.st.Resume, g.st.At = g.Position(now), Paused, Paused, now
	g.changed()
	return nil
}

// Report records a member's state: ready (item loaded at the requested position) or buffering. A
// member who buffers during playback stops the group, which waits for them.
func (g *Group) Report(id domain.ID, ready, buffering bool, now time.Time) error {
	i := g.member(id)
	if i < 0 {
		return domain.NotFound("party.not_found")
	}
	m := &g.st.Members[i]
	before := *m
	m.Ready, m.Buffering = ready && !buffering, buffering
	if m.Ready {
		m.Synced = true
	}
	if *m == before {
		return nil
	}
	g.changed()
	if buffering && m.Synced && g.st.Status == Playing {
		g.wait(Playing, g.Position(now), now)
		return nil
	}
	g.settle(now)
	return nil
}

// Tick moves time forward: a wait that lasted too long ends without the stragglers. changed reports
// a new state to broadcast.
func (g *Group) Tick(now time.Time) (changed bool) {
	v := g.st.Version
	g.settle(now)
	return g.st.Version != v
}

// intent is what the group is doing, or will do: playing or paused.
func (g *Group) intent() Status {
	if g.st.Status == Waiting {
		return g.st.Resume
	}
	return g.st.Status
}

// wait makes the group wait for all its members at position, before going on as resume.
func (g *Group) wait(resume Status, position time.Duration, now time.Time) {
	g.st.Status, g.st.Resume, g.st.Position, g.st.At = Waiting, resume, position, now
	for i := range g.st.Members {
		g.st.Members[i].Ready = false
	}
	g.waitSince = now
	g.changed()
	g.settle(now)
}

// settle ends the wait once every synced member is ready, or after WaitLimit.
func (g *Group) settle(now time.Time) {
	if g.st.Status != Waiting {
		return
	}
	ready := !slices.ContainsFunc(g.st.Members, func(m Member) bool { return m.Synced && (!m.Ready || m.Buffering) })
	if !ready && now.Sub(g.waitSince) < WaitLimit {
		return
	}
	g.st.Status = g.st.Resume
	g.st.At = now
	if g.st.Status == Playing {
		g.st.At = now.Add(Lead)
	}
	g.changed()
}
