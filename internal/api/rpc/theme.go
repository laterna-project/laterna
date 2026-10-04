package rpc

import (
	"context"
	"regexp"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// ThemeService implements laterna.v1.ThemeService.
type ThemeService struct {
	app *app.App
}

var (
	themeDensities = map[domain.ThemeDensity]laternav1.ThemeDensity{
		domain.DensityCompact:     laternav1.ThemeDensity_THEME_DENSITY_COMPACT,
		domain.DensityComfortable: laternav1.ThemeDensity_THEME_DENSITY_COMFORTABLE,
		domain.DensitySpacious:    laternav1.ThemeDensity_THEME_DENSITY_SPACIOUS,
	}
	themeFonts = map[domain.ThemeFont]laternav1.ThemeFont{
		domain.FontSystem:   laternav1.ThemeFont_THEME_FONT_SYSTEM,
		domain.FontInter:    laternav1.ThemeFont_THEME_FONT_INTER,
		domain.FontAtkinson: laternav1.ThemeFont_THEME_FONT_ATKINSON,
		domain.FontLexend:   laternav1.ThemeFont_THEME_FONT_LEXEND,
		domain.FontSerif:    laternav1.ThemeFont_THEME_FONT_SERIF,
	}
	themeModes = map[domain.ThemeMode]laternav1.ThemeMode{
		domain.ThemeAuto:  laternav1.ThemeMode_THEME_MODE_AUTO,
		domain.ThemeDark:  laternav1.ThemeMode_THEME_MODE_DARK,
		domain.ThemeLight: laternav1.ThemeMode_THEME_MODE_LIGHT,
	}
)

// reverse inverts a conversion map.
func reverse[K, V comparable](m map[K]V) map[V]K {
	out := make(map[V]K, len(m))
	for k, v := range m {
		out[v] = k
	}
	return out
}

var (
	densitiesOf = reverse(themeDensities)
	fontsOf     = reverse(themeFonts)
	modesOf     = reverse(themeModes)
)

// GetServerTheme returns the server's theme.
func (s *ThemeService) GetServerTheme(ctx context.Context, _ *connect.Request[laternav1.GetServerThemeRequest]) (*connect.Response[laternav1.GetServerThemeResponse], error) {
	t, err := s.app.DefaultTheme(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.GetServerThemeResponse{Theme: themeMsg(ctx, t, true)}), nil
}

// GetMyTheme returns the theme of the picked profile.
func (s *ThemeService) GetMyTheme(ctx context.Context, _ *connect.Request[laternav1.GetMyThemeRequest]) (*connect.Response[laternav1.GetMyThemeResponse], error) {
	p := principal(ctx)
	t, mode, err := s.app.ProfileTheme(ctx, p)
	if err != nil {
		return nil, err
	}
	def, err := s.app.DefaultThemeID(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.GetMyThemeResponse{
		Theme: themeMsg(ctx, t, t.ID == def), Mode: themeModes[mode], FollowsServer: p.Profile == nil || p.Profile.ThemeID == nil,
	}), nil
}

// SetMyTheme sets the theme of the picked profile.
func (s *ThemeService) SetMyTheme(ctx context.Context, req *connect.Request[laternav1.SetMyThemeRequest]) (*connect.Response[laternav1.SetMyThemeResponse], error) {
	id, err := parseOptionalID(req.Msg.GetThemeId(), "theme_id")
	if err != nil {
		return nil, err
	}
	mode := domain.ThemeAuto
	if m := req.Msg.GetMode(); m != laternav1.ThemeMode_THEME_MODE_UNSPECIFIED {
		var ok bool
		if mode, ok = modesOf[m]; !ok {
			return nil, domain.Invalid("theme.unknown_mode", "mode", m)
		}
	}
	if err := s.app.SetProfileTheme(ctx, principal(ctx), id, mode); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.SetMyThemeResponse{}), nil
}

// ListThemes lists the themes.
func (s *ThemeService) ListThemes(ctx context.Context, _ *connect.Request[laternav1.ListThemesRequest]) (*connect.Response[laternav1.ListThemesResponse], error) {
	themes, err := s.app.Themes(ctx)
	if err != nil {
		return nil, err
	}
	def, err := s.app.DefaultThemeID(ctx)
	if err != nil {
		return nil, err
	}
	resp := &laternav1.ListThemesResponse{}
	for _, t := range themes {
		resp.Themes = append(resp.Themes, themeMsg(ctx, t, t.ID == def))
	}
	return connect.NewResponse(resp), nil
}

// CreateTheme creates a theme.
func (s *ThemeService) CreateTheme(ctx context.Context, req *connect.Request[laternav1.CreateThemeRequest]) (*connect.Response[laternav1.CreateThemeResponse], error) {
	t, err := s.app.CreateTheme(ctx, principal(ctx), req.Msg.GetName(), tokensOf(req.Msg.GetTokens()))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.CreateThemeResponse{Theme: themeMsg(ctx, t, false)}), nil
}

// UpdateTheme replaces a theme.
func (s *ThemeService) UpdateTheme(ctx context.Context, req *connect.Request[laternav1.UpdateThemeRequest]) (*connect.Response[laternav1.UpdateThemeResponse], error) {
	id, err := parseID(req.Msg.GetThemeId(), "theme_id")
	if err != nil {
		return nil, err
	}
	t, err := s.app.UpdateTheme(ctx, principal(ctx), id, req.Msg.GetName(), tokensOf(req.Msg.GetTokens()))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.UpdateThemeResponse{Theme: s.withDefault(ctx, t)}), nil
}

