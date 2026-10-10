package app

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/testfixtures"
)

// login signs an account in and returns the matching principal.
func login(t *testing.T, a *App, username, password string) (Login, domain.Principal) {
	t.Helper()
	ctx := context.Background()
	l, err := a.Login(ctx, username, password, dev("TV"), "10.0.0.2")
	mustNil(t, err)
	p, err := a.Authenticate(ctx, l.Token, "10.0.0.2")
	mustNil(t, err)
	return l, p
}

func TestAccountAdministration(t *testing.T) {
	a, _ := newTestApp(t)
	ctx := context.Background()
	_, admin := setupAdmin(t, a)
	twelve := 12

	// Refused: password too short, restricted administrator, unknown library.
	for _, n := range []NewAccount{
		{Username: "Léa", Password: "short"},
		{Username: "Léa", Password: "a-password", IsAdmin: true, Parental: &domain.ParentalControl{MaxAge: &twelve}},
		{Username: "Léa", Password: "a-password", Libraries: &domain.LibraryAccess{IDs: []domain.ID{domain.NewID()}}},
	} {
		if _, err := a.CreateAccount(ctx, admin, n); !isKind(err, domain.ErrInvalid) {
			t.Errorf("%+v: %v", n, err)
		}
	}
	lea, err := a.CreateAccount(ctx, admin, NewAccount{Username: "Léa", Password: "a-password", Parental: &domain.ParentalControl{MaxAge: &twelve}})
	mustNil(t, err)
	if !lea.Libraries.All || lea.Parental.MaxAge == nil || lea.IsAdmin {
		t.Errorf("account created: %+v", lea)
	}
	if _, err := a.CreateAccount(ctx, admin, NewAccount{Username: " LÉA ", Password: "a-password"}); !isKind(err, domain.ErrConflict) {
		t.Errorf("name taken: %v", err)
	}
	// A profile of the same name, picked automatically at login.
	l, leaP := login(t, a, "léa", "a-password")
	if l.Session.Profile == nil || l.Session.Profile.Name != "Léa" {
		t.Errorf("default profile: %+v", l.Session.Profile)
	}
	if v, _ := leaP.Viewer(); v.Parental.MaxAge == nil || *v.Parental.MaxAge != 12 {
		t.Errorf("account control: %+v", v)
	}

	// New password: her devices are signed out.
	pw := "new-password"
	_, err = a.UpdateAccount(ctx, admin, lea.ID, AccountChanges{Password: &pw})
	mustNil(t, err)
	if _, err := a.Authenticate(ctx, l.Token, ""); !isKind(err, domain.ErrUnauthenticated) {
		t.Errorf("session kept after the password change: %v", err)
	}
	// Disabled: signed out, and no more logins.
	l, _ = login(t, a, "Léa", pw)
	yes := true
	_, err = a.UpdateAccount(ctx, admin, lea.ID, AccountChanges{Disabled: &yes})
	mustNil(t, err)
	if _, err := a.Authenticate(ctx, l.Token, ""); !isKind(err, domain.ErrUnauthenticated) {
		t.Errorf("session kept after disabling: %v", err)
	}
	if _, err := a.Login(ctx, "Léa", pw, dev("TV"), "10.0.0.3"); !isKind(err, domain.ErrForbidden) {
		t.Errorf("login to a disabled account: %v", err)
	}

	// Promoted to administrator: her restrictions are lifted. Restricting an administrator is
	// refused.
	no := false
	lea, err = a.UpdateAccount(ctx, admin, lea.ID, AccountChanges{Disabled: &no, IsAdmin: &yes})
	mustNil(t, err)
	if !lea.IsAdmin || lea.Parental.Active() || !lea.Libraries.All {
		t.Errorf("promoted: %+v", lea)
	}
	if _, err := a.UpdateAccount(ctx, admin, lea.ID, AccountChanges{Libraries: &domain.LibraryAccess{}}); !isKind(err, domain.ErrInvalid) {
		t.Errorf("restricted administrator: %v", err)
	}

	// Nothing against your own account, and there is always an enabled administrator.
	for _, ch := range []AccountChanges{{Disabled: &yes}, {IsAdmin: &no}} {
		if _, err := a.UpdateAccount(ctx, admin, admin.Account.ID, ch); !isKind(err, domain.ErrPrecondition) {
			t.Errorf("on oneself %+v: %v", ch, err)
		}
	}
	if err := a.DeleteAccount(ctx, admin, admin.Account.ID); !isKind(err, domain.ErrPrecondition) {
		t.Errorf("deleting oneself: %v", err)
	}
	_, err = a.UpdateAccount(ctx, admin, lea.ID, AccountChanges{IsAdmin: &no})
	mustNil(t, err)
	stale := domain.Principal{Account: domain.Account{ID: domain.NewID(), IsAdmin: true}}
	if _, err := a.UpdateAccount(ctx, stale, admin.Account.ID, AccountChanges{Disabled: &yes}); !isKind(err, domain.ErrPrecondition) {
		t.Errorf("last administrator disabled: %v", err)
	}
	if err := a.DeleteAccount(ctx, stale, admin.Account.ID); !isKind(err, domain.ErrPrecondition) {
		t.Errorf("last administrator deleted: %v", err)
	}

	list, err := a.Accounts(ctx)
	mustNil(t, err)
	// Léa has had no device signed in since she was disabled.
	if len(list) != 2 || list[1].Account.Username != "Léa" || list[1].Profiles != 1 || list[1].LastActive != nil || list[0].LastActive == nil {
		t.Errorf("accounts: %+v", list)
	}
	mustNil(t, a.DeleteAccount(ctx, admin, lea.ID))
	if _, err := a.Login(ctx, "Léa", pw, dev("TV"), "10.0.0.4"); !isKind(err, domain.ErrUnauthenticated) {
		t.Errorf("deleted account: %v", err)
	}
}

