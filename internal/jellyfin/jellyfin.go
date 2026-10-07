// Package jellyfin reads the data of a Jellyfin server so it can be imported into Laterna: users,
// what they watched, their resume points and favorites, and their playback sessions rebuilt from
// the activity log. Only the single database of Jellyfin 10.11 and later (jellyfin.db, written by
// EF Core) is read, and only a copy of it: the Jellyfin server is never touched. The package knows
// nothing about Laterna's database or catalog.
package jellyfin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go SQLite driver (no cgo)

	"github.com/laterna-project/laterna/internal/domain"
)

// ID is a Jellyfin ID in a single form: 32 lower-case hex digits. Jellyfin writes it sometimes in
// upper case with dashes, sometimes without.
type ID string

// normID puts a Jellyfin ID in its canonical form.
func normID(s string) ID {
	return ID(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "-", "")))
}

// placeholder is the dummy item Jellyfin attaches the data of vanished items to, until they come
// back.
const placeholder = ID("00000000000000000000000000000001")

// Kind is the kind of an imported item.
type Kind string

// Item kinds we import. The others (people, channels, books...) are ignored.
const (
	Movie   Kind = "movie"
	Episode Kind = "episode"
	Track   Kind = "track"
	Series  Kind = "series"
	Season  Kind = "season"
	Album   Kind = "album"
)

var kinds = map[string]Kind{
	"MediaBrowser.Controller.Entities.Movies.Movie":     Movie,
	"MediaBrowser.Controller.Entities.TV.Episode":       Episode,
	"MediaBrowser.Controller.Entities.Audio.Audio":      Track,
	"MediaBrowser.Controller.Entities.TV.Series":        Series,
	"MediaBrowser.Controller.Entities.TV.Season":        Season,
	"MediaBrowser.Controller.Entities.Audio.MusicAlbum": Album,
}

// Leaf reports a kind of item that has a file (movie, episode, track).
func (k Kind) Leaf() bool { return k == Movie || k == Episode || k == Track }

// User is a Jellyfin user.
type User struct {
	ID            ID
	Name          string
	Admin         bool
	Disabled      bool
	DenyDownloads bool
	// AllLibraries means every library; otherwise see Libraries (Jellyfin IDs).
	AllLibraries bool
	Libraries    []ID
	// MaxScore is the highest rating allowed (an age since Jellyfin 10.11); nil means no limit.
	MaxScore     *int
	BlockUnrated bool
	// PasswordHash is Jellyfin's PBKDF2 hash; "" means no password.
	PasswordHash string
}

// Item is a Jellyfin item.
type Item struct {
	ID   ID
	Kind Kind
	// Path of the file as Jellyfin sees it (often inside a container: /media/...).
	Path       string
	Name       string
	SeriesName string
	Runtime    time.Duration
	// Series and Season of an episode. Parent is the album of a track.
	Series, Season, Parent ID
}

// UserData is what a user did with an item.
type UserData struct {
	User, Item ID
	Played     bool
	PlayCount  int
	Position   time.Duration
	LastPlayed *time.Time
	Favorite   bool
}

// Session is one playback from the activity log, from start to stop, in wall-clock time. Pauses are
// included: the log does not say where playback stopped.
type Session struct {
	// Key is the ID of the playback start in the log, unique on that server.
	Key        int64
	User, Item ID
	Start, End time.Time
}

// Export is what Laterna takes from a Jellyfin server.
type Export struct {
	Users []User
	Items map[ID]Item
	// Members lists the items of each Jellyfin library (movies, episodes, tracks).
	Members  map[ID][]ID
	UserData []UserData
	// Orphaned counts data of items that are gone from Jellyfin, Unsupported data of items of a
	// kind we do not import (books, channels...).
	Orphaned, Unsupported int
	Sessions              []Session
	// Unpaired counts playback starts without a stop in the log.
	Unpaired int
}

