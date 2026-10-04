package rpc

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/domain"
)

// Passkeys: authenticator options and responses travel as JSON, as WebAuthn defines them.

// BeginPasskeyRegistration prepares registering a passkey.
func (s *AuthService) BeginPasskeyRegistration(ctx context.Context, _ *connect.Request[laternav1.BeginPasskeyRegistrationRequest]) (*connect.Response[laternav1.BeginPasskeyRegistrationResponse], error) {
	id, opts, err := s.app.BeginPasskeyRegistration(ctx, principal(ctx))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.BeginPasskeyRegistrationResponse{RegistrationId: id, OptionsJson: string(opts)}), nil
}

// FinishPasskeyRegistration registers a passkey.
func (s *AuthService) FinishPasskeyRegistration(ctx context.Context, req *connect.Request[laternav1.FinishPasskeyRegistrationRequest]) (*connect.Response[laternav1.FinishPasskeyRegistrationResponse], error) {
	m := req.Msg
	pk, err := s.app.FinishPasskeyRegistration(ctx, principal(ctx), m.GetRegistrationId(), []byte(m.GetCredentialJson()), m.GetName())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.FinishPasskeyRegistrationResponse{Passkey: passkeyMsg(pk)}), nil
}

// ListPasskeys lists the passkeys of the account.
func (s *AuthService) ListPasskeys(ctx context.Context, _ *connect.Request[laternav1.ListPasskeysRequest]) (*connect.Response[laternav1.ListPasskeysResponse], error) {
	keys, err := s.app.Passkeys(ctx, principal(ctx))
	if err != nil {
		return nil, err
	}
	resp := &laternav1.ListPasskeysResponse{}
	for _, pk := range keys {
		resp.Passkeys = append(resp.Passkeys, passkeyMsg(pk))
	}
	return connect.NewResponse(resp), nil
}

// DeletePasskey removes a passkey.
func (s *AuthService) DeletePasskey(ctx context.Context, req *connect.Request[laternav1.DeletePasskeyRequest]) (*connect.Response[laternav1.DeletePasskeyResponse], error) {
	id, err := parseID(req.Msg.GetPasskeyId(), "passkey_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.DeletePasskey(ctx, principal(ctx), id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.DeletePasskeyResponse{}), nil
}

// BeginPasskeyLogin prepares a passkey login.
func (s *AuthService) BeginPasskeyLogin(context.Context, *connect.Request[laternav1.BeginPasskeyLoginRequest]) (*connect.Response[laternav1.BeginPasskeyLoginResponse], error) {
	id, opts, err := s.app.BeginPasskeyLogin()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.BeginPasskeyLoginResponse{LoginId: id, OptionsJson: string(opts)}), nil
}

// FinishPasskeyLogin opens a session with a passkey.
func (s *AuthService) FinishPasskeyLogin(ctx context.Context, req *connect.Request[laternav1.FinishPasskeyLoginRequest]) (*connect.Response[laternav1.FinishPasskeyLoginResponse], error) {
	m := req.Msg
	l, err := s.app.FinishPasskeyLogin(ctx, m.GetLoginId(), []byte(m.GetCredentialJson()), deviceFromMsg(m.GetDevice()), clientIP(ctx, req.Peer()))
	if err != nil {
		return nil, err
	}
	session, token := loginResponse(l)
	return connect.NewResponse(&laternav1.FinishPasskeyLoginResponse{Session: session, Token: token}), nil
}

func passkeyMsg(pk domain.Passkey) *laternav1.Passkey {
	msg := &laternav1.Passkey{Id: pk.ID.String(), Name: pk.Name, CreatedAt: timestamppb.New(pk.CreatedAt)}
	if pk.LastUsedAt != nil {
		msg.LastUsedAt = timestamppb.New(*pk.LastUsedAt)
	}
	return msg
}
