package app

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/auth"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/jellyfin"
	"github.com/laterna-project/laterna/internal/store"
)

// Import from Jellyfin. The administrator gives the data folder of a Jellyfin server that Laterna
// can read. PreviewJellyfinImport says what would be imported and where, then ImportJellyfin
// imports it, with the targets corrected if needed. Each Jellyfin user becomes an account (with its
// profile), a new profile of an existing account, or joins an existing profile. Their user data is
// matched to Laterna items by file path, and their sessions from Jellyfin's activity log go into
// the history. Importing a second time copies nothing: data is merged and sessions are recognized
// by their key.

// JellyfinTargetKind says where the data of a Jellyfin user goes.
type JellyfinTargetKind string

// Targets for a Jellyfin user.
const (
	// JellyfinNewAccount: an account of the same name, with its profile.
	JellyfinNewAccount JellyfinTargetKind = "new_account"
	// JellyfinNewProfile: a profile of the same name in an existing account (reused if it exists).
	JellyfinNewProfile JellyfinTargetKind = "new_profile"
	// JellyfinProfile: an existing profile.
	JellyfinProfile JellyfinTargetKind = "profile"
	// JellyfinSkip: nothing is imported.
	JellyfinSkip JellyfinTargetKind = "skip"
)

// JellyfinTarget is the target of a Jellyfin user. AccountID is used by JellyfinNewProfile,
// ProfileID by JellyfinProfile.
type JellyfinTarget struct {
	Kind      JellyfinTargetKind
	AccountID domain.ID
	ProfileID domain.ID
}

// JellyfinUser is a Jellyfin user as the import sees them: their target and what would be imported.
type JellyfinUser struct {
	ID, Name                     string
	Admin, Disabled, HasPassword bool
	Target                       JellyfinTarget
	// AccountName and ProfileName spell out the target.
	AccountName, ProfileName string
	// UserData counts the items they have data for, UserDataMatched those found in Laterna.
	UserData, UserDataMatched int
	// Sessions counts the sessions that go into the history, SessionsMatched those whose item was
	// found (the others are imported with just their title).
	Sessions, SessionsMatched int
	// Problem says why the proposed target is not possible (it is then JellyfinSkip).
	Problem domain.Text
}

// JellyfinPlan is what an import would bring in.
type JellyfinPlan struct {
	Users []JellyfinUser
	// Orphaned counts data of items that are gone from Jellyfin, Unsupported data of items of a
	// kind we do not import, Unpaired playback starts without a stop in the log.
	Orphaned, Unsupported, Unpaired int
	// UnmatchedFiles counts the Jellyfin files (with data) that Laterna does not have.
	// UnmatchedSamples gives a few examples.
	UnmatchedFiles   int
	UnmatchedSamples []string
}

// JellyfinImported is what an import brought in for one user.
type JellyfinImported struct {
	ID, Name                       string
	AccountID, ProfileID           domain.ID
	ProfileName                    string
	AccountCreated, ProfileCreated bool
	// UserData counts the items updated, History the sessions added.
	UserData, History int
}

const (
	maxUnmatchedSamples = 20
	jellyfinDevice      = "Jellyfin"
)

// PreviewJellyfinImport reads a Jellyfin server and says what would be imported, and where.
func (a *App) PreviewJellyfinImport(ctx context.Context, path string) (JellyfinPlan, error) {
	run, err := a.prepareJellyfin(ctx, path)
	if err != nil {
		return JellyfinPlan{}, err
	}
	return run.plan, nil
}

// ImportJellyfin imports the data of a Jellyfin server. targets overrides the proposed target of
// some users (by Jellyfin ID).
func (a *App) ImportJellyfin(ctx context.Context, p domain.Principal, path string, targets map[string]JellyfinTarget) ([]JellyfinImported, error) {
	run, err := a.prepareJellyfin(ctx, path)
	if err != nil {
		return nil, err
	}
	for id := range targets {
		if !slices.ContainsFunc(run.plan.Users, func(u JellyfinUser) bool { return u.ID == id }) {
			return nil, domain.Invalid("import.unknown_user", "user_id", id)
		}
	}
	var out []JellyfinImported
	for _, u := range run.plan.Users {
		target, ok := targets[u.ID]
		if !ok {
			target = u.Target
		}
		if target.Kind == JellyfinSkip {
			continue
		}
		done, err := a.importJellyfinUser(ctx, run, run.users[u.ID], target)
		if err != nil {
			return out, fmt.Errorf("%s: %w", u.Name, err)
		}
		out = append(out, done)
		a.record(ctx, domain.Activity{
			Kind: domain.ActivityImport, AccountID: &p.Account.ID,
			Text: domain.T("activity.import_jellyfin", "user", u.Name, "profile", done.ProfileName, "items", done.UserData, "plays", done.History),
		})
	}
	return out, nil
}

