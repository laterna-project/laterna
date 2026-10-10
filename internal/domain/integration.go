package domain

// IntegrationKind names a program that manages a library and writes its metadata.
type IntegrationKind string

// Known integrations.
const (
	IntegrationSonarr        IntegrationKind = "sonarr"
	IntegrationRadarr        IntegrationKind = "radarr"
	IntegrationLidarr        IntegrationKind = "lidarr"
	IntegrationLazyLibrarian IntegrationKind = "lazylibrarian"
	// Bazarr finds subtitles: it is linked so that profiles can ask for one.
	IntegrationBazarr IntegrationKind = "bazarr"
)

// Integration is the state of an integration as Laterna sees it.
type Integration struct {
	Kind IntegrationKind
	// URL of the instance; empty means not configured.
	URL string
	// Reachable means the instance answers with the stored API key. If not, Error says why.
	Reachable bool
	Error     Text
	Version   string
	// KodiMetadata means Kodi metadata (NFO and images) is on with every option we need.
	// MissingOptions names what is not.
	KodiMetadata   bool
	MissingOptions []string
	// Webhook means Laterna's webhook is installed and enabled.
	Webhook bool
	// Folders counts the tracked series, movies or artists that have files. Unmapped counts those
	// whose folder is in no library, WithoutNFO those without an NFO (WithoutNFOTitles lists the
	// first ones, alphabetically).
	Folders          int
	Unmapped         int
	WithoutNFO       int
	WithoutNFOTitles []string
	// ManagesMetadata means the instance writes Kodi metadata and takes a webhook and a refresh
	// (Sonarr, Radarr, Lidarr), so the fields above apply. False for LazyLibrarian and Bazarr.
	ManagesMetadata bool
}

// IntegrationSetup asks to configure an integration for Laterna.
type IntegrationSetup struct {
	// KodiMetadata turns Kodi metadata on with every option we need.
	KodiMetadata bool
	// WebhookURL is Laterna's address as seen from the instance ("http://192.168.1.10:8096"). The
	// webhook is installed there; empty leaves it alone.
	WebhookURL string
	// Refresh asks the instance to refresh every series, movie or artist, then scans.
	Refresh bool
}
