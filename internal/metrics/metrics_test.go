package metrics

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestExposition(t *testing.T) {
	r := NewRegistry()
	req := r.Counter("laterna_requests_total", "Requêtes reçues.", "route", "code")
	req.Inc("/images/{id}", "2xx")
	req.Add(2, "/images/{id}", "2xx")
	req.Inc(`a"b\c`+"\n", "5xx")
	g := r.Gauge("laterna_temperature", "Une jauge\nsur deux lignes.")
	g.Set(21.5)
	g.Add(-1.5)
	h := r.Histogram("laterna_duration_seconds", "Durées.", []float64{0.1, 1}, "route")
	for _, v := range []float64{0.05, 0.1, 0.5, 3} {
		h.Observe(v, "/x")
	}
	r.GaugeFunc("laterna_playbacks", "Lectures.", []string{"method"}, func(_ context.Context, emit func(float64, ...string)) {
		emit(2, "remux")
		emit(1, "direct")
		emit(9) // wrong number of labels: ignored
	})
	var b strings.Builder
	if err := r.Write(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	want := `# HELP laterna_duration_seconds Durées.
# TYPE laterna_duration_seconds histogram
laterna_duration_seconds_bucket{route="/x",le="0.1"} 2
laterna_duration_seconds_bucket{route="/x",le="1"} 3
laterna_duration_seconds_bucket{route="/x",le="+Inf"} 4
laterna_duration_seconds_sum{route="/x"} 3.65
laterna_duration_seconds_count{route="/x"} 4
# HELP laterna_playbacks Lectures.
# TYPE laterna_playbacks gauge
laterna_playbacks{method="direct"} 1
laterna_playbacks{method="remux"} 2
# HELP laterna_requests_total Requêtes reçues.
# TYPE laterna_requests_total counter
laterna_requests_total{route="/images/{id}",code="2xx"} 3
laterna_requests_total{route="a\"b\\c\n",code="5xx"} 1
# HELP laterna_temperature Une jauge\nsur deux lignes.
# TYPE laterna_temperature gauge
laterna_temperature 20
`
	if b.String() != want {
		t.Errorf("exposition:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestRegistration(t *testing.T) {
	r := NewRegistry()
	a := r.Counter("x_total", "x", "k")
	r.Counter("x_total", "x", "k").Inc("v") // same family
	a.Inc("v")
	var b strings.Builder
	_ = r.Write(context.Background(), &b)
	if !strings.Contains(b.String(), `x_total{k="v"} 2`) {
		t.Errorf("shared family: %s", b.String())
	}
	for name, fn := range map[string]func(){
		"autre forme":  func() { r.Gauge("x_total", "x", "k") },
		"other label":  func() { r.Counter("x_total", "x", "j") },
		"nom invalide": func() { r.Counter("x-y", "x") },
		"le label":     func() { r.Histogram("h", "h", []float64{1}, "le") },
		"bornes":       func() { r.Histogram("h2", "h", []float64{2, 1}) },
		"decreasing":   func() { a.Add(-1, "v") },
		"labels":       func() { a.Inc() },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: did not panic", name)
				}
			}()
			fn()
		}()
	}
}

func TestConcurrent(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("c_total", "c", "k")
	h := r.Histogram("h_seconds", "h", DurationBuckets, "k")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for range 1000 {
				c.Inc("a")
				h.Observe(float64(i)/10, "a")
			}
		})
	}
	wg.Go(func() {
		for range 50 {
			_ = r.Write(context.Background(), &strings.Builder{})
		}
	})
	wg.Wait()
	var b strings.Builder
	_ = r.Write(context.Background(), &b)
	if !strings.Contains(b.String(), `c_total{k="a"} 8000`) || !strings.Contains(b.String(), `h_seconds_count{k="a"} 8000`) {
		t.Errorf("counts: %s", b.String())
	}
}

func TestRuntime(t *testing.T) {
	r := NewRegistry()
	r.RegisterRuntime(time.Unix(1700000000, 0))
	var b strings.Builder
	if err := r.Write(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"process_start_time_seconds 1.7e+09", "go_goroutines ", "go_memory_total_bytes ", "go_gc_cycles_total ", "# TYPE go_gc_cycles_total counter", "process_resident_memory_bytes "} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("%q missing:\n%s", want, b.String())
		}
	}
}
