# multica-intake

**Let your chat bots hand work to your [Multica](https://github.com/multica-ai/multica) team — without letting them run it.**

A bot sends a request; it lands in Multica as an issue with no project and no Type, assigned to
the workspace's intake agent, which classifies it. The bot can follow its own issues and answer
questions on them. It cannot edit, reassign, close or delete anything, and it never sees anyone
else's issues.

[![release](https://img.shields.io/github/v/release/vantt/multica-intake)](https://github.com/vantt/multica-intake/releases/latest)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
![single binary](https://img.shields.io/badge/single%20binary-no%20dependencies-success)
![MCP](https://img.shields.io/badge/MCP-Streamable%20HTTP%20%7C%20stdio-informational)

## Quick start

From nothing to a bot that can submit work in about 5 minutes. You need a Multica you can reach
(self-hosted or cloud) and an agent there that triages new issues. No other dependencies.

**1. Install** the single binary into `~/.local/bin`:

```sh
curl -fsSL https://github.com/vantt/multica-intake/releases/latest/download/install.sh | sh
```

<details><summary>Other ways: a version, a directory, Docker, from source</summary>

```sh
curl -fsSL https://github.com/vantt/multica-intake/releases/latest/download/install.sh | sh -s -- --version v0.1.0
curl -fsSL https://github.com/vantt/multica-intake/releases/latest/download/install.sh | sh -s -- --install-dir /opt/bin
docker run --rm -e MULTICA_INTAKE_MULTICA_URL=http://multica:8080 -e MULTICA_INTAKE_INTAKE_AGENT=Triage \
  -e MULTICA_INTAKE_LISTEN=0.0.0.0:8801 -p 127.0.0.1:8801:8801 ghcr.io/vantt/multica-intake:latest
go install github.com/vantt/multica-intake/cmd/multica-intake@latest
```
Linux and macOS, amd64 and arm64. Windows: build from source.
</details>

**2. Set it up** — point it at Multica and name the agent that receives issues:

```sh
multica-intake init
multica-intake config set intake_agent "Triage"
```

**3. Run it** as a user service at `http://127.0.0.1:8801/mcp`:

```sh
multica-intake service install
```

Then add it to your bot as an MCP server (URL type, Streamable HTTP):

| Field | Value |
|---|---|
| URL | `http://127.0.0.1:8801/mcp` |
| Header | `Authorization: Bearer <the bot's Multica personal access token>` |

Give each bot **its own Multica account** (a member of the workspaces it may send to) and put that
account's token in the header. Multica then records the bot as the author, and each bot only ever
sees its own issues. Ask your bot: *"List the Multica workspaces you can send work to."*

<details><summary>Install it with your agent</summary>

Paste this to your coding agent:

> Install multica-intake from https://github.com/vantt/multica-intake/releases/latest/download/install.sh,
> then follow `multica-intake init --help` and `multica-intake doctor --help` until `multica-intake doctor` passes.
> The Multica API is at <URL> and issues go to the agent named <NAME>.
</details>

## The five tools

| Tool | Read-only | What it does |
|---|---|---|
| `list_workspaces` | yes | Workspaces the bot may send to, with their projects and Types (for hints) |
| `submit_issue` | no | Create one issue: no project, no Type, assigned to the intake agent; the bot's project/Type guesses go in the description as suggestions. Optional `request_id` makes retries safe |
| `get_issue_status` | yes | State (`open`, `done`, `rejected`), the project and Type Multica chose, comments, `needs_reply` |
| `reply_to_issue` | no | Answer a question on an open issue; mentions the assigned agent so it wakes |
| `list_my_issues` | yes | The bot's own issues, newest first |

Every submitted issue is tagged `metadata.source = <source>`; a bot sees an issue only when its
own account created it **and** it carries that tag. Errors come back as
`{"error":{"code","why","fix"}}` so the bot can tell its human what to do.

## How it fits

```
chat bot ──► (your chat app's credential proxy) ──► multica-intake ──► Multica API
             holds the bot's PAT, adds the header      checks the rules     records the bot
                                                        stores nothing       as the author
```

multica-intake keeps no state and no secrets: each HTTP request carries its caller's token, which
it checks with Multica (`GET /api/me`) and uses for that call only. The design and the reasons
behind each rule are in [docs/PRD.md](docs/PRD.md) (Vietnamese).

## Commands

```
multica-intake init                first run: create the config, check everything
multica-intake doctor [--fix]      what is wrong, and the command that fixes it
multica-intake status              one line, one next command
multica-intake mcp [--http [ADDR]] serve MCP (stdio reads token_file; HTTP takes each caller's token)
multica-intake service install     systemd user service (Linux)
multica-intake config set|get|show|explain …
multica-intake update [--check]    verified self-update, keeps a rollback copy
multica-intake uninstall [--purge]
```

Every command takes `--json` (one envelope: `{"schema":1,"ok","data","error","next"}`), `--yes`
and `--dry-run`. Exit codes: 0 ok, 1 problem, 2 usage, 10 update available. Agents:
`multica-intake init --yes --json`, then run each `fix`/`next` until `multica-intake doctor --json` is ok.

## Settings

`multica-intake config explain` lists every key. The ones you will touch:

| Key | Default | |
|---|---|---|
| `multica_url` | `http://127.0.0.1:8080` | Multica API (backend) URL |
| `intake_agent` | — | Agent that receives every issue (required) |
| `intake_agents.<slug>` | — | Per-workspace override |
| `workspaces` | all | Restrict to these workspace slugs |
| `source` | `intake` | Tag written to `metadata.source`, e.g. the chat app's name |
| `app_url` | — | Multica web URL, for issue links |
| `locale` | `en` | Language of the text written into issues (`en`, `vi`) |
| `listen` | `127.0.0.1:8801` | Keep it on loopback |

Env vars `MULTICA_INTAKE_<KEY>` override the file. The config holds no secrets.

## Security

- Runs as the caller: Multica enforces the account's own permissions; multica-intake narrows them further.
- No tool can set project, Type, labels, assignee or status, or touch issues the caller did not create.
- Never sends `X-Agent-ID`/`X-Task-ID`, so Multica always records the bot's member account, never an agent.
- Binds loopback by default; warns on anything else. Tokens are never logged; the audit log (stderr,
  one JSON line per call) records account, tool, workspace, issue and outcome only.
- Releases: SHA-256 checksums verified by the installer and by `update`; reproducible builds.

## Development

```sh
go test ./...                       # unit tests with a fake Multica
sh scripts/package.sh               # build every target into dist/
sh scripts/test-installer.sh        # offline installer lifecycle against dist/
sh scripts/release.sh vX.Y.Z --dry-run
```

See [docs/release-runbook.md](docs/release-runbook.md) and [docs/packaging-checklist.md](docs/packaging-checklist.md).

## License

MIT
