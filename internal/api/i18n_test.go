package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/domain"
)

// inLang asks for a language on a request (Accept-Language header); empty asks for none.
func inLang[T any](req *connect.Request[T], lang string) *connect.Request[T] {
	if lang != "" {
		req.Header().Set("Accept-Language", lang)
	}
	return req
}

// Internationalization end to end: an error carries its code and params, and its message is written
// in the language of the request (the one from Accept-Language, otherwise the profile's, otherwise
// the server's). Texts composed by the server (home, activity log, made-up names) come with their
// key.
func TestInternationalisation(t *testing.T) {
	url, token := libraryServer(t, domain.LibraryShows, "Séries")
	ctx := context.Background()
	c := http.DefaultClient
	server := laternav1connect.NewServerServiceClient(c, url)
	auth := laternav1connect.NewAuthServiceClient(c, url)
	profiles := laternav1connect.NewProfileServiceClient(c, url)
	catalog := laternav1connect.NewCatalogServiceClient(c, url)
	home := laternav1connect.NewHomeServiceClient(c, url)
	system := laternav1connect.NewSystemServiceClient(c, url)
	activity := laternav1connect.NewActivityServiceClient(c, url)
	const french = "fr-FR,fr;q=0.9,en;q=0.8"

	info, err := server.GetServerInfo(ctx, connect.NewRequest(&laternav1.GetServerInfoRequest{}))
	if err != nil || info.Msg.GetLanguage() != "en" || !slices.Equal(info.Msg.GetLanguages(), []string{"en", "fr"}) {
		t.Fatalf("server languages: %v %v", info, err)
	}

	// An error: same code and same params in every language. The message follows the requested
	// language, and falls back to English if the server does not have it.
	tooLong := func(lang string) error {
		_, err := profiles.CreateProfile(ctx, inLang(authed(&laternav1.CreateProfileRequest{Name: strings.Repeat("a", 41)}, token), lang))
		return err
	}
	for lang, want := range map[string]string{
		"":                     "The profile name is longer than 40 characters",
		french:                 "Le nom du profil dépasse 40 caractères",
		"de-DE,de;q=0.9":       "The profile name is longer than 40 characters",
		"de,fr;q=0.5,en;q=0.4": "Le nom du profil dépasse 40 caractères",
	} {
		err := tooLong(lang)
		d := errorDetail(err)
		if connect.CodeOf(err) != connect.CodeInvalidArgument || d.GetCode() != "profile.name_too_long" || d.GetParams()["max"] != "40" {
			t.Errorf("language %q: %v (detail %v)", lang, err, d)
		}
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Message() != want {
			t.Errorf("language %q: message %q, want %q", lang, ce.Message(), want)
		}
	}
	if _, err := home.GetHome(ctx, connect.NewRequest(&laternav1.GetHomeRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated || errorCode(err) != "auth.required" {
		t.Errorf("without a token: %v", err)
	}

	// Names made up by the server: written in the language of the request, and given for
	// translation.
	list, err := catalog.ListSeries(ctx, authed(&laternav1.ListSeriesRequest{}, token))
	if err != nil || len(list.Msg.GetSeries()) == 0 {
		t.Fatalf("series: %v %v", list, err)
	}
	seriesID := list.Msg.GetSeries()[0].GetId()
	for lang, want := range map[string]string{"": "Season 1", french: "Saison 1"} {
		got, err := catalog.GetSeries(ctx, inLang(authed(&laternav1.GetSeriesRequest{SeriesId: seriesID}, token), lang))
		if err != nil || len(got.Msg.GetSeasons()) == 0 {
			t.Fatalf("series: %v %v", got, err)
		}
		season := got.Msg.GetSeasons()[0]
		if season.GetTitle() != want || season.GetTitleText().GetKey() != "name.season" || season.GetTitleText().GetParams()["number"] != "1" ||
			season.GetTitleText().GetText() != want {
			t.Errorf("language %q: season %v", lang, season)
		}
	}

	// Home: the title of a row, and what is needed to translate it.
	for lang, want := range map[string]string{"": "Recently added in Séries", french: "Ajouts récents dans Séries"} {
		rows, err := home.GetHome(ctx, inLang(authed(&laternav1.GetHomeRequest{}, token), lang))
		if err != nil || len(rows.Msg.GetRows()) != 1 {
			t.Fatalf("home: %v %v", rows, err)
		}
		row := rows.Msg.GetRows()[0]
		if row.GetTitle() != want || row.GetTitleText().GetKey() != "home.latest" || row.GetTitleText().GetParams()["library"] != "Séries" {
			t.Errorf("language %q: row %q %v", lang, row.GetTitle(), row.GetTitleText())
		}
	}

	// The profile language: used when the request asks for none.
	if _, err := profiles.SetLanguage(ctx, authed(&laternav1.SetLanguageRequest{Language: "not a language"}, token)); errorCode(err) != "profile.invalid_language" {
		t.Errorf("invalid language: %v", err)
	}
	set, err := profiles.SetLanguage(ctx, authed(&laternav1.SetLanguageRequest{Language: "fr-CA"}, token))
	if err != nil || set.Msg.GetProfile().GetLanguage() != "fr-CA" {
		t.Fatalf("profile language: %v %v", set, err)
	}
	var ce *connect.Error
	if err := tooLong(""); !errors.As(err, &ce) || ce.Message() != "Le nom du profil dépasse 40 caractères" {
		t.Errorf("profile language: %v", err)
	}
	if err := tooLong("en"); !errors.As(err, &ce) || ce.Message() != "The profile name is longer than 40 characters" {
		t.Errorf("the request language wins over the profile's: %v", err)
	}
	// A language the server does not have is allowed (the client may have it). The server then
	// writes in its own.
	if set, err := profiles.SetLanguage(ctx, authed(&laternav1.SetLanguageRequest{Language: "pt-BR"}, token)); err != nil || set.Msg.GetProfile().GetLanguage() != "pt-BR" {
		t.Fatalf("language unknown to the server: %v %v", set, err)
	}
	if err := tooLong(""); !errors.As(err, &ce) || ce.Message() != "The profile name is longer than 40 characters" {
		t.Errorf("profile in Portuguese, server in English: %v", err)
	}

	// The server language: a setting, applied right away.
	german, frenchTag := "de", "fr"
	if _, err := system.UpdateSettings(ctx, authed(&laternav1.UpdateSettingsRequest{Language: &german}, token)); errorCode(err) != "settings.invalid_language" {
		t.Errorf("language the server does not have: %v", err)
	}
	updated, err := system.UpdateSettings(ctx, authed(&laternav1.UpdateSettingsRequest{Language: &frenchTag}, token))
	if err != nil || updated.Msg.GetSettings().GetLanguage() != "fr" {
		t.Fatalf("server language: %v %v", updated, err)
	}
	if info, err := server.GetServerInfo(ctx, connect.NewRequest(&laternav1.GetServerInfoRequest{})); err != nil || info.Msg.GetLanguage() != "fr" {
		t.Errorf("announced language: %v %v", info, err)
	}
	_, err = auth.Login(ctx, connect.NewRequest(&laternav1.LoginRequest{Username: "admin", Password: "faux", Device: &laternav1.Device{Name: "Test"}}))
	if !errors.As(err, &ce) || errorCode(err) != "auth.invalid_credentials" || ce.Message() != "Nom d'utilisateur ou mot de passe incorrect" {
		t.Errorf("server in French, no language requested: %v", err)
	}

	// The activity log: a composed text, with its changes as a list.
	for lang, want := range map[string]string{"en": "admin changed the settings: language \"fr\"", french: "admin a modifié les réglages : langue « fr »"} {
		page, err := activity.ListActivity(ctx, inLang(authed(&laternav1.ListActivityRequest{}, token), lang))
		if err != nil {
			t.Fatal(err)
		}
		var entry *laternav1.ActivityEntry
		for _, e := range page.Msg.GetEntries() {
			if e.GetText().GetKey() == "activity.settings_updated" {
				entry = e
				break
			}
		}
		if entry.GetSummary() != want || entry.GetText().GetParams()["actor"] != "admin" || len(entry.GetText().GetList()) != 1 ||
			entry.GetText().GetList()[0].GetKey() != "activity.setting.language" || entry.GetText().GetList()[0].GetParams()["value"] != "fr" {
			t.Errorf("language %q: entry %v", lang, entry)
		}
	}

	// Outside Connect: the code in the header, the text in the body.
	for lang, want := range map[string]string{"en": "Invalid width", french: "Largeur invalide"} {
		resp := get(t, url+"/images/"+domain.NewID().String()+"/x?w=large", "Accept-Language", lang)
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusBadRequest || resp.Header.Get(ErrorHeader) != "request.invalid_width" || strings.TrimSpace(string(body)) != want {
			t.Errorf("language %q: %d %q %q", lang, resp.StatusCode, resp.Header.Get(ErrorHeader), body)
		}
	}
}
