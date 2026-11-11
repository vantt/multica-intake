package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vantt/multica-intake/internal/audit"
	"github.com/vantt/multica-intake/internal/config"
	"github.com/vantt/multica-intake/internal/doctor"
	"github.com/vantt/multica-intake/internal/intake"
	"github.com/vantt/multica-intake/internal/mcpserver"
	"github.com/vantt/multica-intake/internal/selfupdate"
	"github.com/vantt/multica-intake/internal/service"
	"github.com/vantt/multica-intake/internal/version"
)

// ---------- version / paths ----------

func runVersion(e *env, args []string) int {
	if _, code, ok := e.flags("version", args, nil); !ok {
		return code
	}
	v := version.Get()
	if e.json {
		e.emit(true, v, nil, nil, nil)
		return ExitOK
	}
	fmt.Fprintln(e.stdout, v.String())
	return ExitOK
}

func runPaths(e *env, args []string) int {
	if _, code, ok := e.flags("paths", args, nil); !ok {
		return code
	}
	s, err := e.loadSetup()
	if err != nil {
		return e.fail(ExitProblem, "paths", err.Error(), "export HOME=<your home dir>")
	}
	p := s.p
	p.ConfigFile = s.file
	p.TokenFile = s.tokenFile()
	e.emit(true, p, nil, nil, func() {
		fmt.Fprintf(e.stdout, "binary      %s\nmarker      %s\nconfig_dir  %s\nconfig_file %s\ntoken_file  %s\nstate_dir   %s\n",
			p.Binary, p.Marker, p.ConfigDir, p.ConfigFile, p.TokenFile, p.StateDir)
		if p.UnitFile != "" {
			fmt.Fprintf(e.stdout, "unit_file   %s\n", p.UnitFile)
		}
	})
	return ExitOK
}

// ---------- init / doctor / status ----------

func runInit(ctx context.Context, e *env, args []string) int {
	if _, code, ok := e.flags("init", args, nil); !ok {
		return code
	}
	s, err := e.loadSetup()
	if err != nil {
		return e.fail(ExitProblem, "init", err.Error(), "export HOME=<your home dir>")
	}
	if !s.exists && s.loadErr == nil && !e.yes && !e.json && stdinIsTTY(e) {
		r := bufio.NewReader(e.stdin)
		c := s.cfg
		c.MulticaURL = strings.TrimRight(e.prompt(r, "Multica API URL", c.MulticaURL), "/")
		c.IntakeAgent = e.prompt(r, "Agent that receives submitted issues (its name in Multica)", c.IntakeAgent)
		c.Source = e.prompt(r, "Source tag written on each issue (e.g. the chat app's name)", c.Source)
		s.cfg, s.pending = c, &c
	}
	rep := doctor.Run(ctx, s.checks(), doctor.Options{Fix: true, Creates: true, DryRun: e.dryRun})
	return e.report("init", rep, s)
}

func runDoctor(ctx context.Context, e *env, args []string) int {
	var fix, strict bool
	if _, code, ok := e.flags("doctor", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&fix, "fix", false, "")
		fs.BoolVar(&strict, "strict", false, "")
	}); !ok {
		return code
	}
	s, err := e.loadSetup()
	if err != nil {
		return e.fail(ExitProblem, "doctor", err.Error(), "export HOME=<your home dir>")
	}
	rep := doctor.Run(ctx, s.checks(), doctor.Options{Fix: fix, DryRun: e.dryRun, Strict: strict})
	return e.report("doctor", rep, s)
}

