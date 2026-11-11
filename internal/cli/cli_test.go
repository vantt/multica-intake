package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

func tempHome(t *testing.T) string {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(h, ".local", "state"))
	t.Setenv("MULTICA_INTAKE_CONFIG_DIR", "")
	t.Setenv("MULTICA_INTAKE_STATE_DIR", "")
	return h
}

func TestNoArgsPrintsTaskOrderedHelp(t *testing.T) {
	code, out, _ := run(t)
	if code != 0 || !strings.Contains(out, "Get started:") || !strings.Contains(out, "Agents / automation") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestEveryKeyCommandHasSelfSufficientHelp(t *testing.T) {
	for _, c := range []string{"init", "doctor", "update", "config", "uninstall"} {
		code, out, _ := run(t, c, "--help")
		if code != 0 {
			t.Fatalf("%s --help exit %d", c, code)
		}
		for _, want := range []string{"Examples:", "Exit codes:"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s --help lacks %q", c, want)
			}
		}
	}
}

func TestUnknownCommandAndFlag(t *testing.T) {
	code, _, errOut := run(t, "doctr")
	if code != ExitUsage || !strings.Contains(errOut, `Did you mean "doctor"`) {
		t.Fatalf("%d %q", code, errOut)
	}
	code, _, errOut = run(t, "doctor", "--fxi")
	if code != ExitUsage || !strings.Contains(errOut, "--fix") {
		t.Fatalf("%d %q", code, errOut)
	}
}

func TestVersionJSON(t *testing.T) {
	code, out, _ := run(t, "version", "--json")
	var env Envelope
	if code != 0 || json.Unmarshal([]byte(out), &env) != nil || !env.OK || env.Schema != 1 {
		t.Fatalf("%d %q", code, out)
	}
	code, out, _ = run(t, "--version")
	if code != 0 || !strings.HasPrefix(out, "multica-intake ") {
		t.Fatalf("%q", out)
	}
}

func TestAgentLoopInitDoctor(t *testing.T) {
	tempHome(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer api.Close()
	t.Setenv("MULTICA_INTAKE_MULTICA_URL", api.URL)

	// init --yes --json never waits, prints only JSON, names the next step.
	code, out, _ := run(t, "init", "--yes", "--json")
	var env Envelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("stdout is not one JSON envelope: %q", out)
	}
	if code != ExitProblem || env.OK || len(env.Next) == 0 || !strings.Contains(env.Next[0], "intake_agent") {
		t.Fatalf("first init should ask for intake_agent: %d %+v", code, env)
	}
	// Run the fix the envelope names.
	if c, _, e := run(t, "config", "set", "intake_agent", "PM"); c != 0 {
		t.Fatalf("config set: %d %s", c, e)
	}
	code, out, _ = run(t, "doctor", "--json")
	if json.Unmarshal([]byte(out), &env) != nil || code != 0 || !env.OK {
		t.Fatalf("doctor after fix: %d %q", code, out)
	}
	// init again is idempotent.
	code, out, _ = run(t, "init", "--yes", "--json")
	json.Unmarshal([]byte(out), &env)
	data, _ := json.Marshal(env.Data)
	if code != 0 || strings.Contains(string(data), `"changes":["wrote`) {
		t.Fatalf("second init changed something: %s", data)
	}
}

func TestDoctorFixesTokenFileMode(t *testing.T) {
	h := tempHome(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(200)
			return
		}
		http.Error(w, `{"error":"no"}`, 401)
	}))
	defer api.Close()
	t.Setenv("MULTICA_INTAKE_MULTICA_URL", api.URL)
	t.Setenv("MULTICA_INTAKE_INTAKE_AGENT", "PM")
	run(t, "init", "--yes")
	tf := filepath.Join(h, ".config", "multica-intake", "token")
	os.WriteFile(tf, []byte("mul_x\n"), 0o644)
	code, out, _ := run(t, "doctor", "--fix", "--json")
	st, _ := os.Stat(tf)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("token file still %o: %s", st.Mode().Perm(), out)
	}
	// The token itself is refused by Multica: doctor must say so and exit 1.
	if code != ExitProblem || !strings.Contains(out, "refused the token") {
		t.Fatalf("%d %s", code, out)
	}
}

func TestConfigShowGetExplain(t *testing.T) {
	tempHome(t)
	if c, _, _ := run(t, "config", "set", "workspaces", "a,b"); c != 0 {
		t.Fatal("set")
	}
	_, out, _ := run(t, "config", "get", "workspaces")
	if strings.TrimSpace(out) != "a,b" {
		t.Fatalf("get %q", out)
	}
	if c, _, e := run(t, "config", "set", "nope", "1"); c != ExitUsage || !strings.Contains(e, "unknown key") {
		t.Fatalf("%d %s", c, e)
	}
	if c, out, _ := run(t, "config", "explain", "listen"); c != 0 || !strings.Contains(out, "loopback") {
		t.Fatalf("%q", out)
	}
}

func TestUninstallWithoutTTYNeedsYes(t *testing.T) {
	h := tempHome(t)
	t.Setenv("MULTICA_INTAKE_MULTICA_URL", "http://127.0.0.1:1")
	run(t, "config", "set", "intake_agent", "PM")
	if c, _, e := run(t, "uninstall", "--purge"); c != ExitUsage || !strings.Contains(e, "--yes") {
		t.Fatalf("%d %s", c, e)
	}
	if c, _, _ := run(t, "uninstall", "--purge", "--yes"); c != 0 {
		t.Fatal("uninstall")
	}
	if _, err := os.Stat(filepath.Join(h, ".config", "multica-intake")); !os.IsNotExist(err) {
		t.Fatal("config dir kept after --purge")
	}
}

func TestEnvOnlyConfigIsHealthy(t *testing.T) {
	tempHome(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer api.Close()
	t.Setenv("MULTICA_INTAKE_MULTICA_URL", api.URL)
	t.Setenv("MULTICA_INTAKE_INTAKE_AGENT", "PM")
	code, out, _ := run(t, "doctor", "--json")
	if code != 0 || !strings.Contains(out, "environment variables") {
		t.Fatalf("%d %s", code, out)
	}
}
