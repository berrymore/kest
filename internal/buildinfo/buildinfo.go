// Package buildinfo reports the version of the running binary.
//
// Release builds inject values through -ldflags (see the Makefile). Other
// builds fall back to the module and VCS information that the Go toolchain
// embeds automatically.
package buildinfo

import (
	"runtime"
	"runtime/debug"
)

// Set at link time with -ldflags "-X github.com/berrymore/kest/internal/buildinfo.version=…".
// They are never modified at run time.
var (
	version string
	commit  string
	date    string
)

// Info describes the running binary.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Date      string `json:"date,omitempty"`
	GoVersion string `json:"go_version"`
}

// Get returns build information, preferring link-time values over embedded VCS data.
func Get() Info {
	info := Info{
		Version:   version,
		Commit:    commit,
		Date:      date,
		GoVersion: runtime.Version(),
	}

	if bi, ok := debug.ReadBuildInfo(); ok {
		if info.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			info.Version = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = s.Value
				}
			case "vcs.time":
				if info.Date == "" {
					info.Date = s.Value
				}
			}
		}
	}

	if info.Version == "" {
		info.Version = "dev"
	}
	return info
}
