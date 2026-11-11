# Packaging checklist — multica-intake

Skill: cs-packaging-distribution (snapshot 2026-11-05). Priority: UX → safety → correctness → fast & light.
Archetype: **A, single binary** with `update`. Started from: **skillstation** (Go + MCP), and the
skill's `templates/install.sh` for the installer.

## Decisions (UX first)

| Decision | Choice | Why |
|---|---|---|
| Tagline | "Let your chat bots hand work to your Multica team — without letting them run it." | The pain is giving bots write access to a work tracker; the gain is safe hand-off |
| Quick-start promise | "From nothing to a bot that can submit work in about 5 minutes. You need a Multica you can reach and an agent there that triages new issues." | Those two are real prerequisites; nothing else is |
| Verbs | `init doctor status mcp service config update uninstall paths completions version` | `connect` omitted: the MCP client is a chat app configured by hand (the bot's token is the per-entry header), not an agent host on this machine |
| `init --yes` alone works? | Almost: it writes the config and checks Multica, then names the one missing value (`intake_agent`) as a runnable `next` | The intake agent is a business choice with no safe default |
| Install one-liner | `curl -fsSL https://github.com/vantt/multica-intake/releases/latest/download/install.sh \| sh` | Docker and `go install` in `<details>` |
| Dependency budget | none at runtime; installer: `sh`, `curl`/`wget`, `tar`, a SHA-256 tool | Static Go binary |
| Config | YAML at `<config_dir>/config.yaml`, self-explaining comments, `config set` as the one door; env `MULTICA_INTAKE_<KEY>` | Strict keys (security config) |
| Version · targets · naming | git tag (ldflags) · linux/darwin × amd64/arm64 · `multica-intake-<os>-<arch>.tar.gz` | No version in the asset name, so `latest/download/<asset>` works without resolving the tag |
| PATH · signing · hosts | `~/.local/bin`, print the PATH line · checksums only (no signature yet) · n/a | See gaps |

## Rules

| Rule | Level | Status | Evidence / deviation |
|---|---|---|---|
| ONB-01/04/06/07 | MUST | done | README: tagline, badges, promise, one-liner first, others in `<details>` |
| ONB-08/10/12 | MUST | done | 3 steps after install; `init` ends with "✓ Ready" + one thing to try; installer "Next:", `init` and README show the same 3 steps |
| ONB-09 | MUST | partial | Quick-start blocks are exercised by `scripts/test-installer.sh` (install, init, config, doctor), not extracted from the README |
| ONB-14 | MUST | done | Every failure prints Error/Fix and points to `doctor` |
| CMD-01..04 | MUST | done | `internal/doctor`: one engine; `init` = `doctor --fix` + `Creates`; doctor read-only by default; no `setup` verb |
| CMD-06..11 | MUST | done | `--version` and `version --json`; global flags on every verb; exit codes 0/1/2/10; stdout = data only (mcp stdio = protocol only); Error/Why/Fix; one JSON envelope |
| CMD-15..18 | MUST | done | Task-ordered no-arg help; self-sufficient `--help` for init/doctor/update/config/uninstall (tested); no prompt without a TTY; agent loop tested in `cli_test.go` and `test-installer.sh` |
| CMD-22/24 | MUST | done | Settings only through `config`; no setting flags on other verbs (`mcp --http ADDR` is the run address, not a setting write) |
| UX-01 | MUST | partial | linux/darwin amd64+arm64. **Windows not built** — the server runs beside Multica on Linux; `go install` documented |
| UX-02/03/07/08/10/11/14/15 | MUST | done | No questions with `--yes`; PATH line printed; static binary; installer needs sh+curl/wget+tar+sha tool; zero-config except the agent name; commented config; `config edit` validates; "did you mean" |
| INS-01/02/04/05/09/13/15 | MUST | done | Served from `latest/download`, stamped; strict sh, `main "$@"` last; env+flag for every option; no API calls; `~/.local/bin`, refuses root; small; uninstall asks via `/dev/tty` or needs `--yes` |
| INS-03 | MUST | done | Allow-list detection; Windows shells told to build from source |
| INS-06/07/08/16 | MUST/SHOULD | done | Rename into place; ownership marker; rollback copy; archive allow-list |
| INS-12 | MUST | done | Binary owns paths (`paths --json`); installer only places the binary |
| INS-14 | SHOULD | done | `scripts/test-installer.sh` offline fixture: install, re-install, init loop, unmanaged refusal, checksum mismatch, uninstall |
| INS-17 | MUST | n/a | No Windows build |
| SEC-01/04/05 | MUST | done | Exact-name single checksum entry (installer + `update`); https only, timeouts, size caps; safe extraction (tested) |
| SEC-02/03 | SHOULD | **gap** | No cosign/SBOM yet; installer says "not signed yet" instead of pretending |
| SEC-06 | SHOULD | done | `govulncheck` gates `release.sh` and CI; toolchain pinned to go1.26.11 because 1.26.8 had reachable stdlib CVEs |
| UPD-01..06 | MUST | done | Marker required (or `--force`); same checks as installer; base from ldflags var + env; pure tested version policy; real archive format; temp-in-same-dir → smoke → rollback copy → rename (tested) |
| CFG-01..08 | MUST | done | Config/state split per OS; `MULTICA_INTAKE_CONFIG_DIR/STATE_DIR`; `paths --json`; flag > env > file > default; binary owns defaults; atomic 0600 in 0700 dir; token file outside config, refused if loose; corrupt config is an error |
| CFG-09 | SHOULD | done | Unknown keys denied (security config) |
| CFG-13 | MUST | done | Non-loopback listen warns (doctor + server). No fixed default token: callers bring their own Multica PAT |
| DOC-01..03 | MUST | done | Each check: id, status, detail, runnable fix; `--fix` re-runs up to 3 rounds, exit reflects post-fix state; diagnosis never writes |
| UNI-01..05 | MUST | done | `uninstall` and `install.sh --uninstall`; removes service (only our unit), binary (only with marker), rollback, marker; dry-run / `--yes`; keeps config unless `--purge` |
| SVC-01 | MUST | deviation | The service is **opt-in** (`service install`), not created by `init`: in the main deployment it runs as a container, and a second copy on the host would fight for the port |
| SVC-02..04 | MUST | done | Thin pass-through printing the systemctl command; idempotent; no secrets in the unit |
| SVC-06 | MUST | deviation | Binds 127.0.0.1 by default, but no generated token: every caller authenticates with its own Multica PAT, checked against Multica on each request |
| SVC-07 | SHOULD | done | `/healthz` reports the fingerprint |
| AGT-04/05 | MUST | done | stdio mode reads a token **file**; stdout = protocol only |
| VER-01/02 | MUST | done | Tag is the only source (ldflags); `release.sh` creates the tag from the version it packages |
| VER-04/05/08 | SHOULD | done | Fingerprint with commit time; `release.sh` preflight (clean, main == origin/main, tag absent, CHANGELOG section, tests, vulncheck, `--dry-run`); CHANGELOG feeds notes |
| REL-01/02/05/06 | MUST | done | Explicit matrix, CGO off; one naming rule in package.sh / install.sh / selfupdate; one packaging script; checksums of all assets incl. installer, count asserted, re-verified |
| REL-03/04 | MUST | **deviation** | Release is cut locally by `scripts/release.sh`, which runs the full test suite, govulncheck, packaging and the installer lifecycle before tagging; CI runs the same on every push/PR. Reason: GitHub disabled Actions on the owner's fork in 2026-11; a tag workflow would be a single point of failure. Move to a tag workflow once Actions is reliable |
| REL-07/09/10 | SHOULD | done | Draft → asset count check → publish → public one-liner verified; reproducible (SOURCE_DATE_EPOCH, trimpath, deterministic tar, sorted checksums); CI `permissions: {}`, actions pinned by SHA |
| REL-13 | SHOULD | done | `docs/release-runbook.md` |
| TST-01/02/04 | MUST | done | Consumer proof from built assets outside the checkout (`test-installer.sh`); smoke in the release (public one-liner); agent loop non-interactive |
| TST-03 | MUST | todo | Upgrade-from-previous-tag test starts with the second release (no previous tag yet); `update` swap is unit-tested against a fixture release |
| TST-07/08 | SHOULD | done | Pure policies unit-tested; tests use temp HOME and dirs |

Not applicable:
- `80-agent-integration` beyond AGT-04/05: no agent host to connect (the client is a chat app).
- `channels/*`: no package-manager or plugin channel.
- CMD-20 man page: completions only.

## Plan (open items)

1. SEC-02: cosign keyless + SBOM, once releases run in a tag workflow.
2. TST-03: add `update` from the previous tag to `release.sh` from v0.1.1 on.
3. ONB-09: extract README quick-start blocks into the installer test.
