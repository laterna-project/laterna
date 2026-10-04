package store

import (
	"context"
	"strings"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// SubtitleSet reads the extracted subtitles of a file and its fonts; ok is false if they were never
// extracted.
func (q Q) SubtitleSet(ctx context.Context, fileID domain.ID) (set domain.SubtitleSet, ok bool, err error) {
	row, err := q.q.GetSubtitleSet(ctx, fileID)
	if IsNotFound(err) {
		return set, false, nil
	}
	if err != nil {
		return set, false, err
	}
	set = domain.SubtitleSet{Fingerprint: row.Fingerprint, Sidecars: row.Sidecars, ExtractedAt: fromMillis(row.ExtractedAt)}
	subs, err := q.q.ListSubtitles(ctx, fileID)
	if err != nil {
		return set, false, err
	}
	for _, s := range subs {
		set.Subtitles = append(set.Subtitles, domain.Subtitle{
			Position: int(s.Position), StreamIndex: int(s.StreamIdx), Path: s.Path, Codec: s.Codec,
			Language: s.Language, Title: s.Title, Default: s.IsDefault == 1, Forced: s.IsForced == 1,
			HearingImpaired: s.IsHearingImpaired == 1, Formats: splitList(s.Formats, ","),
			Width: int(s.Width), Height: int(s.Height),
		})
	}
	fonts, err := q.q.ListFileFonts(ctx, fileID)
	if err != nil {
		return set, false, err
	}
	for _, f := range fonts {
		set.Fonts = append(set.Fonts, domain.Font{SHA256: f.Sha256, Names: splitList(f.Names, "\n"), Ext: f.Ext, Size: f.Size})
	}
	return set, true, nil
}

// SetSubtitleSet replaces the extracted subtitles of a file and its fonts.
func (q Q) SetSubtitleSet(ctx context.Context, fileID domain.ID, set domain.SubtitleSet) error {
	if err := q.q.UpsertSubtitleSet(ctx, sqlc.UpsertSubtitleSetParams{
		FileID: fileID, Fingerprint: set.Fingerprint, Sidecars: set.Sidecars, ExtractedAt: toMillis(set.ExtractedAt),
	}); err != nil {
		return err
	}
	if err := q.q.DeleteSubtitles(ctx, fileID); err != nil {
		return err
	}
	for _, s := range set.Subtitles {
		if err := q.q.InsertSubtitle(ctx, sqlc.InsertSubtitleParams{
			FileID: fileID, Position: int64(s.Position), StreamIdx: int64(s.StreamIndex), Path: s.Path,
			Codec: s.Codec, Language: s.Language, Title: s.Title, IsDefault: toInt(s.Default),
			IsForced: toInt(s.Forced), IsHearingImpaired: toInt(s.HearingImpaired),
			Formats: strings.Join(s.Formats, ","), Width: int64(s.Width), Height: int64(s.Height),
		}); err != nil {
			return err
		}
	}
	if err := q.q.DeleteFileFonts(ctx, fileID); err != nil {
		return err
	}
	for _, f := range set.Fonts {
		if err := q.q.InsertFont(ctx, sqlc.InsertFontParams{
			Sha256: f.SHA256, Names: strings.Join(f.Names, "\n"), Ext: f.Ext, Size: f.Size,
		}); err != nil {
			return err
		}
		if err := q.q.InsertFileFont(ctx, sqlc.InsertFileFontParams{FileID: fileID, Sha256: f.SHA256}); err != nil {
			return err
		}
	}
	return nil
}

// Font reads a font by its hash.
func (q Q) Font(ctx context.Context, sha256 string) (domain.Font, error) {
	f, err := q.q.GetFont(ctx, sha256)
	if err != nil {
		return domain.Font{}, err
	}
	return domain.Font{SHA256: f.Sha256, Names: splitList(f.Names, "\n"), Ext: f.Ext, Size: f.Size}, nil
}

// DeleteOrphanFonts forgets the fonts no file uses anymore. It returns their hashes and extensions
// so that their files can be deleted.
func (q Q) DeleteOrphanFonts(ctx context.Context) ([]domain.Font, error) {
	rows, err := q.q.DeleteOrphanFonts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Font, len(rows))
	for i, r := range rows {
		out[i] = domain.Font{SHA256: r.Sha256, Ext: r.Ext}
	}
	return out, nil
}

// SubtitleSetFiles lists the files whose subtitles have been extracted.
func (q Q) SubtitleSetFiles(ctx context.Context) ([]domain.ID, error) {
	return q.q.ListSubtitleSetFiles(ctx)
}

func splitList(s, sep string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, sep)
}