func (e *env) report(verb string, rep doctor.Report, s *setup) int {
	var next, later []string
	for _, c := range rep.Checks {
		if (c.Status == doctor.Fail || c.Status == doctor.Warn) && c.Fix != "" {
			// `config set` also creates the file, so settings come before `init`.
			if strings.HasPrefix(c.Fix, "multica-intake init") {
				later = append(later, c.Fix)
			} else {
				next = append(next, c.Fix)
			}
		}
	}
	next = append(next, later...)
	if rep.OK {
		if !s.exists || len(s.cfg.Validate()) > 0 {
			next = append(next, "multica-intake init --yes")
		} else if verb == "init" {
			next = append(next, "multica-intake service install   # or: multica-intake mcp --http")
		}
	} else if len(later) == 0 {
		next = append(next, "multica-intake "+verb)
	}
	var errOut *ErrorOut
	if !rep.OK {
		errOut = &ErrorOut{Code: "not_healthy", Why: "some checks failed", Fix: firstOr(next, "multica-intake doctor")}
	}
	e.emit(rep.OK, rep, errOut, next, func() {
		for _, c := range rep.Checks {
			mark := map[doctor.Status]string{doctor.OK: e.paint("32", "✓"), doctor.Warn: e.paint("33", "!"),
				doctor.Fail: e.paint("31", "✗"), doctor.Skip: e.paint("90", "-")}[c.Status]
			fixed := ""
			if c.Fixed {
				fixed = " (fixed)"
			}
			fmt.Fprintf(e.stderr, "%s %-40s %s%s\n", mark, c.Title, c.Detail, fixed)
		}
		for _, ch := range rep.Changes {
			fmt.Fprintln(e.stderr, "  changed: "+ch)
		}
		if rep.OK && verb == "init" {
			fmt.Fprintln(e.stderr, e.paint("32", "\n✓ Ready.")+" Try it: multica-intake mcp --http, then point your bot at http://"+s.cfg.Listen+"/mcp")
		}
	})
	if rep.OK {
		return ExitOK
	}
	return ExitProblem
}

func firstOr(s []string, d string) string {
	if len(s) > 0 {
		return s[0]
	}
	return d
}

func runStatus(ctx context.Context, e *env, args []string) int {
	if _, code, ok := e.flags("status", args, nil); !ok {
		return code
	}
	s, err := e.loadSetup()
	if err != nil {
		return e.fail(ExitProblem, "status", err.Error(), "export HOME=<your home dir>")
	}
	var fast []doctor.Check
	for _, c := range s.checks() {
		if c.ID == "config_file" || c.ID == "config_valid" || c.ID == "multica_api" || c.ID == "service" {
			fast = append(fast, c)
		}
	}
	rep := doctor.Run(ctx, fast, doctor.Options{})
	next := "multica-intake doctor"
	if rep.OK {
		next = "multica-intake mcp --http"
		if exists, owned := service.Owned(s.p.UnitFile); exists && owned {
			next = "multica-intake service logs"
		}
	}
	data := map[string]any{"version": version.Get().Version, "healthy": rep.OK, "checks": rep.Checks}
	var errOut *ErrorOut
	if !rep.OK {
		errOut = &ErrorOut{Code: "not_healthy", Why: "multica-intake is not ready", Fix: next}
	}
	e.emit(rep.OK, data, errOut, []string{next}, func() {
		state := e.paint("32", "healthy")
		if !rep.OK {
			state = e.paint("31", "not healthy")
		}
		fmt.Fprintf(e.stdout, "multica-intake %s: %s\n", version.Get().Version, state)
	})
	if rep.OK {
		return ExitOK
	}
	return ExitProblem
}

// ---------- config ----------

