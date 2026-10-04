package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/jobs"
	"github.com/laterna-project/laterna/internal/media/fingerprint"
	"github.com/laterna-project/laterna/internal/proc"
	"github.com/laterna-project/laterna/internal/segments"
	"github.com/laterna-project/laterna/internal/store"
)

// Intros, credits and other skippable segments. The named chapters of a file give them at analysis
// time. For an episode, the file.segments job then compares its audio with that of the other
// episodes of the season: what the starts have in common is the intro, what the ends have in common
// is the credits. Fingerprints are cached, so adding an episode only computes one.

// maxNeighbors is how many episodes are tried for each segment (the first episode of a season often
// has no intro, a special no intro or credits at all).
const maxNeighbors = 4

// setChapterSegments stores the segments named by the chapters of a file.
func setChapterSegments(ctx context.Context, q store.Q, f domain.MediaFile) error {
	return q.SetChapterSegments(ctx, f.ID, segments.FromChapters(f.Info.Chapters, f.Info.Duration))
}

// detectSegments looks for the segments of a file: those from its chapters, then, for an episode
// and if the setting allows it, those it shares with the other episodes of the season.
func (a *App) detectSegments(ctx context.Context, target string) error {
	id, err := domain.ParseID(target)
	if err != nil {
		return jobs.Permanent(err)
	}
	read := a.store.Read()
	f, err := read.File(ctx, id)
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if f.MissingSince != nil || f.AnalyzedAt == nil {
		return nil
	}
	found := map[domain.ID][]domain.Segment{}
	var changed []domain.ID
	audio := false
	if a.Settings().DetectSegments && hasVideo(f.Info) {
		ep, err := read.EpisodeOfFile(ctx, id)
		switch {
		case store.IsNotFound(err): // not an episode: chapters only
		case err != nil:
			return err
		default:
			if found, changed, err = a.matchNeighbors(ctx, f, ep); err != nil {
				return err
			}
			audio = true
		}
	}
	if err := a.store.Write(ctx, func(q store.Q) error {
		if err := setChapterSegments(ctx, q, f); err != nil {
			return err
		}
		for fileID, segs := range found {
			for _, s := range segs {
				if err := q.SetAudioSegment(ctx, fileID, s); err != nil {
					return err
				}
			}
		}
		return q.SetSegmentScan(ctx, f.ID, f.Fingerprint, audio, a.now())
	}); err != nil {
		return err
	}
	if len(changed) > 0 {
		a.itemsChanged(f.LibraryID, changed...)
	}
	return nil
}

