package server_test

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/textproto"
	"testing"

	"github.com/berrymore/kest/internal/cluster"
	"github.com/berrymore/kest/internal/config"
	_ "github.com/berrymore/kest/internal/filters/compression"
	_ "github.com/berrymore/kest/internal/filters/headers"
	_ "github.com/berrymore/kest/internal/filters/recovery"
	"github.com/berrymore/kest/internal/logging"
	"github.com/berrymore/kest/internal/registry"
	"github.com/berrymore/kest/internal/server"
)

// streamCluster answers with early hints, then a chunk per value sent on next,
// flushed, until next is closed.
type streamCluster struct {
	next chan string
}

var streams = map[string]*streamCluster{}

func init() {
	cluster.Types.Register(registry.Factory[cluster.Cluster]{
		Type: "test_stream",
		Build: func(bc registry.BuildContext, _ any) (cluster.Cluster, error) {
			s := &streamCluster{next: make(chan string)}
			streams[bc.Name] = s
			return s, nil
		},
	})
}

func (s *streamCluster) Start(context.Context) error { return nil }
func (s *streamCluster) Stop(context.Context) error  { return nil }

func (s *streamCluster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Link", "</app.css>; rel=preload")
	w.WriteHeader(http.StatusEarlyHints)
	w.Header().Del("Link")
	w.Header().Set("Content-Type", "text/event-stream")
	rc := http.NewResponseController(w)
	for {
		select {
		case chunk, ok := <-s.next:
			if !ok {
				return
			}
			_, _ = io.WriteString(w, chunk)
			_ = rc.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// Early hints and flushed chunks get through the whole filter chain: the
// headers filter's hook, gzip and the router's metrics wrappers.
func TestStreamThroughFilters(t *testing.T) {
	src := `version: v1
admin: {address: "127.0.0.1:0"}
listeners:
  - name: main
    address: 127.0.0.1:0
    http:
      filters:
        - type: recovery
        - type: headers
          config: {response: {set: {X-Stack: "yes"}}}
        - type: compression
          config: {min_length: 0}
      virtual_hosts:
        - name: default
          domains: ["*"]
          routes: [{match: {prefix: /}, route: {cluster: events, timeout: 0s}}]
clusters:
  - name: events
    type: test_stream
`
	cfg, err := config.Parse([]byte(src), "kest.yaml", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	s, err := server.New(cfg, logging.Discard())
	if err != nil {
		t.Fatal(err)
	}
	cancel, done := run(t, s)
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	events := streams["events"]

	var hints []string
	trace := &httptrace.ClientTrace{Got1xxResponse: func(code int, h textproto.MIMEHeader) error {
		hints = append(hints, fmt.Sprintf("%d %s", code, h.Get("Link")))
		return nil
	}}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(t.Context(), trace), http.MethodGet,
		"http://"+s.ListenerAddr("main").String()+"/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	go func() { events.next <- "data: one\n\n" }()
	resp, err := (&http.Transport{DisableCompression: true}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "gzip" || resp.Header.Get("X-Stack") != "yes" {
		t.Fatalf("headers %v: want gzip and the headers filter", resp.Header)
	}
	if len(hints) != 1 || hints[0] != "103 </app.css>; rel=preload" {
		t.Errorf("informational responses %q", hints)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	for _, want := range []string{"data: one\n\n", "data: two\n\n"} {
		n, err := gz.Read(buf)
		if err != nil || string(buf[:n]) != want {
			t.Fatalf("read %q, %v; want %q before the stream ends", buf[:n], err, want)
		}
		if want == "data: one\n\n" {
			events.next <- "data: two\n\n"
		}
	}
	close(events.next)
}