func runConfig(e *env, args []string) int {
	pos, code, ok := e.flags("config", args, nil)
	if !ok {
		return code
	}
	if len(pos) == 0 {
		h, _ := helpFor("config")
		fmt.Fprint(e.stdout, h)
		return ExitUsage
	}
	s, err := e.loadSetup()
	if err != nil {
		return e.fail(ExitProblem, "config", err.Error(), "export HOME=<your home dir>")
	}
	sub, rest := pos[0], pos[1:]
	need := func(n int, usage string) bool {
		if len(rest) != n {
			e.usage("usage: multica-intake config "+usage, "multica-intake config --help")
			return false
		}
		return true
	}
	switch sub {
	case "path":
		e.emit(true, map[string]any{"file": s.file, "exists": s.exists}, nil, nil, func() { fmt.Fprintln(e.stdout, s.file) })
		return ExitOK
	case "default":
		b, _ := config.Default().Marshal()
		e.emit(true, config.Default(), nil, nil, func() { e.stdout.Write(b) })
		return ExitOK
	case "explain":
		var keys []config.Key
		for _, k := range config.Keys {
			if len(rest) == 0 || rest[0] == k.Name || strings.HasPrefix(rest[0], k.Name+".") {
				keys = append(keys, k)
			}
		}
		if len(keys) == 0 {
			return e.usage(fmt.Sprintf("unknown key %q", rest[0]), "multica-intake config explain")
		}
		e.emit(true, keys, nil, nil, func() {
			for _, k := range keys {
				fmt.Fprintf(e.stdout, "%s (default: %s)\n    %s\n", k.Name, orDash(k.Default), k.Help)
			}
		})
		return ExitOK
	}
	if s.loadErr != nil {
		return e.fail(ExitProblem, "config_invalid", s.loadErr.Error(), "multica-intake config edit")
	}
	switch sub {
	case "show":
		e.emit(true, s.cfg, nil, nil, func() {
			fmt.Fprintf(e.stdout, "# %s%s\n", s.file, map[bool]string{false: " (not created yet: defaults)", true: ""}[s.exists])
			for _, k := range config.Keys {
				v, _ := s.cfg.Get(k.Name)
				src := "file"
				if _, envSet := os.LookupEnv("MULTICA_INTAKE_" + strings.ToUpper(k.Name)); envSet {
					src = "env"
				} else if d, _ := config.Default().Get(k.Name); d == v {
					src = "default"
				}
				fmt.Fprintf(e.stdout, "%-14s %-40s (%s)\n", k.Name, orDash(v), src)
			}
		})
		return ExitOK
	case "get":
		if !need(1, "get <key>") {
			return ExitUsage
		}
		v, err := s.cfg.Get(rest[0])
		if err != nil {
			return e.usage(err.Error(), "multica-intake config explain")
		}
		e.emit(true, map[string]string{"key": rest[0], "value": v}, nil, nil, func() { fmt.Fprintln(e.stdout, v) })
		return ExitOK
	case "set", "unset":
		var err error
		if sub == "set" {
			if !need(2, "set <key> <value>") {
				return ExitUsage
			}
			err = s.cfg.Set(rest[0], rest[1])
		} else {
			if !need(1, "unset <key>") {
				return ExitUsage
			}
			err = s.cfg.Unset(rest[0])
		}
		if err != nil {
			return e.usage(err.Error(), "multica-intake config explain")
		}
		if p := s.cfg.Validate(); len(p) > 0 && !(len(p) == 1 && strings.HasPrefix(p[0], "intake_agent")) {
			return e.fail(ExitProblem, "invalid_value", strings.Join(p, "; "), "multica-intake config explain "+rest[0])
		}
		if e.dryRun {
			e.info("would write %s", s.file)
			e.emit(true, map[string]any{"file": s.file, "changed": false}, nil, nil, nil)
			return ExitOK
		}
		if err := s.cfg.Save(s.file); err != nil {
			return e.fail(ExitProblem, "write_failed", err.Error(), "check the permissions of "+filepath.Dir(s.file))
		}
		e.info("%s %s in %s", map[string]string{"set": "set", "unset": "reset"}[sub], rest[0], s.file)
		e.emit(true, map[string]any{"file": s.file, "changed": true}, nil, []string{restartHint(s)}, nil)
		return ExitOK
	case "check":
		p := s.cfg.Validate()
		if len(p) == 0 {
			e.emit(true, map[string]any{"file": s.file}, nil, nil, func() { fmt.Fprintln(e.stdout, "ok: "+s.file) })
			return ExitOK
		}
		return e.fail(ExitProblem, "config_invalid", strings.Join(p, "; "), "multica-intake config explain")
	case "edit":
		ed := os.Getenv("VISUAL")
		if ed == "" {
			ed = os.Getenv("EDITOR")
		}
		if ed == "" {
			ed = "vi"
		}
		if !s.exists {
			if err := s.cfg.Save(s.file); err != nil {
				return e.fail(ExitProblem, "write_failed", err.Error(), "")
			}
		}
		cmd := exec.Command("sh", "-c", ed+` "$1"`, "sh", s.file)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return e.fail(ExitProblem, "editor_failed", err.Error(), "export EDITOR=nano")
		}
		c, _, err := config.Load(s.file)
		if err != nil {
			return e.fail(ExitProblem, "config_invalid", err.Error(), "multica-intake config edit")
		}
		if p := c.Validate(); len(p) > 0 {
			return e.fail(ExitProblem, "config_invalid", strings.Join(p, "; "), "multica-intake config edit")
		}
		e.info("ok: %s", s.file)
		return ExitOK
	}
	return e.usage(fmt.Sprintf("unknown config subcommand %q", sub), "multica-intake config --help")
}

