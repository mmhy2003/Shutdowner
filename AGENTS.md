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
