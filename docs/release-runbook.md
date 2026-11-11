# Release runbook

Releases are cut from a maintainer's machine with `scripts/release.sh`, not by a tag
workflow (see packaging-checklist.md, REL-03/04). CI still runs every check on each push.

## Cut

1. Add a `## [X.Y.Z] - YYYY-MM-DD` section to `CHANGELOG.md`, commit, push `main`.
2. Dry run (all checks + build + installer lifecycle, nothing published):

   ```sh
   sh scripts/release.sh vX.Y.Z --dry-run
   ```
3. Release:

   ```sh
   sh scripts/release.sh vX.Y.Z
   ```
   It tags and pushes, builds and pushes `ghcr.io/vantt/multica-intake:vX.Y.Z` and `:latest`
   (linux/amd64 + arm64), creates the GitHub release as a draft with 6 assets, checks the
   count, publishes it, then installs from the public one-liner and checks the version.

Prerequisites: `gh auth status` with `write:packages`, `docker login ghcr.io`, docker buildx
(the script creates a `multica-intake-builder` docker-container builder; the image has no RUN
step, so arm64 needs no QEMU), `govulncheck` on PATH.

## Verify by hand

```sh
HOME=$(mktemp -d) sh -c 'curl -fsSL https://github.com/vantt/multica-intake/releases/latest/download/install.sh | sh -s -- --yes && ~/.local/bin/multica-intake version --json'
docker run --rm ghcr.io/vantt/multica-intake:vX.Y.Z version
```

## Roll back

- Users: `multica-intake update --rollback` (previous binary) or `update --version vPREV --yes`.
- Containers: pin the previous tag.

## Yank

A broken release: `gh release edit vX.Y.Z --repo vantt/multica-intake --draft=true` (installers
and `update` stop seeing it as latest), then release vX.Y.Z+1. Never re-use a tag.