func restartHint(s *setup) string {
	if exists, owned := service.Owned(s.p.UnitFile); exists && owned {
		return "multica-intake service restart"
	}
	return "multica-intake doctor"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// ---------- mcp ----------

func runMCP(ctx context.Context, e *env, args []string) int {
	var httpMode bool
	pos, code, ok := e.flags("mcp", args, func(fs *flag.FlagSet) { fs.BoolVar(&httpMode, "http", false, "") })
	if !ok {
		return code
	}
	s, err := e.loadSetup()
	if err != nil {
		return e.fail(ExitProblem, "mcp", err.Error(), "")
	}
	if s.loadErr != nil {
		return e.fail(ExitProblem, "config_invalid", s.loadErr.Error(), "multica-intake config edit")
	}
	if len(pos) > 0 {
		if !httpMode {
			return e.usage("an address is only taken with --http", "multica-intake mcp --http "+pos[0])
		}
		s.cfg.Listen = pos[0]
	}
	if p := s.cfg.Validate(); len(p) > 0 {
		return e.fail(ExitProblem, "config_invalid", strings.Join(p, "; "), "multica-intake doctor")
	}
	svc := intake.New(s.cfg, audit.New(e.stderr))
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if !httpMode {
		tok, err := readToken(s.tokenFile())
		if err != nil {
			return e.fail(ExitProblem, "no_token", "stdio mode needs a Multica personal access token: "+err.Error(),
				"write the bot account's token to "+quote(s.tokenFile())+" (chmod 600), or run: multica-intake mcp --http")
		}
		c, err := svc.Authenticate(ctx, tok)
		if err != nil {
			return e.fail(ExitProblem, "unauthorized", err.Error(), "multica-intake doctor")
		}
		server := mcpserver.New(svc, mcpserver.Static(c))
		if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintln(e.stderr, "mcp: "+err.Error())
			return ExitProblem
		}
		return ExitOK
	}

	if !s.cfg.LoopbackListen() {
		fmt.Fprintf(e.stderr, "warning: listening on %s, not loopback: anyone who reaches it can try tokens against Multica\n", s.cfg.Listen)
	}
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return e.fail(ExitProblem, "listen_failed", err.Error(), "multica-intake config set listen 127.0.0.1:<free port>")
	}
	srv := &http.Server{Handler: mcpserver.Handler(svc, 1<<20), ReadHeaderTimeout: 10 * time.Second,
		ErrorLog: slog.NewLogLogger(slog.NewTextHandler(e.stderr, nil), slog.LevelWarn)}
	fmt.Fprintf(e.stderr, "%s serving MCP at http://%s/mcp (Multica: %s, intake agent: %s, source: %s)\n",
		version.Get().String(), ln.Addr(), s.cfg.MulticaURL, s.cfg.IntakeAgent, s.cfg.Source)
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sh)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(e.stderr, "mcp: "+err.Error())
		return ExitProblem
	}
	return ExitOK
}

