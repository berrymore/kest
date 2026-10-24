package worker

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
)

// Limits for worker stderr handling.
const (
	maxStderrLine = 64 << 10 // longer lines are logged in pieces
	stderrTail    = 4 << 10  // kept for error messages
)

// stderrLog logs a worker's stderr line by line and keeps the last few KiB
// for error messages. Writes come from exec's copying goroutine; tail is
// called from the worker's.
type stderrLog struct {
	logger *slog.Logger

	mu   sync.Mutex
	line []byte // the current incomplete line
	last []byte // the last stderrTail bytes written
}

func newStderrLog(logger *slog.Logger) *stderrLog {
	return &stderrLog{logger: logger}
}

// setLogger replaces the logger, once the pid is known.
func (l *stderrLog) setLogger(logger *slog.Logger) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logger = logger
}

func (l *stderrLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.keep(p)
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		chunk := p
		if i >= 0 {
			chunk = p[:i]
		}
		if room := maxStderrLine - len(l.line); len(chunk) >= room {
			l.line = append(l.line, chunk[:room]...)
			l.emit()
			p = p[room:]
			continue
		}
		l.line = append(l.line, chunk...)
		if i < 0 {
			break
		}
		l.emit()
		p = p[i+1:]
	}
	return n, nil
}

// flush logs an incomplete last line. It is called after the process exits.
func (l *stderrLog) flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.emit()
}

// tail returns the last lines written, starting at a line boundary.
func (l *stderrLog) tail() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := string(l.last)
	if len(l.last) == stderrTail {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	return strings.TrimSpace(s)
}

func (l *stderrLog) emit() {
	line := bytes.TrimRight(l.line, "\r")
	if len(line) > 0 {
		l.logger.Info("worker stderr", "line", string(line))
	}
	l.line = l.line[:0]
}

func (l *stderrLog) keep(p []byte) {
	if len(p) >= stderrTail {
		l.last = append(l.last[:0], p[len(p)-stderrTail:]...)
		return
	}
	if over := len(l.last) + len(p) - stderrTail; over > 0 {
		l.last = l.last[:copy(l.last, l.last[over:])]
	}
	l.last = append(l.last, p...)
}
