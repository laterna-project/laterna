package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

func TestActivityLog(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	acc := newAccount("Chloé", true)
	mustWrite(t, st, func(q Q) error { return q.CreateAccount(ctx, acc, "h") })
	mustWrite(t, st, func(q Q) error {
		for i := range 5 {
			e := domain.Activity{At: t0.Add(time.Duration(i) * time.Hour), Kind: domain.ActivityLogin, Text: domain.T("activity.login", "username", string(rune('A'+i)))}
			if i%2 == 0 {
				e.AccountID = &acc.ID
			}
			if i == 3 {
				e.Warning, e.Kind = true, domain.ActivityLoginFailed
			}
			if err := q.AddActivity(ctx, e); err != nil {
				return err
			}
		}
		return nil
	})
	read := st.Read()
	page, err := read.Activity(ctx, ActivityQuery{Limit: 2})
	if err != nil || len(page) != 2 || page[0].Text.String() != "activity.login (username=E)" || !page[0].At.Equal(t0.Add(4*time.Hour)) {
		t.Fatalf("first page: %+v %v", page, err)
	}
	next, _ := read.Activity(ctx, ActivityQuery{Before: page[1].ID, Limit: 10})
	if len(next) != 3 || next[0].Text.String() != "activity.login (username=C)" {
		t.Errorf("next page: %+v", next)
	}
	if w, _ := read.Activity(ctx, ActivityQuery{WarningsOnly: true, Limit: 10}); len(w) != 1 || w[0].Kind != domain.ActivityLoginFailed {
		t.Errorf("warnings: %+v", w)
	}
	if mine, _ := read.Activity(ctx, ActivityQuery{AccountID: &acc.ID, Limit: 10}); len(mine) != 3 {
		t.Errorf("by account: %d", len(mine))
	}
	// The deleted account goes away from the entries; the entries stay.
	mustWrite(t, st, func(q Q) error { return q.DeleteAccount(ctx, acc.ID) })
	if all, _ := read.Activity(ctx, ActivityQuery{Limit: 10}); len(all) != 5 || all[0].AccountID != nil {
		t.Errorf("after deleting the account: %+v", all)
	}
	mustWrite(t, st, func(q Q) error {
		n, err := q.DeleteActivityBefore(ctx, t0.Add(2*time.Hour))
		if n != 2 {
			t.Errorf("%d entries purged", n)
		}
		return err
	})
}

func TestFailedJobsAndDevices(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	mustWrite(t, st, func(q Q) error {
		return errors.Join(
			q.EnqueueJob(ctx, JobRequest{Kind: "file.analyze", Target: "a", Class: "io"}, t0),
			q.EnqueueJob(ctx, JobRequest{Kind: "item.metadata", Target: "b", Class: "io"}, t0),
		)
	})
	var claimed []Job
	mustWrite(t, st, func(q Q) error {
		for range 2 {
			j, ok, err := q.ClaimJob(ctx, "io", t0)
			if err != nil || !ok {
				return errors.Join(err, errors.New("nothing to claim"))
			}
			claimed = append(claimed, j)
		}
		return errors.Join(q.FailJob(ctx, claimed[0].ID, "boum", t0), q.FailJob(ctx, claimed[1].ID, "patatras", t0.Add(time.Minute)))
	})
	failed, err := st.Read().FailedJobs(ctx, 10)
	if err != nil || len(failed) != 2 || failed[0].LastError != "patatras" || failed[0].Attempts != 1 {
		t.Fatalf("failed: %+v %v", failed, err)
	}
	if j, err := st.Read().FailedJob(ctx, failed[1].ID); err != nil || j.Kind != "file.analyze" || j.Target != "a" {
		t.Errorf("one job: %+v %v", j, err)
	}
	mustWrite(t, st, func(q Q) error { return q.DeleteFailedJob(ctx, failed[0].ID) })
	if _, err := st.Read().FailedJob(ctx, failed[0].ID); !IsNotFound(err) {
		t.Errorf("forgotten job: %v", err)
	}

	// Devices of every account, with the account name and the picked profile.
	a, b := newAccount("Chloé", true), newAccount("Léa", false)
	profile := domain.Profile{ID: domain.NewID(), AccountID: b.ID, Name: "Tom", CreatedAt: t0, UpdatedAt: t0}
	sa := domain.Session{ID: domain.NewID(), AccountID: a.ID, Device: domain.Device{Name: "PC"}, CreatedAt: t0, LastUsedAt: t0, ExpiresAt: t0.Add(time.Hour)}
	sb := domain.Session{ID: domain.NewID(), AccountID: b.ID, Device: domain.Device{Name: "TV"}, CreatedAt: t0, LastUsedAt: t0.Add(time.Minute), ExpiresAt: t0.Add(time.Hour)}
	old := domain.Session{ID: domain.NewID(), AccountID: b.ID, Device: domain.Device{Name: "Vieux"}, CreatedAt: t0, LastUsedAt: t0, ExpiresAt: t0}
	mustWrite(t, st, func(q Q) error {
		return errors.Join(q.CreateAccount(ctx, a, "h"), q.CreateAccount(ctx, b, "h"), q.CreateProfile(ctx, profile, ""),
			q.CreateSession(ctx, sa, "ja"), q.CreateSession(ctx, sb, "jb"), q.CreateSession(ctx, old, "jc"), q.SetSessionProfile(ctx, sb.ID, profile.ID))
	})
	devices, err := st.Read().AllSessions(ctx, t0.Add(time.Second))
	if err != nil || len(devices) != 2 || devices[0].Username != "Léa" || devices[0].ProfileName != "Tom" ||
		devices[0].Session.Device.Name != "TV" || devices[1].ProfileName != "" {
		t.Errorf("devices: %+v %v", devices, err)
	}
}
