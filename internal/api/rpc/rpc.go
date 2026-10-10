// Package rpc implements the Connect services of the contract (proto/laterna/v1): it turns messages
// into calls to app and results into messages. There is no business logic here.
package rpc

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/api/httpx"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/i18n"
)

// Maximum sizes of received messages.
const (
	maxRequestBytes      = 4 << 20
	maxThemeRequestBytes = 32 << 20
)

// Mount registers every Connect service on mux.
func Mount(mux *http.ServeMux, a *app.App, log *slog.Logger) {
	opts := []connect.HandlerOption{
		// The first interceptor is the outermost: metrics see the final codes, and authentication
		// errors go through error translation too.
		connect.WithInterceptors(newMetricsInterceptor(a.Metrics()), traceInterceptor{}, errorInterceptor{log: log}, &authInterceptor{app: a}),
		// Maximum size of a received message (once decompressed): beyond it, RESOURCE_EXHAUSTED
		// without reading any further.
		connect.WithReadMaxBytes(maxRequestBytes),
	}
	mux.Handle(laternav1connect.NewServerServiceHandler(&ServerService{app: a}, opts...))
	mux.Handle(laternav1connect.NewAuthServiceHandler(&AuthService{app: a}, opts...))
	mux.Handle(laternav1connect.NewProfileServiceHandler(&ProfileService{app: a}, opts...))
	mux.Handle(laternav1connect.NewLibraryServiceHandler(&LibraryService{app: a}, opts...))
	mux.Handle(laternav1connect.NewCatalogServiceHandler(&CatalogService{app: a}, opts...))
	mux.Handle(laternav1connect.NewHomeServiceHandler(&HomeService{app: a}, opts...))
	mux.Handle(laternav1connect.NewEventServiceHandler(&EventService{app: a}, opts...))
	mux.Handle(laternav1connect.NewPlaybackServiceHandler(&PlaybackService{app: a}, opts...))
	mux.Handle(laternav1connect.NewIntegrationServiceHandler(&IntegrationService{app: a}, opts...))
	mux.Handle(laternav1connect.NewAccountServiceHandler(&AccountService{app: a}, opts...))
	mux.Handle(laternav1connect.NewSystemServiceHandler(&SystemService{app: a}, opts...))
	mux.Handle(laternav1connect.NewActivityServiceHandler(&ActivityService{app: a}, opts...))
	mux.Handle(laternav1connect.NewCollectionServiceHandler(&CollectionService{app: a}, opts...))
	mux.Handle(laternav1connect.NewPlaylistServiceHandler(&PlaylistService{app: a}, opts...))
	mux.Handle(laternav1connect.NewHistoryServiceHandler(&HistoryService{app: a}, opts...))
	mux.Handle(laternav1connect.NewMusicServiceHandler(&MusicService{app: a}, opts...))
	mux.Handle(laternav1connect.NewDownloadServiceHandler(&DownloadService{app: a}, opts...))
	mux.Handle(laternav1connect.NewPartyServiceHandler(&PartyService{app: a}, opts...))
	mux.Handle(laternav1connect.NewBookServiceHandler(&BookService{app: a}, opts...))
	mux.Handle(laternav1connect.NewPhotoServiceHandler(&PhotoService{app: a}, opts...))
	mux.Handle(laternav1connect.NewImportServiceHandler(&ImportService{app: a}, opts...))
	mux.Handle(laternav1connect.NewRequestServiceHandler(&RequestService{app: a}, opts...))
	mux.Handle(laternav1connect.NewSubtitleServiceHandler(&SubtitleService{app: a}, opts...))
	// Themes receive images (8 MiB) and exported files that hold two of them.
	mux.Handle(laternav1connect.NewThemeServiceHandler(&ThemeService{app: a}, append(opts, connect.WithReadMaxBytes(maxThemeRequestBytes))...))
}

// errorInterceptor turns errors into Connect errors. Expected errors (domain.Error) go through with
// their code and params (a laterna.v1.ErrorDetail detail) and a message written in the language of
// the request. Any other error is logged and the client only gets "server.internal", never the
// detail of a database or file error.
type errorInterceptor struct {
	log *slog.Logger
}

func (i errorInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		resp, err := next(ctx, req)
		if err == nil {
			return resp, nil
		}
		return nil, toConnectError(ctx, i.log, req.Spec().Procedure, err)
	}
}

func (i errorInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := next(ctx, conn); err != nil {
			return toConnectError(ctx, i.log, conn.Spec().Procedure, err)
		}
		return nil
	}
}

func (i errorInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

var codes = []struct {
	kind error
	code connect.Code
}{
	{domain.ErrNotFound, connect.CodeNotFound},
	{domain.ErrInvalid, connect.CodeInvalidArgument},
	{domain.ErrConflict, connect.CodeAlreadyExists},
	{domain.ErrUnauthenticated, connect.CodeUnauthenticated},
	{domain.ErrForbidden, connect.CodePermissionDenied},
	{domain.ErrPrecondition, connect.CodeFailedPrecondition},
	{domain.ErrTooManyAttempts, connect.CodeResourceExhausted},
	{domain.ErrBusy, connect.CodeResourceExhausted},
}

func toConnectError(ctx context.Context, log *slog.Logger, procedure string, err error) error {
	if connectErr := (*connect.Error)(nil); errors.As(err, &connectErr) {
		return err
	}
	var de *domain.Error
	if errors.As(err, &de) {
		for _, c := range codes {
			if errors.Is(de.Kind, c.kind) {
				return detailed(ctx, c.code, de.Text())
			}
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return connect.NewError(connect.CodeCanceled, err)
	}
	log.ErrorContext(ctx, "internal error in a service",
		"procedure", procedure, "err", err, "request_id", httpx.RequestIDFrom(ctx))
	return detailed(ctx, connect.CodeInternal, domain.T("error.server.internal"))
}

// detailed builds the Connect error for an error text (key "error.<code>"): its message in the
// language of the request, and the detail carrying the code, params and causes.
func detailed(ctx context.Context, code connect.Code, t domain.Text) error {
	lang := i18n.FromContext(ctx)
	out := connect.NewError(code, errors.New(i18n.Render(lang, t)))
	detail := &laternav1.ErrorDetail{Code: strings.TrimPrefix(t.Key, "error."), Params: t.Params}
	for _, cause := range t.List {
		detail.Causes = append(detail.Causes, textIn(lang, cause))
	}
	// A detail that cannot be encoded must not hide the error: it goes out without it.
	if d, err := connect.NewErrorDetail(detail); err == nil {
		out.AddDetail(d)
	}
	return out
}
