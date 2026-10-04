package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// collector is a fake OTLP/HTTP collector that keeps the spans it receives.
type collector struct {
	*httptest.Server
	mu    sync.Mutex
	spans []otlpSpan
	res   []otlpKeyValue
	auth  string
}

func newCollector(t *testing.T) *collector {
	t.Helper()
	c := &collector{}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var req otlpRequest
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		c.mu.Lock()
		c.auth = r.Header.Get("Authorization")
		for _, rs := range req.ResourceSpans {
			c.res = rs.Resource.Attributes
			for _, ss := range rs.ScopeSpans {
				c.spans = append(c.spans, ss.Spans...)
			}
		}
		c.mu.Unlock()
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *collector) byName() map[string]otlpSpan {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]otlpSpan{}
	for _, s := range c.spans {
		out[s.Name] = s
	}
	return out
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func attr(s otlpSpan, key string) string {
	for _, kv := range s.Attributes {
		if kv.Key == key {
			switch {
			case kv.Value.StringValue != nil:
				return *kv.Value.StringValue
			case kv.Value.IntValue != nil:
				return *kv.Value.IntValue
			}
		}
	}
	return ""
}

func TestExportAndPropagation(t *testing.T) {
	col := newCollector(t)
	tr, err := FromEnv(context.Background(), env(map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": col.URL, "OTEL_EXPORTER_OTLP_HEADERS": "Authorization=Bearer%20secret",
		"OTEL_RESOURCE_ATTRIBUTES": "deployment.environment=test",
	}), "1.2.3", nil)
	if err != nil || !tr.Enabled() {
		t.Fatalf("tracer: %v", err)
	}
	// A downstream service gets the trace through the traceparent header.
	var seen string
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r.Header.Get("Traceparent") }))
	defer upstream.Close()
	client := &http.Client{Transport: tr.Transport(nil)}

	incoming := http.Header{}
	incoming.Set("Traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	ctx := tr.Extract(context.Background(), incoming)
	ctx, server := tr.Start(ctx, "GET", Server, String("http.request.method", "GET"))
	server.SetName("GET /images/{id}/{hash}")
	jobCtx, job := tr.Start(ctx, "job", Internal, Int("test", 3))
	req, _ := http.NewRequestWithContext(jobCtx, http.MethodGet, upstream.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	job.Fail("disk full")
	job.End()
	job.End() // only once
	server.End()
	tr.Flush(context.Background())

	spans := col.byName()
	if len(spans) != 3 {
		t.Fatalf("spans: %+v", spans)
	}
	root, child, out := spans["GET /images/{id}/{hash}"], spans["job"], spans["GET"]
	if root.TraceID != "0af7651916cd43dd8448eb211c80319c" || root.ParentSpanID != "b7ad6b7169203331" || root.Kind != Server {
		t.Errorf("request span: %+v", root)
	}
	if child.ParentSpanID != root.SpanID || child.Status.Code != statusError || child.Status.Message != "disk full" || attr(child, "test") != "3" {
		t.Errorf("job span: %+v", child)
	}
	if out.ParentSpanID != child.SpanID || out.Kind != Client || attr(out, "http.response.status_code") != "200" {
		t.Errorf("outgoing span: %+v", out)
	}
	if seen != "00-"+root.TraceID+"-"+out.SpanID+"-01" {
		t.Errorf("traceparent passed on: %q", seen)
	}
	col.mu.Lock()
	if col.auth != "Bearer secret" {
		t.Errorf("collector header: %q", col.auth)
	}
	res := map[string]string{}
	for _, kv := range col.res {
		res[kv.Key] = *kv.Value.StringValue
	}
	col.mu.Unlock()
	if res["service.name"] != "laterna" || res["service.version"] != "1.2.3" || res["deployment.environment"] != "test" {
		t.Errorf("resource: %v", res)
	}
	tr.Shutdown(context.Background())
}

// A trace the caller chose not to sample (flag 00) is not exported, and neither are its children. A
// disabled tracer does nothing.
func TestSampling(t *testing.T) {
	col := newCollector(t)
	tr, err := FromEnv(context.Background(), env(map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": col.URL + "/v1/traces"}), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Set("Traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00")
	ctx, s := tr.Start(tr.Extract(context.Background(), h), "rejected", Server)
	_, c := tr.Start(ctx, "enfant", Internal)
	if s.TraceID() != "" {
		t.Error("trace ID of an unsampled trace given to the logs")
	}
	c.End()
	s.End()
	tr.Flush(context.Background())
	if n := len(col.byName()); n != 0 {
		t.Errorf("%d spans exported", n)
	}
	tr.Shutdown(context.Background())

	off := Disabled()
	ctx, span := off.Start(context.Background(), "x", Internal)
	span.SetAttributes(String("a", "b"))
	span.Fail("rien")
	span.End()
	if span != nil || SpanFromContext(ctx) != nil || off.Transport(nil) != http.DefaultTransport {
		t.Error("disabled tracer is enabled")
	}

	half := sampler{ratio: 0.5}
	kept := 0
	for i := range 10000 {
		var id [16]byte
		id[8], id[9], id[10] = byte(i), byte(i>>8), byte(i*7)
		if half.sample(id) {
			kept++
		}
	}
	if kept < 4000 || kept > 6000 {
		t.Errorf("50%% sampling: %d out of 10,000", kept)
	}
}

func TestFromEnv(t *testing.T) {
	for name, c := range map[string]struct {
		env     map[string]string
		enabled bool
		err     string
	}{
		"no collector": {env: map[string]string{}},
		"disabled":     {env: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318", "OTEL_SDK_DISABLED": "true"}},
		"jamais":       {env: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318", "OTEL_TRACES_SAMPLER": "always_off"}},
		"actif":        {env: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318", "OTEL_TRACES_SAMPLER": "parentbased_traceidratio", "OTEL_TRACES_SAMPLER_ARG": "0.1"}, enabled: true},
		"adresse":      {env: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "c:4318"}, err: "invalid"},
		"part":         {env: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318", "OTEL_TRACES_SAMPLER_ARG": "2"}, err: "ratio"},
		"sampler":      {env: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318", "OTEL_TRACES_SAMPLER": "jaeger_remote"}, err: "not supported"},
		"headers":      {env: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318", "OTEL_EXPORTER_OTLP_HEADERS": "sansvaleur"}, err: "key=value"},
	} {
		tr, err := FromEnv(context.Background(), env(c.env), "", nil)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if err != nil || tr.Enabled() != c.enabled {
			t.Errorf("%s: enabled %v, %v", name, tr.Enabled(), err)
		}
		tr.Shutdown(context.Background())
	}
}

func TestTraceparent(t *testing.T) {
	for v, ok := range map[string]bool{
		"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01":      true,
		"01-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01-plus": true, // future version: extra fields are allowed
		"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01-plus": false,
		"00-00000000000000000000000000000000-b7ad6b7169203331-01":      false,
		"00-0af7651916cd43dd8448eb211c80319c-0000000000000000-01":      false,
		"ff-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01":      false,
		"00-zzf7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01":      false,
		"": false,
	} {
		if _, got := parseTraceparent(v); got != ok {
			t.Errorf("%q: %v", v, got)
		}
	}
}

// An unreachable collector blocks nothing: spans are dropped and counted.
func TestUnreachableCollector(t *testing.T) {
	tr, err := FromEnv(context.Background(), env(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:1"}), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for range queueSize + 100 {
		_, s := tr.Start(context.Background(), "x", Internal)
		s.End()
	}
	if time.Since(start) > time.Second {
		t.Error("spans blocked by the collector")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tr.Shutdown(ctx)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Error("shutdown blocked")
	}
}