// Read reads the data of a Jellyfin server. path is its data folder (the one that holds
// data/jellyfin.db, /config in the official image), the data folder itself, or the jellyfin.db
// file. The database and its WAL are first copied to a temporary folder under tmp, so reading takes
// no lock on Jellyfin's side. A copy taken in the middle of a write can be inconsistent, in which
// case it is rejected.
func Read(ctx context.Context, path, tmp string) (*Export, error) {
	db, err := locate(path)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(tmp, "jellyfin-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	copyPath := filepath.Join(dir, "jellyfin.db")
	if err := copyFile(db, copyPath); err != nil {
		return nil, fmt.Errorf("copying the Jellyfin database: %w", err)
	}
	if err := copyFile(db+"-wal", copyPath+"-wal"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("copying the Jellyfin database journal: %w", err)
	}
	conn, err := sql.Open("sqlite", copyPath+"?_pragma=busy_timeout(1000)")
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	conn.SetMaxOpenConns(1)
	var check string
	if err := conn.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		return nil, domain.Invalid("import.jellyfin_copy_inconsistent")
	}
	if err := checkSchema(ctx, conn); err != nil {
		return nil, err
	}
	r := reader{db: conn}
	e := &Export{Items: map[ID]Item{}, Members: map[ID][]ID{}}
	for _, step := range []func(context.Context, *Export) error{r.users, r.items, r.members, r.userData, r.sessions} {
		if err := step(ctx, e); err != nil {
			return nil, fmt.Errorf("reading the Jellyfin database: %w", err)
		}
	}
	return e, nil
}

// locate finds jellyfin.db from the given path.
func locate(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", domain.Invalid("folder.not_absolute", "path", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", domain.Invalid("folder.not_found", "path", path)
	}
	if !info.IsDir() {
		return path, nil
	}
	for _, candidate := range []string{filepath.Join(path, "data", "jellyfin.db"), filepath.Join(path, "jellyfin.db")} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	if _, err := os.Stat(filepath.Join(path, "data", "library.db")); err == nil {
		return "", domain.Invalid("import.jellyfin_too_old")
	}
	return "", domain.Invalid("import.jellyfin_db_not_found")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// checkSchema checks that the database is from Jellyfin 10.11 or later.
func checkSchema(ctx context.Context, db *sql.DB) error {
	need := map[string][]string{
		"Users":        {"Id", "Username", "Password", "MaxParentalRatingScore"},
		"Permissions":  {"UserId", "Kind", "Value"},
		"Preferences":  {"UserId", "Kind", "Value"},
		"BaseItems":    {"Id", "Type", "Path", "Name", "SeriesName", "RunTimeTicks", "SeriesId", "SeasonId", "ParentId"},
		"AncestorIds":  {"ItemId", "ParentItemId"},
		"UserData":     {"ItemId", "UserId", "Played", "PlayCount", "PlaybackPositionTicks", "LastPlayedDate", "IsFavorite"},
		"ActivityLogs": {"Id", "Type", "UserId", "ItemId", "DateCreated"},
	}
	r := reader{db: db}
	for table, cols := range need {
		var have []string
		err := r.each(ctx, "SELECT name FROM pragma_table_info(?)", func(scan func(...any) error) error {
			var name string
			err := scan(&name)
			have = append(have, name)
			return err
		}, table)
		if err != nil {
			return err
		}
		for _, c := range cols {
			if !slices.Contains(have, c) {
				return domain.Invalid("import.jellyfin_unrecognized", "table", table, "column", c)
			}
		}
	}
	return nil
}

type reader struct{ db *sql.DB }

