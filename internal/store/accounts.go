package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// NameKey normalizes a name (account, profile) for comparisons: uniqueness ignores case and
// surrounding spaces.
func NameKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// Accounts.

// CountAccounts returns the number of accounts.
func (q Q) CountAccounts(ctx context.Context) (int64, error) { return q.q.CountAccounts(ctx) }

// CreateAccount stores an account and its libraries. ErrDuplicate if the name is taken.
func (q Q) CreateAccount(ctx context.Context, a domain.Account, passwordHash string) error {
	if err := translate(q.q.InsertAccount(ctx, sqlc.InsertAccountParams{
		ID: a.ID, Username: a.Username, UsernameKey: NameKey(a.Username), PasswordHash: passwordHash,
		IsAdmin: toInt(a.IsAdmin), AllLibraries: toInt(a.Libraries.All), MaxAge: nullAge(a.Parental.MaxAge),
		BlockUnrated: toInt(a.Parental.BlockUnrated), DenyDownloads: toInt(a.DenyDownloads),
		DenyRequests: toInt(a.DenyRequests), AutoApproveRequests: toInt(a.AutoApproveRequests), RequestQuota: int64(a.RequestQuota),
		CreatedAt: toMillis(a.CreatedAt), UpdatedAt: toMillis(a.UpdatedAt),
	})); err != nil {
		return err
	}
	return q.setAccountLibraries(ctx, a)
}

// UpdateAccount rewrites an account (except its password) and its libraries. ErrDuplicate if the
// name is taken.
func (q Q) UpdateAccount(ctx context.Context, a domain.Account) error {
	if err := translate(q.q.UpdateAccount(ctx, sqlc.UpdateAccountParams{
		Username: a.Username, UsernameKey: NameKey(a.Username), IsAdmin: toInt(a.IsAdmin), Disabled: toInt(a.Disabled),
		AllLibraries: toInt(a.Libraries.All), MaxAge: nullAge(a.Parental.MaxAge), BlockUnrated: toInt(a.Parental.BlockUnrated),
		DenyDownloads: toInt(a.DenyDownloads), DenyRequests: toInt(a.DenyRequests), AutoApproveRequests: toInt(a.AutoApproveRequests),
		RequestQuota: int64(a.RequestQuota), UpdatedAt: toMillis(a.UpdatedAt), ID: a.ID,
	})); err != nil {
		return err
	}
	if err := q.q.DeleteAccountLibraries(ctx, a.ID); err != nil {
		return err
	}
	return q.setAccountLibraries(ctx, a)
}

func (q Q) setAccountLibraries(ctx context.Context, a domain.Account) error {
	if a.Libraries.All {
		return nil
	}
	for _, lib := range a.Libraries.IDs {
		if err := q.q.InsertAccountLibrary(ctx, sqlc.InsertAccountLibraryParams{AccountID: a.ID, LibraryID: lib}); err != nil {
			return err
		}
	}
	return nil
}

// DeleteAccount deletes an account, its profiles, their data and its sessions.
func (q Q) DeleteAccount(ctx context.Context, id domain.ID) error { return q.q.DeleteAccount(ctx, id) }

// Account reads an account.
func (q Q) Account(ctx context.Context, id domain.ID) (domain.Account, error) {
	row, err := q.q.GetAccount(ctx, id)
	if err != nil {
		return domain.Account{}, err
	}
	return q.withLibraries(ctx, accountFromRow(row))
}

// AccountByUsername reads an account and its password hash.
func (q Q) AccountByUsername(ctx context.Context, username string) (domain.Account, string, error) {
	row, err := q.q.GetAccountByUsername(ctx, NameKey(username))
	if err != nil {
		return domain.Account{}, "", err
	}
	a, err := q.withLibraries(ctx, accountFromRow(row))
	return a, row.PasswordHash, err
}

func (q Q) withLibraries(ctx context.Context, a domain.Account) (domain.Account, error) {
	if a.Libraries.All {
		return a, nil
	}
	ids, err := q.q.ListAccountLibraries(ctx, a.ID)
	a.Libraries.IDs = ids
	return a, err
}

// AccountSummary is an account with its profile count and last activity.
type AccountSummary struct {
	Account  domain.Account
	Profiles int
	// LastActive is the last time one of its sessions was used; nil without a session.
	LastActive *time.Time
}

