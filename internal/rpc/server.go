// Package rpc serves the channel PHP workers call plugins through: goridge
// RPC, as RoadRunner's PHP libraries speak it (.ai/research/roadrunner.md
// § RPC). A call "kv.Set" goes to the plugin named kv, method Set; kest hands
// over the body and the codec, and never decodes bodies itself.
package rpc

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/berrymore/kest/internal/listener"
	"github.com/berrymore/kest/internal/metrics"
	"github.com/berrymore/kest/internal/plugin"
	"github.com/berrymore/kest/internal/relay"
)

// Config configures a Server; the config package documents the defaults.
type Config struct {
	// Address is "unix:<absolute path>" or "host:port". Empty: a socket in
	// a private temporary directory, made at start and removed at stop.
	Address        string
	SocketMode     fs.FileMode // of a unix socket at Address
	MaxConnections int
	MaxFrameSize   int // the payload of a call or an answer
	CallTimeout    time.Duration
}

// privateSocket is the name of the socket in a private directory, whose own
// mode keeps everyone but kest's user out.
const (
	privateSocket     = "rpc.sock"
	privateSocketMode = 0o600
	privateDirMode    = 0o700
)

// refusedLogEvery limits warnings about connections over max_connections to
// one per interval; the metric counts them all.
const refusedLogEvery = 10 * time.Second

// Server is the RPC server. It is built without side effects; Start binds.
type Server struct {
	cfg     Config
	plugins map[string]plugin.Plugin
	logger  *slog.Logger
	metrics *serverMetrics

	network, addr string      // what Start binds
	mode          fs.FileMode // of a unix socket
	privateDir    string      // the directory of a private socket, or ""
	rrRPC         string      // the address as PHP's goridge takes it

	ln     net.Listener
	socket *listener.UnixSocket // the socket file of a unix address
	// ctx is the run context of calls, made in Start: canceled when Stop runs
	// out of time, so that plugins return.
	ctx    context.Context //nolint:containedctx // the component's run context, see above
	cancel context.CancelFunc
	closed chan struct{} // closed when Stop begins
	done   sync.WaitGroup

	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	stopping bool
	warned   time.Time // the last warning about a refused connection
}

// New builds a server that routes calls to plugins by name. Nothing starts
// before Start.
func New(cfg Config, plugins map[string]plugin.Plugin, logger *slog.Logger, reg *metrics.Registry) (*Server, error) {
	if cfg.MaxConnections <= 0 || cfg.MaxFrameSize <= 0 || cfg.CallTimeout <= 0 {
		return nil, fmt.Errorf("invalid limits %+v", cfg)
	}
	s := &Server{
		cfg:     cfg,
		plugins: plugins,
		logger:  logger,
		metrics: newServerMetrics(reg, plugins),
		conns:   make(map[net.Conn]struct{}),
		closed:  make(chan struct{}),
	}
	path, unix := strings.CutPrefix(cfg.Address, "unix:")
	switch {
	case cfg.Address == "":
		s.privateDir = filepath.Join(os.TempDir(), fmt.Sprintf("kest-%d-%s", os.Getpid(), rand.Text()[:8]))
		s.network, s.addr, s.mode = "unix", filepath.Join(s.privateDir, privateSocket), privateSocketMode
	case unix:
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("socket path %q is not absolute", path)
		}
		s.network, s.addr, s.mode = "unix", path, cfg.SocketMode
	default:
		s.network, s.addr = "tcp", cfg.Address
	}
	if s.network == "unix" {
		if strings.Contains(s.addr, ":") {
			// The temporary directory can have one, too.
			return nil, fmt.Errorf("socket path %q has a ':', which PHP's goridge can't parse", s.addr)
		}
		s.rrRPC = "unix://" + s.addr
	} else {
		s.rrRPC = "tcp://" + s.addr
	}
	return s, nil
}

// Address returns the address PHP connects to, as RR_RPC carries it:
// "unix:///path/to/rpc.sock" or "tcp://host:port".
func (s *Server) Address() string {
	return s.rrRPC
}

// Start binds the address and serves connections in the background.
func (s *Server) Start(ctx context.Context) error {
	if s.privateDir != "" {
		if err := os.Mkdir(s.privateDir, privateDirMode); err != nil {
			return fmt.Errorf("make the private directory of the socket: %w", err)
		}
	}
	var err error
	if s.network == "unix" {
		s.ln, s.socket, err = listener.ListenUnix(ctx, s.addr, s.mode)
	} else {
		var lc net.ListenConfig
		s.ln, err = lc.Listen(ctx, "tcp", s.addr)
	}
	if err != nil {
		s.removePrivateDir()
		return err
	}
	s.ctx, s.cancel = context.WithCancel(context.WithoutCancel(ctx))
	s.done.Go(s.accept)
	s.logger.Info("rpc listening", "address", s.rrRPC)
	return nil
}

