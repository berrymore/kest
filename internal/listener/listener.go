// Package listener runs HTTP servers on network addresses.
package listener

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// Server limits, until they become configurable (K-065).
const (
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 120 * time.Second
	maxHeaderBytes    = 1 << 20
)

// HTTP serves a handler on one address. It binds in Start and drains
// in-flight requests in Stop.
type HTTP struct {
	name    string
	address string
	server  *http.Server
	logger  *slog.Logger
	onFatal func(error)

	mu sync.Mutex
	ln net.Listener
	wg sync.WaitGroup
}

// NewHTTP creates a listener. onFatal is called if serving fails after
// Start, for example when the listening socket breaks.
func NewHTTP(name, address string, handler http.Handler, logger *slog.Logger, onFatal func(error)) *HTTP {
	return &HTTP{
		name:    name,
		address: address,
		logger:  logger,
		onFatal: onFatal,
		server: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: readHeaderTimeout,
			IdleTimeout:       idleTimeout,
			MaxHeaderBytes:    maxHeaderBytes,
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		},
	}
}

// Name returns the listener name.
func (l *HTTP) Name() string {
	return l.name
}

// Addr returns the bound address, or nil before Start.
func (l *HTTP) Addr() net.Addr {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ln == nil {
		return nil
	}
	return l.ln.Addr()
}

// Start binds the address and starts serving in the background.
func (l *HTTP) Start(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", l.address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	l.mu.Lock()
	l.ln = ln
	l.mu.Unlock()

	l.wg.Go(func() {
		if err := l.server.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			l.onFatal(fmt.Errorf("listener %q: serve: %w", l.name, err))
		}
	})
	l.logger.Info("listening", "address", ln.Addr().String())
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
	l.wg.Wait()
	return err
}
