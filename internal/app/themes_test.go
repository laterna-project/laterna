package app

import (
	"context"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// Built-in themes go through the same check as administrators' themes.
func TestBuiltinThemesReadable(t *testing.T) {
	seen := map[string]bool{}
	for _, th := range builtinThemes {
		name, tokens, err := checkTheme(th.Name, th.Tokens)
		if err != nil {
			t.Errorf("%s: %v", th.Name, err)
		}
		if name != th.Name || tokens != th.Tokens {
			t.Errorf("%s: tokens not normalized", th.Name)
		}
		if seen[th.ID.String()] {
			t.Errorf("%s: duplicate ID", th.Name)
		}
		seen[th.ID.String()] = true
	}
}

// A profile's choice is only announced to that profile; a change of the server's theme goes to
// everyone.
func TestThemesChangedRecipients(t *testing.T) {
	a, _ := newTestApp(t)
	mine, other := domain.NewID(), domain.NewID()
	subMine := a.Subscribe(domain.Principal{Profile: &domain.Profile{ID: mine}})
	defer subMine.Close()
	subOther := a.Subscribe(domain.Principal{Profile: &domain.Profile{ID: other}})
	defer subOther.Close()
	a.bus.Publish(domain.ThemesChanged{ProfileID: &mine})
	a.bus.Publish(domain.ThemesChanged{})
	count := func(s *Subscription) int {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		n := 0
		for {
			if _, err := s.Next(ctx); err != nil {
				return n
			}
			n++
		}
	}
	if got := count(subMine); got != 2 {
		t.Errorf("profile concerned: %d events", got)
	}
	if got := count(subOther); got != 1 {
		t.Errorf("other profile: %d events", got)
	}
}