// Stop closes the listener and the idle connections at once; a connection
// in a call closes after its answer. When ctx ends first, the calls are
// canceled and every connection closed.
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.stopping = true
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	close(s.closed)
	err := s.ln.Close()
	if errors.Is(err, net.ErrClosed) {
		err = nil
	}
	// A connection waiting for its next call wakes up now; one in a call
	// reads after its answer, and ends then.
	now := time.Now()
	for _, c := range conns {
		_ = c.SetReadDeadline(now)
	}

	finished := make(chan struct{})
	go func() {
		s.done.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-ctx.Done():
		s.logger.Warn("rpc calls still running at the stop deadline; canceling them")
		s.cancel()
		s.mu.Lock()
		for c := range s.conns {
			_ = c.Close()
		}
		s.mu.Unlock()
		<-finished
	}
	s.cancel()

	if s.socket != nil {
		if rerr := s.socket.Remove(); rerr != nil {
			err = errors.Join(err, fmt.Errorf("remove the socket file: %w", rerr))
		}
	}
	s.removePrivateDir()
	return err
}

func (s *Server) removePrivateDir() {
	if s.privateDir == "" {
		return
	}
	if err := os.Remove(s.privateDir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.logger.Warn("can't remove the private directory of the rpc socket", "dir", s.privateDir, "err", err)
	}
}

// Accept errors other than a closed listener (too many open files) are
// retried after a pause that doubles from acceptRetryMin to acceptRetryMax.
const (
	acceptRetryMin = 5 * time.Millisecond
	acceptRetryMax = time.Second
)

func (s *Server) accept() {
	var delay time.Duration
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			delay = min(max(2*delay, acceptRetryMin), acceptRetryMax)
			s.logger.Warn("rpc accept failed", "err", err, "retry_in", delay.String())
			select {
			case <-time.After(delay):
				continue
			case <-s.closed:
				return
			}
		}
		delay = 0
		if !s.track(c) {
			_ = c.Close()
			continue
		}
		s.done.Go(func() { s.serve(c) })
	}
}

// track adds c to the open connections, unless the server is stopping or
// has max_connections already.
func (s *Server) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return false
	}
	if len(s.conns) >= s.cfg.MaxConnections {
		s.metrics.refused.Inc()
		if now := time.Now(); now.Sub(s.warned) >= refusedLogEvery {
			s.warned = now
			s.logger.Warn("rpc connection refused: rpc.max_connections are open", "max_connections",
				s.cfg.MaxConnections)
		}
		return false
	}
	s.conns[c] = struct{}{}
	s.metrics.connections.Inc()
	return true
}

func (s *Server) untrack(c net.Conn) {
	_ = c.Close()
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	s.metrics.connections.Dec()
}

// serve answers the calls of one connection, one at a time, until the client
// closes it, sends something that isn't a call, or the server stops.
func (s *Server) serve(c net.Conn) {
	defer s.untrack(c)
	conn := relay.NewConn(c, c, s.cfg.MaxFrameSize)
	for {
		var f relay.Frame
		if err := conn.Receive(&f); err != nil {
			s.closing(err)
			return
		}
		req, err := parseRequest(&f)
		if err != nil {
			s.logger.Warn("rpc connection closed: not a call", "err", err)
			return
		}
		if err := conn.Send(s.call(req)); err != nil {
			s.closing(err)
			return
		}
	}
}

// closing logs why a connection closes, unless it is the usual: the client
// closed it, or the server is stopping.
func (s *Server) closing(err error) {
	var perr *relay.ProtocolError
	switch {
	case errors.Is(err, relay.ErrFrameTooLarge):
		s.logger.Warn("rpc connection closed: a call over rpc.max_frame_size", "err", err)
	case errors.As(err, &perr):
		s.logger.Warn("rpc connection closed: not a goridge frame", "err", err)
	case errors.Is(err, io.EOF), errors.Is(err, net.ErrClosed), errors.Is(err, os.ErrDeadlineExceeded):
	default:
		s.logger.Debug("rpc connection closed", "err", err)
	}
}

// call routes req to its plugin and returns the answer frame.
func (s *Server) call(req *request) *relay.Frame {
	dot := strings.LastIndexByte(req.method, '.')
	if dot < 0 {
		s.metrics.of("").unknown.Inc()
		return failure(req, "rpc: service/method request ill-formed: "+req.method)
	}
	name, method := req.method[:dot], req.method[dot+1:]
	p, ok := s.plugins[name]
	if !ok {
		s.metrics.of("").unknown.Inc()
		return failure(req, "rpc: can't find service "+req.method)
	}

	m := s.metrics.of(name)
	ctx, cancel := context.WithTimeout(s.ctx, s.cfg.CallTimeout)
	start := time.Now()
	body, err := p.Call(ctx, plugin.Call{Method: method, Codec: req.codec, Body: req.body})
	m.duration.Observe(time.Since(start).Seconds())
	ctxErr := ctx.Err()
	cancel()

	switch {
	case err == nil && len(req.method)+len(body) > s.cfg.MaxFrameSize:
		m.failed.Inc()
		return failure(req, fmt.Sprintf("rpc: the answer of %s is %d bytes, over rpc.max_frame_size", req.method,
			len(body)))
	case err == nil:
		m.ok.Inc()
		return answer(req, body)
	case errors.Is(err, plugin.ErrUnknownMethod):
		m.unknown.Inc()
		return failure(req, "rpc: can't find method "+req.method)
	case errors.Is(ctxErr, context.DeadlineExceeded):
		m.timeout.Inc()
		return failure(req, fmt.Sprintf("rpc: %s timed out after %s (rpc.call_timeout)", req.method,
			s.cfg.CallTimeout))
	case ctxErr != nil:
		m.failed.Inc()
		return failure(req, "rpc: kest is stopping")
	default:
		m.failed.Inc()
		return failure(req, err.Error())
	}
}
