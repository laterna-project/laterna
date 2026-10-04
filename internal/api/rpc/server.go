package rpc

import (
	"context"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/buildinfo"
	"github.com/laterna-project/laterna/internal/i18n"
)

// ServerService implements laterna.v1.ServerService.
type ServerService struct {
	app *app.App
}

// GetServerInfo returns the server's identity and version.
func (s *ServerService) GetServerInfo(
	ctx context.Context, _ *connect.Request[laternav1.GetServerInfoRequest],
) (*connect.Response[laternav1.GetServerInfoResponse], error) {
	setup, err := s.app.SetupRequired(ctx)
	if err != nil {
		return nil, err
	}
	srv := s.app.Server()
	return connect.NewResponse(&laternav1.GetServerInfoResponse{
		Id:            srv.ID.String(),
		Name:          srv.Name,
		Version:       buildinfo.Version,
		Commit:        buildinfo.Commit(),
		Passkeys:      s.app.PasskeysAvailable(),
		OidcProvider:  s.app.OIDCButton(),
		SetupRequired: setup,
		Language:      string(s.app.Language()),
		Languages:     languages(),
		PublicUrl:     s.app.Settings().PublicURL,
	}), nil
}

// languages returns the languages the server can write its texts in.
func languages() []string {
	langs := i18n.Languages()
	out := make([]string, len(langs))
	for i, l := range langs {
		out[i] = string(l)
	}
	return out
}
