package metrics

import (
	"bytes"
	"math"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// parse reads text with Prometheus's own parser, the oracle of ADR-0007.
func parse(t *testing.T, text []byte) map[string]*dto.MetricFamily {
	t.Helper()
	p := expfmt.NewTextParser(model.UTF8Validation)
	fams, err := p.TextToMetricFamilies(bytes.NewReader(text))
	if err != nil {
		t.Fatalf("expfmt: %v\n%s", err, text)
	}
	return fams
}

func write(t *testing.T, r *Registry) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := r.WriteText(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestWriteText(t *testing.T) {
	r := NewRegistry()
	req := r.NewCounterVec("kest_requests_total", "Requests.\nBy \\ code.", "listener", "code")
	req.With("main", "200").Add(3)
	req.With("main", "500").Inc()
	req.With(`a"b\c`+"\n", "200").Inc()
	r.NewGauge("kest_in_flight", "In flight.").Set(-1.5)
	h := r.NewHistogramVec("kest_duration_seconds", "Duration.", []float64{0.1, 1}, "listener").With("main")
	h.Observe(0.05)
	h.Observe(0.1)
	h.Observe(5)
	r.NewCollector("kest_workers", "Workers.", TypeGauge, []string{"state"}, func(emit func(float64, ...string)) {
		emit(2, "ready")
		emit(1, "working")
	})
	r.NewCounterVec("kest_empty_total", "No series yet.", "x")

	want := `# HELP kest_duration_seconds Duration.
# TYPE kest_duration_seconds histogram
kest_duration_seconds_bucket{listener="main",le="0.1"} 2
kest_duration_seconds_bucket{listener="main",le="1"} 2
kest_duration_seconds_bucket{listener="main",le="+Inf"} 3
kest_duration_seconds_sum{listener="main"} 5.15
kest_duration_seconds_count{listener="main"} 3
# HELP kest_empty_total No series yet.
# TYPE kest_empty_total counter
# HELP kest_in_flight In flight.
# TYPE kest_in_flight gauge
kest_in_flight -1.5
# HELP kest_requests_total Requests.\nBy \\ code.
# TYPE kest_requests_total counter
kest_requests_total{listener="a\"b\\c\n",code="200"} 1
kest_requests_total{listener="main",code="200"} 3
kest_requests_total{listener="main",code="500"} 1
# HELP kest_workers Workers.
# TYPE kest_workers gauge
kest_workers{state="ready"} 2
kest_workers{state="working"} 1
`
	got := write(t, r)
	if string(got) != want {
		t.Errorf("WriteText:\n%s\nwant:\n%s", got, want)
	}

	fams := parse(t, got)
	if v := fams["kest_requests_total"].Metric[0].Label[0].GetValue(); v != `a"b\c`+"\n" {
		t.Errorf("escaped label value read back as %q", v)
	}
	if help := fams["kest_requests_total"].GetHelp(); help != "Requests.\nBy \\ code." {
		t.Errorf("help read back as %q", help)
	}
	if hist := fams["kest_duration_seconds"].Metric[0].Histogram; hist.GetSampleCount() != 3 || hist.GetSampleSum() != 5.15 {
		t.Errorf("histogram read back as count %d, sum %v", hist.GetSampleCount(), hist.GetSampleSum())
	}
}

// Any label value, help text and value survives the round trip through
// Prometheus's parser.
func FuzzWriteText(f *testing.F) {
	f.Add("main", "Requests.", 1.0)
	f.Add(`quote " and \ backslash`, "line\nbreak \\ ", math.Copysign(0, -1))
	f.Add("ünïcödé ✅", "", math.Inf(1))
	f.Add("", "trailing\\", math.NaN())
	f.Add("\n\n", "\t tab", 1e-300)
	f.Fuzz(func(t *testing.T, label, help string, v float64) {
		if !utf8.ValidString(label) || !utf8.ValidString(help) {
			t.Skip("the text format is UTF-8")
		}
		if strings.TrimLeft(help, " \t") != help {
			t.Skip("the parser drops blanks before a help text; kest's help texts have none")
		}
		r := NewRegistry()
		r.NewGaugeVec("g", help, "l").With(label).Set(v)
		r.NewCounterVec("c_total", help, "l").With(label).Add(7)
		r.NewHistogramVec("h", help, []float64{1}, "l").With(label).Observe(v)

		fams := parse(t, write(t, r))
		g := fams["g"]
		if g.GetHelp() != help || g.Metric[0].Label[0].GetValue() != label {
			t.Errorf("help %q, label %q read back as %q, %q", help, label, g.GetHelp(), g.Metric[0].Label[0].GetValue())
		}
		if got := g.Metric[0].Gauge.GetValue(); got != v && !(math.IsNaN(got) && math.IsNaN(v)) {
			t.Errorf("gauge %v read back as %v", v, got)
		}
		if got := fams["c_total"].Metric[0].Counter.GetValue(); got != 7 {
			t.Errorf("counter read back as %v", got)
		}
		if got := fams["h"].Metric[0].Histogram.GetSampleCount(); got != 1 {
			t.Errorf("histogram count read back as %v", got)
		}
	})
}

func TestValues(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounter("c_total", "")
	c.Inc()
	c.Add(41)
	g := r.NewGauge("g", "")
	g.Set(10)
	g.Inc()
	g.Dec()
	g.Add(-2.5)
	h := r.NewHistogram("h", "", []float64{1, 2})
	for _, v := range []float64{0.5, 1, 1.5, 3} {
		h.Observe(v)
	}
	if c.Value() != 42 || g.Value() != 7.5 || h.Count() != 4 || h.Sum() != 6 {
		t.Errorf("counter %d, gauge %v, histogram %d/%v", c.Value(), g.Value(), h.Count(), h.Sum())
	}
	// v == an upper bound falls into that bucket (le is "less or equal").
	if got := []uint64{h.counts[0].Load(), h.counts[1].Load(), h.counts[2].Load()}; got[0] != 2 || got[1] != 1 || got[2] != 1 {
		t.Errorf("bucket counts %v, want [2 1 1]", got)
	}
}

// Registering a family twice with one definition shares it, and its series.
func TestRegisterAgain(t *testing.T) {
	r := NewRegistry()
	a := r.NewCounterVec("c_total", "help", "l").With("x")
	b := r.NewCounterVec("c_total", "help", "l").With("x")
	if a != b {
		t.Error("the same family and label values gave two counters")
	}
	if r.NewCounterVec("c_total", "help", "l").With("y") == a {
		t.Error("other label values gave the same counter")
	}
	r.NewCollector("w", "help", TypeGauge, nil, func(emit func(float64, ...string)) { emit(1) })
	r.NewCollector("w", "help", TypeGauge, nil, func(emit func(float64, ...string)) { emit(2) })
	if got := string(write(t, r)); !strings.Contains(got, "\nw 1\nw 2\n") {
		t.Errorf("both collectors should emit:\n%s", got)
	}
}

func TestRegisterPanics(t *testing.T) {
	collect := func(func(float64, ...string)) {}
	tests := map[string]func(r *Registry){
		"invalid name":           func(r *Registry) { r.NewCounter("1abc", "") },
		"invalid label":          func(r *Registry) { r.NewCounterVec("c", "", "a-b") },
		"reserved label":         func(r *Registry) { r.NewCounterVec("c", "", "__name") },
		"le on a histogram":      func(r *Registry) { r.NewHistogramVec("h", "", []float64{1}, "le") },
		"no buckets":             func(r *Registry) { r.NewHistogram("h", "", nil) },
		"unsorted buckets":       func(r *Registry) { r.NewHistogram("h", "", []float64{2, 1}) },
		"infinite bucket":        func(r *Registry) { r.NewHistogram("h", "", []float64{1, math.Inf(1)}) },
		"wrong label count":      func(r *Registry) { r.NewCounterVec("c", "", "a", "b").With("x") },
		"collector of histogram": func(r *Registry) { r.NewCollector("h", "", TypeHistogram, nil, collect) },
		"another help": func(r *Registry) {
			r.NewCounter("c", "one")
			r.NewCounter("c", "two")
		},
		"another type": func(r *Registry) {
			r.NewCounter("c", "")
			r.NewGauge("c", "")
		},
		"other labels": func(r *Registry) {
			r.NewCounterVec("c", "", "a")
			r.NewCounterVec("c", "", "b")
		},
		"other buckets": func(r *Registry) {
			r.NewHistogram("h", "", []float64{1})
			r.NewHistogram("h", "", []float64{2})
		},
		"collector and series": func(r *Registry) {
			r.NewGauge("g", "")
			r.NewCollector("g", "", TypeGauge, nil, collect)
		},
	}
	for name, register := range tests {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("no panic")
				}
			}()
			register(NewRegistry())
		})
	}
}

