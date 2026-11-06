# SD-2 implementation plan: develop branch, CI & release workflows, agent rules

## Goal
Add a `develop` integration branch, a CI workflow on pushes to `main`/`develop`, a Release workflow that publishes `vX.Y.Z` with `dist/shutdowner.exe` when a new `VERSION` lands on `main`, and `AGENTS.md`/`CLAUDE.md` rules telling the agent how to branch, bump and release.

## Architecture
The workflows are plain GitHub Actions YAML with no extra scripts. `ci.yml` is one job (gofmt, `make test`, `make vet`, `make build-windows`). It can also be called through `workflow_call`. `release.yml` has three jobs:
- `check` reads and validates `VERSION`, skips when the tag already exists, checks the version against the latest `v*` tag and extracts the CHANGELOG section.
- `ci` reuses `ci.yml`.
- `release` builds and runs `gh release create`, which also creates the tag.

The workflow never computes or commits a version. The agent bumps `VERSION` and `CHANGELOG.md` once, in the release PR, following `AGENTS.md`. `CLAUDE.md` only imports `AGENTS.md`.

## Facts about the repo (checked at `fab589f`)
- Go module `shutdowner`, `go 1.25.0` in `go.mod`. Packages: `cmd/shutdowner`, `internal/{action,auth,config,logging,power,sysinfo,volume,web,winsvc}`, `tools/mkicon`.
- Makefile targets: `test` (`go test ./...`), `vet` (host + `GOOS=windows GOARCH=amd64`), `fmt`, `build-windows` (writes `dist/shutdowner.exe`, `CGO_ENABLED=0`, `-ldflags "-s -w"`), `run-dev`, `icon`, `clean`. `dist/` is gitignored.
- There is no `.github/`, `VERSION`, `CHANGELOG.md`, `AGENTS.md` or `CLAUDE.md` yet. There are no tags. Remote branches are only `origin/main` and the SD-1 branch.
- Commit style: conventional prefixes (`feat:`, `fix:`, `docs:`). Specs live in `docs/superpowers/specs/`, plans in `docs/superpowers/plans/`.
- README sections: Build, Install, Configuration, Command line, **Development** (line 102), **How it behaves** (line 113), ...
- Breaking-change surfaces: `.env` keys in `internal/config/config.go` (`SHUTDOWNER_*`); CLI flags in `cmd/shutdowner/main.go`; routes in `internal/web/server.go` (`/login`, `/logout`, `/api/status`, `/api/action`, `/api/abort`, `/api/dismiss`, `/api/volume`); `schedule.json` in `internal/action/store.go`; service setup in `internal/winsvc/`.

## File map
| File | Action | Responsibility |
|---|---|---|
| `develop` branch (remote) | create | Integration branch, created from `origin/main` (`fab589f`) |
| `.github/workflows/ci.yml` | create | CI on push to `main`/`develop` and through `workflow_call` |
| `.github/workflows/release.yml` | create | Version checks, CI, build and GitHub Release on push to `main` |
| `VERSION` | create | `1.0.0` |
| `CHANGELOG.md` | create | Keep-a-Changelog file, first entry `## [1.0.0] - <date>` |
| `AGENTS.md` | create | Single source of truth for agent rules |
| `CLAUDE.md` | create | Only `@AGENTS.md` |
| `README.md` | modify | New "Releases & versioning" section between Development and How it behaves |

## Tooling used for verification
- `actionlint` with no install: `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7` from the repo root. It lints `.github/workflows/*.yml`. It needs the network on first run.
- No Go code changes, so no new Go unit tests. `make test` and `make vet` must keep passing.

---

## Step 1: Create and push the `develop` branch from `main`

No file changes and no commit.
1. `git fetch origin`
2. `git ls-remote --heads origin develop`. If it prints a line, `develop` already exists. **Do not overwrite it**: compare it with `git rev-parse origin/main`, report the result and go to the next step.
3. `git rev-parse origin/main`. Expected `fab589f…`. If `main` has moved, use the current `origin/main` and say so in the report.
4. `git push origin origin/main:refs/heads/develop`
5. Verify: `git ls-remote --heads origin develop` prints the same SHA as `git rev-parse origin/main`.

