// Package api puts the server's HTTP surfaces together behind the shared middlewares: the health
// probe, the Connect services of the contract, the byte routes (images, playback streams,
// subtitles, fonts, logs) and the Sonarr and Radarr webhooks.
package api

import (
	"cmp"
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/laterna-project/laterna/internal/api/httpx"
	"github.com/laterna-project/laterna/internal/api/rpc"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/auth"
	"github.com/laterna-project/laterna/internal/buildinfo"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/metrics"
	"github.com/laterna-project/laterna/internal/telemetry"
)

// Options configures the root handler.
type Options struct {
	CORSOrigins []string
	// TrustedProxies are the reverse proxies we trust.
	TrustedProxies []netip.Prefix
	Logger         *slog.Logger
}

// NewHandler returns the server's root handler.
func NewHandler(a *app.App, opts Options) http.Handler {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /health", health(a))
	mux.Handle("GET /images/{id}/{hash}", images(a, log))
	mux.Handle("GET /playback/{session}/{token}/{file}", playback(a, log))
	mux.Handle("GET /playback/{session}/{token}/subtitles/{file}", subtitle(a, log))
	mux.Handle("GET /fonts/{file}", font(a, log))
	mux.Handle("POST /hooks/{kind}", hooks(a, log))
	mux.Handle("GET "+app.OIDCCallbackPath, oidcCallback(a))
	mux.Handle("POST "+app.OIDCCallbackPath, oidcConfirm(a))
	mux.Handle("GET /logs/{file}", logFiles(a, log))
	mux.Handle("GET /backups/{file}", backupFiles(a, log))
	mux.Handle("GET /trickplay/{file}/{key}/{sheet}", trickplaySheets(a, log))
	mux.Handle("GET /books/{file}/{key}/file", bookFile(a, log))
	mux.Handle("GET /books/{file}/{key}/pages/{page}", bookPage(a, log))
	mux.Handle("GET /downloads/{id}/file", downloadFile(a, log))
	mux.Handle("GET /downloads/{id}/subtitles/{file}", downloadSubtitle(a, log))
	mux.Handle("GET "+rpc.MetricsPath, metricsHandler(a))
	rpc.Mount(mux, a, log)
	return httpx.Chain(mux,
		httpx.Recover(log),
		httpx.ClientIP(opts.TrustedProxies),
		httpx.RequestID(),
		httpx.Language(a.Language),
		httpx.Instrument(traceHTTP(a.Tracer()), httpMetrics(a.Metrics())),
		httpx.AccessLog(log),
		httpx.CORS(opts.CORSOrigins),
	)
}

// traceHTTP opens the span of each request (continuing the trace of a caller that passes one on).
// It is named after its route, or after the procedure for an API call.
func traceHTTP(t *telemetry.Tracer) httpx.Hook {
	if !t.Enabled() {
		return nil
	}
	return func(r *http.Request) (context.Context, func(httpx.Observation)) {
		ctx, span := t.Start(t.Extract(r.Context(), r.Header), r.Method, telemetry.Server,
			telemetry.String("http.request.method", r.Method))
		return ctx, func(o httpx.Observation) {
			if o.Route != "" {
				if !span.Renamed() {
					span.SetName(o.Route)
				}
				_, path, _ := strings.Cut(o.Route, " ")
				span.SetAttributes(telemetry.String("http.route", cmp.Or(path, o.Route)))
			}
			span.SetAttributes(telemetry.Int("http.response.status_code", o.Status))
			if o.Status >= 500 {
				span.Fail(http.StatusText(o.Status))
			}
			span.End()
		}
	}
}

// httpMetrics counts requests by route and status class, their duration and the bytes served.
func httpMetrics(reg *metrics.Registry) httpx.Hook {
	requests := reg.Counter("laterna_http_requests_total", "HTTP requests, by route and status class.", "route", "status")
	duration := reg.Histogram("laterna_http_request_duration_seconds", "Duration of HTTP requests.", metrics.DurationBuckets, "route")
	bytes := reg.Counter("laterna_http_response_bytes_total", "Bytes served, by route (streams, images, downloads...).", "route")
	end := func(o httpx.Observation) {
		route := o.Route
		if route == "" {
			route = "autre"
		}
		class := strconv.Itoa(o.Status/100) + "xx"
		if o.Status == 499 {
			class = "499"
		}
		requests.Inc(route, class)
		duration.Observe(o.Duration.Seconds(), route)
		bytes.Add(float64(o.Bytes), route)
	}
	return func(r *http.Request) (context.Context, func(httpx.Observation)) { return r.Context(), end }
}

// metricsHandler serves the metrics in the Prometheus text format to whoever presents the metrics
// token; not found as long as no token exists.
func metricsHandler(a *app.App) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.MetricsEnabled() {
			http.NotFound(w, r)
			return
		}
		token, ok := auth.Bearer(r.Header.Get("Authorization"))
		if !ok || !a.AuthorizeMetrics(token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="laterna-metrics"`)
			writeError(w, r, http.StatusUnauthorized, domain.T("error.metrics.token_required"))
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = a.Metrics().Write(r.Context(), w)
	})
}

type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Error   string `json:"error,omitempty"`
}

// health answers 200 if the database answers and 503 otherwise (Docker healthcheck, monitoring).
func health(a *app.App) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := healthResponse{Status: "ok", Version: buildinfo.Version, Commit: buildinfo.Commit()}
		status := http.StatusOK
		if err := a.Healthy(r.Context()); err != nil {
			resp.Status, resp.Error, status = "degraded", err.Error(), http.StatusServiceUnavailable
		}
		_ = httpx.WriteJSON(w, status, resp)
	})
}
