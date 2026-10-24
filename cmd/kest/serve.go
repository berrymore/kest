package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/logging"
	"github.com/berrymore/kest/internal/server"
)

const defaultConfigFile = "kest.yaml"

func runServe(args []string, _, stderr io.Writer) int {
	fs := newFlagSet("serve", stderr)
	configFile := fs.String("c", defaultConfigFile, "path to the config file")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}

	_, srv, ok := build(*configFile, stderr, true)
	if !ok {
		return exitError
	}

	// The first SIGINT/SIGTERM starts a graceful shutdown. Restoring the
	// default handlers then lets a second signal terminate immediately.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	context.AfterFunc(ctx, stop)

	if err := srv.Run(ctx); err != nil {
		fmt.Fprintf(stderr, "kest: %v\n", err)
		return exitError
	}
	return exitOK
}

func runValidate(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("validate", stderr)
	configFile := fs.String("c", defaultConfigFile, "path to the config file")
	dump := fs.Bool("dump", false, "print the effective config, with defaults applied")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}

	cfg, _, ok := build(*configFile, stderr, false)
	if !ok {
		return exitError
	}

	if !*dump {
		fmt.Fprintf(stdout, "%s: configuration is valid\n", *configFile)
		return exitOK
	}
	out, err := cfg.Dump()
	if err != nil {
		fmt.Fprintf(stderr, "kest: dump config: %v\n", err)
		return exitError
	}
	_, _ = stdout.Write(out)
	return exitOK
}

// build loads the config and builds the server graph, which also validates
// extension configs. It reports problems on stderr and returns ok=false on
// failure. Logs go to stderr when withLogs is set.
func build(file string, stderr io.Writer, withLogs bool) (*config.Config, *server.Server, bool) {
	cfg, err := config.Load(file)
	if err != nil {
		fmt.Fprintf(stderr, "kest: invalid configuration:\n%v\n", err)
		return nil, nil, false
	}

	logger := logging.Discard()
	if withLogs {
		if logger, err = logging.New(cfg.Log, stderr); err != nil {
			fmt.Fprintf(stderr, "kest: %v\n", err)
			return nil, nil, false
		}
	}

	srv, err := server.New(cfg, logger)
	if err != nil {
		fmt.Fprintf(stderr, "kest: invalid configuration:\n%v\n", err)
		return nil, nil, false
	}
	return cfg, srv, true
}
