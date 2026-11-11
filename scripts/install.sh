#!/bin/sh
# multica-intake installer — adapted from cs-packaging-distribution templates/install.sh (INS-*, SEC-*).
# main "$@" is the LAST line so a truncated `curl | sh` never runs a partial script (INS-02).
set -eu

APP="multica-intake"
REPO="vantt/multica-intake"
DEFAULT_VERSION='@APP_VERSION@'   # stamped by the release workflow (REL-08)
MARKER=".${APP}-managed"
MAX_BYTES=134217728               # 128 MiB per download (SEC-04)

usage() {
  cat <<EOF
Install, upgrade or remove ${APP}.

Usage:
  curl -fsSL https://github.com/${REPO}/releases/latest/download/install.sh | sh
  curl -fsSL https://github.com/${REPO}/releases/latest/download/install.sh | sh -s -- [options]

Options (each also as an env var):
  --version <vX.Y.Z>     install this version          MULTICA_INTAKE_VERSION      (default: this release)
  --install-dir <dir>    where the binary goes         MULTICA_INTAKE_INSTALL_DIR  (default: ~/.local/bin)
  --dry-run              show what would happen        MULTICA_INTAKE_DRY_RUN=1
  --no-modify-path       don't print PATH advice       MULTICA_INTAKE_NO_MODIFY_PATH=1
  --uninstall            remove the binary (keeps config and data)   MULTICA_INTAKE_UNINSTALL=1
  -y, --yes              don't ask for confirmation    MULTICA_INTAKE_YES=1
  -h, --help             show this help
Other env: MULTICA_INTAKE_RELEASE_BASE (mirror / draft / test fixture), MULTICA_INTAKE_ALLOW_ROOT=1

Exit codes: 0 ok, 1 failed, 2 usage error.
After installing, run:  ${APP} init
EOF
}

say() { printf '%s\n' "$*" >&2; }
die() { say "error: $1"; [ -z "${2:-}" ] || say "  why: $2"; [ -z "${3:-}" ] || say "  fix: $3"; exit 1; }
usage_err() { say "error: $1"; say "  fix: sh install.sh --help"; exit 2; }

parse_args() {
  VERSION="${MULTICA_INTAKE_VERSION:-$DEFAULT_VERSION}"
  INSTALL_DIR="${MULTICA_INTAKE_INSTALL_DIR:-$HOME/.local/bin}"
  BASE="${MULTICA_INTAKE_RELEASE_BASE:-https://github.com/$REPO/releases}"
  DRY_RUN="${MULTICA_INTAKE_DRY_RUN:-0}"; NO_PATH="${MULTICA_INTAKE_NO_MODIFY_PATH:-0}"
  UNINSTALL="${MULTICA_INTAKE_UNINSTALL:-0}"; YES="${MULTICA_INTAKE_YES:-0}"
  while [ $# -gt 0 ]; do
    case "$1" in
      --version|--install-dir)
        [ $# -ge 2 ] && [ -n "$2" ] || usage_err "$1 needs a value"
        if [ "$1" = --version ]; then VERSION="$2"; else INSTALL_DIR="$2"; fi; shift ;;
      --version=*) VERSION="${1#*=}" ;;
      --install-dir=*) INSTALL_DIR="${1#*=}" ;;
      --dry-run) DRY_RUN=1 ;;
      --no-modify-path) NO_PATH=1 ;;
      --uninstall) UNINSTALL=1 ;;
      -y|--yes) YES=1 ;;
      -h|--help) usage; exit 0 ;;
      *) usage_err "unknown option: $1" ;;
    esac; shift
  done
  case "$VERSION" in @*@|v*) ;; [0-9]*) VERSION="v$VERSION" ;; esac   # accept 1.2.3 and v1.2.3
  case "$BASE" in https://*|http://127.0.0.1*|http://localhost*) ;; *) die "release base must be https: $BASE" ;; esac
  if [ "$(id -u)" = 0 ] && [ "${MULTICA_INTAKE_ALLOW_ROOT:-0}" != 1 ]; then
    die "refusing to run as root" "installs are per-user" "re-run as your user, or set MULTICA_INTAKE_ALLOW_ROOT=1"
  fi
}

