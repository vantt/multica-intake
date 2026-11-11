#!/bin/sh
# Cut a release from this machine (VER-05, REL-06/07, SEC-06). The tag is the only
# version source; CHANGELOG.md must already have its section.
#
#   sh scripts/release.sh v0.1.0 --dry-run     # every check and the build, no tag, no upload
#   sh scripts/release.sh v0.1.0               # tag, push, image, GitHub release
#
# Needs: go, git, gh (logged in), docker buildx logged in to ghcr.io with write:packages,
# govulncheck (go install golang.org/x/vuln/cmd/govulncheck@latest).
# Why local and not a tag workflow: see docs/release-runbook.md.
set -eu

REPO=vantt/multica-intake
IMAGE=ghcr.io/vantt/multica-intake

die() { echo "error: $1" >&2; [ -z "${2:-}" ] || echo "  fix: $2" >&2; exit 1; }
step() { echo "== $*" >&2; }

main() {
  cd "$(dirname "$0")/.."
  [ $# -ge 1 ] || die "usage: sh scripts/release.sh vX.Y.Z [--dry-run] [--no-image]" "sh scripts/release.sh v0.1.0 --dry-run"
  V="$1"; shift
  case "$V" in v[0-9]*.[0-9]*.[0-9]*) ;; *) die "version must look like v1.2.3: $V" ;; esac
  DRY=0; IMG=1
  for a in "$@"; do case "$a" in --dry-run) DRY=1 ;; --no-image) IMG=0 ;; *) die "unknown option $a" ;; esac; done

  step "preflight"
  [ -z "$(git status --porcelain)" ] || die "the working tree is not clean" "git status"
  [ "$(git rev-parse --abbrev-ref HEAD)" = main ] || die "not on main" "git switch main"
  git fetch -q origin 2>/dev/null || true
  if git rev-parse -q --verify origin/main >/dev/null; then
    [ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || die "main is not origin/main" "git pull --ff-only && git push"
  fi
  ! git rev-parse -q --verify "refs/tags/$V" >/dev/null || die "tag $V exists already" "pick the next version"
  grep -q "^## \[${V#v}\]" CHANGELOG.md || die "CHANGELOG.md has no section for ${V#v}" "add '## [${V#v}] - $(date +%F)'"
  [ -z "$(gofmt -l .)" ] || die "gofmt: $(gofmt -l . | tr "\n" " ")" "gofmt -w ."
  go vet ./...
  go test -count=1 ./...
  command -v govulncheck >/dev/null || PATH="$PATH:$(go env GOPATH)/bin"
  govulncheck ./... >/dev/null || die "govulncheck found vulnerabilities the code calls" "govulncheck ./...  then bump the toolchain or the dependency"

  step "package $V"
  sh scripts/package.sh "$V"
  step "installer lifecycle"
  sh scripts/test-installer.sh

  if [ "$DRY" = 1 ]; then
    echo "dry run done: dist/ holds $V; nothing tagged or uploaded" >&2
    return 0
  fi

  step "tag and push"
  awk -v v="${V#v}" '$0 ~ "^## \\["v"\\]" {p=1; next} /^## \[/ {p=0} p' CHANGELOG.md > dist/notes.md
  git tag -a "$V" -m "multica-intake $V"
  git push -q origin main "$V"

  if [ "$IMG" = 1 ]; then
    step "image $IMAGE:$V (linux/amd64, linux/arm64)"
    # A docker-container builder pushes a multi-arch manifest; the image has no RUN step, so no QEMU.
    docker buildx inspect multica-intake-builder >/dev/null 2>&1 \
      || docker buildx create --name multica-intake-builder --driver docker-container >/dev/null
    docker buildx build --builder multica-intake-builder --platform linux/amd64,linux/arm64 --build-arg VERSION="$V" \
      -t "$IMAGE:$V" -t "$IMAGE:latest" --push .
  fi

  step "GitHub release (draft, then verify, then publish)"
  gh release create "$V" --repo "$REPO" --draft --title "multica-intake $V" --notes-file dist/notes.md \
    dist/multica-intake-*.tar.gz dist/install.sh dist/checksums.txt
  n=$(gh release view "$V" --repo "$REPO" --json assets --jq '.assets | length')
  [ "$n" -eq 6 ] || die "the draft has $n assets, expected 6" "gh release view $V --repo $REPO"
  gh release edit "$V" --repo "$REPO" --draft=false --latest

  step "verify the public release"
  sleep 3
  t="$(mktemp -d)"
  curl -fsSL "https://github.com/$REPO/releases/latest/download/checksums.txt" -o "$t/checksums.txt"
  cmp -s "$t/checksums.txt" dist/checksums.txt || die "published checksums.txt differs from dist/"
  HOME="$t" sh -c "curl -fsSL https://github.com/$REPO/releases/latest/download/install.sh | sh -s -- --yes" >/dev/null 2>&1 \
    || die "the published one-line install failed" "HOME=\$(mktemp -d) sh -c 'curl -fsSL https://github.com/$REPO/releases/latest/download/install.sh | sh'"
  "$t/.local/bin/multica-intake" --version | grep -q "$V" || die "the published installer did not install $V"
  rm -rf "$t"

  # Upgrade from the previous release (TST-03): install it, write a config, `update`,
  # then the version must have changed and the config must have survived.
  prev="$(git tag --list 'v*' --sort=-v:refname | grep -vx "$V" | head -1 || true)"
  if [ -n "$prev" ]; then
    step "upgrade $prev -> $V"
    t="$(mktemp -d)"
    HOME="$t" sh -c "curl -fsSL https://github.com/$REPO/releases/download/$prev/install.sh | sh -s -- --yes" >/dev/null 2>&1 \
      || die "could not install $prev for the upgrade test"
    HOME="$t" "$t/.local/bin/multica-intake" config set intake_agent "Upgrade Test" >/dev/null 2>&1
    HOME="$t" "$t/.local/bin/multica-intake" update --yes >/dev/null 2>&1 || die "update $prev -> $V failed"
    HOME="$t" "$t/.local/bin/multica-intake" --version | grep -q "$V" || die "update did not install $V"
    [ "$(HOME="$t" "$t/.local/bin/multica-intake" config get intake_agent)" = "Upgrade Test" ] || die "the config did not survive the update"
    rm -rf "$t"
  fi
  echo "released $V: https://github.com/$REPO/releases/tag/$V" >&2
}

main "$@"
