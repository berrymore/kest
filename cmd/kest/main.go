// Command kest is an Envoy-inspired application server for PHP workers.
package main

import (
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