// jellyfinRun is a prepared import: the export that was read, the files found, the plan.
type jellyfinRun struct {
	export *jellyfin.Export
	users  map[string]jellyfin.User
	// files maps a Jellyfin item (movie, episode, track) to its file in Laterna.
	files map[jellyfin.ID]*indexedFile
	// containers maps a Jellyfin series, season or album to the same one in Laterna (for
	// favorites).
	containers map[jellyfin.ID]domain.ID
	plan       JellyfinPlan
}

func (a *App) prepareJellyfin(ctx context.Context, path string) (*jellyfinRun, error) {
	tmp := a.cacheDir
	if tmp == "" {
		tmp = os.TempDir()
	}
	export, err := jellyfin.Read(ctx, strings.TrimSpace(path), tmp)
	if err != nil {
		// The reader says itself what is wrong (folder, version). Anything else is a database it
		// could not read.
		if domain.CodeOf(err) != "" {
			return nil, err
		}
		return nil, domain.Invalid("import.unreadable", "reason", err)
	}
	index, err := a.fileIndex(ctx)
	if err != nil {
		return nil, err
	}
	run := &jellyfinRun{
		export: export, users: map[string]jellyfin.User{}, files: map[jellyfin.ID]*indexedFile{},
		containers: map[jellyfin.ID]domain.ID{},
		plan:       JellyfinPlan{Orphaned: export.Orphaned, Unsupported: export.Unsupported, Unpaired: export.Unpaired},
	}
	for id, it := range export.Items {
		if !it.Kind.Leaf() || it.Path == "" {
			continue
		}
		f := index.match(it.Path)
		if f == nil {
			continue
		}
		run.files[id] = f
		first := f.items[0]
		for jf, laterna := range map[jellyfin.ID]*domain.ID{it.Series: first.SeriesID, it.Season: first.SeasonID, it.Parent: first.AlbumID} {
			if jf != "" && laterna != nil {
				run.containers[jf] = *laterna
			}
		}
	}
	unmatched := map[string]bool{}
	byUser := map[jellyfin.ID]*JellyfinUser{}
	for _, u := range export.Users {
		run.users[string(u.ID)] = u
		ju := JellyfinUser{ID: string(u.ID), Name: u.Name, Admin: u.Admin, Disabled: u.Disabled, HasPassword: auth.IsLegacyHash(u.PasswordHash)}
		if err := a.proposeJellyfinTarget(ctx, u, &ju); err != nil {
			return nil, err
		}
		run.plan.Users = append(run.plan.Users, ju)
	}
	for i := range run.plan.Users {
		byUser[jellyfin.ID(run.plan.Users[i].ID)] = &run.plan.Users[i]
	}
	for _, d := range export.UserData {
		ju := byUser[d.User]
		if ju == nil {
			continue
		}
		ju.UserData++
		if run.itemsOf(d.Item) != nil {
			ju.UserDataMatched++
		} else if it := export.Items[d.Item]; it.Kind.Leaf() && it.Path != "" {
			unmatched[it.Path] = true
		}
	}
	for _, s := range export.Sessions {
		ju := byUser[s.User]
		if ju == nil {
			continue
		}
		if _, ok := run.sessionPlay(s); !ok {
			continue
		}
		ju.Sessions++
		if run.files[s.Item] != nil {
			ju.SessionsMatched++
		}
	}
	run.plan.UnmatchedFiles = len(unmatched)
	for p := range unmatched {
		run.plan.UnmatchedSamples = append(run.plan.UnmatchedSamples, p)
	}
	slices.Sort(run.plan.UnmatchedSamples)
	run.plan.UnmatchedSamples = run.plan.UnmatchedSamples[:min(len(run.plan.UnmatchedSamples), maxUnmatchedSamples)]
	return run, nil
}