Acceptance: `origin/develop` exists and equals `origin/main`. The SD-2 work branch (`megoo/SD-2-…`) is based on `main` at the same commit, so its PR can target `develop` with no extra commits.

## Step 2: Add the CI workflow

**Failing check first:** `ls .github/workflows/ci.yml` fails (no such file). Then run `gofmt -l .`. It must print nothing, because CI will enforce this. If it lists files, run `make fmt` and commit those files first as `style: gofmt the tree`.

**Create `.github/workflows/ci.yml`:**
```yaml
name: CI

on:
  push:
    branches: [main, develop]
  workflow_call:

permissions:
  contents: read

jobs:
  test:
    name: Format, test, vet, build
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Check gofmt
        run: |
          unformatted="$(gofmt -l .)"
          if [ -n "$unformatted" ]; then
            echo "::error::these files need gofmt (run make fmt):"
            echo "$unformatted"
            exit 1
          fi
      - name: Test
        run: make test
      - name: Vet
        run: make vet
      - name: Build Windows executable
        run: make build-windows
```

**Verify:**
- `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7` prints nothing and exits with 0.
- `make test`, `make vet` and `make build-windows` pass locally (this is what CI runs; `make vet` already includes `GOOS=windows`).

**Commit:** `ci: run gofmt, tests, vet and the Windows build on main and develop`

## Step 3: Add VERSION and CHANGELOG.md

**Failing check first:** `cat VERSION` fails (no such file).

**`VERSION`** is exactly one line, `1.0.0`, with a trailing newline and nothing else.

**`CHANGELOG.md`** (use the date the step is done, `date +%F`):
```markdown
# Changelog

All notable changes to Shutdowner are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions are
`MAJOR.MINOR.BUILD` as described in [AGENTS.md](AGENTS.md). Each version's
section becomes the notes of its GitHub Release, so write it for the people who
install the app.

## [1.0.0] - 2026-11-06

The first tagged release. It covers everything built so far.

### Added
- Shut down, restart, sleep or hibernate the PC from a password-protected web
  page, with a confirm dialog and a countdown you can cancel
  (`SHUTDOWNER_DELAY_SECONDS`).
- Runs as a Windows service: `--install-service`, `--uninstall-service`, plus
  `--init` and `--hash-password` for setup and `--console` for the foreground.
- Signed sessions, CSRF protection, login rate limiting, and a loopback-only
  listener unless `--allow-public-bind` is given.
- Scheduled actions, kept across restarts in `schedule.json`.
- Remote volume control: a slider and mute for the default audio device, run
  through a helper in the signed-in user's session.
- App icon, favicon and dashboard logo, and a colour for each action.
- A rotating log file.
- CI and Release workflows, the `VERSION` file and the agent rules in
  `AGENTS.md`.

### Fixed
- Sleep on PCs that use modern standby.
```

**Verify that the extraction Release will use finds the section** (it should print the body, from "The first tagged release" to the last bullet):
```
awk -v v=1.0.0 'index($0, "## [" v "]") == 1 { found = 1; next } found && /^## \[/ { exit } found { print }' CHANGELOG.md
```
and `grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' VERSION; echo $?` prints `0`.

**Commit:** `chore: start versioning at 1.0.0 with VERSION and CHANGELOG.md`

## Step 4: Add the Release workflow

**Failing check first:** `ls .github/workflows/release.yml` fails.

