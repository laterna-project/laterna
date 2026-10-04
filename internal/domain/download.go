package domain

import "time"

// DownloadQuality is the quality of an offline download.
type DownloadQuality string

// Download qualities: the file as it is when the device can play it, or a lighter copy.
const (
	DownloadOriginal DownloadQuality = "original"
	DownloadHigh     DownloadQuality = "high"
	DownloadMedium   DownloadQuality = "medium"
	DownloadLow      DownloadQuality = "low"
)

// Valid reports a known quality.
func (q DownloadQuality) Valid() bool {
	switch q {
	case DownloadOriginal, DownloadHigh, DownloadMedium, DownloadLow:
		return true
	}
	return false
}

// DownloadState is the state of a download.
type DownloadState string

// Download states: waiting to be prepared, being prepared (the server is converting it), ready to
// fetch, or failed.
const (
	DownloadQueued    DownloadState = "queued"
	DownloadPreparing DownloadState = "preparing"
	DownloadReady     DownloadState = "ready"
	DownloadFailed    DownloadState = "failed"
)

// Download is a file a device asked for so it can play it offline. It belongs to the session (the
// device) and the profile that asked.
type Download struct {
	ID        ID
	AccountID ID
	ProfileID ID
	SessionID ID
	ItemID    ID
	FileID    ID
	Quality   DownloadQuality
	State     DownloadState
	// Progress of the preparation, from 0 to 1.
	Progress float64
	// Estimate is the expected size in bytes, Size the real one once ready.
	Estimate, Size int64
	// Path is the file to serve, either the original or the prepared copy. Empty until ready.
	Path string
	// Error says why the preparation failed.
	Error     string
	CreatedAt time.Time
	UpdatedAt time.Time
	ReadyAt   *time.Time
}