// AccountSummaries lists the accounts, oldest first.
func (q Q) AccountSummaries(ctx context.Context) ([]AccountSummary, error) {
	rows, err := q.q.ListAccountSummaries(ctx)
	if err != nil {
		return nil, err
	}
	libs, err := q.q.ListAllAccountLibraries(ctx)
	if err != nil {
		return nil, err
	}
	byAccount := map[domain.ID][]domain.ID{}
	for _, l := range libs {
		byAccount[l.AccountID] = append(byAccount[l.AccountID], l.LibraryID)
	}
	out := make([]AccountSummary, len(rows))
	for i, r := range rows {
		a := accountFromRow(sqlc.Account{
			ID: r.ID, Username: r.Username, IsAdmin: r.IsAdmin, Disabled: r.Disabled, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
			AllLibraries: r.AllLibraries, MaxAge: r.MaxAge, BlockUnrated: r.BlockUnrated, DenyDownloads: r.DenyDownloads,
			DenyRequests: r.DenyRequests, AutoApproveRequests: r.AutoApproveRequests, RequestQuota: r.RequestQuota,
		})
		if !a.Libraries.All {
			a.Libraries.IDs = byAccount[a.ID]
		}
		out[i] = AccountSummary{Account: a, Profiles: int(r.ProfileCount)}
		if r.LastActive > 0 {
			t := fromMillis(r.LastActive)
			out[i].LastActive = &t
		}
	}
	return out, nil
}

// CountEnabledAdmins returns the number of enabled administrators.
func (q Q) CountEnabledAdmins(ctx context.Context) (int64, error) { return q.q.CountEnabledAdmins(ctx) }

// PasswordHash reads the password hash of an account.
func (q Q) PasswordHash(ctx context.Context, id domain.ID) (string, error) {
	row, err := q.q.GetAccount(ctx, id)
	return row.PasswordHash, err
}

// SetPassword replaces the password hash of an account.
func (q Q) SetPassword(ctx context.Context, id domain.ID, hash string, now time.Time) error {
	return q.q.UpdateAccountPassword(ctx, sqlc.UpdateAccountPasswordParams{PasswordHash: hash, UpdatedAt: toMillis(now), ID: id})
}

func accountFromRow(r sqlc.Account) domain.Account {
	return domain.Account{
		ID: r.ID, Username: r.Username, IsAdmin: r.IsAdmin == 1, Disabled: r.Disabled == 1,
		Libraries:     domain.LibraryAccess{All: r.AllLibraries == 1},
		Parental:      domain.ParentalControl{MaxAge: optAge(r.MaxAge), BlockUnrated: r.BlockUnrated == 1},
		DenyDownloads: r.DenyDownloads == 1, DenyRequests: r.DenyRequests == 1, AutoApproveRequests: r.AutoApproveRequests == 1,
		RequestQuota: int(r.RequestQuota),
		CreatedAt:    fromMillis(r.CreatedAt), UpdatedAt: fromMillis(r.UpdatedAt),
	}
}

func nullAge(age *int) sql.NullInt64 {
	if age == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*age), Valid: true}
}

func optAge(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	age := int(v.Int64)
	return &age
}

// Profiles.

// CreateProfile stores a profile. An empty pinHash means no PIN. ErrDuplicate if the name is
// already taken in the account.
func (q Q) CreateProfile(ctx context.Context, p domain.Profile, pinHash string) error {
	return translate(q.q.InsertProfile(ctx, sqlc.InsertProfileParams{
		ID: p.ID, AccountID: p.AccountID, Name: p.Name, NameKey: NameKey(p.Name), PinHash: nullString(pinHash),
		Kid: toInt(p.Kid), MaxAge: nullAge(p.Parental.MaxAge), BlockUnrated: toInt(p.Parental.BlockUnrated),
		Language: p.Language, CreatedAt: toMillis(p.CreatedAt), UpdatedAt: toMillis(p.UpdatedAt),
	}))
}

// Profile reads a profile and its PIN hash ("" without a PIN).
func (q Q) Profile(ctx context.Context, id domain.ID) (domain.Profile, string, error) {
	row, err := q.q.GetProfile(ctx, id)
	if err != nil {
		return domain.Profile{}, "", err
	}
	return profileFromRow(row), row.PinHash.String, nil
}

// Profiles lists the profiles of an account, oldest first.
func (q Q) Profiles(ctx context.Context, accountID domain.ID) ([]domain.Profile, error) {
	rows, err := q.q.ListProfiles(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Profile, len(rows))
	for i, r := range rows {
		out[i] = profileFromRow(r)
	}
	return out, nil
}

// UpdateProfile rewrites a profile. An empty pinHash means no PIN. ErrDuplicate if the name is
// taken.
func (q Q) UpdateProfile(ctx context.Context, p domain.Profile, pinHash string) error {
	return translate(q.q.UpdateProfile(ctx, sqlc.UpdateProfileParams{
		Name: p.Name, NameKey: NameKey(p.Name), PinHash: nullString(pinHash), Kid: toInt(p.Kid),
		MaxAge: nullAge(p.Parental.MaxAge), BlockUnrated: toInt(p.Parental.BlockUnrated), Language: p.Language,
		SubtitleMode: string(p.SubtitleMode), SubtitleLanguage: p.SubtitleLanguage,
		UpdatedAt: toMillis(p.UpdatedAt), ID: p.ID,
	}))
}

