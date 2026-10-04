package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/jobs"
	"github.com/laterna-project/laterna/internal/library"
	"github.com/laterna-project/laterna/internal/media/matroska"
	"github.com/laterna-project/laterna/internal/media/subtitles"
	"github.com/laterna-project/laterna/internal/proc"
	"github.com/laterna-project/laterna/internal/store"
)

// Subtitles: extracted at import time, served on the side, rendered by the client. Nothing is done
// at playback time, except burn-in as a last resort.
const (
	// maxFontSize: a bigger attachment is not kept as a font.
	maxFontSize = 64 << 20
	// maxSidecarSize: a bigger external text subtitle is refused (malicious file).
	maxSidecarSize = 64 << 20
	// subtitleWait caps the wait for an extraction in progress (requested with priority when a
	// playback opens).
	subtitleWait = 2 * time.Minute
)

// subtitlesDir is the folder of the extracted subtitles of a file.
func (a *App) subtitlesDir(fileID domain.ID) string {
	return filepath.Join(a.metadataDir, "subtitles", fileID.String())
}

// subtitlePath is the path of an extracted subtitle in a given format.
func (a *App) subtitlePath(fileID domain.ID, position int, format string) string {
	return filepath.Join(a.subtitlesDir(fileID), strconv.Itoa(position)+"."+format)
}

// fontPath is the path of a font, stored by hash.
func (a *App) fontPath(f domain.Font) string {
	return filepath.Join(a.metadataDir, "fonts", f.SHA256[:2], f.SHA256+f.Ext)
}

// sidecarsOf finds the external subtitles of a file and their signature (library.Near: only the
// file's folder).
func sidecarsOf(f domain.MediaFile) ([]library.Sidecar, string, error) {
	near, err := library.Near(filepath.Dir(f.Path))
	if err != nil {
		return nil, "", err
	}
	i := slices.IndexFunc(near.Entries, func(e library.Entry) bool { return e.Path == f.Path })
	if i < 0 {
		return nil, "", nil
	}
	subs := library.Sidecars(near)[f.Path]
	return subs, library.Signature(near.Entries[i], subs), nil
}

// plannedSubtitles lists the subtitles of a file, streams then external files, with the formats the
// extraction must produce.
func plannedSubtitles(f domain.MediaFile, sidecars []library.Sidecar) []domain.Subtitle {
	var out []domain.Subtitle
	for _, st := range f.Info.Streams {
		if st.Kind != domain.StreamSubtitle {
			continue
		}
		out = append(out, domain.Subtitle{
			Position: len(out), StreamIndex: st.Index, Codec: st.Codec, Language: st.Language, Title: st.Title,
			Default: st.Default, Forced: st.Forced, HearingImpaired: st.HearingImpaired,
			Formats: subtitles.Formats(st.Codec), Width: st.Width, Height: st.Height,
		})
	}
	for _, s := range sidecars {
		codec := sidecarCodec(s.Rel)
		out = append(out, domain.Subtitle{
			Position: len(out), StreamIndex: -1, Path: s.Path, Codec: codec, Language: s.Language,
			Title: strings.ToValidUTF8(s.Title, "?"), Default: s.Default, Forced: s.Forced,
			HearingImpaired: s.HearingImpaired, Formats: subtitles.Formats(codec),
		})
	}
	return out
}

// sidecarCodec is the codec of an external subtitle, from its extension.
func sidecarCodec(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".ass":
		return "ass"
	case ".ssa":
		return "ssa"
	case ".srt":
		return "subrip"
	case ".vtt":
		return "webvtt"
	case ".sub":
		return "microdvd"
	case ".sup":
		return "hdmv_pgs_subtitle"
	case ".idx":
		return "dvd_subtitle"
	}
	return ""
}

// extractSubtitles is the file.subtitles job: every subtitle stream of the file (in a single read),
// its external files and its fonts, ready before the first playback. Nothing is redone as long as
// neither the file nor its external subtitles change.
func (a *App) extractSubtitles(ctx context.Context, target string) error {
	id, err := domain.ParseID(target)
	if err != nil {
		return jobs.Permanent(err)
	}
	f, err := a.store.Read().File(ctx, id)
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if f.MissingSince != nil || f.AnalyzedAt == nil {
		return nil // the analysis will ask for the extraction again
	}
	sidecars, sig, err := sidecarsOf(f)
	if err != nil {
		return err
	}
	if set, ok, err := a.store.Read().SubtitleSet(ctx, f.ID); err != nil || (ok && set.Fingerprint == f.Fingerprint && set.Sidecars == sig) {
		return err
	}
	start := time.Now()
	set, err := a.extract(ctx, f, sidecars)
	if err != nil {
		return err
	}
	set.Fingerprint, set.Sidecars, set.ExtractedAt = f.Fingerprint, sig, a.now()
	if err := a.store.Write(ctx, func(q store.Q) error { return q.SetSubtitleSet(ctx, f.ID, set) }); err != nil {
		return err
	}
	a.log.DebugContext(ctx, "subtitles extracted", "path", f.Path, "subtitles", len(set.Subtitles),
		"fonts", len(set.Fonts), "duration", time.Since(start).Round(time.Millisecond))
	return nil
}

