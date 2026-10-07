package app

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/laterna-project/laterna/internal/auth"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/i18n"
	"github.com/laterna-project/laterna/internal/store"
)

const (
	maxProfiles       = 12
	maxProfileNameLen = 40
)

// ProfileChanges describes a change to a profile; a nil field is left alone. An empty PIN removes
// the PIN, and an inactive parental control removes the control.
type ProfileChanges struct {
	Name     *string
	PIN      *string
	Kid      *bool
	Parental *domain.ParentalControl
	// Language is a language tag; empty means none.
	Language *string
}

// Profiles lists the profiles of the caller's account.
func (a *App) Profiles(ctx context.Context, p domain.Principal) ([]domain.Profile, error) {
	return a.store.Read().Profiles(ctx, p.Account.ID)
}

// CreateProfile adds a profile to the caller's account. A nil parental means the default control
// for a kid profile (domain.KidParentalControl) and none for an adult.
func (a *App) CreateProfile(ctx context.Context, p domain.Principal, name, pin string, kid bool, parental *domain.ParentalControl, language string) (domain.Profile, error) {
	if p.Restricted() {
		return domain.Profile{}, domain.Forbidden("auth.restricted_profile")
	}
	name = strings.TrimSpace(name)
	if err := validateProfileName(name); err != nil {
		return domain.Profile{}, err
	}
	language, err := profileLanguage(language)
	if err != nil {
		return domain.Profile{}, err
	}
	control := domain.ParentalControl{}
	switch {
	case parental != nil:
		if err := validateParental(*parental); err != nil {
			return domain.Profile{}, err
		}
		control = *parental
	case kid:
		control = domain.KidParentalControl()
	}
	pinHash, err := hashPIN(pin)
	if err != nil {
		return domain.Profile{}, err
	}
	now := a.now()
	profile := domain.Profile{
		ID: domain.NewID(), AccountID: p.Account.ID, Name: name, HasPIN: pinHash != "", Kid: kid, Parental: control,
		Language: language, CreatedAt: now, UpdatedAt: now,
	}
	err = a.store.Write(ctx, func(q store.Q) error {
		existing, err := q.Profiles(ctx, p.Account.ID)
		if err != nil {
			return err
		}
		if len(existing) >= maxProfiles {
			return domain.Precondition("profile.too_many", "max", maxProfiles)
		}
		return profileWriteError(q.CreateProfile(ctx, profile, pinHash), name)
	})
	return profile, err
}

// UpdateProfile changes a profile of the caller's account. currentPIN is required if the profile
// has a PIN. A profile that becomes a kid profile without parental control gets the default one.
func (a *App) UpdateProfile(ctx context.Context, p domain.Principal, id domain.ID, currentPIN string, ch ProfileChanges) (domain.Profile, error) {
	if p.Restricted() {
		return domain.Profile{}, domain.Forbidden("auth.restricted_profile")
	}
	profile, pinHash, err := a.ownProfile(ctx, p, id)
	if err != nil {
		return domain.Profile{}, err
	}
	if err := a.checkPIN(profile, pinHash, currentPIN); err != nil {
		return domain.Profile{}, err
	}
	if ch.Name != nil {
		name := strings.TrimSpace(*ch.Name)
		if err := validateProfileName(name); err != nil {
			return domain.Profile{}, err
		}
		profile.Name = name
	}
	if ch.PIN != nil {
		if pinHash, err = hashPIN(*ch.PIN); err != nil {
			return domain.Profile{}, err
		}
		profile.HasPIN = pinHash != ""
	}
	if ch.Parental != nil {
		if err := validateParental(*ch.Parental); err != nil {
			return domain.Profile{}, err
		}
		profile.Parental = *ch.Parental
	}
	if ch.Kid != nil {
		if *ch.Kid && !profile.Kid && ch.Parental == nil && !profile.Parental.Active() {
			profile.Parental = domain.KidParentalControl()
		}
		profile.Kid = *ch.Kid
	}
	if ch.Language != nil {
		if profile.Language, err = profileLanguage(*ch.Language); err != nil {
			return domain.Profile{}, err
		}
	}
	profile.UpdatedAt = a.now()
	err = a.store.Write(ctx, func(q store.Q) error {
		if profile.Restricted() {
			if err := keepAFreeProfile(ctx, q, p.Account.ID, profile.ID); err != nil {
				return err
			}
		}
		return profileWriteError(q.UpdateProfile(ctx, profile, pinHash), profile.Name)
	})
	return profile, err
}

// SetLanguage changes the language of the picked profile. Everyone sets their own, including a
// restricted profile, which can change nothing else.
func (a *App) SetLanguage(ctx context.Context, p domain.Principal, language string) (domain.Profile, error) {
	if p.Profile == nil {
		return domain.Profile{}, domain.Precondition("profile.required")
	}
	language, err := profileLanguage(language)
	if err != nil {
		return domain.Profile{}, err
	}
	var profile domain.Profile
	err = a.store.Write(ctx, func(q store.Q) error {
		var pinHash string
		var err error
		if profile, pinHash, err = q.Profile(ctx, p.Profile.ID); err != nil {
			return err
		}
		profile.Language, profile.UpdatedAt = language, a.now()
		return q.UpdateProfile(ctx, profile, pinHash)
	})
	return profile, err
}

