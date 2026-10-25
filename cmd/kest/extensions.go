package main

// Built-in extensions register themselves in init (ADR-0004). A type that
// isn't imported here is unknown to the binary.
import (
	_ "github.com/berrymore/kest/internal/cluster/phpworkers"
	_ "github.com/berrymore/kest/internal/filters/accesslog"
	_ "github.com/berrymore/kest/internal/filters/recovery"
	_ "github.com/berrymore/kest/internal/filters/requestid"
)
