package app

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/arr"
	"github.com/laterna-project/laterna/internal/auth"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/jobs"
	"github.com/laterna-project/laterna/internal/library"
	"github.com/laterna-project/laterna/internal/store"
)

// Sonarr and Radarr integrations. They write the NFO files and images Laterna reads, and tell it
// about each import through a webhook. Laterna checks how they are set up, can fix it when an
// administrator asks, and can ask for a refresh. All of this is optional: without them NFO files
// are read again at scan time.

const (
	// keyIntegration prefixes the setting of each integration ("integration.sonarr").
	keyIntegration = "integration."
	// webhookUser is the Basic auth user of the webhook.
	webhookUser = "laterna"
	// webhookDelay gives the instance time to write NFO files and images after an import.
	webhookDelay = 30 * time.Second
	// maxWithoutNFO caps the list of titles without an NFO.
	maxWithoutNFO = 10
)

// integrationSettings is the stored setting of an integration.
type integrationSettings struct {
	URL    string `json:"url"`
	APIKey string `json:"api_key"`
	// Webhook is the hash of the secret of the installed webhook, WebhookNext that of the secret
	// being installed: the instance tries the webhook before it agrees to save it.
	Webhook     string `json:"webhook,omitempty"`
	WebhookNext string `json:"webhook_next,omitempty"`
}

// acceptsSecret checks the secret a webhook presents.
func (s integrationSettings) acceptsSecret(secret string) bool {
	if secret == "" {
		return false
	}
	h := []byte(auth.HashToken(secret))
	ok := false
	for _, want := range []string{s.Webhook, s.WebhookNext} {
		if want != "" && subtle.ConstantTimeCompare(h, []byte(want)) == 1 {
			ok = true
		}
	}
	return ok
}

func arrKind(k domain.IntegrationKind) (arr.Kind, error) {
	for _, kind := range arr.Kinds {
		if string(kind) == string(k) {
			return kind, nil
		}
	}
	return "", domain.Invalid("integration.unknown_kind", "kind", k)
}

// libraryKind is the kind of library an integration manages.
func libraryKind(k arr.Kind) domain.LibraryKind {
	if k == arr.Radarr {
		return domain.LibraryMovies
	}
	return domain.LibraryShows
}

func (a *App) loadIntegration(ctx context.Context, k arr.Kind) (integrationSettings, bool, error) {
	raw, ok, err := a.store.Read().Setting(ctx, keyIntegration+string(k))
	if err != nil || !ok {
		return integrationSettings{}, false, err
	}
	var s integrationSettings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return integrationSettings{}, false, fmt.Errorf("unreadable setting %s: %w", k, err)
	}
	return s, true, nil
}

func (a *App) saveIntegration(ctx context.Context, k arr.Kind, s integrationSettings) error {
	// The API key is stored as is, since it has to be sent with every call. It stays in the
	// database and the API never returns it.
	raw, err := json.Marshal(s) //nolint:gosec // G117: see above
	if err != nil {
		return err
	}
	return a.store.Write(ctx, func(q store.Q) error { return q.SetSetting(ctx, keyIntegration+string(k), string(raw)) })
}

func (a *App) arrClient(k arr.Kind, s integrationSettings) *arr.Client {
	return arr.New(k, s.URL, s.APIKey, a.http)
}

// arrProblem explains a Sonarr or Radarr error to an administrator.
func arrProblem(k arr.Kind, err error) domain.Text {
	var refused *arr.Error
	switch {
	case errors.Is(err, arr.ErrUnauthorized):
		return domain.T("error.integration.unauthorized", "name", k.Name())
	case errors.As(err, &refused):
		return domain.T("error.integration.refused", "name", k.Name(), "reason", refused)
	case errors.Is(err, context.DeadlineExceeded):
		return domain.T("error.integration.timeout", "name", k.Name())
	}
	return domain.T("error.integration.unreachable", "name", k.Name(), "reason", err)
}

// Integrations returns the state of Sonarr and Radarr.
func (a *App) Integrations(ctx context.Context) ([]domain.Integration, error) {
	out := make([]domain.Integration, 0, len(arr.Kinds))
	for _, k := range arr.Kinds {
		s, ok, err := a.loadIntegration(ctx, k)
		if err != nil {
			return nil, err
		}
		out = append(out, a.integrationStatus(ctx, k, s, ok))
	}
	return out, nil
}

