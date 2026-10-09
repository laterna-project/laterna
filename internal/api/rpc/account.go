package rpc

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
)

// AccountService implements laterna.v1.AccountService.
type AccountService struct {
	app *app.App
}

// ListAccounts lists the accounts.
func (s *AccountService) ListAccounts(ctx context.Context, _ *connect.Request[laternav1.ListAccountsRequest]) (*connect.Response[laternav1.ListAccountsResponse], error) {
	list, err := s.app.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*laternav1.AccountSummary, len(list))
	for i, a := range list {
		out[i] = &laternav1.AccountSummary{Account: accountMsg(a.Account), ProfileCount: clampInt32(a.Profiles)}
		if a.LastActive != nil {
			out[i].LastActiveAt = timestamppb.New(*a.LastActive)
		}
	}
	return connect.NewResponse(&laternav1.ListAccountsResponse{Accounts: out}), nil
}

// CreateAccount creates an account.
func (s *AccountService) CreateAccount(ctx context.Context, req *connect.Request[laternav1.CreateAccountRequest]) (*connect.Response[laternav1.CreateAccountResponse], error) {
	m := req.Msg
	libs, err := libraryAccessFromMsg(m.GetLibraries())
	if err != nil {
		return nil, err
	}
	a, err := s.app.CreateAccount(ctx, principal(ctx), app.NewAccount{
		Username: m.GetUsername(), Password: m.GetPassword(), IsAdmin: m.GetIsAdmin(),
		Libraries: libs, Parental: parentalFromMsg(m.GetParental()), DenyDownloads: m.GetDenyDownloads(),
		DenyRequests: m.GetDenyRequests(), AutoApproveRequests: m.GetAutoApproveRequests(), RequestQuota: optInt(m.RequestQuota),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.CreateAccountResponse{Account: accountMsg(a)}), nil
}

// UpdateAccount changes an account.
func (s *AccountService) UpdateAccount(ctx context.Context, req *connect.Request[laternav1.UpdateAccountRequest]) (*connect.Response[laternav1.UpdateAccountResponse], error) {
	m := req.Msg
	id, err := parseID(m.GetAccountId(), "account_id")
	if err != nil {
		return nil, err
	}
	libs, err := libraryAccessFromMsg(m.GetLibraries())
	if err != nil {
		return nil, err
	}
	a, err := s.app.UpdateAccount(ctx, principal(ctx), id, app.AccountChanges{
		Username: m.Username, Password: m.Password, IsAdmin: m.IsAdmin, Disabled: m.Disabled,
		Libraries: libs, Parental: parentalFromMsg(m.GetParental()), DenyDownloads: m.DenyDownloads,
		DenyRequests: m.DenyRequests, AutoApproveRequests: m.AutoApproveRequests, RequestQuota: optInt(m.RequestQuota),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.UpdateAccountResponse{Account: accountMsg(a)}), nil
}

// DeleteAccount deletes an account.
func (s *AccountService) DeleteAccount(ctx context.Context, req *connect.Request[laternav1.DeleteAccountRequest]) (*connect.Response[laternav1.DeleteAccountResponse], error) {
	id, err := parseID(req.Msg.GetAccountId(), "account_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.DeleteAccount(ctx, principal(ctx), id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.DeleteAccountResponse{}), nil
}
