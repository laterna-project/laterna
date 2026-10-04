package app

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/library"
)

// Folder picker: the client cannot see the server's disks (Laterna often runs in a container, on a
// NAS), so it asks the server what a folder holds in order to choose the folders of a library, or
// the folder of a Jellyfin server to import.

// FolderRelation says how a folder relates to a library.
type FolderRelation string

// How a folder relates to a library.
const (
	// FolderRoot: it is one of the library's folders.
	FolderRoot FolderRelation = "root"
	// FolderInside: it is inside one of the library's folders.
	FolderInside FolderRelation = "inside"
	// FolderContains: it contains one of the library's folders.
	FolderContains FolderRelation = "contains"
)

// FolderLibrary is the library a folder touches. Choosing it for another library would be refused,
// since the folders of two libraries cannot overlap.
type FolderLibrary struct {
	ID       domain.ID
	Name     string
	Relation FolderRelation
}

// BrowsedFolder is a folder on offer, with the library it touches.
type BrowsedFolder struct {
	library.Folder
	Library *FolderLibrary
}

// FolderListing is the content of a folder; an empty Path means the starting points.
type FolderListing struct {
	Path, Parent string
	Library      *FolderLibrary
	Folders      []BrowsedFolder
	Media        library.MediaCounts
	Truncated    bool
}

// BrowseFolders returns the subfolders of a folder on the server, or the starting points if path is
// empty. Laterna's own folders (data, cache, metadata, logs) are hidden.
func (a *App) BrowseFolders(ctx context.Context, path string) (FolderListing, error) {
	libs, err := a.rootsOfLibraries(ctx)
	if err != nil {
		return FolderListing{}, err
	}
	var out FolderListing
	var folders []library.Folder
	if path = strings.TrimSpace(path); path == "" {
		folders = library.Starts(ctx)
	} else {
		l, err := library.List(ctx, path, a.ownDirs())
		if err != nil {
			return FolderListing{}, browseError(path, err)
		}
		out = FolderListing{Path: l.Path, Parent: l.Parent, Media: l.Media, Truncated: l.Truncated, Library: libs.of(l.Path)}
		folders = l.Folders
	}
	for _, f := range folders {
		out.Folders = append(out.Folders, BrowsedFolder{Folder: f, Library: libs.of(f.Path)})
	}
	return out, nil
}

func browseError(path string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return domain.NotFound("folder.not_found", "path", path)
	case errors.Is(err, fs.ErrPermission):
		return domain.Forbidden("folder.access_denied", "path", path)
	case !filepath.IsAbs(path):
		return domain.Invalid("folder.not_absolute", "path", path)
	}
	return domain.Invalid("folder.unreadable", "path", path, "reason", err)
}

// ownDirs returns Laterna's folders, which are not offered.
func (a *App) ownDirs() []string {
	var dirs []string
	for _, d := range []string{a.dataDir, a.cacheDir, a.metadataDir, a.logDir} {
		if d != "" {
			dirs = append(dirs, filepath.Clean(d))
		}
	}
	return dirs
}

// libraryRoot is one folder of a library.
type libraryRoot struct {
	path string
	lib  domain.Library
}

type libraryRoots []libraryRoot

func (a *App) rootsOfLibraries(ctx context.Context) (libraryRoots, error) {
	libs, err := a.store.Read().Libraries(ctx)
	if err != nil {
		return nil, err
	}
	var roots libraryRoots
	for _, l := range libs {
		for _, p := range l.Paths {
			roots = append(roots, libraryRoot{path: p, lib: l})
		}
	}
	slices.SortFunc(roots, func(x, y libraryRoot) int { return strings.Compare(x.path, y.path) })
	return roots, nil
}

// of returns the library a folder touches: the one it is a folder of, otherwise the one that
// contains it, otherwise the first one it contains.
func (roots libraryRoots) of(path string) *FolderLibrary {
	var found *FolderLibrary
	rank := map[FolderRelation]int{FolderRoot: 3, FolderInside: 2, FolderContains: 1}
	for _, r := range roots {
		var rel FolderRelation
		switch {
		case r.path == path:
			rel = FolderRoot
		case library.Under(path, []string{r.path}):
			rel = FolderInside
		case library.Under(r.path, []string{path}):
			rel = FolderContains
		default:
			continue
		}
		if found == nil || rank[rel] > rank[found.Relation] {
			found = &FolderLibrary{ID: r.lib.ID, Name: r.lib.Name, Relation: rel}
		}
	}
	return found
}
