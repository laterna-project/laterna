package rpc

import (
	"context"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// IntegrationService implements laterna.v1.IntegrationService.
type IntegrationService struct {
	app *app.App
}

var integrationKinds = map[domain.IntegrationKind]laternav1.IntegrationKind{
	domain.IntegrationSonarr: laternav1.IntegrationKind_INTEGRATION_KIND_SONARR,
	domain.IntegrationRadarr: laternav1.IntegrationKind_INTEGRATION_KIND_RADARR,
}

func integrationKindFromMsg(k laternav1.IntegrationKind) domain.IntegrationKind {
	for d, m := range integrationKinds {
		if m == k {
			return d
		}
	}
	return ""
}

func integrationMsg(ctx context.Context, i domain.Integration) *laternav1.Integration {
	return &laternav1.Integration{
		Kind: integrationKinds[i.Kind], Url: i.URL, Reachable: i.Reachable, Version: i.Version,
		Error: render(ctx, i.Error), ErrorText: textMsg(ctx, i.Error),
		KodiMetadata: i.KodiMetadata, MissingOptions: i.MissingOptions, Webhook: i.Webhook,
		Folders: clampInt32(i.Folders), Unmapped: clampInt32(i.Unmapped),
		WithoutNfo: clampInt32(i.WithoutNFO), WithoutNfoTitles: i.WithoutNFOTitles,
	}
}

// ListIntegrations returns the state of Sonarr and Radarr.
func (s *IntegrationService) ListIntegrations(ctx context.Context, _ *connect.Request[laternav1.ListIntegrationsRequest]) (*connect.Response[laternav1.ListIntegrationsResponse], error) {
	list, err := s.app.Integrations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*laternav1.Integration, len(list))
	for i, it := range list {
		out[i] = integrationMsg(ctx, it)
	}
	return connect.NewResponse(&laternav1.ListIntegrationsResponse{Integrations: out}), nil
}

// SetIntegration stores an instance.
func (s *IntegrationService) SetIntegration(ctx context.Context, req *connect.Request[laternav1.SetIntegrationRequest]) (*connect.Response[laternav1.SetIntegrationResponse], error) {
	m := req.Msg
	it, err := s.app.SetIntegration(ctx, principal(ctx), integrationKindFromMsg(m.GetKind()), m.GetUrl(), m.GetApiKey())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.SetIntegrationResponse{Integration: integrationMsg(ctx, it)}), nil
}

// DeleteIntegration forgets an instance.
func (s *IntegrationService) DeleteIntegration(ctx context.Context, req *connect.Request[laternav1.DeleteIntegrationRequest]) (*connect.Response[laternav1.DeleteIntegrationResponse], error) {
	if err := s.app.DeleteIntegration(ctx, principal(ctx), integrationKindFromMsg(req.Msg.GetKind())); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.DeleteIntegrationResponse{}), nil
}

// ConfigureIntegration sets an instance up for Laterna.
func (s *IntegrationService) ConfigureIntegration(ctx context.Context, req *connect.Request[laternav1.ConfigureIntegrationRequest]) (*connect.Response[laternav1.ConfigureIntegrationResponse], error) {
	m := req.Msg
	it, err := s.app.ConfigureIntegration(ctx, principal(ctx), integrationKindFromMsg(m.GetKind()), domain.IntegrationSetup{
		KodiMetadata: m.GetKodiMetadata(), WebhookURL: m.GetWebhookUrl(), Refresh: m.GetRefresh(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.ConfigureIntegrationResponse{Integration: integrationMsg(ctx, it)}), nil
}
