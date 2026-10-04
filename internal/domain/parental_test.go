package domain

import (
	"slices"
	"testing"
)

func TestRatingAge(t *testing.T) {
	for _, c := range []struct {
		in  string
		age int
		ok  bool
	}{
		// What Sonarr and Radarr actually write.
		{"TV-Y7", 7, true},
		{"TV-PG", 10, true},
		{"TV-14", 14, true},
		{"TV-MA", 17, true},
		{"-10", 10, true},
		{"12", 12, true},
		{"12+", 12, true},
		{"16", 16, true},
		{"PG", 10, true},
		// US movies: NC-17 is not "17".
		{"G", 0, true},
		{"PG-13", 13, true},
		{"R", 17, true},
		{"NC-17", 18, true},
		// Country prefixes, long forms.
		{"US:PG-13", 13, true},
		{"FR:-12", 12, true},
		{"FR-16", 16, true},
		{"DE-FSK 12", 12, true},
		{"Rated R", 17, true},
		{"PG-13 (violence)", 13, true},
		{"tv-14", 14, true},
		{"Tous publics", 0, true},
		{"FSK 16", 16, true},
		{"MA15+", 15, true},
		{"U", 0, true},
		{"12A", 12, true},
		// Unknown.
		{"", 0, false},
		{"None", 0, false},
		{"NR", 0, false},
		{"Not Rated", 0, false},
		{"Unrated", 0, false},
		{"99", 0, false},
	} {
		if age, ok := RatingAge(c.in); age != c.age || ok != c.ok {
			t.Errorf("RatingAge(%q) = %d, %v; want %d, %v", c.in, age, ok, c.age, c.ok)
		}
	}
}

func TestParentalControl(t *testing.T) {
	twelve, sixteen := 12, 16
	c := ParentalControl{MaxAge: &sixteen}.Combine(ParentalControl{MaxAge: &twelve, BlockUnrated: true})
	if c.MaxAge == nil || *c.MaxAge != 12 || !c.BlockUnrated {
		t.Errorf("combined: %+v", c)
	}
	if c := (ParentalControl{}).Combine(ParentalControl{MaxAge: &sixteen}); c.MaxAge == nil || *c.MaxAge != 16 || c.BlockUnrated {
		t.Errorf("no limit + 16: %+v", c)
	}
	kid := KidParentalControl()
	if !kid.Allows(7, true) || kid.Allows(14, true) || kid.Allows(0, false) || !(ParentalControl{}).Allows(0, false) {
		t.Error("Allows")
	}
}

func TestPrincipalViewer(t *testing.T) {
	lib, other := NewID(), NewID()
	fourteen := 14
	profile := &Profile{ID: NewID(), Parental: ParentalControl{BlockUnrated: true}}
	p := Principal{
		Account: Account{Libraries: LibraryAccess{IDs: []ID{lib}}, Parental: ParentalControl{MaxAge: &fourteen}},
		Profile: profile,
	}
	v, ok := p.Viewer()
	if !ok || !slices.Equal(v.Libraries, []ID{lib}) || v.Parental.MaxAge == nil || *v.Parental.MaxAge != 14 || !v.Parental.BlockUnrated {
		t.Errorf("restricted account: %+v", v)
	}
	if !v.AllowsLibrary(lib) || v.AllowsLibrary(other) || !p.AllowsLibrary(lib) || p.AllowsLibrary(other) {
		t.Error("libraries")
	}
	// No library allowed: an empty list, not "all".
	p.Account.Libraries = LibraryAccess{}
	if v, _ := p.Viewer(); v.Libraries == nil || v.AllowsLibrary(lib) {
		t.Errorf("no library: %+v", v.Libraries)
	}
	// Administrator: everything, except the control of their own profile.
	p.Account.IsAdmin = true
	if v, _ := p.Viewer(); v.Libraries != nil || v.Parental.MaxAge != nil || !v.Parental.BlockUnrated {
		t.Errorf("administrator: %+v", v)
	}
	if p.CanAdminister() || !p.Restricted() {
		t.Error("administrator on a restricted profile must not administer")
	}
	p.Profile = &Profile{ID: NewID()}
	if !p.CanAdminister() {
		t.Error("administrator on an unrestricted profile")
	}
	if _, ok := (Principal{}).Viewer(); ok {
		t.Error("no profile chosen")
	}
}