// extract produces the subtitles and fonts of a file in a temporary folder that then replaces the
// old one (never a half-written extraction).
func (a *App) extract(ctx context.Context, f domain.MediaFile, sidecars []library.Sidecar) (domain.SubtitleSet, error) {
	var set domain.SubtitleSet
	base := filepath.Join(a.metadataDir, "subtitles")
	if err := os.MkdirAll(base, 0o750); err != nil {
		return set, err
	}
	tmp, err := os.MkdirTemp(base, f.ID.String()+".tmp-")
	if err != nil {
		return set, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	subs := plannedSubtitles(f, sidecars)
	if err := a.extractTracks(ctx, f, subs, tmp); err != nil {
		return set, err
	}
	for _, s := range subs {
		if !s.External() || len(s.Formats) == 0 {
			continue
		}
		if err := a.convertSidecar(ctx, s, tmp); err != nil {
			if ctx.Err() != nil {
				return set, ctx.Err()
			}
			a.log.WarnContext(ctx, "unreadable external subtitle", "path", s.Path, "err", err)
		}
	}
	for i := range subs {
		s := &subs[i]
		name := filepath.Join(tmp, strconv.Itoa(s.Position))
		if slices.Contains(s.Formats, subtitles.ASS) {
			if b, err := os.ReadFile(name + ".ass"); err == nil {
				//nolint:gosec // temporary extraction folder, the name is the subtitle's position
				if err := os.WriteFile(name+".vtt", subtitles.ASSToVTT(b), 0o600); err != nil {
					return set, err
				}
			}
		}
		// Formats really produced (an unreadable stream has none).
		s.Formats = slices.DeleteFunc(s.Formats, func(format string) bool {
			st, err := os.Stat(name + "." + format)
			return err != nil || st.Size() == 0
		})
		if s.Image() && len(s.Formats) > 0 && (s.Width == 0 || s.Height == 0) {
			// Canvas size of the subtitles (needed for burn-in): the extracted file gives it from
			// its very start, the media file only at the first line.
			if info, err := a.prober.Probe(ctx, name+"."+s.Formats[0]); err == nil && len(info.Streams) > 0 {
				s.Width, s.Height = info.Streams[0].Width, info.Streams[0].Height
			}
		}
	}
	set.Subtitles = subs
	if set.Fonts, err = a.extractFonts(f); err != nil {
		a.log.WarnContext(ctx, "unreadable attached fonts", "path", f.Path, "err", err)
	}
	if err := replaceDir(tmp, a.subtitlesDir(f.ID)); err != nil {
		return set, err
	}
	return set, nil
}

// extractTracks extracts every subtitle stream of the file in a single read. If FFmpeg fails
// (damaged stream) it goes stream by stream, to save the others.
func (a *App) extractTracks(ctx context.Context, f domain.MediaFile, subs []domain.Subtitle, dir string) error {
	var tracks []subtitles.Track
	for _, s := range subs {
		if !s.External() && len(s.Formats) > 0 {
			tracks = append(tracks, subtitles.Track{Index: s.StreamIndex, Codec: s.Codec, Name: strconv.Itoa(s.Position)})
		}
	}
	if len(tracks) == 0 {
		return nil
	}
	err := a.runFFmpeg(ctx, subtitles.ExtractArgs(f.Path, tracks, dir))
	if err == nil || ctx.Err() != nil {
		return ctx.Err()
	}
	if _, statErr := os.Stat(f.Path); statErr != nil {
		return err // file no longer reachable (share disconnected): the job will be retried
	}
	a.log.WarnContext(ctx, "single-pass subtitle extraction failed: going track by track", "path", f.Path, "err", err)
	for _, t := range tracks {
		if err := a.runFFmpeg(ctx, subtitles.ExtractArgs(f.Path, []subtitles.Track{t}, dir)); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			a.log.WarnContext(ctx, "unreadable subtitle track", "path", f.Path, "stream", t.Index, "err", err)
		}
	}
	return nil
}

