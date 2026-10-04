package app

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/jobs"
	"github.com/laterna-project/laterna/internal/library"
	"github.com/laterna-project/laterna/internal/store"
)

// scanBatch caps the size of a scan's write transactions: the write lock is released regularly
// (progress reads, other jobs).
const scanBatch = 200

// scanStats sums up a scan for the log.
type scanStats struct {
	added, changed, moved, missing, returned, forgotten int
	// subtitles counts the files whose subtitles must be extracted (again).
	subtitles int
	// metadata counts the items to read again because their NFO files or images changed.
	metadata int
}

// scanLibrary compares the folders of a library with what is known:
//   - new file: fingerprint, then either a recognized move (same fingerprint as a missing
//     file) or a new file, then analysis;
//   - changed file: new fingerprint and new analysis;
//   - missing file: marked missing, only forgotten after the grace period;
//   - root unreachable, or empty where files were known: nothing is marked;
//   - NFO or image added, changed or removed: the items concerned are read again.
func (a *App) scanLibrary(ctx context.Context, target string) error {
	id, err := domain.ParseID(target)
	if err != nil {
		return jobs.Permanent(err)
	}
	lib, err := a.store.Read().Library(ctx, id)
	if store.IsNotFound(err) {
		return nil // library deleted in the meantime
	}
	if err != nil {
		return err
	}
	walked, err := library.Walk(ctx, lib.Paths, mediaOf(lib.Kind))
	if err != nil {
		return err
	}
	known, err := a.store.Read().LibraryFiles(ctx, lib.ID)
	if err != nil {
		return err
	}

	unsafe := slices.Concat(walked.Unavailable, walked.Unreadable)
	// An empty root where files were known: missing mount point.
	knownPerRoot := map[string]int{}
	for _, k := range known {
		for _, root := range lib.Paths {
			if library.Under(k.Path, []string{root}) && k.MissingSince == nil {
				knownPerRoot[root]++
			}
		}
	}
	for _, root := range lib.Paths {
		if walked.Counts[root] == 0 && knownPerRoot[root] > 0 {
			unsafe = append(unsafe, root)
		}
	}
	for _, root := range unsafe {
		a.log.WarnContext(ctx, "scan: folder unreachable, its content is kept as is", "library", lib.ID, "path", root)
	}

	byPath := make(map[string]store.KnownFile, len(known))
	for _, k := range known {
		byPath[k.Path] = k
	}
	onDisk := make(map[string]bool, len(walked.Entries))
	for _, e := range walked.Entries {
		onDisk[e.Path] = true
	}
	// Known files that are no longer where they were: candidates for a move.
	gone := map[string][]store.KnownFile{}
	for _, k := range known {
		if !onDisk[k.Path] {
			gone[k.Fingerprint] = append(gone[k.Fingerprint], k)
		}
	}

	var stats scanStats
	var ops []func(q store.Q) error
	now := a.now()
	sidecars := library.Sidecars(walked)
	// Subtitles and scrubbing thumbnails are for video only.
	video := lib.Kind == domain.LibraryMovies || lib.Kind == domain.LibraryShows
	trickplayOn := a.Settings().Trickplay && video
	segmentsAudio := a.Settings().DetectSegments && lib.Kind == domain.LibraryShows
	for _, e := range walked.Entries {
		k, exists := byPath[e.Path]
		switch {
		case exists && (k.Size != e.Size || k.ModTime.UnixMilli() != e.ModTime.UnixMilli()):
			fp, err := library.Fingerprint(e.Path)
			if err != nil {
				a.log.WarnContext(ctx, "scan: cannot fingerprint file", "path", e.Path, "err", err)
				continue
			}
			stats.changed++
			ops = append(ops, func(q store.Q) error {
				if err := q.UpdateFileContent(ctx, k.ID, e.Size, e.ModTime, fp, now); err != nil {
					return err
				}
				return a.jobs.Enqueue(ctx, q, jobAnalyzeFile, k.ID.String(), priorityBackground)
			})
		case exists:
			if k.MissingSince != nil {
				stats.returned++
				ops = append(ops, func(q store.Q) error { return q.MarkFilePresent(ctx, k.ID, now) })
			}
			if !k.Analyzed {
				ops = append(ops, func(q store.Q) error {
					return a.jobs.Enqueue(ctx, q, jobAnalyzeFile, k.ID.String(), priorityBackground)
				})
			} else if video && (k.SubtitlesFingerprint != k.Fingerprint || k.SubtitlesSidecars != library.Signature(e, sidecars[e.Path])) {
				// Subtitles never extracted (file known from an earlier version), or an external
				// subtitle added, changed or removed.
				stats.subtitles++
				ops = append(ops, func(q store.Q) error {
					return a.jobs.Enqueue(ctx, q, jobExtractSubtitles, k.ID.String(), priorityBackground)
				})
			}
			if k.Analyzed && k.TrickplayFingerprint != k.Fingerprint && trickplayOn {
				// Scrubbing thumbnails never generated, or made from previous content.
				ops = append(ops, func(q store.Q) error {
					return a.jobs.Enqueue(ctx, q, jobTrickplay, k.ID.String(), priorityBackground)
				})
			}
			if k.Analyzed && video && (k.SegmentsFingerprint != k.Fingerprint || segmentsAudio && !k.SegmentsAudio) {
				// Intro and credits never looked for, or for previous content, or not yet by audio.
				ops = append(ops, func(q store.Q) error {
					return a.jobs.Enqueue(ctx, q, jobDetectSegments, k.ID.String(), priorityBackground)
				})
			}
		default:
			fp, err := library.Fingerprint(e.Path)
			if err != nil {
				a.log.WarnContext(ctx, "scan: cannot fingerprint file", "path", e.Path, "err", err)
				continue
			}
			if candidates := gone[fp]; len(candidates) > 0 {
				moved := candidates[0]
				gone[fp] = candidates[1:]
				delete(byPath, moved.Path) // will not be marked missing
				stats.moved++
				ops = append(ops, func(q store.Q) error {
					if err := q.MoveFile(ctx, moved.ID, e.Path, now); err != nil {
						return err
					}
					// Analyze again: the new path may change the item (renamed folder).
					return a.jobs.Enqueue(ctx, q, jobAnalyzeFile, moved.ID.String(), priorityBackground)
				})
				continue
			}
			stats.added++
			f := domain.MediaFile{ID: domain.NewID(), LibraryID: lib.ID, Path: e.Path, Size: e.Size, ModTime: e.ModTime, Fingerprint: fp}
			ops = append(ops, func(q store.Q) error {
				if err := q.CreateFile(ctx, f, now); err != nil {
					return err
				}
				return a.jobs.Enqueue(ctx, q, jobAnalyzeFile, f.ID.String(), priorityBackground)
			})
		}
	}
	for path, k := range byPath {
		if onDisk[path] || k.MissingSince != nil || library.Under(path, unsafe) {
			continue
		}
		stats.missing++
		ops = append(ops, func(q store.Q) error { return q.MarkFileMissing(ctx, k.ID, now) })
	}
	metaOps, n, err := a.metadataChanges(ctx, lib, walked, unsafe)
	if err != nil {
		return err
	}
	stats.metadata = n
	ops = append(ops, metaOps...)
	if err := a.applyBatches(ctx, ops); err != nil {
		return err
	}

	// Forget the files that have been missing for longer than the grace period.
	err = a.store.Write(ctx, func(q store.Q) error {
		expired, err := q.FilesMissingBefore(ctx, lib.ID, now.Add(-a.Settings().MissingGrace))
		if err != nil {
			return err
		}
		for _, fid := range expired {
			if err := q.DeleteFile(ctx, fid); err != nil {
				return err
			}
		}
		stats.forgotten = len(expired)
		if len(expired) > 0 {
			// Subtitles and fonts of forgotten files.
			if err := a.jobs.Enqueue(ctx, q, jobPurgeSubtitles, "", priorityBackground); err != nil {
				return err
			}
		}
		if _, err := q.DeleteOrphanItems(ctx, lib.ID); err != nil {
			return err
		}
		return q.SetLibraryScanned(ctx, lib.ID, now)
	})
	if err != nil {
		return err
	}
	// Files were added or removed: refresh the planner statistics once their analyses are done
	// (items only come into being at analysis time).
	if stats.added+stats.changed+stats.moved+stats.missing+stats.forgotten > 0 {
		if err := a.store.Write(ctx, func(q store.Q) error {
			return a.jobs.Enqueue(ctx, q, jobOptimizeStore, "", priorityIdle)
		}); err != nil {
			a.log.WarnContext(ctx, "database statistics: cannot request update", "err", err)
		}
	}
	a.jobs.Kick()
	// Added or changed files are announced after their analysis. Missing, returned and forgotten
	// ones change what is visible: the whole library must be reloaded.
	if stats.missing+stats.returned+stats.forgotten > 0 {
		a.libraryChanged(lib.ID)
	}
	a.bus.Publish(domain.LibraryScanned{
		LibraryID: lib.ID, Files: len(walked.Entries), Added: stats.added, Changed: stats.changed, Moved: stats.moved,
		Missing: stats.missing, Returned: stats.returned, Forgotten: stats.forgotten,
	})
	a.log.InfoContext(ctx, "scan done", "library", lib.ID, "name", lib.Name, "files", len(walked.Entries),
		"added", stats.added, "changed", stats.changed, "moved", stats.moved, "missing", stats.missing,
		"returned", stats.returned, "forgotten", stats.forgotten, "subtitles", stats.subtitles, "metadata", stats.metadata)
	if changes := stats.texts(); len(changes) > 0 {
		a.record(ctx, domain.Activity{Kind: domain.ActivityLibraryScanned, Text: domain.T("activity.scan", "library", lib.Name, changes)})
	}
	return nil
}

