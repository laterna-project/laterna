package app

import (
	"context"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/arr"
	"github.com/laterna-project/laterna/internal/bazarr"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/lazylibrarian"
	"github.com/laterna-project/laterna/internal/store"
)

// Programs linked by their address and API key alone. Unlike Sonarr, Radarr and Lidarr they write
// no Kodi metadata and have no webhook:
//
//   - LazyLibrarian finds and downloads books. It is linked for book requests only, and the folder
//     watch notices the books it files.
//   - Bazarr finds subtitles. It is linked so that a profile can ask for one (subtitle_search.go).

const (
	lazyLibrarian     = string(domain.IntegrationLazyLibrarian)
	lazyLibrarianName = "LazyLibrarian"
	bazarrKey         = string(domain.IntegrationBazarr)
	bazarrName        = "Bazarr"
)

// linkedProgram is one of these programs: its name, and how to ask for its version, which proves
// the address and the key.
type linkedProgram struct {
	kind    domain.IntegrationKind
	name    string
	version func(ctx context.Context, a *App, s integrationSettings) (string, error)
}

// linkedPrograms lists them in the order administrators see them.
var linkedPrograms = []linkedProgram{
	{
		kind: domain.IntegrationLazyLibrarian, name: lazyLibrarianName,
		version: func(ctx context.Context, a *App, s integrationSettings) (string, error) {
			return a.lazyClient(s).Version(ctx)
		},
	},
	{
		kind: domain.IntegrationBazarr, name: bazarrName,
		version: func(ctx context.Context, a *App, s integrationSettings) (string, error) {
			return a.bazarrClient(s).Version(ctx)
		},
	},
}

// linkedProgramOf finds a linked program by its kind.
func linkedProgramOf(kind domain.IntegrationKind) (linkedProgram, bool) {
	for _, l := range linkedPrograms {
		if l.kind == kind {
			return l, true
		}
	}
	return linkedProgram{}, false
}

func (a *App) lazyClient(s integrationSettings) *lazylibrarian.Client {
	return lazylibrarian.New(s.URL, s.APIKey, a.http)
}

func (a *App) bazarrClient(s integrationSettings) *bazarr.Client {
	return bazarr.New(s.URL, s.APIKey, a.http)
}

// linkedStatus checks the connection to a linked program.
func (a *App) linkedStatus(ctx context.Context, l linkedProgram) (domain.Integration, error) {
	st := domain.Integration{Kind: l.kind}
	s, ok, err := a.loadIntegration(ctx, string(l.kind))
	if err != nil || !ok {
		return st, err
	}
	st.URL = s.URL
	check, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	version, err := l.version(check, a, s)
	if err != nil {
		st.Error = integrationProblem(l.name, err)
		return st, nil
	}
	st.Reachable, st.Version = true, version
	return st, nil
}

// setLinked stores the address and API key of a linked program, after trying them.
func (a *App) setLinked(ctx context.Context, p domain.Principal, l linkedProgram, rawURL, apiKey string) (domain.Integration, error) {
	rawURL, apiKey = strings.TrimSuffix(strings.TrimSpace(rawURL), "/"), strings.TrimSpace(apiKey)
	if err := arr.ValidURL(rawURL); err != nil {
		return domain.Integration{}, domain.Invalid("integration.invalid_url")
	}
	if apiKey == "" {
		return domain.Integration{}, domain.Invalid("integration.api_key_required")
	}
	s := integrationSettings{URL: rawURL, APIKey: apiKey}
	check, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := l.version(check, a, s); err != nil {
		return domain.Integration{}, domain.FromText(domain.ErrInvalid, integrationProblem(l.name, err))
	}
	if err := a.saveIntegration(ctx, string(l.kind), s); err != nil {
		return domain.Integration{}, err
	}
	a.forgetSubtitleLanguages()
	a.log.InfoContext(ctx, "integration saved", "kind", l.kind, "url", rawURL)
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityIntegration, AccountID: &p.Account.ID,
		Text: domain.T("activity.integration_linked", "actor", p.Account.Username, "name", l.name, "url", rawURL),
	})
	return a.linkedStatus(ctx, l)
}

// deleteLinked forgets a linked program.
func (a *App) deleteLinked(ctx context.Context, p domain.Principal, l linkedProgram) error {
	if _, ok, err := a.loadIntegration(ctx, string(l.kind)); err != nil || !ok {
		return err
	}
	if err := a.store.Write(ctx, func(q store.Q) error { return q.DeleteSetting(ctx, keyIntegration+string(l.kind)) }); err != nil {
		return err
	}
	a.forgetSubtitleLanguages()
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityIntegration, AccountID: &p.Account.ID,
		Text: domain.T("activity.integration_unlinked", "actor", p.Account.Username, "name", l.name),
	})
	return nil
}
