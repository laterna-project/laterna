package app

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/laterna-project/laterna/internal/auth"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// Accounts managed by an administrator. The API layer checks admin rights from the contract; what
// is left here are the rules: there is always an enabled administrator, an administrator has no
// restrictions, and nobody acts against their own account.

// NewAccount describes an account to create. Nil Libraries means all of them, nil Parental means
// none.
type NewAccount struct {
	Username  string
	Password  string
	IsAdmin   bool
	Libraries *domain.LibraryAccess
	Parental  *domain.ParentalControl
	// DenyDownloads takes offline downloads away (refused for an administrator).
	DenyDownloads bool
}

// AccountChanges describes a change to an account; a nil field is left alone. A new password or
// disabling the account signs out all its devices.
type AccountChanges struct {
	Username  *string
	Password  *string
	IsAdmin   *bool
	Disabled  *bool
	Libraries *domain.LibraryAccess
	Parental  *domain.ParentalControl
	// DenyDownloads takes offline downloads away or gives them back.
	DenyDownloads *bool
}

// Accounts lists the accounts with their profile count and last activity.
func (a *App) Accounts(ctx context.Context) ([]store.AccountSummary, error) {
	return a.store.Read().AccountSummaries(ctx)
}

// CreateAccount creates an account with a profile of the same name. p is the administrator asking.
func (a *App) CreateAccount(ctx context.Context, p domain.Principal, n NewAccount) (domain.Account, error) {
	username := strings.TrimSpace(n.Username)
	if err := validateUsername(username); err != nil {
		return domain.Account{}, err
	}
	if err := validatePassword(n.Password); err != nil {
		return domain.Account{}, err
	}
	now := a.now()
	if n.IsAdmin && n.DenyDownloads {
		return domain.Account{}, domain.Invalid("account.admin_always_downloads")
	}
	account := domain.Account{
		ID: domain.NewID(), Username: username, IsAdmin: n.IsAdmin, Libraries: domain.AllLibraries(),
		DenyDownloads: n.DenyDownloads, CreatedAt: now, UpdatedAt: now,
	}
	if err := a.applyRestrictions(ctx, &account, n.Libraries, n.Parental); err != nil {
		return domain.Account{}, err
	}
	hash, err := auth.HashPassword(n.Password)
	if err != nil {
		return domain.Account{}, err
	}
	profile := domain.Profile{ID: domain.NewID(), AccountID: account.ID, Name: username, CreatedAt: now, UpdatedAt: now}
	err = a.store.Write(ctx, func(q store.Q) error {
		if err := accountWriteError(q.CreateAccount(ctx, account, hash), username); err != nil {
			return err
		}
		return q.CreateProfile(ctx, profile, "")
	})
	if err != nil {
		return domain.Account{}, err
	}
	a.log.InfoContext(ctx, "account created", "account", account.ID, "username", username, "admin", account.IsAdmin)
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityAccountCreated, AccountID: &p.Account.ID,
		Text: accountCreatedText(p.Account.Username, username, account.IsAdmin),
	})
	return account, nil
}

// UpdateAccount changes an account. p is the administrator asking: they can neither disable
// themselves nor drop their own admin rights.
func (a *App) UpdateAccount(ctx context.Context, p domain.Principal, id domain.ID, ch AccountChanges) (domain.Account, error) {
	self := id == p.Account.ID
	if self && ((ch.Disabled != nil && *ch.Disabled) || (ch.IsAdmin != nil && !*ch.IsAdmin)) {
		return domain.Account{}, domain.Precondition("account.own_account")
	}
	var hash string
	if ch.Password != nil {
		if err := validatePassword(*ch.Password); err != nil {
			return domain.Account{}, err
		}
		var err error
		if hash, err = auth.HashPassword(*ch.Password); err != nil {
			return domain.Account{}, err
		}
	}
	var account domain.Account
	err := a.store.Write(ctx, func(q store.Q) error {
		var err error
		account, err = q.Account(ctx, id)
		if store.IsNotFound(err) {
			return domain.NotFound("account.not_found")
		}
		if err != nil {
			return err
		}
		wasActiveAdmin := account.IsAdmin && !account.Disabled
		if ch.Username != nil {
			name := strings.TrimSpace(*ch.Username)
			if err := validateUsername(name); err != nil {
				return err
			}
			account.Username = name
		}
		if ch.IsAdmin != nil {
			if *ch.IsAdmin && !account.IsAdmin {
				// An administrator sees everything: their restrictions are lifted.
				account.Libraries, account.Parental, account.DenyDownloads = domain.AllLibraries(), domain.ParentalControl{}, false
			}
			account.IsAdmin = *ch.IsAdmin
		}
		if ch.Disabled != nil {
			account.Disabled = *ch.Disabled
		}
		if ch.DenyDownloads != nil {
			if *ch.DenyDownloads && account.IsAdmin {
				return domain.Invalid("account.admin_always_downloads")
			}
			account.DenyDownloads = *ch.DenyDownloads
		}
		if err := a.applyRestrictions(ctx, &account, ch.Libraries, ch.Parental); err != nil {
			return err
		}
		account.UpdatedAt = a.now()
		if err := accountWriteError(q.UpdateAccount(ctx, account), account.Username); err != nil {
			return err
		}
		if wasActiveAdmin && (!account.IsAdmin || account.Disabled) {
			if err := keepAnAdmin(ctx, q); err != nil {
				return err
			}
		}
		if hash != "" {
			if err := q.SetPassword(ctx, account.ID, hash, account.UpdatedAt); err != nil {
				return err
			}
		}
		if hash != "" || account.Disabled {
			return q.DeleteAccountSessions(ctx, account.ID)
		}
		return nil
	})
	if err != nil {
		return domain.Account{}, err
	}
	a.log.InfoContext(ctx, "account updated", "account", account.ID, "username", account.Username,
		"admin", account.IsAdmin, "disabled", account.Disabled, "password", hash != "")
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityAccountUpdated, AccountID: &p.Account.ID,
		Text: accountChangesText(p.Account.Username, account.Username, ch),
	})
	return account, nil
}