// DeleteProfile deletes a profile. Sessions that had picked it go back to the profile picker.
func (q Q) DeleteProfile(ctx context.Context, id domain.ID) error { return q.q.DeleteProfile(ctx, id) }

func profileFromRow(r sqlc.Profile) domain.Profile {
	return domain.Profile{
		ID: r.ID, AccountID: r.AccountID, Name: r.Name, HasPIN: r.PinHash.Valid, Kid: r.Kid == 1,
		Parental: domain.ParentalControl{MaxAge: optAge(r.MaxAge), BlockUnrated: r.BlockUnrated == 1},
		ThemeID:  r.ThemeID, ThemeMode: domain.ThemeMode(r.ThemeMode), Language: r.Language,
		SubtitleMode: domain.SubtitleMode(r.SubtitleMode), SubtitleLanguage: r.SubtitleLanguage,
		CreatedAt: fromMillis(r.CreatedAt), UpdatedAt: fromMillis(r.UpdatedAt),
	}
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// Sessions.

// CreateSession stores a session (no profile picked yet) and the hash of its token.
func (q Q) CreateSession(ctx context.Context, s domain.Session, tokenHash string) error {
	return translate(q.q.InsertSession(ctx, sqlc.InsertSessionParams{
		ID: s.ID, AccountID: s.AccountID, TokenHash: tokenHash,
		DeviceName: s.Device.Name, Client: s.Device.Client, ClientVersion: s.Device.ClientVersion, Platform: s.Device.Platform,
		CreatedAt: toMillis(s.CreatedAt), LastUsedAt: toMillis(s.LastUsedAt), ExpiresAt: toMillis(s.ExpiresAt), LastIp: s.LastIP,
	}))
}

// SessionByTokenHash finds the session of a token.
func (q Q) SessionByTokenHash(ctx context.Context, tokenHash string) (domain.Session, error) {
	row, err := q.q.GetSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		return domain.Session{}, err
	}
	return sessionFromRow(row), nil
}

// Session reads a session.
func (q Q) Session(ctx context.Context, id domain.ID) (domain.Session, error) {
	row, err := q.q.GetSession(ctx, id)
	if err != nil {
		return domain.Session{}, err
	}
	return sessionFromRow(row), nil
}

// Sessions lists the sessions of an account that are still valid, most recently used first.
func (q Q) Sessions(ctx context.Context, accountID domain.ID, now time.Time) ([]domain.Session, error) {
	rows, err := q.q.ListSessions(ctx, sqlc.ListSessionsParams{AccountID: accountID, ExpiresAt: toMillis(now)})
	if err != nil {
		return nil, err
	}
	out := make([]domain.Session, len(rows))
	for i, r := range rows {
		out[i] = sessionFromRow(r)
	}
	return out, nil
}

// TouchSession extends a session that was just used.
func (q Q) TouchSession(ctx context.Context, id domain.ID, lastUsed, expires time.Time, ip string) error {
	return q.q.TouchSession(ctx, sqlc.TouchSessionParams{
		LastUsedAt: toMillis(lastUsed), ExpiresAt: toMillis(expires), LastIp: ip, ID: id,
	})
}

// SetSessionProfile picks the profile of a session.
func (q Q) SetSessionProfile(ctx context.Context, sessionID, profileID domain.ID) error {
	return q.q.SetSessionProfile(ctx, sqlc.SetSessionProfileParams{ProfileID: &profileID, ID: sessionID})
}

// DeleteSession closes a session.
func (q Q) DeleteSession(ctx context.Context, id domain.ID) error { return q.q.DeleteSession(ctx, id) }

// DeleteAccountSessions closes every session of an account.
func (q Q) DeleteAccountSessions(ctx context.Context, accountID domain.ID) error {
	return q.q.DeleteAccountSessions(ctx, accountID)
}

// DeleteOtherSessions closes every session of an account but one.
func (q Q) DeleteOtherSessions(ctx context.Context, accountID, keep domain.ID) error {
	return q.q.DeleteOtherSessions(ctx, sqlc.DeleteOtherSessionsParams{AccountID: accountID, ID: keep})
}

// DeleteExpiredSessions deletes expired sessions and returns how many there were.
func (q Q) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	return q.q.DeleteExpiredSessions(ctx, toMillis(now))
}

func sessionFromRow(r sqlc.Session) domain.Session {
	return domain.Session{
		ID: r.ID, AccountID: r.AccountID, ProfileID: r.ProfileID,
		Device:    domain.Device{Name: r.DeviceName, Client: r.Client, ClientVersion: r.ClientVersion, Platform: r.Platform},
		CreatedAt: fromMillis(r.CreatedAt), LastUsedAt: fromMillis(r.LastUsedAt), ExpiresAt: fromMillis(r.ExpiresAt),
		LastIP: r.LastIp,
	}
}