// integrationStatus queries an instance: identity, Kodi metadata, webhook, and the NFO files
// present in the folder of each tracked series or movie.
func (a *App) integrationStatus(ctx context.Context, k arr.Kind, s integrationSettings, configured bool) domain.Integration {
	st := domain.Integration{Kind: domain.IntegrationKind(k)}
	if !configured {
		return st
	}
	st.URL = s.URL
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c := a.arrClient(k, s)
	status, err := c.Status(ctx)
	if err != nil {
		st.Error = arrProblem(k, err)
		return st
	}
	st.Reachable, st.Version = true, status.Version
	kodi, err := c.Kodi(ctx)
	if err != nil {
		st.Error = arrProblem(k, err)
		return st
	}
	st.KodiMetadata = kodi.Enabled && len(kodi.Missing) == 0
	st.MissingOptions = kodi.Missing
	if !kodi.Enabled {
		st.MissingOptions = append([]string{"Kodi (XBMC) / Emby"}, kodi.Missing...)
	}
	hook, ok, err := c.Webhook(ctx)
	if err != nil {
		st.Error = arrProblem(k, err)
		return st
	}
	st.Webhook = ok && hook.Active && s.Webhook != ""
	folders, err := c.Folders(ctx)
	if err != nil {
		st.Error = arrProblem(k, err)
		return st
	}
	roots, err := a.libraryRoots(ctx, k)
	if err != nil {
		st.Error = domain.T("error.server.internal")
		return st
	}
	for _, f := range folders {
		if !f.HasFiles {
			continue
		}
		st.Folders++
		local, ok := arr.MapPath(f.Path, roots, dirExists)
		if !ok {
			st.Unmapped++
			continue
		}
		nfos := []string{filepath.Join(local, "tvshow.nfo")}
		if k == arr.Radarr {
			nfos = []string{filepath.Join(local, "movie.nfo")}
			if f.File != "" {
				nfos = append(nfos, filepath.Join(local, filepath.FromSlash(strings.TrimSuffix(f.File, path.Ext(f.File))+".nfo")))
			}
		}
		if !slices.ContainsFunc(nfos, fileExists) {
			st.WithoutNFO++
			st.WithoutNFOTitles = append(st.WithoutNFOTitles, f.Title)
		}
	}
	slices.SortFunc(st.WithoutNFOTitles, func(x, y string) int { return strings.Compare(strings.ToLower(x), strings.ToLower(y)) })
	st.WithoutNFOTitles = st.WithoutNFOTitles[:min(len(st.WithoutNFOTitles), maxWithoutNFO)]
	return st
}

func dirExists(p string) bool {
	if st, err := os.Stat(p); err == nil {
		return st.IsDir()
	}
	return false
}

func fileExists(p string) bool {
	if st, err := os.Stat(p); err == nil {
		return !st.IsDir()
	}
	return false
}

// libraryRoots lists the folders of the libraries an integration manages.
func (a *App) libraryRoots(ctx context.Context, k arr.Kind) ([]string, error) {
	libs, err := a.integrationLibraries(ctx, k)
	var roots []string
	for _, l := range libs {
		roots = append(roots, l.Paths...)
	}
	return roots, err
}

func (a *App) integrationLibraries(ctx context.Context, k arr.Kind) ([]domain.Library, error) {
	libs, err := a.store.Read().Libraries(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(libs, func(l domain.Library) bool { return l.Kind != libraryKind(k) }), nil
}

// SetIntegration stores the address and API key of Sonarr or Radarr, after trying them.
func (a *App) SetIntegration(ctx context.Context, p domain.Principal, kind domain.IntegrationKind, rawURL, apiKey string) (domain.Integration, error) {
	k, err := arrKind(kind)
	if err != nil {
		return domain.Integration{}, err
	}
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
	if _, err := a.arrClient(k, s).Status(check); err != nil {
		return domain.Integration{}, domain.FromText(domain.ErrInvalid, arrProblem(k, err))
	}
	if prev, ok, err := a.loadIntegration(ctx, k); err != nil {
		return domain.Integration{}, err
	} else if ok {
		s.Webhook = prev.Webhook // same instance or another one: the webhook is still valid
	}
	if err := a.saveIntegration(ctx, k, s); err != nil {
		return domain.Integration{}, err
	}
	a.log.InfoContext(ctx, "integration saved", "kind", k, "url", rawURL)
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityIntegration, AccountID: &p.Account.ID,
		Text: domain.T("activity.integration_linked", "actor", p.Account.Username, "name", k.Name(), "url", rawURL),
	})
	return a.integrationStatus(ctx, k, s, true), nil
}

