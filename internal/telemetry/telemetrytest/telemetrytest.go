// Package telemetrytest is a fake OpenTelemetry collector (OTLP/HTTP with JSON) for tests. It keeps
// the spans it receives.
package telemetrytest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Span is a received span, with its attributes turned into text.
type Span struct {
	TraceID, SpanID, ParentSpanID string
	Name                          string
	Kind                          int
	Attributes                    map[string]string
	StatusCode                    int
	StatusMessage                 string
}

// Collector is the fake collector. Env gives the variables that send traces to it.
type Collector struct {
	*httptest.Server
	mu    sync.Mutex
	spans []Span
}

// New starts a collector that is stopped when the test ends.
func New(t testing.TB) *Collector {
	t.Helper()
	c := &Collector{}
	c.Server = httptest.NewServer(http.HandlerFunc(c.receive))
	t.Cleanup(c.Close)
	return c
}

// Env returns an environment lookup that sends traces here.
func (c *Collector) Env() func(string) string {
	return func(k string) string {
		if k == "OTEL_EXPORTER_OTLP_ENDPOINT" {
			return c.URL
		}
		return ""
	}
}

// Spans returns the received spans.
func (c *Collector) Spans() []Span {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Span(nil), c.spans...)
}

// Find returns the first span with the given name.
func (c *Collector) Find(name string) (Span, bool) {
	for _, s := range c.Spans() {
		if s.Name == name {
			return s, true
		}
	}
	return Span{}, false
}

type value struct {
	StringValue *string  `json:"stringValue"`
	IntValue    *string  `json:"intValue"`
	BoolValue   *bool    `json:"boolValue"`
	DoubleValue *float64 `json:"doubleValue"`
}

func (v value) text() string {
	switch {
	case v.StringValue != nil:
		return *v.StringValue
	case v.IntValue != nil:
		return *v.IntValue
	case v.BoolValue != nil:
		if *v.BoolValue {
			return "true"
		}
		return "false"
	case v.DoubleValue != nil:
		b, _ := json.Marshal(*v.DoubleValue)
		return string(b)
	}
	return ""
}

func (c *Collector) receive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/traces" || r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "unexpected request", http.StatusBadRequest)
		return
	}
	var req struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []struct {
					TraceID      string `json:"traceId"`
					SpanID       string `json:"spanId"`
					ParentSpanID string `json:"parentSpanId"`
					Name         string `json:"name"`
					Kind         int    `json:"kind"`
					Attributes   []struct {
						Key   string `json:"key"`
						Value value  `json:"value"`
					} `json:"attributes"`
					Status struct {
						Code    int    `json:"code"`
						Message string `json:"message"`
					} `json:"status"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	body, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, rs := range req.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			for _, s := range ss.Spans {
				span := Span{
					TraceID: s.TraceID, SpanID: s.SpanID, ParentSpanID: s.ParentSpanID, Name: s.Name, Kind: s.Kind,
					Attributes: map[string]string{}, StatusCode: s.Status.Code, StatusMessage: s.Status.Message,
				}
				for _, a := range s.Attributes {
					span.Attributes[a.Key] = a.Value.text()
				}
				c.spans = append(c.spans, span)
			}
		}
	}
}
