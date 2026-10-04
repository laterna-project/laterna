package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/library"
	"github.com/laterna-project/laterna/internal/store"
)

const (
	maxLibraryNameLen = 60
	defaultLanguage   = "en-US"
)

var languageTag = regexp.MustCompile(`^[a-z]{2,3}(-[A-Z]{2})?$`)

// LibraryInfo is a library and its item counts.
type LibraryInfo struct {
	Library domain.Library
	Counts  map[domain.ItemKind]int
}

// LibraryChanges describes a change to a library; a nil field is left alone.
type LibraryChanges struct {
	Name     *string
	Paths    []string
	Language *string
}

// Libraries lists the libraries with their item counts.
func (a *App) Libraries(ctx context.Context) ([]LibraryInfo, error) {
	read := a.store.Read()
	libs, err := read.Libraries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]LibraryInfo, len(libs))
	for i, l := range libs {
		counts, err := read.LibraryItemCounts(ctx, l.ID)
		if err != nil {
			return nil, err
		}
		out[i] = LibraryInfo{Library: l, Counts: counts}
	}
	return out, nil
}

// CreateLibrary creates a library and starts its first scan. Administrators only (checked by the
// API layer from the contract).
func (a *App) CreateLibrary(ctx context.Context, name string, kind domain.LibraryKind, paths []string, language string) (domain.Library, error) {
	name = strings.TrimSpace(name)
	if err := validateLibraryName(name); err != nil {
		return domain.Library{}, err
	}
	if !kind.Valid() {
		return domain.Library{}, domain.Invalid("library.unknown_kind")
	}
	if language == "" {
		language = defaultLanguage
	}
	if !languageTag.MatchString(language) {
		return domain.Library{}, domain.Invalid("library.invalid_language", "language", language)
	}
	now := a.now()
	lib := domain.Library{ID: domain.NewID(), Name: name, Kind: kind, Language: language, CreatedAt: now, UpdatedAt: now}
	err := a.store.Write(ctx, func(q store.Q) error {
		var err error
		if lib.Paths, err = a.checkPaths(ctx, q, paths, lib.ID); err != nil {
			return err
		}
		if err := libraryWriteError(q.CreateLibrary(ctx, lib), name); err != nil {
			return err
		}
		return a.jobs.Enqueue(ctx, q, jobScanLibrary, lib.ID.String(), priorityUser)
	})
	if err != nil {
		return domain.Library{}, err
	}
	a.jobs.Kick()
	a.bus.Publish(domain.LibrariesChanged{})
	a.resyncWatches()
	a.log.InfoContext(ctx, "library created", "library", lib.ID, "name", name, "kind", kind, "paths", lib.Paths)
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityLibraryCreated, Text: domain.T("activity.library_created", "name", name, "paths", strings.Join(lib.Paths, ", ")),
	})
	return lib, nil
}

// UpdateLibrary changes a library. Changing its folders triggers a scan.
func (a *App) UpdateLibrary(ctx context.Context, id domain.ID, ch LibraryChanges) (domain.Library, error) {
	var lib domain.Library
	err := a.store.Write(ctx, func(q store.Q) error {
		var err error
		lib, err = q.Library(ctx, id)
		if store.IsNotFound(err) {
			return domain.NotFound("library.not_found")
		}
		if err != nil {
			return err
		}
		if ch.Name != nil {
			name := strings.TrimSpace(*ch.Name)
			if err := validateLibraryName(name); err != nil {
				return err
			}
			lib.Name = name
		}
		if ch.Language != nil {
			if !languageTag.MatchString(*ch.Language) {
				return domain.Invalid("library.invalid_language", "language", *ch.Language)
			}
			lib.Language = *ch.Language
		}
		rescan := false
		if ch.Paths != nil {
			paths, err := a.checkPaths(ctx, q, ch.Paths, lib.ID)
			if err != nil {
				return err
			}
			rescan = !slices.Equal(paths, lib.Paths)
			lib.Paths = paths
		}
		lib.UpdatedAt = a.now()
		if err := libraryWriteError(q.UpdateLibrary(ctx, lib), lib.Name); err != nil {
			return err
		}
		if rescan {
			return a.jobs.Enqueue(ctx, q, jobScanLibrary, lib.ID.String(), priorityUser)
		}
		return nil
	})
	if err != nil {
		return domain.Library{}, err
	}
	a.jobs.Kick()
	a.bus.Publish(domain.LibrariesChanged{})
	a.resyncWatches()
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityLibraryUpdated, Text: domain.T("activity.library_updated", "name", lib.Name, "paths", strings.Join(lib.Paths, ", ")),
	})
	return lib, nil
}