// DeleteIntegration forgets Sonarr or Radarr, and removes Laterna's webhook from it if it answers.
func (a *App) DeleteIntegration(ctx context.Context, p domain.Principal, kind domain.IntegrationKind) error {
	k, err := arrKind(kind)
	if err != nil {
		return err
	}
	s, ok, err := a.loadIntegration(ctx, k)
	if err != nil || !ok {
		return err
	}
	rm, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := a.arrClient(k, s).RemoveWebhook(rm); err != nil {
		a.log.WarnContext(ctx, "integration: webhook left on the instance", "kind", k, "err", err)
	}
	if err := a.store.Write(ctx, func(q store.Q) error { return q.DeleteSetting(ctx, keyIntegration+string(k)) }); err != nil {
		return err
	}
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityIntegration, AccountID: &p.Account.ID, Text: domain.T("activity.integration_unlinked", "actor", p.Account.Username, "name", k.Name()),
	})
	return nil
}

// ConfigureIntegration sets Sonarr or Radarr up for Laterna: Kodi metadata, webhook, refresh (see
// domain.IntegrationSetup). Each step changes the instance's configuration, so it only happens when
// an administrator asks.
func (a *App) ConfigureIntegration(ctx context.Context, p domain.Principal, kind domain.IntegrationKind, setup domain.IntegrationSetup) (domain.Integration, error) {
	k, err := arrKind(kind)
	if err != nil {
		return domain.Integration{}, err
	}
	s, ok, err := a.loadIntegration(ctx, k)
	if err != nil {
		return domain.Integration{}, err
	}
	if !ok {
		return domain.Integration{}, domain.Precondition("integration.not_configured", "name", k.Name())
	}
	c := a.arrClient(k, s)
	if setup.KodiMetadata {
		if err := c.EnableKodi(ctx); err != nil {
			return domain.Integration{}, domain.FromText(domain.ErrPrecondition, arrProblem(k, err))
		}
		a.log.InfoContext(ctx, "integration: Kodi metadata enabled", "kind", k)
	}
	if setup.WebhookURL != "" {
		if s, err = a.installWebhook(ctx, k, s, setup.WebhookURL); err != nil {
			return domain.Integration{}, err
		}
	}
	if setup.Refresh {
		if err := a.store.Write(ctx, func(q store.Q) error {
			return a.jobs.Enqueue(ctx, q, jobArrRefresh, string(k), priorityUser)
		}); err != nil {
			return domain.Integration{}, err
		}
		a.jobs.Kick()
	}
	var steps []domain.Text
	if setup.KodiMetadata {
		steps = append(steps, domain.T("activity.step.kodi"))
	}
	if setup.WebhookURL != "" {
		steps = append(steps, domain.T("activity.step.webhook"))
	}
	if setup.Refresh {
		steps = append(steps, domain.T("activity.step.refresh"))
	}
	if len(steps) > 0 {
		a.record(ctx, domain.Activity{
			Kind: domain.ActivityIntegration, AccountID: &p.Account.ID,
			Text: domain.T("activity.integration_configured", "actor", p.Account.Username, "name", k.Name(), steps),
		})
	}
	return a.integrationStatus(ctx, k, s, true), nil
}