// DeleteTheme deletes a theme.
func (s *ThemeService) DeleteTheme(ctx context.Context, req *connect.Request[laternav1.DeleteThemeRequest]) (*connect.Response[laternav1.DeleteThemeResponse], error) {
	id, err := parseID(req.Msg.GetThemeId(), "theme_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.DeleteTheme(ctx, principal(ctx), id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.DeleteThemeResponse{}), nil
}

// SetServerTheme picks the server's theme.
func (s *ThemeService) SetServerTheme(ctx context.Context, req *connect.Request[laternav1.SetServerThemeRequest]) (*connect.Response[laternav1.SetServerThemeResponse], error) {
	id, err := parseID(req.Msg.GetThemeId(), "theme_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.SetDefaultTheme(ctx, principal(ctx), id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.SetServerThemeResponse{}), nil
}

// SetThemeImage replaces the logo or the background of a theme.
func (s *ThemeService) SetThemeImage(ctx context.Context, req *connect.Request[laternav1.SetThemeImageRequest]) (*connect.Response[laternav1.SetThemeImageResponse], error) {
	id, err := parseID(req.Msg.GetThemeId(), "theme_id")
	if err != nil {
		return nil, err
	}
	var kind domain.ImageKind
	switch req.Msg.GetKind() {
	case laternav1.ThemeImageKind_THEME_IMAGE_KIND_LOGO:
		kind = domain.ImageLogo
	case laternav1.ThemeImageKind_THEME_IMAGE_KIND_BACKGROUND:
		kind = domain.ImageBackdrop
	case laternav1.ThemeImageKind_THEME_IMAGE_KIND_UNSPECIFIED:
		return nil, domain.Invalid("theme.unknown_image_kind")
	default:
		return nil, domain.Invalid("theme.unknown_image_kind", "kind", req.Msg.GetKind())
	}
	t, err := s.app.SetThemeImage(ctx, principal(ctx), id, kind, req.Msg.GetData())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.SetThemeImageResponse{Theme: s.withDefault(ctx, t)}), nil
}

// unsafeFileName matches the characters removed from the file name suggested on export.
var unsafeFileName = regexp.MustCompile(`[^\p{L}\p{N} ._-]+`)

// ExportTheme exports a theme.
func (s *ThemeService) ExportTheme(ctx context.Context, req *connect.Request[laternav1.ExportThemeRequest]) (*connect.Response[laternav1.ExportThemeResponse], error) {
	id, err := parseID(req.Msg.GetThemeId(), "theme_id")
	if err != nil {
		return nil, err
	}
	data, name, err := s.app.ExportTheme(ctx, id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.ExportThemeResponse{
		FileName: unsafeFileName.ReplaceAllString(name, "_") + ".laterna-theme.json", Data: data,
	}), nil
}

// ImportTheme imports an exported theme.
func (s *ThemeService) ImportTheme(ctx context.Context, req *connect.Request[laternav1.ImportThemeRequest]) (*connect.Response[laternav1.ImportThemeResponse], error) {
	t, err := s.app.ImportTheme(ctx, principal(ctx), req.Msg.GetData())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.ImportThemeResponse{Theme: s.withDefault(ctx, t)}), nil
}

// withDefault converts a theme, saying whether it is the server's.
func (s *ThemeService) withDefault(ctx context.Context, t domain.Theme) *laternav1.Theme {
	def, err := s.app.DefaultThemeID(ctx)
	return themeMsg(ctx, t, err == nil && def == t.ID)
}

func themeMsg(ctx context.Context, t domain.Theme, serverDefault bool) *laternav1.Theme {
	name, builtin := app.ThemeName(t)
	msg := &laternav1.Theme{
		Id: t.ID.String(), Name: t.Name, BuiltIn: t.BuiltIn, ServerDefault: serverDefault, UpdatedAt: timestamppb.New(t.UpdatedAt),
		Tokens: &laternav1.ThemeTokens{
			Dark: paletteMsg(t.Tokens.Dark), Light: paletteMsg(t.Tokens.Light), Radius: clampInt32(t.Tokens.Radius),
			Density: themeDensities[t.Tokens.Density], Font: themeFonts[t.Tokens.Font],
		},
	}
	msg.Name, msg.NameText = given(ctx, t.Name, name, builtin)
	if t.Logo != nil {
		msg.Logo = imagesMsg([]domain.Image{*t.Logo})[0]
	}
	if t.Background != nil {
		msg.Background = imagesMsg([]domain.Image{*t.Background})[0]
	}
	return msg
}

func paletteMsg(p domain.Palette) *laternav1.Palette {
	return &laternav1.Palette{
		Background: p.Background, Surface: p.Surface, SurfaceRaised: p.SurfaceRaised, Text: p.Text, TextMuted: p.TextMuted,
		Accent: p.Accent, OnAccent: p.OnAccent, Outline: p.Outline, Error: p.Error, OnError: p.OnError, Success: p.Success, Warning: p.Warning,
	}
}

func paletteOf(p *laternav1.Palette) domain.Palette {
	return domain.Palette{
		Background: p.GetBackground(), Surface: p.GetSurface(), SurfaceRaised: p.GetSurfaceRaised(), Text: p.GetText(),
		TextMuted: p.GetTextMuted(), Accent: p.GetAccent(), OnAccent: p.GetOnAccent(), Outline: p.GetOutline(),
		Error: p.GetError(), OnError: p.GetOnError(), Success: p.GetSuccess(), Warning: p.GetWarning(),
	}
}

// tokensOf converts received tokens. An unknown density or font becomes empty and is rejected by
// the theme check.
func tokensOf(t *laternav1.ThemeTokens) domain.ThemeTokens {
	return domain.ThemeTokens{
		Dark: paletteOf(t.GetDark()), Light: paletteOf(t.GetLight()), Radius: int(t.GetRadius()),
		Density: densitiesOf[t.GetDensity()], Font: fontsOf[t.GetFont()],
	}
}