// proposeJellyfinTarget picks the target of a user: the account of the same name (its profile of
// the same name, otherwise its first unrestricted profile), otherwise a new account.
func (a *App) proposeJellyfinTarget(ctx context.Context, u jellyfin.User, ju *JellyfinUser) error {
	if validateUsername(u.Name) != nil {
		ju.Target, ju.Problem = JellyfinTarget{Kind: JellyfinSkip}, domain.T("import.problem.name_refused")
		return nil //nolint:nilerr // name rejected: reported in the plan, it is not an error
	}
	read := a.store.Read()
	account, _, err := read.AccountByUsername(ctx, u.Name)
	switch {
	case store.IsNotFound(err):
		ju.Target, ju.AccountName, ju.ProfileName = JellyfinTarget{Kind: JellyfinNewAccount}, u.Name, u.Name
		return nil
	case err != nil:
		return err
	}
	profiles, err := read.Profiles(ctx, account.ID)
	if err != nil {
		return err
	}
	pick := slices.IndexFunc(profiles, func(p domain.Profile) bool { return store.NameKey(p.Name) == store.NameKey(u.Name) })
	if pick < 0 {
		pick = slices.IndexFunc(profiles, func(p domain.Profile) bool { return !p.Restricted() })
	}
	if pick < 0 {
		ju.Target, ju.Problem = JellyfinTarget{Kind: JellyfinSkip}, domain.T("import.problem.no_usable_profile")
		return nil
	}
	pr := profiles[pick]
	ju.Target = JellyfinTarget{Kind: JellyfinProfile, AccountID: account.ID, ProfileID: pr.ID}
	ju.AccountName, ju.ProfileName = account.Username, pr.Name
	return nil
}

// itemsOf returns the Laterna items of a Jellyfin item: those of its file (a multi-episode file has
// several), or the matching series, season or album.
func (run *jellyfinRun) itemsOf(id jellyfin.ID) []domain.ID {
	if f := run.files[id]; f != nil {
		ids := make([]domain.ID, len(f.items))
		for i, it := range f.items {
			ids[i] = it.ItemID
		}
		return ids
	}
	if c, ok := run.containers[id]; ok {
		return []domain.ID{c}
	}
	return nil
}

// sessionPlay prepares the history play for a session from the log (without a profile or a Laterna
// item); false if it does not belong there: item unknown to Jellyfin or of another kind, or too
// short. The log only gives the start and the stop, so the time watched is the elapsed time, capped
// at the item's runtime, and the position reached is assumed to be the same.
func (run *jellyfinRun) sessionPlay(s jellyfin.Session) (domain.Play, bool) {
	it, ok := run.export.Items[s.Item]
	if !ok {
		return domain.Play{}, false
	}
	var kind domain.ItemKind
	switch it.Kind {
	case jellyfin.Movie:
		kind = domain.ItemMovie
	case jellyfin.Episode:
		kind = domain.ItemEpisode
	case jellyfin.Track:
		kind = domain.ItemTrack
	default:
		return domain.Play{}, false
	}
	watched := s.End.Sub(s.Start)
	if it.Runtime > 0 {
		watched = min(watched, it.Runtime)
	}
	if !domain.CountsAsPlay(kind, watched, it.Runtime) {
		return domain.Play{}, false
	}
	_, finished := domain.Progress(watched, it.Runtime)
	pl := domain.Play{
		Kind: kind, Title: it.Name, StartedAt: s.Start, EndedAt: s.End, Watched: watched, Position: watched,
		Duration: it.Runtime, Completed: finished, Device: jellyfinDevice,
	}
	if kind == domain.ItemEpisode {
		pl.Subtitle = it.SeriesName
	}
	return pl, true
}

