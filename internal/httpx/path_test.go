package httpx

import (
	"strings"
	"testing"
)

func TestCleanPath(t *testing.T) {
	tests := []struct {
		in            string
		dots, slashes bool
		want          string
	}{
		{"/", true, true, "/"},
		{"/a/b", true, true, "/a/b"},
		{"/a/b/", true, true, "/a/b/"},
		{"/.well-known/x", true, true, "/.well-known/x"},
		{"/a/./b", true, true, "/a/b"},
		{"/a/.", true, true, "/a/"},
		{"/a/b/..", true, true, "/a/"},
		{"/a/b/../", true, true, "/a/"},
		{"/a/../../b", true, true, "/b"},
		{"/..", true, true, "/"},
		{"/../admin", true, true, "/admin"},
		{"/public/../admin/x", true, true, "/admin/x"},
		{"//admin", true, true, "/admin"},
		{"/a//b///c", true, true, "/a/b/c"},
		{"/a//", true, true, "/a/"},
		{"/a/...", true, true, "/a/..."},
		{"/a/..b/c", true, true, "/a/..b/c"},
		// One option without the other.
		{"/a//b/../c", true, false, "/a//c"},
		{"/a//../b", true, false, "/a/b"},
		{"/a//b/../c", false, true, "/a/b/../c"},
		{"/a/./b", false, true, "/a/./b"},
	}
	for _, tt := range tests {
		if got := cleanPath(tt.in, tt.dots, tt.slashes); got != tt.want {
			t.Errorf("cleanPath(%q, dots %v, slashes %v) = %q, want %q", tt.in, tt.dots, tt.slashes, got, tt.want)
		}
	}
}

func FuzzCleanPath(f *testing.F) {
	for _, s := range []string{"/", "/a/../b", "//a//", "/./", "/a/b/.."} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, p string) {
		p = "/" + p
		clean := cleanPath(p, true, true)
		if clean[0] != '/' {
			t.Fatalf("cleanPath(%q) = %q: not absolute", p, clean)
		}
		if again := cleanPath(clean, true, true); again != clean {
			t.Fatalf("cleanPath(%q) = %q, but cleaning it again gives %q", p, clean, again)
		}
		if strings.Contains(clean, "//") {
			t.Fatalf("cleanPath(%q) = %q has repeated slashes", p, clean)
		}
		for s := range strings.SplitSeq(clean, "/") {
			if s == "." || s == ".." {
				t.Fatalf("cleanPath(%q) = %q has a %q segment", p, clean, s)
			}
		}
	})
}
