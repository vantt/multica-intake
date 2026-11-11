#!/bin/sh
# The one packaging script (REL-05): local dry runs, CI and releases all call it.
#
#   sh scripts/package.sh v0.1.0          # build every target into dist/
#   sh scripts/package.sh                 # version from `git describe` (dev builds)
#
# Output (dist/):
#   multica-intake-<os>-<arch>.tar.gz     one per target: the binary + LICENSE + README.md
#   install.sh                            the installer, stamped with the version (REL-08)
#   checksums.txt                         sha256 of every file above, sorted (REL-06)
#   bin/<os>-<arch>/multica-intake        unpacked binaries (the Docker image copies these)
#
# Naming rule (REL-02), shared with internal/selfupdate.AssetName and scripts/install.sh:
#   multica-intake-<os>-<arch>.tar.gz
set -eu

TARGETS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"

main() {
  cd "$(dirname "$0")/.."
  VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
  case "$VERSION" in v*|dev*) ;; [0-9]*) VERSION="v$VERSION" ;; esac
  COMMIT="$(git rev-parse --short=7 HEAD 2>/dev/null || echo unknown)"
  # Commit time, not build time: two builds of one commit are byte-identical (REL-09).
  SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct 2>/dev/null || date +%s)}"
  export SOURCE_DATE_EPOCH
  DATE="$(date -u -d "@$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ)"
  DIRTY=false
  [ -z "$(git status --porcelain 2>/dev/null)" ] || DIRTY=true
  PKG=github.com/vantt/multica-intake/internal/version
  LDFLAGS="-s -w -X $PKG.Version=$VERSION -X $PKG.Commit=$COMMIT -X $PKG.Date=$DATE -X $PKG.Dirty=$DIRTY"

  rm -rf dist && mkdir -p dist/bin
  for t in $TARGETS; do
    os="${t%/*}"; arch="${t#*/}"
    out="dist/bin/$os-$arch"
    mkdir -p "$out"
    echo "build $os/$arch" >&2
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -buildvcs=false -ldflags "$LDFLAGS" \
      -o "$out/multica-intake" ./cmd/multica-intake
    stage="$(mktemp -d)"
    cp "$out/multica-intake" LICENSE README.md "$stage/"
    # Deterministic tar: fixed order, owner, mtime.
    tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$SOURCE_DATE_EPOCH" \
      -C "$stage" -cf - multica-intake LICENSE README.md | gzip -n -9 > "dist/multica-intake-$os-$arch.tar.gz"
    rm -rf "$stage"
  done
  sed "s|@APP_VERSION@|$VERSION|" scripts/install.sh > dist/install.sh
  (cd dist && sha256sum multica-intake-*.tar.gz install.sh | LC_ALL=C sort -k2 > checksums.txt)

  want=$(( $(echo "$TARGETS" | wc -w) + 1 ))
  got=$(wc -l < dist/checksums.txt)
  [ "$got" -eq "$want" ] || { echo "error: checksums.txt lists $got files, expected $want" >&2; exit 1; }
  (cd dist && sha256sum -c --quiet checksums.txt)
  echo "dist/ ready for $VERSION ($COMMIT, dirty=$DIRTY): $want assets + checksums.txt" >&2
}

main "$@"