// installWebhook installs Laterna's webhook on the instance with a new secret. The secret is
// accepted before the installation, because the instance tries it right away.
func (a *App) installWebhook(ctx context.Context, k arr.Kind, s integrationSettings, base string) (integrationSettings, error) {
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	if u, err := url.Parse(base); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return s, domain.Invalid("integration.invalid_server_url", "url", base)
	}
	secret, hash, err := auth.NewToken()
	if err != nil {
		return s, err
	}
	s.WebhookNext = hash
	if err := a.saveIntegration(ctx, k, s); err != nil {
		return s, err
	}
	installErr := a.arrClient(k, s).InstallWebhook(ctx, base+"/hooks/"+string(k), webhookUser, secret)
	if installErr == nil {
		s.Webhook = hash
	}
	s.WebhookNext = ""
	if err := a.saveIntegration(ctx, k, s); err != nil {
		return s, err
	}
	if installErr != nil {
		return s, domain.FromText(domain.ErrPrecondition, arrProblem(k, installErr))
	}
	a.log.InfoContext(ctx, "integration: webhook installed", "kind", k, "url", base+"/hooks/"+string(k))
	return s, nil
}

// ArrWebhook receives an event from Sonarr or Radarr. After an import, a rename or a deletion, the
// libraries concerned are scanned 30 s later, which gives the instance time to write NFO files and
// images. Events close together make a single scan.
func (a *App) ArrWebhook(ctx context.Context, kind, secret string, body []byte) error {
	k, err := arrKind(domain.IntegrationKind(kind))
	if err != nil {
		return domain.NotFound("integration.unknown_webhook")
	}
	s, ok, err := a.loadIntegration(ctx, k)
	if err != nil {
		return err
	}
	if !ok || !s.acceptsSecret(secret) {
		return domain.Unauthenticated("integration.invalid_webhook_secret")
	}
	ev, err := arr.ParseEvent(body)
	if err != nil {
		return domain.Invalid("integration.invalid_event")
	}
	if !ev.ChangesFiles() {
		a.log.DebugContext(ctx, "webhook: nothing to do", "kind", k, "event", ev.Type)
		return nil
	}
	libs, err := a.integrationLibraries(ctx, k)
	if err != nil {
		return err
	}
	if local, ok := arr.MapPath(ev.Path, libraryPaths(libs), dirExists); ok {
		if within := slices.DeleteFunc(slices.Clone(libs), func(l domain.Library) bool { return !library.Under(local, l.Paths) }); len(within) > 0 {
			libs = within
		}
	}
	if err := a.store.Write(ctx, func(q store.Q) error {
		for _, l := range libs {
			if err := a.jobs.EnqueueAfter(ctx, q, jobScanLibrary, l.ID.String(), priorityUser, webhookDelay); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	a.jobs.Kick()
	a.log.InfoContext(ctx, "webhook: scan scheduled", "kind", k, "event", ev.Type, "path", ev.Path, "libraries", len(libs))
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityWebhook, Text: domain.T("activity.webhook", "name", k.Name(), "event", ev.Type, "path", ev.Path),
	})
	return nil
}

func libraryPaths(libs []domain.Library) []string {
	var out []string
	for _, l := range libs {
		out = append(out, l.Paths...)
	}
	return out
}

// refreshArr asks Sonarr or Radarr to refresh all its series or movies (missing NFO files and
// images get written), waits for the command to end, then scans the libraries it manages: no
// webhook reports a refresh.
func (a *App) refreshArr(ctx context.Context, target string) error {
	k, err := arrKind(domain.IntegrationKind(target))
	if err != nil {
		return jobs.Permanent(err)
	}
	s, ok, err := a.loadIntegration(ctx, k)
	if err != nil || !ok {
		return err
	}
	c := a.arrClient(k, s)
	id, err := c.Refresh(ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", k.Name(), err)
	}
	a.log.InfoContext(ctx, "integration: refresh requested", "kind", k, "command", id)
	tick := time.NewTicker(a.arrPoll)
	defer tick.Stop()
	for done := false; !done; {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
		var failed string
		if done, failed, err = c.Command(ctx, id); err != nil {
			return fmt.Errorf("%s: %w", k.Name(), err)
		}
		if failed != "" {
			a.log.WarnContext(ctx, "integration: refresh failed, scanning anyway", "kind", k, "status", failed)
		}
	}
	libs, err := a.integrationLibraries(ctx, k)
	if err != nil {
		return err
	}
	if err := a.store.Write(ctx, func(q store.Q) error {
		for _, l := range libs {
			if err := a.jobs.Enqueue(ctx, q, jobScanLibrary, l.ID.String(), priorityBackground); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	a.jobs.Kick()
	a.log.InfoContext(ctx, "integration: refresh done, scan requested", "kind", k, "libraries", len(libs))
	return nil
}
