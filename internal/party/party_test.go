package party

import (
	"errors"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

var t0 = time.Date(2026, 9, 30, 20, 0, 0, 0, time.UTC)

func member(name string) Member {
	return Member{ID: domain.NewID(), ProfileID: domain.NewID(), Name: name}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func isKind(err error, kind error) bool { return errors.Is(err, kind) }

func TestGroupPlaysTogether(t *testing.T) {
	a, b := member("Alice"), member("Bob")
	film, episode := domain.NewID(), domain.NewID()
	g := New(a, []domain.ID{film, episode}, false, t0)
	if st := g.State(); st.Status != Paused || st.Position != 0 || !st.Members[0].Host || g.Current() != film {
		t.Fatalf("new group: %+v", st)
	}

	// Play: wait for the host to load, then start after Lead.
	must(t, g.Play(a.ID, t0))
	if st := g.State(); st.Status != Waiting || st.Resume != Playing {
		t.Fatalf("waiting for the host to load: %+v", st)
	}
	now := t0.Add(2 * time.Second)
	must(t, g.Report(a.ID, true, false, now))
	st := g.State()
	if st.Status != Playing || !st.At.Equal(now.Add(Lead)) || g.Position(now.Add(Lead+10*time.Second)) != 10*time.Second {
		t.Fatalf("start: %+v", st)
	}

	// Joining during playback: Bob catches up without stopping the group.
	now = now.Add(Lead + 60*time.Second)
	must(t, g.Join(b, now))
	must(t, g.Report(b.ID, false, true, now))
	if g.State().Status != Playing {
		t.Fatalf("a newcomer stops the group: %+v", g.State())
	}
	must(t, g.Report(b.ID, true, false, now.Add(3*time.Second)))
	// After that, his buffering makes everyone wait.
	now = now.Add(10 * time.Second)
	must(t, g.Report(b.ID, false, true, now))
	if st := g.State(); st.Status != Waiting || st.Resume != Playing || st.Position != 70*time.Second {
		t.Fatalf("member buffering: %+v", st)
	}
	must(t, g.Report(a.ID, true, false, now.Add(time.Second)))
	if g.State().Status != Waiting {
		t.Fatal("went on without Bob")
	}
	now = now.Add(2 * time.Second)
	must(t, g.Report(b.ID, true, false, now))
	if st := g.State(); st.Status != Playing || st.Position != 70*time.Second || !st.At.Equal(now.Add(Lead)) {
		t.Fatalf("resuming together: %+v", st)
	}

	// Seek: everyone seeks, then the group goes on.
	must(t, g.Seek(b.ID, 20*time.Minute, now.Add(5*time.Second)))
	must(t, g.Report(a.ID, true, false, now.Add(6*time.Second)))
	must(t, g.Report(b.ID, true, false, now.Add(7*time.Second)))
	if st := g.State(); st.Status != Playing || st.Position != 20*time.Minute {
		t.Fatalf("seek: %+v", st)
	}

	// Pause: wherever the group is.
	now = now.Add(7*time.Second + Lead + 30*time.Second)
	must(t, g.Pause(a.ID, now))
	if st := g.State(); st.Status != Paused || st.Position != 20*time.Minute+30*time.Second {
		t.Fatalf("pause: %+v", st)
	}

	// End of the item: the next one, from its start. A late report does nothing.
	must(t, g.Play(a.ID, now))
	must(t, g.Report(a.ID, true, false, now))
	must(t, g.Report(b.ID, true, false, now))
	must(t, g.Ended(b.ID, 0, now.Add(time.Hour)))
	must(t, g.Ended(a.ID, 0, now.Add(time.Hour)))
	if st := g.State(); st.Index != 1 || st.Status != Waiting || st.Resume != Playing || st.Position != 0 {
		t.Fatalf("next item: %+v", st)
	}
	if g.Current() != episode {
		t.Fatal("wrong current item")
	}
}

func TestGroupWaitLimitAndHost(t *testing.T) {
	a, b, c := member("Alice"), member("Bob"), member("Tom")
	g := New(a, []domain.ID{domain.NewID()}, true, t0)
	must(t, g.Join(b, t0))
	must(t, g.Join(c, t0))
	// Only the host controls.
	if err := g.Play(b.ID, t0); !isKind(err, domain.ErrForbidden) {
		t.Fatalf("control by a member: %v", err)
	}
	must(t, g.Play(a.ID, t0))
	must(t, g.Report(a.ID, true, false, t0))
	must(t, g.Report(b.ID, true, false, t0))
	// Tom does not answer: the group goes on without him after WaitLimit.
	if g.Tick(t0.Add(WaitLimit - time.Second)) {
		t.Fatal("went on too early")
	}
	if !g.Tick(t0.Add(WaitLimit)) || g.State().Status != Playing {
		t.Fatalf("endless wait: %+v", g.State())
	}
	// The host leaves: the oldest member takes over.
	if g.Leave(a.ID, t0.Add(time.Minute)) {
		t.Fatal("group wrongly empty")
	}
	st := g.State()
	if len(st.Members) != 2 || !st.Members[0].Host || st.Members[0].ID != b.ID {
		t.Fatalf("new host: %+v", st.Members)
	}
	must(t, g.SetHostOnly(b.ID, false))
	must(t, g.Pause(c.ID, t0.Add(time.Minute)))
	if err := g.Kick(c.ID, b.ID, t0); !isKind(err, domain.ErrForbidden) {
		t.Errorf("kick by a member: %v", err)
	}
	must(t, g.Kick(b.ID, c.ID, t0.Add(2*time.Minute)))
	if !g.Leave(b.ID, t0.Add(3*time.Minute)) {
		t.Error("want an empty group")
	}
}

func TestGroupLimits(t *testing.T) {
	a := member("Alice")
	g := New(a, []domain.ID{domain.NewID()}, false, t0)
	for range MaxMembers - 1 {
		must(t, g.Join(member("x"), t0))
	}
	if err := g.Join(member("one too many"), t0); !isKind(err, domain.ErrPrecondition) {
		t.Errorf("full group: %v", err)
	}
	if err := g.Select(a.ID, 3, t0); !isKind(err, domain.ErrInvalid) {
		t.Errorf("item outside the queue: %v", err)
	}
	if err := g.Seek(a.ID, -time.Second, t0); !isKind(err, domain.ErrInvalid) {
		t.Errorf("negative position: %v", err)
	}
	if err := g.Play(domain.NewID(), t0); !isKind(err, domain.ErrNotFound) {
		t.Errorf("control by a stranger: %v", err)
	}
	v := g.State().Version
	must(t, g.Join(a, t0)) // already a member: nothing changes
	if g.State().Version != v {
		t.Error("a returning member counted as a change")
	}
}
