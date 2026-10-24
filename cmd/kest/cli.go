package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/berrymore/kest/internal/buildinfo"
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

type command struct {
	name    string
	summary string
	run     func(args []string, stdout, stderr io.Writer) int
}

func commands() []command {
	return []command{
		{name: "serve", summary: "Run the server", run: runServe},
		{name: "validate", summary: "Check a config file and optionally print the effective config", run: runValidate},
		{name: "version", summary: "Print version information", run: runVersion},
	}
}

// run dispatches to a subcommand and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return exitUsage
	}

	name := args[0]
	switch name {
	case "help", "-h", "-help", "--help":
		printUsage(stdout)
		return exitOK
	}

	for _, c := range commands() {
		if c.name == name {
			return c.run(args[1:], stdout, stderr)
		}
	}

	fmt.Fprintf(stderr, "kest: unknown command %q\n\n", name)
	printUsage(stderr)
	return exitUsage
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: kest <command> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, c := range commands() {
		fmt.Fprintf(w, "  %-10s %s\n", c.name, c.summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, `Run "kest <command> -h" for command flags.`)
}

// newFlagSet creates a flag set that reports errors instead of exiting.
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("kest "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// parseFlags parses args and maps the result to an exit code when parsing must stop.
func parseFlags(fs *flag.FlagSet, args []string) (code int, ok bool) {
	err := fs.Parse(args)
	switch {
	case err == nil:
		if fs.NArg() > 0 {
			fmt.Fprintf(fs.Output(), "%s: unexpected arguments: %v\n", fs.Name(), fs.Args())
			return exitUsage, false
		}
		return exitOK, true
	case errors.Is(err, flag.ErrHelp):
		return exitOK, false
	default:
		return exitUsage, false
	}
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("version", stderr)
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}

	info := buildinfo.Get()
	fmt.Fprintf(stdout, "kest %s\n", info.Version)
	if info.Commit != "" {
		fmt.Fprintf(stdout, "commit: %s\n", info.Commit)
	}
	if info.Date != "" {
		fmt.Fprintf(stdout, "built:  %s\n", info.Date)
	}
	fmt.Fprintf(stdout, "go:     %s\n", info.GoVersion)
	return exitOK
}
