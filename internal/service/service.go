// Package service manages the systemd user unit that runs `mcp --http`
// (Linux only). Every action prints the native command it runs (SVC-02).
package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Marker is the first line of a unit this tool wrote; a unit without it is the
// user's and is never touched.
const Marker = "# managed by multica-intake — remove this line to keep your own edits"

// Supported reports whether user services are supported here.
func Supported() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("user services are supported on Linux (systemd) only; on %s run `multica-intake mcp --http` under your own supervisor", runtime.GOOS)
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("systemctl not found: run `multica-intake mcp --http` under your own supervisor")
	}
	return nil
}

// Unit renders the unit for binary. No secrets: the HTTP server takes each
// caller's token per request.
func Unit(binary string) string {
	return Marker + `
[Unit]
Description=multica-intake MCP server (submit work to Multica)
Documentation=https://github.com/vantt/multica-intake
After=network-online.target

[Service]
ExecStart=` + binary + ` mcp --http
Restart=on-failure
RestartSec=3
NoNewPrivileges=yes
PrivateTmp=yes

[Install]
WantedBy=default.target
`
}

// Owned reports whether the unit file exists and carries the marker.
func Owned(unitFile string) (exists, owned bool) {
	b, err := os.ReadFile(unitFile)
	if err != nil {
		return false, false
	}
	return true, bytes.HasPrefix(b, []byte(Marker))
}

// Systemctl runs `systemctl --user args…`, echoing the command to w.
func Systemctl(w io.Writer, args ...string) (string, error) {
	full := append([]string{"--user"}, args...)
	if w != nil {
		fmt.Fprintln(w, "$ systemctl "+strings.Join(full, " "))
	}
	out, err := exec.Command("systemctl", full...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Active reports `systemctl --user is-active multica-intake`.
func Active() bool {
	out, _ := exec.Command("systemctl", "--user", "is-active", "multica-intake").Output()
	return strings.TrimSpace(string(out)) == "active"
}
