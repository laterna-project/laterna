// Package jellyfintest writes a Jellyfin 10.11 database cut down to the tables and columns the
// import reads, in Jellyfin's own formats: upper-case IDs with dashes (32 lower-case hex digits in
// the activity log), EF Core dates in UTC, durations in 100 ns ticks, permissions and preferences
// by index.
package jellyfintest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go SQLite driver (no cgo)
)

// Server is the content of a Jellyfin server.
type Server struct {
	Users     []User
	Libraries []Library
	Items     []Item
	UserData  []UserData
	Log       []Event
}

// User is a user. Password is a hash in Jellyfin's format ("" for none).
type User struct {
	ID, Name, Password string
	Admin, Disabled    bool
	// AllLibraries, otherwise Libraries (library IDs).
	AllLibraries bool
	Libraries    []string
	MaxScore     *int
	BlockUnrated bool
}

// Library is a library (CollectionFolder).
type Library struct{ ID, Name string }

// Item is an item. Type is Movie, Episode, Audio, Series, Season, MusicAlbum or Book.
type Item struct {
	ID, Type, Path, Name, SeriesName string
	Runtime                          time.Duration
	Series, Season, Parent           string
	// Libraries the item belongs to.
	Libraries []string
}

// UserData is what a user did with an item. Key is Jellyfin's key (there may be several rows for
// one item).
type UserData struct {
	User, Item, Key string
	Played          bool
	PlayCount       int
	Position        time.Duration
	LastPlayed      time.Time
	Favorite        bool
}

// Event is an activity log entry: VideoPlayback, VideoPlaybackStopped, AudioPlayback,
// AudioPlaybackStopped...
type Event struct {
	Type, User, Item string
	At               time.Time
}

// Placeholder is the dummy item Jellyfin attaches the data of vanished items to.
const Placeholder = "00000000-0000-0000-0000-000000000001"

// NewID returns a random ID in Jellyfin's format.
func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return strings.ToUpper(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]))
}

const schema = `
CREATE TABLE Users (Id TEXT PRIMARY KEY, InternalId INTEGER, Username TEXT NOT NULL, NormalizedUsername TEXT,
	Password TEXT, MaxParentalRatingScore INTEGER, MaxParentalRatingSubScore INTEGER);
CREATE TABLE Permissions (Id INTEGER PRIMARY KEY, Kind INTEGER, RowVersion INTEGER, UserId TEXT, Value INTEGER);
CREATE TABLE Preferences (Id INTEGER PRIMARY KEY, Kind INTEGER, RowVersion INTEGER, UserId TEXT, Value TEXT);
CREATE TABLE BaseItems (Id TEXT PRIMARY KEY, Type TEXT NOT NULL, Path TEXT, Name TEXT, SeriesName TEXT,
	RunTimeTicks INTEGER, SeriesId TEXT, SeasonId TEXT, ParentId TEXT, TopParentId TEXT);
CREATE TABLE AncestorIds (ItemId TEXT, ParentItemId TEXT, PRIMARY KEY (ItemId, ParentItemId));
CREATE TABLE UserData (ItemId TEXT, UserId TEXT, CustomDataKey TEXT, AudioStreamIndex INTEGER, IsFavorite INTEGER,
	LastPlayedDate TEXT, Likes INTEGER, PlayCount INTEGER, PlaybackPositionTicks INTEGER, Played INTEGER, Rating REAL,
	SubtitleStreamIndex INTEGER, RetentionDate TEXT, PRIMARY KEY (ItemId, UserId, CustomDataKey));
CREATE TABLE ActivityLogs (Id INTEGER PRIMARY KEY AUTOINCREMENT, Name TEXT, Overview TEXT, ShortOverview TEXT, Type TEXT,
	UserId TEXT, ItemId TEXT, DateCreated TEXT, LogSeverity INTEGER, RowVersion INTEGER);
`

