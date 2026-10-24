// Package logging builds the process logger from configuration.
package logging

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/berrymore/kest/internal/config"
)

// New returns a logger that writes to w with the configured level and format.
func New(cfg config.Log, w io.Writer) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		return nil, fmt.Errorf("log level: %w", err)
	}
	opts := &slog.HandlerOptions{Level: level}

	switch cfg.Format {
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("unknown log format %q", cfg.Format)
	}
}

// Discard returns a logger that drops everything.
func Discard() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
