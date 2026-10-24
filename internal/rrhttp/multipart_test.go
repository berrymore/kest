package rrhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/berrymore/kest/internal/relay"
)

type testFile struct {
	field, name, mime, content string
}

func multipartFiles(t *testing.T, values map[string]string, files ...testFile) (contentType, body string) {
	t.Helper()
	return multipartBody(t, func(w *multipart.Writer) {
		for k, v := range values {
			_ = w.WriteField(k, v)
		}
		for _, f := range files {
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", `form-data; name="`+f.field+`"; filename="`+f.name+`"`)
			if f.mime != "" {
				h.Set("Content-Type", f.mime)
			}
			pw, err := w.CreatePart(h)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = pw.Write([]byte(f.content))
		}
	})
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func uploadTree(t *testing.T, req *Request) map[string]any {
	t.Helper()
	var tree map[string]any
	if err := json.Unmarshal(req.Uploads, &tree); err != nil {
		t.Fatalf("uploads %q: %v", req.Uploads, err)
	}
	return tree
}

func TestUploads(t *testing.T) {
	dir := t.TempDir()
	ct, body := multipartFiles(t, map[string]string{"title": "kest"},
		testFile{"doc", "report.pdf", "application/pdf", "%PDF-1.7"},
		testFile{"photos[]", "a.jpg", "image/jpeg", "jpeg-a"},
		testFile{"photos[]", "../../b.jpg", "image/jpeg", "jpeg-bb"},
		testFile{"user[avatar]", "me.png", "", "png"},
		testFile{"single", "first.txt", "text/plain", "1"},
		testFile{"single", "second.txt", "text/plain", "22"},
	)
	req := mustRequest(t, newHTTPRequest(t, http.MethodPost, "/", ct, body), Options{Codec: relay.CodecJSON, Uploads: Uploads{Dir: dir}})

	if !req.Parsed || string(req.Body) != `{"title":"kest"}` {
		t.Errorf("parsed=%v body=%s", req.Parsed, req.Body)
	}
	tree := uploadTree(t, req)
	leaf := func(v any) map[string]any {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("not a file: %v", v)
		}
		return m
	}
	check := func(f map[string]any, name, mime, content string) {
		t.Helper()
		if f["name"] != name || f["mime"] != mime || f["error"] != float64(0) || f["size"] != float64(len(content)) {
			t.Errorf("file %v, want %s %s %d bytes", f, name, mime, len(content))
		}
		tmp, _ := f["tmpName"].(string)
		if filepath.Dir(tmp) != dir {
			t.Errorf("tmpName %q is not in %s", tmp, dir)
		}
		if got, err := os.ReadFile(tmp); err != nil || string(got) != content {
			t.Errorf("%s holds %q, %v; want %q", tmp, got, err, content)
		}
	}
	check(leaf(tree["doc"]), "report.pdf", "application/pdf", "%PDF-1.7")
	photos, _ := tree["photos"].([]any)
	if len(photos) != 2 {
		t.Fatalf("photos = %v", tree["photos"])
	}
	check(leaf(photos[0]), "a.jpg", "image/jpeg", "jpeg-a")
	check(leaf(photos[1]), "b.jpg", "image/jpeg", "jpeg-bb") // the client's path is dropped
	user, _ := tree["user"].(map[string]any)
	check(leaf(user["avatar"]), "me.png", "", "png")            // no Content-Type: empty, as in RoadRunner
	check(leaf(tree["single"]), "first.txt", "text/plain", "1") // a scalar key keeps the first file, as in RoadRunner

	if got := dirEntries(t, dir); len(got) != 6 {
		t.Errorf("temp files %v, want 6", got)
	}
	if err := req.Close(); err != nil {
		t.Fatal(err)
	}
	if got := dirEntries(t, dir); len(got) != 0 {
		t.Errorf("temp files left after Close: %v", got)
	}
	if err := req.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestUploadsInPayload(t *testing.T) {
	ct, body := multipartFiles(t, nil, testFile{"f", "a.txt", "text/plain", "x"})
	req := mustRequest(t, newHTTPRequest(t, http.MethodPost, "/", ct, body), Options{Codec: relay.CodecJSON, Uploads: Uploads{Dir: t.TempDir()}})
	defer func() { _ = req.Close() }()

	p, err := req.Payload(relay.CodecJSON)
	if err != nil {
		t.Fatal(err)
	}
	var ctx struct {
		Uploads map[string]map[string]any `json:"uploads"`
	}
	if err := json.Unmarshal(p.Context, &ctx); err != nil || ctx.Uploads["f"]["name"] != "a.txt" {
		t.Errorf("JSON uploads %v, %v", ctx.Uploads, err)
	}

	p, err = req.Payload(relay.CodecProto)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for b := p.Context; len(b) > 0; {
		field, _, data, rest, err := nextField(b)
		if err != nil {
			t.Fatal(err)
		}
		if field == reqUploads {
			found = bytes.Equal(data, req.Uploads)
		}
		b = rest
	}
	if !found {
		t.Errorf("protobuf context has no uploads field %s: %x", req.Uploads, p.Context)
	}
}

func TestUploadsEmptyTree(t *testing.T) {
	ct, body := multipartFiles(t, map[string]string{"a": "1"})
	req := mustRequest(t, newHTTPRequest(t, http.MethodPost, "/", ct, body), jsonOpts)
	if string(req.Uploads) != "{}" {
		t.Errorf("uploads = %q, want {}", req.Uploads)
	}
	// Other requests have no upload tree.
	plain := mustRequest(t, newHTTPRequest(t, http.MethodPost, "/", "application/x-www-form-urlencoded", "a=1"), jsonOpts)
	p, _ := plain.Payload(relay.CodecJSON)
	if plain.Uploads != nil || !strings.Contains(string(p.Context), `"uploads":null`) {
		t.Errorf("uploads = %q, context %s", plain.Uploads, p.Context)
	}
}

func TestUploadsExtensions(t *testing.T) {
	tests := []struct {
		name          string
		forbid, allow []string
		file          string
		wantError     int
	}{
		{"forbidden", []string{".php"}, nil, "shell.PHP", uploadExtension},
		{"not forbidden", []string{".php"}, nil, "a.txt", uploadOK},
		{"allowed", nil, []string{".jpg", ".png"}, "a.JPG", uploadOK},
		{"not allowed", nil, []string{".jpg"}, "a.png", uploadExtension},
		{"no extension", nil, []string{".jpg"}, "README", uploadExtension},
		{"forbid wins", []string{".jpg"}, []string{".jpg"}, "a.jpg", uploadExtension},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			ct, body := multipartFiles(t, nil, testFile{"f", tt.file, "", "data"})
			req := mustRequest(t, newHTTPRequest(t, http.MethodPost, "/", ct, body),
				Options{Codec: relay.CodecJSON, Uploads: Uploads{Dir: dir, Forbid: tt.forbid, Allow: tt.allow}})
			defer func() { _ = req.Close() }()
			f, _ := uploadTree(t, req)["f"].(map[string]any)
			if f["error"] != float64(tt.wantError) {
				t.Errorf("error = %v, want %d", f["error"], tt.wantError)
			}
			if stored := len(dirEntries(t, dir)) == 1; stored != (tt.wantError == uploadOK) {
				t.Errorf("stored = %v with error %v", stored, f["error"])
			}
		})
	}
}