**Create `.github/workflows/release.yml`:**
```yaml
name: Release

on:
  push:
    branches: [main]

permissions:
  contents: write

concurrency:
  group: release
  cancel-in-progress: false

jobs:
  check:
    name: Check version
    runs-on: ubuntu-latest
    outputs:
      release: ${{ steps.version.outputs.release }}
      version: ${{ steps.version.outputs.version }}
      notes: ${{ steps.notes.outputs.notes }}
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - name: Read and validate VERSION
        id: version
        run: |
          version="$(tr -d '[:space:]' < VERSION)"
          if ! printf '%s' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
            echo "::error file=VERSION::VERSION must be MAJOR.MINOR.BUILD, got '$version'"
            exit 1
          fi
          tag="v$version"
          if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
            echo "$tag already released, nothing to do"
            echo "release=false" >> "$GITHUB_OUTPUT"
            exit 0
          fi
          latest="$(git tag -l 'v*' | sort -V | tail -n 1)"
          if [ -n "$latest" ]; then
            highest="$(printf '%s\n%s\n' "${latest#v}" "$version" | sort -V | tail -n 1)"
            if [ "$highest" != "$version" ]; then
              echo "::error file=VERSION::$version is not above the latest release $latest"
              exit 1
            fi
          fi
          echo "release=true" >> "$GITHUB_OUTPUT"
          echo "version=$version" >> "$GITHUB_OUTPUT"
      - name: Extract release notes from CHANGELOG.md
        id: notes
        if: steps.version.outputs.release == 'true'
        env:
          VERSION: ${{ steps.version.outputs.version }}
        run: |
          awk -v v="$VERSION" '
            index($0, "## [" v "]") == 1 { found = 1; next }
            found && /^## \[/ { exit }
            found { print }
          ' CHANGELOG.md > notes.md
          if ! grep -q '[^[:space:]]' notes.md; then
            echo "::error file=CHANGELOG.md::no '## [$VERSION]' section, or it is empty"
            exit 1
          fi
          {
            echo 'notes<<CHANGELOG_EOF'
            cat notes.md
            echo 'CHANGELOG_EOF'
          } >> "$GITHUB_OUTPUT"

  ci:
    name: CI
    needs: check
    if: needs.check.outputs.release == 'true'
    uses: ./.github/workflows/ci.yml

  release:
    name: Publish release
    needs: [check, ci]
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Build Windows executable
        run: make build-windows
      - name: Create GitHub Release
        env:
          GH_TOKEN: ${{ github.token }}
          TAG: v${{ needs.check.outputs.version }}
          NOTES: ${{ needs.check.outputs.notes }}
        run: |
          printf '%s\n' "$NOTES" > notes.md
          gh release create "$TAG" dist/shutdowner.exe \
            --target "$GITHUB_SHA" --title "$TAG" --notes-file notes.md
```
Notes:
- When `check` sets `release=false`, `ci` is skipped, so `release` (which `needs` it) is skipped too. The run ends green with the "already released" log line.
- Values reach shell scripts only through `env:`, never as `${{ }}` inside `run:` text, so markdown in the notes cannot inject commands.
- `gh release create` with `--target` creates the tag `vX.Y.Z` on the merged commit.

**Verify:**
1. `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7` prints nothing.
2. Exercise the `check` shell logic in a scratch clone outside the repo: `git clone . /tmp/sd2-rel`, then `cd /tmp/sd2-rel` and save the body of the "Read and validate VERSION" step to `/tmp/check.sh`. Run it with `GITHUB_OUTPUT=/tmp/out bash /tmp/check.sh; cat /tmp/out` in each of these cases:
   - As is: expect `release=true` and `version=1.0.0`.
   - `git tag v1.0.0`: expect "v1.0.0 already released, nothing to do", `release=false`, exit 0.
   - `git tag -d v1.0.0; git tag v1.2.0`: expect the error "1.0.0 is not above the latest release v1.2.0", exit 1.
   - `echo 1.0 > VERSION`: expect the "must be MAJOR.MINOR.BUILD" error, exit 1.

   Remove `/tmp/sd2-rel` afterwards.
3. Run the awk from step 3 with `v=9.9.9`. It should print nothing, which is the case the workflow reports as a missing section.

**Commit:** `ci: release the VERSION on main as a tagged GitHub Release with shutdowner.exe`

## Step 5: Write AGENTS.md and CLAUDE.md

