package rpc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
)

// Library management through the API. Background work is not started: the requested scan stays in
// the queue, which is enough here (the scan itself is tested in app).
func TestLibraryServiceOverHTTP(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	films, series := filepath.Join(t.TempDir(), "Films"), filepath.Join(t.TempDir(), "Séries")
	for _, d := range []string{films, series} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.library.ListLibraries(ctx, withToken(&laternav1.ListLibrariesRequest{}, "")); code(err) != connect.CodeUnauthenticated {
		t.Errorf("without a token: %v", err)
	}
	token := setup(t, s)

	created, err := s.library.CreateLibrary(ctx, withToken(&laternav1.CreateLibraryRequest{
		Name: "Films", Kind: laternav1.LibraryKind_LIBRARY_KIND_MOVIES, Paths: []string{films},
	}, token))
	if err != nil {
		t.Fatal(err)
	}
	lib := created.Msg.GetLibrary()
	if lib.GetKind() != laternav1.LibraryKind_LIBRARY_KIND_MOVIES || lib.GetLanguage() != "en-US" ||
		len(lib.GetPaths()) != 1 || lib.LastScanAt != nil {
		t.Errorf("library created: %v", lib)
	}

	invalid := []*laternav1.CreateLibraryRequest{
		{Name: "No kind", Paths: []string{series}},
		{Name: "Relatif", Kind: laternav1.LibraryKind_LIBRARY_KIND_SHOWS, Paths: []string{"Séries"}},
		{Name: "No folder", Kind: laternav1.LibraryKind_LIBRARY_KIND_SHOWS},
	}
	for _, m := range invalid {
		if _, err := s.library.CreateLibrary(ctx, withToken(m, token)); code(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", m.GetName(), err)
		}
	}
	_, err = s.library.CreateLibrary(ctx, withToken(&laternav1.CreateLibraryRequest{
		Name: "Doublon", Kind: laternav1.LibraryKind_LIBRARY_KIND_MOVIES, Paths: []string{filepath.Join(films, "..", "Films")},
	}, token))
	if code(err) != connect.CodeAlreadyExists {
		t.Errorf("folder already taken: %v", err)
	}

	// Empty Paths: folders unchanged.
	name := "Mes films"
	updated, err := s.library.UpdateLibrary(ctx, withToken(&laternav1.UpdateLibraryRequest{LibraryId: lib.GetId(), Name: &name}, token))
	if err != nil {
		t.Fatal(err)
	}
	if u := updated.Msg.GetLibrary(); u.GetName() != name || len(u.GetPaths()) != 1 || u.GetPaths()[0] != lib.GetPaths()[0] {
		t.Errorf("after the update: %v", u)
	}

	list, err := s.library.ListLibraries(ctx, withToken(&laternav1.ListLibrariesRequest{}, token))
	if err != nil || len(list.Msg.GetLibraries()) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
	if sum := list.Msg.GetLibraries()[0]; sum.GetLibrary().GetName() != name || sum.GetCounts().GetMovies() != 0 {
		t.Errorf("summary: %v", sum)
	}

	// Library order: by kind as long as nobody chose one, then the one the administrator gives.
	second, err := s.library.CreateLibrary(ctx, withToken(&laternav1.CreateLibraryRequest{
		Name: "Animes", Kind: laternav1.LibraryKind_LIBRARY_KIND_SHOWS, Paths: []string{series},
	}, token))
	if err != nil {
		t.Fatal(err)
	}
	shows := second.Msg.GetLibrary().GetId()
	if list, err := s.library.ListLibraries(ctx, withToken(&laternav1.ListLibrariesRequest{}, token)); err != nil ||
		len(list.Msg.GetLibraries()) != 2 || list.Msg.GetLibraries()[0].GetLibrary().GetId() != lib.GetId() {
		t.Errorf("default order: %v %v", list, err)
	}
	if _, err := s.library.ReorderLibraries(ctx, withToken(&laternav1.ReorderLibrariesRequest{LibraryIds: []string{shows}}, token)); code(err) != connect.CodeInvalidArgument || errorCode(err) != "library.invalid_order" {
		t.Errorf("incomplete order: %v", err)
	}
	reordered, err := s.library.ReorderLibraries(ctx, withToken(&laternav1.ReorderLibrariesRequest{LibraryIds: []string{shows, lib.GetId()}}, token))
	if err != nil || len(reordered.Msg.GetLibraries()) != 2 || reordered.Msg.GetLibraries()[0].GetLibrary().GetId() != shows {
		t.Fatalf("chosen order: %v %v", reordered, err)
	}
	if _, err := s.library.DeleteLibrary(ctx, withToken(&laternav1.DeleteLibraryRequest{LibraryId: shows}, token)); err != nil {
		t.Fatal(err)
	}

	if _, err := s.library.ScanLibrary(ctx, withToken(&laternav1.ScanLibraryRequest{LibraryId: lib.GetId()}, token)); err != nil {
		t.Errorf("scan: %v", err)
	}
	if _, err := s.library.DeleteLibrary(ctx, withToken(&laternav1.DeleteLibraryRequest{LibraryId: lib.GetId()}, token)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.library.ScanLibrary(ctx, withToken(&laternav1.ScanLibraryRequest{LibraryId: lib.GetId()}, token)); code(err) != connect.CodeNotFound {
		t.Errorf("scan of a deleted library: %v", err)
	}
	if _, err := os.Stat(films); err != nil {
		t.Errorf("the folder must never be touched: %v", err)
	}
}
