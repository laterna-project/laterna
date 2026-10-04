package rpc

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
)

// Device login.

var deviceLoginStates = map[app.DeviceLoginState]laternav1.DeviceLoginState{
	app.DeviceLoginPending:  laternav1.DeviceLoginState_DEVICE_LOGIN_STATE_PENDING,
	app.DeviceLoginApproved: laternav1.DeviceLoginState_DEVICE_LOGIN_STATE_APPROVED,
	app.DeviceLoginDenied:   laternav1.DeviceLoginState_DEVICE_LOGIN_STATE_DENIED,
	app.DeviceLoginExpired:  laternav1.DeviceLoginState_DEVICE_LOGIN_STATE_EXPIRED,
}

// StartDeviceLogin opens a device login request.
func (s *AuthService) StartDeviceLogin(ctx context.Context, req *connect.Request[laternav1.StartDeviceLoginRequest]) (*connect.Response[laternav1.StartDeviceLoginResponse], error) {
	st, err := s.app.StartDeviceLogin(ctx, deviceFromMsg(req.Msg.GetDevice()), clientIP(ctx, req.Peer()))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.StartDeviceLoginResponse{
		DeviceCode: st.DeviceCode, UserCode: st.UserCode, ExpiresAt: timestamppb.New(st.ExpiresAt), Interval: durationpb.New(st.Interval),
		VerificationUrl: st.VerificationURL, VerificationUrlComplete: st.VerificationURLComplete,
	}), nil
}

// PollDeviceLogin returns the state of a request and, once approved, the session.
func (s *AuthService) PollDeviceLogin(ctx context.Context, req *connect.Request[laternav1.PollDeviceLoginRequest]) (*connect.Response[laternav1.PollDeviceLoginResponse], error) {
	p, err := s.app.PollDeviceLogin(ctx, req.Msg.GetDeviceCode())
	if err != nil {
		return nil, err
	}
	resp := &laternav1.PollDeviceLoginResponse{State: deviceLoginStates[p.State]}
	if p.Interval > 0 {
		resp.Interval = durationpb.New(p.Interval)
	}
	if p.Login != nil {
		resp.Session, resp.Token = loginResponse(*p.Login)
	}
	return connect.NewResponse(resp), nil
}

// GetDeviceLogin describes the device waiting behind a code.
func (s *AuthService) GetDeviceLogin(ctx context.Context, req *connect.Request[laternav1.GetDeviceLoginRequest]) (*connect.Response[laternav1.GetDeviceLoginResponse], error) {
	r, err := s.app.DeviceLogin(ctx, principal(ctx), req.Msg.GetUserCode())
	if err != nil {
		return nil, err
	}
	d := r.Device
	return connect.NewResponse(&laternav1.GetDeviceLoginResponse{
		Device: &laternav1.Device{Name: d.Name, Client: d.Client, ClientVersion: d.ClientVersion, Platform: d.Platform},
		Ip:     r.IP, CreatedAt: timestamppb.New(r.CreatedAt), ExpiresAt: timestamppb.New(r.ExpiresAt),
	}), nil
}

// ApproveDeviceLogin signs the device in to the caller's account.
func (s *AuthService) ApproveDeviceLogin(ctx context.Context, req *connect.Request[laternav1.ApproveDeviceLoginRequest]) (*connect.Response[laternav1.ApproveDeviceLoginResponse], error) {
	if err := s.app.ApproveDeviceLogin(ctx, principal(ctx), req.Msg.GetUserCode(), req.Msg.GetSelectProfile()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.ApproveDeviceLoginResponse{}), nil
}

// DenyDeviceLogin refuses the request.
func (s *AuthService) DenyDeviceLogin(ctx context.Context, req *connect.Request[laternav1.DenyDeviceLoginRequest]) (*connect.Response[laternav1.DenyDeviceLoginResponse], error) {
	if err := s.app.DenyDeviceLogin(ctx, principal(ctx), req.Msg.GetUserCode()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.DenyDeviceLoginResponse{}), nil
}