**Failing check first:** `ls AGENTS.md CLAUDE.md` fails.

**`CLAUDE.md`** is exactly:
```markdown
@AGENTS.md
```

**`AGENTS.md`** (full content):
````markdown
# Agent rules for Shutdowner

These rules apply to every agent (Claude Code, Codex, MegooColab and others) and
to people working in this repository. `CLAUDE.md` only imports this file. Change
the rules here and nowhere else.

## The project

A Go module (`shutdowner`, see `go.mod` for the Go version) that builds one
Windows executable, `cmd/shutdowner`. It runs as a Windows service and serves a
password-protected web UI for power actions, scheduled actions and volume.
Packages live in `internal/`. The release target is `windows/amd64`, but the
tests run on any OS.

## Commands

- `make test`: `go test ./...`
- `make vet`: `go vet` for the host and for `GOOS=windows`
- `make fmt`: `go fmt ./...`
- `make build-windows`: builds `dist/shutdowner.exe`

Before opening any PR, run `make fmt`, `make test` and `make vet`, and make sure
they pass. CI also fails on any file that `gofmt -l .` lists.

## How work is done

- Spec first: a design in `docs/superpowers/specs/`, then a step-by-step test-first
  plan in `docs/superpowers/plans/`, then one commit per plan step.
- Commit subjects use conventional prefixes: `feat:`, `fix:`, `docs:`,
  `refactor:`, `test:`, `style:`, `ci:`, `chore:`. Mark a breaking change with
  `!` (`feat!:`) or a `BREAKING CHANGE:` footer.

## Branches

- `main`: released code only. Never push to it directly. Only `develop` is
  merged into it, through a PR.
- `develop`: the integration branch. Create every `feat/<topic>`, `fix/<topic>`
  or `megoo/<task>` branch from `develop`, and open its PR against `develop`.
- `release/X.Y.Z`: created from `develop`, only to prepare a release (see below).
- There are no hotfixes directly on `main`. A fix goes through `develop` like
  everything else.

## CI and release workflows

- `.github/workflows/ci.yml` runs on every push to `main` and `develop`: the
  gofmt check, `make test`, `make vet` and `make build-windows`.
- `.github/workflows/release.yml` runs on every push to `main`:
  1. It reads `VERSION`, which must be `MAJOR.MINOR.BUILD`.
  2. If the tag `vX.Y.Z` already exists, it logs "already released" and stops.
     It ends green.
  3. Otherwise `VERSION` must be above the latest `v*` tag, and `CHANGELOG.md`
     must have a non-empty `## [X.Y.Z]` section. If either check fails, the run
     fails before anything is published.
  4. It runs CI, builds `dist/shutdowner.exe`, and creates the GitHub Release
     `vX.Y.Z` on the merged commit, with the exe attached and the CHANGELOG
     section as the release notes. This also creates the tag.
- Never create tags or GitHub Releases by hand. The workflow does it.

## Versioning

`VERSION` holds one line, `MAJOR.MINOR.BUILD`, such as `1.4.2`. The workflow
never changes it. The agent changes it, and only in a release PR. Feature and fix
branches never touch `VERSION` or add a CHANGELOG version section.

To choose the bump, read every commit since the last release and its diff.
Work out the highest category below. That part goes up by one, and every part
to its right goes back to 0.

- **MAJOR**: something that breaks an existing install. Examples:
  - a `SHUTDOWNER_*` key in `.env` renamed or removed, or its meaning changed
    (`internal/config/config.go`)
  - a CLI flag renamed or removed (`cmd/shutdowner/main.go`)
  - an HTTP route or JSON API changed or removed (`internal/web/server.go`)
  - the `schedule.json` format changed without a migration
    (`internal/action/store.go`)
  - a service change that needs `--uninstall-service` and `--install-service`
    again (`internal/winsvc/`)
- **MINOR**: a new user-visible feature or capability (usually `feat:`
  commits), with nothing breaking.
- **BUILD**: everything else, such as fixes, docs, refactors, tests and build or
  CI changes.

