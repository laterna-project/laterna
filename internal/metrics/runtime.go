package metrics

import (
	"context"
	"runtime"
	"runtime/metrics"
	"time"
)

// RegisterRuntime registers the process and Go runtime metrics: memory really in use (SQLite's
// cache lives outside the Go heap), heap, threads, GC, start time.
func (r *Registry) RegisterRuntime(start time.Time) {
	r.GaugeFunc("process_start_time_seconds", "Process start time (Unix seconds).", nil, func(_ context.Context, emit func(float64, ...string)) {
		emit(float64(start.UnixMilli()) / 1000)
	})
	r.GaugeFunc("process_resident_memory_bytes", "Resident memory of the process (working set on Windows).", nil,
		func(_ context.Context, emit func(float64, ...string)) {
			if rss, ok := residentMemory(); ok {
				emit(float64(rss))
			}
		})
	r.GaugeFunc("go_goroutines", "Go goroutines.", nil, func(_ context.Context, emit func(float64, ...string)) {
		emit(float64(runtime.NumGoroutine()))
	})
	samples := []metrics.Sample{
		{Name: "/memory/classes/total:bytes"},
		{Name: "/gc/heap/live:bytes"},
		{Name: "/gc/cycles/total:gc-cycles"},
	}
	read := func() []metrics.Sample {
		out := make([]metrics.Sample, len(samples))
		copy(out, samples)
		metrics.Read(out)
		return out
	}
	value := func(s metrics.Sample) (float64, bool) {
		if s.Value.Kind() != metrics.KindUint64 {
			return 0, false
		}
		return float64(s.Value.Uint64()), true
	}
	r.GaugeFunc("go_memory_total_bytes", "Memory obtained from the system by Go.", nil, func(_ context.Context, emit func(float64, ...string)) {
		if v, ok := value(read()[0]); ok {
			emit(v)
		}
	})
	r.GaugeFunc("go_heap_live_bytes", "Live Go heap at the last garbage collection.", nil, func(_ context.Context, emit func(float64, ...string)) {
		if v, ok := value(read()[1]); ok {
			emit(v)
		}
	})
	r.CounterFunc("go_gc_cycles_total", "Garbage collection cycles.", nil, func(_ context.Context, emit func(float64, ...string)) {
		if v, ok := value(read()[2]); ok {
			emit(v)
		}
	})
}