func TestUploadsNoTempDir(t *testing.T) {
	ct, body := multipartFiles(t, nil, testFile{"f", "a.txt", "", "data"})
	missing := filepath.Join(t.TempDir(), "missing")
	req := mustRequest(t, newHTTPRequest(t, http.MethodPost, "/", ct, body),
		Options{Codec: relay.CodecJSON, Uploads: Uploads{Dir: missing}})
	f, _ := uploadTree(t, req)["f"].(map[string]any)
	if f["error"] != float64(uploadNoTmpDir) || f["tmpName"] != "" {
		t.Errorf("file %v, want error %d", f, uploadNoTmpDir)
	}
	if errs := req.UploadErrors(); len(errs) != 1 || !errors.Is(errs[0], os.ErrNotExist) {
		t.Errorf("UploadErrors = %v", errs)
	}
}

func TestUploadsCleanupOnError(t *testing.T) {
	ct, body := multipartFiles(t, nil,
		testFile{"a", "a.txt", "", strings.Repeat("a", 1000)},
		testFile{"b", "b.txt", "", strings.Repeat("b", 1000)},
	)
	conflict, conflictBody := multipartFiles(t, nil, testFile{"f", "a.txt", "", "x"}, testFile{"f[x]", "b.txt", "", "y"})
	tests := []struct {
		name  string
		ctype string
		body  string
		limit int64
		want  string
	}{
		{"truncated", ct, body[:1500], 0, "unexpected EOF"},
		{"body limit", ct, body, 1500, "request body too large"},
		{"tree conflict", conflict, conflictBody, 0, "invalid multiple values"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			r := newHTTPRequest(t, http.MethodPost, "/", tt.ctype, tt.body)
			if tt.limit > 0 {
				r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, tt.limit)
			}
			_, err := NewRequest(r, Options{Codec: relay.CodecJSON, Uploads: Uploads{Dir: dir}})
			if !isType[*RequestError](err) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want a *RequestError containing %q", err, tt.want)
			}
			if got := dirEntries(t, dir); len(got) != 0 {
				t.Errorf("temp files left: %v", got)
			}
		})
	}
}

func TestUploadsRawBody(t *testing.T) {
	dir := t.TempDir()
	ct, body := multipartFiles(t, nil, testFile{"f", "a.txt", "", "data"})
	req := mustRequest(t, newHTTPRequest(t, http.MethodPost, "/", ct, body),
		Options{Codec: relay.CodecJSON, RawBody: true, Uploads: Uploads{Dir: dir}})
	if req.Parsed || req.Uploads != nil || string(req.Body) != body || len(dirEntries(t, dir)) != 0 {
		t.Errorf("raw body: parsed=%v uploads=%q, %d bytes", req.Parsed, req.Uploads, len(req.Body))
	}
}
