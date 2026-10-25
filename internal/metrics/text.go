package metrics

import (
	"bufio"
	"cmp"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
)

// ContentType is the media type of WriteText's output.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

var (
	labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	helpEscaper  = strings.NewReplacer(`\`, `\\`, "\n", `\n`)
)

// renderLabels renders label pairs for the inside of {…}.
func renderLabels(names, values []string) string {
	var b strings.Builder
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(n)
		b.WriteString(`="`)
		b.WriteString(labelEscaper.Replace(values[i]))
		b.WriteByte('"')
	}
	return b.String()
}

// WriteText writes every family in the Prometheus text format 0.0.4: families
// sorted by name, each with its HELP and TYPE lines, series sorted by label
// values (collector series in the order they were emitted).
func (r *Registry) WriteText(out io.Writer) error {
	r.mu.Lock()
	families := make([]*family, 0, len(r.families))
	for _, f := range r.families {
		families = append(families, f)
	}
	r.mu.Unlock()
	slices.SortFunc(families, func(a, b *family) int { return cmp.Compare(a.name, b.name) })

	b := bufio.NewWriter(out)
	for _, f := range families {
		f.write(textWriter{b})
	}
	return b.Flush()
}

// textWriter writes to a bufio.Writer. Its first error sticks, and Flush
// returns it, so the writes don't check.
type textWriter struct{ b *bufio.Writer }

func (w textWriter) str(s string) { _, _ = w.b.WriteString(s) }

func (w textWriter) char(c byte) { _ = w.b.WriteByte(c) }

func (f *family) write(w textWriter) {
	f.mu.Lock()
	all := make([]*series, 0, len(f.series))
	for _, s := range f.series {
		all = append(all, s)
	}
	collects := slices.Clone(f.collects)
	f.mu.Unlock()
	slices.SortFunc(all, func(a, b *series) int { return cmp.Compare(a.key, b.key) })

	w.str("# HELP " + f.name + " " + helpEscaper.Replace(f.help) + "\n")
	w.str("# TYPE " + f.name + " " + string(f.typ) + "\n")
	for _, s := range all {
		switch {
		case s.counter != nil:
			writeSample(w, f.name, s.labels, "", strconv.FormatUint(s.counter.Value(), 10))
		case s.gauge != nil:
			writeSample(w, f.name, s.labels, "", formatFloat(s.gauge.Value()))
		default:
			f.writeHistogram(w, s)
		}
	}
	for _, collect := range collects {
		collect(func(value float64, labelValues ...string) {
			if len(labelValues) != len(f.labels) {
				panic("metrics: " + f.name + ": a collector emitted the wrong number of label values")
			}
			writeSample(w, f.name, renderLabels(f.labels, labelValues), "", formatFloat(value))
		})
	}
}

func (f *family) writeHistogram(w textWriter, s *series) {
	h := s.hist
	var cumulative uint64
	for i := range h.counts {
		cumulative += h.counts[i].Load()
		le := "+Inf"
		if i < len(h.upper) {
			le = formatFloat(h.upper[i])
		}
		writeSample(w, f.name+"_bucket", s.labels, `le="`+le+`"`, strconv.FormatUint(cumulative, 10))
	}
	// The sum is read after the buckets, so a concurrent observation may be in
	// the sum but not in the count. The count always equals the +Inf bucket.
	writeSample(w, f.name+"_sum", s.labels, "", formatFloat(h.Sum()))
	writeSample(w, f.name+"_count", s.labels, "", strconv.FormatUint(cumulative, 10))
}

func writeSample(w textWriter, name, labels, extra, value string) {
	w.str(name)
	if labels != "" || extra != "" {
		w.char('{')
		w.str(labels)
		if labels != "" && extra != "" {
			w.char(',')
		}
		w.str(extra)
		w.char('}')
	}
	w.char(' ')
	w.str(value)
	w.char('\n')
}

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
