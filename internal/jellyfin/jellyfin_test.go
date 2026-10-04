package jellyfin_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/jellyfin"
	"github.com/laterna-project/laterna/internal/jellyfin/jellyfintest"
)

func norm(id string) jellyfin.ID {
	return jellyfin.ID(strings.ToLower(strings.ReplaceAll(id, "-", "")))
}

func TestRead(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	admin, kid, films := jellyfintest.NewID(), jellyfintest.NewID(), jellyfintest.NewID()
	movie, episode, series, book := jellyfintest.NewID(), jellyfintest.NewID(), jellyfintest.NewID(), jellyfintest.NewID()
	twelve := 12
	t0 := time.Date(2026, 5, 1, 20, 0, 0, 0, time.UTC)
	err := jellyfintest.Write(dir, jellyfintest.Server{
		Users: []jellyfintest.User{
			{ID: admin, Name: "root", Password: "$PBKDF2-SHA512$iterations=1000$00$00", Admin: true, AllLibraries: true},
			{ID: kid, Name: "Sam", Libraries: []string{films}, MaxScore: &twelve, BlockUnrated: true},
		},
		Libraries: []jellyfintest.Library{{ID: films, Name: "Films"}},
		Items: []jellyfintest.Item{
			{ID: movie, Type: "Movie", Path: "/media/movies/Film (2020)/Film (2020).mkv", Name: "Film", Runtime: 100 * time.Minute, Libraries: []string{films}},
			{ID: series, Type: "Series", Path: "/media/tv/Série", Name: "Série"},
			{ID: episode, Type: "Episode", Path: "/media/tv/Série/S01E01.mkv", Name: "Pilote", SeriesName: "Série", Series: series, Runtime: 24 * time.Minute},
			{ID: book, Type: "Book", Path: "/media/books/livre.epub", Name: "Livre"},
		},
		UserData: []jellyfintest.UserData{
			// Two keys for the same item: merged.
			{User: admin, Item: movie, Key: "tt123", Played: true, PlayCount: 2, LastPlayed: t0},
			{User: admin, Item: movie, Key: movie, Position: 30 * time.Minute, PlayCount: 1, LastPlayed: t0.Add(time.Hour), Favorite: true},
			{User: kid, Item: episode, Key: "e1", Position: 10 * time.Minute, LastPlayed: t0},
			{User: kid, Item: jellyfintest.Placeholder, Key: "375892006001", Played: true},
			{User: kid, Item: book, Key: "b1", Played: true},
		},
		Log: []jellyfintest.Event{
			{Type: "VideoPlayback", User: admin, Item: movie, At: t0},
			{Type: "VideoPlayback", User: admin, Item: movie, At: t0.Add(17 * time.Second)}, // stream restart: same session
			{Type: "VideoPlaybackStopped", User: admin, Item: movie, At: t0.Add(40 * time.Minute)},
			{Type: "VideoPlayback", User: kid, Item: episode, At: t0.Add(time.Hour)},          // never stopped
			{Type: "VideoPlaybackStopped", User: kid, Item: movie, At: t0.Add(2 * time.Hour)}, // stop without a start
			{Type: "VideoPlayback", User: kid, Item: movie, At: t0.Add(3 * time.Hour)},        // more than 24 h
			{Type: "VideoPlaybackStopped", User: kid, Item: movie, At: t0.Add(30 * time.Hour)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	e, err := jellyfin.Read(ctx, dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Users) != 2 {
		t.Fatalf("users: %+v", e.Users)
	}
	root, leo := e.Users[0], e.Users[1]
	if root.ID != norm(admin) || !root.Admin || !root.AllLibraries || root.PasswordHash == "" || root.DenyDownloads {
		t.Errorf("root: %+v", root)
	}
	if leo.Admin || leo.AllLibraries || len(leo.Libraries) != 1 || leo.Libraries[0] != norm(films) ||
		leo.MaxScore == nil || *leo.MaxScore != 12 || !leo.BlockUnrated || leo.PasswordHash != "" {
		t.Errorf("Sam: %+v", leo)
	}
	if it := e.Items[norm(episode)]; it.Kind != jellyfin.Episode || it.Series != norm(series) || it.Runtime != 24*time.Minute || it.SeriesName != "Série" {
		t.Errorf("episode: %+v", it)
	}
	if _, ok := e.Items[norm(book)]; ok {
		t.Error("book imported")
	}
	if m := e.Members[norm(films)]; len(m) != 1 || m[0] != norm(movie) {
		t.Errorf("members: %v", e.Members)
	}
	if len(e.UserData) != 2 || e.Orphaned != 1 || e.Unsupported != 1 {
		t.Fatalf("data: %+v, orphaned %d, unsupported %d", e.UserData, e.Orphaned, e.Unsupported)
	}
	d := e.UserData[0]
	if !d.Played || d.PlayCount != 2 || !d.Favorite || d.Position != 30*time.Minute || d.LastPlayed == nil || !d.LastPlayed.Equal(t0.Add(time.Hour)) {
		t.Errorf("merged data: %+v", d)
	}
	if len(e.Sessions) != 1 || e.Unpaired != 2 {
		t.Fatalf("sessions: %+v, unpaired %d", e.Sessions, e.Unpaired)
	}
	if s := e.Sessions[0]; s.User != norm(admin) || !s.Start.Equal(t0) || !s.End.Equal(t0.Add(40*time.Minute)) {
		t.Errorf("session: %+v", s)
	}
}

func TestReadRefused(t *testing.T) {
	ctx := context.Background()
	old := t.TempDir()
	if err := os.MkdirAll(filepath.Join(old, "data"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "data", "library.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	notDB := filepath.Join(t.TempDir(), "jellyfin.db")
	if err := os.WriteFile(notDB, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ path, want string }{
		"relatif":        {"config", "folder.not_absolute"},
		"absent":         {filepath.Join(t.TempDir(), "rien"), "folder.not_found"},
		"vide":           {t.TempDir(), "import.jellyfin_db_not_found"},
		"before 10.11":   {old, "import.jellyfin_too_old"},
		"not a database": {notDB, "import.jellyfin_copy_inconsistent"},
	} {
		if _, err := jellyfin.Read(ctx, c.path, t.TempDir()); domain.CodeOf(err) != c.want {
			t.Errorf("%s: %v", name, err)
		}
	}
}
