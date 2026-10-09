package domain

import "time"

// Settings are the server settings that can change at runtime. They live in the database.
type Settings struct {
	// ServerName is the name announced to clients.
	ServerName string
	// Language is used for server-written text when neither the request nor the profile asks for a
	// language the server knows. Defaults to "en".
	Language string
	// DownloadImages allows fetching images that only an NFO gives a URL for (posters, thumbs, cast
	// photos). False means no outgoing request at all.
	DownloadImages bool
	// ScanInterval is the time between automatic library scans; 0 disables them.
	ScanInterval time.Duration
	// MissingGrace is how long a vanished file is remembered. If it comes back in time it keeps its
	// whole history.
	MissingGrace time.Duration
	// Trickplay enables scrubbing thumbnails for videos.
	Trickplay bool
	// DetectSegments enables intro and credits detection by audio. Named chapters are always used.
	DetectSegments bool
	// WatchLibraries watches library folders and scans as soon as they change.
	WatchLibraries bool
	// MaxTranscodes caps concurrent video transcodes; 0 picks a value from the encoder in use and
	// the core count.
	MaxTranscodes int
	// BackupKeep is the number of nightly database backups to keep; 0 turns automatic backups off.
	BackupKeep int
	// PublicURL is where clients reach the server ("https://media.example.org"), empty if unknown.
	// It sets the passkey relying party and the OIDC redirect address.
	PublicURL string
	// WebURL is where the web client lives ("https://app.example.org"). Empty means PublicURL, for
	// a web client served by the server itself. Device login tells users to go there.
	WebURL string
	// PasskeyRPID is the passkey relying party ID, a domain name. Empty means the host of
	// PublicURL.
	PasskeyRPID string
	// PasskeyOrigins are origins allowed besides PublicURL: a web client hosted elsewhere, an
	// Android app ("android:apk-key-hash:...").
	PasskeyOrigins []string
}

// ActivityKind is the kind of an activity log entry.
type ActivityKind string

// Activity log entries.
const (
	ActivityLogin           ActivityKind = "login.succeeded"
	ActivityLoginFailed     ActivityKind = "login.failed"
	ActivityAccountCreated  ActivityKind = "account.created"
	ActivityAccountUpdated  ActivityKind = "account.updated"
	ActivityAccountDeleted  ActivityKind = "account.deleted"
	ActivitySessionRevoked  ActivityKind = "session.revoked"
	ActivityLibraryCreated  ActivityKind = "library.created"
	ActivityLibraryUpdated  ActivityKind = "library.updated"
	ActivityLibraryDeleted  ActivityKind = "library.deleted"
	ActivityLibraryScanned  ActivityKind = "library.scanned"
	ActivityPlaybackStarted ActivityKind = "playback.started"
	ActivityPlaybackStopped ActivityKind = "playback.stopped"
	ActivitySettingsUpdated ActivityKind = "settings.updated"
	ActivityIntegration     ActivityKind = "integration.updated"
	ActivityWebhook         ActivityKind = "integration.webhook"
	ActivityJobFailed       ActivityKind = "job.failed"
	ActivityPartyStarted    ActivityKind = "party.started"
	ActivityImport          ActivityKind = "import"
	ActivityRequest         ActivityKind = "request"
)

// Activity is one entry of the activity log: what happened, who did it, to what.
type Activity struct {
	ID   int64
	At   time.Time
	Kind ActivityKind
	// Warning marks entries worth a look (refused login, failed job).
	Warning   bool
	AccountID *ID
	ProfileID *ID
	ItemID    *ID
	// Text tells the entry. Names are kept in its params, so a deleted account still reads fine.
	// Entries written before texts were keyed are literals.
	Text Text
}