func TestProfileParentalRules(t *testing.T) {
	a, _ := newTestApp(t)
	ctx := context.Background()
	_, adult := setupAdmin(t, a)
	kid, err := a.CreateProfile(ctx, adult, "Tom", "", true, nil, "")
	mustNil(t, err)
	if kid.Parental.MaxAge == nil || *kid.Parental.MaxAge != domain.KidMaxAge || !kid.Parental.BlockUnrated {
		t.Errorf("default control of a kid: %+v", kid.Parental)
	}
	sixteen, big := 16, 99
	teen, err := a.CreateProfile(ctx, adult, "Ado", "", false, &domain.ParentalControl{MaxAge: &sixteen}, "")
	mustNil(t, err)
	if _, err := a.CreateProfile(ctx, adult, "X", "", false, &domain.ParentalControl{MaxAge: &big}, ""); !isKind(err, domain.ErrInvalid) {
		t.Errorf("age out of range: %v", err)
	}
	// A restricted profile, even an adult one, manages nothing and does not administer.
	mustNil(t, func() error { _, err := a.SelectProfile(ctx, adult, teen.ID, ""); return err }())
	onTeen := adult
	onTeen.Profile = &teen
	if _, err := a.CreateProfile(ctx, onTeen, "Free", "", false, nil, ""); !isKind(err, domain.ErrForbidden) {
		t.Errorf("restricted profile creating one: %v", err)
	}
	if _, err := a.UpdateProfile(ctx, onTeen, teen.ID, "", ProfileChanges{Parental: &domain.ParentalControl{}}); !isKind(err, domain.ErrForbidden) {
		t.Errorf("restricted profile lifting its own control: %v", err)
	}
	if onTeen.CanAdminister() {
		t.Error("administration from a restricted profile")
	}
	// The last unrestricted profile stays that way.
	if _, err := a.UpdateProfile(ctx, adult, adult.Profile.ID, "", ProfileChanges{Parental: &domain.ParentalControl{BlockUnrated: true}}); !isKind(err, domain.ErrPrecondition) {
		t.Errorf("last unrestricted profile restricted: %v", err)
	}
	// Lifting the control of a profile, from an unrestricted one.
	teen, err = a.UpdateProfile(ctx, adult, teen.ID, "", ProfileChanges{Parental: &domain.ParentalControl{}})
	mustNil(t, err)
	if teen.Restricted() {
		t.Errorf("control lifted: %+v", teen)
	}
	// A profile that becomes a kid profile gets the default control.
	yes := true
	teen, err = a.UpdateProfile(ctx, adult, teen.ID, "", ProfileChanges{Kid: &yes})
	mustNil(t, err)
	if teen.Parental.MaxAge == nil || *teen.Parental.MaxAge != domain.KidMaxAge {
		t.Errorf("became a kid: %+v", teen.Parental)
	}
}

