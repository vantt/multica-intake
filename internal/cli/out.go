package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Exit codes, the same for every verb (CMD-08).
const (
	ExitOK      = 0
	ExitProblem = 1
	ExitUsage   = 2
	ExitUpdate  = 10 // update --check found a newer version
)

// Envelope is the one --json shape (CMD-11).
type Envelope struct {
	Schema int       `json:"schema"`
	OK     bool      `json:"ok"`
	Data   any       `json:"data"`
	Error  *ErrorOut `json:"error"`
	Next   []string  `json:"next"`
}

// ErrorOut is Error / Why / Fix.
type ErrorOut struct {
	Code string `json:"code"`
	Why  string `json:"why"`
	Fix  string `json:"fix"`
}

// env carries the global flags and streams of one run.
type env struct {
	stdout, stderr io.Writer
	stdin          io.Reader
	json           bool
	yes            bool
	dryRun         bool
	quiet          bool
	verbose        bool
	color          bool
	configFile     string
}

func (e *env) emit(ok bool, data any, errOut *ErrorOut, next []string, human func()) {
	if next == nil {
		next = []string{}
	}
	if e.json {
		enc := json.NewEncoder(e.stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		_ = enc.Encode(Envelope{Schema: 1, OK: ok, Data: data, Error: errOut, Next: next})
		return
	}
	if human != nil {
		human()
	}
	if errOut != nil {
		e.errorBlock(errOut)
	}
	if len(next) > 0 && !e.quiet {
		fmt.Fprintln(e.stderr, "\nNext:")
		for _, n := range next {
			fmt.Fprintln(e.stderr, "  "+n)
		}
	}
}

func (e *env) errorBlock(x *ErrorOut) {
	fmt.Fprintln(e.stderr, e.paint("31", "Error: ")+x.Why)
	if x.Fix != "" {
		fmt.Fprintln(e.stderr, e.paint("33", "Fix:   ")+x.Fix)
	}
}

// fail reports an error and returns its exit code.
func (e *env) fail(code int, errCode, why, fix string) int {
	e.emit(false, nil, &ErrorOut{Code: errCode, Why: why, Fix: fix}, nil, nil)
	return code
}

func (e *env) usage(why, fix string) int { return e.fail(ExitUsage, "usage", why, fix) }

func (e *env) info(format string, a ...any) {
	if e.quiet || e.json {
		return
	}
	fmt.Fprintf(e.stderr, format+"\n", a...)
}

func (e *env) paint(code, s string) string {
	if !e.color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func isTTY(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func colorWanted(stderr io.Writer, noColor bool) bool {
	if noColor || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := stderr.(*os.File)
	return ok && isTTY(f)
}

func quote(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\"'$`\\!*?;&|<>()[]{}~#") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}