// each runs a query and calls fn for each row. Rows are read to the end.
func (r reader) each(ctx context.Context, query string, fn func(scan func(...any) error) error, args ...any) error {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := fn(rows.Scan); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Indexes of Jellyfin's permissions and preferences (Jellyfin.Data.Enums).
const (
	permAdministrator = 0
	permDisabled      = 2
	permDownloads     = 11
	permAllFolders    = 16
	prefFolders       = 5
	prefBlockUnrated  = 10
)

func (r reader) users(ctx context.Context, e *Export) error {
	byID := map[ID]*User{}
	err := r.each(ctx, "SELECT Id, Username, COALESCE(Password, ''), MaxParentalRatingScore FROM Users ORDER BY InternalId",
		func(scan func(...any) error) error {
			var id, name, hash string
			var score sql.NullInt64
			if err := scan(&id, &name, &hash, &score); err != nil {
				return err
			}
			u := User{ID: normID(id), Name: strings.ToValidUTF8(name, ""), PasswordHash: hash}
			if score.Valid {
				v := int(score.Int64)
				u.MaxScore = &v
			}
			e.Users = append(e.Users, u)
			return nil
		})
	if err != nil {
		return err
	}
	for i := range e.Users {
		byID[e.Users[i].ID] = &e.Users[i]
	}
	// Jellyfin 10.11 keeps rows that belong to no user: skipped.
	err = r.each(ctx, "SELECT UserId, Kind, Value FROM Permissions WHERE UserId IS NOT NULL", func(scan func(...any) error) error {
		var user string
		var kind, value int64
		if err := scan(&user, &kind, &value); err != nil {
			return err
		}
		u := byID[normID(user)]
		if u == nil {
			return nil
		}
		switch kind {
		case permAdministrator:
			u.Admin = value == 1
		case permDisabled:
			u.Disabled = value == 1
		case permDownloads:
			u.DenyDownloads = value == 0
		case permAllFolders:
			u.AllLibraries = value == 1
		}
		return nil
	})
	if err != nil {
		return err
	}
	return r.each(ctx, "SELECT UserId, Kind, COALESCE(Value, '') FROM Preferences WHERE UserId IS NOT NULL AND Kind IN (?, ?)", func(scan func(...any) error) error {
		var user, value string
		var kind int64
		if err := scan(&user, &kind, &value); err != nil {
			return err
		}
		u := byID[normID(user)]
		if u == nil {
			return nil
		}
		switch kind {
		case prefFolders:
			for f := range strings.SplitSeq(value, ",") {
				if f = strings.TrimSpace(f); f != "" {
					u.Libraries = append(u.Libraries, normID(f))
				}
			}
		case prefBlockUnrated:
			u.BlockUnrated = strings.TrimSpace(value) != ""
		}
		return nil
	}, prefFolders, prefBlockUnrated)
}

func (r reader) items(ctx context.Context, e *Export) error {
	types := make([]any, 0, len(kinds))
	for t := range kinds {
		types = append(types, t)
	}
	q := "SELECT Id, Type, COALESCE(Path, ''), COALESCE(Name, ''), COALESCE(SeriesName, ''), COALESCE(RunTimeTicks, 0), " +
		"COALESCE(SeriesId, ''), COALESCE(SeasonId, ''), COALESCE(ParentId, '') FROM BaseItems WHERE Type IN (?" +
		strings.Repeat(", ?", len(types)-1) + ")"
	return r.each(ctx, q, func(scan func(...any) error) error {
		var id, typ, path, name, seriesName, series, season, parent string
		var ticks int64
		if err := scan(&id, &typ, &path, &name, &seriesName, &ticks, &series, &season, &parent); err != nil {
			return err
		}
		it := Item{
			ID: normID(id), Kind: kinds[typ], Path: strings.ToValidUTF8(path, ""), Name: strings.ToValidUTF8(name, ""),
			SeriesName: strings.ToValidUTF8(seriesName, ""), Runtime: ticksToDuration(ticks),
			Series: normID(series), Season: normID(season), Parent: normID(parent),
		}
		e.Items[it.ID] = it
		return nil
	}, types...)
}

// members reads the content of each library: its items have the library as an ancestor.
func (r reader) members(ctx context.Context, e *Export) error {
	return r.each(ctx, `SELECT a.ParentItemId, a.ItemId FROM AncestorIds a
		JOIN BaseItems f ON f.Id = a.ParentItemId AND f.Type = 'MediaBrowser.Controller.Entities.CollectionFolder'`,
		func(scan func(...any) error) error {
			var lib, item string
			if err := scan(&lib, &item); err != nil {
				return err
			}
			if it, ok := e.Items[normID(item)]; ok && it.Kind.Leaf() {
				e.Members[normID(lib)] = append(e.Members[normID(lib)], it.ID)
			}
			return nil
		})
}

// userData reads the users' data. Jellyfin sometimes keeps several rows for one item (one per key:
// TVDB ID, IMDb ID...). They are merged into one.
func (r reader) userData(ctx context.Context, e *Export) error {
	type key struct{ user, item ID }
	index := map[key]int{}
	err := r.each(ctx, `SELECT UserId, ItemId, Played, PlayCount, PlaybackPositionTicks, LastPlayedDate, IsFavorite FROM UserData`,
		func(scan func(...any) error) error {
			var user, item string
			var played, count, ticks, fav int64
			var last sql.NullString
			if err := scan(&user, &item, &played, &count, &ticks, &last, &fav); err != nil {
				return err
			}
			d := UserData{
				User: normID(user), Item: normID(item), Played: played == 1, PlayCount: int(count),
				Position: ticksToDuration(ticks), LastPlayed: parseTime(last.String), Favorite: fav == 1,
			}
			if d.Item == placeholder {
				e.Orphaned++
				return nil
			}
			if _, ok := e.Items[d.Item]; !ok {
				e.Unsupported++
				return nil
			}
			k := key{d.User, d.Item}
			if i, ok := index[k]; ok {
				e.UserData[i] = Merge(e.UserData[i], d)
				return nil
			}
			index[k] = len(e.UserData)
			e.UserData = append(e.UserData, d)
			return nil
		})
	return err
}

// Merge merges two records for the same item: played or favorite if either says so, the higher play
// count, the position of the most recent one.
func Merge(a, b UserData) UserData {
	out := a
	out.Played = a.Played || b.Played
	out.Favorite = a.Favorite || b.Favorite
	out.PlayCount = max(a.PlayCount, b.PlayCount)
	if b.LastPlayed != nil && (a.LastPlayed == nil || b.LastPlayed.After(*a.LastPlayed)) {
		out.LastPlayed, out.Position = b.LastPlayed, b.Position
	}
	return out
}

// Activity log entry types for playback.
var playbackStart = map[string]bool{"VideoPlayback": true, "AudioPlayback": true}

var playbackStop = map[string]bool{"VideoPlaybackStopped": true, "AudioPlaybackStopped": true}

// restartWithin: a new playback start of the same item sooner than this continues the open session.
const restartWithin = time.Hour

// sessions rebuilds the sessions: each playback start with the next stop by the same user on the
// same item, within 24 hours. A new start of the same item less than an hour later, with no stop in
// between, continues the session: Jellyfin logs one every time the stream restarts (seek, track
// change), typically some 17 s after the first.
func (r reader) sessions(ctx context.Context, e *Export) error {
	type key struct{ user, item ID }
	open := map[key]Session{}
	err := r.each(ctx, `SELECT Id, Type, COALESCE(UserId, ''), COALESCE(ItemId, ''), DateCreated FROM ActivityLogs
		WHERE Type IN ('VideoPlayback', 'VideoPlaybackStopped', 'AudioPlayback', 'AudioPlaybackStopped')
		ORDER BY DateCreated, Id`,
		func(scan func(...any) error) error {
			var id int64
			var typ, user, item, date string
			if err := scan(&id, &typ, &user, &item, &date); err != nil {
				return err
			}
			at := parseTime(date)
			if at == nil || user == "" || item == "" {
				return nil
			}
			k := key{normID(user), normID(item)}
			switch {
			case playbackStart[typ]:
				if prev, ok := open[k]; ok {
					if at.Sub(prev.Start) < restartWithin {
						return nil
					}
					e.Unpaired++
				}
				open[k] = Session{Key: id, User: k.user, Item: k.item, Start: *at}
			case playbackStop[typ]:
				s, ok := open[k]
				if !ok {
					return nil
				}
				delete(open, k)
				if s.End = *at; s.End.Sub(s.Start) > 24*time.Hour {
					e.Unpaired++
					return nil
				}
				e.Sessions = append(e.Sessions, s)
			}
			return nil
		})
	e.Unpaired += len(open)
	return err
}

// ticksToDuration converts .NET ticks (100 ns).
func ticksToDuration(ticks int64) time.Duration {
	if ticks <= 0 {
		return 0
	}
	return time.Duration(ticks) * 100
}

// parseTime reads an EF Core date (UTC, up to 7 decimals); nil if it is missing or unreadable.
func parseTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05.9999999", "2006-01-02T15:04:05.9999999Z07:00", "2006-01-02T15:04:05.9999999"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}
