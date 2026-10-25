package staticfiles

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"index.html":                "<h1>home</h1>",
		"style.css":                 "body{}",
		"index.php":                 "<?php secret();",
		".env":                      "SECRET=1",
		".git/config":               "[core]",
		".well-known/security.txt":  "Contact: x",
		"docs/index.html":           "<h1>docs</h1>",
		"docs/guide.txt":            "0123456789",
		"assets/.hidden/x.js":       "hidden",
		"../outside/not-served.txt": "outside",
		"nested/deeper/app.min.JS":  "js",
		"uploads/user/avatar.PHP":   "<?php evil();",
		"uploads/user/readme.md":    "ok",
		"docs/sub/../sneaky.txt":    "sneaky",
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The operator's symlinks are followed, also out of the directory.
	if err := os.Symlink(filepath.Join(dir, "..", "outside", "not-served.txt"), filepath.Join(dir, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "style.css"), filepath.Join(dir, "inside.css")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func open(t *testing.T, opts Options) *Files {
	t.Helper()
	f := New(opts)
	if err := f.Check(); err != nil {
		t.Fatal(err)
	}
	return f
}

// serve returns whether the files answered, and the response.
func serve(t *testing.T, f *Files, method, target string, headers ...string) (bool, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, target, http.NoBody)
	for _, h := range headers {
		name, value, _ := strings.Cut(h, ": ")
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	return f.Serve(rec, req), rec
}

func TestServe(t *testing.T) {
	dir := writeTree(t)
	php := open(t, Options{Dir: dir, Forbid: []string{".php"}, Headers: map[string]string{"Cache-Control": "public, max-age=60"}})
	site := open(t, Options{Dir: dir, Index: "index.html"})

	for _, tt := range []struct {
		files  *Files
		target string
		served bool
		status int
		body   string
	}{
		{php, "/style.css", true, 200, "body{}"},
		{php, "/inside.css", true, 200, "body{}"},
		{php, "/docs/guide.txt?v=2", true, 200, "0123456789"},
		{php, "/.well-known/security.txt", true, 200, "Contact: x"},
		{php, "/nested/deeper/app.min.JS", true, 200, "js"},
		{php, "/", false, 0, ""},
		{php, "/docs/", false, 0, ""},
		{php, "/docs", false, 0, ""},
		{php, "/index.php", false, 0, ""},
		{php, "/uploads/user/avatar.PHP", false, 0, ""},
		{php, "/.env", false, 0, ""},
		{php, "/.git/config", false, 0, ""},
		{php, "/assets/.hidden/x.js", false, 0, ""},
		{php, "/docs/.well-known/x", false, 0, ""},
		{php, "/escape.txt", true, 200, "outside"},
		{php, "/../outside/not-served.txt", false, 0, ""},
		{php, "/missing.css", false, 0, ""},
		{site, "/", true, 200, "<h1>home</h1>"},
		{site, "/docs/", true, 200, "<h1>docs</h1>"},
		{site, "/docs?x=1", true, 301, ""},
		{site, "/index.php", true, 200, "<?php secret();"},
	} {
		served, rec := serve(t, tt.files, http.MethodGet, tt.target)
		if served != tt.served || (served && (rec.Code != tt.status || (tt.body != "" && rec.Body.String() != tt.body))) {
			t.Errorf("GET %s: served %v, %d %q; want %v, %d %q", tt.target, served, rec.Code, rec.Body.String(),
				tt.served, tt.status, tt.body)
		}
	}

	_, rec := serve(t, site, http.MethodGet, "/docs?x=1")
	if got := rec.Header().Get("Location"); got != "/docs/?x=1" {
		t.Errorf("directory redirect to %q, want /docs/?x=1", got)
	}
	_, rec = serve(t, php, http.MethodGet, "/style.css")
	h := rec.Header()
	if h.Get("Content-Type") != "text/css; charset=utf-8" || h.Get("Cache-Control") != "public, max-age=60" ||
		!strings.HasPrefix(h.Get("Etag"), `W/"`) || h.Get("Last-Modified") == "" || h.Get("Content-Length") != "6" {
		t.Errorf("headers %v", h)
	}
	if served, _ := serve(t, php, http.MethodPost, "/style.css"); served {
		t.Error("served a POST")
	}
}

func TestConditionalAndRange(t *testing.T) {
	f := open(t, Options{Dir: writeTree(t)})
	_, first := serve(t, f, http.MethodGet, "/docs/guide.txt")
	etag := first.Header().Get("Etag")

	_, rec := serve(t, f, http.MethodGet, "/docs/guide.txt", "If-None-Match: "+etag)
	if rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d, want 304", rec.Code)
	}
	_, rec = serve(t, f, http.MethodGet, "/docs/guide.txt", "Range: bytes=2-4")
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "234" {
		t.Errorf("Range: %d %q, want 206 \"234\"", rec.Code, rec.Body.String())
	}
	_, rec = serve(t, f, http.MethodHead, "/docs/guide.txt")
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "10" {
		t.Errorf("HEAD: %d, %d body bytes, length %s", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Length"))
	}
}

func TestCheck(t *testing.T) {
	dir := writeTree(t)
	for _, bad := range []string{filepath.Join(dir, "missing"), filepath.Join(dir, "style.css")} {
		if err := New(Options{Dir: bad}).Check(); err == nil {
			t.Errorf("Check(%s) succeeded", bad)
		}
	}
}

func TestValidateFields(t *testing.T) {
	err := ValidateFields("static.", []string{".php", "php", ".a/b"}, map[string]string{"Bad Name": "x", "X-Ok": "a\nb"})
	want := []string{
		`static.forbid[1]: invalid extension "php": want a dot and a name, like .php`,
		`static.forbid[2]: invalid extension ".a/b": want a dot and a name, like .php`,
		`static.headers.Bad Name: invalid header name "Bad Name"`,
		`static.headers.X-Ok: the value has control characters`,
	}
	if err == nil || err.Error() != strings.Join(want, "\n") {
		t.Errorf("ValidateFields =\n%v\nwant\n%s", err, strings.Join(want, "\n"))
	}
}
