package rpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
)

func callThrough(t *testing.T, fail error) (string, error) {
	t.Helper()
	var logs bytes.Buffer
	interceptor := errorInterceptor{log: slog.New(slog.NewTextHandler(&logs, nil))}
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) { return nil, fail }
	_, err := interceptor.WrapUnary(next)(context.Background(), connect.NewRequest(&laternav1.GetServerInfoRequest{}))
	return logs.String(), err
}

func TestErrorInterceptorHidesInternalErrors(t *testing.T) {
	logs, err := callThrough(t, fmt.Errorf("reading /srv/secret.db: %w", errors.New("disk full")))
	if err == nil || connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("code %v, want internal", connect.CodeOf(err))
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "disk") {
		t.Errorf("internal detail leaked to the client: %v", err)
	}
	if !strings.Contains(logs, "disk full") {
		t.Errorf("the detail must stay in the logs: %s", logs)
	}
}

func TestErrorInterceptorKeepsTypedErrors(t *testing.T) {
	typed := connect.NewError(connect.CodeNotFound, errors.New("movie not found"))
	logs, err := callThrough(t, typed)
	if err == nil || connect.CodeOf(err) != connect.CodeNotFound || !strings.Contains(err.Error(), "movie not found") {
		t.Errorf("typed error altered: %v", err)
	}
	if logs != "" {
		t.Errorf("an expected error should not be logged: %s", logs)
	}
}

func TestErrorInterceptorCancellation(t *testing.T) {
	_, err := callThrough(t, context.Canceled)
	if connect.CodeOf(err) != connect.CodeCanceled {
		t.Errorf("code %v, want canceled", connect.CodeOf(err))
	}
}