// importJellyfinUser imports a user into their target.
func (a *App) importJellyfinUser(ctx context.Context, run *jellyfinRun, u jellyfin.User, target JellyfinTarget) (JellyfinImported, error) {
	done := JellyfinImported{ID: string(u.ID), Name: u.Name}
	if err := a.jellyfinProfile(ctx, run, u, target, &done); err != nil {
		return done, err
	}
	changed, err := a.importJellyfinUserData(ctx, run, u.ID, done.ProfileID)
	if err != nil {
		return done, err
	}
	done.UserData = len(changed)
	if done.History, err = a.importJellyfinSessions(ctx, run, u.ID, done.ProfileID); err != nil {
		return done, err
	}
	if len(changed) > 0 {
		a.userDataChanged(done.ProfileID, changed)
	} else if done.History > 0 {
		a.tasteChanged(done.ProfileID)
	}
	return done, nil
}

// jellyfinProfile finds or creates the target profile.
func (a *App) jellyfinProfile(ctx context.Context, run *jellyfinRun, u jellyfin.User, target JellyfinTarget, done *JellyfinImported) error {
	now := a.now()
	switch target.Kind {
	case JellyfinProfile:
		pr, _, err := a.store.Read().Profile(ctx, target.ProfileID)
		if store.IsNotFound(err) {
			return domain.NotFound("profile.not_found", "profile_id", target.ProfileID)
		}
		if err != nil {
			return err
		}
		done.AccountID, done.ProfileID, done.ProfileName = pr.AccountID, pr.ID, pr.Name
		return nil
	case JellyfinNewProfile:
		if _, err := a.store.Read().Account(ctx, target.AccountID); store.IsNotFound(err) {
			return domain.NotFound("account.not_found", "account_id", target.AccountID)
		} else if err != nil {
			return err
		}
		name := strings.TrimSpace(u.Name)
		if err := validateProfileName(name); err != nil {
			return err
		}
		profile := domain.Profile{ID: domain.NewID(), AccountID: target.AccountID, Name: name, CreatedAt: now, UpdatedAt: now}
		if !u.Admin {
			profile.Parental = jellyfinParental(u)
		}
		err := a.store.Write(ctx, func(q store.Q) error {
			existing, err := q.Profiles(ctx, target.AccountID)
			if err != nil {
				return err
			}
			if i := slices.IndexFunc(existing, func(p domain.Profile) bool { return store.NameKey(p.Name) == store.NameKey(name) }); i >= 0 {
				profile = existing[i] // already created by an earlier import
				return nil
			}
			if len(existing) >= maxProfiles {
				return domain.Precondition("profile.too_many", "max", maxProfiles)
			}
			done.ProfileCreated = true
			return profileWriteError(q.CreateProfile(ctx, profile, ""), name)
		})
		if err != nil {
			return err
		}
		done.AccountID, done.ProfileID, done.ProfileName = profile.AccountID, profile.ID, profile.Name
		return nil
	case JellyfinNewAccount:
		return a.jellyfinAccount(ctx, run, u, done)
	case JellyfinSkip:
		return nil
	}
	return domain.Invalid("import.unknown_target", "target", target.Kind)
}

// jellyfinAccount creates the account of a Jellyfin user with its profile: administrator, disabled,
// downloads, libraries and parental control carry over. Their Jellyfin password still works
// (rehashed with argon2id at the first login). Without a password they get a random one, for the
// administrator to replace.
func (a *App) jellyfinAccount(ctx context.Context, run *jellyfinRun, u jellyfin.User, done *JellyfinImported) error {
	username := strings.TrimSpace(u.Name)
	if err := validateUsername(username); err != nil {
		return err
	}
	now := a.now()
	account := domain.Account{
		ID: domain.NewID(), Username: username, IsAdmin: u.Admin, Disabled: u.Disabled, Libraries: domain.AllLibraries(),
		DenyDownloads: !u.Admin && u.DenyDownloads, RequestQuota: domain.DefaultRequestQuota, CreatedAt: now, UpdatedAt: now,
	}
	if !u.Admin {
		account.Parental = jellyfinParental(u)
		if !u.AllLibraries {
			account.Libraries = domain.LibraryAccess{IDs: run.libraries(u.Libraries)}
		}
	}
	hash := u.PasswordHash
	if !auth.IsLegacyHash(hash) {
		var err error
		if hash, err = auth.HashPassword(randomPassword()); err != nil {
			return err
		}
	}
	profile := domain.Profile{ID: domain.NewID(), AccountID: account.ID, Name: username, CreatedAt: now, UpdatedAt: now}
	err := a.store.Write(ctx, func(q store.Q) error {
		if err := accountWriteError(q.CreateAccount(ctx, account, hash), username); err != nil {
			return err
		}
		return q.CreateProfile(ctx, profile, "")
	})
	if err != nil {
		return err
	}
	done.AccountID, done.ProfileID, done.ProfileName = account.ID, profile.ID, profile.Name
	done.AccountCreated, done.ProfileCreated = true, true
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityAccountCreated, AccountID: &account.ID,
		Text: accountImportedText(username, account.IsAdmin),
	})
	return nil
}

