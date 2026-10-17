package app

import (
	"context"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/laterna-project/laterna/internal/auth"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// API tokens: sessions of a tool (Home Assistant, a dashboard, a script) rather than of a device.
// A token acts as the profile that created it, does not expire and is never extended, and goes
// away like the other sessions: revoked, or when the password changes or the account is disabled.
// Which methods it may call is declared in the contract (option api_tokens) and enforced by the API
// layer.

// API token limits.
const (
	maxAPITokens       = 50
	maxAPITokenNameLen = 64
	// apiTokenLifetime stands for "no expiry": the expiry of a session is a date.
	apiTokenLifetime = 100 * 365 * 24 * time.Hour
)

// APIToken is an API token and its profile (nil if that profile was deleted).
type APIToken struct {
	Session domain.Session
	Profile *domain.Profile
}

// CreateAPIToken creates an API token for the caller's profile. It returns the token, the only
// time it is seen: only its hash is kept.
func (a *App) CreateAPIToken(ctx context.Context, p domain.Principal, name string, readOnly bool) (string, APIToken, error) {
	if p.Restricted() {
		return "", APIToken{}, domain.Forbidden("auth.restricted_profile")
	}
	if p.Profile == nil {
		return "", APIToken{}, domain.Precondition("profile.required")
	}
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", APIToken{}, domain.Invalid("api_token.name_required")
	case utf8.RuneCountInString(name) > maxAPITokenNameLen:
		return "", APIToken{}, domain.Invalid("api_token.name_too_long", "max", maxAPITokenNameLen)
	case strings.ContainsFunc(name, unicode.IsControl):
		return "", APIToken{}, domain.Invalid("api_token.name_invalid")
	}
	token, hash, err := auth.NewToken()
	if err != nil {
		return "", APIToken{}, err
	}
	now := a.now()
	s := domain.Session{
		ID: domain.NewID(), AccountID: p.Account.ID, ProfileID: &p.Profile.ID,
		Device:    domain.Device{Name: name, Client: "API token"},
		CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(apiTokenLifetime), APIToken: true, ReadOnly: readOnly,
	}
	err = a.store.Write(ctx, func(q store.Q) error {
		n, err := q.CountAPITokens(ctx, p.Account.ID)
		if err != nil {
			return err
		}
		if n >= maxAPITokens {
			return domain.Precondition("api_token.too_many", "max", maxAPITokens)
		}
		return q.CreateSession(ctx, s, hash)
	})
	if err != nil {
		return "", APIToken{}, err
	}
	a.log.InfoContext(ctx, "API token created", "account", p.Account.ID, "profile", p.Profile.ID, "name", name, "read_only", readOnly)
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityAPITokenCreated, AccountID: &p.Account.ID, ProfileID: &p.Profile.ID,
		Text: domain.T("activity.api_token_created", "username", p.Account.Username, "profile", p.Profile.Name, "name", name),
	})
	return token, APIToken{Session: s, Profile: p.Profile}, nil
}

// APITokens lists the API tokens of the caller's account, the most recently used first.
func (a *App) APITokens(ctx context.Context, p domain.Principal) ([]APIToken, error) {
	read := a.store.Read()
	sessions, err := read.APITokens(ctx, p.Account.ID)
	if err != nil {
		return nil, err
	}
	profiles, err := read.Profiles(ctx, p.Account.ID)
	if err != nil {
		return nil, err
	}
	byID := make(map[domain.ID]*domain.Profile, len(profiles))
	for i := range profiles {
		byID[profiles[i].ID] = &profiles[i]
	}
	out := make([]APIToken, len(sessions))
	for i, s := range sessions {
		out[i] = APIToken{Session: s}
		if s.ProfileID != nil {
			out[i].Profile = byID[*s.ProfileID]
		}
	}
	return out, nil
}

// RevokeAPIToken revokes an API token of the caller's account.
func (a *App) RevokeAPIToken(ctx context.Context, p domain.Principal, id domain.ID) error {
	if p.Restricted() {
		return domain.Forbidden("auth.restricted_profile")
	}
	var name string
	err := a.store.Write(ctx, func(q store.Q) error {
		s, err := q.Session(ctx, id)
		if store.IsNotFound(err) || (err == nil && (s.AccountID != p.Account.ID || !s.APIToken)) {
			return domain.NotFound("api_token.not_found")
		}
		if err != nil {
			return err
		}
		name = s.Device.Name
		return q.DeleteSession(ctx, id)
	})
	if err != nil {
		return err
	}
	a.log.InfoContext(ctx, "API token revoked", "account", p.Account.ID, "name", name)
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityAPITokenRevoked, AccountID: &p.Account.ID,
		Text: domain.T("activity.api_token_revoked", "username", p.Account.Username, "name", name),
	})
	return nil
}
