// Package version holds the build fingerprint. The release build sets the
// variables with -ldflags -X; a plain `go build` falls back to the module's
// build info so a dev binary still says where it came from.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

var (
	Version = "dev" // vX.Y.Z from the git tag (the only version source)
	Commit  = ""    // short commit sha
	Date    = ""    // commit time, RFC 3339 (not build time: reproducible)
	Dirty   = ""    // "true" when built from a modified tree
)

// Info is the fingerprint printed by `version --json` and served at /healthz.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	Dirty     bool   `json:"dirty"`
	GoVersion string `json:"go"`
	Platform  string `json:"platform"`
}

// Get returns the fingerprint, filling gaps from debug.ReadBuildInfo.
func Get() Info {
	in := Info{Version: Version, Commit: Commit, Date: Date, Dirty: Dirty == "true",
		GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if in.Commit == "" && len(s.Value) >= 7 {
					in.Commit = s.Value[:7]
				}
			case "vcs.time":
				if in.Date == "" {
					in.Date = s.Value
				}
			case "vcs.modified":
				if Dirty == "" {
					in.Dirty = s.Value == "true"
				}
			}
		}
	}
	return in
}

// String is the one-line form: `multica-intake v1.2.3 (abc1234-dirty, 2026-11-11T10:00:00Z)`.
func (i Info) String() string {
	c := i.Commit
	if c == "" {
		c = "unknown"
	}
	if i.Dirty {
		c += "-dirty"
	}
	if i.Date != "" {
		c += ", " + i.Date
	}
	return fmt.Sprintf("multica-intake %s (%s)", i.Version, c)
}
