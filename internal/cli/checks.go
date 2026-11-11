package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/vantt/multica-intake/internal/config"
	"github.com/vantt/multica-intake/internal/doctor"
	"github.com/vantt/multica-intake/internal/multica"
	"github.com/vantt/multica-intake/internal/paths"
	"github.com/vantt/multica-intake/internal/service"
	"github.com/vantt/multica-intake/internal/version"
)

// setup is everything the checks look at.
type setup struct {
	p       paths.Paths
	file    string
	cfg     config.Config
	exists  bool
	loadErr error
	pending *config.Config // values gathered by init's prompts, written by the config_file fix
}

func (e *env) loadSetup() (*setup, error) {
	p, err := paths.Resolve()
	if err != nil {
		return nil, err
	}
	s := &setup{p: p, file: p.ConfigFile}
	if e.configFile != "" {
		s.file = e.configFile
	}
	s.cfg, s.exists, s.loadErr = config.Load(s.file)
	return s, nil
}

func (s *setup) tokenFile() string {
	if s.cfg.TokenFile != "" {
		return s.cfg.TokenFile
	}
	return s.p.TokenFile
}

// readToken reads the stdio token file, refusing loose permissions (CFG-07).
func readToken(file string) (string, error) {
	st, err := os.Stat(file)
	if err != nil {
		return "", err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is readable by others (mode %o); run: chmod 600 %s", file, st.Mode().Perm(), quote(file))
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", fmt.Errorf("%s is empty", file)
	}
	return t, nil
}

