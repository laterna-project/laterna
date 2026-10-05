package api

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/store"
)

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Themes end to end: server theme without signing in, built-in themes, unreadable theme rejected,
// creation, logo served by the image route, server theme changed (event), a profile's choice,
//
//export then import, deletion.
func TestThemes(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a, err := app.New(ctx, st, app.Options{ServerName: "Test", CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(a, Options{}))
	t.Cleanup(srv.Close)
	setup, err := laternav1connect.NewAuthServiceClient(srv.Client(), srv.URL).Setup(ctx, connect.NewRequest(&laternav1.SetupRequest{
		Username: "admin", Password: "a-strong-password", Device: &laternav1.Device{Name: "Test", Client: "Test", ClientVersion: "1", Platform: "Go"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	token := setup.Msg.GetToken()
	themes := laternav1connect.NewThemeServiceClient(srv.Client(), srv.URL)

	// Without signing in: the built-in theme.
	server, err := themes.GetServerTheme(ctx, connect.NewRequest(&laternav1.GetServerThemeRequest{}))
	if err != nil || server.Msg.GetTheme().GetName() != "Laterna" || !server.Msg.GetTheme().GetServerDefault() || !server.Msg.GetTheme().GetBuiltIn() {
		t.Fatalf("server theme: %v %v", server, err)
	}
	list, err := themes.ListThemes(ctx, authed(&laternav1.ListThemesRequest{}, token))
	if err != nil || len(list.Msg.GetThemes()) != 4 {
		t.Fatalf("built-in themes: %v %v", list, err)
	}
	builtin := list.Msg.GetThemes()[0]
	if _, err := themes.UpdateTheme(ctx, authed(&laternav1.UpdateThemeRequest{ThemeId: builtin.GetId(), Name: "X", Tokens: builtin.GetTokens()}, token)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("built-in theme edited: %v", err)
	}

	// An unreadable theme is rejected, with the reason.
	tokens := builtin.GetTokens()
	unreadable := *tokens.GetDark()
	unreadable.Text = "#30343c"
	bad := &laternav1.ThemeTokens{Dark: &unreadable, Light: tokens.GetLight(), Radius: 8, Density: tokens.GetDensity(), Font: tokens.GetFont()}
	_, err = themes.CreateTheme(ctx, authed(&laternav1.CreateThemeRequest{Name: "Gray", Tokens: bad}, token))
	rejected := errorDetail(err)
	if connect.CodeOf(err) != connect.CodeInvalidArgument || rejected.GetCode() != "theme.rejected" || len(rejected.GetCauses()) == 0 {
		t.Fatalf("unreadable theme: %v", err)
	}
	if c := rejected.GetCauses()[0]; c.GetKey() != "theme.problem.low_contrast" || c.GetParams()["foreground"] != "text" ||
		c.GetParams()["background"] != "background" || c.GetParams()["palette"] != "dark" || !strings.Contains(c.GetText(), "too little contrast") {
		t.Errorf("cause of the rejection: %v", c)
	}

	// Creation, logo.
	mine := &laternav1.ThemeTokens{Dark: tokens.GetDark(), Light: tokens.GetLight(), Radius: 20, Density: laternav1.ThemeDensity_THEME_DENSITY_COMPACT, Font: laternav1.ThemeFont_THEME_FONT_SERIF}
	created, err := themes.CreateTheme(ctx, authed(&laternav1.CreateThemeRequest{Name: "House", Tokens: mine}, token))
	if err != nil {
		t.Fatal(err)
	}
	house := created.Msg.GetTheme()
	if _, err := themes.SetThemeImage(ctx, authed(&laternav1.SetThemeImageRequest{ThemeId: house.GetId(), Kind: laternav1.ThemeImageKind_THEME_IMAGE_KIND_LOGO, Data: []byte("not an image")}, token)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("unreadable logo: %v", err)
	}
	withLogo, err := themes.SetThemeImage(ctx, authed(&laternav1.SetThemeImageRequest{ThemeId: house.GetId(), Kind: laternav1.ThemeImageKind_THEME_IMAGE_KIND_LOGO, Data: testPNG(t, 64, 32)}, token))
	if err != nil {
		t.Fatal(err)
	}
	logo := withLogo.Msg.GetTheme().GetLogo()
	if logo.GetWidth() != 64 || logo.GetHeight() != 32 || logo.GetBlurhash() == "" {
		t.Fatalf("logo: %v", logo)
	}
	resp, err := http.Get(srv.URL + logo.GetUrl())
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" {
		t.Errorf("logo served: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	// Server theme changed: announced to connected clients.
	sub := connect.NewRequest(&laternav1.SubscribeRequest{})
	sub.Header().Set("Authorization", "Bearer "+token)
	streamCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stream, err := laternav1connect.NewEventServiceClient(srv.Client(), srv.URL).Subscribe(streamCtx, sub)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if !stream.Receive() || stream.Msg().GetEvent().GetHeartbeat() == nil {
		t.Fatalf("stream: %v", stream.Err())
	}
	if _, err := themes.SetServerTheme(ctx, authed(&laternav1.SetServerThemeRequest{ThemeId: house.GetId()}, token)); err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() || stream.Msg().GetEvent().GetThemesChanged() == nil {
		t.Errorf("want an event: %v %v", stream.Msg(), stream.Err())
	}
	if server, err := themes.GetServerTheme(ctx, connect.NewRequest(&laternav1.GetServerThemeRequest{})); err != nil || server.Msg.GetTheme().GetName() != "House" {
		t.Errorf("new server theme: %v %v", server, err)
	}

	// The profile's choice.
	my, err := themes.GetMyTheme(ctx, authed(&laternav1.GetMyThemeRequest{}, token))
	if err != nil || my.Msg.GetTheme().GetName() != "House" || !my.Msg.GetFollowsServer() || my.Msg.GetMode() != laternav1.ThemeMode_THEME_MODE_AUTO {
		t.Fatalf("profile theme: %v %v", my, err)
	}
	ocean := list.Msg.GetThemes()[1]
	if _, err := themes.SetMyTheme(ctx, authed(&laternav1.SetMyThemeRequest{ThemeId: ocean.GetId(), Mode: laternav1.ThemeMode_THEME_MODE_DARK}, token)); err != nil {
		t.Fatal(err)
	}
	if my, err := themes.GetMyTheme(ctx, authed(&laternav1.GetMyThemeRequest{}, token)); err != nil || my.Msg.GetTheme().GetId() != ocean.GetId() ||
		my.Msg.GetFollowsServer() || my.Msg.GetMode() != laternav1.ThemeMode_THEME_MODE_DARK {
		t.Errorf("chosen theme: %v %v", my, err)
	}

	// Export then import: same tokens, logo included, numbered name.
	exported, err := themes.ExportTheme(ctx, authed(&laternav1.ExportThemeRequest{ThemeId: house.GetId()}, token))
	if err != nil || exported.Msg.GetFileName() != "House.laterna-theme.json" {
		t.Fatalf("export: %v %v", exported, err)
	}
	imported, err := themes.ImportTheme(ctx, authed(&laternav1.ImportThemeRequest{Data: exported.Msg.GetData()}, token))
	if err != nil {
		t.Fatal(err)
	}
	copyTheme := imported.Msg.GetTheme()
	if copyTheme.GetName() != "House (2)" || copyTheme.GetTokens().GetRadius() != 20 || copyTheme.GetLogo().GetWidth() != 64 {
		t.Errorf("import: %v", copyTheme)
	}

	// The profile picks the copy, then the copy is deleted: back to the server's theme.
	if _, err := themes.SetMyTheme(ctx, authed(&laternav1.SetMyThemeRequest{ThemeId: copyTheme.GetId()}, token)); err != nil {
		t.Fatal(err)
	}
	if _, err := themes.DeleteTheme(ctx, authed(&laternav1.DeleteThemeRequest{ThemeId: copyTheme.GetId()}, token)); err != nil {
		t.Fatal(err)
	}
	if my, err := themes.GetMyTheme(ctx, authed(&laternav1.GetMyThemeRequest{}, token)); err != nil || my.Msg.GetTheme().GetName() != "House" || !my.Msg.GetFollowsServer() {
		t.Errorf("after deleting the chosen theme: %v %v", my, err)
	}
	// Deleting the server's theme brings the built-in one back.
	if _, err := themes.DeleteTheme(ctx, authed(&laternav1.DeleteThemeRequest{ThemeId: house.GetId()}, token)); err != nil {
		t.Fatal(err)
	}
	if server, err := themes.GetServerTheme(ctx, connect.NewRequest(&laternav1.GetServerThemeRequest{})); err != nil || server.Msg.GetTheme().GetName() != "Laterna" {
		t.Errorf("after deleting the server theme: %v %v", server, err)
	}
	if resp, err := http.Get(srv.URL + logo.GetUrl()); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("logo of a deleted theme: %v %v", resp, err)
	} else {
		_ = resp.Body.Close()
	}
}