// ---------- service ----------

func runService(e *env, args []string) int {
	pos, code, ok := e.flags("service", args, nil)
	if !ok {
		return code
	}
	if len(pos) != 1 {
		return e.usage("usage: multica-intake service install|uninstall|start|stop|restart|status|logs", "multica-intake service --help")
	}
	if err := service.Supported(); err != nil {
		return e.fail(ExitProblem, "unsupported", err.Error(), "multica-intake mcp --http")
	}
	s, err := e.loadSetup()
	if err != nil {
		return e.fail(ExitProblem, "service", err.Error(), "")
	}
	unit := s.p.UnitFile
	exists, owned := service.Owned(unit)
	echo := e.stderr
	if e.json {
		echo = nil
	}
	run := func(a ...string) int {
		out, err := service.Systemctl(echo, a...)
		if err != nil {
			return e.fail(ExitProblem, "systemctl_failed", out, "multica-intake service logs")
		}
		if out != "" && !e.json {
			fmt.Fprintln(e.stdout, out)
		}
		return ExitOK
	}
	switch pos[0] {
	case "install":
		if exists && !owned {
			return e.fail(ExitProblem, "not_ours", unit+" exists and was not written by multica-intake", "remove it yourself, then: multica-intake service install")
		}
		if p := s.cfg.Validate(); len(p) > 0 {
			return e.fail(ExitProblem, "config_invalid", strings.Join(p, "; "), "multica-intake init")
		}
		if e.dryRun {
			e.info("would write %s and enable it", unit)
			return ExitOK
		}
		if err := config.WriteAtomic(unit, []byte(service.Unit(s.p.Binary)), 0o644); err != nil {
			return e.fail(ExitProblem, "write_failed", err.Error(), "")
		}
		e.info("wrote %s", unit)
		if c := run("daemon-reload"); c != 0 {
			return c
		}
		if c := run("enable", "--now", "multica-intake"); c != 0 {
			return c
		}
		if !service.Active() {
			_, _ = service.Systemctl(echo, "restart", "multica-intake")
		}
		e.emit(true, map[string]any{"unit": unit, "active": service.Active()}, nil, []string{"multica-intake doctor"}, func() {
			fmt.Fprintf(e.stderr, "✓ Running at http://%s/mcp\n", s.cfg.Listen)
		})
		return ExitOK
	case "uninstall":
		if !exists {
			e.info("not installed")
			return ExitOK
		}
		if !owned {
			return e.fail(ExitProblem, "not_ours", unit+" was not written by multica-intake; left alone", "")
		}
		if e.dryRun {
			e.info("would stop, disable and remove %s", unit)
			return ExitOK
		}
		_, _ = service.Systemctl(echo, "disable", "--now", "multica-intake")
		if err := os.Remove(unit); err != nil {
			return e.fail(ExitProblem, "remove_failed", err.Error(), "")
		}
		_, _ = service.Systemctl(echo, "daemon-reload")
		e.info("removed %s", unit)
		return ExitOK
	case "start", "stop", "restart", "status":
		if !exists {
			return e.fail(ExitProblem, "not_installed", "the user service is not installed", "multica-intake service install")
		}
		return run(pos[0], "multica-intake")
	case "logs":
		if !e.json {
			fmt.Fprintln(e.stderr, "$ journalctl --user -u multica-intake -n 100 --no-pager")
		}
		out, _ := exec.Command("journalctl", "--user", "-u", "multica-intake", "-n", "100", "--no-pager").CombinedOutput()
		e.stdout.Write(out)
		return ExitOK
	}
	return e.usage(fmt.Sprintf("unknown service action %q", pos[0]), "multica-intake service --help")
}

// ---------- update ----------

