package domain

// Event is pushed to connected clients so they can refresh without polling. An event says what
// changed, not the new state: the client reloads what it shows.
//
//sumtype:decl
type Event interface{ isEvent() }

// Resync means events were lost because the client was too slow. Everything on screen should be
// reloaded.
type Resync struct{}

// LibrariesChanged is sent when a library is created, changed or deleted.
type LibrariesChanged struct{}

// LibraryScanned is sent to administrators when a library scan ends.
type LibraryScanned struct {
	LibraryID                                       ID
	Files, Added, Changed, Moved, Missing, Returned int
	Forgotten                                       int
}

// ItemsChanged is sent when items of a library are added, changed or removed. Truncated means the
// list is incomplete and the whole library should be reloaded.
type ItemsChanged struct {
	LibraryID ID
	ItemIDs   []ID
	Truncated bool
}

// UserDataChanged is sent when a profile's data on some items changed (played, resume point,
// favorite), possibly from another device. Truncated means there were too many items to list.
type UserDataChanged struct {
	ProfileID ID
	ItemIDs   []ID
	Truncated bool
}

// DownloadsChanged is sent when downloads of a device change (created, preparing, ready, failed,
// removed). Only that device's session gets it.
type DownloadsChanged struct {
	SessionID   ID
	DownloadIDs []ID
}

// ThemesChanged is sent when the theme to show may have changed: a theme was edited or deleted, the
// server theme changed (ProfileID nil: everyone), or a profile picked another one (that profile
// only). The client reloads its theme.
type ThemesChanged struct {
	ProfileID *ID
}

// RequestsChanged is sent when requests change (created, approved, declined, downloading,
// available, removed): to the profile that made them and to the administrators.
type RequestsChanged struct {
	ProfileID  ID
	RequestIDs []ID
}

// SubtitleSearchChanged is sent to the profile that asked for a subtitle when its search starts or
// ends.
type SubtitleSearchChanged struct {
	ProfileID ID
	FileID    ID
}

func (SubtitleSearchChanged) isEvent() {}
func (Resync) isEvent()                {}
func (ThemesChanged) isEvent()         {}
func (RequestsChanged) isEvent()       {}
func (DownloadsChanged) isEvent()      {}
func (LibrariesChanged) isEvent()      {}
func (LibraryScanned) isEvent()        {}
func (ItemsChanged) isEvent()          {}
func (UserDataChanged) isEvent()       {}
