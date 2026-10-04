package rpc

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// OpenID Connect.

var oidcStates = map[app.OIDCLoginState]laternav1.OidcLoginState{
	app.OIDCPending:  laternav1.OidcLoginState_OIDC_LOGIN_STATE_PENDING,
	app.OIDCApproved: laternav1.OidcLoginState_OIDC_LOGIN_STATE_APPROVED,
	app.OIDCDenied:   laternav1.OidcLoginState_OIDC_LOGIN_STATE_DENIED,
	app.OIDCExpired:  laternav1.OidcLoginState_OIDC_LOGIN_STATE_EXPIRED,
}

// StartOidcLogin starts a login through the provider.
func (s *AuthService) StartOidcLogin(ctx context.Context, req *connect.Request[laternav1.StartOidcLoginRequest]) (*connect.Response[laternav1.StartOidcLoginResponse], error) {
	start, err := s.app.StartOIDCLogin(ctx, deviceFromMsg(req.Msg.GetDevice()), clientIP(ctx, req.Peer()))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.StartOidcLoginResponse{
		LoginId: start.LoginID, AuthorizationUrl: start.AuthURL,
		Interval: durationpb.New(start.Interval), ExpiresIn: durationpb.New(start.ExpiresIn),
	}), nil
}

// PollOidcLogin returns the state of a login through the provider.
func (s *AuthService) PollOidcLogin(ctx context.Context, req *connect.Request[laternav1.PollOidcLoginRequest]) (*connect.Response[laternav1.PollOidcLoginResponse], error) {
	state, login, msg := s.app.PollOIDCLogin(ctx, req.Msg.GetLoginId())
	resp := &laternav1.PollOidcLoginResponse{State: oidcStates[state], Message: render(ctx, msg), MessageText: textMsg(ctx, msg)}
	if login != nil {
		resp.Session, resp.Token = loginResponse(*login)
	}
	return connect.NewResponse(resp), nil
}

// GetOidcProvider describes the configured provider.
func (s *SystemService) GetOidcProvider(context.Context, *connect.Request[laternav1.GetOidcProviderRequest]) (*connect.Response[laternav1.GetOidcProviderResponse], error) {
	return connect.NewResponse(&laternav1.GetOidcProviderResponse{Provider: s.oidcProviderMsg(s.app.OIDCProvider())}), nil
}

// SetOidcProvider sets the provider.
func (s *SystemService) SetOidcProvider(ctx context.Context, req *connect.Request[laternav1.SetOidcProviderRequest]) (*connect.Response[laternav1.SetOidcProviderResponse], error) {
	m := req.Msg
	cfg, err := s.app.SetOIDCProvider(ctx, principal(ctx), domain.OIDCProvider{
		Issuer: m.GetIssuer(), ClientID: m.GetClientId(), ClientSecret: m.GetClientSecret(), Name: m.GetName(), AutoCreate: m.GetAutoCreate(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.SetOidcProviderResponse{Provider: s.oidcProviderMsg(cfg)}), nil
}

func (s *SystemService) oidcProviderMsg(cfg domain.OIDCProvider) *laternav1.OidcProvider {
	msg := &laternav1.OidcProvider{
		Issuer: cfg.Issuer, ClientId: cfg.ClientID, Name: cfg.Name, AutoCreate: cfg.AutoCreate, HasSecret: cfg.ClientSecret != "",
	}
	if public := s.app.Settings().PublicURL; public != "" {
		msg.CallbackUrl = public + app.OIDCCallbackPath
	}
	return msg
}