// jellyfinParental converts the parental control of a Jellyfin user: since 10.11 their maximum
// rating is an age.
func jellyfinParental(u jellyfin.User) domain.ParentalControl {
	c := domain.ParentalControl{BlockUnrated: u.BlockUnrated}
	if u.MaxScore != nil && *u.MaxScore >= 0 && *u.MaxScore <= domain.MaxRatingAge {
		age := *u.MaxScore
		c.MaxAge = &age
	}
	return c
}

// libraries converts Jellyfin libraries to the Laterna ones that hold their files.
func (run *jellyfinRun) libraries(jf []jellyfin.ID) []domain.ID {
	out := []domain.ID{}
	for _, lib := range jf {
		for _, id := range run.export.Members[lib] {
			if f := run.files[id]; f != nil && !slices.Contains(out, f.library) {
				out = append(out, f.library)
			}
		}
	}
	return out
}

// importJellyfinUserData merges a user's data into the profile's: played or favorite if either says
// so, the higher play count, the position of the most recent playback. It returns the items that
// changed.
func (a *App) importJellyfinUserData(ctx context.Context, run *jellyfinRun, user jellyfin.ID, profile domain.ID) ([]domain.ID, error) {
	wanted := map[domain.ID]jellyfin.UserData{}
	var order []domain.ID
	for _, d := range run.export.UserData {
		if d.User != user {
			continue
		}
		leaf := run.files[d.Item] != nil
		for _, id := range run.itemsOf(d.Item) {
			if !leaf { // series, season, album: only the favorite flag means anything
				d = jellyfin.UserData{Favorite: d.Favorite}
			}
			if prev, ok := wanted[id]; ok {
				wanted[id] = jellyfin.Merge(prev, d)
				continue
			}
			wanted[id] = d
			order = append(order, id)
		}
	}
	var changed []domain.ID
	now := a.now()
	err := a.store.Write(ctx, func(q store.Q) error {
		changed = changed[:0]
		for _, id := range order {
			old, _, err := q.UserData(ctx, profile, id)
			if err != nil {
				return err
			}
			merged := mergeUserData(old, wanted[id])
			if userDataEqual(old, merged) {
				continue
			}
			if err := q.SetUserData(ctx, profile, id, merged, now); err != nil {
				return err
			}
			changed = append(changed, id)
		}
		return nil
	})
	return changed, err
}

// mergeUserData merges Jellyfin data into a profile's. Dates are truncated to the millisecond, as
// in the database: otherwise a Jellyfin date (down to a tenth of a microsecond) would always look
// more recent than the same one read back, and a second import would rewrite everything.
func mergeUserData(old domain.UserData, jf jellyfin.UserData) domain.UserData {
	out := old
	out.Played = old.Played || jf.Played
	out.Favorite = old.Favorite || jf.Favorite
	out.PlayCount = max(old.PlayCount, jf.PlayCount)
	if jf.LastPlayed != nil {
		last := jf.LastPlayed.Truncate(time.Millisecond)
		if old.LastPlayedAt == nil || last.After(*old.LastPlayedAt) {
			out.LastPlayedAt, out.Position = &last, jf.Position
		}
	}
	return out
}

func userDataEqual(a, b domain.UserData) bool {
	sameTime := (a.LastPlayedAt == nil) == (b.LastPlayedAt == nil) && (a.LastPlayedAt == nil || a.LastPlayedAt.Equal(*b.LastPlayedAt))
	return sameTime && a.Played == b.Played && a.Favorite == b.Favorite && a.PlayCount == b.PlayCount && a.Position == b.Position
}