detect_target() {   # single naming rule (REL-02) — keep in sync with the release workflow and the updater
  case "$(uname -s)" in
    Linux) os=linux ;; Darwin) os=darwin ;;
    MINGW*|MSYS*|CYGWIN*) die "Windows is not supported" "no prebuilt binary" "build from source: go install github.com/vantt/multica-intake/cmd/multica-intake@latest" ;;
    *) die "unsupported OS: $(uname -s)" "no prebuilt binary" "build from source: go install github.com/vantt/multica-intake/cmd/multica-intake@latest" ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;;
    *) die "unsupported CPU: $(uname -m)" "no prebuilt binary" "build from source: go install github.com/vantt/multica-intake/cmd/multica-intake@latest" ;;
  esac
  ASSET="${APP}-${os}-${arch}.tar.gz"   # same rule as selfupdate.AssetName and scripts/package.sh
}

fetch() {  # url dest — HTTPS only (except a local test fixture), wall-clock timeout, size cap (SEC-04)
  case "$1" in https://*) PROTO="--proto =https --tlsv1.2" ;; *) PROTO="" ;; esac
  if command -v curl >/dev/null 2>&1; then
    curl $PROTO -fsSL --connect-timeout 10 --max-time 120 --max-filesize "$MAX_BYTES" -o "$2" "$1" \
      || die "download failed: $1" "network error or missing release asset" "check the version exists: $BASE"
  elif command -v wget >/dev/null 2>&1; then
    # busybox wget has no --https-only; HTTPS is already enforced on BASE above
    if command -v timeout >/dev/null 2>&1; then timeout 130 wget -q -O "$2" "$1"; else wget -q -O "$2" "$1"; fi \
      || die "download failed: $1" "network error or missing release asset" "check the version exists: $BASE"
    [ "$(wc -c < "$2")" -le "$MAX_BYTES" ] || die "download larger than allowed: $1"
  else
    die "need curl or wget" "" "install curl (e.g. apk add curl / apt install curl)"
  fi
}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | cut -d' ' -f1
  elif command -v openssl >/dev/null 2>&1; then openssl dgst -sha256 "$1" | awk '{print $NF}'
  else die "no SHA-256 tool found" "" "install coreutils or openssl"; fi
}

verify() {  # exact filename match, exactly one entry (SEC-01)
  expected="$(awk -v f="$ASSET" '{n=$2; sub(/^\*/,"",n); if (n==f) {print $1; c++}} END{if (c!=1) exit 1}' "$TMP/checksums.txt")" \
    || die "checksum entry for $ASSET missing or duplicated" "the release is incomplete" "try again later or pick another --version"
  [ "$(sha256 "$TMP/$ASSET")" = "$expected" ] || die "checksum mismatch for $ASSET" "download corrupted or tampered" "retry; report it if it persists"
  # Releases are not signed yet (docs/packaging-checklist.md, SEC-02): say so instead of pretending.
  if [ "${MULTICA_INTAKE_REQUIRE_SIGNATURE:-0}" = 1 ]; then
    die "signature required but this release is not signed" "" "unset MULTICA_INTAKE_REQUIRE_SIGNATURE"
  fi
  say "note: checksum verified; releases are not signed yet"
}

