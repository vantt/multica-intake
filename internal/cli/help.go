package cli

import "strings"

const topHelp = `multica-intake — let a chat bot hand work to a Multica team, as suggestions only.

An MCP server: a bot sends a request, it becomes a Multica issue with no project and
no Type, assigned to the workspace's intake agent, which classifies it. The bot can
follow its own issues and answer questions; it can change nothing else.

Get started:
  1. multica-intake init                          create the config, check Multica
  2. multica-intake config set intake_agent "<agent name>"   who receives the issues
  3. multica-intake service install               run it at 127.0.0.1:8801/mcp
     (or: multica-intake mcp --http)              run it in the foreground
  Then add the MCP server to your bot: URL http://127.0.0.1:8801/mcp,
  header "Authorization: Bearer <the bot account's Multica personal access token>".

Commands:
  init          first run: create the config and check everything      (init --help)
  doctor        find what is wrong; --fix repairs it                    (doctor --help)
  status        one-line health and the next command
  mcp           serve MCP: --http for bots (each sends its own token), stdio by default
  service       install|uninstall|start|stop|restart|status|logs the user service (Linux)
  config        path|show|get|set|unset|default|explain|check|edit      (config --help)
  update        install a newer release (alias: upgrade)                (update --help)
  paths         where the binary, config and service live
  uninstall     remove the service and binary; --purge also the config  (uninstall --help)
  completions   bash|zsh|fish completion script
  version       print the version (--json: full fingerprint)

Global flags (every command): --json  -y/--yes  --dry-run  -q/--quiet  -v/--verbose
  --no-color (or NO_COLOR=1)  -c/--config <file>

Exit codes: 0 ok · 1 problems found or failed · 2 usage error · 10 update available (update --check)

Agents / automation:
  multica-intake init --yes --json
  while ! multica-intake doctor --json; do run each "fix" and "next" command it prints; done
  Every --json output is {"schema":1,"ok":…,"data":…,"error":{"code","why","fix"}|null,"next":[…]}.
  ` + "`multica-intake help --json`" + ` lists the commands and flags.
`

