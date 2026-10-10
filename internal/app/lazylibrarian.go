package app

import (
	"context"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/arr"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/lazylibrarian"
	"github.com/laterna-project/laterna/internal/store"
)

// LazyLibrarian finds and downloads books. Unlike Sonarr, Radarr and Lidarr it writes no Kodi
// metadata and has no webhook: it is linked for book requests only, and the folder watch notices
// the books it files.

const (
	lazyLibrarian     = string(domain.IntegrationLazyLibrarian)
	lazyLibrarianName = "LazyLibrarian"
)

func (a *App) lazyClient(s integrationSettings) *lazylibrarian.Client {
	return lazylibrarian.New(s.URL, s.APIKey, a.http)
}

// lazyLibrarianStatus checks the connection to LazyLibrarian.
func (a *App) lazyLibrarianStatus(ctx context.Context) (domain.Integration, error) {
	st := domain.Integration{Kind: domain.IntegrationLazyLibrarian}
	s, ok, err := a.loadIntegration(ctx, lazyLibrarian)
	if err != nil || !ok {
		return st, err
	}
	st.URL = s.URL
	check, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	version, err := a.lazyClient(s).Version(check)
	if err != nil {
		st.Error = integrationProblem(lazyLibrarianName, err)
		return st, nil
	}
	st.Reachable, st.Version = true, version
	return st, nil
}

// setLazyLibrarian stores the address and API key of LazyLibrarian, after trying them.
func (a *App) setLazyLibrarian(ctx context.Context, p domain.Principal, rawURL, apiKey string) (domain.Integration, error) {
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
	if _, err := a.lazyClient(s).Version(check); err != nil {
		return domain.Integration{}, domain.FromText(domain.ErrInvalid, integrationProblem(lazyLibrarianName, err))
	}
	if err := a.saveIntegration(ctx, lazyLibrarian, s); err != nil {
		return domain.Integration{}, err
	}
	a.log.InfoContext(ctx, "integration saved", "kind", lazyLibrarian, "url", rawURL)
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityIntegration, AccountID: &p.Account.ID,
		Text: domain.T("activity.integration_linked", "actor", p.Account.Username, "name", lazyLibrarianName, "url", rawURL),
	})
	return a.lazyLibrarianStatus(ctx)
}

// deleteLazyLibrarian forgets LazyLibrarian.
func (a *App) deleteLazyLibrarian(ctx context.Context, p domain.Principal) error {
	if _, ok, err := a.loadIntegration(ctx, lazyLibrarian); err != nil || !ok {
		return err
	}
	if err := a.store.Write(ctx, func(q store.Q) error { return q.DeleteSetting(ctx, keyIntegration+lazyLibrarian) }); err != nil {
		return err
	}
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityIntegration, AccountID: &p.Account.ID,
		Text: domain.T("activity.integration_unlinked", "actor", p.Account.Username, "name", lazyLibrarianName),
	})
	return nil
}
