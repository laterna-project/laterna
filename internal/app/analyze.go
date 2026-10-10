package app

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/jobs"
	"github.com/laterna-project/laterna/internal/naming"
	"github.com/laterna-project/laterna/internal/store"
)

// analyzeFile analyzes a file (ffprobe, or reading a book) and files it in the catalog: movie,
// series / season / episode, artist / album / track, or series / book. The items it creates get
// their metadata afterwards.
func (a *App) analyzeFile(ctx context.Context, target string) error {
	id, err := domain.ParseID(target)
	if err != nil {
		return jobs.Permanent(err)
	}
	read := a.store.Read()
	f, err := read.File(ctx, id)
	if store.IsNotFound(err) {
		return nil // file forgotten in the meantime
	}
	if err != nil {
		return err
	}
	if f.MissingSince != nil {
		return nil
	}
	lib, err := read.Library(ctx, f.LibraryID)
	if err != nil {
		return err
	}
	_, rel, ok := relativeTo(lib.Paths, f.Path)
	if !ok {
		return jobs.Permanent(fmt.Errorf("%s is outside the library folders", f.Path))
	}
	switch lib.Kind {
	case domain.LibraryBooks:
		return a.analyzeBook(ctx, lib, f, rel)
	case domain.LibraryPhotos:
		return a.analyzePhoto(ctx, lib, f, rel)
	case domain.LibraryMovies, domain.LibraryShows, domain.LibraryMusic:
	}

	info, probeErr := a.prober.Probe(ctx, f.Path)
	now := a.now()
	if probeErr != nil {
		_ = a.store.Write(ctx, func(q store.Q) error { return q.SetFileAnalysisError(ctx, f.ID, probeErr.Error(), now) })
		return probeErr
	}

	f.Info = info
	var enrich, changed []domain.ID
	var removed int64
	var arrived *newEpisode
	err = a.store.Write(ctx, func(q store.Q) error {
		if err := q.SetFileAnalysis(ctx, f.ID, info, now); err != nil {
			return err
		}
		// Named chapters give intro and credits right away; audio comes later.
		if err := setChapterSegments(ctx, q, f); err != nil {
			return err
		}
		var err error
		switch lib.Kind {
		case domain.LibraryMovies:
			enrich, err = a.placeMovie(ctx, q, lib, f, rel)
		case domain.LibraryShows:
			enrich, arrived, err = a.placeEpisode(ctx, q, lib, f, rel)
		case domain.LibraryMusic:
			enrich, err = a.placeTrack(ctx, q, lib, f, rel)
		case domain.LibraryBooks, domain.LibraryPhotos: // analyzeBook, analyzePhoto
		}
		if err != nil {
			return err
		}
		for _, itemID := range enrich {
			if err := a.jobs.Enqueue(ctx, q, jobItemMetadata, itemID.String(), priorityBackground); err != nil {
				return err
			}
		}
		// The keyframe index (HLS playback) and the subtitles are ready before the first playback.
		if hasVideo(info) {
			for _, kind := range []string{jobIndexFile, jobExtractSubtitles, jobTrickplay, jobDetectSegments} {
				if err := a.jobs.Enqueue(ctx, q, kind, f.ID.String(), priorityBackground); err != nil {
					return err
				}
			}
		}
		if owner, err := q.FileItem(ctx, f.ID); err == nil {
			changed = append(changed, owner)
		} else if !store.IsNotFound(err) {
			return err
		}
		// A moved file may have left behind an item with nothing in it.
		removed, err = q.DeleteOrphanItems(ctx, lib.ID)
		return err
	})
	if err != nil {
		return err
	}
	a.jobs.Kick()
	a.itemsChanged(lib.ID, append(changed, enrich...)...)
	if removed > 0 {
		a.libraryChanged(lib.ID)
	}
	if arrived != nil {
		a.episodeArrived(arrived.series, arrived.episode)
	}
	return nil
}

// placeMovie files a movie file. It returns the items whose metadata must be read.
func (a *App) placeMovie(ctx context.Context, q store.Q, lib domain.Library, f domain.MediaFile, rel string) ([]domain.ID, error) {
	m := naming.ParseMovie(rel)
	title := movieTitle(m, rel)
	key := "movie:" + m.GroupKey
	if err := a.followMove(ctx, q, lib.ID, f.ID, key, false); err != nil {
		return nil, err
	}
	item, _, err := a.findOrCreate(ctx, q, domain.Item{
		LibraryID: lib.ID, Kind: domain.ItemMovie, GroupKey: key, Title: title, Year: m.Year,
	})
	if err != nil {
		return nil, err
	}
	if err := q.LinkFile(ctx, item.ID, f.ID, m.Version, m.Part); err != nil {
		return nil, err
	}
	// Even for an existing movie (new version, renamed folder) the local metadata is read again:
	// the new file may bring an NFO or a poster.
	return []domain.ID{item.ID}, nil
}

