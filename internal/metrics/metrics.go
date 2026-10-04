// Package metrics holds the server's metrics and writes them in the Prometheus text format (version
// 0.0.4) for /metrics. It has counters, gauges and histograms, with labels. Gauges computed at
// scrape time (GaugeFunc) avoid keeping up to date what is easier to measure on demand. No
// dependency: the format is simple, and the official library would pull in a dozen modules and
// their memory.
package metrics

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	metricName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	labelName  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// Registry holds metric families.
type Registry struct {
	mu       sync.Mutex
	families map[string]*family
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{families: map[string]*family{}} }

type kind string

const (
	counterKind   kind = "counter"
	gaugeKind     kind = "gauge"
	histogramKind kind = "histogram"
)

// family is a metric and all its series (one per set of label values).
type family struct {
	name, help string
	kind       kind
	labels     []string
	buckets    []float64

	mu     sync.Mutex
	series map[string]*series
	// collect is set for a metric computed at scrape time (GaugeFunc, CounterFunc).
	collect Collect
}

// Collect computes a metric at scrape time by calling emit for each series. ctx is the scrape's
// context, canceled if the scraper goes away.
type Collect func(ctx context.Context, emit func(value float64, labelValues ...string))

type series struct {
	values []string
	// value of a counter or a gauge (the bits of a float64).
	value atomic.Uint64
	// counts, sum and count are for histograms.
	mu     sync.Mutex
	counts []uint64
	sum    float64
	count  uint64
}

