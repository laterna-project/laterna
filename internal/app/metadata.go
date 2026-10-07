package app

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/jobs"
	"github.com/laterna-project/laterna/internal/media/images"
	"github.com/laterna-project/laterna/internal/metadata"
	"github.com/laterna-project/laterna/internal/naming"
	"github.com/laterna-project/laterna/internal/store"
)

// localSources says where to look for the local metadata of an item, and what the names of its
// folders and files say about it.
type localSources struct {
	nfo     []string
	artwork func() ([]metadata.Artwork, error)
	// names holds the title, sort key, year and IDs taken from the names.
	names    domain.Metadata
	duration int64 // ms, runtime of the file (movies, episodes)
	// file is the first file of a movie or an episode.
	file *domain.MediaFile
}

// refreshMetadata reads the metadata of an item again and applies it: what the names say,
// overridden by the NFO; the images sitting next to the media and, for kinds without a local image,
// those the NFO gives a URL for (downloaded separately); the photo of each credited person. Nothing
// else: what is no longer in the NFO goes away.
func (a *App) refreshMetadata(ctx context.Context, target string) error {
	id, err := domain.ParseID(target)
	if err != nil {
		return jobs.Permanent(err)
	}
	read := a.store.Read()
	item, err := read.Item(ctx, id)
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	lib, err := read.Library(ctx, item.LibraryID)
	if err != nil {
		return err
	}
	switch item.Kind {
	case domain.ItemBook:
		return a.refreshBook(ctx, lib, item)
	case domain.ItemBookSeries:
		return a.refreshBookSeries(ctx, item)
	case domain.ItemPhotoAlbum:
		return a.refreshPhotoAlbum(ctx, item)
	case domain.ItemPhoto:
		return nil // everything that describes it comes from the file, read at analysis time
	case domain.ItemMovie, domain.ItemSeries, domain.ItemSeason, domain.ItemEpisode, domain.ItemArtist, domain.ItemAlbum,
		domain.ItemTrack:
	}
	src, ok, err := a.localSources(ctx, lib, item)
	if err != nil || !ok {
		return err
	}

	meta := src.names
	if src.duration > 0 {
		meta.Runtime = runtimeOf(0, time.Duration(src.duration)*time.Millisecond)
	}
	var nfo *metadata.NFO
	if nfoPath := metadata.NFOPath(src.nfo...); nfoPath != "" {
		n, err := readNFO(nfoPath)
		if err != nil {
			a.log.WarnContext(ctx, "unreadable NFO, ignored", "path", nfoPath, "err", err)
		} else {
			nfo = &n
			applyNFO(n, item.Kind, &meta)
		}
	}
	if age, ok := domain.RatingAge(meta.OfficialRating); ok {
		meta.AgeRating = &age
	}
	var art []metadata.Artwork
	if src.artwork != nil {
		if art, err = src.artwork(); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	// An album without a cover next to its tracks: use the one embedded in a track, if any. An
	// episode without a thumbnail of its own: a frame of its video (stills.go).
	var embedded []wantedImage
	if item.Kind == domain.ItemAlbum && !slices.ContainsFunc(art, func(a metadata.Artwork) bool { return a.Kind == domain.ImagePoster }) {
		if w, ok := a.embeddedCover(ctx, item.ID); ok {
			embedded = append(embedded, w)
		}
	}
	if item.Kind == domain.ItemEpisode && src.file != nil && a.needsStill(art, nfo) {
		if w, ok := a.episodeStill(ctx, item.ID, *src.file); ok {
			embedded = append(embedded, w)
		}
	}
	want := a.wantedImages(item.ID, art, embedded, nfo)

	err = a.store.Write(ctx, func(q store.Q) error {
		people, err := q.SetMetadata(ctx, item.ID, meta, a.now())
		if err != nil {
			return err
		}
		if err := a.syncImages(ctx, q, item.ID, want); err != nil {
			return err
		}
		if item.Kind == domain.ItemMovie || item.Kind == domain.ItemSeries {
			var set *store.NFOSet
			if nfo != nil {
				set = nfoSet(nfo.Collection)
			}
			if err := q.SetNFOCollection(ctx, item.ID, set, a.now()); err != nil {
				return err
			}
		}
		return a.setPeopleImages(ctx, q, meta.Credits, people)
	})
	if err != nil {
		return err
	}
	a.jobs.Kick()
	a.itemsChanged(item.LibraryID, item.ID)
	return nil
}

// localSources gathers the locations of local metadata depending on the kind of item.
func (a *App) localSources(ctx context.Context, lib domain.Library, item domain.Item) (localSources, bool, error) {
	read := a.store.Read()
	switch item.Kind {
	case domain.ItemMovie, domain.ItemEpisode:
		files, err := read.ItemFiles(ctx, item.ID)
		if err != nil || len(files) == 0 {
			return localSources{}, false, err
		}
		file := files[0].File
		dir := filepath.Dir(file.Path)
		base := strings.TrimSuffix(filepath.Base(file.Path), filepath.Ext(file.Path))
		_, rel, _ := relativeTo(lib.Paths, file.Path)
		src := localSources{
			nfo: []string{filepath.Join(dir, base+".nfo")}, duration: file.Info.Duration.Milliseconds(), file: &file,
			names: domain.Metadata{Title: item.Title, SortTitle: item.SortTitle, Year: item.Year},
		}
		if item.Kind == domain.ItemMovie {
			ownFolder := path.Dir(rel) != "."
			if ownFolder {
				src.nfo = append(src.nfo, filepath.Join(dir, "movie.nfo"))
			}
			m := naming.ParseMovie(rel)
			title := movieTitle(m, rel)
			src.names = domain.Metadata{Title: title, SortTitle: naming.SortTitle(title), Year: m.Year, ProviderIDs: m.IDs}
			src.artwork = func() ([]metadata.Artwork, error) { return metadata.MovieArtwork(dir, base, ownFolder) }
		} else {
			if e, ok := naming.ParseEpisode(rel); ok {
				src.names.Title = episodeTitle(e)
			}
			src.artwork = func() ([]metadata.Artwork, error) { return metadata.EpisodeArtwork(dir, base) }
		}
		return src, true, nil

	case domain.ItemSeries:
		p, err := read.FirstFileOfSeries(ctx, item.ID)
		if store.IsNotFound(err) {
			return localSources{}, false, nil
		}
		if err != nil {
			return localSources{}, false, err
		}
		seriesDir, rel := seriesDirOf(lib, p)
		src := localSources{names: domain.Metadata{Title: item.Title, SortTitle: item.SortTitle, Year: item.Year}}
		if e, ok := naming.ParseEpisode(rel); ok && e.SeriesTitle != "" {
			src.names = domain.Metadata{Title: e.SeriesTitle, SortTitle: naming.SortTitle(e.SeriesTitle), Year: e.SeriesYear, ProviderIDs: e.SeriesIDs}
		}
		if seriesDir != "" {
			src.nfo = []string{filepath.Join(seriesDir, "tvshow.nfo")}
			src.artwork = func() ([]metadata.Artwork, error) { return metadata.SeriesArtwork(seriesDir) }
		}
		return src, true, nil

	case domain.ItemSeason:
		season, err := read.Season(ctx, item.ID)
		if err != nil {
			return localSources{}, false, err
		}
		p, err := read.FirstFileOfSeason(ctx, item.ID)
		if store.IsNotFound(err) {
			return localSources{}, false, nil
		}
		if err != nil {
			return localSources{}, false, err
		}
		names := domain.Metadata{Title: domain.SeasonTitle(season.Number), SortTitle: item.SortTitle}
		seriesDir, _ := seriesDirOf(lib, p)
		if seriesDir == "" {
			return localSources{names: names}, true, nil
		}
		seasonDir := filepath.Dir(p)
		src := localSources{names: names, artwork: func() ([]metadata.Artwork, error) {
			return metadata.SeasonArtwork(seriesDir, seasonDir, season.Number)
		}}
		if seasonDir != seriesDir {
			src.nfo = []string{filepath.Join(seasonDir, "season.nfo")}
		}
		return src, true, nil

	case domain.ItemArtist, domain.ItemAlbum, domain.ItemTrack:
		return a.musicSources(ctx, lib, item)
	case domain.ItemBookSeries, domain.ItemBook, domain.ItemPhotoAlbum, domain.ItemPhoto: // read separately
	}
	return localSources{}, false, nil
}

// seriesDirOf returns the folder of a series (first level under the root) from the path of one of
// its files, or "" if the files sit at the root.
func seriesDirOf(lib domain.Library, filePath string) (dir, rel string) {
	root, rel, ok := relativeTo(lib.Paths, filePath)
	if !ok {
		return "", ""
	}
	first, _, found := strings.Cut(rel, "/")
	if !found {
		return "", rel
	}
	return filepath.Join(root, first), rel
}

// readNFO reads an NFO file.
func readNFO(p string) (metadata.NFO, error) {
	f, err := os.Open(p)
	if err != nil {
		return metadata.NFO{}, err
	}
	defer func() { _ = f.Close() }()
	return metadata.ParseNFO(f)
}

// applyNFO lays the content of an NFO over the metadata: whatever it sets wins.
func applyNFO(n metadata.NFO, kind domain.ItemKind, meta *domain.Metadata) {
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&meta.Title, n.Title)
	// Seasons and episodes sort by number: their sort key does not depend on the title.
	if kind == domain.ItemMovie || kind == domain.ItemSeries {
		switch {
		case n.SortTitle != "":
			meta.SortTitle = naming.SortTitle(n.SortTitle)
		case n.Title != "":
			meta.SortTitle = naming.SortTitle(n.Title)
		}
	}
	set(&meta.OriginalTitle, n.OriginalTitle)
	if n.Year > 0 {
		meta.Year = n.Year
	}
	set(&meta.PremiereDate, n.Premiered)
	set(&meta.Overview, n.Plot)
	set(&meta.Tagline, n.Tagline)
	set(&meta.OfficialRating, n.MPAA)
	if n.Rating > 0 {
		meta.CommunityRating = n.Rating
	}
	if n.RuntimeMins > 0 {
		meta.Runtime = runtimeOf(n.RuntimeMins, 0)
	}
	if len(n.Genres) > 0 {
		meta.Genres = n.Genres
	}
	if len(n.Studios) > 0 {
		meta.Studios = n.Studios
	}
	if len(n.IDs) > 0 {
		ids := maps.Clone(meta.ProviderIDs)
		if ids == nil {
			ids = map[string]string{}
		}
		maps.Copy(ids, n.IDs)
		meta.ProviderIDs = ids
	}
	for _, actor := range n.Actors {
		meta.Credits = append(meta.Credits, domain.Credit{Name: actor.Name, Role: domain.RoleActor, Character: actor.Role, Order: actor.Order, Thumb: actor.Thumb})
	}
	for i, d := range n.Directors {
		meta.Credits = append(meta.Credits, domain.Credit{Name: d, Role: domain.RoleDirector, Order: i})
	}
	for i, w := range n.Writers {
		meta.Credits = append(meta.Credits, domain.Credit{Name: w, Role: domain.RoleWriter, Order: i})
	}
}