// importJellyfinSessions adds the user's sessions to the profile's history and returns the number
// of new ones.
func (a *App) importJellyfinSessions(ctx context.Context, run *jellyfinRun, user jellyfin.ID, profile domain.ID) (int, error) {
	type pending struct {
		play domain.Play
		item *domain.ID
		key  string
	}
	var plays []pending
	var ids []domain.ID
	for _, s := range run.export.Sessions {
		if s.User != user {
			continue
		}
		pl, ok := run.sessionPlay(s)
		if !ok {
			continue
		}
		p := pending{play: pl, key: "jellyfin:" + string(user) + ":" + strconv.FormatInt(s.Key, 10)}
		if f := run.files[s.Item]; f != nil {
			id := f.items[0].ItemID
			p.item = &id
			ids = append(ids, id)
		}
		plays = append(plays, p)
	}
	views, err := a.store.Read().ViewsByID(ctx, domain.Viewer{ProfileID: profile}, ids)
	if err != nil {
		return 0, err
	}
	added := 0
	err = a.store.Write(ctx, func(q store.Q) error {
		added = 0
		for _, p := range plays {
			pl := p.play
			if p.item != nil {
				if view, ok := views[*p.item]; ok {
					from := playOf(profile, view, pl.Duration, pl.StartedAt)
					pl.ItemID, pl.SeriesID, pl.AlbumID, pl.ArtistID = from.ItemID, from.SeriesID, from.AlbumID, from.ArtistID
					pl.Title, pl.Subtitle, pl.Kind = from.Title, from.Subtitle, from.Kind
				}
			}
			pl.ID, pl.ProfileID = domain.NewID(), profile
			ok, err := q.AddImportedPlay(ctx, pl, p.key)
			if err != nil {
				return err
			}
			if ok {
				added++
			}
		}
		return nil
	})
	return added, err
}

// fileIndex indexes the present files of every library by name, to find a Jellyfin file by the tail
// of its path: both servers read the same files, but rarely under the same path (/media/... in a
// container, D:\media\... elsewhere).
func (a *App) fileIndex(ctx context.Context) (*fileIndex, error) {
	read := a.store.Read()
	libs, err := read.Libraries(ctx)
	if err != nil {
		return nil, err
	}
	idx := &fileIndex{byName: map[string][]*indexedFile{}}
	for _, lib := range libs {
		rows, err := read.PresentFileItems(ctx, lib.ID)
		if err != nil {
			return nil, err
		}
		byPath := map[string]*indexedFile{}
		for _, r := range rows {
			f := byPath[r.Path]
			if f == nil {
				f = &indexedFile{parts: pathParts(r.Path), library: lib.ID}
				byPath[r.Path] = f
				idx.byName[f.parts[0]] = append(idx.byName[f.parts[0]], f)
			}
			f.items = append(f.items, r)
		}
	}
	return idx, nil
}

type fileIndex struct {
	byName map[string][]*indexedFile
}

type indexedFile struct {
	// parts holds the path elements in lower case, from the file name up to the root.
	parts   []string
	library domain.ID
	items   []store.FileItem
}

// match finds a file by its path in Jellyfin: the one with the same name whose path shares the
// longest tail, at least the parent folder if several have that name. Nothing on a tie.
func (idx *fileIndex) match(path string) *indexedFile {
	parts := pathParts(path)
	var best *indexedFile
	bestScore, tie := 0, false
	candidates := idx.byName[parts[0]]
	for _, f := range candidates {
		score := 0
		for score < len(parts) && score < len(f.parts) && parts[score] == f.parts[score] {
			score++
		}
		switch {
		case score > bestScore:
			best, bestScore, tie = f, score, false
		case score == bestScore:
			tie = true
		}
	}
	if best == nil || tie || (bestScore < 2 && len(candidates) > 1) {
		return nil
	}
	return best
}

// pathParts splits a Windows or POSIX path, from the file name up to the root.
func pathParts(path string) []string {
	parts := strings.FieldsFunc(strings.ToLower(path), func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return []string{""}
	}
	slices.Reverse(parts)
	return parts
}

// accountImportedText tells that a Jellyfin account was imported.
func accountImportedText(username string, admin bool) domain.Text {
	if admin {
		return domain.T("activity.account_imported_admin", "username", username)
	}
	return domain.T("activity.account_imported", "username", username)
}
