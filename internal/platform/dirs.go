// Package platform isolates what depends on the OS: the default folder locations.
package platform

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// Dirs groups the server's working folders. They are separate so that the cache can be wiped
// safely: nothing irreplaceable is kept there.
type Dirs struct {
	// Data holds the SQLite database and the logs. Back it up.
	Data string
	// Cache holds resized images, transcodes, extracted subtitles...
	Cache string
	// Metadata holds downloaded images and metadata. Nothing is ever written next to the media.
	Metadata string
	// Backups receives the database backups; empty means under Data.
	Backups string
}

// BackupDir returns the backup folder: Backups, or "backups" under Data.
func (d Dirs) BackupDir() string {
	if d.Backups != "" {
		return d.Backups
	}
	return filepath.Join(d.Data, "backups")
}

// Log returns the log folder (under Data).
func (d Dirs) Log() string { return filepath.Join(d.Data, "log") }

// DefaultDirs returns the default locations for the OS:
//   - Windows: %LOCALAPPDATA%\Laterna\{data,cache,metadata} (not the roaming profile);
//   - macOS: ~/Library/Application Support/Laterna and ~/Library/Caches/Laterna;
//   - Linux: $XDG_DATA_HOME/laterna (or ~/.local/share/laterna) and $XDG_CACHE_HOME/laterna.
func DefaultDirs() (Dirs, error) {
	return defaultDirs(runtime.GOOS, os.Getenv, os.UserHomeDir)
}

func defaultDirs(goos string, getenv func(string) string, home func() (string, error)) (Dirs, error) {
	switch goos {
	case "windows":
		base := getenv("LOCALAPPDATA")
		if base == "" {
			return Dirs{}, errors.New("platform: %LOCALAPPDATA% is not set")
		}
		root := filepath.Join(base, "Laterna")
		return Dirs{
			Data:     filepath.Join(root, "data"),
			Cache:    filepath.Join(root, "cache"),
			Metadata: filepath.Join(root, "metadata"),
		}, nil
	case "darwin":
		h, err := home()
		if err != nil {
			return Dirs{}, err
		}
		data := filepath.Join(h, "Library", "Application Support", "Laterna")
		return Dirs{
			Data:     data,
			Cache:    filepath.Join(h, "Library", "Caches", "Laterna"),
			Metadata: filepath.Join(data, "metadata"),
		}, nil
	default:
		dataHome, cacheHome := getenv("XDG_DATA_HOME"), getenv("XDG_CACHE_HOME")
		if dataHome == "" || cacheHome == "" {
			h, err := home()
			if err != nil {
				return Dirs{}, err
			}
			if dataHome == "" {
				dataHome = filepath.Join(h, ".local", "share")
			}
			if cacheHome == "" {
				cacheHome = filepath.Join(h, ".cache")
			}
		}
		data := filepath.Join(dataHome, "laterna")
		return Dirs{
			Data:     data,
			Cache:    filepath.Join(cacheHome, "laterna"),
			Metadata: filepath.Join(data, "metadata"),
		}, nil
	}
}

// Ensure creates the missing folders.
func (d Dirs) Ensure() error {
	for _, p := range []string{d.Data, d.Cache, d.Metadata, d.Log(), d.BackupDir()} {
		if err := os.MkdirAll(p, 0o750); err != nil {
			return err
		}
	}
	return nil
}
