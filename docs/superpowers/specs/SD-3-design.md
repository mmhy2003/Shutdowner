# SD-3: First GitHub release (v1.0.0)

## Goal
Publish Shutdowner's first GitHub Release, `v1.0.0`, with `shutdowner.exe` attached and the 1.0.0 CHANGELOG section as the notes. The Release workflow does this when `develop` is merged into `main`.

## Current state (checked in the repo)
- `origin/develop` (0d8333b) has `VERSION` = `1.0.0`, `CHANGELOG.md` with a `## [1.0.0] - 2026-11-06` section, `.github/workflows/release.yml` and `ci.yml` (with `workflow_call`), and the release rules in `AGENTS.md`.
- `origin/main` (fab589f) has none of these yet.
- There are no `v*` tags. That makes this the first release, and the version check passes because there is no earlier tag to compare against.
- `AGENTS.md` says: "`1.0.0` was set when the workflows were added (SD-2)." So we skip the bump steps of the release procedure (steps 2 to 7: the `release/X.Y.Z` branch, editing `VERSION` and `CHANGELOG.md`, the `chore(release)` commit). Only steps 8 and 9 apply.

## Decisions
- **Version 1.0.0, no bump.** It was set in SD-2, and nothing has been released before it.
- **No file changes.** The changelog already describes everything built so far and is dated today.
- **The human merges the Release PR** (answer: "I'll merge it"). The agent opens it and checks the result afterwards. The agent never pushes to `main` and never creates tags or releases by hand.
- Rejected: an agent merge (merging publishes the release publicly), and a `release/1.0.0` branch (there is nothing to commit).

## Steps
1. `git fetch origin --tags`. Confirm that there are still no `v*` tags, that `develop`'s CI run on 0d8333b is green, and that `VERSION`/`CHANGELOG.md` on `develop` are as above.
2. Open a PR from `develop` into `main` titled **Release 1.0.0**. The body says this is the first tagged release, that 1.0.0 was set in SD-2 (f168804 `chore: start versioning at 1.0.0`, 3463dfb release workflow), summarizes the 1.0.0 changelog, and notes that merging publishes `v1.0.0`.
3. The human reviews and merges the PR.
4. The agent checks the Release workflow run on `main` (Check version, then CI, then Publish release) and the Releases page: tag `v1.0.0` points at the merge commit, `shutdowner.exe` is attached, and the notes match the CHANGELOG section. The agent reports the links.

## Error handling
- If CI or the release run fails, nothing is published, or the run fails before `gh release create`. The agent reports the failing step and log. Any fix goes through a branch from `develop` and its PR, and then a new develop→main merge. No hand-made tags.
- If `v1.0.0` already exists, the workflow logs "already released" and the agent reports that.
- `ci.yml` has no `pull_request` trigger, so the PR shows no checks of its own. Green CI on `develop` and the CI job inside the release run are the gates.

## Testing
No code changes. Verification is the green Release workflow run and the published release that the agent checks in step 4.

## Out of scope
Code or changelog edits, code signing, an installer, and changes to the workflows.