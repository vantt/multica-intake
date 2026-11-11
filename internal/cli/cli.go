// Package cli is the command surface: init / doctor / update first, the same
// verbs and flags as every other tool of ours (cs-packaging-distribution).
package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Run executes args (without the program name) and returns the exit code.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	e := &env{stdout: stdout, stderr: stderr, stdin: stdin}
	rest, noColor, err := e.globals(args)
	e.color = colorWanted(stderr, noColor)
	if err != nil {
		return e.usage(err.Error(), "multica-intake --help")
	}
	if len(rest) == 0 {
		if e.json {
			return runHelpJSON(e)
		}
		fmt.Fprint(stdout, topHelp)
		return ExitOK
	}
	cmd, rest := rest[0], rest[1:]
	if cmd == "--help" || cmd == "-h" {
		fmt.Fprint(stdout, topHelp)
		return ExitOK
	}
	if cmd == "--version" {
		cmd = "version"
	}
	if wantsHelp(rest) {
		if h, ok := helpFor(cmd); ok {
			fmt.Fprint(stdout, h)
			return ExitOK
		}
	}
	switch cmd {
	case "help":
		if len(rest) > 0 {
			if h, ok := helpFor(rest[0]); ok {
				fmt.Fprint(stdout, h)
				return ExitOK
			}
		}
		if e.json {
			return runHelpJSON(e)
		}
		fmt.Fprint(stdout, topHelp)
		return ExitOK
	case "version":
		return runVersion(e, rest)
	case "init":
		return runInit(ctx, e, rest)
	case "doctor":
		return runDoctor(ctx, e, rest)
	case "status":
		return runStatus(ctx, e, rest)
	case "config":
		return runConfig(e, rest)
	case "paths":
		return runPaths(e, rest)
	case "mcp":
		return runMCP(ctx, e, rest)
	case "service":
		return runService(e, rest)
	case "update", "upgrade":
		return runUpdate(ctx, e, rest)
	case "uninstall", "remove":
		return runUninstall(e, rest)
	case "completions", "completion":
		return runCompletions(e, rest)
	case "internal-healthcheck": // hidden: Docker HEALTHCHECK (CMD-13)
		return runHealthcheck(ctx, e)
	}
	fix := "multica-intake --help"
	if s := suggest(cmd, commands); s != "" {
		return e.usage(fmt.Sprintf("unknown command %q. Did you mean %q?", cmd, s), "multica-intake "+s+" --help")
	}
	return e.usage(fmt.Sprintf("unknown command %q", cmd), fix)
}

// globals strips the global flags (accepted anywhere) and returns the rest.
func (e *env) globals(args []string) ([]string, bool, error) {
	var rest []string
	noColor := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			e.json = true
		case a == "-y" || a == "--yes":
			e.yes = true
		case a == "--dry-run":
			e.dryRun = true
		case a == "-q" || a == "--quiet":
			e.quiet = true
		case a == "-v" || a == "--verbose":
			e.verbose = true
		case a == "--no-color":
			noColor = true
		case a == "-c" || a == "--config":
			if i+1 >= len(args) {
				return nil, noColor, fmt.Errorf("%s needs a file", a)
			}
			e.configFile = args[i+1]
			i++
		case strings.HasPrefix(a, "--config="):
			e.configFile = strings.TrimPrefix(a, "--config=")
		default:
			rest = append(rest, a)
		}
	}
	return rest, noColor, nil
}

func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--help" || a == "-h" {
			return true
		}
	}
	return false
}

// flags parses command flags; unknown ones are a usage error with a hint.
func (e *env) flags(cmd string, args []string, def func(*flag.FlagSet)) ([]string, int, bool) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if def != nil {
		def(fs)
	}
	// Go's flag stops at the first positional; move flags first.
	var fl, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			fl = append(fl, a)
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(name, "=") {
				if f := fs.Lookup(name); f != nil {
					if _, isBool := f.Value.(interface{ IsBoolFlag() bool }); !isBool && i+1 < len(args) {
						fl = append(fl, args[i+1])
						i++
					}
				}
			}
			continue
		}
		pos = append(pos, a)
	}
	if err := fs.Parse(fl); err != nil {
		var names []string
		fs.VisitAll(func(f *flag.Flag) { names = append(names, "--"+f.Name) })
		msg := err.Error()
		if bad := strings.TrimPrefix(msg, "flag provided but not defined: "); bad != msg {
			if s := suggest(strings.TrimLeft(bad, "-"), stripDashes(names)); s != "" {
				msg += fmt.Sprintf(". Did you mean --%s?", s)
			}
		}
		return nil, e.usage(msg, "multica-intake "+cmd+" --help"), false
	}
	return pos, 0, true
}

func stripDashes(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.TrimLeft(s, "-")
	}
	return out
}

func suggest(s string, cands []string) string {
	best, bestD := "", 3
	for _, c := range cands {
		if d := lev(s, c); d < bestD {
			best, bestD = c, d
		}
	}
	return best
}

func lev(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := 1
			if a[i-1] == b[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev = cur
	}
	return prev[len(b)]
}

func runHelpJSON(e *env) int {
	type cmdInfo struct {
		Name string `json:"name"`
		Help string `json:"help"`
	}
	var cs []cmdInfo
	for _, c := range commands {
		h, _ := helpFor(c)
		cs = append(cs, cmdInfo{Name: c, Help: h})
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
	e.json = true
	e.emit(true, map[string]any{
		"commands":     cs,
		"global_flags": []string{"--json", "-y/--yes", "--dry-run", "-q/--quiet", "-v/--verbose", "--no-color", "-c/--config FILE"},
		"exit_codes":   map[string]string{"0": "ok", "1": "problems found or failed", "2": "usage error", "10": "update available (update --check)"},
		"agent_loop":   []string{"multica-intake init --yes --json", "multica-intake doctor --json  # run each fix/next until ok"},
	}, nil, nil, nil)
	return ExitOK
}

func printJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func stdinIsTTY(e *env) bool {
	f, ok := e.stdin.(*os.File)
	return ok && isTTY(f)
}
