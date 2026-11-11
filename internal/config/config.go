// Package config owns the settings file: defaults, load, validate, and the one
// writer that every `config set|unset` and `init` goes through.
//
// Precedence: flag > env (MULTICA_INTAKE_<KEY>) > config file > default.
// Unknown keys are an error: this is security config, and a typo must not be
// silently ignored (CFG-09).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Config is the whole settings file.
type Config struct {
	MulticaURL   string            `yaml:"multica_url"`
	AppURL       string            `yaml:"app_url"`
	Listen       string            `yaml:"listen"`
	Source       string            `yaml:"source"`
	IntakeAgent  string            `yaml:"intake_agent"`
	IntakeAgents map[string]string `yaml:"intake_agents"`
	Workspaces   []string          `yaml:"workspaces"`
	TypeProperty string            `yaml:"type_property"`
	Locale       string            `yaml:"locale"`
	Timeout      string            `yaml:"timeout"`
	TokenFile    string            `yaml:"token_file"`
	MaxTitle     int               `yaml:"max_title"`
	MaxRequest   int               `yaml:"max_request"`
	MaxComment   int               `yaml:"max_comment"`
	MaxList      int               `yaml:"max_list"`
}

// Key describes one setting for `config explain` and `--help`.
type Key struct {
	Name    string
	Default string
	Help    string
}

// Keys is the schema, in the order `config default` prints it.
var Keys = []Key{
	{"multica_url", "http://127.0.0.1:8080", "Base URL of the Multica API (the backend, not the web app)."},
	{"app_url", "", "Base URL of the Multica web app, used for issue links. Empty: no links."},
	{"listen", "127.0.0.1:8801", "Address `mcp --http` listens on. Keep it on loopback; a non-loopback address prints a warning."},
	{"source", "intake", "Value written to the issue metadata key `source` (e.g. mausbot). Every tool only sees issues with this source."},
	{"intake_agent", "", "Name of the Multica agent every submitted issue is assigned to (the one that triages). Required."},
	{"intake_agents", "{}", "Per-workspace override of intake_agent: intake_agents.<workspace-slug> = <agent name>."},
	{"workspaces", "[]", "Workspace slugs callers may submit to. Empty: every workspace the caller's account is a member of."},
	{"type_property", "Type", "Name of the select property whose options are offered as Type hints. Empty: no Type hints."},
	{"locale", "en", "Language of the text written into issues: en or vi."},
	{"timeout", "15s", "Timeout of each Multica API call."},
	{"token_file", "", "PAT file for `mcp` over stdio (0600). Empty: <config_dir>/token. Never used by `mcp --http`, where each caller sends its own."},
	{"max_title", "200", "Longest title accepted, in characters."},
	{"max_request", "20000", "Longest request text accepted, in characters."},
	{"max_comment", "8000", "Longest reply accepted, in characters."},
	{"max_list", "50", "Most issues list_my_issues returns."},
}

// Default returns the built-in defaults (the binary owns them: CFG-05).
func Default() Config {
	return Config{
		MulticaURL: "http://127.0.0.1:8080", Listen: "127.0.0.1:8801", Source: "intake",
		IntakeAgents: map[string]string{}, Workspaces: []string{}, TypeProperty: "Type",
		Locale: "en", Timeout: "15s", MaxTitle: 200, MaxRequest: 20000, MaxComment: 8000, MaxList: 50,
	}
}