check_layout() {  # allow-list (INS-16): one executable + optional docs/completions, no links
  tar -tzvf "$TMP/$ASSET" | awk '{print substr($1,1,1)}' | grep -q '[lh]' && die "archive contains links" "unsafe archive" "report this release"
  tar -tzf "$TMP/$ASSET" | while IFS= read -r m; do
    case "$m" in
      "$APP"|LICENSE*|README*) ;;
      /*|*..*) die "unsafe path in archive: $m" ;;
      *) die "unexpected file in archive: $m" "layout differs from the naming rule" "report this release" ;;
    esac
  done
}

install_bin() {
  check_layout
  tar -xzf "$TMP/$ASSET" -C "$TMP" "$APP"
  [ -f "$TMP/$APP" ] || die "archive has no '$APP' binary"
  chmod 755 "$TMP/$APP"
  "$TMP/$APP" --version >/dev/null 2>&1 || die "the downloaded binary does not run here" "wrong platform or corrupt file" "report your OS/CPU: $(uname -sm)"
  target="$INSTALL_DIR/$APP"
  if [ -e "$target" ] && [ ! -f "$INSTALL_DIR/$MARKER" ]; then
    die "$target exists and was not installed by this script" "" "remove it, or use --install-dir <other dir>"
  fi
  if [ "$DRY_RUN" = 1 ]; then say "dry-run: would install $ASSET to $target"; return 0; fi
  mkdir -p "$INSTALL_DIR"
  [ -f "$target" ] && cp -p "$target" "$target.rollback"
  cp "$TMP/$APP" "$INSTALL_DIR/.$APP.new.$$" && chmod 755 "$INSTALL_DIR/.$APP.new.$$"
  mv -f "$INSTALL_DIR/.$APP.new.$$" "$target"            # rename: no "Text file busy" (INS-06)
  if ! "$target" --version >/dev/null 2>&1; then
    [ -f "$target.rollback" ] && mv -f "$target.rollback" "$target"
    die "the new binary failed to start; previous version restored"
  fi
  rm -f "$target.rollback"
  (umask 077; printf '%s-managed-v1\nversion=%s\n' "$APP" "$VERSION" > "$INSTALL_DIR/$MARKER")
}

confirm() {  # ask via /dev/tty (stdin is the script under curl|sh); no TTY → require --yes
  [ "$YES" = 1 ] && return 0
  if (: </dev/tty >/dev/tty) 2>/dev/null; then   # opening proves a controlling terminal exists
    printf '%s [y/N] ' "$1" > /dev/tty; read -r ans < /dev/tty || ans=""
    case "$ans" in y|Y|yes|YES) return 0 ;; *) say "cancelled"; exit 1 ;; esac
  fi
  die "confirmation needed but no terminal is available" "" "re-run with --yes"
}

uninstall() {
  [ -f "$INSTALL_DIR/$MARKER" ] || die "no install made by this script in $INSTALL_DIR" "" "use --install-dir <dir> if you installed elsewhere"
  say "will remove: $INSTALL_DIR/$APP"
  say "will keep:   config and data — to remove them too, first run: $INSTALL_DIR/$APP uninstall --purge"
  [ "$DRY_RUN" = 1 ] && { say "dry-run: nothing removed"; return 0; }
  confirm "Remove $APP?"
  rm -f "$INSTALL_DIR/$APP" "$INSTALL_DIR/$MARKER"
  say "removed $APP"
}

next_steps() {   # same steps as README quick start and `init` output (ONB-12)
  bin="$APP"
  case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *) bin="$INSTALL_DIR/$APP"
       [ "$NO_PATH" = 1 ] || say "PATH: add this line to your shell profile:  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
  esac
  say "installed: $("$INSTALL_DIR/$APP" --version 2>/dev/null || echo "$APP $VERSION")"
  say "Next:"
  say "  1. $bin init"
  say "  2. $bin config set intake_agent \"<agent name>\""
  say "  3. $bin service install     (or: $bin mcp --http)"
}

main() {
  parse_args "$@"
  if [ "$UNINSTALL" = 1 ]; then uninstall; return 0; fi
  case "$VERSION" in @*@) die "this copy of the installer has no version" "it was not downloaded from a release" "pass --version vX.Y.Z, or use the releases/latest/download/install.sh URL" ;; esac
  detect_target
  TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT INT TERM
  fetch "$BASE/download/$VERSION/$ASSET" "$TMP/$ASSET"
  fetch "$BASE/download/$VERSION/checksums.txt" "$TMP/checksums.txt"
  verify
  install_bin
  [ "$DRY_RUN" = 1 ] || next_steps
}

main "$@"
