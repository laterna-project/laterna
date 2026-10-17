package app

import (
	"context"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/naming"
	"github.com/laterna-project/laterna/internal/store"
)

// Extras of movies and series (trailers, featurettes...): items whose parent is their movie or
// series, played like a movie but never resumed, counted or recommended.

// placeExtra files an extra under its movie or series. false means the file is not an extra, or not
// yet: an episode, or a suffix that neither a movie of its folder nor the folder itself is named
// after ("Lost (2001)/Lost.2001.BluRay-SCENE.mkv"); it is filed as a movie, and filed again as an
// extra if a movie of that name shows up. An extra whose movie or series is not there yet waits,
// with an analysis error, and is analyzed again when its owner arrives (adoptExtras).
func (a *App) placeExtra(ctx context.Context, q store.Q, lib domain.Library, f domain.MediaFile, root, rel string, x naming.Extra) ([]domain.ID, bool, error) {
	var owner *domain.ID
	switch lib.Kind {
	case domain.LibraryShows:
		if _, episode := naming.ParseEpisode(rel); episode && x.OwnerName != "" {
			return nil, false, nil
		}
		if dir := extraSeriesDir(x.Owner); dir != "" {
			series, err := q.ItemByGroupKey(ctx, lib.ID, "series:"+dir)
			switch {
			case err == nil:
				owner = &series.ID
			case !store.IsNotFound(err):
				return nil, false, err
			}
		}
	case domain.LibraryMovies:
		movies, err := q.MoviesInFolder(ctx, lib.ID, filepath.Join(root, filepath.FromSlash(x.Owner)))
		if err != nil {
			return nil, false, err
		}
		owner = ownerMovie(movies, x)
		if owner == nil && x.OwnerName != "" && !strings.EqualFold(path.Base(x.Owner), x.OwnerName) {
			return nil, false, nil
		}
	case domain.LibraryMusic, domain.LibraryBooks, domain.LibraryPhotos:
		return nil, false, nil
	}
	if owner == nil {
		a.log.InfoContext(ctx, "extra waiting for its movie or series", "path", f.Path)
		return nil, true, q.SetFileAnalysisError(ctx, f.ID, "extra without its movie or series yet", a.now())
	}
	item, created, err := a.findOrCreate(ctx, q, domain.Item{
		LibraryID: lib.ID, Kind: domain.ItemExtra, ParentID: owner, GroupKey: "extra:" + rel, Title: x.Title,
	})
	if err != nil {
		return nil, true, err
	}
	if !created && (item.ParentID == nil || *item.ParentID != *owner) {
		if err := q.SetItemParent(ctx, item.ID, owner, a.now()); err != nil {
			return nil, true, err
		}
	}
	if err := q.SetExtra(ctx, domain.Extra{ItemID: item.ID, Kind: x.Kind}); err != nil {
		return nil, true, err
	}
	if err := q.LinkFile(ctx, item.ID, f.ID, "", 0); err != nil {
		return nil, true, err
	}
	return []domain.ID{item.ID}, true, nil
}

// ownerMovie picks the movie of an extra among those of its folder (item → its files): the one it
// is named after, otherwise, for an extra in a folder of extras, the only one; with several, the one
// whose file comes first, so that the choice does not change from one scan to the next. nil if
// none fits.
func ownerMovie(movies map[domain.ID][]string, x naming.Extra) *domain.ID {
	type candidate struct {
		id   domain.ID
		path string
	}
	var all []candidate
	for id, files := range movies {
		for _, p := range files {
			base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
			if x.OwnerName != "" && strings.EqualFold(base, x.OwnerName) {
				return &id
			}
			all = append(all, candidate{id, p})
		}
	}
	if x.OwnerName != "" || len(all) == 0 {
		return nil
	}
	first := slices.MinFunc(all, func(a, b candidate) int { return strings.Compare(a.path, b.path) })
	return &first.id
}

// extraSeriesDir is the series folder of an extra's owner folder: its first level ("Show/Season 01"
// gives "Show"); "" at the root.
func extraSeriesDir(owner string) string {
	if owner == "." || owner == "" {
		return ""
	}
	first, _, _ := strings.Cut(owner, "/")
	return first
}

// adoptExtras queues the analysis of the extras of a movie or a series whose file (self) has just
// been filed in folder (relative to root): the files below that folder (directly in it at the root)
// that are extras of that owner (mine) and are not filed as extras yet, waiting or taken for a
// movie. mine must only accept a suffix named after that owner: a file taken for a movie because no
// movie has its name would otherwise be analyzed again and again.
func (a *App) adoptExtras(ctx context.Context, q store.Q, lib domain.Library, root, folder string, self domain.ID, mine func(naming.Extra) bool) error {
	dir := filepath.Join(root, filepath.FromSlash(folder))
	files, err := q.FilesUnder(ctx, lib.ID, dir)
	if err != nil {
		return err
	}
	for _, f := range files {
		rel, err := filepath.Rel(root, f.Path)
		if err != nil || f.ID == self || f.ItemKind == domain.ItemExtra || (folder == "." && filepath.Dir(f.Path) != filepath.Clean(dir)) {
			continue
		}
		if x, ok := naming.ParseExtra(path.Clean(filepath.ToSlash(rel))); ok && mine(x) {
			if err := a.jobs.Enqueue(ctx, q, jobAnalyzeFile, f.ID.String(), priorityBackground); err != nil {
				return err
			}
		}
	}
	return nil
}