// register returns the family with that name, creating it if needed. Registering a family again in
// a different shape is a programming error.
func (r *Registry) register(name, help string, k kind, buckets []float64, labels []string) *family {
	if !metricName.MatchString(name) {
		panic("metrics: invalid name " + name)
	}
	for _, l := range labels {
		if !labelName.MatchString(l) || l == "le" {
			panic("metrics: invalid label " + l)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.families[name]; ok {
		if f.kind != k || !slices.Equal(f.labels, labels) || !slices.Equal(f.buckets, buckets) {
			panic("metrics: " + name + " already registered differently")
		}
		return f
	}
	f := &family{name: name, help: help, kind: k, labels: slices.Clone(labels), buckets: slices.Clone(buckets), series: map[string]*series{}}
	r.families[name] = f
	return f
}

// with returns the series for a set of label values, creating it if needed.
func (f *family) with(values []string) *series {
	if len(values) != len(f.labels) {
		panic(fmt.Sprintf("metrics: %s expects %d labels, not %d", f.name, len(f.labels), len(values)))
	}
	key := strings.Join(values, "\xff")
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.series[key]
	if !ok {
		s = &series{values: slices.Clone(values)}
		if f.kind == histogramKind {
			s.counts = make([]uint64, len(f.buckets))
		}
		f.series[key] = s
	}
	return s
}

// Counter is a family of counters.
type Counter struct{ f *family }

// Counter registers a counter. By convention its name ends with _total.
func (r *Registry) Counter(name, help string, labels ...string) Counter {
	return Counter{r.register(name, help, counterKind, nil, labels)}
}

// Add adds v (positive) to the counter for the given label values.
func (c Counter) Add(v float64, labelValues ...string) {
	if v < 0 {
		panic("metrics: a counter cannot decrease")
	}
	addFloat(&c.f.with(labelValues).value, v)
}

// Inc adds 1.
func (c Counter) Inc(labelValues ...string) { c.Add(1, labelValues...) }

// Gauge is a family of gauges.
type Gauge struct{ f *family }

// Gauge registers a gauge.
func (r *Registry) Gauge(name, help string, labels ...string) Gauge {
	return Gauge{r.register(name, help, gaugeKind, nil, labels)}
}

// Set sets the gauge.
func (g Gauge) Set(v float64, labelValues ...string) {
	g.f.with(labelValues).value.Store(math.Float64bits(v))
}

// Add adds v, which may be negative, to the gauge.
func (g Gauge) Add(v float64, labelValues ...string) { addFloat(&g.f.with(labelValues).value, v) }

// GaugeFunc registers a gauge computed on every scrape: collect calls emit for each series.
func (r *Registry) GaugeFunc(name, help string, labels []string, collect Collect) {
	r.register(name, help, gaugeKind, nil, labels).setCollect(collect)
}

// CounterFunc registers a counter kept somewhere else (the Go GC...) and read on every scrape.
func (r *Registry) CounterFunc(name, help string, labels []string, collect Collect) {
	r.register(name, help, counterKind, nil, labels).setCollect(collect)
}

func (f *family) setCollect(collect Collect) {
	f.mu.Lock()
	f.collect = collect
	f.mu.Unlock()
}

// Histogram is a family of histograms.
type Histogram struct{ f *family }

// Histogram registers a histogram with the given increasing bounds (without +Inf).
func (r *Registry) Histogram(name, help string, buckets []float64, labels ...string) Histogram {
	if !slices.IsSorted(buckets) || len(buckets) == 0 {
		panic("metrics: histogram bounds not increasing")
	}
	return Histogram{r.register(name, help, histogramKind, buckets, labels)}
}

// Observe records a value.
func (h Histogram) Observe(v float64, labelValues ...string) {
	s := h.f.with(labelValues)
	i, _ := slices.BinarySearch(h.f.buckets, v)
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < len(s.counts) {
		s.counts[i]++
	}
	s.sum += v
	s.count++
}

// DurationBuckets are bounds in seconds for request durations, from 5 ms to 30 s.
var DurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

func addFloat(u *atomic.Uint64, v float64) {
	for {
		old := u.Load()
		if u.CompareAndSwap(old, math.Float64bits(math.Float64frombits(old)+v)) {
			return
		}
	}
}

// Write writes every metric in the Prometheus text format, families and series sorted.
func (r *Registry) Write(ctx context.Context, w io.Writer) error {
	r.mu.Lock()
	families := make([]*family, 0, len(r.families))
	for _, f := range r.families {
		families = append(families, f)
	}
	r.mu.Unlock()
	slices.SortFunc(families, func(a, b *family) int { return strings.Compare(a.name, b.name) })
	bw := bufio.NewWriter(w)
	cw := &countingWriter{w: bw}
	for _, f := range families {
		f.write(ctx, cw)
	}
	if cw.err != nil {
		return cw.err
	}
	return bw.Flush()
}

// countingWriter remembers the first write error.
type countingWriter struct {
	w   io.Writer
	err error
}

func (c *countingWriter) printf(format string, args ...any) {
	if c.err == nil {
		_, c.err = fmt.Fprintf(c.w, format, args...)
	}
}

type sample struct {
	values []string
	value  float64
	counts []uint64
	sum    float64
	count  uint64
}

func (f *family) write(ctx context.Context, w *countingWriter) {
	samples := f.snapshot(ctx)
	w.printf("# HELP %s %s\n# TYPE %s %s\n", f.name, escapeHelp(f.help), f.name, f.kind)
	for _, s := range samples {
		if f.kind != histogramKind {
			w.printf("%s%s %s\n", f.name, labelSet(f.labels, s.values, "", ""), formatFloat(s.value))
			continue
		}
		var cumulative uint64
		for i, b := range f.buckets {
			cumulative += s.counts[i]
			w.printf("%s_bucket%s %d\n", f.name, labelSet(f.labels, s.values, "le", formatFloat(b)), cumulative)
		}
		w.printf("%s_bucket%s %d\n", f.name, labelSet(f.labels, s.values, "le", "+Inf"), s.count)
		w.printf("%s_sum%s %s\n", f.name, labelSet(f.labels, s.values, "", ""), formatFloat(s.sum))
		w.printf("%s_count%s %d\n", f.name, labelSet(f.labels, s.values, "", ""), s.count)
	}
}

// snapshot copies the series, sorted by label values, and runs the collect function if there is
// one.
func (f *family) snapshot(ctx context.Context) []sample {
	f.mu.Lock()
	collect := f.collect
	var out []sample
	for _, s := range f.series {
		smp := sample{values: s.values, value: math.Float64frombits(s.value.Load())}
		if f.kind == histogramKind {
			s.mu.Lock()
			smp.counts, smp.sum, smp.count = slices.Clone(s.counts), s.sum, s.count
			s.mu.Unlock()
		}
		out = append(out, smp)
	}
	f.mu.Unlock()
	if collect != nil {
		collect(ctx, func(value float64, labelValues ...string) {
			if len(labelValues) == len(f.labels) {
				out = append(out, sample{values: slices.Clone(labelValues), value: value})
			}
		})
	}
	slices.SortFunc(out, func(a, b sample) int { return slices.Compare(a.values, b.values) })
	return out
}

func labelSet(names, values []string, extraName, extraValue string) string {
	if len(names) == 0 && extraName == "" {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(n)
		b.WriteString(`="`)
		b.WriteString(escapeLabel(values[i]))
		b.WriteByte('"')
	}
	if extraName != "" {
		if len(names) > 0 {
			b.WriteByte(',')
		}
		b.WriteString(extraName)
		b.WriteString(`="`)
		b.WriteString(extraValue)
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

var (
	labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	helpEscaper  = strings.NewReplacer(`\`, `\\`, "\n", `\n`)
)

func escapeLabel(s string) string { return labelEscaper.Replace(strings.ToValidUTF8(s, "�")) }

func escapeHelp(s string) string { return helpEscaper.Replace(s) }

func formatFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