var types = map[string]string{
	"Movie":      "MediaBrowser.Controller.Entities.Movies.Movie",
	"Episode":    "MediaBrowser.Controller.Entities.TV.Episode",
	"Series":     "MediaBrowser.Controller.Entities.TV.Series",
	"Season":     "MediaBrowser.Controller.Entities.TV.Season",
	"Audio":      "MediaBrowser.Controller.Entities.Audio.Audio",
	"MusicAlbum": "MediaBrowser.Controller.Entities.Audio.MusicAlbum",
	"Book":       "MediaBrowser.Controller.Entities.Book",
}

// Write writes the database to dir/data/jellyfin.db (dir stands for /config).
func Write(dir string, s Server) error {
	path := filepath.Join(dir, "data", "jellyfin.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return err
	}
	exec := func(q string, args ...any) {
		if err == nil {
			_, err = db.ExecContext(ctx, q, args...)
		}
	}
	for i, u := range s.Users {
		var score any
		if u.MaxScore != nil {
			score = *u.MaxScore
		}
		var pw any
		if u.Password != "" {
			pw = u.Password
		}
		exec("INSERT INTO Users (Id, InternalId, Username, NormalizedUsername, Password, MaxParentalRatingScore) VALUES (?, ?, ?, ?, ?, ?)",
			u.ID, i+1, u.Name, strings.ToUpper(u.Name), pw, score)
		for kind, value := range map[int]bool{0: u.Admin, 2: u.Disabled, 11: true, 16: u.AllLibraries} {
			exec("INSERT INTO Permissions (Kind, RowVersion, UserId, Value) VALUES (?, 0, ?, ?)", kind, u.ID, value)
		}
		blocked := ""
		if u.BlockUnrated {
			blocked = "Movie,Series"
		}
		exec("INSERT INTO Preferences (Kind, RowVersion, UserId, Value) VALUES (5, 0, ?, ?)", u.ID, strings.ToLower(strings.Join(u.Libraries, ",")))
		exec("INSERT INTO Preferences (Kind, RowVersion, UserId, Value) VALUES (10, 0, ?, ?)", u.ID, blocked)
	}
	for _, l := range s.Libraries {
		exec("INSERT INTO BaseItems (Id, Type, Path, Name) VALUES (?, 'MediaBrowser.Controller.Entities.CollectionFolder', ?, ?)",
			l.ID, "/config/root/default/"+l.Name, l.Name)
	}
	for _, it := range s.Items {
		t, ok := types[it.Type]
		if !ok {
			return fmt.Errorf("sorte %q inconnue", it.Type)
		}
		exec("INSERT INTO BaseItems (Id, Type, Path, Name, SeriesName, RunTimeTicks, SeriesId, SeasonId, ParentId) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			it.ID, t, nullable(it.Path), it.Name, nullable(it.SeriesName), int64(it.Runtime/100), nullable(it.Series), nullable(it.Season), nullable(it.Parent))
		for _, lib := range it.Libraries {
			exec("INSERT INTO AncestorIds (ItemId, ParentItemId) VALUES (?, ?)", it.ID, lib)
		}
	}
	for _, d := range s.UserData {
		var last any
		if !d.LastPlayed.IsZero() {
			last = efTime(d.LastPlayed)
		}
		exec(`INSERT INTO UserData (ItemId, UserId, CustomDataKey, IsFavorite, LastPlayedDate, PlayCount, PlaybackPositionTicks, Played)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, d.Item, d.User, d.Key, d.Favorite, last, d.PlayCount, int64(d.Position/100), d.Played)
	}
	for _, e := range s.Log {
		exec("INSERT INTO ActivityLogs (Name, Type, UserId, ItemId, DateCreated, LogSeverity, RowVersion) VALUES (?, ?, ?, ?, ?, 2, 0)",
			e.Type, e.Type, e.User, strings.ToLower(strings.ReplaceAll(e.Item, "-", "")), efTime(e.At))
	}
	return err
}

// efTime formats a date the way EF Core does: UTC, seven decimals.
func efTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05.0000000") }

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
