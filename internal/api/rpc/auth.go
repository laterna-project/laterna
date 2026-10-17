package rpc

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
)

// AuthService implements laterna.v1.AuthService.
type AuthService struct {
	app *app.App
}

func loginResponse(l app.Login) (*laternav1.Session, string) {
	return sessionMsg(l.Session, l.Session.Session.ID), l.Token
}

// Setup creates the first administrator account.
func (s *AuthService) Setup(ctx context.Context, req *connect.Request[laternav1.SetupRequest]) (*connect.Response[laternav1.SetupResponse], error) {
	m := req.Msg
	l, err := s.app.Setup(ctx, m.GetUsername(), m.GetPassword(), deviceFromMsg(m.GetDevice()), clientIP(ctx, req.Peer()))
	if err != nil {
		return nil, err
	}
	session, token := loginResponse(l)
	return connect.NewResponse(&laternav1.SetupResponse{Session: session, Token: token}), nil
}

// Login opens a session.
func (s *AuthService) Login(ctx context.Context, req *connect.Request[laternav1.LoginRequest]) (*connect.Response[laternav1.LoginResponse], error) {
	m := req.Msg
	l, err := s.app.Login(ctx, m.GetUsername(), m.GetPassword(), deviceFromMsg(m.GetDevice()), clientIP(ctx, req.Peer()))
	if err != nil {
		return nil, err
	}
	session, token := loginResponse(l)
	return connect.NewResponse(&laternav1.LoginResponse{Session: session, Token: token}), nil
}

// Logout closes the current session.
func (s *AuthService) Logout(ctx context.Context, _ *connect.Request[laternav1.LogoutRequest]) (*connect.Response[laternav1.LogoutResponse], error) {
	if err := s.app.Logout(ctx, principal(ctx)); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.LogoutResponse{}), nil
}

// GetSession describes the current session.
func (s *AuthService) GetSession(ctx context.Context, _ *connect.Request[laternav1.GetSessionRequest]) (*connect.Response[laternav1.GetSessionResponse], error) {
	p := principal(ctx)
	d, err := s.app.Session(ctx, p)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.GetSessionResponse{Session: sessionMsg(d, p.SessionID)}), nil
}

// ListSessions lists the devices signed in to the account.
func (s *AuthService) ListSessions(ctx context.Context, _ *connect.Request[laternav1.ListSessionsRequest]) (*connect.Response[laternav1.ListSessionsResponse], error) {
	p := principal(ctx)
	list, err := s.app.Sessions(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]*laternav1.Session, len(list))
	for i, d := range list {
		out[i] = sessionMsg(d, p.SessionID)
	}
	return connect.NewResponse(&laternav1.ListSessionsResponse{Sessions: out}), nil
}

// RevokeSession signs a device out of the account.
func (s *AuthService) RevokeSession(ctx context.Context, req *connect.Request[laternav1.RevokeSessionRequest]) (*connect.Response[laternav1.RevokeSessionResponse], error) {
	id, err := parseID(req.Msg.GetSessionId(), "session_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.RevokeSession(ctx, principal(ctx), id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.RevokeSessionResponse{}), nil
}

// CreateApiToken creates an API token for the picked profile.
func (s *AuthService) CreateApiToken(ctx context.Context, req *connect.Request[laternav1.CreateApiTokenRequest]) (*connect.Response[laternav1.CreateApiTokenResponse], error) { //nolint:revive // name generated from the contract
	token, t, err := s.app.CreateAPIToken(ctx, principal(ctx), req.Msg.GetName(), req.Msg.GetReadOnly())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.CreateApiTokenResponse{Token: token, ApiToken: apiTokenMsg(t)}), nil
}

// ListApiTokens lists the API tokens of the account.
func (s *AuthService) ListApiTokens(ctx context.Context, _ *connect.Request[laternav1.ListApiTokensRequest]) (*connect.Response[laternav1.ListApiTokensResponse], error) { //nolint:revive // name generated from the contract
	list, err := s.app.APITokens(ctx, principal(ctx))
	if err != nil {
		return nil, err
	}
	out := make([]*laternav1.ApiToken, len(list))
	for i, t := range list {
		out[i] = apiTokenMsg(t)
	}
	return connect.NewResponse(&laternav1.ListApiTokensResponse{ApiTokens: out}), nil
}

// RevokeApiToken revokes an API token of the account.
func (s *AuthService) RevokeApiToken(ctx context.Context, req *connect.Request[laternav1.RevokeApiTokenRequest]) (*connect.Response[laternav1.RevokeApiTokenResponse], error) { //nolint:revive // name generated from the contract
	id, err := parseID(req.Msg.GetApiTokenId(), "api_token_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.RevokeAPIToken(ctx, principal(ctx), id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.RevokeApiTokenResponse{}), nil
}

func apiTokenMsg(t app.APIToken) *laternav1.ApiToken {
	s := t.Session
	msg := &laternav1.ApiToken{
		Id: s.ID.String(), Name: s.Device.Name, ReadOnly: s.ReadOnly,
		CreatedAt: timestamppb.New(s.CreatedAt), LastUsedAt: timestamppb.New(s.LastUsedAt), LastIp: s.LastIP,
	}
	if t.Profile != nil {
		msg.Profile = profileMsg(*t.Profile)
	}
	return msg
}

// ChangePassword changes the account's password.
func (s *AuthService) ChangePassword(ctx context.Context, req *connect.Request[laternav1.ChangePasswordRequest]) (*connect.Response[laternav1.ChangePasswordResponse], error) {
	if err := s.app.ChangePassword(ctx, principal(ctx), req.Msg.GetCurrentPassword(), req.Msg.GetNewPassword()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.ChangePasswordResponse{}), nil
}
