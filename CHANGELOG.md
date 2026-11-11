# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow semver.

## [0.1.0] - 2026-11-11

First release.

### Added
- MCP server with five tools: `list_workspaces`, `submit_issue`, `get_issue_status`,
  `reply_to_issue`, `list_my_issues`.
- Submitted issues carry no project, Type or labels and are assigned to the workspace's intake
  agent; the caller's project/Type guesses go into the description and metadata as suggestions.
- Each caller authenticates with its own Multica personal access token and only sees issues its
  account created with the configured `source` tag.
- Safe retries: `request_id`, and Multica's open-duplicate check mapped to `already_sent`.
- `reply_to_issue` mentions the assigned agent so it wakes.
- Streamable HTTP (stateless, JSON) and stdio transports; `/healthz` with the build fingerprint.
- Audit log: one JSON line per call on stderr, without content or tokens.
- Command surface: `init`, `doctor [--fix]`, `status`, `mcp`, `service`, `config`, `update`
  (verified, with rollback), `paths`, `uninstall`, `completions`, `version`; `--json` envelope.
- One-line installer, release archives for linux/darwin × amd64/arm64, `checksums.txt`, and the
  `ghcr.io/vantt/multica-intake` image (distroless, uid 61103).