// placeEpisode files an episode file, creating the series and season if needed.
func (a *App) placeEpisode(ctx context.Context, q store.Q, lib domain.Library, f domain.MediaFile, rel string) ([]domain.ID, *newEpisode, error) {
	e, ok := naming.ParseEpisode(rel)
	if !ok {
		a.log.InfoContext(ctx, "file not recognised as an episode, ignored", "path", f.Path)
		return nil, nil, q.SetFileAnalysisError(ctx, f.ID, "no episode number found in the name", a.now())
	}
	seriesKey := "series:" + e.SeriesDir
	if e.SeriesDir == "" {
		seriesKey = "series:/" + naming.Key(e.SeriesTitle)
	}
	if err := a.followMove(ctx, q, lib.ID, f.ID, seriesKey, true); err != nil {
		return nil, nil, err
	}
	var enrich []domain.ID
	series, created, err := a.findOrCreate(ctx, q, domain.Item{
		LibraryID: lib.ID, Kind: domain.ItemSeries, GroupKey: seriesKey, Title: e.SeriesTitle, Year: e.SeriesYear,
	})
	if err != nil {
		return nil, nil, err
	}
	if created {
		enrich = append(enrich, series.ID)
	}
	season, created, err := a.findOrCreate(ctx, q, domain.Item{
		LibraryID: lib.ID, Kind: domain.ItemSeason, ParentID: &series.ID,
		GroupKey: fmt.Sprintf("season:%s:%d", series.ID, e.Season), Title: domain.SeasonTitle(e.Season),
		SortTitle: fmt.Sprintf("%04d", e.Season),
	})
	if err != nil {
		return nil, nil, err
	}
	if created {
		if err := q.CreateSeason(ctx, domain.Season{ItemID: season.ID, SeriesID: series.ID, Number: e.Season}); err != nil {
			return nil, nil, err
		}
		enrich = append(enrich, season.ID)
	}
	epKey := fmt.Sprintf("episode:%s:%d:%d", series.ID, e.Season, e.Episode)
	if e.EpisodeEnd > 0 {
		epKey += fmt.Sprintf("-%d", e.EpisodeEnd)
	}
	episode, created, err := a.findOrCreate(ctx, q, domain.Item{
		LibraryID: lib.ID, Kind: domain.ItemEpisode, ParentID: &season.ID, GroupKey: epKey, Title: episodeTitle(e),
		SortTitle: fmt.Sprintf("%04d", e.Episode),
	})
	if err != nil {
		return nil, nil, err
	}
	// An episode the catalog did not have is announced to those who follow its series.
	var arrived *newEpisode
	if created {
		if err := q.CreateEpisode(ctx, domain.Episode{
			ItemID: episode.ID, SeriesID: series.ID, SeasonID: season.ID, SeasonNumber: e.Season,
			Number: e.Episode, NumberEnd: e.EpisodeEnd, Absolute: e.Absolute,
		}); err != nil {
			return nil, nil, err
		}
		arrived = &newEpisode{series: series.ID, episode: episode.ID}
	}
	if err := q.LinkFile(ctx, episode.ID, f.ID, "", 0); err != nil {
		return nil, nil, err
	}
	return append(enrich, episode.ID), arrived, nil
}

// followMove keeps the identity of an item whose folder was renamed: if the file already belonged
// to a movie (or a series) whose key no longer matches, and no item has the new key, it is the same
// item that moved. Its key is updated instead of creating another item and losing its history.
func (a *App) followMove(ctx context.Context, q store.Q, libraryID, fileID domain.ID, newKey string, series bool) error {
	prevID, err := q.FileItem(ctx, fileID)
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if series {
		ep, err := q.Episode(ctx, prevID)
		if store.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		prevID = ep.SeriesID
	}
	prev, err := q.Item(ctx, prevID)
	if err != nil {
		return err
	}
	if prev.GroupKey == newKey {
		return nil
	}
	if _, err := q.ItemByGroupKey(ctx, libraryID, newKey); err == nil || !store.IsNotFound(err) {
		return err // the new key already exists: the file joins that item
	}
	return q.SetItemGroupKey(ctx, prev.ID, newKey, a.now())
}

// findOrCreate finds the item of a group key, or creates it.
func (a *App) findOrCreate(ctx context.Context, q store.Q, it domain.Item) (domain.Item, bool, error) {
	existing, err := q.ItemByGroupKey(ctx, it.LibraryID, it.GroupKey)
	if err == nil {
		return existing, false, nil
	}
	if !store.IsNotFound(err) {
		return domain.Item{}, false, err
	}
	now := a.now()
	it.ID = domain.NewID()
	if it.SortTitle == "" {
		it.SortTitle = naming.SortTitle(it.Title)
	}
	it.AddedAt, it.UpdatedAt = now, now
	if err := q.CreateItem(ctx, it); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			return domain.Item{}, false, fmt.Errorf("item %q created twice: %w", it.GroupKey, err)
		}
		return domain.Item{}, false, err
	}
	return it, true, nil
}

// movieTitle is the title of a movie from its name (the file name as a fallback).
func movieTitle(m naming.Movie, rel string) string {
	if m.Title != "" {
		return m.Title
	}
	return strings.TrimSuffix(path.Base(rel), path.Ext(rel))
}

// episodeTitle is the title of an episode from its name (domain.EpisodeTitle as a fallback).
func episodeTitle(e naming.Episode) string {
	if e.Title != "" {
		return e.Title
	}
	return domain.EpisodeTitle(e.Episode)
}

// runtimeOf returns the runtime of an item from its metadata or, failing that, from its file.
func runtimeOf(nfoMinutes int, file time.Duration) time.Duration {
	if nfoMinutes > 0 {
		return time.Duration(nfoMinutes) * time.Minute
	}
	return file.Round(time.Second)
}

func hasVideo(info domain.MediaInfo) bool {
	for _, s := range info.Streams {
		if s.Kind == domain.StreamVideo {
			return true
		}
	}
	return false
}