func (s *setup) checks() []doctor.Check {
	var cs []doctor.Check
	cs = append(cs, doctor.Check{
		ID: "config_file", Title: "Config file", Creates: true,
		Run: func(context.Context) doctor.Result {
			if s.loadErr != nil {
				return doctor.Result{Status: doctor.Fail, Detail: s.loadErr.Error(),
					Fix: "multica-intake config edit   # or: multica-intake config default > " + quote(s.file)}
			}
			if !s.exists {
				return doctor.Result{Status: doctor.Fail, Detail: "no config at " + s.file, Fix: "multica-intake init --yes"}
			}
			return doctor.Result{Status: doctor.OK, Detail: s.file}
		},
		Apply: func(_ context.Context, dry bool) (string, error) {
			if s.exists || s.loadErr != nil {
				return "", nil
			}
			c := config.Default()
			if s.pending != nil {
				c = *s.pending
			} else {
				// keep env-provided values (MULTICA_INTAKE_*) in the new file
				c = s.cfg
			}
			if dry {
				return "would write " + s.file, nil
			}
			if err := c.Save(s.file); err != nil {
				return "", err
			}
			s.cfg, s.exists = c, true
			return "wrote " + s.file, nil
		},
	})

	cs = append(cs, doctor.Check{
		ID: "config_valid", Title: "Settings",
		Run: func(context.Context) doctor.Result {
			if s.loadErr != nil {
				return doctor.Result{Status: doctor.Skip, Detail: "config file unreadable"}
			}
			probs := s.cfg.Validate()
			if len(probs) == 0 {
				return doctor.Result{Status: doctor.OK, Detail: "intake agent: " + s.cfg.IntakeAgent}
			}
			fix := "multica-intake config explain"
			for _, p := range probs {
				if strings.HasPrefix(p, "intake_agent") {
					fix = `multica-intake config set intake_agent "<name of the Multica agent that receives issues>"`
				}
			}
			return doctor.Result{Status: doctor.Fail, Detail: strings.Join(probs, "; "), Fix: fix}
		},
	})

	cs = append(cs, doctor.Check{
		ID: "multica_api", Title: "Multica API",
		Run: func(ctx context.Context) doctor.Result {
			cl := multica.New(s.cfg.MulticaURL, "", 10*time.Second, "multica-intake/"+version.Get().Version)
			if err := cl.Health(ctx); err != nil {
				return doctor.Result{Status: doctor.Fail, Detail: s.cfg.MulticaURL + ": " + err.Error(),
					Fix: "multica-intake config set multica_url <the Multica API URL, e.g. http://127.0.0.1:8080>"}
			}
			return doctor.Result{Status: doctor.OK, Detail: s.cfg.MulticaURL}
		},
	})

	cs = append(cs, doctor.Check{
		ID: "listen", Title: "Listen address",
		Run: func(context.Context) doctor.Result {
			if s.cfg.LoopbackListen() {
				return doctor.Result{Status: doctor.OK, Detail: "http://" + s.cfg.Listen + "/mcp"}
			}
			return doctor.Result{Status: doctor.Warn, Detail: s.cfg.Listen + " is not loopback: anyone who can reach it can try tokens against Multica",
				Fix: "multica-intake config set listen 127.0.0.1:8801"}
		},
	})

	tf := s.tokenFile()
	cs = append(cs, doctor.Check{
		ID: "token_file", Title: "Token file (stdio mode)",
		Run: func(context.Context) doctor.Result {
			st, err := os.Stat(tf)
			if errors.Is(err, os.ErrNotExist) {
				return doctor.Result{Status: doctor.Skip, Detail: "none: only needed for `mcp` over stdio; HTTP callers send their own token"}
			}
			if err != nil {
				return doctor.Result{Status: doctor.Fail, Detail: err.Error()}
			}
			if st.Mode().Perm()&0o077 != 0 {
				return doctor.Result{Status: doctor.Fail, Detail: fmt.Sprintf("%s has mode %o; it must be 0600", tf, st.Mode().Perm()), Fix: "chmod 600 " + quote(tf)}
			}
			return doctor.Result{Status: doctor.OK, Detail: tf}
		},
		Apply: func(_ context.Context, dry bool) (string, error) {
			st, err := os.Stat(tf)
			if err != nil || st.Mode().Perm()&0o077 == 0 {
				return "", nil
			}
			if dry {
				return "would chmod 600 " + tf, nil
			}
			return "chmod 600 " + tf, os.Chmod(tf, 0o600)
		},
	})

	cs = append(cs, doctor.Check{
		ID: "account", Title: "Account and intake agents (stdio token)",
		Run: func(ctx context.Context) doctor.Result {
			tok, err := readToken(tf)
			if errors.Is(err, os.ErrNotExist) {
				return doctor.Result{Status: doctor.Skip, Detail: "no token file, so the intake agent is not checked here; each HTTP caller is checked when it calls"}
			}
			if err != nil {
				return doctor.Result{Status: doctor.Fail, Detail: err.Error(), Fix: "chmod 600 " + quote(tf)}
			}
			cl := multica.New(s.cfg.MulticaURL, tok, 10*time.Second, "multica-intake/"+version.Get().Version)
			me, err := cl.Me(ctx)
			if err != nil {
				if multica.IsStatus(err, 401) {
					return doctor.Result{Status: doctor.Fail, Detail: "Multica refused the token in " + tf + " (expired or revoked)",
						Fix: "create a new personal access token in Multica (Settings → Tokens) and write it to " + tf}
				}
				return doctor.Result{Status: doctor.Fail, Detail: err.Error()}
			}
			ws, err := cl.Workspaces(ctx)
			if err != nil {
				return doctor.Result{Status: doctor.Fail, Detail: err.Error()}
			}
			allow := map[string]bool{}
			for _, w := range s.cfg.Workspaces {
				allow[strings.ToLower(w)] = true
			}
			var okWs, missing []string
			for _, w := range ws {
				if len(allow) > 0 && !allow[strings.ToLower(w.Slug)] {
					continue
				}
				agents, err := cl.Agents(ctx, w.ID)
				if err != nil {
					return doctor.Result{Status: doctor.Fail, Detail: err.Error()}
				}
				found := false
				for _, a := range agents {
					if strings.EqualFold(strings.TrimSpace(a.Name), strings.TrimSpace(s.cfg.AgentFor(w.Slug))) {
						found = true
					}
				}
				if found {
					okWs = append(okWs, w.Slug)
				} else {
					missing = append(missing, w.Slug)
				}
			}
			if len(okWs) == 0 {
				return doctor.Result{Status: doctor.Fail, Detail: me.Name + " can submit nowhere: no allowed workspace has the intake agent " + quote(s.cfg.IntakeAgent),
					Fix: `multica-intake config set intake_agent "<agent name>"`}
			}
			if len(missing) > 0 {
				return doctor.Result{Status: doctor.Warn, Detail: fmt.Sprintf("%s: ok in %s; no intake agent in %s", me.Name, strings.Join(okWs, ", "), strings.Join(missing, ", ")),
					Fix: fmt.Sprintf(`multica-intake config set intake_agents.%s "<agent name>"`, missing[0])}
			}
			return doctor.Result{Status: doctor.OK, Detail: me.Name + " → " + strings.Join(okWs, ", ")}
		},
	})

	if s.p.UnitFile != "" {
		cs = append(cs, doctor.Check{
			ID: "service", Title: "User service",
			Run: func(context.Context) doctor.Result {
				exists, owned := service.Owned(s.p.UnitFile)
				switch {
				case !exists:
					return doctor.Result{Status: doctor.Skip, Detail: "not installed (optional): multica-intake service install"}
				case !owned:
					return doctor.Result{Status: doctor.Warn, Detail: s.p.UnitFile + " was not written by multica-intake; left alone"}
				case !service.Active():
					return doctor.Result{Status: doctor.Fail, Detail: "installed but not running", Fix: "multica-intake service restart"}
				}
				return doctor.Result{Status: doctor.OK, Detail: "running"}
			},
			Apply: func(_ context.Context, dry bool) (string, error) {
				exists, owned := service.Owned(s.p.UnitFile)
				if !exists || !owned || service.Active() {
					return "", nil
				}
				if dry {
					return "would restart the user service", nil
				}
				if out, err := service.Systemctl(nil, "restart", "multica-intake"); err != nil {
					return "", errors.New(out)
				}
				return "restarted the user service", nil
			},
		})
	}
	return cs
}

// prompt asks one question on the terminal.
func (e *env) prompt(r *bufio.Reader, q, def string) string {
	if def != "" {
		fmt.Fprintf(e.stderr, "%s [%s]: ", q, def)
	} else {
		fmt.Fprintf(e.stderr, "%s: ", q)
	}
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}
