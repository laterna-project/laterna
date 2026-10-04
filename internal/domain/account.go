package domain

import "time"

// Account is a person or a household: it signs in and owns profiles.
type Account struct {
	ID       ID
	Username string
	IsAdmin  bool
	Disabled bool
	// Libraries the account may browse. An administrator sees them all.
	Libraries LibraryAccess
	// Parental is set by an administrator and applies on top of each profile's own control. Never
	// set on an administrator.
	Parental ParentalControl
	// DenyDownloads takes offline downloads away from the account. Never set on an administrator.
	DenyDownloads bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Profile is one "Who's watching?" entry of an account. History, favorites and resume points belong
// to it.
type Profile struct {
	ID        ID
	AccountID ID
	Name      string
	HasPIN    bool
	Kid       bool
	// Parental is the profile's own control, set by the account holder.
	Parental ParentalControl
	// ThemeID is the chosen theme (nil: the server's). ThemeMode is dark, light or whatever the
	// device uses.
	ThemeID   *ID
	ThemeMode ThemeMode
	// Language is a BCP 47 tag ("fr", "pt-BR"). Empty means the device's language, then the
	// server's.
	Language  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Restricted reports a kid profile or one under parental control. Such a profile cannot manage
// profiles, devices, the password or the server.
func (p Profile) Restricted() bool { return p.Kid || p.Parental.Active() }

// Device is what a client said about itself when it signed in.
type Device struct {
	Name          string
	Client        string
	ClientVersion string
	Platform      string
}

// Session is a device signed in to an account.
type Session struct {
	ID        ID
	AccountID ID
	// ProfileID is the profile picked on the device, nil until one is.
	ProfileID  *ID
	Device     Device
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time
	LastIP     string
}

// Principal is the caller of an authenticated request.
type Principal struct {
	SessionID ID
	Account   Account
	// Profile is nil until the session has picked one.
	Profile *Profile
}

// Restricted reports a caller on a restricted profile.
func (p Principal) Restricted() bool { return p.Profile != nil && p.Profile.Restricted() }

// CanAdminister reports an administrator who is not on a restricted profile: a kid on the
// administrator's account administers nothing.
func (p Principal) CanAdminister() bool { return p.Account.IsAdmin && !p.Restricted() }

// AllowsLibrary reports whether the caller's account may browse the library.
func (p Principal) AllowsLibrary(id ID) bool {
	return p.Account.IsAdmin || p.Account.Libraries.Allows(id)
}

// CanDownload reports whether the caller may download for offline playback.
func (p Principal) CanDownload() bool { return p.Account.IsAdmin || !p.Account.DenyDownloads }

// Viewer returns what the chosen profile is allowed to see; ok is false when no profile is chosen.
// An administrator sees every library, only the profile's own control applies.
func (p Principal) Viewer() (Viewer, bool) {
	if p.Profile == nil {
		return Viewer{}, false
	}
	v := Viewer{ProfileID: p.Profile.ID, Parental: p.Profile.Parental}
	if p.Account.IsAdmin {
		return v, true
	}
	v.Parental = v.Parental.Combine(p.Account.Parental)
	if !p.Account.Libraries.All {
		v.Libraries = append(make([]ID, 0, len(p.Account.Libraries.IDs)), p.Account.Libraries.IDs...)
	}
	return v, true
}
