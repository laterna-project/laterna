package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Themes. Tokens are stored as JSON; the logo and the background are images.

// UpsertBuiltinTheme writes a built-in theme (at startup). Nothing changes if it is up to date.
func (q Q) UpsertBuiltinTheme(ctx context.Context, t domain.Theme, now time.Time) error {
	tokens, err := json.Marshal(t.Tokens)
	if err != nil {
		return err
	}
	return translate(q.q.UpsertBuiltinTheme(ctx, sqlc.UpsertBuiltinThemeParams{
		ID: t.ID, Name: t.Name, NameKey: NameKey(t.Name), Tokens: string(tokens), CreatedAt: toMillis(now), UpdatedAt: toMillis(now),
	}))
}

// CreateTheme stores a theme; ErrDuplicate if the name is taken.
func (q Q) CreateTheme(ctx context.Context, t domain.Theme) error {
	tokens, err := json.Marshal(t.Tokens)
	if err != nil {
		return err
	}
	return translate(q.q.InsertTheme(ctx, sqlc.InsertThemeParams{
		ID: t.ID, Name: t.Name, NameKey: NameKey(t.Name), Tokens: string(tokens),
		CreatedAt: toMillis(t.CreatedAt), UpdatedAt: toMillis(t.UpdatedAt),
	}))
}

// UpdateTheme rewrites the name and tokens of a theme (never a built-in one).
func (q Q) UpdateTheme(ctx context.Context, t domain.Theme) error {
	tokens, err := json.Marshal(t.Tokens)
	if err != nil {
		return err
	}
	return translate(q.q.UpdateTheme(ctx, sqlc.UpdateThemeParams{
		Name: t.Name, NameKey: NameKey(t.Name), Tokens: string(tokens), UpdatedAt: toMillis(t.UpdatedAt), ID: t.ID,
	}))
}

// DeleteTheme deletes a theme (never a built-in one); false if it did not exist. Its images go with
// it, and profiles that had picked it go back to the server theme.
func (q Q) DeleteTheme(ctx context.Context, id domain.ID) (bool, error) {
	n, err := q.q.DeleteTheme(ctx, id)
	return n > 0, err
}

// Theme reads a theme and its images.
func (q Q) Theme(ctx context.Context, id domain.ID) (domain.Theme, error) {
	row, err := q.q.GetTheme(ctx, id)
	if err != nil {
		return domain.Theme{}, err
	}
	t, err := themeFromRow(row)
	if err != nil {
		return domain.Theme{}, err
	}
	for _, kind := range []domain.ImageKind{domain.ImageLogo, domain.ImageBackdrop} {
		img, err := q.q.GetThemeImage(ctx, sqlc.GetThemeImageParams{ThemeID: &id, Kind: string(kind)})
		if IsNotFound(err) {
			continue
		}
		if err != nil {
			return domain.Theme{}, err
		}
		attachThemeImage(&t, imageFromRow(img))
	}
	return t, nil
}

// Themes lists the themes (built-in first, then by name) with their images.
func (q Q) Themes(ctx context.Context) ([]domain.Theme, error) {
	rows, err := q.q.ListThemes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Theme, 0, len(rows))
	byID := map[domain.ID]int{}
	for _, r := range rows {
		t, err := themeFromRow(r)
		if err != nil {
			return nil, err
		}
		byID[t.ID] = len(out)
		out = append(out, t)
	}
	images, err := q.q.ListThemeImages(ctx)
	if err != nil {
		return nil, err
	}
	for _, img := range images {
		if img.ThemeID == nil {
			continue
		}
		if i, ok := byID[*img.ThemeID]; ok {
			attachThemeImage(&out[i], imageFromRow(img))
		}
	}
	return out, nil
}

// ThemeImage reads the logo or the background of a theme.
func (q Q) ThemeImage(ctx context.Context, themeID domain.ID, kind domain.ImageKind) (domain.Image, error) {
	row, err := q.q.GetThemeImage(ctx, sqlc.GetThemeImageParams{ThemeID: &themeID, Kind: string(kind)})
	if err != nil {
		return domain.Image{}, err
	}
	return imageFromRow(row), nil
}

// AddThemeImage stores the logo or the background of a theme, already analyzed.
func (q Q) AddThemeImage(ctx context.Context, img domain.Image) error {
	return q.q.InsertThemeImage(ctx, sqlc.InsertThemeImageParams{
		ID: img.ID, ThemeID: img.ThemeID, Kind: string(img.Kind), Path: img.Path, Width: int64(img.Width), Height: int64(img.Height),
		Blurhash: img.BlurHash, Hash: img.Hash, UpdatedAt: toMillis(img.UpdatedAt),
	})
}

// SetProfileTheme sets the theme of a profile (nil for the server's) and its mode.
func (q Q) SetProfileTheme(ctx context.Context, profileID domain.ID, themeID *domain.ID, mode domain.ThemeMode, now time.Time) error {
	return q.q.SetProfileTheme(ctx, sqlc.SetProfileThemeParams{ThemeID: themeID, ThemeMode: string(mode), UpdatedAt: toMillis(now), ID: profileID})
}

func themeFromRow(r sqlc.Theme) (domain.Theme, error) {
	t := domain.Theme{ID: r.ID, Name: r.Name, BuiltIn: r.BuiltIn == 1, CreatedAt: fromMillis(r.CreatedAt), UpdatedAt: fromMillis(r.UpdatedAt)}
	if err := json.Unmarshal([]byte(r.Tokens), &t.Tokens); err != nil {
		return domain.Theme{}, fmt.Errorf("unreadable tokens of theme %s: %w", r.ID, err)
	}
	return t, nil
}

func attachThemeImage(t *domain.Theme, img domain.Image) {
	switch img.Kind { //nolint:exhaustive // a theme only has a logo and a background
	case domain.ImageLogo:
		t.Logo = &img
	case domain.ImageBackdrop:
		t.Background = &img
	}
}
