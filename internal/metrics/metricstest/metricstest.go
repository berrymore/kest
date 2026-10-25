// Package metricstest reads a metrics registry back for tests, through
// Prometheus's own parser (ADR-0007). Only tests import it.
package metricstest

import (
	"bytes"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/berrymore/kest/internal/metrics"
)

// Scrape writes reg in the text format and parses it.
func Scrape(tb testing.TB, reg *metrics.Registry) map[string]*dto.MetricFamily {
	tb.Helper()
	var buf bytes.Buffer
	if err := reg.WriteText(&buf); err != nil {
		tb.Fatal(err)
	}
	p := expfmt.NewTextParser(model.UTF8Validation)
	fams, err := p.TextToMetricFamilies(&buf)
	if err != nil {
		tb.Fatalf("parse metrics: %v\n%s", err, buf.String())
	}
	return fams
}

// Value returns the value of the series of the family name whose labels are
// exactly labels, given as name, value pairs; for a histogram, the number of
// observations. A missing series is 0, as for a counter not incremented yet.
func Value(tb testing.TB, reg *metrics.Registry, name string, labels ...string) float64 {
	tb.Helper()
	if len(labels)%2 != 0 {
		tb.Fatalf("metricstest.Value(%s): odd number of label arguments", name)
	}
	f, ok := Scrape(tb, reg)[name]
	if !ok {
		tb.Fatalf("no metric family %s", name)
	}
	for _, m := range f.Metric {
		if matches(m.Label, labels) {
			switch {
			case m.Counter != nil:
				return m.Counter.GetValue()
			case m.Gauge != nil:
				return m.Gauge.GetValue()
			case m.Histogram != nil:
				return float64(m.Histogram.GetSampleCount())
			}
		}
	}
	return 0
}

func matches(got []*dto.LabelPair, want []string) bool {
	if len(got) != len(want)/2 {
		return false
	}
	for i := 0; i < len(want); i += 2 {
		found := false
		for _, p := range got {
			if p.GetName() == want[i] {
				found = p.GetValue() == want[i+1]
			}
		}
		if !found {
			return false
		}
	}
	return true
}