// texts sums up what a scan changed (added, missing...); empty if it changed nothing.
func (s scanStats) texts() []domain.Text {
	var parts []domain.Text
	add := func(n int, t domain.Text) {
		if n > 0 {
			parts = append(parts, t)
		}
	}
	add(s.added, domain.T("activity.scan.added", "count", s.added))
	add(s.changed, domain.T("activity.scan.changed", "count", s.changed))
	add(s.moved, domain.T("activity.scan.moved", "count", s.moved))
	add(s.missing, domain.T("activity.scan.missing", "count", s.missing))
	add(s.returned, domain.T("activity.scan.returned", "count", s.returned))
	add(s.forgotten, domain.T("activity.scan.forgotten", "count", s.forgotten))
	add(s.metadata, domain.T("activity.scan.metadata", "count", s.metadata))
	return parts
}

// metadataChanges compares the signature of the NFO files and images of each folder with the one
// from the previous scan. For a changed folder, the items to read again are the item of each file
// in it (movie, episode and its season) and, for a series folder, the series and its seasons (their
// posters live there). It returns the writes (new signatures, items to read again) and the number
// of items to read again. A folder under an unreachable root keeps its signature.
func (a *App) metadataChanges(ctx context.Context, lib domain.Library, walked library.Walked, unsafe []string) ([]func(q store.Q) error, int, error) {
	current := library.MetadataSignatures(walked)
	known, err := a.store.Read().MetadataDirs(ctx, lib.ID)
	if err != nil {
		return nil, 0, err
	}
	changed := map[string]bool{}
	for dir, sig := range current {
		if known[dir] != sig {
			changed[dir] = true
		}
	}
	for dir := range known {
		if _, ok := current[dir]; !ok && !library.Under(dir, unsafe) {
			changed[dir] = true
		}
	}
	if len(changed) == 0 {
		return nil, 0, nil
	}
	files, err := a.store.Read().PresentFileItems(ctx, lib.ID)
	if err != nil {
		return nil, 0, err
	}
	items, series := map[domain.ID]bool{}, map[domain.ID]bool{}
	for _, f := range files {
		if f.AlbumID != nil && f.ArtistID != nil {
			// Track: nothing to read again for the track itself (its tags are the source of truth),
			// but its album and artist read their folders again.
			albumDirs, artistDir := musicDirs(lib, f.Path)
			if slices.ContainsFunc(albumDirs, func(d string) bool { return changed[d] }) {
				items[*f.AlbumID] = true
			}
			if artistDir != "" && changed[artistDir] {
				items[*f.ArtistID] = true
			}
			continue
		}
		if changed[filepath.Dir(f.Path)] {
			items[f.ItemID] = true
			if f.SeasonID != nil {
				items[*f.SeasonID] = true
			}
		}
		if f.SeriesID != nil {
			if dir, _ := seriesDirOf(lib, f.Path); dir != "" && changed[dir] {
				series[*f.SeriesID] = true
			}
		}
	}
	var ops []func(q store.Q) error
	for dir := range changed {
		ops = append(ops, func(q store.Q) error { return q.SetMetadataDir(ctx, lib.ID, dir, current[dir]) })
	}
	for id := range series {
		ops = append(ops, func(q store.Q) error {
			seasons, err := q.SeasonIDs(ctx, id)
			if err != nil {
				return err
			}
			for _, s := range append(seasons, id) {
				if err := a.jobs.Enqueue(ctx, q, jobItemMetadata, s.String(), priorityBackground); err != nil {
					return err
				}
			}
			return nil
		})
	}
	for id := range items {
		ops = append(ops, func(q store.Q) error {
			return a.jobs.Enqueue(ctx, q, jobItemMetadata, id.String(), priorityBackground)
		})
	}
	return ops, len(items) + len(series), nil
}

// applyBatches runs the writes in batches, each batch in its own transaction.
func (a *App) applyBatches(ctx context.Context, ops []func(q store.Q) error) error {
	for start := 0; start < len(ops); start += scanBatch {
		batch := ops[start:min(start+scanBatch, len(ops))]
		if err := a.store.Write(ctx, func(q store.Q) error {
			for _, op := range batch {
				if err := op(q); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return fmt.Errorf("scan: write: %w", err)
		}
		a.jobs.Kick()
	}
	return nil
}

// relativeTo returns the library root that contains path, and the relative path (with forward
// slashes).
func relativeTo(roots []string, path string) (root, rel string, ok bool) {
	for _, r := range roots {
		if library.Under(path, []string{r}) {
			rel, err := filepath.Rel(r, path)
			if err != nil {
				continue
			}
			return r, filepath.ToSlash(rel), true
		}
	}
	return "", "", false
}