A commit prefix is a hint, not the answer. Always check the diff. A `fix:` that
renames an `.env` key is MAJOR. If nothing user-visible changed since the last
release, there is nothing to release.

## Release procedure

Only start this when a person asks for a release.

1. Find out what is being released:
   ```
   git fetch origin --tags
   git checkout develop
   git pull
   git tag -l 'v*' | sort -V | tail -n 1     # the last release, e.g. v1.0.0
   git log --oneline v1.0.0..develop
   git diff --stat v1.0.0..develop
   ```
   Read the diffs, starting with the breaking-change surfaces listed above.
2. Choose MAJOR, MINOR or BUILD using the rules above, and work out `X.Y.Z`.
3. `git checkout -b release/X.Y.Z develop`
4. Write `X.Y.Z` into `VERSION`.
5. In `CHANGELOG.md`, add `## [X.Y.Z] - YYYY-MM-DD` (today's date) above the
   previous version. Group the entries under `### Added`, `### Changed`,
   `### Fixed` and `### Removed`. Put any breaking change first, with what an
   existing install has to do.
6. Commit only those two files, with the subject `chore(release): X.Y.Z`.
7. Open a PR from `release/X.Y.Z` into `develop` and merge it once CI is green.
8. Open a PR from `develop` into `main` titled `Release X.Y.Z`. In the body,
   give the bump and why: name the commits that put it in that category.
9. When that PR is merged, the Release workflow publishes `vX.Y.Z`. Check the
   Actions run and the Releases page.

Never edit `VERSION` outside these steps, never push to `main`, and never
create a tag by hand. Merging `develop` into `main` without a new `VERSION` is
allowed. The workflow logs "already released" and publishes nothing.

`1.0.0` was set when the workflows were added (SD-2). Every version after it
follows the procedure above.
````

**Verify:** `cat CLAUDE.md` prints exactly `@AGENTS.md`. `grep -n 'chore(release)' AGENTS.md` matches. Every path that AGENTS.md names exists (`ls internal/config/config.go cmd/shutdowner/main.go internal/web/server.go internal/action/store.go internal/winsvc .github/workflows/ci.yml .github/workflows/release.yml VERSION CHANGELOG.md`).

**Commit:** `docs: agent rules for branching, CI, versioning and releases`

## Step 6: README "Releases & versioning" section

**Failing check first:** `grep -n '## Releases & versioning' README.md` prints nothing.

Insert this between the end of `## Development` (after its code block, which ends with `make run-dev`) and `## How it behaves`:
```markdown
## Releases & versioning

The current version is in `VERSION` (`MAJOR.MINOR.BUILD`), and each version's
notes are in `CHANGELOG.md`. Work is merged into `develop`. CI
(`.github/workflows/ci.yml`) runs on every push to `develop` and `main`. When
`develop` is merged into `main` with a new version in `VERSION`,
`.github/workflows/release.yml` tags `vX.Y.Z` and publishes a GitHub Release
with `shutdowner.exe` attached. The branching rules, how to choose the bump and
the release steps are in [AGENTS.md](AGENTS.md).
```

**Verify:** `grep -n '^## ' README.md` shows `Development`, then `Releases & versioning`, then `How it behaves`.

**Commit:** `docs: point the README at the release and versioning rules`

---

## Final verification
1. `gofmt -l .` prints nothing. `make test`, `make vet` and `make build-windows` pass.
2. `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7` prints nothing.
3. `git ls-remote --heads origin develop` equals `origin/main`.
4. Push the SD-2 branch and open its PR against **`develop`**. After the merge, the push to `develop` runs **CI**, which must go green. The Release workflow must not run, because it only triggers on `main`.
5. The first `develop → main` PR (a human merges it) must produce a Release run that creates `v1.0.0` with `shutdowner.exe` attached and the 1.0.0 CHANGELOG section as its notes.
6. A later `develop → main` merge with no `VERSION` change must log "v1.0.0 already released, nothing to do" and finish green.