func runUpdate(ctx context.Context, e *env, args []string) int {
	var check, rollback, force bool
	var want string
	if _, code, ok := e.flags("update", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&check, "check", false, "")
		fs.BoolVar(&rollback, "rollback", false, "")
		fs.BoolVar(&force, "force", false, "")
		fs.StringVar(&want, "version", "", "")
	}); !ok {
		return code
	}
	s, err := e.loadSetup()
	if err != nil {
		return e.fail(ExitProblem, "update", err.Error(), "")
	}
	bin := s.p.Binary
	if rollback {
		if err := selfupdate.Rollback(bin); err != nil {
			return e.fail(ExitProblem, "no_rollback", err.Error(), "multica-intake update --version <vX.Y.Z> --yes")
		}
		e.emit(true, map[string]any{"rolled_back": true}, nil, []string{"multica-intake --version"}, func() { fmt.Fprintln(e.stderr, "✓ previous binary restored") })
		return ExitOK
	}
	cur := version.Get().Version
	plan, err := selfupdate.Prepare(ctx, cur, want, bin)
	if err != nil {
		return e.fail(ExitProblem, "update_check_failed", err.Error(), "check the network, then: multica-intake update --check")
	}
	if check {
		if plan.Needed {
			e.emit(true, plan, nil, []string{"multica-intake update --yes"}, func() { fmt.Fprintf(e.stdout, "update available: %s → %s\n", cur, plan.Target) })
			return ExitUpdate
		}
		e.emit(true, plan, nil, nil, func() { fmt.Fprintf(e.stdout, "up to date (%s)\n", cur) })
		return ExitOK
	}
	if !plan.Needed {
		e.emit(true, plan, nil, nil, func() { fmt.Fprintf(e.stdout, "already at %s\n", cur) })
		return ExitOK
	}
	if _, err := os.Stat(s.p.Marker); err != nil && !force {
		return e.fail(ExitProblem, "not_managed", bin+" was not placed by the installer (no "+filepath.Base(s.p.Marker)+"); it may belong to a package manager or a container image",
			"multica-intake update --force --yes   # or reinstall: curl -fsSL https://github.com/vantt/multica-intake/releases/latest/download/install.sh | sh")
	}
	if !e.yes {
		if !stdinIsTTY(e) {
			return e.usage("update needs confirmation; there is no terminal to ask on", "multica-intake update --yes")
		}
		fmt.Fprintf(e.stderr, "Update %s → %s at %s? [y/N] ", cur, plan.Target, bin)
		ans, _ := bufio.NewReader(e.stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
			e.info("nothing changed")
			return ExitOK
		}
	}
	if e.dryRun {
		e.emit(true, plan, nil, nil, func() { fmt.Fprintf(e.stderr, "would update %s → %s\n", cur, plan.Target) })
		return ExitOK
	}
	if err := selfupdate.Apply(ctx, plan); err != nil {
		return e.fail(ExitProblem, "update_failed", err.Error(), "multica-intake update --yes   # try again; nothing was changed")
	}
	_ = os.WriteFile(s.p.Marker, []byte("multica-intake-managed-v1\nversion="+plan.Target+"\n"), 0o600)
	if exists, owned := service.Owned(s.p.UnitFile); exists && owned && service.Active() {
		_, _ = service.Systemctl(nil, "restart", "multica-intake")
	}
	e.emit(true, plan, nil, []string{"multica-intake doctor"}, func() { fmt.Fprintf(e.stderr, "✓ updated %s → %s\n", cur, plan.Target) })
	return ExitOK
}

// ---------- uninstall ----------