// matchNeighbors compares the start and the end of an episode with those of its neighbors in the
// season, for each segment its chapters do not give. It returns the segments found, by file (the
// episode and the neighbor that matches it), and the episodes concerned.
func (a *App) matchNeighbors(ctx context.Context, f domain.MediaFile, ep store.FileEpisode) (map[domain.ID][]domain.Segment, []domain.ID, error) {
	found := map[domain.ID][]domain.Segment{}
	have := segments.FromChapters(f.Info.Chapters, f.Info.Duration)
	var kinds []domain.SegmentKind
	for _, k := range []domain.SegmentKind{domain.SegmentIntro, domain.SegmentCredits} {
		if !hasSegment(have, k) {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) == 0 || f.Info.Duration <= 0 {
		return found, nil, nil
	}
	neighbors, err := a.neighbors(ctx, ep)
	if err != nil || len(neighbors) == 0 {
		return found, nil, err
	}
	stream, lang, ok := audioStream(f.Info, "")
	if !ok {
		return found, nil, nil
	}
	var changed []domain.ID
	for _, kind := range kinds {
		mine, err := a.audioPrint(ctx, f, stream, kind)
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if err != nil {
			// Unreadable audio: nothing to find, no point in trying again at every scan.
			a.log.WarnContext(ctx, "segments: cannot compute audio fingerprint", "path", f.Path, "err", err)
			return found, changed, nil
		}
		// The episode keeps the longest match found with its neighbors: a neighbor whose intro
		// changed (new song) only shares the beginning. The match also serves neighbors that have
		// none yet: the one that was processed before them had nobody to compare with.
		var best *domain.Segment
		for _, n := range neighbors {
			nStream, _, ok := audioStream(n.file.Info, lang)
			if !ok {
				continue
			}
			theirs, err := a.audioPrint(ctx, n.file, nStream, kind)
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			if err != nil {
				a.log.WarnContext(ctx, "segments: cannot compute audio fingerprint", "path", n.file.Path, "err", err)
				continue
			}
			ra, rb, ok := fingerprint.Common(mine, theirs, a.segmentMin)
			if !ok || !segments.FromAudio(kind, ra.Start, ra.End, a.segmentMin) || !segments.FromAudio(kind, rb.Start, rb.End, a.segmentMin) {
				continue
			}
			if best == nil || ra.Duration() > best.End-best.Start {
				best = &domain.Segment{Kind: kind, Start: ra.Start, End: ra.End, Source: domain.SegmentFromAudio}
			}
			if !hasSegment(n.file.Segments, kind) {
				found[n.file.ID] = append(found[n.file.ID], domain.Segment{Kind: kind, Start: rb.Start, End: rb.End, Source: domain.SegmentFromAudio})
				changed = append(changed, n.item)
			}
		}
		if best != nil {
			found[f.ID] = append(found[f.ID], *best)
			changed = append(changed, ep.ItemID)
		}
	}
	return found, changed, nil
}

type neighbor struct {
	item domain.ID
	file domain.MediaFile
}

// neighbors returns the other episodes of the season, nearest first (the next one, the previous
// one, then further and further away), maxNeighbors at most: one file per episode, the first.
func (a *App) neighbors(ctx context.Context, ep store.FileEpisode) ([]neighbor, error) {
	read := a.store.Read()
	files, err := read.SeasonFiles(ctx, ep.SeasonID)
	if err != nil {
		return nil, err
	}
	var after, before []store.SeasonFile
	for i, sf := range files {
		if i > 0 && files[i-1].Episode == sf.Episode {
			continue // another version of the same episode
		}
		switch {
		case sf.Episode > ep.Number:
			after = append(after, sf)
		case sf.Episode < ep.Number:
			before = append([]store.SeasonFile{sf}, before...)
		}
	}
	var out []neighbor
	for i := 0; len(out) < maxNeighbors && (i < len(after) || i < len(before)); i++ {
		for _, side := range [][]store.SeasonFile{after, before} {
			if i >= len(side) || len(out) >= maxNeighbors {
				continue
			}
			f, err := read.File(ctx, side[i].FileID)
			if store.IsNotFound(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			out = append(out, neighbor{item: side[i].ItemID, file: f})
		}
	}
	return out, nil
}

// audioStream picks the audio stream to compare: the one in the wanted language if there is one,
// otherwise the default stream, otherwise the first. It returns its position and language.
func audioStream(info domain.MediaInfo, lang string) (int, string, bool) {
	var first, def *domain.Stream
	for i := range info.Streams {
		s := &info.Streams[i]
		if s.Kind != domain.StreamAudio {
			continue
		}
		if lang != "" && s.Language == lang {
			return s.Index, s.Language, true
		}
		if first == nil {
			first = s
		}
		if s.Default && def == nil {
			def = s
		}
	}
	if def == nil {
		def = first
	}
	if def == nil {
		return 0, "", false
	}
	return def.Index, def.Language, true
}

func hasSegment(segs []domain.Segment, kind domain.SegmentKind) bool {
	for _, s := range segs {
		if s.Kind == kind {
			return true
		}
	}
	return false
}

// audioPrint returns the fingerprint of the start (SegmentIntro) or the end (SegmentCredits) of a
// file, from the cache or computed.
func (a *App) audioPrint(ctx context.Context, f domain.MediaFile, stream int, kind domain.SegmentKind) (fingerprint.Print, error) {
	w := segments.IntroWindow(f.Info.Duration)
	if kind == domain.SegmentCredits {
		w = segments.CreditsWindow(f.Info.Duration)
	}
	path := a.printPath(f, stream, kind)
	if data, err := os.ReadFile(path); err == nil {
		var p fingerprint.Print
		if p.UnmarshalBinary(data) == nil && p.Start == w.Start.Truncate(time.Millisecond) {
			return p, nil
		}
	}
	bin := a.ffmpeg
	if bin == "" {
		bin = "ffmpeg"
	}
	cmd := proc.Command(ctx, bin, fingerprint.Args(f.Path, stream, w.Start, w.Length)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return fingerprint.Print{}, err
	}
	if err := cmd.Start(); err != nil {
		return fingerprint.Print{}, err
	}
	p, computeErr := fingerprint.Compute(out, w.Start)
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return fingerprint.Print{}, ctx.Err()
		}
		return fingerprint.Print{}, fmt.Errorf("FFmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if computeErr != nil {
		return fingerprint.Print{}, computeErr
	}
	if data, err := p.MarshalBinary(); err == nil {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err == nil {
			_ = writeAtomic(path, data)
		}
	}
	return p, nil
}

// printPath is where the fingerprints of a file are cached, named after its content (file
// fingerprint), the part and the stream.
func (a *App) printPath(f domain.MediaFile, stream int, kind domain.SegmentKind) string {
	id := f.ID.String()
	return filepath.Join(a.cacheDir, "fingerprints", id[:2], id, printPrefix(f.Fingerprint)+string(kind)+"-"+strconv.Itoa(stream)+".lfp")
}

func printPrefix(fileFingerprint string) string {
	return fileFingerprint[:min(16, len(fileFingerprint))] + "-"
}

// purgePrints deletes the fingerprints of files that were forgotten or whose content changed.
func (a *App) purgePrints(ctx context.Context) error {
	root := filepath.Join(a.cacheDir, "fingerprints")
	shards, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	read := a.store.Read()
	for _, shard := range shards {
		dirs, err := os.ReadDir(filepath.Join(root, shard.Name()))
		if err != nil {
			continue
		}
		for _, d := range dirs {
			dir := filepath.Join(root, shard.Name(), d.Name())
			id, err := domain.ParseID(d.Name())
			if err != nil {
				continue
			}
			f, err := read.File(ctx, id)
			if store.IsNotFound(err) {
				_ = os.RemoveAll(dir)
				continue
			}
			if err != nil {
				return err
			}
			prints, _ := os.ReadDir(dir)
			for _, p := range prints {
				if !strings.HasPrefix(p.Name(), printPrefix(f.Fingerprint)) {
					_ = os.Remove(filepath.Join(dir, p.Name()))
				}
			}
		}
	}
	return nil
}
