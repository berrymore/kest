// Package metrics is kest's metrics core (ADR-0007): counters, gauges and
// histograms with fixed label names, written in the Prometheus text format
// 0.0.4.
//
// Components register their families and resolve their series when the
// server graph is built, so the request path only does atomic updates.
// Registering a family that exists with the same definition returns it, so
// every listener or cluster can register the families it uses. Registration
// panics on an invalid name, on a family registered again with another
// definition, and on a wrong number of label values: these are programming
// errors, which tests catch.
package metrics

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// Type is the type of a metric family.
type Type string

// Family types.
const (
	TypeCounter   Type = "counter"
	TypeGauge     Type = "gauge"
	TypeHistogram Type = "histogram"
)

// DurationBuckets are the default histogram buckets for durations in
// seconds: 5 ms to 60 s.
var DurationBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60}

var (
	nameRE  = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	labelRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// Registry holds metric families. It is safe for concurrent use.
type Registry struct {
	mu       sync.Mutex
	families map[string]*family
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{families: make(map[string]*family)}
}

// CollectFunc produces the series of a collector family at scrape time. It
// calls emit once per series, with one value per label of the family.
type CollectFunc func(emit func(value float64, labelValues ...string))

// family is a metric name with its definition and its series.
type family struct {
	name, help string
	typ        Type
	labels     []string
	buckets    []float64 // histograms only

	mu       sync.Mutex
	series   map[string]*series // by label values joined with labelSep
	collects []CollectFunc      // collector families only
}

// labelSep joins label values into a series key; it can't occur in UTF-8.
const labelSep = "\xff"

// series is one label set of a family; one of its metric fields is set.
type series struct {
	key     string // label values joined with labelSep, for sorting
	labels  string // rendered label pairs: a="x",b="y"
	counter *Counter
	gauge   *Gauge
	hist    *Histogram
}

// register returns the family called name, creating it if needed. A family
// that exists must have the same definition.
func (r *Registry) register(name, help string, typ Type, labels []string, buckets []float64, collector bool) *family {
	if !nameRE.MatchString(name) {
		panic(fmt.Sprintf("metrics: invalid metric name %q", name))
	}
	for _, l := range labels {
		if !labelRE.MatchString(l) || strings.HasPrefix(l, "__") || (typ == TypeHistogram && l == "le") {
			panic(fmt.Sprintf("metrics: %s: invalid label name %q", name, l))
		}
	}
	if typ == TypeHistogram {
		checkBuckets(name, buckets)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.families[name]; ok {
		if f.help != help || f.typ != typ || !slices.Equal(f.labels, labels) ||
			!slices.Equal(f.buckets, buckets) || (f.series == nil) != collector {
			panic(fmt.Sprintf("metrics: %s registered again with another definition", name))
		}
		return f
	}
	f := &family{name: name, help: help, typ: typ, labels: slices.Clone(labels), buckets: slices.Clone(buckets)}
	if !collector {
		f.series = make(map[string]*series)
	}
	r.families[name] = f
	return f
}

func checkBuckets(name string, buckets []float64) {
	if len(buckets) == 0 {
		panic(fmt.Sprintf("metrics: %s: no buckets", name))
	}
	for i, b := range buckets {
		if math.IsNaN(b) || math.IsInf(b, 0) || (i > 0 && b <= buckets[i-1]) {
			panic(fmt.Sprintf("metrics: %s: buckets must be finite and increasing", name))
		}
	}
}

// with returns the series for values, creating it with create if needed.
func (f *family) with(values []string, create func() *series) *series {
	if len(values) != len(f.labels) {
		panic(fmt.Sprintf("metrics: %s: %d label values for %d labels", f.name, len(values), len(f.labels)))
	}
	key := strings.Join(values, labelSep)
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.series[key]; ok {
		return s
	}
	s := create()
	s.key, s.labels = key, renderLabels(f.labels, values)
	f.series[key] = s
	return s
}

// CounterVec is a counter family.
type CounterVec struct{ f *family }

// NewCounterVec registers a counter family with the given label names.
func (r *Registry) NewCounterVec(name, help string, labels ...string) *CounterVec {
	return &CounterVec{r.register(name, help, TypeCounter, labels, nil, false)}
}

// NewCounter registers a counter without labels.
func (r *Registry) NewCounter(name, help string) *Counter {
	return r.NewCounterVec(name, help).With()
}

// With returns the counter for the label values, one per label name.
func (v *CounterVec) With(values ...string) *Counter {
	return v.f.with(values, func() *series { return &series{counter: &Counter{}} }).counter
}

// Counter is a monotonic count.
type Counter struct{ v atomic.Uint64 }

// Inc adds 1.
func (c *Counter) Inc() { c.v.Add(1) }

// Add adds n.
func (c *Counter) Add(n uint64) { c.v.Add(n) }

// Value returns the count.
func (c *Counter) Value() uint64 { return c.v.Load() }

// GaugeVec is a gauge family.
type GaugeVec struct{ f *family }

// NewGaugeVec registers a gauge family with the given label names.
func (r *Registry) NewGaugeVec(name, help string, labels ...string) *GaugeVec {
	return &GaugeVec{r.register(name, help, TypeGauge, labels, nil, false)}
}

// NewGauge registers a gauge without labels.
func (r *Registry) NewGauge(name, help string) *Gauge {
	return r.NewGaugeVec(name, help).With()
}

// With returns the gauge for the label values, one per label name.
func (v *GaugeVec) With(values ...string) *Gauge {
	return v.f.with(values, func() *series { return &series{gauge: &Gauge{}} }).gauge
}

// Gauge is a value that goes up and down.
type Gauge struct{ bits atomic.Uint64 }

// Set sets the value.
func (g *Gauge) Set(v float64) { g.bits.Store(math.Float64bits(v)) }

// Add adds delta, which may be negative.
func (g *Gauge) Add(delta float64) { addFloat(&g.bits, delta) }

// Inc adds 1.
func (g *Gauge) Inc() { g.Add(1) }

// Dec subtracts 1.
func (g *Gauge) Dec() { g.Add(-1) }

// Value returns the value.
func (g *Gauge) Value() float64 { return math.Float64frombits(g.bits.Load()) }

func addFloat(bits *atomic.Uint64, delta float64) {
	for {
		old := bits.Load()
		if bits.CompareAndSwap(old, math.Float64bits(math.Float64frombits(old)+delta)) {
			return
		}
	}
}

// HistogramVec is a histogram family.
type HistogramVec struct{ f *family }

// NewHistogramVec registers a histogram family with fixed bucket upper
// bounds (finite and increasing; +Inf is implied) and label names.
func (r *Registry) NewHistogramVec(name, help string, buckets []float64, labels ...string) *HistogramVec {
	return &HistogramVec{r.register(name, help, TypeHistogram, labels, buckets, false)}
}

// NewHistogram registers a histogram without labels.
func (r *Registry) NewHistogram(name, help string, buckets []float64) *Histogram {
	return r.NewHistogramVec(name, help, buckets).With()
}

// With returns the histogram for the label values, one per label name.
func (v *HistogramVec) With(values ...string) *Histogram {
	return v.f.with(values, func() *series {
		return &series{hist: &Histogram{upper: v.f.buckets, counts: make([]atomic.Uint64, len(v.f.buckets)+1)}}
	}).hist
}

// Histogram counts observations in buckets.
type Histogram struct {
	upper  []float64       // shared with the family; read only
	counts []atomic.Uint64 // per bucket, not cumulative; the last one is +Inf
	sum    atomic.Uint64   // float64 bits
}

// Observe records v.
func (h *Histogram) Observe(v float64) {
	i, _ := slices.BinarySearch(h.upper, v) // the first bucket with upper bound >= v
	h.counts[i].Add(1)
	addFloat(&h.sum, v)
}

// Count returns the number of observations.
func (h *Histogram) Count() uint64 {
	var n uint64
	for i := range h.counts {
		n += h.counts[i].Load()
	}
	return n
}

// Sum returns the sum of the observations.
func (h *Histogram) Sum() float64 { return math.Float64frombits(h.sum.Load()) }

// NewCollector registers a family whose series fn produces at scrape time,
// for values that are cheaper to read than to keep up to date (pool states,
// process memory). Registering it again with the same definition adds fn:
// each cluster adds its own series. fn must be fast and must not block, and
// its label values must come from a bounded set.
func (r *Registry) NewCollector(name, help string, typ Type, labels []string, fn CollectFunc) {
	if typ != TypeCounter && typ != TypeGauge {
		panic(fmt.Sprintf("metrics: %s: a collector must be a counter or a gauge", name))
	}
	f := r.register(name, help, typ, labels, nil, true)
	f.mu.Lock()
	f.collects = append(f.collects, fn)
	f.mu.Unlock()
}
