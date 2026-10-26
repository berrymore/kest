package worker

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"sync"
)

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
	return slog.New(slog.NewJSONHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// records returns the logged records with the given message.
func (b *logBuffer) records(msg string) []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for line := range bytes.Lines(b.buf.Bytes()) {
		var r map[string]any
		if err := json.Unmarshal(line, &r); err != nil {
			panic(err)
		}
		if r["msg"] == msg {
			out = append(out, r)
		}
	}
	return out
}

// stderrLines returns the worker stderr lines that were logged.
func (b *logBuffer) stderrLines() []string {
	var out []string
	for _, r := range b.records("worker stderr") {
		s, _ := r["line"].(string)
		out = append(out, s)
	}
	return out
}
