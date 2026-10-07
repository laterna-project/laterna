package rpc

import (
	"context"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
)

// ProfileService implements laterna.v1.ProfileService.
type ProfileService struct {
	app *app.App
}

// ListProfiles lists the profiles of the account.
func (s *ProfileService) ListProfiles(ctx context.Context, _ *connect.Request[laternav1.ListProfilesRequest]) (*connect.Response[laternav1.ListProfilesResponse], error) {
	list, err := s.app.Profiles(ctx, principal(ctx))
	if err != nil {
		return nil, err
	}
	out := make([]*laternav1.Profile, len(list))
	for i, p := range list {
		out[i] = profileMsg(p)
	}
	return connect.NewResponse(&laternav1.ListProfilesResponse{Profiles: out}), nil
}

// CreateProfile adds a profile.
func (s *ProfileService) CreateProfile(ctx context.Context, req *connect.Request[laternav1.CreateProfileRequest]) (*connect.Response[laternav1.CreateProfileResponse], error) {
	m := req.Msg
	p, err := s.app.CreateProfile(ctx, principal(ctx), m.GetName(), m.GetPin(), m.GetKid(), parentalFromMsg(m.GetParental()), m.GetLanguage())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.CreateProfileResponse{Profile: profileMsg(p)}), nil
}

// UpdateProfile changes a profile.
func (s *ProfileService) UpdateProfile(ctx context.Context, req *connect.Request[laternav1.UpdateProfileRequest]) (*connect.Response[laternav1.UpdateProfileResponse], error) {
	m := req.Msg
	id, err := parseID(m.GetProfileId(), "profile_id")
	if err != nil {
		return nil, err
	}
	p, err := s.app.UpdateProfile(ctx, principal(ctx), id, m.GetCurrentPin(), app.ProfileChanges{
		Name: m.Name, PIN: m.Pin, Kid: m.Kid, Parental: parentalFromMsg(m.GetParental()), Language: m.Language,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.UpdateProfileResponse{Profile: profileMsg(p)}), nil
}

// SetSubtitlePreferences changes when playback starts with a subtitle, for the picked profile.
func (s *ProfileService) SetSubtitlePreferences(ctx context.Context, req *connect.Request[laternav1.SetSubtitlePreferencesRequest]) (*connect.Response[laternav1.SetSubtitlePreferencesResponse], error) {
	p, err := s.app.SetSubtitlePreferences(ctx, principal(ctx), subtitleModeFromMsg(req.Msg.GetMode()), req.Msg.GetLanguage())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.SetSubtitlePreferencesResponse{Profile: profileMsg(p)}), nil
}

// SetLanguage changes the language of the picked profile.
func (s *ProfileService) SetLanguage(ctx context.Context, req *connect.Request[laternav1.SetLanguageRequest]) (*connect.Response[laternav1.SetLanguageResponse], error) {
	p, err := s.app.SetLanguage(ctx, principal(ctx), req.Msg.GetLanguage())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.SetLanguageResponse{Profile: profileMsg(p)}), nil
}

// DeleteProfile deletes a profile.
func (s *ProfileService) DeleteProfile(ctx context.Context, req *connect.Request[laternav1.DeleteProfileRequest]) (*connect.Response[laternav1.DeleteProfileResponse], error) {
	id, err := parseID(req.Msg.GetProfileId(), "profile_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.DeleteProfile(ctx, principal(ctx), id, req.Msg.GetCurrentPin()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.DeleteProfileResponse{}), nil
}

// SelectProfile picks the profile of the session.
func (s *ProfileService) SelectProfile(ctx context.Context, req *connect.Request[laternav1.SelectProfileRequest]) (*connect.Response[laternav1.SelectProfileResponse], error) {
	id, err := parseID(req.Msg.GetProfileId(), "profile_id")
	if err != nil {
		return nil, err
	}
	p, err := s.app.SelectProfile(ctx, principal(ctx), id, req.Msg.GetPin())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.SelectProfileResponse{Profile: profileMsg(p)}), nil
}
