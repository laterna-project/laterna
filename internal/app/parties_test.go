package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/party"
)

// nextUpdate waits for the next update of a group (one second at most).
func nextUpdate(t *testing.T, s *PartySubscription) PartyUpdate {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	u, err := s.Next(ctx)
	if err != nil {
		t.Fatalf("want an update: %v", err)
	}
	return u
}

// nextState skips messages until the next state.
func nextState(t *testing.T, s *PartySubscription) party.State {
	t.Helper()
	for {
		if u := nextUpdate(t, s); u.State != nil {
			return *u.State
		}
	}
}

// Watch party: Chloé and Léa, each on their own device.
func TestParties(t *testing.T) {
	a, c := startMediaApp(t)
	_, chloe := setupAdmin(t, a)
	ctx := context.Background()
	films, err := a.CreateLibrary(ctx, "Films", domain.LibraryMovies, []string{testRoot("Films")}, "")
	mustNil(t, err)
	waitIdle(t, a)
	_, err = a.CreateAccount(ctx, chloe, NewAccount{Username: "Léa", Password: "a-password"})
	mustNil(t, err)
	_, lea := login(t, a, "Léa", "a-password")
	movies, err := a.ListMovies(ctx, chloe, ListQuery{Sort: domain.SortTitle})
	mustNil(t, err)
	big, two := movies.Items[0].Item.ID, movies.Items[1].Item.ID

	pv, err := a.CreateParty(ctx, chloe, []domain.ID{big, two}, false)
	mustNil(t, err)
	if len(pv.Code) != 6 || pv.State.Status != party.Paused || len(pv.Items) != 2 || len(pv.State.Members) != 1 || !pv.State.Members[0].Host {
		t.Fatalf("group created: %+v", pv)
	}
	subC, err := a.WatchParty(chloe, pv.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer subC.Close()
	if st := nextState(t, subC); st.Version != pv.State.Version {
		t.Fatalf("first state: %+v", st)
	}

	// Léa joins with the code (case does not matter).
	lv, err := a.JoinParty(ctx, lea, strings.ToLower(pv.Code))
	mustNil(t, err)
	if lv.ID != pv.ID || lv.MemberID == pv.MemberID || len(lv.State.Members) != 2 {
		t.Fatalf("Léa joining: %+v", lv)
	}
	if st := nextState(t, subC); len(st.Members) != 2 || st.Members[1].Name != "Léa" {
		t.Fatalf("Chloé sees Léa join: %+v", st.Members)
	}
	subL, err := a.WatchParty(lea, pv.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer subL.Close()
	nextState(t, subL)

	// Play: each device loads, reports ready, and they all start together.
	st, err := a.ControlParty(ctx, chloe, pv.ID, PartyCommand{Play: true})
	mustNil(t, err)
	if st.Status != party.Waiting {
		t.Fatalf("play requested: %+v", st)
	}
	mustNil(t, a.ReportPartyStatus(chloe, pv.ID, true, false, nil))
	mustNil(t, a.ReportPartyStatus(lea, pv.ID, true, false, nil))
	for st.Status != party.Playing {
		st = nextState(t, subL)
	}
	if !st.At.Equal(c.now().Add(party.Lead)) {
		t.Errorf("common start: %v", st.At)
	}

	// A message and a reaction, received by everyone.
	mustNil(t, a.SendPartyMessage(lea, pv.ID, "  Salut !  ", false))
	for {
		if u := nextUpdate(t, subC); u.Message != nil {
			if u.Message.Text != "Salut !" || u.Message.Name != "Léa" || u.Message.Reaction {
				t.Errorf("message: %+v", u.Message)
			}
			break
		}
	}
	if err := a.SendPartyMessage(lea, pv.ID, strings.Repeat("😂", 40), true); !isKind(err, domain.ErrInvalid) {
		t.Errorf("reaction too long: %v", err)
	}

	// Léa buffering makes Chloé wait.
	mustNil(t, a.ReportPartyStatus(lea, pv.ID, false, true, nil))
	if st := nextState(t, subC); st.Status != party.Waiting {
		t.Fatalf("waiting for Léa: %+v", st)
	}
	mustNil(t, a.ReportPartyStatus(chloe, pv.ID, true, false, nil))
	mustNil(t, a.ReportPartyStatus(lea, pv.ID, true, false, nil))

	// The movie ends on Chloé's side: the group moves to the next one.
	zero := 0
	mustNil(t, a.ReportPartyStatus(chloe, pv.ID, false, false, &zero))
	got, err := a.GetParty(ctx, lea, pv.ID)
	mustNil(t, err)
	if got.State.Index != 1 || got.State.Status != party.Waiting {
		t.Fatalf("next item: %+v", got.State)
	}

	// An account that cannot see these movies cannot join.
	_, err = a.CreateAccount(ctx, chloe, NewAccount{
		Username: "Tom", Password: "a-password", Libraries: &domain.LibraryAccess{IDs: []domain.ID{}},
	})
	mustNil(t, err)
	_, tom := login(t, a, "Tom", "a-password")
	if _, err := a.JoinParty(ctx, tom, pv.Code); !isKind(err, domain.ErrForbidden) {
		t.Errorf("joining without access: %v", err)
	}
	if _, err := a.JoinParty(ctx, tom, "ZZZZZZ"); !isKind(err, domain.ErrNotFound) {
		t.Errorf("unknown code: %v", err)
	}
	_ = films

	// The host kicks Léa: her stream ends, with the reason.
	mustNil(t, a.KickPartyMember(chloe, pv.ID, lv.MemberID))
	for {
		u := nextUpdate(t, subL)
		if !u.Ended.IsZero() {
			if u.Ended.Key != "party.ended.removed" {
				t.Errorf("reason: %v", u.Ended)
			}
			break
		}
	}
	if _, err := subL.Next(ctx); !errors.Is(err, ErrSubscriptionClosed) {
		t.Errorf("Léa's stream: %v", err)
	}
	if _, err := a.GetParty(ctx, lea, pv.ID); !isKind(err, domain.ErrNotFound) {
		t.Errorf("Léa is no longer a member: %v", err)
	}
	if _, err := a.JoinParty(ctx, lea, pv.Code); !isKind(err, domain.ErrForbidden) {
		t.Errorf("Léa coming back with the code: %v", err)
	}

	// Chloé closes her stream and does not come back: after 30 s the group ends.
	subC.Close()
	a.tickParties(c.now().Add(10 * time.Second))
	if _, err := a.GetParty(ctx, chloe, pv.ID); err != nil {
		t.Fatalf("group ended too early: %v", err)
	}
	a.tickParties(c.now().Add(partyGrace + time.Second))
	if _, err := a.GetParty(ctx, chloe, pv.ID); !isKind(err, domain.ErrNotFound) {
		t.Errorf("abandoned group: %v", err)
	}

	page, err := a.Activity(ctx, ActivityQuery{})
	mustNil(t, err)
	found := false
	for _, e := range page.Entries {
		found = found || (e.Kind == domain.ActivityPartyStarted && e.Text.Params["title"] == "Big Test Movie")
	}
	if !found {
		t.Error("watch party missing from the activity log")
	}
}
