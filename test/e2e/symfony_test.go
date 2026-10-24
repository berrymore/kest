//go:build e2e && integration

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// TestSymfonySmoke boots an unmodified Symfony app on the RoadRunner runtime
// (testdata/symfony) behind kest, with both codecs: the runtime pulls in
// spiral/roadrunner-http 3.6, which reads JSON and protobuf.
func TestSymfonySmoke(t *testing.T) {
	const dir = "testdata/symfony"
	requirePHP(t, dir)
	for _, codec := range []string{"json", "proto"} {
		t.Run(codec, func(t *testing.T) { symfonySmoke(t, dir, codec) })
	}
}

func symfonySmoke(t *testing.T, dir, codec string) {
	t.Helper()
	listenAddr, adminAddr := freeAddr(t), freeAddr(t)
	k := serve(t, filepath.Join(dir, "kest.yaml"), adminAddr, "KEST_ADDRESS="+listenAddr, "KEST_ADMIN_ADDRESS="+adminAddr,
		"KEST_CODEC="+codec, "APP_CACHE_DIR="+t.TempDir())
	base := "http://" + listenAddr
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	type reply struct {
		StatusCode int
		Header     http.Header
	}
	do := func(req *http.Request) (reply, string) {
		t.Helper()
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return reply{resp.StatusCode, resp.Header}, string(b)
	}
	request := func(method, path, contentType string, body io.Reader) *http.Request {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, base+path, body)
		if err != nil {
			t.Fatal(err)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		return req
	}

	// Routing, query, cookies, client IP; the kernel stays booted between requests.
	served := 0
	for range 4 {
		req := request(http.MethodGet, "/hello/kest?x=1", "", http.NoBody)
		req.AddCookie(&http.Cookie{Name: "session", Value: "abc"})
		resp, body := do(req)
		var hello struct {
			Hello    string            `json:"hello"`
			Query    map[string]string `json:"query"`
			Scheme   string            `json:"scheme"`
			ClientIP string            `json:"client_ip"`
			Cookies  map[string]string `json:"cookies"`
			Served   int               `json:"served_by_this_worker"`
		}
		if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &hello) != nil {
			t.Fatalf("GET /hello/kest: %d %s\n%s", resp.StatusCode, body, k.logs.String())
		}
		if hello.Hello != "kest" || hello.Query["x"] != "1" || hello.Scheme != "http" || hello.ClientIP != "127.0.0.1" ||
			hello.Cookies["session"] != "abc" {
			t.Errorf("GET /hello/kest: %+v", hello)
		}
		served = max(served, hello.Served)
	}
	if served < 2 {
		t.Errorf("no worker served more than one request: the kernel isn't kept between requests")
	}

	// A form with a file upload.
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	_ = w.WriteField("title", "smoke")
	fw, _ := w.CreateFormFile("doc", "notes.txt")
	_, _ = fw.Write([]byte("uploaded through kest"))
	_ = w.Close()
	resp, body := do(request(http.MethodPost, "/form", w.FormDataContentType(), &b))
	if resp.StatusCode != http.StatusOK ||
		body != `{"fields":{"title":"smoke"},"files":{"doc":{"name":"notes.txt","content":"uploaded through kest"}}}` {
		t.Errorf("POST /form: %d %s", resp.StatusCode, body)
	}

	// A redirect that sets a cookie.
	resp, _ = do(request(http.MethodGet, "/redirect", "", http.NoBody))
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/hello/redirected" ||
		!strings.HasPrefix(resp.Header.Get("Set-Cookie"), "visited=yes;") {
		t.Errorf("GET /redirect: %d %v", resp.StatusCode, resp.Header)
	}

	// Symfony's own error pages.
	if resp, _ := do(request(http.MethodGet, "/nope", "", http.NoBody)); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /nope: %d, want 404", resp.StatusCode)
	}
	if resp, _ := do(request(http.MethodGet, "/boom", "", http.NoBody)); resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("GET /boom: %d, want 500", resp.StatusCode)
	}

	k.stop(t)
	if logs := k.logs.String(); !strings.Contains(logs, `Uncaught PHP Exception RuntimeException: \"boom\"`) {
		t.Errorf("Symfony's log line for /boom is not in kest's log:\n%s", logs)
	}
}
