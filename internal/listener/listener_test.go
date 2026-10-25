package listener

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/berrymore/kest/internal/leaktest"
	"github.com/berrymore/kest/internal/logging"
)

func TestMain(m *testing.M) { leaktest.VerifyTestMain(m) }

func start(t *testing.T, h http.Handler) *HTTP {
	t.Helper()
	l := NewHTTP("test", "127.0.0.1:0", h, DefaultLimits(), logging.Discard(), func(err error) {
		t.Errorf("unexpected fatal error: %v", err)
	})
	if err := l.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return l
}

func get(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func TestServeAndStop(t *testing.T) {
	l := start(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello")
	}))
	if l.Name() != "test" {
		t.Errorf("Name() = %q", l.Name())
	}

	got, err := get(t.Context(), "http://"+l.Addr().String()+"/")
	if err != nil || got != "hello" {
		t.Fatalf("GET = %q, %v", got, err)
	}

	if err := l.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := get(t.Context(), "http://"+l.Addr().String()+"/"); err == nil {
		t.Error("the listener still accepts connections after Stop")
	}
}

func TestStopDrainsInFlightRequests(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	l := start(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		_, _ = io.WriteString(w, "done")
	}))

	type result struct {
		body string
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		body, err := get(context.Background(), "http://"+l.Addr().String()+"/")
		resCh <- result{body, err}
	}()
	<-entered

	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stopped <- l.Stop(ctx)
	}()

	select {
	case err := <-stopped:
		t.Fatalf("Stop returned before the in-flight request finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	if res := <-resCh; res.err != nil || res.body != "done" {
		t.Errorf("in-flight request = %q, %v; want it to complete", res.body, res.err)
	}
	if err := <-stopped; err != nil {
		t.Errorf("Stop: %v", err)
	}
}

func TestStopClosesAfterDeadline(t *testing.T) {
	entered := make(chan struct{})
	l := start(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done() // never finishes on its own
	}))

	errCh := make(chan error, 1)
	go func() {
		_, err := get(context.Background(), "http://"+l.Addr().String()+"/")
		errCh <- err
	}()
	<-entered

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := l.Stop(ctx); err != nil {
		t.Errorf("Stop: %v", err)
	}
	if err := <-errCh; err == nil {
		t.Error("the stuck request succeeded; want its connection closed")
	}
}

func TestStartAddressInUse(t *testing.T) {
	first := start(t, http.NotFoundHandler())
	defer func() { _ = first.Stop(t.Context()) }()

	second := NewHTTP("second", first.Addr().String(), http.NotFoundHandler(), DefaultLimits(), logging.Discard(), func(error) {})
	err := second.Start(t.Context())
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Errorf("Start on a used address = %v, want a listen error", err)
	}
	if second.Addr() != nil {
		t.Error("Addr() is set although Start failed")
	}
}

func startWithLimits(t *testing.T, limits Limits) *HTTP {
	t.Helper()
	l := NewHTTP("test", "127.0.0.1:0", http.NotFoundHandler(), limits, logging.Discard(), func(err error) {
		t.Errorf("unexpected fatal error: %v", err)
	})
	if err := l.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = l.Stop(context.Background()) })
	return l
}

// A client that doesn't finish its headers in time is disconnected.
func TestReadHeaderTimeout(t *testing.T) {
	limits := DefaultLimits()
	limits.ReadHeaderTimeout = 200 * time.Millisecond
	l := startWithLimits(t, limits)
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\n"); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("read: %v, want the server to close the connection", err)
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond || elapsed > 3*time.Second {
		t.Errorf("closed after %s, want about 200ms", elapsed)
	}
}

func TestMaxHeaderBytes(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxHeaderBytes = 1 << 10
	l := startWithLimits(t, limits)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+l.Addr().String()+"/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Big", strings.Repeat("x", 8<<10))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Errorf("status %d, want 431", resp.StatusCode)
	}
}
