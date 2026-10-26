package proc

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func TestLineLogLines(t *testing.T) {
	long := strings.Repeat("x", maxLine)
	tests := []struct {
		name   string
		writes []string
		want   []string
	}{
		{name: "lines", writes: []string{"one\ntwo\n"}, want: []string{"one", "two"}},
		{name: "split across writes", writes: []string{"o", "ne\nt", "wo\n"}, want: []string{"one", "two"}},
		{name: "crlf and blank lines", writes: []string{"one\r\n\n\r\ntwo\n"}, want: []string{"one", "two"}},
		{name: "incomplete last line", writes: []string{"one\ntw", "o"}, want: []string{"one", "two"}},
		{name: "line at the limit", writes: []string{long + "\nnext\n"}, want: []string{long, "next"}},
		{name: "line over the limit", writes: []string{long + "yz\n"}, want: []string{long, "yz"}},
		{name: "long line in pieces", writes: []string{long[:10], long[10:] + "yz"}, want: []string{long, "yz"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := &logBuffer{}
			l := NewLineLog(logs.logger(), "worker stderr")
			for _, w := range tt.writes {
				if n, err := l.Write([]byte(w)); n != len(w) || err != nil {
					t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(w))
				}
			}
			l.Flush()

			got := logs.lines()
			if len(got) != len(tt.want) {
				t.Fatalf("logged %d lines, want %d: %.200q", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("line %d = %.40q, want %.40q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestLineLogTail(t *testing.T) {
	l := NewLineLog(slog.New(slog.DiscardHandler), "worker stderr")
	if got := l.Tail(); got != "" {
		t.Errorf("tail of nothing = %q", got)
	}

	_, _ = l.Write([]byte("PHP Warning: one\nPHP Fatal error: two\n"))
	if got, want := l.Tail(), "PHP Warning: one\nPHP Fatal error: two"; got != want {
		t.Errorf("tail = %q, want %q", got, want)
	}

	// Past the limit, the tail starts at a line boundary and ends with the
	// latest output, whatever the write sizes.
	for _, size := range []int{1, 100, tailSize, 3 * tailSize} {
		l := NewLineLog(slog.New(slog.DiscardHandler), "worker stderr")
		data := []byte(strings.Repeat("filler line\n", 1000) + "last words")
		for len(data) > 0 {
			n := min(size, len(data))
			_, _ = l.Write(data[:n])
			data = data[n:]
		}
		got := l.Tail()
		if !strings.HasPrefix(got, "filler line\n") || !strings.HasSuffix(got, "\nlast words") {
			t.Errorf("writes of %d bytes: tail = %.30q…%.30q", size, got, got[max(0, len(got)-30):])
		}
		if len(got) > tailSize {
			t.Errorf("writes of %d bytes: tail is %d bytes, limit %d", size, len(got), tailSize)
		}
	}
}

// logBuffer collects JSON log records for assertions.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(b, nil))
}

// lines returns the lines logged as "worker stderr".
func (b *logBuffer) lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for line := range bytes.Lines(b.buf.Bytes()) {
		var r struct{ Msg, Line string }
		if err := json.Unmarshal(line, &r); err != nil {
			panic(err)
		}
		if r.Msg == "worker stderr" {
			out = append(out, r.Line)
		}
	}
	return out
}
