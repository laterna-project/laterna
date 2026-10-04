package rpc

import (
	"context"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// LibraryService implements laterna.v1.LibraryService.
type LibraryService struct {
	app *app.App
}

// ListLibraries lists the libraries.
func (s *LibraryService) ListLibraries(ctx context.Context, _ *connect.Request[laternav1.ListLibrariesRequest]) (*connect.Response[laternav1.ListLibrariesResponse], error) {
	list, err := s.app.Libraries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*laternav1.LibrarySummary, len(list))
	for i, l := range list {
		out[i] = &laternav1.LibrarySummary{Library: libraryMsg(l.Library), Counts: itemCountsMsg(l.Counts)}
	}
	return connect.NewResponse(&laternav1.ListLibrariesResponse{Libraries: out}), nil
}

// ReorderLibraries puts the libraries in the given order.
func (s *LibraryService) ReorderLibraries(ctx context.Context, req *connect.Request[laternav1.ReorderLibrariesRequest]) (*connect.Response[laternav1.ReorderLibrariesResponse], error) {
	ids := make([]domain.ID, len(req.Msg.GetLibraryIds()))
	for i, raw := range req.Msg.GetLibraryIds() {
		id, err := parseID(raw, "library_ids")
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}
	list, err := s.app.ReorderLibraries(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]*laternav1.LibrarySummary, len(list))
	for i, l := range list {
		out[i] = &laternav1.LibrarySummary{Library: libraryMsg(l.Library), Counts: itemCountsMsg(l.Counts)}
	}
	return connect.NewResponse(&laternav1.ReorderLibrariesResponse{Libraries: out}), nil
}

// CreateLibrary creates a library.
func (s *LibraryService) CreateLibrary(ctx context.Context, req *connect.Request[laternav1.CreateLibraryRequest]) (*connect.Response[laternav1.CreateLibraryResponse], error) {
	m := req.Msg
	lib, err := s.app.CreateLibrary(ctx, m.GetName(), libraryKindFromMsg(m.GetKind()), m.GetPaths(), m.GetLanguage())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.CreateLibraryResponse{Library: libraryMsg(lib)}), nil
}

// UpdateLibrary changes a library.
func (s *LibraryService) UpdateLibrary(ctx context.Context, req *connect.Request[laternav1.UpdateLibraryRequest]) (*connect.Response[laternav1.UpdateLibraryResponse], error) {
	m := req.Msg
	id, err := parseID(m.GetLibraryId(), "library_id")
	if err != nil {
		return nil, err
	}
	ch := app.LibraryChanges{Name: m.Name, Language: m.Language}
	if len(m.GetPaths()) > 0 {
		ch.Paths = m.GetPaths()
	}
	lib, err := s.app.UpdateLibrary(ctx, id, ch)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.UpdateLibraryResponse{Library: libraryMsg(lib)}), nil
}

// DeleteLibrary deletes a library.
func (s *LibraryService) DeleteLibrary(ctx context.Context, req *connect.Request[laternav1.DeleteLibraryRequest]) (*connect.Response[laternav1.DeleteLibraryResponse], error) {
	id, err := parseID(req.Msg.GetLibraryId(), "library_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.DeleteLibrary(ctx, id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.DeleteLibraryResponse{}), nil
}

// ScanLibrary asks for a scan.
func (s *LibraryService) ScanLibrary(ctx context.Context, req *connect.Request[laternav1.ScanLibraryRequest]) (*connect.Response[laternav1.ScanLibraryResponse], error) {
	id, err := parseID(req.Msg.GetLibraryId(), "library_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.ScanLibrary(ctx, id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.ScanLibraryResponse{}), nil
}

var folderRelations = map[app.FolderRelation]laternav1.FolderRelation{
	app.FolderRoot:     laternav1.FolderRelation_FOLDER_RELATION_ROOT,
	app.FolderInside:   laternav1.FolderRelation_FOLDER_RELATION_INSIDE,
	app.FolderContains: laternav1.FolderRelation_FOLDER_RELATION_CONTAINS,
}

// BrowseFolders browses the server's folders.
func (s *LibraryService) BrowseFolders(ctx context.Context, req *connect.Request[laternav1.BrowseFoldersRequest]) (*connect.Response[laternav1.BrowseFoldersResponse], error) {
	l, err := s.app.BrowseFolders(ctx, req.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	resp := &laternav1.BrowseFoldersResponse{
		Path: l.Path, Parent: l.Parent, Library: folderLibraryMsg(l.Library), Truncated: l.Truncated,
		Media: &laternav1.MediaCounts{
			Videos: int32(l.Media.Videos), Audio: int32(l.Media.Audio), Books: int32(l.Media.Books), Photos: int32(l.Media.Photos), //nolint:gosec // counts for one folder
		},
	}
	for _, f := range l.Folders {
		resp.Folders = append(resp.Folders, &laternav1.Folder{
			Name: f.Name, Path: f.Path, Readable: f.Readable, HasSubfolders: f.HasSubfolders, Library: folderLibraryMsg(f.Library),
			Media: &laternav1.MediaCounts{
				Videos: int32(f.Media.Videos), Audio: int32(f.Media.Audio), Books: int32(f.Media.Books), Photos: int32(f.Media.Photos), //nolint:gosec // 200 at most
			},
		})
	}
	return connect.NewResponse(resp), nil
}

func folderLibraryMsg(l *app.FolderLibrary) *laternav1.FolderLibrary {
	if l == nil {
		return nil
	}
	return &laternav1.FolderLibrary{LibraryId: l.ID.String(), LibraryName: l.Name, Relation: folderRelations[l.Relation]}
}
