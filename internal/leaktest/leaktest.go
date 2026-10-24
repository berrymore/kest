// Package leaktest fails a test binary that leaves goroutines behind.
// It is imported only from tests.
package leaktest

import (
	"fmt"
	"net/http"
	"os"
	"testing"

	"go.uber.org/goleak"
)

// VerifyTestMain runs the package's tests, then checks for leaked
// goroutines. Call it from TestMain in every package that starts goroutines:
//
//	func TestMain(m *testing.M) { leaktest.VerifyTestMain(m) }
//
// Idle keep-alive connections of http.DefaultClient are closed first: they
// belong to the client, not to the code under test.
func VerifyTestMain(m *testing.M) {
	code := m.Run()
	if code == 0 {
		http.DefaultClient.CloseIdleConnections()
		if err := goleak.Find(); err != nil {
			fmt.Fprintf(os.Stderr, "leaktest: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}