// convertSidecar stores an external subtitle in dir: ASS as UTF-8, text as WebVTT, PGS as is,
// VobSub as Matroska.
func (a *App) convertSidecar(ctx context.Context, s domain.Subtitle, dir string) error {
	name := filepath.Join(dir, strconv.Itoa(s.Position))
	switch s.Codec {
	case "hdmv_pgs_subtitle":
		return copyFile(s.Path, name+".sup")
	case "dvd_subtitle":
		return a.runFFmpeg(ctx, subtitles.ConvertArgs(s.Path, name+".mks"))
	}
	b, err := readBounded(s.Path, maxSidecarSize)
	if err != nil {
		return err
	}
	b = subtitles.ToUTF8(b)
	if s.Codec == "ass" || s.Codec == "ssa" {
		return os.WriteFile(name+".ass", b, 0o600)
	}
	// FFmpeg reads text as UTF-8: give it the converted copy.
	src := name + ".source" + strings.ToLower(filepath.Ext(s.Path))
	if err := os.WriteFile(src, b, 0o600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(src) }()
	return a.runFFmpeg(ctx, subtitles.ConvertArgs(src, name+".vtt"))
}

// extractFonts stores the attached fonts of a Matroska file by hash (a font shared by a whole
// series is written once) and returns their names.
func (a *App) extractFonts(f domain.MediaFile) ([]domain.Font, error) {
	m, err := matroska.Open(f.Path)
	if errors.Is(err, matroska.ErrNotMatroska) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = m.Close() }()
	atts, err := m.Attachments()
	if err != nil {
		return nil, err
	}
	var fonts []domain.Font
	for _, att := range atts {
		if !subtitles.IsFont(att.Name, att.MimeType) || att.Size > maxFontSize {
			continue
		}
		data, err := io.ReadAll(m.Data(att))
		if err != nil {
			return fonts, err
		}
		sum := sha256.Sum256(data)
		font := domain.Font{SHA256: hex.EncodeToString(sum[:]), Ext: subtitles.FontExt(att.Name), Size: int64(len(data))}
		if slices.ContainsFunc(fonts, func(x domain.Font) bool { return x.SHA256 == font.SHA256 }) {
			continue
		}
		names, ok := subtitles.FontNames(data)
		if !ok {
			// A font we cannot read (libass may still manage): use its file name.
			names = []string{strings.ToLower(strings.TrimSuffix(att.Name, path.Ext(att.Name)))}
		}
		for i := range names {
			names[i] = strings.ToValidUTF8(names[i], "?")
		}
		font.Names = names
		p := a.fontPath(font)
		if _, err := os.Stat(p); err != nil {
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				return fonts, err
			}
			if err := writeAtomic(p, data); err != nil {
				return fonts, err
			}
		}
		fonts = append(fonts, font)
	}
	return fonts, nil
}

// purgeSubtitles is the subtitles.purge job: fonts no file uses anymore, subtitle folders of
// forgotten files, leftovers of interrupted extractions. It runs in the same class as extraction,
// so never at the same time.
func (a *App) purgeSubtitles(ctx context.Context, _ string) error {
	var orphans []domain.Font
	if err := a.store.Write(ctx, func(q store.Q) error {
		var err error
		orphans, err = q.DeleteOrphanFonts(ctx)
		return err
	}); err != nil {
		return err
	}
	for _, f := range orphans {
		if len(f.SHA256) >= 2 {
			_ = os.Remove(a.fontPath(f))
		}
	}
	ids, err := a.store.Read().SubtitleSetFiles(ctx)
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, id := range ids {
		keep[id.String()] = true
	}
	base := filepath.Join(a.metadataDir, "subtitles")
	entries, err := os.ReadDir(base)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	removed := 0
	for _, e := range entries {
		if !keep[e.Name()] {
			if err := os.RemoveAll(filepath.Join(base, e.Name())); err == nil {
				removed++
			}
		}
	}
	if len(orphans)+removed > 0 {
		a.log.InfoContext(ctx, "subtitles: purge", "fonts", len(orphans), "dirs", removed)
	}
	return nil
}

// runFFmpeg runs FFmpeg to completion (extraction, conversion).
func (a *App) runFFmpeg(ctx context.Context, args []string) error {
	bin := a.ffmpeg
	if bin == "" {
		bin = "ffmpeg"
	}
	cmd := proc.Command(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 1000 {
			msg = msg[len(msg)-1000:]
		}
		return fmt.Errorf("ffmpeg: %w: %s", err, msg)
	}
	return nil
}