// SetSubtitlePreferences changes when playback starts with a subtitle, and in which language, for
// the picked profile. Everyone sets their own, including a restricted profile.
func (a *App) SetSubtitlePreferences(ctx context.Context, p domain.Principal, mode domain.SubtitleMode, language string) (domain.Profile, error) {
	if p.Profile == nil {
		return domain.Profile{}, domain.Precondition("profile.required")
	}
	switch mode {
	case domain.SubtitleAuto, domain.SubtitleAlways, domain.SubtitleForced, domain.SubtitleOff:
	default:
		return domain.Profile{}, domain.Invalid("profile.invalid_subtitle_mode", "mode", string(mode))
	}
	language, err := profileLanguage(language)
	if err != nil {
		return domain.Profile{}, err
	}
	var profile domain.Profile
	err = a.store.Write(ctx, func(q store.Q) error {
		var pinHash string
		var err error
		if profile, pinHash, err = q.Profile(ctx, p.Profile.ID); err != nil {
			return err
		}
		profile.SubtitleMode, profile.SubtitleLanguage, profile.UpdatedAt = mode, language, a.now()
		return q.UpdateProfile(ctx, profile, pinHash)
	})
	return profile, err
}

// profileLanguage validates the language of a profile: a well-formed tag, whether the server has a
// catalog for it or not (the client may have one). Empty means none.
func profileLanguage(tag string) (string, error) {
	tag = strings.TrimSpace(tag)
	if tag != "" && !i18n.ValidTag(tag) {
		return "", domain.Invalid("profile.invalid_language", "language", tag)
	}
	return tag, nil
}

// DeleteProfile deletes a profile of the caller's account, with its history and favorites.
func (a *App) DeleteProfile(ctx context.Context, p domain.Principal, id domain.ID, currentPIN string) error {
	if p.Restricted() {
		return domain.Forbidden("auth.restricted_profile")
	}
	profile, pinHash, err := a.ownProfile(ctx, p, id)
	if err != nil {
		return err
	}
	if err := a.checkPIN(profile, pinHash, currentPIN); err != nil {
		return err
	}
	return a.store.Write(ctx, func(q store.Q) error {
		if err := keepAFreeProfile(ctx, q, p.Account.ID, profile.ID); err != nil {
			return err
		}
		return q.DeleteProfile(ctx, profile.ID)
	})
}

// SelectProfile picks the profile of the caller's session.
func (a *App) SelectProfile(ctx context.Context, p domain.Principal, id domain.ID, pin string) (domain.Profile, error) {
	profile, pinHash, err := a.ownProfile(ctx, p, id)
	if err != nil {
		return domain.Profile{}, err
	}
	if err := a.checkPIN(profile, pinHash, pin); err != nil {
		return domain.Profile{}, err
	}
	err = a.store.Write(ctx, func(q store.Q) error { return q.SetSessionProfile(ctx, p.SessionID, profile.ID) })
	return profile, err
}

// ownProfile reads a profile of the caller's account (not found if it belongs to another).
func (a *App) ownProfile(ctx context.Context, p domain.Principal, id domain.ID) (domain.Profile, string, error) {
	profile, pinHash, err := a.store.Read().Profile(ctx, id)
	if store.IsNotFound(err) || (err == nil && profile.AccountID != p.Account.ID) {
		return domain.Profile{}, "", domain.NotFound("profile.not_found")
	}
	return profile, pinHash, err
}

// checkPIN checks the PIN of a protected profile, slowing down repeated attempts.
func (a *App) checkPIN(profile domain.Profile, pinHash, pin string) error {
	if !profile.HasPIN {
		return nil
	}
	key := "profile:" + profile.ID.String()
	if wait, ok := a.limiter.Allow(key); !ok {
		return domain.TooManyAttempts("profile.too_many_pin_attempts", "retry_after_seconds", roundUp(wait))
	}
	if pin == "" {
		return domain.Forbidden("profile.pin_required")
	}
	ok, _, err := auth.VerifyPassword(pinHash, pin)
	if err != nil {
		return err
	}
	if !ok {
		a.limiter.Fail(key)
		return domain.Forbidden("profile.wrong_pin")
	}
	a.limiter.Succeed(key)
	return nil
}

// keepAFreeProfile refuses to delete (or restrict) the last unrestricted profile: without it nobody
// could manage the account's profiles anymore.
func keepAFreeProfile(ctx context.Context, q store.Q, accountID, changing domain.ID) error {
	profiles, err := q.Profiles(ctx, accountID)
	if err != nil {
		return err
	}
	for _, other := range profiles {
		if other.ID != changing && !other.Restricted() {
			return nil
		}
	}
	return domain.Precondition("profile.last_unrestricted")
}

// validateParental checks a parental control.
func validateParental(c domain.ParentalControl) error {
	if c.MaxAge != nil && (*c.MaxAge < 0 || *c.MaxAge > domain.MaxRatingAge) {
		return domain.Invalid("profile.invalid_max_age", "max", domain.MaxRatingAge)
	}
	return nil
}

func profileWriteError(err error, name string) error {
	if errors.Is(err, store.ErrDuplicate) {
		return domain.Conflict("profile.name_taken", "name", name)
	}
	return err
}

func validateProfileName(name string) error {
	if name == "" {
		return domain.Invalid("profile.name_required")
	}
	if utf8.RuneCountInString(name) > maxProfileNameLen {
		return domain.Invalid("profile.name_too_long", "max", maxProfileNameLen)
	}
	if strings.ContainsFunc(name, unicode.IsControl) {
		return domain.Invalid("profile.name_invalid")
	}
	return nil
}

// hashPIN validates and hashes a profile PIN (4 to 8 digits); "" means no PIN.
func hashPIN(pin string) (string, error) {
	if pin == "" {
		return "", nil
	}
	if len(pin) < 4 || len(pin) > 8 || strings.ContainsFunc(pin, func(r rune) bool { return r < '0' || r > '9' }) {
		return "", domain.Invalid("profile.invalid_pin")
	}
	return auth.HashPassword(pin)
}
