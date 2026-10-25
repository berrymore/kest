//go:build e2e

package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeCert writes a self-signed certificate for host and its key to dir as
// name.pem and name.key, and returns the certificate.
func writeCert(t *testing.T, dir, name, host string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: host},
		DNSNames:              []string{host},
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
	writeFile(t, filepath.Join(dir, name+".pem"), string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
	writeFile(t, filepath.Join(dir, name+".key"), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})))
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// tlsClient trusts cert and sends every request to addr, whatever the URL's
// host, with HTTP/2 when the server offers it.
func tlsClient(addr string, cert *x509.Certificate) *http.Client {
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &http.Client{Transport: &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}}
}

// response is what do returns: the parts of an *http.Response that tests check.
type response struct {
	Status int
	Proto  string
	TLS    bool
	Header http.Header
	Body   string
}

func do(t *testing.T, c *http.Client, url string) response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: read body: %v", url, err)
	}
	return response{Status: resp.StatusCode, Proto: resp.Proto, TLS: resp.TLS != nil, Header: resp.Header, Body: string(body)}
}

func TestTLSListener(t *testing.T) {
	dir := t.TempDir()
	cert := writeCert(t, dir, "site", "site.test")
	listenAddr, adminAddr := freeAddr(t), freeAddr(t)
	configPath := filepath.Join(dir, "kest.yaml")
	writeFile(t, configPath, `version: v1
admin: {address: "`+adminAddr+`"}
listeners:
  - name: secure
    address: "`+listenAddr+`"
    tls:
      certificates:
        - {cert_file: site.pem, key_file: site.key}
    http:
      virtual_hosts:
        - name: default
          domains: ["*"]
          routes:
            - match: {prefix: /}
              direct_response: {body: "secure\n"}
`)

	k := serve(t, configPath, adminAddr)
	resp := do(t, tlsClient(listenAddr, cert), "https://site.test/")
	if resp.Status != http.StatusOK || resp.Body != "secure\n" || !resp.TLS || resp.Proto != "HTTP/2.0" {
		t.Errorf("GET https://site.test/ = %d %q (%s, TLS %v); want HTTP/2 over TLS", resp.Status, resp.Body, resp.Proto, resp.TLS)
	}
	_, metrics, err := get(t, "http://"+adminAddr+"/metrics")
	if err != nil || !strings.Contains(metrics, `kest_tls_certificate_expiry_timestamp_seconds{listener="secure",cert_file="`+
		filepath.Join(dir, "site.pem")+`"}`) {
		t.Errorf("no certificate expiry in /metrics (%v)", err)
	}
	k.stop(t)

	// kest validate reads the files too.
	if err := os.Remove(filepath.Join(dir, "site.key")); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := kest(t, "validate", "-c", configPath)
	if want := "listeners[0].tls.certificates[0]: open " + filepath.Join(dir, "site.key"); code == 0 ||
		!strings.Contains(stderr, want) {
		t.Errorf("validate = %d %q, want a failure with %q", code, stderr, want)
	}
}

// rawGet sends path as is, without a client cleaning it, and returns the
// status line and the body.
func rawGet(t *testing.T, addr, path string) string {
	t.Helper()
	var d net.Dialer
	conn, err := d.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "GET "+path+" HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	status, rest, _ := strings.Cut(string(out), "\r\n")
	_, body, _ := strings.Cut(rest, "\r\n\r\n")
	return status + " " + body
}

// A deny route can't be bypassed with dot segments or repeated slashes.
func TestPathNormalization(t *testing.T) {
	listenAddr, adminAddr := freeAddr(t), freeAddr(t)
	configPath := filepath.Join(t.TempDir(), "kest.yaml")
	writeFile(t, configPath, `version: v1
admin: {address: "`+adminAddr+`"}
listeners:
  - name: main
    address: "`+listenAddr+`"
    http:
      virtual_hosts:
        - name: default
          domains: ["*"]
          routes:
            - match: {prefix: /admin/}
              direct_response: {status: 403, body: "denied"}
            - match: {prefix: /}
              direct_response: {body: "public"}
`)
	k := serve(t, configPath, adminAddr)
	defer k.stop(t)
	for _, path := range []string{"/admin/x", "/public/../admin/x", "/public/%2e%2e/admin/x", "//admin/x", "/./admin/x"} {
		if got := rawGet(t, listenAddr, path); got != "HTTP/1.1 403 Forbidden denied" {
			t.Errorf("GET %s = %q, want 403", path, got)
		}
	}
	if got := rawGet(t, listenAddr, "/admin/../public"); got != "HTTP/1.1 200 OK public" {
		t.Errorf("GET /admin/../public = %q, want 200", got)
	}
}
