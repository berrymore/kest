//go:build e2e && integration

package e2e

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// phpHello returns the absolute directory of examples/php-hello, skipping the
// test without PHP.
func phpHello(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../../examples/php-hello")
	if err != nil {
		t.Fatal(err)
	}
	requirePHP(t, dir)
	return dir
}

// helloCluster is a php_workers cluster named hello that runs the php-hello
// worker in dir, for configs written by tests.
func helloCluster(dir string) string {
	return `clusters:
  - name: hello
    type: php_workers
    config:
      command: [php, worker.php]
      dir: "` + dir + `"
      pool: {workers: 2}
`
}

// info is the JSON that php-hello's /info answers.
type info struct {
	Protocol string `json:"protocol"`
	URI      string `json:"uri"`
	Client   string `json:"client"`
}

func decodeInfo(t *testing.T, r response) info {
	t.Helper()
	var i info
	if r.Status != http.StatusOK || json.Unmarshal([]byte(r.Body), &i) != nil {
		t.Fatalf("/info = %d %q", r.Status, r.Body)
	}
	return i
}

// HTTP/2 reaches PHP over TLS (h2) and over plain TCP (h2c).
func TestPHPOverHTTP2(t *testing.T) {
	hello := phpHello(t)
	dir := t.TempDir()
	cert := writeCert(t, dir, "site", "site.test")
	tlsAddr, h2cAddr, adminAddr := freeAddr(t), freeAddr(t), freeAddr(t)
	configPath := filepath.Join(dir, "kest.yaml")
	writeFile(t, configPath, `version: v1
admin: {address: "`+adminAddr+`"}
listeners:
  - name: secure
    address: "`+tlsAddr+`"
    tls:
      certificates: [{cert_file: site.pem, key_file: site.key}]
    http:
      virtual_hosts: &vh
        - name: default
          domains: ["*"]
          routes: [{match: {prefix: /}, route: {cluster: hello}}]
  - name: h2c
    address: "`+h2cAddr+`"
    http:
      protocols: [http1, http2]
      virtual_hosts: *vh
`+helloCluster(hello))

	k := serve(t, configPath, adminAddr)
	defer k.stop(t)

	resp := do(t, tlsClient(tlsAddr, cert), "https://site.test/info")
	if got := decodeInfo(t, resp); resp.Proto != "HTTP/2.0" || got.Protocol != "2" || got.URI != "https://site.test/info" {
		t.Errorf("over TLS: %s, PHP saw %+v; want HTTP/2.0, protocol 2 and an https URI", resp.Proto, got)
	}

	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	h2c := &http.Client{Transport: &http.Transport{Protocols: &p}}
	resp = do(t, h2c, "http://"+h2cAddr+"/info")
	if got := decodeInfo(t, resp); resp.Proto != "HTTP/2.0" || got.Protocol != "2" {
		t.Errorf("h2c: %s, PHP saw %+v; want HTTP/2.0 and protocol 2", resp.Proto, got)
	}
	resp = do(t, http.DefaultClient, "http://"+h2cAddr+"/info")
	if got := decodeInfo(t, resp); resp.Proto != "HTTP/1.1" || got.Protocol != "1.1" {
		t.Errorf("HTTP/1.1 on the h2c listener: %s, PHP saw %+v", resp.Proto, got)
	}
}

// Behind a trusted proxy, PHP and the access log see the client that the
// proxy reported; a spoofed X-Forwarded-For from the client is ignored.
func TestPHPClientIP(t *testing.T) {
	hello := phpHello(t)
	listenAddr, adminAddr := freeAddr(t), freeAddr(t)
	configPath := filepath.Join(t.TempDir(), "kest.yaml")
	writeFile(t, configPath, `version: v1
admin: {address: "`+adminAddr+`"}
listeners:
  - name: main
    address: "`+listenAddr+`"
    http:
      filters:
        - type: access_log
        - type: client_ip
          config: {trusted_proxies: [127.0.0.1]}
      virtual_hosts:
        - name: default
          domains: ["*"]
          routes: [{match: {prefix: /}, route: {cluster: hello}}]
`+helloCluster(hello))
	k := serve(t, configPath, adminAddr)
	defer k.stop(t)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+listenAddr+"/info", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Forwarded-For", "1.1.1.1, 198.51.100.1") // the client sent 1.1.1.1; our proxy appended the rest
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got info
	err = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if err != nil || got.Client != "198.51.100.1" || !strings.HasPrefix(got.URI, "https://") {
		t.Errorf("PHP saw %+v (%v); want client 198.51.100.1 and an https URI", got, err)
	}
	if logs := k.logs.String(); !strings.Contains(logs, "remote=198.51.100.1") {
		t.Errorf("the access log doesn't show the client:\n%s", logs)
	}
}
