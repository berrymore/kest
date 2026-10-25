package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kest.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const minimalConfig = `version: v1
listeners:
  - name: main
    address: 127.0.0.1:8080
    http:
      virtual_hosts:
        - name: default
          domains: ["*"]
          routes:
            - match: {prefix: /}
              direct_response: {body: "hi"}
`

func TestValidate(t *testing.T) {
	valid := writeConfig(t, minimalConfig)
	invalid := writeConfig(t, strings.Replace(minimalConfig, "address: 127.0.0.1:8080", "address: nowhere", 1))
	badExtension := writeConfig(t, minimalConfig+"clusters:\n  - {name: app, type: nope}\n")
	noCommand := writeConfig(t, minimalConfig+"clusters:\n  - {name: app, type: php_workers}\n")
	phpWorkers := writeConfig(t, minimalConfig+"clusters:\n  - {name: app, type: php_workers, config: {command: [php, worker.php]}}\n")

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "valid",
			args:       []string{"validate", "-c", valid},
			wantCode:   exitOK,
			wantStdout: "configuration is valid",
		},
		{
			name:       "dump",
			args:       []string{"validate", "-c", valid, "-dump"},
			wantCode:   exitOK,
			wantStdout: "drain_timeout: 15s",
		},
		{
			name:       "invalid",
			args:       []string{"validate", "-c", invalid},
			wantCode:   exitError,
			wantStderr: `kest.yaml:4:14: listeners[0].address: invalid address "nowhere"`,
		},
		{
			name:       "unknown extension type",
			args:       []string{"validate", "-c", badExtension},
			wantCode:   exitError,
			wantStderr: `clusters[0].type: unknown cluster type "nope" (known types: php_workers)`,
		},
		{
			name:       "php_workers without a command",
			args:       []string{"validate", "-c", noCommand},
			wantCode:   exitError,
			wantStderr: `kest.yaml:13:5: clusters[0].config.command: must not be empty`,
		},
		{
			name:       "php_workers defaults in the dump",
			args:       []string{"validate", "-c", phpWorkers, "-dump"},
			wantCode:   exitOK,
			wantStdout: "  config:\n    command:\n    - php\n    - worker.php\n    inherit_env: true\n    codec: json\n",
		},
		{
			name:       "missing file",
			args:       []string{"validate", "-c", filepath.Join(t.TempDir(), "nope.yaml")},
			wantCode:   exitError,
			wantStderr: "read config",
		},
		{
			name:       "example config",
			args:       []string{"validate", "-c", "../../examples/hello/kest.yaml"},
			wantCode:   exitOK,
			wantStdout: "configuration is valid",
		},
		{
			name:       "serve with invalid config",
			args:       []string{"serve", "-c", invalid},
			wantCode:   exitError,
			wantStderr: "invalid configuration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d\nstderr: %s", code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}
