package rpc

import (
	"testing"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/domain"
)

func TestFirstLanguage(t *testing.T) {
	for _, c := range []struct{ header, want string }{
		{"fr-FR,fr;q=0.9,en;q=0.8", "fr-FR"},
		{"de;q=0.9", "de"},
		{" pt-BR , en", "pt-BR"},
		{"*", ""},
		{"", ""},
	} {
		if got := firstLanguage(c.header); got != c.want {
			t.Errorf("%q: %q, want %q", c.header, got, c.want)
		}
	}
}

func TestSubtitleModes(t *testing.T) {
	for mode, msg := range subtitleModes {
		if subtitleModeFromMsg(msg) != mode {
			t.Errorf("%q does not come back from %s", mode, msg)
		}
	}
	if got := subtitleModeFromMsg(laternav1.SubtitleMode(42)); got == domain.SubtitleAuto {
		t.Errorf("unknown value taken as automatic")
	}
}
