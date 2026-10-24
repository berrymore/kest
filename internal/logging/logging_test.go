package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/berrymore/kest/internal/config"
)

func TestNew(t *testing.T) {
	var buf bytes.Buffer
	logger, err := New(config.Log{Level: "warn", Format: "json"}, &buf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logger.Info("dropped")
	logger.Warn("kept", "listener", "main")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not one JSON line: %v\n%s", err, buf.String())
	}
	if entry["msg"] != "kept" || entry["listener"] != "main" {
		t.Errorf("entry = %v", entry)
	}
}

func TestNewText(t *testing.T) {
	var buf bytes.Buffer
	logger, err := New(config.Log{Level: "debug", Format: "text"}, &buf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logger.Debug("hello", "cluster", "app")
	if out := buf.String(); !strings.Contains(out, "level=DEBUG") || !strings.Contains(out, "cluster=app") {
		t.Errorf("output = %q", out)
	}
}

func TestNewErrors(t *testing.T) {
	if _, err := New(config.Log{Level: "loud", Format: "text"}, &bytes.Buffer{}); err == nil {
		t.Error("unknown level accepted")
	}
	if _, err := New(config.Log{Level: "info", Format: "xml"}, &bytes.Buffer{}); err == nil {
		t.Error("unknown format accepted")
	}
}