func runUninstall(e *env, args []string) int {
	var purge bool
	if _, code, ok := e.flags("uninstall", args, func(fs *flag.FlagSet) { fs.BoolVar(&purge, "purge", false, "") }); !ok {
		return code
	}
	s, err := e.loadSetup()
	if err != nil {
		return e.fail(ExitProblem, "uninstall", err.Error(), "")
	}
	var plan []string
	exists, owned := service.Owned(s.p.UnitFile)
	if exists && owned {
		plan = append(plan, "stop and remove the user service "+s.p.UnitFile)
	}
	_, markerErr := os.Stat(s.p.Marker)
	managed := markerErr == nil
	if managed {
		plan = append(plan, "remove "+s.p.Binary+" (+ .rollback, marker)")
	}
	if purge {
		plan = append(plan, "remove "+s.p.ConfigDir+" (config and token file)", "remove "+s.p.StateDir)
	}
	if len(plan) == 0 {
		e.emit(true, map[string]any{"removed": []string{}}, nil, nil, func() {
			fmt.Fprintln(e.stderr, "nothing to remove (the binary has no installer marker; delete it yourself: rm "+quote(s.p.Binary)+")")
		})
		return ExitOK
	}
	if e.dryRun {
		e.emit(true, map[string]any{"would_remove": plan}, nil, nil, func() {
			for _, p := range plan {
				fmt.Fprintln(e.stderr, "would "+p)
			}
		})
		return ExitOK
	}
	if !e.yes {
		if !stdinIsTTY(e) {
			return e.usage("uninstall needs confirmation; there is no terminal to ask on", "multica-intake uninstall --yes")
		}
		for _, p := range plan {
			fmt.Fprintln(e.stderr, "will "+p)
		}
		fmt.Fprint(e.stderr, "Continue? [y/N] ")
		ans, _ := bufio.NewReader(e.stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
			e.info("nothing changed")
			return ExitOK
		}
	}
	if !purge {
		e.info("keeping %s (remove it too with: multica-intake uninstall --purge --yes)", s.p.ConfigDir)
	}
	if exists && owned && runtime.GOOS == "linux" {
		_, _ = service.Systemctl(nil, "disable", "--now", "multica-intake")
		_ = os.Remove(s.p.UnitFile)
		_, _ = service.Systemctl(nil, "daemon-reload")
	}
	if purge {
		_ = os.RemoveAll(s.p.ConfigDir)
		_ = os.RemoveAll(s.p.StateDir)
	}
	if managed {
		_ = os.Remove(s.p.Binary + ".rollback")
		_ = os.Remove(s.p.Marker)
		if err := os.Remove(s.p.Binary); err != nil {
			return e.fail(ExitProblem, "remove_failed", err.Error(), "rm "+quote(s.p.Binary))
		}
	}
	e.emit(true, map[string]any{"removed": plan}, nil, nil, func() { fmt.Fprintln(e.stderr, "✓ removed") })
	return ExitOK
}

// ---------- completions ----------

func runCompletions(e *env, args []string) int {
	pos, code, ok := e.flags("completions", args, nil)
	if !ok {
		return code
	}
	if len(pos) != 1 {
		return e.usage("usage: multica-intake completions bash|zsh|fish", "multica-intake completions bash")
	}
	cmds := commandList()
	switch pos[0] {
	case "bash":
		fmt.Fprintf(e.stdout, "complete -W %q multica-intake\n", cmds+" --json --yes --dry-run --help")
	case "zsh":
		fmt.Fprintf(e.stdout, "#compdef multica-intake\n_arguments '1: :(%s)' '*: :_files'\n", cmds)
	case "fish":
		for _, c := range commands {
			fmt.Fprintf(e.stdout, "complete -c multica-intake -n __fish_use_subcommand -a %s\n", c)
		}
	default:
		return e.usage(fmt.Sprintf("unknown shell %q", pos[0]), "multica-intake completions bash|zsh|fish")
	}
	return ExitOK
}

// runHealthcheck asks the local server's /healthz (used by the container image,
// which has no curl).
func runHealthcheck(ctx context.Context, e *env) int {
	s, err := e.loadSetup()
	if err != nil {
		return ExitProblem
	}
	host, port, err := net.SplitHostPort(s.cfg.Listen)
	if err != nil {
		return ExitProblem
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(e.stderr, "unhealthy: "+err.Error())
		return ExitProblem
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return ExitProblem
	}
	return ExitOK
}