// Updates, new series and scrapes run concurrently (go test -race), and no
// update is lost.
func TestConcurrent(t *testing.T) {
	r := NewRegistry()
	vec := r.NewCounterVec("c_total", "", "worker")
	h := r.NewHistogram("h", "", DurationBuckets)
	g := r.NewGauge("g", "")
	const goroutines, updates = 8, 1000
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Go(func() {
			c := vec.With(string(rune('a' + i%2)))
			for range updates {
				c.Inc()
				h.Observe(0.01)
				g.Inc()
			}
		})
	}
	wg.Go(func() {
		for range 10 {
			_ = r.WriteText(&bytes.Buffer{})
		}
	})
	wg.Wait()
	fams := parse(t, write(t, r))
	var total float64
	for _, m := range fams["c_total"].Metric {
		total += m.Counter.GetValue()
	}
	if total != goroutines*updates || h.Count() != goroutines*updates || g.Value() != goroutines*updates {
		t.Errorf("counter %v, histogram %d, gauge %v; want %d each", total, h.Count(), g.Value(), goroutines*updates)
	}
}

func BenchmarkCounterAndHistogram(b *testing.B) {
	r := NewRegistry()
	c := r.NewCounterVec("c_total", "", "listener", "virtual_host", "cluster", "code").With("main", "app", "php", "200")
	h := r.NewHistogramVec("h", "", DurationBuckets, "listener").With("main")
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Inc()
			h.Observe(0.01)
		}
	})
}
