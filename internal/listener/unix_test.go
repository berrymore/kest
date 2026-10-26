package listener

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/berrymore/kest/internal/logging"
)

// socketDir returns a fresh directory with a path short enough for sockets.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "kest") //nolint:usetesting // t.TempDir's paths can pass the socket path limit on macOS
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func newUnix(path string, mode fs.FileMode, h http.Handler) *HTTP {
	return NewHTTP("test", "unix:"+path, h, Options{Limits: DefaultLimits(), SocketMode: mode}, logging.Discard(), func(error) {})
}

// getUnix sends a GET through the unix socket at path.
func getUnix(ctx context.Context, path string) (string, error) {
	client := &http.Client{Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://kest/", http.NoBody)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestUnixServeAndStop(t *testing.T) {
	dir := socketDir(t)
	path := filepath.Join(dir, "kest.sock")
	l := newUnix(path, 0o600, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "peer %q", r.RemoteAddr)
	}))
	if err := l.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	got, err := getUnix(t.Context(), path)
	if err != nil || got != `peer ""` {
		t.Errorf("GET = %q, %v; want an empty peer address", got, err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode() != fs.ModeSocket|0o600 {
		t.Errorf("socket file mode = %v, %v; want %v", info.Mode(), err, fs.ModeSocket|0o600)
	}
	if a := l.Addr(); a == nil || a.Network() != "unix" || a.String() != path {
		t.Errorf("Addr() = %v", a)
	}
	if got := names(t, dir); !slices.Equal(got, []string{"kest.sock"}) {
		t.Errorf("files next to the socket: %v, want only kest.sock", got)
	}

	if err := l.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("the socket file is still there after Stop: %v", err)
	}
}

// A socket file left by a killed process doesn't block the start.
func TestUnixStaleSocket(t *testing.T) {
	path := filepath.Join(socketDir(t), "kest.sock")
	var lc net.ListenConfig
	old, err := lc.Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	old.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = old.Close()

	l := newUnix(path, 0o660, http.NotFoundHandler())
	if err := l.Start(t.Context()); err != nil {
		t.Fatalf("Start over a stale socket: %v", err)
	}
	_ = l.Stop(t.Context())
}

func TestUnixStartRefused(t *testing.T) {
	dir := socketDir(t)
	live := filepath.Join(dir, "live.sock")
	var lc net.ListenConfig
	other, err := lc.Listen(t.Context(), "unix", live)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	long := filepath.Join(dir, strings.Repeat("x", 120)+".sock")

	for _, tt := range []struct {
		name, path, want string
	}{
		{"another process listens", live, "another process listens on " + live},
		{"not a socket", file, file + " exists and is not a socket"},
		{"path too long", long, "is too long"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l := newUnix(tt.path, 0o660, http.NotFoundHandler())
			err := l.Start(t.Context())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Start = %v, want an error with %q", err, tt.want)
			}
			if l.Addr() != nil {
				t.Error("Addr() is set although Start failed")
			}
		})
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "data" {
		t.Errorf("the file at the address changed: %q, %v", b, err)
	}
	if got := names(t, dir); !slices.Equal(got, []string{"file", "live.sock"}) {
		t.Errorf("files left: %v", got)
	}
}

// Stop removes the socket file only while it is still the listener's.
func TestUnixStopKeepsReplacedFile(t *testing.T) {
	path := filepath.Join(socketDir(t), "kest.sock")
	l := newUnix(path, 0o660, http.NotFoundHandler())
	if err := l.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("someone else's"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("Stop removed a file it didn't create: %v", err)
	}
}
