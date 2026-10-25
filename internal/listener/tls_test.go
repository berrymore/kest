package listener

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/berrymore/kest/internal/logging"
	"github.com/berrymore/kest/internal/metrics"
	"github.com/berrymore/kest/internal/metrics/metricstest"
)

// writeCert writes a self-signed certificate for hosts and its key to dir,
// as name.pem and name.key.
func writeCert(t *testing.T, dir, name string, hosts ...string) (CertFiles, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: hosts[0]},
		DNSNames:              hosts,
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	files := CertFiles{Cert: filepath.Join(dir, name+".pem"), Key: filepath.Join(dir, name+".key")}
	writeFile(t, files.Cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	writeFile(t, files.Key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return files, cert
}

func writeFile(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// client trusts certs and connects to every host at addr.
func client(addr string, certs ...*x509.Certificate) *http.Client {
	pool := x509.NewCertPool()
	for _, c := range certs {
		pool.AddCert(c)
	}
	return &http.Client{Transport: &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}}
}

// servedCert returns the certificate the listener at addr serves for host.
func servedCert(t *testing.T, addr, host string) *x509.Certificate {
	t.Helper()
	d := tls.Dialer{Config: &tls.Config{ServerName: host, InsecureSkipVerify: true}}
	conn, err := d.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.(*tls.Conn).ConnectionState().PeerCertificates[0]
}

func TestTLS(t *testing.T) {
	dir := t.TempDir()
	aFiles, aCert := writeCert(t, dir, "a", "a.test")
	bFiles, bCert := writeCert(t, dir, "b", "b.test", "*.b.test")
	certs, err := LoadCertificates([]CertFiles{aFiles, bFiles}, tls.VersionTLS12)
	if err != nil {
		t.Fatal(err)
	}
	reg := metrics.NewRegistry()
	l := NewHTTP("secure", "127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Proto+" "+r.TLS.ServerName)
	}), Options{Limits: DefaultLimits(), TLS: certs, Metrics: reg}, logging.Discard(), func(err error) {
		t.Errorf("unexpected fatal error: %v", err)
	})
	if err := l.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := l.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	addr := l.Addr().String()

	// A failed handshake is counted, and logged before the connection closes.
	var d net.Dialer
	conn, err := d.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte("\x00\x00\x00\x00\x00\x00"))
	_, _ = io.Copy(io.Discard, conn)
	conn.Close()

	c := client(addr, aCert, bCert)
	for host, want := range map[string]string{"a.test": "HTTP/2.0 a.test", "x.b.test": "HTTP/2.0 x.b.test"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+host+"/", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("GET https://%s/: %v", host, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != want {
			t.Errorf("GET https://%s/ = %q, want %q", host, body, want)
		}
	}

	// SNI picks the certificate; an unknown name gets the first one.
	for host, want := range map[string]*x509.Certificate{"b.test": bCert, "a.test": aCert, "other.test": aCert} {
		if got := servedCert(t, addr, host); !got.Equal(want) {
			t.Errorf("%s: served the certificate of %s", host, got.Subject.CommonName)
		}
	}

	if got := metricstest.Value(t, reg, "kest_tls_handshake_errors_total", "listener", "secure"); got != 1 {
		t.Errorf("handshake errors = %v, want 1", got)
	}
	got := metricstest.Value(t, reg, "kest_tls_certificate_expiry_timestamp_seconds",
		"listener", "secure", "cert_file", bFiles.Cert)
	if want := float64(bCert.NotAfter.Unix()); got != want {
		t.Errorf("expiry = %v, want %v", got, want)
	}
}

func TestLoadCertificatesErrors(t *testing.T) {
	dir := t.TempDir()
	good, _ := writeCert(t, dir, "good", "good.test")
	other, _ := writeCert(t, dir, "other", "other.test")
	garbage := filepath.Join(dir, "garbage.pem")
	writeFile(t, garbage, []byte("not a certificate"))

	_, err := LoadCertificates([]CertFiles{
		good,
		{Cert: filepath.Join(dir, "missing.pem"), Key: good.Key},
		{Cert: garbage, Key: good.Key},
		{Cert: good.Cert, Key: other.Key},
	}, tls.VersionTLS12)
	var got []string
	for _, e := range unjoinErrs(err) {
		ce, ok := errors.AsType[*CertError](e)
		if !ok {
			t.Fatalf("not a *CertError: %v", e)
		}
		got = append(got, ce.Error())
	}
	want := []string{
		"certificate 1: open " + filepath.Join(dir, "missing.pem") + ": no such file or directory",
		"certificate 2: " + garbage + " and " + good.Key + ": tls: failed to find any PEM data in certificate input",
		"certificate 3: " + good.Cert + " and " + other.Key + ": tls: private key does not match public key",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func unjoinErrs(err error) []error {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		return j.Unwrap()
	}
	return []error{err}
}

func TestCertificateReload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		files, oldCert := writeCert(t, dir, "site", "site.test")
		certs, err := LoadCertificates([]CertFiles{files}, tls.VersionTLS12)
		if err != nil {
			t.Fatal(err)
		}
		served := func() *x509.Certificate { return certs.current[0].Load().Leaf }
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			certs.watch(ctx, time.Second, logging.Discard())
		}()
		check := func(want *x509.Certificate, when string) {
			t.Helper()
			time.Sleep(time.Second)
			synctest.Wait()
			if !served().Equal(want) {
				t.Errorf("%s: serving serial %v, want %v", when, served().SerialNumber, want.SerialNumber)
			}
		}

		// Renewed: the new pair is loaded once it holds still for a check.
		next := t.TempDir()
		newFiles, newCert := writeCert(t, next, "site", "site.test")
		copyFile(t, newFiles.Cert, files.Cert)
		check(oldCert, "a change seen once")
		copyFile(t, newFiles.Key, files.Key)
		check(oldCert, "the key changed too")
		check(newCert, "both held still")

		// A broken pair keeps the certificate served, and isn't retried.
		writeFile(t, files.Key, []byte("broken"))
		check(newCert, "broken, seen once")
		check(newCert, "broken, held still")
		check(newCert, "broken, still")

		cancel()
		<-done
	})
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, to, data)
}
