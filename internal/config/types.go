package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Duration is a time.Duration written as a Go duration string: "500ms", "30s", "1m30s".
type Duration time.Duration

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration {
	return time.Duration(d)
}

// String formats the duration like time.Duration.
func (d Duration) String() string {
	return time.Duration(d).String()
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(d.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("invalid duration %q: use a number with a unit, like 500ms, 30s or 1m", b)
	}
	*d = Duration(v)
	return nil
}

// ByteSize is a size in bytes, written as an integer with an optional unit:
// B, K/KB/KiB, M/MB/MiB, G/GB/GiB, T/TB/TiB. All units are powers of 1024,
// as in PHP's memory_limit: "128MB" is 128 × 1024 × 1024 bytes.
type ByteSize int64

// byteUnits lists accepted unit spellings (upper case); names[1] is used for formatting.
var byteUnits = []struct {
	names []string
	size  int64
}{
	{[]string{"T", "TB", "TIB"}, 1 << 40},
	{[]string{"G", "GB", "GIB"}, 1 << 30},
	{[]string{"M", "MB", "MIB"}, 1 << 20},
	{[]string{"K", "KB", "KIB"}, 1 << 10},
	{[]string{"", "B"}, 1},
}

// ParseByteSize parses a size such as "512", "64KB" or "1GiB".
func ParseByteSize(s string) (ByteSize, error) {
	t := strings.TrimSpace(s)
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	num, unit := t[:i], strings.ToUpper(strings.TrimSpace(t[i:]))
	if num == "" {
		return 0, fmt.Errorf("invalid size %q: use an integer with an optional unit, like 512KB or 128MB", s)
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	for _, u := range byteUnits {
		for _, name := range u.names {
			if unit != name {
				continue
			}
			if n > (1<<63-1)/u.size {
				return 0, fmt.Errorf("invalid size %q: too large", s)
			}
			return ByteSize(n * u.size), nil
		}
	}
	return 0, fmt.Errorf("invalid size %q: unknown unit %q (use B, KB, MB, GB or TB)", s, t[i:])
}

// String formats the size with the largest unit that divides it exactly.
func (b ByteSize) String() string {
	if b == 0 {
		return "0B"
	}
	for _, u := range byteUnits {
		if int64(b)%u.size == 0 {
			return strconv.FormatInt(int64(b)/u.size, 10) + u.names[1]
		}
	}
	return strconv.FormatInt(int64(b), 10) + "B"
}

// MarshalText implements encoding.TextMarshaler.
func (b ByteSize) MarshalText() ([]byte, error) {
	return []byte(b.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (b *ByteSize) UnmarshalText(text []byte) error {
	v, err := ParseByteSize(string(text))
	if err != nil {
		return err
	}
	*b = v
	return nil
}