// replaceDir replaces dst with src (by renaming: dst is never half written).
func replaceDir(src, dst string) error {
	old := dst + ".old"
	_ = os.RemoveAll(old)
	if err := os.Rename(dst, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(old)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// readBounded reads a file of at most limit bytes.
func readBounded(p string, limit int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: more than %d bytes", filepath.Base(p), limit)
	}
	return b, nil
}

// subtitlesOf returns the subtitles of a file and its fonts. If they are not extracted yet, the
// extraction jumps the queue and the planned list is returned (ready false): the subtitle URLs will
// wait for the extraction.
func (a *App) subtitlesOf(ctx context.Context, f domain.MediaFile) (subs []domain.Subtitle, fonts []domain.Font, ready bool, err error) {
	set, ok, err := a.store.Read().SubtitleSet(ctx, f.ID)
	if err != nil {
		return nil, nil, false, err
	}
	if ok && set.Fingerprint == f.Fingerprint {
		return set.Subtitles, set.Fonts, true, nil
	}
	if err := a.store.Write(ctx, func(q store.Q) error {
		return a.jobs.Enqueue(ctx, q, jobExtractSubtitles, f.ID.String(), priorityUser)
	}); err != nil {
		return nil, nil, false, err
	}
	a.jobs.Kick()
	sidecars, _, err := sidecarsOf(f)
	if err != nil {
		a.log.WarnContext(ctx, "unreadable external subtitles", "path", f.Path, "err", err)
	}
	return plannedSubtitles(f, sidecars), nil, false, nil
}

// awaitSubtitles waits for the subtitles of a file to be extracted (for its fingerprint).
func (a *App) awaitSubtitles(ctx context.Context, f domain.MediaFile, limit time.Duration) (domain.SubtitleSet, error) {
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		set, ok, err := a.store.Read().SubtitleSet(ctx, f.ID)
		if err != nil || (ok && set.Fingerprint == f.Fingerprint) {
			return set, err
		}
		select {
		case <-ctx.Done():
			return set, ctx.Err()
		case <-deadline.C:
			return set, domain.Precondition("subtitle.not_ready")
		case <-tick.C:
		}
	}
}

// PlaybackSubtitles reads again the subtitles and fonts of a playback of the profile. wait waits
// for the extraction to finish (it was requested with priority when the playback opened).
func (a *App) PlaybackSubtitles(ctx context.Context, p domain.Principal, id domain.ID, wait bool) (PlayInfo, error) {
	s, err := a.ownSession(p, id)
	if err != nil {
		return PlayInfo{}, err
	}
	info := PlayInfo{SessionID: s.id, Token: s.token, File: s.file}
	if wait {
		set, err := a.awaitSubtitles(ctx, s.file, subtitleWait)
		if err != nil {
			return PlayInfo{}, err
		}
		info.Subtitles, info.Fonts, info.SubtitlesReady = set.Subtitles, set.Fonts, true
		return info, nil
	}
	info.Subtitles, info.Fonts, info.SubtitlesReady, err = a.subtitlesOf(ctx, s.file)
	return info, err
}

// SubtitleFile returns the path of a subtitle of a playback (position, format), waiting for its
// extraction if it is under way.
func (a *App) SubtitleFile(ctx context.Context, id domain.ID, token string, position int, format string) (string, error) {
	s, err := a.session(id, token)
	if err != nil {
		return "", err
	}
	set, err := a.awaitSubtitles(ctx, s.file, subtitleWait)
	if err != nil {
		return "", err
	}
	if position < 0 || position >= len(set.Subtitles) || !slices.Contains(set.Subtitles[position].Formats, format) {
		return "", domain.NotFound("subtitle.not_found")
	}
	return a.subtitlePath(s.file.ID, position, format), nil
}

// FontFile returns the path of a font from its hash (the URL needs no authentication, like images:
// the hash cannot be guessed and a font is nothing private).
func (a *App) FontFile(ctx context.Context, sha string) (domain.Font, string, error) {
	if len(sha) != 64 || strings.Trim(sha, "0123456789abcdef") != "" {
		return domain.Font{}, "", domain.NotFound("subtitle.font_not_found")
	}
	f, err := a.store.Read().Font(ctx, sha)
	if store.IsNotFound(err) {
		return domain.Font{}, "", domain.NotFound("subtitle.font_not_found")
	}
	if err != nil {
		return domain.Font{}, "", err
	}
	return f, a.fontPath(f), nil
}
