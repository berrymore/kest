package sendfile

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/logging"
)

// tree makes files/report.csv (1000 bytes), files/link.csv (a link to it),
// files/escape.csv (a link to secret.txt), files/sub/ and secret.txt beside
// files/.
func tree(t *testing.T) (root, files string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files = filepath.Join(root, "files")
	if err := os.MkdirAll(filepath.Join(files, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(files, "report.csv"), strings.Repeat("0123456789", 100))
	write(filepath.Join(root, "secret.txt"), "secret")
	for link, target := range map[string]string{"link.csv": "report.csv", "escape.csv": "../secret.txt"} {
		if err := os.Symlink(target, filepath.Join(files, link)); err != nil {
			t.Fatal(err)
		}
	}
	return root, files
}

// app answers like Symfony's BinaryFileResponse with X-Sendfile: the header,
// the application's own headers, Content-Length of the file, no body. It
// records the X-Sendfile-Type it was asked with.
type app struct {
	file   string
	status int
	asked  string
}

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.asked = r.Header.Get(TypeHeader)
	w.Header().Set(Header, a.file)
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="report.csv"`)
	w.Header().Set("Content-Length", "1000")
	status := a.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
}

func handler(t *testing.T, root string, next http.Handler, logger *slog.Logger) http.Handler {
	t.Helper()
	f, err := New(&Config{Dirs: []string{"files"}}, root, logger)
	if err != nil {
		t.Fatal(err)
	}
	return f(next)
}

func do(t *testing.T, h http.Handler, method string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, "/export", http.NoBody)
	maps.Copy(req.Header, header)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSendfile(t *testing.T) {
	root, files := tree(t)
	a := &app{file: filepath.Join(files, "report.csv")}
	h := handler(t, root, a, logging.Discard())

	rec := do(t, h, http.MethodGet, http.Header{TypeHeader: {"X-Accel-Redirect"}})
	if rec.Code != http.StatusOK || rec.Body.Len() != 1000 || rec.Header().Get("Content-Length") != "1000" {
		t.Errorf("GET = %d, %d bytes, Content-Length %q", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Length"))
	}
	for name, want := range map[string]string{"Content-Type": "text/csv", Header: "",
		"Content-Disposition": `attachment; filename="report.csv"`, "Accept-Ranges": "bytes"} {
		if got := rec.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if a.asked != Header {
		t.Errorf("the application was asked with %s %q, want %q (the client's replaced)", TypeHeader, a.asked, Header)
	}

	rec = do(t, h, http.MethodGet, http.Header{"Range": {"bytes=10-19"}})
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "0123456789" ||
		rec.Header().Get("Content-Range") != "bytes 10-19/1000" {
		t.Errorf("a range = %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Range"))
	}

	modified := rec.Header().Get("Last-Modified")
	if _, err := http.ParseTime(modified); err != nil {
		t.Fatalf("Last-Modified %q: %v", modified, err)
	}
	if rec := do(t, h, http.MethodGet, http.Header{"If-Modified-Since": {modified}}); rec.Code != http.StatusNotModified {
		t.Errorf("If-Modified-Since = %d, want 304", rec.Code)
	}
	if rec := do(t, h, http.MethodHead, nil); rec.Code != http.StatusOK || rec.Body.Len() != 0 ||
		rec.Header().Get("Content-Length") != "1000" {
		t.Errorf("HEAD = %d, %d bytes, Content-Length %q", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Length"))
	}

	a.file = filepath.Join(files, "link.csv") // a link inside the directory
	if rec := do(t, h, http.MethodGet, nil); rec.Code != http.StatusOK || rec.Body.Len() != 1000 {
		t.Errorf("a link inside = %d, %d bytes", rec.Code, rec.Body.Len())
	}
}

func TestSendfileRefused(t *testing.T) {
	root, files := tree(t)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	for _, tt := range []struct {
		file string
		want int
	}{
		{filepath.Join(files, "missing.csv"), http.StatusNotFound},
		{filepath.Join(files, "sub"), http.StatusNotFound}, // a directory
		{files, http.StatusForbidden},                      // not inside
		{filepath.Join(root, "secret.txt"), http.StatusForbidden},
		{filepath.Join(files, "escape.csv"), http.StatusForbidden}, // a link out
		{filepath.Join(files, "..", "secret.txt"), http.StatusForbidden},
		{"files/report.csv", http.StatusForbidden}, // relative
	} {
		logs.Reset()
		rec := do(t, handler(t, root, &app{file: tt.file}, logger), http.MethodGet, nil)
		if rec.Code != tt.want || strings.Contains(rec.Body.String(), "secret") || rec.Header().Get(Header) != "" ||
			rec.Header().Get("Content-Disposition") != "" {
			t.Errorf("%s: %d %q %v, want %d without the file or its headers", tt.file, rec.Code, rec.Body.String(),
				rec.Header(), tt.want)
		}
		if !strings.Contains(logs.String(), "X-Sendfile refused") {
			t.Errorf("%s: not logged: %q", tt.file, logs.String())
		}
	}
}

// Other statuses pass through, without the header; so do responses without it.
func TestSendfilePassThrough(t *testing.T) {
	root, files := tree(t)
	rec := do(t, handler(t, root, &app{file: filepath.Join(files, "report.csv"), status: http.StatusNotFound},
		logging.Discard()), http.MethodGet, nil)
	if rec.Code != http.StatusNotFound || rec.Header().Get(Header) != "" {
		t.Errorf("a 404 with X-Sendfile = %d %v", rec.Code, rec.Header())
	}
	plain := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "plain")
		_ = http.NewResponseController(w).Flush()
	})
	rec = do(t, handler(t, root, plain, logging.Discard()), http.MethodGet, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "plain" || !rec.Flushed {
		t.Errorf("a plain response = %d %q flushed %v", rec.Code, rec.Body.String(), rec.Flushed)
	}

	// 103 Early Hints go out before the file.
	hints := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", "</style.css>; rel=preload")
		w.WriteHeader(http.StatusEarlyHints)
		(&app{file: filepath.Join(files, "report.csv")}).ServeHTTP(w, r)
	})
	cw := &codes{ResponseRecorder: httptest.NewRecorder()}
	handler(t, root, hints, logging.Discard()).ServeHTTP(cw,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))
	if len(cw.codes) != 2 || cw.codes[0] != http.StatusEarlyHints || cw.codes[1] != http.StatusOK || cw.Body.Len() != 1000 {
		t.Errorf("codes %v, %d bytes; want 103, 200 and the file", cw.codes, cw.Body.Len())
	}
}

// codes records every status written.
type codes struct {
	*httptest.ResponseRecorder
	codes []int
}

func (c *codes) WriteHeader(code int) {
	c.codes = append(c.codes, code)
	c.ResponseRecorder.WriteHeader(code)
}

func TestNew(t *testing.T) {
	root, files := tree(t)
	if err := (&Config{}).Validate(); err == nil || !strings.Contains(err.Error(), "dirs: at least one directory") {
		t.Errorf("Validate without dirs = %v", err)
	}
	for _, bad := range []string{"missing", filepath.Join(files, "report.csv")} {
		_, err := New(&Config{Dirs: []string{"files", bad}}, root, logging.Discard())
		if fe, ok := errors.AsType[*config.FieldError](err); !ok || fe.Field != "dirs[1]" {
			t.Errorf("New with %s: %v, want an error on dirs[1]", bad, err)
		}
	}
}
