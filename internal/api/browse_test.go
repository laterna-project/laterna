package api

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// Folder picker: subfolders of a folder on the server, how each relates to the libraries, Laterna's
// own folders hidden, errors.
func TestBrowseFolders(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	for _, d := range []string{"Media/Movies/Movie (2020)", "Media/Shows", "Media/Laterna/cache", "Other"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "Media", "Movies", "Movie (2020)", "Movie (2020).mkv"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a, err := app.New(ctx, st, app.Options{ServerName: "Test", CacheDir: filepath.Join(root, "Media", "Laterna", "cache")})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(a, Options{}))
	t.Cleanup(srv.Close)
	admin, err := a.Setup(ctx, "admin", "a-strong-password", domain.Device{Name: "Test", Client: "Test", ClientVersion: "1", Platform: "Go"}, "")
	if err != nil {
		t.Fatal(err)
	}
	films, err := a.CreateLibrary(ctx, "Movies", domain.LibraryMovies, []string{filepath.Join(root, "Media", "Movies")}, "")
	if err != nil {
		t.Fatal(err)
	}
	libraries := laternav1connect.NewLibraryServiceClient(srv.Client(), srv.URL)
	browse := func(path string) (*laternav1.BrowseFoldersResponse, error) {
		resp, err := libraries.BrowseFolders(ctx, authed(&laternav1.BrowseFoldersRequest{Path: path}, admin.Token))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}

	starts, err := browse("")
	if err != nil || len(starts.GetFolders()) == 0 || starts.GetPath() != "" {
		t.Fatalf("starting points: %v %v", starts, err)
	}

	media, err := browse(filepath.Join(root, "Media"))
	if err != nil {
		t.Fatal(err)
	}
	if media.GetParent() != root || len(media.GetFolders()) != 3 {
		t.Fatalf("Media: %v", media)
	}
	if own, err := browse(filepath.Join(root, "Media", "Laterna")); err != nil || len(own.GetFolders()) != 0 {
		t.Errorf("Laterna's cache offered: %v %v", own, err)
	}
	byName := map[string]*laternav1.Folder{}
	for _, f := range media.GetFolders() {
		byName[f.GetName()] = f
	}
	if f := byName["Movies"]; f.GetLibrary().GetRelation() != laternav1.FolderRelation_FOLDER_RELATION_ROOT ||
		f.GetLibrary().GetLibraryId() != films.ID.String() || !f.GetHasSubfolders() || !f.GetReadable() {
		t.Errorf("Movies: %v", f)
	}
	if f := byName["Shows"]; f.GetLibrary() != nil {
		t.Errorf("Shows: %v", f)
	}
	if media.GetLibrary().GetRelation() != laternav1.FolderRelation_FOLDER_RELATION_CONTAINS {
		t.Errorf("Media contains Movies: %v", media.GetLibrary())
	}
	inside, err := browse(filepath.Join(root, "Media", "Movies", "Movie (2020)"))
	if err != nil || inside.GetLibrary().GetRelation() != laternav1.FolderRelation_FOLDER_RELATION_INSIDE || inside.GetMedia().GetVideos() != 1 {
		t.Errorf("inside Movies: %v %v", inside, err)
	}

	for path, code := range map[string]connect.Code{
		"relative":                    connect.CodeInvalidArgument,
		filepath.Join(root, "absent"): connect.CodeNotFound,
	} {
		if _, err := browse(path); connect.CodeOf(err) != code {
			t.Errorf("%s: %v", path, err)
		}
	}
}
