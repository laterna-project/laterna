package rpc

import (
	"context"
	"errors"
	"net"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/api/httpx"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/auth"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/i18n"
)

// authInterceptor enforces the access level declared in the contract (the laterna.v1.access option
// of each method) before running the call. A method without the option requires a picked profile,
// so forgetting to protect one is not possible.
type authInterceptor struct {
	app *app.App
}

type principalKey struct{}

// principal returns the authenticated caller. It is always set in methods that are not public.
func principal(ctx context.Context) domain.Principal {
	p, _ := ctx.Value(principalKey{}).(domain.Principal)
	return p
}

func (i *authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, err := i.authorize(ctx, req.Spec(), req.Header(), req.Peer())
		if err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (i *authInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, err := i.authorize(ctx, conn.Spec(), conn.RequestHeader(), conn.Peer())
		if err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

func (i *authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *authInterceptor) authorize(ctx context.Context, spec connect.Spec, header http.Header, peer connect.Peer) (context.Context, error) {
	level := accessOf(spec)
	if level == laternav1.Access_ACCESS_PUBLIC {
		if throttled[spec.Procedure] {
			if err := i.app.ThrottlePublic(clientIP(ctx, peer)); err != nil {
				return ctx, err
			}
		}
		return ctx, nil
	}
	token, ok := auth.BearerToken(header.Get("Authorization"))
	if !ok {
		return ctx, domain.Unauthenticated("auth.required")
	}
	p, err := i.app.Authenticate(ctx, token, clientIP(ctx, peer))
	if err != nil {
		return ctx, err
	}
	// When the request asked for no language, use the profile's: the refusals that follow are
	// already written in it.
	if p.Profile != nil {
		i18n.Prefer(ctx, p.Profile.Language)
	}
	if err := allowed(level, p); err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, principalKey{}, p), nil
}

// allowed checks that an authenticated caller has the required level.
func allowed(level laternav1.Access, p domain.Principal) error {
	switch level {
	case laternav1.Access_ACCESS_PUBLIC, laternav1.Access_ACCESS_ACCOUNT:
		return nil
	case laternav1.Access_ACCESS_ADMIN:
		if !p.Account.IsAdmin {
			return domain.Forbidden("auth.admin_only")
		}
		if !p.CanAdminister() {
			return domain.Forbidden("auth.restricted_profile")
		}
		return nil
	case laternav1.Access_ACCESS_UNSPECIFIED, laternav1.Access_ACCESS_PROFILE:
		if p.Profile == nil {
			return domain.Precondition("profile.required")
		}
		return nil
	default:
		return errors.New("unknown access level")
	}
}

// accessOf reads the access level the contract declares for a method.
func accessOf(spec connect.Spec) laternav1.Access {
	md, ok := spec.Schema.(protoreflect.MethodDescriptor)
	if !ok {
		return laternav1.Access_ACCESS_PROFILE
	}
	level, _ := proto.GetExtension(md.Options(), laternav1.E_Access).(laternav1.Access)
	if level == laternav1.Access_ACCESS_UNSPECIFIED {
		return laternav1.Access_ACCESS_PROFILE
	}
	return level
}

// clientIP returns the client address: the one set by httpx.ClientIP (which takes trusted proxies
// into account), otherwise the connection's.
func clientIP(ctx context.Context, p connect.Peer) string {
	if ip := httpx.ClientIPFrom(ctx); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(p.Addr)
	if err != nil {
		return p.Addr
	}
	return host
}

// throttled lists the public procedures that are expensive (argon2id, state kept in memory) and are
// rate limited per address. Polls (PollDeviceLogin, PollOidcLogin) have their own pace and cost
// nothing.
var throttled = map[string]bool{
	laternav1connect.AuthServiceSetupProcedure:              true,
	laternav1connect.AuthServiceLoginProcedure:              true,
	laternav1connect.AuthServiceStartDeviceLoginProcedure:   true,
	laternav1connect.AuthServiceBeginPasskeyLoginProcedure:  true,
	laternav1connect.AuthServiceFinishPasskeyLoginProcedure: true,
	laternav1connect.AuthServiceStartOidcLoginProcedure:     true,
}
