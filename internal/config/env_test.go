package config

import (
	"strings"
	"testing"
)

func TestExpandEnv(t *testing.T) {
	env := map[string]string{"HOST": "example.com", "PORT": "8080", "EMPTY": ""}
	lookup := func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}

	tests := []struct {
		in      string
		want    string
		wantErr string
	}{
		{in: "plain", want: "plain"},
		{in: "${HOST}:${PORT}", want: "example.com:8080"},
		{in: "http://${HOST}/x", want: "http://example.com/x"},
		{in: "${MISSING:-fallback}", want: "fallback"},
		{in: "${EMPTY:-fallback}", want: "fallback"},
		{in: "${EMPTY}", want: ""},
		{in: "${MISSING:-}", want: ""},
		{in: "${HOST:-unused}", want: "example.com"},
		{in: "$${HOST}", want: "${HOST}"},
		{in: "pa$$word", want: "pa$$word"},
		{in: "cost: $5", want: "cost: $5"},
		{in: "$", want: "$"},
		{in: "${MISSING}", wantErr: "environment variable MISSING is not set"},
		{in: "${HOST", wantErr: "unterminated variable reference"},
		{in: "${}", wantErr: "invalid variable reference"},
		{in: "${1ABC}", wantErr: "invalid variable reference"},
		{in: "${A-B}", wantErr: "invalid variable reference"},
		{in: "${A:-${B}}", wantErr: "nested variable references are not supported"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := expandEnv(tt.in, lookup)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expandEnv(%q) error = %v, want %q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("expandEnv(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("expandEnv(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func FuzzExpandEnv(f *testing.F) {
	for _, s := range []string{"", "x", "${A}", "${A:-b}", "$${A}", "${", "$", "${A:-${B}}", "a$b${C}d"} {
		f.Add(s)
	}
	lookup := func(name string) (string, bool) {
		if name == "A" {
			return "value", true
		}
		return "", false
	}

	f.Fuzz(func(t *testing.T, s string) {
		got, err := expandEnv(s, lookup)
		if err != nil {
			return
		}
		if !strings.Contains(s, "${") && got != s {
			t.Errorf("expandEnv(%q) = %q; input without references must be unchanged", s, got)
		}
	})
}
