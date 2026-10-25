package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func parse(t *testing.T, src string, env map[string]string) (*Config, error) {
	t.Helper()
	lookup := func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
	return Parse([]byte(src), "kest.yaml", lookup)
}

const validConfig = `
version: v1
log:
  level: debug
admin:
  address: ${ADMIN_ADDR:-127.0.0.1:9901}
listeners:
  - name: main
    address: 127.0.0.1:${PORT}
    http:
      virtual_hosts:
        - name: default
          domains: ["Example.COM.", "*.example.com", "*"]
          routes:
            - match: { path: /ping }
              direct_response:
                status: ${STATUS}
                body: "pong\n"
            - match: { prefix: /api/ }
              route: { cluster: app, timeout: 5s }
            - match: { prefix: / }
              route: { cluster: app }
clusters:
  - name: app
    type: fake
    config:
      workers: 4
`

func TestParseValid(t *testing.T) {
	cfg, err := parse(t, validConfig, map[string]string{"PORT": "8080", "STATUS": "202"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if cfg.File() != "kest.yaml" {
		t.Errorf("File() = %q", cfg.File())
	}
	if cfg.Log.Level != "debug" || cfg.Log.Format != DefaultLogFormat {
		t.Errorf("log = %+v, want level debug and the default format", cfg.Log)
	}
	if cfg.Admin.Address != "127.0.0.1:9901" {
		t.Errorf("admin.address = %q, want the ${...:-default} value", cfg.Admin.Address)
	}
	if got := cfg.Server.DrainTimeout.Std(); got != DefaultDrainTimeout {
		t.Errorf("server.drain_timeout = %v, want default %v", got, DefaultDrainTimeout)
	}

	l := cfg.Listeners[0]
	if l.Address != "127.0.0.1:8080" {
		t.Errorf("listener address = %q", l.Address)
	}
	if h := l.HTTP; h.ReadHeaderTimeout.Std() != DefaultReadHeaderTimeout || h.ReadTimeout.Std() != 0 ||
		h.WriteTimeout.Std() != 0 || h.IdleTimeout.Std() != DefaultIdleTimeout || h.MaxHeaderBytes != DefaultMaxHeaderBytes {
		t.Errorf("server limits = %v %v %v %v %v, want the defaults", h.ReadHeaderTimeout, h.ReadTimeout, h.WriteTimeout,
			h.IdleTimeout, h.MaxHeaderBytes)
	}
	vh := l.HTTP.VirtualHosts[0]
	if want := []string{"example.com", "*.example.com", "*"}; strings.Join(vh.Domains, ",") != strings.Join(want, ",") {
		t.Errorf("domains = %q, want normalized %q", vh.Domains, want)
	}
	if dr := vh.Routes[0].DirectResponse; dr.Status != 202 || dr.Body != "pong\n" {
		t.Errorf("direct_response = %+v, want status 202 from ${STATUS} as an int", dr)
	}
	if got := vh.Routes[1].Route.Timeout.Std(); got != 5*time.Second {
		t.Errorf("explicit route timeout = %v", got)
	}
	if got := vh.Routes[2].Route.Timeout.Std(); got != DefaultRouteTimeout {
		t.Errorf("default route timeout = %v", got)
	}

	if l.TLS != nil {
		t.Errorf("tls = %+v, want none", l.TLS)
	}

	c := cfg.Clusters[0]
	if c.Name != "app" || c.Type != "fake" || !c.HasConfig() || c.Path() != "clusters[0]" {
		t.Errorf("cluster = name %q type %q hasConfig %v path %q", c.Name, c.Type, c.HasConfig(), c.Path())
	}
}

func TestParseTLS(t *testing.T) {
	src := `version: v1
listeners:
  - name: secure
    address: 127.0.0.1:8443
    tls:
      certificates: [{cert_file: a.pem, key_file: a.key}]
    http: &http
      virtual_hosts: [{name: a, domains: ["*"], routes: [{match: {prefix: /}, direct_response: {}}]}]
  - name: modern
    address: 127.0.0.1:8444
    tls:
      certificates: [{cert_file: a.pem, key_file: a.key}]
      min_version: 1.3
    http: *http
`
	cfg, err := parse(t, src, nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := cfg.Listeners[0].TLS; got.MinVersion != TLS12 || got.Certificates[0] != (Certificate{"a.pem", "a.key"}) {
		t.Errorf("tls = %+v, want a.pem and the default version", got)
	}
	if got := cfg.Listeners[1].TLS.MinVersion; got != TLS13 {
		t.Errorf("min_version = %q, want an unquoted 1.3 read as %q", got, TLS13)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		env  map[string]string
		want []string // each must appear in the error text, in order
	}{
		{
			name: "empty",
			src:  "# nothing here\n",
			want: []string{"kest.yaml: configuration is empty"},
		},
		{
			name: "not a mapping",
			src:  "- a\n- b\n",
			want: []string{"configuration must be a YAML mapping"},
		},
		{
			name: "syntax error",
			src:  "version: [v1\nlisteners: []\n",
			want: []string{"kest.yaml:2:"},
		},
		{
			name: "missing version and listeners",
			src:  "log: {level: info}\n",
			want: []string{
				"kest.yaml:1:1: listeners: at least one listener is required",
				"kest.yaml:1:1: version: required",
			},
		},
		{
			name: "unknown fields everywhere, reported together",
			src: `version: v1
bogus: 1
listeners:
  - name: main
    listen_on: 127.0.0.1:8080
`,
			want: []string{
				"kest.yaml:2:1: bogus: unknown field",
				"kest.yaml:5:5: listeners[0].listen_on: unknown field",
			},
		},
		{
			name: "bad values",
			src: `version: v2
log: {level: loud, format: xml}
admin: {address: localhost}
server: {drain_timeout: 10 parsecs}
listeners:
  - name: main
    address: 127.0.0.1:8080
    http:
      virtual_hosts:
        - name: a
          domains: ["*"]
          routes:
            - match: {prefix: /}
              direct_response: {status: 200}
`,
			want: []string{
				`kest.yaml:4:25: server.drain_timeout: invalid duration "10 parsecs"`,
			},
		},
		{
			name: "semantic errors",
			src: `version: v2
log: {level: loud, format: xml}
admin: {address: localhost}
listeners:
  - name: "bad name!"
    address: 127.0.0.1:99999
    http:
      virtual_hosts:
        - name: a
          domains: ["foo.*", "example.com:8080"]
          routes:
            - match: {prefix: api}
              route: {cluster: nope}
            - match: {prefix: /, path: /x}
              direct_response: {status: 42}
              route: {cluster: nope}
            - match: {}
`,
			want: []string{
				`kest.yaml:1:10: version: unsupported version "v2"`,
				`kest.yaml:2:14: log.level: unknown level "loud"`,
				`kest.yaml:2:28: log.format: unknown format "xml"`,
				`kest.yaml:3:18: admin.address: invalid address "localhost"`,
				`kest.yaml:5:11: listeners[0].name: invalid name "bad name!"`,
				`kest.yaml:6:14: listeners[0].address: invalid port "99999"`,
				`kest.yaml:10:21: listeners[0].http.virtual_hosts[0].domains[0]: invalid domain "foo.*"`,
				`kest.yaml:10:30: listeners[0].http.virtual_hosts[0].domains[1]: invalid domain "example.com:8080": domains must not include a port`,
				`kest.yaml:12:31: listeners[0].http.virtual_hosts[0].routes[0].match.prefix: must start with /`,
				`kest.yaml:13:32: listeners[0].http.virtual_hosts[0].routes[0].route.cluster: unknown cluster "nope"`,
				`kest.yaml:14:15: listeners[0].http.virtual_hosts[0].routes[1]: set only one action`,
				`kest.yaml:14:22: listeners[0].http.virtual_hosts[0].routes[1].match: set only one of prefix or path`,
				`kest.yaml:17:15: listeners[0].http.virtual_hosts[0].routes[2]: an action is required`,
				`kest.yaml:17:22: listeners[0].http.virtual_hosts[0].routes[2].match: required: set prefix or path`,
			},
		},
		{
			name: "tls",
			src: `version: v1
listeners:
  - name: a
    address: 127.0.0.1:8443
    tls: {min_version: "1.1"}
    http: &http
      virtual_hosts:
        - name: a
          domains: ["*"]
          routes: [{match: {prefix: /}, direct_response: {}}]
  - name: b
    address: 127.0.0.1:8444
    tls:
      certificates:
        - {cert_file: site.pem}
        - {key_file: site.key}
    http: *http
`,
			want: []string{
				`kest.yaml:5:10: listeners[0].tls.certificates: at least one certificate is required`,
				`kest.yaml:5:24: listeners[0].tls.min_version: unsupported TLS version "1.1"; use "1.2" or "1.3"`,
				`kest.yaml:15:11: listeners[1].tls.certificates[0].key_file: required`,
				`kest.yaml:16:11: listeners[1].tls.certificates[1].cert_file: required`,
			},
		},
		{
			name: "duplicates",
			src: `version: v1
admin: {address: 127.0.0.1:9000}
listeners:
  - name: main
    address: 127.0.0.1:9000
    http:
      virtual_hosts:
        - name: a
          domains: ["*"]
          routes: [{match: {prefix: /}, direct_response: {}}]
        - name: a
          domains: ["*"]
          routes: [{match: {prefix: /}, direct_response: {}}]
  - name: main
    address: 127.0.0.1:9001
clusters:
  - {name: app, type: fake}
  - {name: app}
`,
			want: []string{
				"listeners[0].address: address 127.0.0.1:9000 is already used by admin.address",
				`listeners[0].http.virtual_hosts[1].name: duplicate virtual host name "a"`,
				`listeners[0].http.virtual_hosts[1].domains[0]: domain "*" is already used by listeners[0].http.virtual_hosts[0].domains[0]`,
				"kest.yaml:14:5: listeners[1].http: required",
				`kest.yaml:14:11: listeners[1].name: duplicate listener name "main"`,
				"kest.yaml:18:5: clusters[1].type: required",
				`kest.yaml:18:12: clusters[1].name: duplicate cluster name "app"`,
			},
		},
		{
			name: "port 0 never conflicts, but a real port does",
			src: `version: v1
admin: {address: "127.0.0.1:0"}
listeners:
  - {name: a, address: "127.0.0.1:0"}
  - {name: b, address: "127.0.0.1:8080"}
  - {name: c, address: "127.0.0.1:8080"}
`,
			want: []string{
				"kest.yaml:4:5: listeners[0].http: required",
				"kest.yaml:5:5: listeners[1].http: required",
				"kest.yaml:6:5: listeners[2].http: required",
				"kest.yaml:6:24: listeners[2].address: address 127.0.0.1:8080 is already used by listeners[1].address",
			},
		},
		{
			name: "server limits",
			src: `version: v1
listeners:
  - name: main
    address: 127.0.0.1:0
    http:
      read_timeout: -1s
      max_header_bytes: 512
      virtual_hosts: [{name: d, domains: ["*"], routes: [{match: {prefix: /}, direct_response: {}}]}]
`,
			want: []string{
				"kest.yaml:6:21: listeners[0].http.read_timeout: must not be negative",
				"kest.yaml:7:25: listeners[0].http.max_header_bytes: must be between 1KB and 64MB, got 512B",
			},
		},
		{
			name: "unset environment variable",
			src: `version: v1
listeners:
  - name: main
    address: 127.0.0.1:${PORT}
`,
			want: []string{"kest.yaml:4:14: listeners[0].address: environment variable PORT is not set"},
		},
		{
			name: "duration without unit",
			src: `version: v1
server: {drain_timeout: 30}
listeners: []
`,
			want: []string{`kest.yaml:2:25: server.drain_timeout: invalid duration "30"`},
		},
		{
			name: "quoted reference into an int field",
			src: `version: v1
listeners:
  - name: main
    address: 127.0.0.1:8080
    http:
      virtual_hosts:
        - name: a
          domains: ["*"]
          routes: [{match: {prefix: /}, direct_response: {status: "${STATUS}"}}]
`,
			env:  map[string]string{"STATUS": "204"},
			want: []string{"kest.yaml:9:67: listeners[0].http.virtual_hosts[0].routes[0].direct_response.status: cannot construct !!str `204` into int (a quoted ${...} reference stays a string"},
		},
		{
			name: "duplicate key",
			src:  "version: v1\nversion: v1\n",
			want: []string{`kest.yaml:2:1: version: mapping key "version" already defined at line 1`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse(t, tt.src, tt.env)
			if err == nil {
				t.Fatal("Parse succeeded, want an error")
			}
			text := err.Error()
			rest := text
			for _, w := range tt.want {
				i := strings.Index(rest, w)
				if i < 0 {
					t.Fatalf("error text does not contain %q (in order):\n%s", w, text)
				}
				rest = rest[i+len(w):]
			}
		})
	}
}

func TestParseReturnsErrors(t *testing.T) {
	_, err := parse(t, "version: v1\n", nil)
	var errs Errors
	if !errors.As(err, &errs) {
		t.Fatalf("error type = %T, want Errors", err)
	}
	if len(errs) != 1 || errs[0].Path != "listeners" || errs[0].Line != 1 {
		t.Errorf("errors = %v", errs)
	}
}

type fakeClusterConfig struct {
	Workers int      `yaml:"workers"`
	Command []string `yaml:"command"`
	Timeout Duration `yaml:"timeout"`
}

func TestExtensionDecodeConfig(t *testing.T) {
	src := `version: v1
listeners:
  - name: main
    address: 127.0.0.1:8080
    http:
      virtual_hosts:
        - name: default
          domains: ["*"]
          routes: [{match: {prefix: /}, route: {cluster: a}}]
clusters:
  - name: a
    type: fake
    config:
      workers: 4
  - name: b
    type: fake
    config:
      workers: two
      extra: true
  - name: c
    type: fake
    config: ~
`
	cfg, err := parse(t, src, nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	a := &cfg.Clusters[0]
	got := &fakeClusterConfig{Timeout: Duration(time.Second)}
	if err := a.DecodeConfig(got); err != nil {
		t.Fatalf("DecodeConfig(a): %v", err)
	}
	if got.Workers != 4 || got.Timeout.Std() != time.Second {
		t.Errorf("decoded = %+v, want workers 4 and the default timeout kept", got)
	}

	err = cfg.Clusters[1].DecodeConfig(&fakeClusterConfig{})
	want := []string{
		"kest.yaml:18:16: clusters[1].config.workers: cannot construct !!str `two` into int",
		"kest.yaml:19:7: clusters[1].config.extra: unknown field",
	}
	if err == nil || !containsInOrder(err.Error(), want) {
		t.Errorf("DecodeConfig(b) error =\n%v\nwant it to contain %q", err, want)
	}

	c := &cfg.Clusters[2]
	if c.HasConfig() {
		t.Error("a null config block should count as absent")
	}
	if err := c.DecodeConfig(&fakeClusterConfig{}); err != nil {
		t.Errorf("DecodeConfig(c): %v", err)
	}

	if err := a.Errorf("type", "unknown cluster type %q", a.Type); err.Error() != `kest.yaml:12:11: clusters[0].type: unknown cluster type "fake"` {
		t.Errorf("Errorf = %v", err)
	}
	if err := a.Errorf("config.workers", "too many"); err.Error() != "kest.yaml:14:16: clusters[0].config.workers: too many" {
		t.Errorf("Errorf = %v", err)
	}
}

func TestDump(t *testing.T) {
	cfg, err := parse(t, validConfig, map[string]string{"PORT": "8080", "STATUS": "200"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Before the graph is built, extension configs are dumped as written.
	raw, err := cfg.Dump()
	if err != nil {
		t.Fatalf("Dump (raw): %v", err)
	}
	if !strings.Contains(string(raw), "workers: 4") || strings.Contains(string(raw), "timeout: 1m0s") {
		t.Errorf("raw dump should show the config block as written:\n%s", raw)
	}

	cfg.Clusters[0].SetDecoded(&fakeClusterConfig{Workers: 4, Timeout: Duration(time.Minute)})

	out, err := cfg.Dump()
	if err != nil {
		t.Fatalf("Dump: %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"drain_timeout: 15s",
		"timeout: 30s",
		"status: 200",
		"workers: 4",
		"timeout: 1m0s",
		`- '*.example.com'`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("dump does not contain %q:\n%s", want, text)
		}
	}

	// The dump is itself a valid configuration.
	if _, err := parse(t, text, nil); err != nil {
		t.Errorf("re-parsing the dump: %v\n%s", err, text)
	}
}

func containsInOrder(text string, parts []string) bool {
	for _, p := range parts {
		i := strings.Index(text, p)
		if i < 0 {
			return false
		}
		text = text[i+len(p):]
	}
	return true
}

type secretConfig struct {
	Password string `yaml:"password"`
}

func (c *secretConfig) Redacted() any { return &secretConfig{Password: Redacted} }

func TestDumpRedacted(t *testing.T) {
	cfg, err := parse(t, validConfig, map[string]string{"PORT": "8080", "STATUS": "200"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Not decoded: the block's content is unknown, so it is hidden.
	out, err := cfg.DumpRedacted()
	if err != nil || strings.Contains(string(out), "workers: 4") || !strings.Contains(string(out), "config: '[redacted]'") {
		t.Errorf("DumpRedacted before decoding (%v):\n%s", err, out)
	}

	cfg.Clusters[0].SetDecoded(&secretConfig{Password: "hunter2"})
	out, err = cfg.DumpRedacted()
	if err != nil || strings.Contains(string(out), "hunter2") || !strings.Contains(string(out), "password: '[redacted]'") {
		t.Errorf("DumpRedacted (%v):\n%s", err, out)
	}
	// The config itself is unchanged.
	if plain, _ := cfg.Dump(); !strings.Contains(string(plain), "password: hunter2") {
		t.Errorf("Dump after DumpRedacted:\n%s", plain)
	}
}
