#!/bin/sh
# Offline installer lifecycle test (INS-14, TST-01, TST-05): serves dist/ from a local
# fixture and runs install, re-install, the first-run loop, unmanaged refusal, checksum
# mismatch and uninstall in a throwaway HOME. Needs dist/ (sh scripts/package.sh <ver>).
#
#   sh scripts/test-installer.sh
set -eu

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok   $*" >&2; }

main() {
  cd "$(dirname "$0")/.."
  [ -f dist/checksums.txt ] || fail "no dist/: run sh scripts/package.sh first"
  VERSION="$(sed -n 's/^DEFAULT_VERSION=.\(v[^'"'"']*\).*/\1/p' dist/install.sh)"
  [ -n "$VERSION" ] || fail "dist/install.sh is not version-stamped"
  W="$(mktemp -d)"; trap 'kill "$SRV" 2>/dev/null || true; rm -rf "$W"' EXIT INT TERM
  mkdir -p "$W/site/download/$VERSION" "$W/home"
  cp dist/*.tar.gz dist/checksums.txt dist/install.sh "$W/site/download/$VERSION/"
  PORT=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])')
  python3 -m http.server "$PORT" --bind 127.0.0.1 --directory "$W/site" >/dev/null 2>&1 & SRV=$!
  sleep 1
  export HOME="$W/home" XDG_CONFIG_HOME="$W/home/.config" XDG_STATE_HOME="$W/home/.local/state"
  export MULTICA_INTAKE_RELEASE_BASE="http://127.0.0.1:$PORT"
  BIN="$HOME/.local/bin/multica-intake"

  out="$(sh dist/install.sh --yes 2>&1)" || fail "install: $out"
  [ -x "$BIN" ] || fail "binary not installed"
  [ -f "$HOME/.local/bin/.multica-intake-managed" ] || fail "no ownership marker"
  "$BIN" --version | grep -q "$VERSION" || fail "installed binary is not $VERSION"
  echo "$out" | grep -q "multica-intake init" || fail "installer did not print the next step"
  pass "install $VERSION"

  sh dist/install.sh --yes >/dev/null 2>&1 || fail "re-install"
  pass "re-install"

  # First run, the agent loop: init --yes --json prints one JSON envelope and names the fix.
  MULTICA_INTAKE_MULTICA_URL="http://127.0.0.1:$PORT" "$BIN" init --yes --json > "$W/init.json" || true
  python3 -c 'import json,sys;e=json.load(open(sys.argv[1]));assert e["schema"]==1 and not e["ok"] and any("intake_agent" in n for n in e["next"]),e' "$W/init.json" \
    || fail "init --yes --json envelope"
  "$BIN" config set intake_agent "PM" >/dev/null 2>&1 || fail "config set"
  "$BIN" config set multica_url "http://127.0.0.1:$PORT" >/dev/null 2>&1 || fail "config set url"
  # The fixture answers /health with 404, so doctor must fail on the API and say how to fix it.
  if "$BIN" doctor --json > "$W/doc.json"; then fail "doctor passed against a non-Multica URL"; fi
  grep -q multica_url "$W/doc.json" || fail "doctor did not name the multica_url fix"
  pass "init / config / doctor loop"

  mkdir -p "$W/other"; printf '#!/bin/sh\necho mine\n' > "$W/other/multica-intake"; chmod +x "$W/other/multica-intake"
  if sh dist/install.sh --yes --install-dir "$W/other" >/dev/null 2>&1; then fail "overwrote an unmanaged binary"; fi
  grep -q mine "$W/other/multica-intake" || fail "unmanaged binary changed"
  pass "refuses to overwrite an unmanaged binary"

  cp -r "$W/site/download/$VERSION" "$W/site/download/v0.0.0-bad"
  for f in "$W/site/download/v0.0.0-bad"/*.tar.gz; do printf 'x' >> "$f"; done
  if sh dist/install.sh --yes --version v0.0.0-bad --install-dir "$W/bad" >/dev/null 2>&1; then fail "installed a corrupt archive"; fi
  [ ! -e "$W/bad/multica-intake" ] || fail "corrupt binary left behind"
  pass "checksum mismatch refused"

  if sh dist/install.sh --uninstall </dev/null >/dev/null 2>&1; then fail "uninstall without --yes and no TTY"; fi
  sh dist/install.sh --uninstall --yes >/dev/null 2>&1 || fail "uninstall"
  [ ! -e "$BIN" ] || fail "binary still there"
  [ -f "$XDG_CONFIG_HOME/multica-intake/config.yaml" ] || fail "uninstall removed the config"
  pass "uninstall keeps the config"
  echo "installer lifecycle: all passed ($VERSION)" >&2
}

main "$@"
