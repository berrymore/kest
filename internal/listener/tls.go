package listener

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/berrymore/kest/internal/metrics"
)

// CertFiles names a certificate chain and its private key, in PEM files.
type CertFiles struct {
	Cert string // the leaf first, then the intermediates
	Key  string
}

// CertError reports a certificate pair that can't be loaded. Index is its
// position in the list given to LoadCertificates.
type CertError struct {
	Index int
	Err   error
}

func (e *CertError) Error() string {
	return fmt.Sprintf("certificate %d: %v", e.Index, e.Err)
}

func (e *CertError) Unwrap() error {
	return e.Err
}

// CertCheckInterval is how often a TLS listener checks its certificate files
// for changes.
const CertCheckInterval = 10 * time.Second

// Certificates are the certificates of a TLS listener. They are loaded when
// created; while the listener runs, a pair whose files change is loaded again
// once the change holds still for one check, so a renewed certificate is
// served without a restart. A pair that fails to load keeps the old
// certificate.
type Certificates struct {
	files      []CertFiles
	minVersion uint16
	current    []atomic.Pointer[tls.Certificate] // by pair; read by handshakes
	loaded     []fileState                       // by pair, the files as last loaded; owned by watch
}

// fileState is what a check compares: a change of size or modification time
// counts, as with reload.
type fileState struct {
	cert, key fileInfo
}

type fileInfo struct {
	size, mtime int64
	missing     bool
}

func statFile(name string) fileInfo {
	fi, err := os.Stat(name)
	if err != nil {
		return fileInfo{missing: true}
	}
	return fileInfo{size: fi.Size(), mtime: fi.ModTime().UnixNano()}
}

func (f CertFiles) state() fileState {
	return fileState{cert: statFile(f.Cert), key: statFile(f.Key)}
}

// LoadCertificates loads every pair. minVersion is tls.VersionTLS12 or
// tls.VersionTLS13. Each pair that fails is reported as a *CertError,
// joined with errors.Join.
func LoadCertificates(files []CertFiles, minVersion uint16) (*Certificates, error) {
	c := &Certificates{
		files:      files,
		minVersion: minVersion,
		current:    make([]atomic.Pointer[tls.Certificate], len(files)),
		loaded:     make([]fileState, len(files)),
	}
	var errs []error
	for i, f := range files {
		c.loaded[i] = f.state()
		cert, err := loadPair(f)
		if err != nil {
			errs = append(errs, &CertError{Index: i, Err: err})
			continue
		}
		c.current[i].Store(cert)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return c, nil
}

func loadPair(f CertFiles) (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(f.Cert, f.Key)
	if _, ok := errors.AsType[*fs.PathError](err); ok {
		return nil, err // names the file already
	}
	if err != nil {
		return nil, fmt.Errorf("%s and %s: %w", f.Cert, f.Key, err)
	}
	return &cert, nil
}

// config returns the TLS configuration of a listener that serves these
// certificates.
func (c *Certificates) config() *tls.Config {
	return &tls.Config{
		MinVersion:     c.minVersion,
		GetCertificate: c.certificate,
	}
}

// certificate picks the first certificate the client supports, which is the
// one for the server name it asked for, or else the first one; as crypto/tls
// does with a fixed list.
func (c *Certificates) certificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	first := c.current[0].Load()
	if len(c.current) == 1 {
		return first, nil
	}
	for i := range c.current {
		if cert := c.current[i].Load(); hello.SupportsCertificate(cert) == nil {
			return cert, nil
		}
	}
	return first, nil
}

// watch checks the files every interval until ctx ends and reloads the pairs
// that changed.
func (c *Certificates) watch(ctx context.Context, interval time.Duration, logger *slog.Logger) {
	seen := make([]fileState, len(c.files)) // by pair, the files at the previous check
	copy(seen, c.loaded)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		for i, f := range c.files {
			now := f.state()
			switch {
			case now == c.loaded[i]:
			case now != seen[i]:
				// Changing, maybe one file of the two so far: wait until it holds still.
			default:
				// A failed load isn't retried until the files change again.
				c.loaded[i] = now
				cert, err := loadPair(f)
				if err != nil {
					logger.Error("certificate not reloaded; serving the old one", "cert_file", f.Cert, "err", err)
					break
				}
				c.current[i].Store(cert)
				logger.Info("certificate reloaded", "cert_file", f.Cert, "not_after", cert.Leaf.NotAfter.UTC())
			}
			seen[i] = now
		}
	}
}

// register adds the expiry of each certificate to reg.
func (c *Certificates) register(reg *metrics.Registry, listener string) {
	reg.NewCollector("kest_tls_certificate_expiry_timestamp_seconds",
		"When each certificate a TLS listener serves expires, as a Unix timestamp.", metrics.TypeGauge,
		[]string{"listener", "cert_file"}, func(emit func(float64, ...string)) {
			for i, f := range c.files {
				if cert := c.current[i].Load(); cert != nil && cert.Leaf != nil {
					emit(float64(cert.Leaf.NotAfter.Unix()), listener, f.Cert)
				}
			}
		})
}
