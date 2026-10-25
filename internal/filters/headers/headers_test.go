package headers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHeaders(t *testing.T) {
	cfg := Config{
		Request: Rules{
			Remove:      []string{"x-debug"},
			Set:         map[string]string{"x-env": "prod"},
			Add:         map[string]string{"X-Via": "kest"},
			SetIfAbsent: map[string]string{"X-Tenant": "default", "X-Kept": "unused"},
		},
		Response: Rules{
			Remove:      []string{"X-Powered-By", "Date"},
			Set:         map[string]string{"X-Content-Type-Options": "nosniff"},
			Add:         map[string]string{"Vary": "Origin"},
			SetIfAbsent: map[string]string{"Cache-Control": "no-store", "X-Frame-Options": "DENY"},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	var seen http.Header
	h := New(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header
		w.Header().Set("X-Powered-By", "PHP/8.5")
		w.Header().Set("X-Content-Type-Options", "sniff-away")
		w.Header().Set("Vary", "Accept-Encoding")
		w.Header().Set("Cache-Control", "max-age=60")
		w.WriteHeader(http.StatusTeapot)
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	req.Header.Set("X-Debug", "1")
	req.Header.Set("X-Env", "dev")
	req.Header.Set("X-Via", "lb")
	req.Header.Set("X-Kept", "client")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	for name, want := range map[string]string{"X-Debug": "", "X-Env": "prod", "X-Via": "lb,kest", "X-Tenant": "default",
		"X-Kept": "client"} {
		if got := strings.Join(seen.Values(name), ","); got != want {
			t.Errorf("request %s = %q, want %q", name, got, want)
		}
	}
	if req.Header.Get("X-Env") != "dev" {
		t.Error("the filter changed the request it got")
	}
	resp := rec.Result().Header
	for name, want := range map[string]string{"X-Powered-By": "", "X-Content-Type-Options": "nosniff",
		"Vary": "Accept-Encoding,Origin", "Cache-Control": "max-age=60", "X-Frame-Options": "DENY"} {
		if got := strings.Join(resp.Values(name), ","); got != want {
			t.Errorf("response %s = %q, want %q", name, got, want)
		}
	}
	if v, ok := rec.Header()["Date"]; !ok || v != nil {
		t.Errorf("Date = %v, %v; want a nil value, which stops net/http from adding one", v, ok)
	}
}

func TestValidate(t *testing.T) {
	cfg := Config{
		Request:  Rules{Remove: []string{"Host"}, Set: map[string]string{"Bad Name": "x"}},
		Response: Rules{Add: map[string]string{"X-Ok": "line\r\nbreak"}, SetIfAbsent: map[string]string{"content-length": "1"}},
	}
	want := []string{
		"request.remove[0]: Host can't be changed",
		`request.set.Bad Name: invalid header name "Bad Name"`,
		"response.add.X-Ok: the value has control characters",
		"response.set_if_absent.content-length: Content-Length can't be changed",
	}
	if err := cfg.Validate(); err == nil || err.Error() != strings.Join(want, "\n") {
		t.Errorf("Validate() =\n%v\nwant\n%s", err, strings.Join(want, "\n"))
	}
}
