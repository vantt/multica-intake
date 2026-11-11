// Package doctor is the one engine that reaches the desired state (CMD-01):
// every piece of setup is a check, optionally with a fix. `doctor` only runs
// the checks; `doctor --fix` applies fixes for drift and re-checks; `init` is
// `doctor --fix` with the checks marked Creates enabled.
package doctor

import (
	"context"
	"time"
)

// Status of one check.
type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
	Skip Status = "skip"
)

// Result of running a check.
type Result struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
	// Fix is a copy-pasteable, non-interactive command that repairs the problem
	// when the check has no automatic fix (or the fix needs a human).
	Fix   string `json:"fix,omitempty"`
	Fixed bool   `json:"fixed,omitempty"`
}

// Check is one desired state.
type Check struct {
	ID      string
	Title   string
	Creates bool // its fix creates something new: runs only under init
	Run     func(ctx context.Context) Result
	// Apply repairs the state; nil when there is no automatic fix. dryRun
	// returns what would change without changing it.
	Apply func(ctx context.Context, dryRun bool) (string, error)
}

// Report is the outcome of a run.
type Report struct {
	OK      bool     `json:"ok"`
	Checks  []Result `json:"checks"`
	Changes []string `json:"changes"`
	Rounds  int      `json:"rounds"`
}

// Options of a run.
type Options struct {
	Fix     bool
	Creates bool // init: allow Creates fixes
	DryRun  bool
	Strict  bool // warnings count as problems
}

const maxRounds = 3

// Run executes the checks; with Fix it applies fixes and re-runs everything,
// up to maxRounds, so the exit code reflects the state after the fixes.
func Run(ctx context.Context, checks []Check, o Options) Report {
	rep := Report{Changes: []string{}}
	fixed := map[string]bool{}
	for round := 1; round <= maxRounds; round++ {
		rep.Rounds = round
		rep.Checks = rep.Checks[:0]
		changed := false
		for _, c := range checks {
			cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			r := c.Run(cctx)
			cancel()
			r.ID, r.Title = c.ID, c.Title
			if o.Fix && c.Apply != nil && (r.Status == Fail || r.Status == Warn) && (!c.Creates || o.Creates) {
				cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
				what, err := c.Apply(cctx, o.DryRun)
				cancel()
				if err != nil {
					r.Detail += " (fix failed: " + err.Error() + ")"
				} else if what != "" {
					rep.Changes = append(rep.Changes, what)
					if !o.DryRun {
						changed = true
						fixed[c.ID] = true
					}
				}
			}
			rep.Checks = append(rep.Checks, r)
		}
		if !changed {
			break
		}
	}
	rep.OK = true
	for i := range rep.Checks {
		rep.Checks[i].Fixed = fixed[rep.Checks[i].ID]
	}
	for _, r := range rep.Checks {
		if r.Status == Fail || (o.Strict && r.Status == Warn) {
			rep.OK = false
		}
	}
	return rep
}