// Load reads the file over the defaults, then env overrides. A missing file is
// not an error (defaults apply); a corrupt one is.
func Load(file string) (Config, bool, error) {
	c := Default()
	exists := false
	b, err := os.ReadFile(file)
	switch {
	case err == nil:
		exists = true
		if err := decodeStrict(b, &c); err != nil {
			return c, true, fmt.Errorf("%s: %w", file, err)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return c, false, err
	}
	if c.IntakeAgents == nil {
		c.IntakeAgents = map[string]string{}
	}
	if c.Workspaces == nil {
		c.Workspaces = []string{}
	}
	for _, k := range Keys {
		env := "MULTICA_INTAKE_" + strings.ToUpper(k.Name)
		if v, ok := os.LookupEnv(env); ok {
			if err := c.Set(k.Name, v); err != nil {
				return c, exists, fmt.Errorf("%s: %w", env, err)
			}
		}
	}
	return c, exists, nil
}

func decodeStrict(b []byte, c *Config) error {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// Validate reports every problem at once (CMD-10).
func (c Config) Validate() []string {
	var p []string
	if u, err := url.Parse(c.MulticaURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		p = append(p, "multica_url must be an http(s) URL, e.g. http://127.0.0.1:8080")
	}
	if c.AppURL != "" {
		if u, err := url.Parse(c.AppURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			p = append(p, "app_url must be empty or an http(s) URL")
		}
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		p = append(p, "listen must be host:port, e.g. 127.0.0.1:8801")
	}
	if !validSource(c.Source) {
		p = append(p, "source must be 1-40 characters of a-z, 0-9, - or _")
	}
	if strings.TrimSpace(c.IntakeAgent) == "" {
		p = append(p, "intake_agent is not set: name the Multica agent that receives submitted issues")
	}
	if c.Locale != "en" && c.Locale != "vi" {
		p = append(p, "locale must be en or vi")
	}
	if _, err := time.ParseDuration(c.Timeout); err != nil {
		p = append(p, "timeout must be a duration, e.g. 15s")
	}
	for name, v := range map[string]int{"max_title": c.MaxTitle, "max_request": c.MaxRequest, "max_comment": c.MaxComment, "max_list": c.MaxList} {
		if v < 1 {
			p = append(p, name+" must be a positive number")
		}
	}
	sort.Strings(p)
	return p
}

func validSource(s string) bool {
	if len(s) == 0 || len(s) > 40 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// TimeoutDuration is Timeout parsed (Validate guarantees it parses).
func (c Config) TimeoutDuration() time.Duration {
	d, err := time.ParseDuration(c.Timeout)
	if err != nil {
		return 15 * time.Second
	}
	return d
}

// AgentFor returns the intake agent name for a workspace slug.
func (c Config) AgentFor(slug string) string {
	if a := c.IntakeAgents[slug]; a != "" {
		return a
	}
	return c.IntakeAgent
}

// LoopbackListen reports whether Listen is a loopback address.
func (c Config) LoopbackListen() bool {
	h, _, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return false
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Get returns a key's value as text.
func (c Config) Get(key string) (string, error) {
	if strings.HasPrefix(key, "intake_agents.") {
		return c.IntakeAgents[strings.TrimPrefix(key, "intake_agents.")], nil
	}
	switch key {
	case "multica_url":
		return c.MulticaURL, nil
	case "app_url":
		return c.AppURL, nil
	case "listen":
		return c.Listen, nil
	case "source":
		return c.Source, nil
	case "intake_agent":
		return c.IntakeAgent, nil
	case "intake_agents":
		var parts []string
		for k, v := range c.IntakeAgents {
			parts = append(parts, k+"="+v)
		}
		sort.Strings(parts)
		return strings.Join(parts, ","), nil
	case "workspaces":
		return strings.Join(c.Workspaces, ","), nil
	case "type_property":
		return c.TypeProperty, nil
	case "locale":
		return c.Locale, nil
	case "timeout":
		return c.Timeout, nil
	case "token_file":
		return c.TokenFile, nil
	case "max_title":
		return strconv.Itoa(c.MaxTitle), nil
	case "max_request":
		return strconv.Itoa(c.MaxRequest), nil
	case "max_comment":
		return strconv.Itoa(c.MaxComment), nil
	case "max_list":
		return strconv.Itoa(c.MaxList), nil
	}
	return "", unknownKey(key)
}

// Set changes one key. Lists take comma-separated values.
func (c *Config) Set(key, val string) error {
	val = strings.TrimSpace(val)
	if strings.HasPrefix(key, "intake_agents.") {
		slug := strings.TrimPrefix(key, "intake_agents.")
		if slug == "" {
			return errors.New("intake_agents.<workspace-slug>: the slug is missing")
		}
		if c.IntakeAgents == nil {
			c.IntakeAgents = map[string]string{}
		}
		if val == "" {
			delete(c.IntakeAgents, slug)
		} else {
			c.IntakeAgents[slug] = val
		}
		return nil
	}
	atoi := func(dst *int) error {
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 {
			return fmt.Errorf("%s must be a positive number", key)
		}
		*dst = n
		return nil
	}
	switch key {
	case "multica_url":
		c.MulticaURL = strings.TrimRight(val, "/")
	case "app_url":
		c.AppURL = strings.TrimRight(val, "/")
	case "listen":
		c.Listen = val
	case "source":
		c.Source = val
	case "intake_agent":
		c.IntakeAgent = val
	case "intake_agents":
		c.IntakeAgents = map[string]string{}
		for _, kv := range splitList(val) {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return errors.New("intake_agents takes slug=agent pairs, comma-separated")
			}
			c.IntakeAgents[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	case "workspaces":
		c.Workspaces = splitList(val)
	case "type_property":
		c.TypeProperty = val
	case "locale":
		c.Locale = val
	case "timeout":
		c.Timeout = val
	case "token_file":
		c.TokenFile = val
	case "max_title":
		return atoi(&c.MaxTitle)
	case "max_request":
		return atoi(&c.MaxRequest)
	case "max_comment":
		return atoi(&c.MaxComment)
	case "max_list":
		return atoi(&c.MaxList)
	default:
		return unknownKey(key)
	}
	return nil
}

// Unset puts a key back to its default.
func (c *Config) Unset(key string) error {
	if strings.HasPrefix(key, "intake_agents.") {
		return c.Set(key, "")
	}
	d := Default()
	v, err := d.Get(key)
	if err != nil {
		return err
	}
	if key == "intake_agents" {
		c.IntakeAgents = map[string]string{}
		return nil
	}
	return c.Set(key, v)
}

func splitList(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func unknownKey(key string) error {
	names := make([]string, 0, len(Keys))
	for _, k := range Keys {
		names = append(names, k.Name)
	}
	hint := closest(key, names)
	if hint != "" {
		return fmt.Errorf("unknown key %q: did you mean %q? (all keys: multica-intake config explain)", key, hint)
	}
	return fmt.Errorf("unknown key %q (all keys: multica-intake config explain)", key)
}

// closest returns the candidate within edit distance 3, if any.
func closest(s string, cands []string) string {
	best, bestD := "", 4
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
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// Marshal renders the file so it explains itself (UX-11): each key is preceded by
// what it means and its default.
func (c Config) Marshal() ([]byte, error) {
	var full bytes.Buffer
	enc := yaml.NewEncoder(&full)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return nil, err
	}
	var doc map[string]yaml.Node
	if err := yaml.Unmarshal(full.Bytes(), &doc); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString("# multica-intake settings. Change them with `multica-intake config set <key> <value>`\n")
	buf.WriteString("# (or edit this file, then run `multica-intake config check`). Env MULTICA_INTAKE_<KEY> overrides a key.\n")
	for _, k := range Keys {
		node, ok := doc[k.Name]
		if !ok {
			continue
		}
		buf.WriteString("\n# " + k.Help + "\n")
		if k.Default != "" {
			buf.WriteString("# default: " + k.Default + "\n")
		}
		one, err := yaml.Marshal(map[string]*yaml.Node{k.Name: &node})
		if err != nil {
			return nil, err
		}
		buf.Write(one)
	}
	return buf.Bytes(), nil
}

// Save writes atomically (temp file in the same dir, then rename), keeping the
// existing mode, in a 0700 dir.
func (c Config) Save(file string) error {
	b, err := c.Marshal()
	if err != nil {
		return err
	}
	return WriteAtomic(file, b, 0o600)
}

// WriteAtomic writes data via a temp file + rename; an existing file keeps its mode.
func WriteAtomic(file string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	if st, err := os.Stat(file); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}
