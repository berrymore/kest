// Package listener runs HTTP servers on TCP addresses and unix sockets.
package listener

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/berrymore/kest/internal/metrics"
)

// Limits are a listener's server timeouts and header size; a zero timeout
// is off. See net/http.Server for their exact meaning.
type Limits struct {
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
}

// DefaultLimits are the limits of the admin server and of listeners whose
// config leaves them out.
func DefaultLimits() Limits {
	return Limits{ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20}
}

// Options configure a listener.
type Options struct {
	Limits
	// TLS makes the listener serve HTTPS with these certificates; nil serves
	// plain HTTP.
	TLS *Certificates
	// HTTP1 and HTTP2 choose the protocol versions. HTTP/2 without TLS is h2c
	// with prior knowledge. Both false: net/http's defaults (HTTP/1, and
	// HTTP/2 with TLS).
	HTTP1, HTTP2 bool
	// SocketMode is the file mode of a unix socket address.
	SocketMode fs.FileMode
	// Metrics gets the listener's metrics; nil: a registry nothing reads.
	Metrics *metrics.Registry
}

// HTTP serves a handler on one address. It binds in Start and drains
// in-flight requests in Stop.
type HTTP struct {
	name       string
	address    string // host:port, or unix:<absolute path>
	socketMode fs.FileMode
	server     *http.Server
	logger     *slog.Logger
	onFatal    func(error)

	certs        *Certificates // nil without TLS
	certInterval time.Duration // how often certs are checked; replaced in tests

	mu        sync.Mutex
	ln        net.Listener
	socket    *UnixSocket        // the socket file of a unix address, once bound
	stopWatch context.CancelFunc // stops the certificate checks; nil without TLS
	wg        sync.WaitGroup
}

// NewHTTP creates a listener on address: host:port, or unix:<absolute path>.
// onFatal is called if serving fails after Start, for example when the
// listening socket breaks.
func NewHTTP(name, address string, handler http.Handler, opts Options, logger *slog.Logger, onFatal func(error)) *HTTP {
	reg := opts.Metrics
	if reg == nil {
		reg = metrics.NewRegistry()
	}
	if _, unix := unixPath(address); unix {
		handler = noPeerAddress(handler)
	}
	l := &HTTP{
		name:         name,
		address:      address,
		socketMode:   opts.SocketMode,
		logger:       logger,
		onFatal:      onFatal,
		certs:        opts.TLS,
		certInterval: CertCheckInterval,
		server: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: opts.ReadHeaderTimeout,
			ReadTimeout:       opts.ReadTimeout,
			WriteTimeout:      opts.WriteTimeout,
			IdleTimeout:       opts.IdleTimeout,
			MaxHeaderBytes:    opts.MaxHeaderBytes,
		},
	}
	if opts.HTTP1 || opts.HTTP2 {
		var p http.Protocols
		p.SetHTTP1(opts.HTTP1)
		if l.certs != nil {
			p.SetHTTP2(opts.HTTP2)
		} else {
			p.SetUnencryptedHTTP2(opts.HTTP2)
		}
		l.server.Protocols = &p
	}
	handshakeErrors := &metrics.Counter{} // nothing reads it without TLS
	if l.certs != nil {
		l.server.TLSConfig = l.certs.config()
		l.certs.register(reg, name)
		handshakeErrors = reg.NewCounterVec("kest_tls_handshake_errors_total",
			"TLS handshakes that failed, by listener.", "listener").With(name)
	}
	l.server.ErrorLog = log.New(&serverLog{logger: logger, handshakeErrors: handshakeErrors}, "", 0)
	return l
}

// serverLog takes net/http's messages for a listener's server: warnings,
// except TLS handshake errors. Clients cause those all the time (scanners,
// outdated trust stores), so they are counted and logged at debug level.
type serverLog struct {
	logger          *slog.Logger
	handshakeErrors *metrics.Counter
}

func (s *serverLog) Write(p []byte) (int, error) {
	msg := strings.TrimSuffix(string(p), "\n")
	if strings.HasPrefix(msg, "http: TLS handshake error") {
		s.handshakeErrors.Inc()
		s.logger.Debug(msg)
	} else {
		s.logger.Warn(msg)
	}
	return len(p), nil
}

// Name returns the listener name.
func (l *HTTP) Name() string {
	return l.name
}

// Addr returns the bound address, or nil before Start.
func (l *HTTP) Addr() net.Addr {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.ln == nil:
		return nil
	case l.socket != nil:
		return &net.UnixAddr{Name: l.socket.path, Net: "unix"} // the listener has the temporary name
	}
	return l.ln.Addr()
}

// Start binds the address and starts serving in the background.
func (l *HTTP) Start(ctx context.Context) error {
	var (
		ln     net.Listener
		socket *UnixSocket
		err    error
	)
	if path, unix := unixPath(l.address); unix {
		ln, socket, err = ListenUnix(ctx, path, l.socketMode)
	} else {
		var lc net.ListenConfig
		ln, err = lc.Listen(ctx, "tcp", l.address)
	}
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	l.mu.Lock()
	l.ln = ln
	l.socket = socket
	l.mu.Unlock()

	serve := l.server.Serve
	if l.certs != nil {
		serve = func(ln net.Listener) error { return l.server.ServeTLS(ln, "", "") }
		wctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		l.stopWatch = cancel
		l.wg.Go(func() { l.certs.watch(wctx, l.certInterval, l.logger) })
	}
	l.wg.Go(func() {
		if err := serve(ln); !errors.Is(err, http.ErrServerClosed) {
			l.onFatal(fmt.Errorf("listener %q: serve: %w", l.name, err))
		}
	})
	address := ln.Addr().String()
	if socket != nil {
		address = "unix:" + socket.path
	}
	l.logger.Info("listening", "address", address, "tls", l.certs != nil)
	return nil
}

// Stop stops accepting connections and waits for in-flight requests until
// the ctx deadline, then closes the remaining connections.
func (l *HTTP) Stop(ctx context.Context) error {
	err := l.server.Shutdown(ctx)
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		l.logger.Warn("drain timeout reached; closing remaining connections")
		err = l.server.Close()
	}
	if l.stopWatch != nil {
		l.stopWatch()
	}
	l.wg.Wait()
	l.mu.Lock()
	socket := l.socket
	l.mu.Unlock()
	if socket != nil {
		if rerr := socket.Remove(); rerr != nil {
			l.logger.Warn("can't remove the socket file", "path", socket.path, "err", rerr)
		}
	}
	return err
}

// unixPath returns the socket path of a unix:<path> address.
func unixPath(address string) (string, bool) {
	return strings.CutPrefix(address, "unix:")
}

// noPeerAddress clears the remote address of requests on a unix socket: the
// peer has none, and Go reports "@" on Linux and "" on macOS. Only what a
// trusted proxy says (client_ip) gives the client's IP there.
func noPeerAddress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r2 := new(http.Request)
		*r2 = *r
		r2.RemoteAddr = ""
		next.ServeHTTP(w, r2)
	})
}