// What an account limited to one library sees, then its profiles under parental control: lists,
// search, details, home, playback, events.
func TestLibraryAccessAndParentalControl(t *testing.T) {
	if testing.Short() {
		t.Skip("full scan")
	}
	a, _ := startMediaApp(t)
	ctx := context.Background()
	_, admin := setupAdmin(t, a)
	root := t.TempDir()
	for name, mpaa := range map[string]string{"Big Test Movie (2020)": "TV-MA", "Versions (2017)": "-10"} {
		dir := filepath.Join(root, name)
		copyTree(t, filepath.Join(testfixtures.Root(), "Movies", name), dir)
		writeText(t, filepath.Join(dir, "movie.nfo"), "<movie><title>"+name[:len(name)-7]+"</title><mpaa>"+mpaa+"</mpaa></movie>")
	}
	films, err := a.CreateLibrary(ctx, "Movies", domain.LibraryMovies, []string{root}, "")
	mustNil(t, err)
	series, err := a.CreateLibrary(ctx, "Shows", domain.LibraryShows, []string{testRoot("Shows")}, "")
	mustNil(t, err)
	waitIdle(t, a)

	_, err = a.CreateAccount(ctx, admin, NewAccount{Username: "Léa", Password: "a-password", Libraries: &domain.LibraryAccess{IDs: []domain.ID{films.ID}}})
	mustNil(t, err)
	_, lea := login(t, a, "Léa", "a-password")
	movieTitles := func(p domain.Principal) []string {
		t.Helper()
		page, err := a.ListMovies(ctx, p, ListQuery{})
		mustNil(t, err)
		return titles(page.Items)
	}
	if got := movieTitles(lea); !slices.Equal(got, []string{"Big Test Movie", "Versions"}) {
		t.Errorf("Léa's movies: %v", got)
	}
	// The series library does not exist for her.
	if libs, _ := a.CatalogLibraries(ctx, lea); len(libs) != 1 || libs[0].Library.ID != films.ID || libs[0].Counts[domain.ItemMovie] != 2 {
		t.Errorf("Léa's libraries: %+v", libs)
	}
	if page, err := a.ListSeries(ctx, lea, ListQuery{}); err != nil || len(page.Items) != 0 {
		t.Errorf("Léa's series: %v %v", titles(page.Items), err)
	}
	if _, err := a.ListSeries(ctx, lea, ListQuery{LibraryID: &series.ID}); !isKind(err, domain.ErrNotFound) {
		t.Errorf("forbidden library requested: %v", err)
	}
	allSeries, err := a.ListSeries(ctx, admin, ListQuery{})
	mustNil(t, err)
	show := allSeries.Items[0].Item.ID
	eps, err := a.Episodes(ctx, admin, show, nil)
	mustNil(t, err)
	if len(eps) == 0 {
		t.Fatal("no episode")
	}
	if _, _, _, err := a.Series(ctx, lea, show); !isKind(err, domain.ErrNotFound) {
		t.Errorf("details of a forbidden series: %v", err)
	}
	if _, err := a.Episodes(ctx, lea, show, nil); !isKind(err, domain.ErrNotFound) {
		t.Errorf("episodes of a forbidden series: %v", err)
	}
	if _, err := a.StartPlayback(ctx, lea, PlayRequest{ItemID: eps[0].Item.ID}); !isKind(err, domain.ErrNotFound) {
		t.Errorf("playback of a forbidden episode: %v", err)
	}
	if found, _ := a.Search(ctx, lea, "test", 20); slices.ContainsFunc(found, func(v domain.ItemView) bool { return v.Item.LibraryID == series.ID }) {
		t.Errorf("search: %v", titles(found))
	}
	if err := a.SetFavorite(ctx, lea, show, true); !isKind(err, domain.ErrNotFound) {
		t.Errorf("forbidden favorite: %v", err)
	}
	sub := a.Subscribe(lea)
	defer sub.Close()
	if sub.wants(domain.ItemsChanged{LibraryID: series.ID}) || !sub.wants(domain.ItemsChanged{LibraryID: films.ID}) || sub.wants(domain.LibraryScanned{}) {
		t.Error("Léa's events")
	}

	// Kid profile: age 10 at most, unrated hidden.
	tom, err := a.CreateProfile(ctx, lea, "Tom", "", true, nil, "")
	mustNil(t, err)
	onTom := lea
	onTom.Profile = &tom
	if got := movieTitles(onTom); !slices.Equal(got, []string{"Versions"}) {
		t.Errorf("Tom's movies: %v", got)
	}
	var big domain.ID
	for _, v := range mustList(t, a, admin) {
		if v.Item.Title == "Big Test Movie" {
			big = v.Item.ID
		}
	}
	if _, _, err := a.Movie(ctx, onTom, big); !isKind(err, domain.ErrNotFound) {
		t.Errorf("details of a forbidden movie: %v", err)
	}
	rows, err := a.Home(ctx, onTom, 0, false)
	mustNil(t, err)
	for _, r := range rows {
		for _, v := range r.Items {
			if v.Item.ID == big {
				t.Errorf("Tom's home: %s in %q", v.Item.Title, r.Title)
			}
		}
	}
	if g, _ := a.Genres(ctx, onTom, nil); len(g) != 0 {
		t.Errorf("Tom's genres: %v", g)
	}

	// Account-level control: it applies on top of each profile's.
	sixteen := 16
	acct, err := a.UpdateAccount(ctx, admin, lea.Account.ID, AccountChanges{Parental: &domain.ParentalControl{MaxAge: &sixteen}})
	mustNil(t, err)
	lea.Account = acct
	if got := movieTitles(lea); !slices.Equal(got, []string{"Versions"}) {
		t.Errorf("Léa under the account control: %v", got)
	}
}

func mustList(t *testing.T, a *App, p domain.Principal) []domain.ItemView {
	t.Helper()
	page, err := a.ListMovies(context.Background(), p, ListQuery{})
	mustNil(t, err)
	return page.Items
}
