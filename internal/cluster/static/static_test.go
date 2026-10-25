package static

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCluster(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("home"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Dir = filepath.Base(dir)
	cfg.Headers = map[string]string{"Cache-Control": "max-age=60"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	c := New(&cfg, filepath.Dir(dir))
	if err := c.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(t.Context()) }()

	for _, tt := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/", 200},
		{http.MethodHead, "/index.html", 200},
		{http.MethodGet, "/missing", 404},
		{http.MethodPost, "/", 405},
	} {
		rec := httptest.NewRecorder()
		c.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, http.NoBody))
		if rec.Code != tt.status {
			t.Errorf("%s %s = %d, want %d", tt.method, tt.path, rec.Code, tt.status)
		}
		if tt.status == 200 && rec.Header().Get("Cache-Control") != "max-age=60" {
			t.Errorf("%s %s: no Cache-Control", tt.method, tt.path)
		}
	}

	missing := New(&Config{Dir: filepath.Join(dir, "nope")}, "")
	if err := missing.Start(t.Context()); err == nil {
		t.Error("Start with a missing directory succeeded")
	}
}

func TestValidate(t *testing.T) {
	c := Config{Index: "../x", Forbid: []string{"php"}}
	want := []string{
		"dir: required",
		"index: must be a file name, like index.html",
		`forbid[0]: invalid extension "php": want a dot and a name, like .php`,
	}
	if err := c.Validate(); err == nil || err.Error() != strings.Join(want, "\n") {
		t.Errorf("Validate() =\n%v\nwant\n%s", err, strings.Join(want, "\n"))
	}
}
