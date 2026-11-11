package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultsNeedOnlyTheIntakeAgent(t *testing.T) {
	p := Default().Validate()
	if len(p) != 1 || !strings.HasPrefix(p[0], "intake_agent") {
		t.Fatalf("defaults should only miss intake_agent, got %v", p)
	}
}

func TestEveryKeyRoundTrips(t *testing.T) {
	c := Default()
	for _, k := range Keys {
		v, err := c.Get(k.Name)
		if err != nil {
			t.Fatalf("get %s: %v", k.Name, err)
		}
		if err := c.Set(k.Name, v); err != nil && v != "" {
			t.Fatalf("set %s=%q: %v", k.Name, v, err)
		}
	}
}

func TestSetGetUnset(t *testing.T) {
	c := Default()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(c.Set("workspaces", "a, b ,,c"))
	if v, _ := c.Get("workspaces"); v != "a,b,c" {
		t.Fatalf("workspaces %q", v)
	}
	must(c.Set("intake_agents.sales", "Mai"))
	if c.AgentFor("sales") != "Mai" || c.AgentFor("x") != "" {
		t.Fatal("AgentFor")
	}
	must(c.Set("multica_url", "http://h:1/"))
	if c.MulticaURL != "http://h:1" {
		t.Fatal("trailing slash kept")
	}
	must(c.Unset("multica_url"))
	if c.MulticaURL != Default().MulticaURL {
		t.Fatal("unset")
	}
	if err := c.Set("max_list", "0"); err == nil {
		t.Fatal("max_list 0 accepted")
	}
	err := c.Set("intake_agnet", "x")
	if err == nil || !strings.Contains(err.Error(), `did you mean "intake_agent"`) {
		t.Fatalf("typo hint: %v", err)
	}
}

func TestLoadIsStrictAndEnvWins(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(f, []byte("intake_agent: A\nbogus: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(f); err == nil {
		t.Fatal("unknown key accepted")
	}
	if err := os.WriteFile(f, []byte("intake_agent: A\nsource: file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_INTAKE_SOURCE", "env")
	c, exists, err := Load(f)
	if err != nil || !exists || c.Source != "env" || c.IntakeAgent != "A" {
		t.Fatalf("%+v %v %v", c, exists, err)
	}
	if _, exists, err := Load(filepath.Join(dir, "none.yaml")); err != nil || exists {
		t.Fatal("missing file should load defaults")
	}
	if err := os.WriteFile(f, []byte(":::"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(f); err == nil {
		t.Fatal("corrupt file accepted")
	}
}

func TestSaveIsAtomicAndPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	f := filepath.Join(dir, "config.yaml")
	c := Default()
	c.IntakeAgent = "Phương"
	if err := c.Save(f); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(f)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	d, _ := os.Stat(dir)
	if d.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %o", d.Mode().Perm())
	}
	got, _, err := Load(f)
	if err != nil || got.IntakeAgent != "Phương" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestValidateReportsEverything(t *testing.T) {
	c := Default()
	c.MulticaURL, c.Listen, c.Source, c.Locale, c.Timeout = "ftp://x", "nope", "Bad Source", "fr", "soon"
	if p := c.Validate(); len(p) != 6 {
		t.Fatalf("%d problems: %v", len(p), p)
	}
	c = Default()
	c.Listen = "0.0.0.0:1"
	if c.LoopbackListen() {
		t.Fatal("0.0.0.0 is not loopback")
	}
}
