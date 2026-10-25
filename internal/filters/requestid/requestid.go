// Package requestid implements the request_id HTTP filter: every request
// gets an ID, in a header that the cluster sees and that goes back to the
// client, and in its httpx.RequestInfo for access_log.
package requestid

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/httpx"
	"github.com/berrymore/kest/internal/registry"
)

// Type is the filter type name in configuration.
const Type = "request_id"

func init() {
	httpx.Filters.Register(registry.Factory[httpx.Filter]{
		Type: Type,
		NewConfig: func() any {
			c := DefaultConfig()
			return &c
		},
		Build: func(_ registry.BuildContext, cfg any) (httpx.Filter, error) {
			c, ok := cfg.(*Config)
			if !ok {
				return nil, errors.New("request_id: unexpected config type")
			}
			return New(*c), nil
		},
	})
}

// Config configures the filter.
type Config struct {
	// Header carries the ID to the cluster and back to the client.
	Header string `yaml:"header"`
	// TrustIncoming keeps an ID the client sent, if it is valid (1–128
	// visible ASCII characters). Off by default: the client can't choose it.
	TrustIncoming bool `yaml:"trust_incoming"`
}

// DefaultConfig returns the defaults.
func DefaultConfig() Config {
	return Config{Header: "X-Request-Id"}
}

// Validate reports an invalid header name.
func (c *Config) Validate() error {
	if !httpx.IsToken(c.Header) {
		return &config.FieldError{Field: "header", Msg: fmt.Sprintf("must be a valid HTTP header name, got %q", c.Header)}
	}
	return nil
}

// maxIncoming bounds an ID taken from the client.
const maxIncoming = 128

// New returns the filter for a validated config.
func New(cfg Config) httpx.Filter {
	header := http.CanonicalHeaderKey(cfg.Header)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := ""
			if cfg.TrustIncoming {
				id = r.Header.Get(header)
				if !validIncoming(id) {
					id = ""
				}
			}
			if id == "" {
				id = newID()
			}
			// Handlers must not change the request they get: change a copy.
			out := *r
			out.Header = r.Header.Clone()
			out.Header.Set(header, id)
			w.Header().Set(header, id)
			httpx.Info(r).RequestID = id
			next.ServeHTTP(w, &out)
		})
	}
}

// newID returns 128 random bits as 32 hex digits.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // never fails (crypto/rand)
	return hex.EncodeToString(b[:])
}

func validIncoming(id string) bool {
	if id == "" || len(id) > maxIncoming {
		return false
	}
	for i := range len(id) {
		if id[i] < 0x21 || id[i] > 0x7e {
			return false
		}
	}
	return true
}
