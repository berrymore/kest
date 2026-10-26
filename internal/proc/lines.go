package proc

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
)

// Limits of LineLog.
const (
	maxLine  = 64 << 10 // longer lines are logged in pieces
	tailSize = 4 << 10  // kept for error messages
)

// LineLog logs what a child process writes, line by line, as msg with the
// line under "line", and keeps the last few KiB for error messages. Writes
// come from exec's copying goroutine; Tail may be called from another.
type LineLog struct {
	logger *slog.Logger
	msg    string

	mu   sync.Mutex
	line []byte // the current incomplete line
	last []byte // the last tailSize bytes written
}

// NewLineLog returns a LineLog that logs to logger as msg ("worker stderr").
func NewLineLog(logger *slog.Logger, msg string) *LineLog {
	return &LineLog{logger: logger, msg: msg}
}

// SetLogger replaces the logger, once the pid is known.
func (l *LineLog) SetLogger(logger *slog.Logger) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logger = logger
}

// Write logs the complete lines of p.
func (l *LineLog) Write(p []byte) (int, error) {
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
		if room := maxLine - len(l.line); len(chunk) >= room {
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

// Flush logs an incomplete last line. Call it after the process exits.
func (l *LineLog) Flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.emit()
}

// Tail returns the last lines written, starting at a line boundary.
func (l *LineLog) Tail() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := string(l.last)
	if len(l.last) == tailSize {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	return strings.TrimSpace(s)
}

func (l *LineLog) emit() {
	line := bytes.TrimRight(l.line, "\r")
	if len(line) > 0 {
		l.logger.Info(l.msg, "line", string(line))
	}
	l.line = l.line[:0]
}

func (l *LineLog) keep(p []byte) {
	if len(p) >= tailSize {
		l.last = append(l.last[:0], p[len(p)-tailSize:]...)
		return
	}
	if over := len(l.last) + len(p) - tailSize; over > 0 {
		l.last = l.last[:copy(l.last, l.last[over:])]
	}
	l.last = append(l.last, p...)
}
