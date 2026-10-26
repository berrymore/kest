// Package kvtest runs PHP's roadrunner/kv against a kv plugin, built in or
// external, and expects the same answers of both (kv/testdata/php).
package kvtest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// PHPDir returns the PHP project of the checks, skipping the test when php
// or its composer packages are missing.
func PHPDir(t *testing.T) (php, dir string) {
	t.Helper()
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php is not in PATH")
	}
	_, file, _, _ := runtime.Caller(0)
	dir = filepath.Join(filepath.Dir(file), "..", "testdata", "php")
	if _, err := os.Stat(filepath.Join(dir, "vendor", "autoload.php")); err != nil {
		t.Skipf("%s has no vendor/: run `make test-integration`, which runs composer install", dir)
	}
	return php, dir
}

// RunPHPChecks runs client.php against the RPC server at rrRPC, whose plugin
// kv has the storages local and other, and compares its output with
// expected.jsonl.
func RunPHPChecks(t *testing.T, rrRPC string) {
	t.Helper()
	php, dir := PHPDir(t)
	cmd := exec.CommandContext(t.Context(), php, "client.php") //nolint:gosec // php from PATH, the test's script
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "RR_RPC="+rrRPC)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("client.php: %v\n%s", err, stderr.String())
	}

	expected, err := os.ReadFile(filepath.Join(dir, "expected.jsonl")) //nolint:gosec // the package's testdata
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for line := range bytes.Lines(expected) {
		want[check(t, line)] = string(bytes.TrimSpace(line))
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		name := check(t, sc.Bytes())
		if got := sc.Text(); got != want[name] {
			t.Errorf("%s:\n got %s\nwant %s", name, got, want[name])
		}
		delete(want, name)
	}
	for name := range want {
		t.Errorf("client.php made no %q check:\n%s", name, out)
	}
}

func check(t *testing.T, line []byte) string {
	t.Helper()
	var c struct{ Check string }
	if err := json.Unmarshal(line, &c); err != nil {
		t.Fatalf("%q: %v", line, err)
	}
	return c.Check
}
