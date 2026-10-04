// Package httpx holds the generic HTTP building blocks: middlewares and response writing.
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	connectcors "connectrpc.com/cors"

	"github.com/laterna-project/laterna/internal/telemetry"
)

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// Chain applies the middlewares: the first one in the list is the outermost.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for _, mw := range slices.Backward(mws) {
		h = mw(h)
	}
	return h
}

// Recover turns a panic into a logged 500 response without stopping the server.
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					recovered(log, w, r, v)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func recovered(log *slog.Logger, w http.ResponseWriter, r *http.Request, v any) {
	// http.ErrAbortHandler is used to cut a response short on purpose: pass it on.
	if v == http.ErrAbortHandler { //nolint:errorlint // sentinel value compared as is
		panic(v)
	}
	log.ErrorContext(r.Context(), "panic in a handler",
		"method", r.Method, "path", r.URL.Path, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

type requestIDKey struct{}

// RequestIDHeader carries the request ID, returned to the client and used in logs.
const RequestIDHeader = "X-Request-Id"

// RequestID gives each request an ID (or reuses the one from an upstream proxy).
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(RequestIDHeader)
			if id == "" || len(id) > 64 {
				var b [8]byte
				_, _ = rand.Read(b[:])
				id = hex.EncodeToString(b[:])
			}
			w.Header().Set(RequestIDHeader, id)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
		})
	}
}

// RequestIDFrom returns the ID of the current request, or "".
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// statusClientGone means the client left before the response (an nginx convention). It is never
// sent, only logged.
const statusClientGone = 499

// AccessLog logs each request: at debug level when all is well, at info for client errors and slow
// requests, at error for 5xx.
func AccessLog(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &recorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			elapsed := time.Since(start)
			status := rec.status
			switch {
			case status == 0 && r.Context().Err() != nil:
				status = statusClientGone
			case status == 0:
				status = http.StatusOK
			}
			level := slog.LevelDebug
			switch {
			case status >= 500:
				level = slog.LevelError
			case status >= 400 || elapsed > time.Second:
				level = slog.LevelInfo
			}
			attrs := []any{
				"method", r.Method, "path", r.URL.Path, "status", status, "bytes", rec.bytes, "ip", ClientIPFrom(r.Context()),
				"duration", elapsed.Round(time.Microsecond), "request_id", RequestIDFrom(r.Context()),
			}
			if trace := telemetry.SpanFromContext(r.Context()).TraceID(); trace != "" {
				attrs = append(attrs, "trace_id", trace)
			}
			log.Log(r.Context(), level, "request", attrs...)
		})
	}
}

// Observation is a served request, for metrics.
type Observation struct {
	// Route is the ServeMux pattern that served it ("GET /images/{id}/{hash}"); "" if none.
	Route    string
	Status   int
	Bytes    int64
	Duration time.Duration
}

// Hook is called at the start of each request. It may return a new context (the request's span) and
// a function called at the end with the observation. A nil Hook is skipped.
type Hook func(r *http.Request) (context.Context, func(Observation))

// Instrument calls the hooks around each request (metrics, tracing). It sits under RequestID and
// sees the request once the ServeMux has filled in its pattern, so metrics and traces are per
// route, never per URL (which sometimes carries a secret).
func Instrument(hooks ...Hook) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ends := make([]func(Observation), 0, len(hooks))
			for _, h := range hooks {
				if h == nil {
					continue
				}
				ctx, end := h(r)
				r = r.WithContext(ctx) //nolint:contextcheck // a hook returns a context derived from r.Context()
				if end != nil {
					ends = append(ends, end)
				}
			}
			rec := &recorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			status := rec.status
			switch {
			case status == 0 && r.Context().Err() != nil:
				status = statusClientGone
			case status == 0:
				status = http.StatusOK
			}
			o := Observation{Route: r.Pattern, Status: status, Bytes: rec.bytes, Duration: time.Since(start)}
			for _, end := range slices.Backward(ends) {
				end(o)
			}
		})
	}
}

// recorder remembers the status and size of the response. It exposes Flush (Connect streams need
// it: without it a streaming call fails), Unwrap (for http.ResponseController: Hijack...) and
// ReadFrom (to keep sendfile when serving a file).
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

func (r *recorder) ReadFrom(src io.Reader) (int64, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	var n int64
	var err error
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		n, err = rf.ReadFrom(src)
	} else {
		n, err = io.Copy(r.ResponseWriter, src)
	}
	r.bytes += n
	return n, err
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *recorder) Flush() {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	_ = http.NewResponseController(r.ResponseWriter).Flush()
}

// exposedHeaders are the headers a web client from another origin may read: those of Connect and
// gRPC-Web (status, error message) and those of streams (<video crossOrigin> player, downloads).
var exposedHeaders = strings.Join(append(connectcors.ExposedHeaders(),
	"Content-Range", "Accept-Ranges", "Content-Length", "Content-Disposition", "ETag", RequestIDHeader, "Laterna-Error"), ", ")

// CORS lets in web clients served from another origin (a client in development on another port, a
// client hosted elsewhere...). Tokens travel in the Authorization header or the URL, never in a
// cookie, so allowing every origin gives access to nothing without a token.
func CORS(allowedOrigins []string) Middleware {
	allowAll := slices.Contains(allowedOrigins, "*")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" || (!allowAll && !slices.Contains(allowedOrigins, origin)) {
				next.ServeHTTP(w, r)
				return
			}
			h := w.Header()
			if allowAll {
				h.Set("Access-Control-Allow-Origin", "*")
			} else {
				h.Set("Access-Control-Allow-Origin", origin)
				h.Add("Vary", "Origin")
			}
			h.Set("Access-Control-Expose-Headers", exposedHeaders)

			if r.Method != http.MethodOptions || r.Header.Get("Access-Control-Request-Method") == "" {
				next.ServeHTTP(w, r)
				return
			}
			// Preflight request: answer without going any further.
			h.Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
			// "*" does not cover Authorization: send back the requested list.
			if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
				h.Set("Access-Control-Allow-Headers", reqHeaders)
			}
			// Chrome: a public page reaching a server on the local network.
			if strings.EqualFold(r.Header.Get("Access-Control-Request-Private-Network"), "true") {
				h.Set("Access-Control-Allow-Private-Network", "true")
			}
			h.Set("Access-Control-Max-Age", "7200")
			w.WriteHeader(http.StatusNoContent)
		})
	}
}
