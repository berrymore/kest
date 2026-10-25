package static

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/berrymore/kest/internal/config"
)

// Files are answered; anything else reaches the rest of the chain.
func TestFilter(t *testing.T) {
	dir := writeTree(t)
	cfg := DefaultConfig()
	cfg.Dir = filepath.Base(dir) // relative to the config file's directory
	filter, err := New(&cfg, filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	h := filter(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "php")
	}))
	for _, tt := range []struct{ method, path, want string }{
		{http.MethodGet, "/style.css", "body{}"},
		{http.MethodHead, "/style.css", ""},
		{http.MethodGet, "/index.php", "php"},
		{http.MethodGet, "/", "php"},
		{http.MethodGet, "/missing.css", "php"},
		{http.MethodPost, "/style.css", "php"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, http.NoBody))
		if got := rec.Body.String(); got != tt.want {
			t.Errorf("%s %s = %q, want %q", tt.method, tt.path, got, tt.want)
		}
	}
}

// A dir that isn't a directory is reported on the dir field.
func TestNewDir(t *testing.T) {
	dir := writeTree(t)
	for _, bad := range []string{"missing", "style.css"} {
		cfg := DefaultConfig()
		cfg.Dir = bad
		_, err := New(&cfg, dir)
		if fe, ok := errors.AsType[*config.FieldError](err); !ok || fe.Field != "dir" {
			t.Errorf("New with dir %s: %v, want an error on dir", bad, err)
		}
	}
}

func TestValidate(t *testing.T) {
	cfg := Config{Forbid: []string{".php", "php", ".a/b"}, Headers: map[string]string{"Bad Name": "x", "X-Ok": "a\nb"}}
	err := cfg.Validate()
	want := []string{
		"dir: required",
		`forbid[1]: invalid extension "php": want a dot and a name, like .php`,
		`forbid[2]: invalid extension ".a/b": want a dot and a name, like .php`,
		`headers.Bad Name: invalid header name "Bad Name"`,
		`headers.X-Ok: the value has control characters`,
	}
	if err == nil || err.Error() != strings.Join(want, "\n") {
		t.Errorf("Validate =\n%v\nwant\n%s", err, strings.Join(want, "\n"))
	}
}
