# SD-2: develop branch, CI & release workflows, agent rules

## Goal
Shutdowner gets a `develop` integration branch, GitHub Actions CI on `main` and `develop`, and a Release workflow that publishes a tagged GitHub Release with `shutdowner.exe` whenever a new version lands on `main`. The version lives in a `VERSION` file. The agent chooses MAJOR, MINOR or BUILD by reading what changed since the last release. `CLAUDE.md` and `AGENTS.md` hold the rules the agent follows.

## Decisions
- **Version format `MAJOR.MINOR.BUILD`** in a root `VERSION` file (one line, e.g. `1.0.0`). Tags are `vMAJOR.MINOR.BUILD`. Today the repo has no version and no tags.
- **The agent decides the bump, not the workflow** (your answer). The workflow never computes or commits a version. It only releases what `VERSION` says.
- **The bump happens once, in the develop → main release PR** (your answer). Feature and fix branches never touch `VERSION`. This avoids conflicts between parallel PRs.
- **CHANGELOG.md** gets one section per release, written by the agent in the release PR. That section becomes the GitHub Release notes.
- Rejected options: the workflow bumping BUILD on its own, version bumps worked out from commit messages, and a bump in every feature PR.
- **Initial version `1.0.0`**: the app already ships power control, schedules and volume. Say so if you prefer `0.1.0`.

## Architecture / files
| File | Purpose |
|---|---|
| `develop` branch | Created from `main` at `fab589f` and pushed to origin. From then on, feature branches (`feat/*`, `fix/*`, `megoo/*`) branch from and merge into `develop`. Only `develop` merges into `main`. |
| `.github/workflows/ci.yml` | Triggers: `push` to `main` and `develop`, plus `workflow_call` so Release can reuse it. One `ubuntu-latest` job: `actions/setup-go` with `go-version-file: go.mod`, a `gofmt -l` check, then `make test`, `make vet` (which also vets with `GOOS=windows`) and `make build-windows`. |
| `.github/workflows/release.yml` | Trigger: `push` to `main`. `permissions: contents: write`. Steps are listed below. |
| `VERSION` | `1.0.0` |
| `CHANGELOG.md` | Keep-a-Changelog style. The first entry, `## [1.0.0] - <date>`, summarises the existing features. |
| `AGENTS.md` | Full agent rules: branching, CI/release, versioning and the release procedure. |
| `CLAUDE.md` | Contains `@AGENTS.md` (a Claude Code import), so one file is the single source of truth and the two cannot drift. |
| `README.md` | Short "Releases & versioning" section that points to AGENTS.md. |

**Release workflow steps:**
1. Read `VERSION` and check it matches `^[0-9]+\.[0-9]+\.[0-9]+$`.
2. If tag `vX.Y.Z` already exists, log "already released" and stop successfully. This covers merges to `main` that carry no bump.
3. Check that the version is greater than the latest `v*` tag, compared with `sort -V`.
4. Extract the `## [X.Y.Z]` section from `CHANGELOG.md`. If it is missing, fail.
5. Run the CI job through `uses: ./.github/workflows/ci.yml`; the release job `needs` it.
6. Run `make build-windows`.
7. Run `gh release create vX.Y.Z dist/shutdowner.exe --target <sha> --title vX.Y.Z --notes-file <section>`. This also creates the tag.

## Versioning rules (written into AGENTS.md)
To pick the bump, the agent reads `git log <last-tag>..develop` plus the diffs. The highest category found wins, and lower parts reset to 0:
- **MAJOR**: a breaking change for an existing install. Examples: a renamed or removed `.env` key or CLI flag, a changed HTTP API or route, a changed `schedule.json` format without migration, a service change that needs a reinstall.
- **MINOR**: a new user-visible feature or capability (typically `feat:` commits), with nothing breaking.
- **BUILD**: only fixes, docs, refactors, tests or build/CI changes.

## Release procedure (AGENTS.md)
1. Only when a human asks for a release: branch `release/X.Y.Z` from `develop`, update `VERSION` and `CHANGELOG.md`, and commit with the subject `chore(release): X.Y.Z`.
2. Merge into `develop`, then open a `develop → main` PR. The bump reason goes in the PR body.
3. Never create tags by hand, never push to `main` directly, and never edit `VERSION` outside a release.

AGENTS.md also records the existing conventions: the spec-first workflow, conventional commit prefixes, and running `make test` / `make vet` before a PR.

## Rollout order
1. Push `develop` from `main`.
2. The SD-2 PR, holding the workflows, VERSION, CHANGELOG and the agent files, targets `develop`. CI runs there.
3. A `develop → main` PR produces the first release, `v1.0.0`.

## Error handling / edge cases
- An invalid `VERSION`, a version not above the latest tag, or a missing CHANGELOG section fails the Release workflow before anything is published.
- If the tag already exists, the workflow does nothing. Re-running is safe.
- If CI fails, nothing is released.

## Testing
- CI is checked on the SD-2 PR branch: the push to `develop` must go green.
- Release is checked by the first `develop → main` merge, which must produce `v1.0.0` with the exe attached.
- The skip path is checked by merging a no-bump change, which should log "already released". There are no Go code changes, so no new unit tests.

## Out of scope
- A `--version` flag or embedding the version in the binary through `-X` ldflags. This can be a follow-up.
- Code signing, installers, PR-trigger CI, branch-protection settings (done in GitHub settings by hand), and hotfixes directly on `main`.