package rpc

import (
	"context"

	"connectrpc.com/connect"

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

// ChangePassword changes the account's password.
func (s *AuthService) ChangePassword(ctx context.Context, req *connect.Request[laternav1.ChangePasswordRequest]) (*connect.Response[laternav1.ChangePasswordResponse], error) {
	if err := s.app.ChangePassword(ctx, principal(ctx), req.Msg.GetCurrentPassword(), req.Msg.GetNewPassword()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.ChangePasswordResponse{}), nil
}