// analyzeImage computes the dimensions, blurhash and content hash of an image.
func (a *App) analyzeImage(ctx context.Context, target string) error {
	id, err := domain.ParseID(target)
	if err != nil {
		return jobs.Permanent(err)
	}
	img, err := a.store.Read().Image(ctx, id)
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// A photo comes out of the analysis with its thumbnails, so its first grid does not wait for
	// them.
	var thumbs func(images.Analysis) map[int]string
	if img.Kind == domain.ImagePhoto {
		thumbs = func(res images.Analysis) map[int]string {
			out := map[int]string{}
			for _, w := range photoThumbs {
				if p := a.imageCachePath(res.Hash, w); w < res.Width && !fileExists(p) {
					out[w] = p
				}
			}
			return out
		}
	}
	res, err := images.AnalyzeWith(img.Path, thumbs)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // image removed in the meantime: the next metadata pass will remove it
	}
	if err != nil {
		return jobs.Permanent(err)
	}
	err = a.store.Write(ctx, func(q store.Q) error {
		return q.SetImageAnalysis(ctx, img.ID, res.Width, res.Height, res.BlurHash, res.Hash, a.now())
	})
	if err != nil || img.ItemID == nil {
		return err
	}
	// The image can now be served: clients showing the item load it.
	if it, err := a.store.Read().Item(ctx, *img.ItemID); err == nil {
		a.itemsChanged(it.LibraryID, it.ID)
	}
	return nil
}