// ReorderLibraries puts the libraries in the given order: the order of their lists and, kind by
// kind, of the home rows. ids names every library once, and only once.
func (a *App) ReorderLibraries(ctx context.Context, ids []domain.ID) ([]LibraryInfo, error) {
	var names []string
	err := a.store.Write(ctx, func(q store.Q) error {
		libs, err := q.Libraries(ctx)
		if err != nil {
			return err
		}
		known := make(map[domain.ID]string, len(libs))
		for _, l := range libs {
			known[l.ID] = l.Name
		}
		names = names[:0]
		for _, id := range ids {
			name, ok := known[id]
			if !ok { // unknown, or named twice
				return domain.Invalid("library.invalid_order")
			}
			delete(known, id)
			names = append(names, name)
		}
		if len(known) > 0 {
			return domain.Invalid("library.invalid_order")
		}
		return q.SetLibraryOrder(ctx, ids)
	})
	if err != nil {
		return nil, err
	}
	a.bus.Publish(domain.LibrariesChanged{})
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityLibraryUpdated, Text: domain.T("activity.libraries_reordered", "names", strings.Join(names, ", ")),
	})
	return a.Libraries(ctx)
}

// DeleteLibrary deletes a library and its catalog (never the files on disk).
func (a *App) DeleteLibrary(ctx context.Context, id domain.ID) error {
	var lib domain.Library
	err := a.store.Write(ctx, func(q store.Q) error {
		var err error
		if lib, err = q.Library(ctx, id); store.IsNotFound(err) {
			return domain.NotFound("library.not_found")
		} else if err != nil {
			return err
		}
		return q.DeleteLibrary(ctx, id)
	})
	if err == nil {
		a.bus.Publish(domain.LibrariesChanged{})
		a.resyncWatches()
		a.record(ctx, domain.Activity{Kind: domain.ActivityLibraryDeleted, Text: domain.T("activity.library_deleted", "name", lib.Name)})
	}
	return err
}

// ScanLibrary asks for a scan right now.
func (a *App) ScanLibrary(ctx context.Context, id domain.ID) error {
	err := a.store.Write(ctx, func(q store.Q) error {
		if _, err := q.Library(ctx, id); store.IsNotFound(err) {
			return domain.NotFound("library.not_found")
		} else if err != nil {
			return err
		}
		return a.jobs.Enqueue(ctx, q, jobScanLibrary, id.String(), priorityUser)
	})
	if err == nil {
		a.jobs.Kick()
	}
	return err
}

// checkPaths validates the folders of a library: absolute, existing, readable, and not overlapping
// another library (a file must belong to only one).
func (a *App) checkPaths(ctx context.Context, q store.Q, paths []string, self domain.ID) ([]string, error) {
	if len(paths) == 0 {
		return nil, domain.Invalid("library.folder_required")
	}
	var out []string
	for _, p := range paths {
		p = filepath.Clean(strings.TrimSpace(p))
		if !filepath.IsAbs(p) {
			return nil, domain.Invalid("folder.not_absolute", "path", p)
		}
		st, err := os.Stat(p)
		if err != nil || !st.IsDir() {
			return nil, domain.Invalid("folder.not_found", "path", p)
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	for i, p := range out {
		for _, other := range out[i+1:] {
			if library.Under(other, []string{p}) {
				return nil, domain.Invalid("library.folders_overlap", "path", p, "other", other)
			}
		}
	}
	existing, err := q.AllLibraryPaths(ctx)
	if err != nil {
		return nil, err
	}
	for other, owner := range existing {
		if owner == self {
			continue
		}
		for _, p := range out {
			if library.Under(p, []string{other}) || library.Under(other, []string{p}) {
				return nil, domain.Conflict("library.folder_in_use", "path", p, "other", other)
			}
		}
	}
	return out, nil
}

func validateLibraryName(name string) error {
	if name == "" {
		return domain.Invalid("library.name_required")
	}
	if utf8.RuneCountInString(name) > maxLibraryNameLen {
		return domain.Invalid("library.name_too_long", "max", maxLibraryNameLen)
	}
	return nil
}

func libraryWriteError(err error, name string) error {
	if errors.Is(err, store.ErrDuplicate) {
		return domain.Conflict("library.already_exists", "name", name)
	}
	return err
}
