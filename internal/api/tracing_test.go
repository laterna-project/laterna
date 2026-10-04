package api

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/oidc/oidctest"
	"github.com/laterna-project/laterna/internal/store"
	"github.com/laterna-project/laterna/internal/telemetry"
	"github.com/laterna-project/laterna/internal/telemetry/telemetrytest"
)

// syncBuffer is a log shared by the server and the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Tracing end to end: a request continues the caller's trace and is named after its route; an API
// call is named after its procedure, and an expected refusal is not an error; the access log gives
// the trace ID; a background job has its own span; an outgoing call is a child of the request that
// makes it.
func TestTracing(t *testing.T) {
	ctx := context.Background()
	col := telemetrytest.New(t)
	tracer, err := telemetry.FromEnv(ctx, col.Env(), "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	var logs syncBuffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	a, err := app.New(ctx, st, app.Options{ServerName: "Test", CacheDir: t.TempDir(), Tracer: tracer, NoAutoScans: true, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	if err := a.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(a, Options{Logger: log}))
	t.Cleanup(func() { srv.Close(); cancel(); a.Wait(); tracer.Shutdown(ctx); _ = st.Close() })

	// A request that passes its trace on.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// API calls: one that succeeds, one that is refused (as expected), one with an outgoing call.
	login, err := a.Setup(ctx, "admin", "a-strong-password", domain.Device{Name: "Test", Client: "Test", ClientVersion: "1", Platform: "Go"}, "")
	if err != nil {
		t.Fatal(err)
	}
	catalog := laternav1connect.NewCatalogServiceClient(srv.Client(), srv.URL)
	if _, err := catalog.GetMovie(ctx, authed(&laternav1.GetMovieRequest{MovieId: domain.NewID().String()}, login.Token)); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown movie: %v", err)
	}
	idp := oidctest.New("laterna", "secret")
	t.Cleanup(idp.Close)
	system := laternav1connect.NewSystemServiceClient(srv.Client(), srv.URL)
	if _, err := system.SetOidcProvider(ctx, authed(&laternav1.SetOidcProviderRequest{Issuer: idp.URL, ClientId: "laterna", ClientSecret: "secret"}, login.Token)); err != nil {
		t.Fatal(err)
	}
	// A background job.
	if _, err := system.RunTask(ctx, authed(&laternav1.RunTaskRequest{Task: laternav1.SystemTask_SYSTEM_TASK_PURGE}, login.Token)); err != nil {
		t.Fatal(err)
	}

	var job telemetrytest.Span
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		tracer.Flush(ctx)
		found := false
		for _, s := range col.Spans() {
			if strings.HasPrefix(s.Name, "job ") {
				job, found = s, true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no job span: %+v", col.Spans())
		}
	}

	health, ok := col.Find("GET /health")
	if !ok || health.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || health.ParentSpanID != "00f067aa0ba902b7" ||
		health.Kind != int(telemetry.Server) || health.Attributes["http.route"] != "/health" || health.Attributes["http.response.status_code"] != "200" {
		t.Errorf("/health span: %+v", health)
	}
	if !strings.Contains(logs.String(), "trace_id=4bf92f3577b34da6a3ce929d0e0e4736") {
		t.Error("trace ID missing from the access log")
	}
	movie, ok := col.Find("laterna.v1.CatalogService/GetMovie")
	if !ok || movie.Attributes["rpc.method"] != "GetMovie" || movie.Attributes["rpc.connect_rpc.error_code"] != "not_found" || movie.StatusCode != 0 {
		t.Errorf("GetMovie span: %+v", movie)
	}
	setOIDC, ok := col.Find("laterna.v1.SystemService/SetOidcProvider")
	if !ok {
		t.Fatalf("SetOidcProvider span missing: %+v", col.Spans())
	}
	var discovery telemetrytest.Span
	for _, s := range col.Spans() {
		if s.Kind == int(telemetry.Client) && s.ParentSpanID == setOIDC.SpanID {
			discovery = s
		}
	}
	if discovery.Name != "GET" || discovery.TraceID != setOIDC.TraceID || discovery.Attributes["http.response.status_code"] != "200" {
		t.Errorf("outgoing call: %+v", discovery)
	}
	if job.ParentSpanID != "" || job.Attributes["job.kind"] == "" {
		t.Errorf("job span: %+v", job)
	}
}
