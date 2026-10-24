package config

import (
	"strings"
	"testing"
	"time"
)

func TestByteSize(t *testing.T) {
	tests := []struct {
		in      string
		want    ByteSize
		str     string
		wantErr string
	}{
		{in: "0", want: 0, str: "0B"},
		{in: "512", want: 512, str: "512B"},
		{in: "512B", want: 512, str: "512B"},
		{in: "64k", want: 64 << 10, str: "64KB"},
		{in: "64KB", want: 64 << 10, str: "64KB"},
		{in: "128M", want: 128 << 20, str: "128MB"},
		{in: "128 MiB", want: 128 << 20, str: "128MB"},
		{in: "1536KB", want: 1536 << 10, str: "1536KB"},
		{in: "2GB", want: 2 << 30, str: "2GB"},
		{in: "1TiB", want: 1 << 40, str: "1TB"},
		{in: "", wantErr: "invalid size"},
		{in: "MB", wantErr: "invalid size"},
		{in: "1.5GB", wantErr: "unknown unit"},
		{in: "10 parsecs", wantErr: "unknown unit"},
		{in: "-1MB", wantErr: "invalid size"},
		{in: "99999999999TB", wantErr: "too large"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseByteSize(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseByteSize(%q) error = %v, want %q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseByteSize(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseByteSize(%q) = %d, want %d", tt.in, got, tt.want)
			}
			if got.String() != tt.str {
				t.Errorf("String() = %q, want %q", got.String(), tt.str)
			}
		})
	}
}

func TestDurationText(t *testing.T) {
	var d Duration
	if err := d.UnmarshalText([]byte("1m30s")); err != nil || d.Std() != 90*time.Second {
		t.Fatalf("UnmarshalText = %v, %v", d, err)
	}
	if b, _ := d.MarshalText(); string(b) != "1m30s" {
		t.Errorf("MarshalText = %q", b)
	}
	if err := d.UnmarshalText([]byte("5")); err == nil {
		t.Error("a number without a unit must be rejected")
	}
}