var cmdHelp = map[string]string{
	"init": `multica-intake init — first run: create the config and check that everything works.

What it does:
  Runs every doctor check and repairs what it can, including the ones that create
  things: writes the config file with defaults (<config_dir>/config.yaml, 0600) when
  there is none. Re-running it is always safe: it changes nothing that is already right.
  It never installs the service (that is ` + "`service install`" + `) and never asks for a token.

Prerequisites: a reachable Multica API (multica_url) and an agent there to receive issues.

Flags:
  --yes        never prompt (default: prompt on a terminal for multica_url and intake_agent)
  --dry-run    show what would change, change nothing
  --json       JSON envelope on stdout
  -c FILE      use this config file instead of <config_dir>/config.yaml

Examples:
  multica-intake init
  multica-intake init --yes --json
  MULTICA_INTAKE_MULTICA_URL=http://127.0.0.1:8310 multica-intake init --yes
  multica-intake init --dry-run

Exit codes: 0 ready · 1 something still needs you (see "next") · 2 usage error
Next: multica-intake service install   (or multica-intake mcp --http)
`,
	"doctor": `multica-intake doctor — find what is wrong, and how to fix it.

What it does:
  Read-only by default: checks the config, that Multica answers, the token file (stdio
  mode) and the account behind it, the intake agent in each workspace, the listen
  address, and the user service when it is installed. With --fix it repairs drift
  (file permissions, a stopped service) and re-checks, up to 3 rounds. It never
  creates a config: that is init.

Flags:
  --fix        repair what can be repaired, then re-check
  --dry-run    with --fix: show the repairs, change nothing
  --strict     warnings count as problems (exit 1)
  --json       JSON envelope on stdout; each problem carries a runnable "fix"

Examples:
  multica-intake doctor
  multica-intake doctor --json
  multica-intake doctor --fix --yes

Exit codes: 0 healthy (after fixes) · 1 problems remain · 2 usage error
Next: run each printed fix command, then doctor again.
`,
	"config": `multica-intake config — the one door for every setting.

  config path                 the config file in use
  config show                 every key with its value and where it comes from
  config get <key>
  config set <key> <value>    lists are comma-separated; intake_agents.<slug> sets one workspace
  config unset <key>          back to the default
  config default              the built-in defaults, as a file
  config explain [key]        what each key means
  config check                validate the file (exit 1 when invalid)
  config edit                 open the file in $EDITOR, validate after

Precedence: flag > env MULTICA_INTAKE_<KEY> > config file > default.
Secrets never go in the config: callers send their own token; stdio mode reads token_file.

Examples:
  multica-intake config set multica_url http://127.0.0.1:8310
  multica-intake config set intake_agent "Triage"
  multica-intake config set intake_agents.sales "Mai"
  multica-intake config set workspaces sales,support
  multica-intake config show --json

Exit codes: 0 ok · 1 invalid value or file · 2 usage error
Next: multica-intake doctor
`,
	"update": `multica-intake update — install a newer release (alias: upgrade).

What it does:
  Resolves the latest release (or --version), downloads the archive and checksums.txt
  over HTTPS, verifies the SHA-256 of the exact file, smoke-tests the new binary, keeps
  the old one as <binary>.rollback, then swaps it in. If the user service runs, it is
  restarted. Refuses to replace a binary the installer did not place (no
  .multica-intake-managed marker) unless --force.

Flags:
  --check            only report whether an update exists (exit 10 when it does)
  --version vX.Y.Z   install this version (up or down)
  --rollback         put back the previous binary
  --force            replace a binary without the installer's marker
  --yes              do not ask for confirmation
  --json

Examples:
  multica-intake update --check
  multica-intake update --yes
  multica-intake update --version v0.1.0 --yes

Exit codes: 0 up to date or updated · 1 failed (nothing changed) · 2 usage · 10 update available (--check)
Next: multica-intake doctor
`,
	"uninstall": `multica-intake uninstall — remove what this tool installed (alias: remove).

What it does:
  Stops and removes the user service (only a unit carrying our marker), then removes the
  binary (only with the installer's .multica-intake-managed marker), its .rollback and
  the marker. Keeps the config and token file unless --purge.

Flags:
  --purge      also remove the config dir and state dir (the token file goes with it)
  --dry-run    list what would be removed
  --yes        required without a terminal

Examples:
  multica-intake uninstall --dry-run
  multica-intake uninstall --yes
  multica-intake uninstall --purge --yes

Exit codes: 0 removed · 1 failed · 2 usage error
`,
	"mcp": `multica-intake mcp — serve the MCP tools.

  mcp                  stdio, one account: the Multica PAT is read from token_file (0600)
  mcp --http [ADDR]    Streamable HTTP at http://ADDR/mcp (default: config listen,
                       127.0.0.1:8801); every request must carry its caller's own
                       "Authorization: Bearer <Multica PAT>". GET /healthz is open.

Tools: list_workspaces, submit_issue, get_issue_status, reply_to_issue, list_my_issues.
Stdout carries only the protocol; logs and the audit trail go to stderr.

Examples:
  multica-intake mcp --http
  multica-intake mcp --http 127.0.0.1:9900
  claude mcp add multica-intake -- multica-intake mcp

Exit codes: 0 stopped cleanly · 1 failed to start · 2 usage error
`,
	"service": `multica-intake service — the systemd user service that runs ` + "`mcp --http`" + ` (Linux).

  service install     write ~/.config/systemd/user/multica-intake.service, enable and start it
  service uninstall   stop, disable and remove it (only a unit this tool wrote)
  service start|stop|restart|status|logs

Each action prints the systemctl/journalctl command it runs.
Examples:
  multica-intake service install
  multica-intake service logs
Exit codes: 0 ok · 1 failed · 2 usage error
Next: multica-intake doctor
`,
}

func helpFor(cmd string) (string, bool) {
	h, ok := cmdHelp[cmd]
	if !ok {
		switch cmd {
		case "status":
			return "multica-intake status — fast health summary and exactly one next command.\n\nFlags: --json\nExit codes: 0 healthy · 1 not healthy\n", true
		case "paths":
			return "multica-intake paths — where the binary, config, state and service live (the single source for installers).\n\nFlags: --json\n", true
		case "version":
			return "multica-intake version — print the version; --json gives the full fingerprint (version, commit, date, dirty, go, platform).\n", true
		case "completions":
			return "multica-intake completions <bash|zsh|fish> — print a completion script.\n\nExample: multica-intake completions bash > ~/.local/share/bash-completion/completions/multica-intake\n", true
		}
		return "", false
	}
	return h, true
}

var commands = []string{"init", "doctor", "status", "mcp", "service", "config", "update", "paths", "uninstall", "completions", "version", "help"}

func commandList() string { return strings.Join(commands, " ") }
