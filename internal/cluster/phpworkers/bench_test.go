package phpworkers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/berrymore/kest/internal/relay"
	"github.com/berrymore/kest/internal/worker"
)

// BenchmarkServeHTTP measures what the cluster does per request besides the
// worker: encode the request, decode and write the response (the answers of
// examples/php-hello, from a pool that answers at once).
func BenchmarkServeHTTP(b *testing.B) {
	for _, tt := range []struct {
		name, method, target, ctype, body, header, answer string
	}{
		{"hello", http.MethodGet, "/?name=bench", "", "",
			`{"status":200,"headers":{"Content-Type":["text/plain; charset=utf-8"]}}`, "Hello, bench! (PHP 8.5.12, worker 4242)\n"},
		{"form", http.MethodPost, "/info", "application/x-www-form-urlencoded", "a=1&b[]=2&b[]=3&user[name]=kest",
			`{"status":200,"headers":{"Content-Type":["application/json"]}}`, strings.Repeat("x", 300)},
	} {
		for _, codec := range []string{CodecJSON, CodecProto} {
			b.Run(tt.name+"/"+codec, func(b *testing.B) {
				answer := worker.Payload{Codec: relay.CodecJSON, Context: []byte(tt.header), Body: []byte(tt.answer)}
				p := &fakePool{exec: func(context.Context, worker.Payload) (worker.Payload, error) { return answer, nil }}
				c := newTestCluster(b, func(c *Config) { c.Codec = codec }, p)
				b.ReportAllocs()
				for b.Loop() {
					r := httptest.NewRequestWithContext(b.Context(), tt.method, "http://example.com"+tt.target,
						strings.NewReader(tt.body))
					r.Header.Set("User-Agent", "loadgen")
					r.Header.Set("Accept-Encoding", "gzip")
					if tt.ctype != "" {
						r.Header.Set("Content-Type", tt.ctype)
					}
					rec := httptest.NewRecorder()
					c.ServeHTTP(rec, r)
					if rec.Code != http.StatusOK {
						b.Fatalf("status %d: %s", rec.Code, rec.Body)
					}
				}
			})
		}
	}
}