// DeleteAccount deletes an account, its profiles, their data and its sessions. p is the
// administrator asking: they cannot delete their own account.
func (a *App) DeleteAccount(ctx context.Context, p domain.Principal, id domain.ID) error {
	if id == p.Account.ID {
		return domain.Precondition("account.own_account")
	}
	var account domain.Account
	err := a.store.Write(ctx, func(q store.Q) error {
		var err error
		account, err = q.Account(ctx, id)
		if store.IsNotFound(err) {
			return domain.NotFound("account.not_found")
		}
		if err != nil {
			return err
		}
		if err := q.DeleteAccount(ctx, id); err != nil {
			return err
		}
		if account.IsAdmin && !account.Disabled {
			return keepAnAdmin(ctx, q)
		}
		return nil
	})
	if err == nil {
		a.log.InfoContext(ctx, "account deleted", "account", id, "username", account.Username)
		a.record(ctx, domain.Activity{
			Kind: domain.ActivityAccountDeleted, AccountID: &p.Account.ID,
			Text: domain.T("activity.account_deleted", "actor", p.Account.Username, "username", account.Username),
		})
	}
	return err
}

// applyRestrictions applies the requested libraries and parental control (nil leaves them alone)
// after checking them. An administrator has none.
func (a *App) applyRestrictions(ctx context.Context, account *domain.Account, libs *domain.LibraryAccess, parental *domain.ParentalControl) error {
	if account.IsAdmin && ((libs != nil && !libs.All) || (parental != nil && parental.Active())) {
		return domain.Invalid("account.admin_unrestricted")
	}
	if libs != nil {
		access := domain.LibraryAccess{All: libs.All}
		if !libs.All {
			known, err := a.store.Read().Libraries(ctx)
			if err != nil {
				return err
			}
			access.IDs = []domain.ID{}
			for _, id := range libs.IDs {
				if !slices.ContainsFunc(known, func(l domain.Library) bool { return l.ID == id }) {
					return domain.Invalid("library.not_found", "library_id", id)
				}
				if !slices.Contains(access.IDs, id) {
					access.IDs = append(access.IDs, id)
				}
			}
		}
		account.Libraries = access
	}
	if parental != nil {
		if err := validateParental(*parental); err != nil {
			return err
		}
		account.Parental = *parental
	}
	return nil
}

// keepAnAdmin checks, inside the transaction, that an enabled administrator is left.
func keepAnAdmin(ctx context.Context, q store.Q) error {
	n, err := q.CountEnabledAdmins(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.Precondition("account.last_admin")
	}
	return nil
}

// accountCreatedText tells that an administrator created an account.
func accountCreatedText(actor, username string, admin bool) domain.Text {
	if admin {
		return domain.T("activity.account_created_admin", "actor", actor, "username", username)
	}
	return domain.T("activity.account_created", "actor", actor, "username", username)
}

// accountChangesText tells that an account was changed, and what changed.
func accountChangesText(actor, username string, ch AccountChanges) domain.Text {
	var parts []domain.Text
	either := func(b bool, yes, no domain.Text) domain.Text {
		if b {
			return yes
		}
		return no
	}
	if ch.Username != nil {
		parts = append(parts, domain.T("activity.change.username"))
	}
	if ch.Password != nil {
		parts = append(parts, domain.T("activity.change.password"))
	}
	if ch.IsAdmin != nil {
		parts = append(parts, either(*ch.IsAdmin, domain.T("activity.change.promoted"), domain.T("activity.change.demoted")))
	}
	if ch.Disabled != nil {
		parts = append(parts, either(*ch.Disabled, domain.T("activity.change.disabled"), domain.T("activity.change.enabled")))
	}
	if ch.Libraries != nil {
		parts = append(parts, domain.T("activity.change.libraries"))
	}
	if ch.Parental != nil {
		parts = append(parts, domain.T("activity.change.parental"))
	}
	if ch.DenyDownloads != nil {
		parts = append(parts, either(*ch.DenyDownloads, domain.T("activity.change.downloads_denied"), domain.T("activity.change.downloads_allowed")))
	}
	if len(parts) == 0 {
		return domain.T("activity.account_updated", "actor", actor, "username", username)
	}
	return domain.T("activity.account_changed", "actor", actor, "username", username, parts)
}

func accountWriteError(err error, username string) error {
	if errors.Is(err, store.ErrDuplicate) {
		return domain.Conflict("account.username_taken", "username", username)
	}
	return err
}
