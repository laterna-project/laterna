package rpc

import (
	"context"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// ImportService implements laterna.v1.ImportService.
type ImportService struct {
	app *app.App
}

var jellyfinTargets = map[app.JellyfinTargetKind]laternav1.JellyfinTargetKind{
	app.JellyfinNewAccount: laternav1.JellyfinTargetKind_JELLYFIN_TARGET_KIND_NEW_ACCOUNT,
	app.JellyfinNewProfile: laternav1.JellyfinTargetKind_JELLYFIN_TARGET_KIND_NEW_PROFILE,
	app.JellyfinProfile:    laternav1.JellyfinTargetKind_JELLYFIN_TARGET_KIND_PROFILE,
	app.JellyfinSkip:       laternav1.JellyfinTargetKind_JELLYFIN_TARGET_KIND_SKIP,
}

// PreviewJellyfinImport says what an import would bring in.
func (s *ImportService) PreviewJellyfinImport(ctx context.Context, req *connect.Request[laternav1.PreviewJellyfinImportRequest]) (*connect.Response[laternav1.PreviewJellyfinImportResponse], error) {
	plan, err := s.app.PreviewJellyfinImport(ctx, req.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	resp := &laternav1.PreviewJellyfinImportResponse{
		OrphanedUserData: int32(plan.Orphaned), UnsupportedUserData: int32(plan.Unsupported), //nolint:gosec // counts bounded by the Jellyfin database
		UnpairedSessions: int32(plan.Unpaired), UnmatchedFiles: int32(plan.UnmatchedFiles), //nolint:gosec // same
		UnmatchedSamples: plan.UnmatchedSamples,
	}
	for _, u := range plan.Users {
		resp.Users = append(resp.Users, &laternav1.JellyfinUser{
			Id: u.ID, Name: u.Name, Administrator: u.Admin, Disabled: u.Disabled, HasPassword: u.HasPassword,
			Target: jellyfinTargetMsg(u.Target), AccountName: u.AccountName, ProfileName: u.ProfileName,
			UserData: int32(u.UserData), UserDataMatched: int32(u.UserDataMatched), //nolint:gosec // same
			Sessions: int32(u.Sessions), SessionsMatched: int32(u.SessionsMatched), //nolint:gosec // same
			Problem: render(ctx, u.Problem), ProblemText: textMsg(ctx, u.Problem),
		})
	}
	return connect.NewResponse(resp), nil
}

// ImportJellyfin imports the data of a Jellyfin server.
func (s *ImportService) ImportJellyfin(ctx context.Context, req *connect.Request[laternav1.ImportJellyfinRequest]) (*connect.Response[laternav1.ImportJellyfinResponse], error) {
	targets := map[string]app.JellyfinTarget{}
	for id, t := range req.Msg.GetTargets() {
		target, err := jellyfinTargetOf(t)
		if err != nil {
			return nil, err
		}
		targets[id] = target
	}
	done, err := s.app.ImportJellyfin(ctx, principal(ctx), req.Msg.GetPath(), targets)
	if err != nil {
		return nil, err
	}
	resp := &laternav1.ImportJellyfinResponse{}
	for _, d := range done {
		resp.Users = append(resp.Users, &laternav1.JellyfinImport{
			Id: d.ID, Name: d.Name, AccountId: d.AccountID.String(), ProfileId: d.ProfileID.String(), ProfileName: d.ProfileName,
			AccountCreated: d.AccountCreated, ProfileCreated: d.ProfileCreated,
			UserData: int32(d.UserData), History: int32(d.History), //nolint:gosec // same
		})
	}
	return connect.NewResponse(resp), nil
}

func jellyfinTargetMsg(t app.JellyfinTarget) *laternav1.JellyfinTarget {
	msg := &laternav1.JellyfinTarget{Kind: jellyfinTargets[t.Kind]}
	switch t.Kind {
	case app.JellyfinNewProfile:
		msg.AccountId = t.AccountID.String()
	case app.JellyfinProfile:
		msg.AccountId, msg.ProfileId = t.AccountID.String(), t.ProfileID.String()
	case app.JellyfinNewAccount, app.JellyfinSkip:
	}
	return msg
}

func jellyfinTargetOf(t *laternav1.JellyfinTarget) (app.JellyfinTarget, error) {
	var out app.JellyfinTarget
	var err error
	switch t.GetKind() {
	case laternav1.JellyfinTargetKind_JELLYFIN_TARGET_KIND_NEW_ACCOUNT:
		out.Kind = app.JellyfinNewAccount
	case laternav1.JellyfinTargetKind_JELLYFIN_TARGET_KIND_NEW_PROFILE:
		out.Kind = app.JellyfinNewProfile
		out.AccountID, err = parseID(t.GetAccountId(), "account_id")
	case laternav1.JellyfinTargetKind_JELLYFIN_TARGET_KIND_PROFILE:
		out.Kind = app.JellyfinProfile
		out.ProfileID, err = parseID(t.GetProfileId(), "profile_id")
	case laternav1.JellyfinTargetKind_JELLYFIN_TARGET_KIND_SKIP:
		out.Kind = app.JellyfinSkip
	case laternav1.JellyfinTargetKind_JELLYFIN_TARGET_KIND_UNSPECIFIED:
		return out, domain.Invalid("import.target_required")
	default:
		return out, domain.Invalid("import.unknown_target", "target", t.GetKind())
	}
	return out, err
}
