package server_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/berrymore/kest/internal/cluster"
	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/logging"
	"github.com/berrymore/kest/internal/plugin"
	"github.com/berrymore/kest/internal/registry"
	"github.com/berrymore/kest/internal/relay"
	"github.com/berrymore/kest/internal/server"
)

// events records lifecycle events of the components below, in order.
type events struct {
	mu   sync.Mutex
	list []string
}

func (e *events) add(format string, args ...any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, fmt.Sprintf(format, args...))
}

func (e *events) get() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.list)
}

var lifecycleEvents events

// echoPlugin answers Echo with its body, and records its lifecycle.
type echoPlugin struct {
	name string
	down atomic.Bool
}

var echoPlugins = map[string]*echoPlugin{}

// rpcCluster calls the plugin "echo" through RPC when it starts and when it
// stops, as a worker that boots and finishes its last request would.
type rpcCluster struct {
	http.Handler
	name, address string
}

func init() {
	plugin.Types.Register(registry.Factory[plugin.Plugin]{
		Type: "test_echo",
		Build: func(bc registry.BuildContext, _ any) (plugin.Plugin, error) {
			p := &echoPlugin{name: bc.Name}
			echoPlugins[bc.Name] = p
			return p, nil
		},
	})
	cluster.Types.Register(registry.Factory[cluster.Cluster]{
		Type: "test_rpc",
		Build: func(bc registry.BuildContext, _ any) (cluster.Cluster, error) {
			return &rpcCluster{Handler: http.NotFoundHandler(), name: bc.Name, address: bc.RPCAddress}, nil
		},
	})
}

func (p *echoPlugin) Call(_ context.Context, c plugin.Call) ([]byte, error) {
	if c.Method != "Echo" {
		return nil, plugin.ErrUnknownMethod
	}
	return c.Body, nil
}

func (p *echoPlugin) Start(context.Context) error {
	lifecycleEvents.add("start plugin %s", p.name)
	return nil
}

func (p *echoPlugin) Stop(context.Context) error {
	lifecycleEvents.add("stop plugin %s", p.name)
	return nil
}

func (p *echoPlugin) Serving() bool { return !p.down.Load() }

func (c *rpcCluster) Start(ctx context.Context) error {
	lifecycleEvents.add("start cluster %s: %s", c.name, c.call(ctx))
	return nil
}

func (c *rpcCluster) Stop(ctx context.Context) error {
	lifecycleEvents.add("stop cluster %s: %s", c.name, c.call(ctx))
	return nil
}

// call calls echo.Echo through RPC and returns the answer, or the error.
func (c *rpcCluster) call(ctx context.Context) string {
	path, ok := strings.CutPrefix(c.address, "unix://")
	if !ok {
		return "no unix address: " + c.address
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return err.Error()
	}
	defer func() { _ = conn.Close() }()
	rc := relay.NewConn(conn, conn, 1<<10)
	const method = "echo.Echo"
	err = rc.Send(&relay.Frame{Flags: relay.CodecRaw, Options: []uint32{1, uint32(len(method))},
		Payload: []byte(method + "hi")})
	var f relay.Frame
	if err == nil {
		err = rc.Receive(&f)
	}
	if err != nil {
		return err.Error()
	}
	return string(f.Payload[len(method):])
}

const pluginsConfig = `version: v1
admin: {address: "127.0.0.1:0"}
listeners:
  - name: main
    address: 127.0.0.1:0
    http: {virtual_hosts: [{name: a, domains: ["*"], routes: [{match: {prefix: /}, route: {cluster: app}}]}]}
clusters:
  - name: app
    type: test_rpc
plugins:
  - name: echo
    type: test_echo
rpc:
`

func newPluginsServer(t *testing.T, src string) *server.Server {
	t.Helper()
	cfg, err := config.Parse([]byte(src), "kest.yaml", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s, err := server.New(cfg, logging.Discard())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// Plugins and the RPC server start before the clusters and stop after them:
// workers call plugins while they boot and while they finish.
func TestPluginsOutliveClusters(t *testing.T) {
	lifecycleEvents = events{}
	s := newPluginsServer(t, pluginsConfig)
	cancel, done := run(t, s)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"start plugin echo", "start cluster app: hi", "stop cluster app: hi", "stop plugin echo"}
	if got := lifecycleEvents.get(); !slices.Equal(got, want) {
		t.Errorf("events:\n got %q\nwant %q", got, want)
	}
}

func TestReadyFollowsPlugins(t *testing.T) {
	s := newPluginsServer(t, pluginsConfig)
	cancel, done := run(t, s)
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	ready := "http://" + s.AdminAddr().String() + "/ready"
	echoPlugins["echo"].down.Store(true)
	if code, body := get(t, ready); code != http.StatusServiceUnavailable || strings.TrimSpace(body) != "not serving: plugin echo" {
		t.Errorf("/ready = %d %q, want 503 naming the plugin", code, body)
	}
	echoPlugins["echo"].down.Store(false)
	if code, _ := get(t, ready); code != http.StatusOK {
		t.Errorf("/ready = %d after the plugin is back", code)
	}
}

// Without rpc, plugins still run, and clusters get no address.
func TestPluginsWithoutRPC(t *testing.T) {
	lifecycleEvents = events{}
	s := newPluginsServer(t, strings.TrimSuffix(pluginsConfig, "rpc:\n"))
	cancel, done := run(t, s)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"start plugin echo", "start cluster app: no unix address: ", "stop cluster app: no unix address: ",
		"stop plugin echo"}
	if got := lifecycleEvents.get(); !slices.Equal(got, want) {
		t.Errorf("events:\n got %q\nwant %q", got, want)
	}
}

func TestNewReportsPluginErrors(t *testing.T) {
	src := strings.Replace(pluginsConfig, "type: test_echo", "type: nope", 1)
	cfg, err := config.Parse([]byte(src), "kest.yaml", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, err = server.New(cfg, logging.Discard())
	var errs config.Errors
	if !errors.As(err, &errs) || !strings.Contains(err.Error(), `kest.yaml:12:11: plugins[0].type: unknown plugin type "nope"`) {
		t.Errorf("New = %v, want the located unknown type", err)
	}
}
