# SD-3 plan: first GitHub release (v1.0.0)

## Goal
Publish GitHub Release `v1.0.0`, with `shutdowner.exe` attached and the 1.0.0 CHANGELOG section as its notes. To do that, merge `develop` into `main` through a PR titled "Release 1.0.0" that the human merges.

## Architecture
No code changes. `.github/workflows/release.yml` (already on `develop`) runs on every push to `main`. Its jobs run in order: **Check version**, which reads `VERSION`, skips if the tag `v1.0.0` already exists, fails if the version is not above the latest `v*` tag, and extracts the `## [1.0.0]` section from `CHANGELOG.md`. Then **CI**, which is `ci.yml` through `workflow_call`. Then **Publish release**, which runs `make build-windows` and then `gh release create v1.0.0 dist/shutdowner.exe --target $GITHUB_SHA --notes-file notes.md`, and that call also creates the tag. The agent only opens the PR and checks the result. It never pushes to `main` and never creates tags or releases by hand.

## File map
No repository files are created or changed. The work is all git and `gh` operations:
- PR `develop` → `main`, titled `Release 1.0.0` (created in step 2).
- Release `v1.0.0` and tag `v1.0.0`, both created by the workflow after the human merges the PR.

State checked during planning: `origin/develop` = 0d8333b, `origin/main` = fab589f, 7 commits in `main..develop`, no `v*` tags, and `VERSION` on develop = `1.0.0`. CHANGELOG has `## [1.0.0] - 2026-11-06`, with an Added list and Fixed: modern-standby sleep.

## Steps

### 1. Pre-flight: confirm develop is ready to release
- `git fetch origin --tags`
- `git tag -l 'v*'`: expect empty output. If `v1.0.0` exists, stop and report "already released".
- `git show origin/develop:VERSION`: expect `1.0.0`.
- `git show origin/develop:CHANGELOG.md | grep -n '^## \[1.0.0\]'`: expect `## [1.0.0] - 2026-11-06`.
- `git ls-tree origin/develop .github/workflows/`: expect `ci.yml` and `release.yml`.
- `gh run list --branch develop --workflow ci.yml --limit 5`: expect the latest run on commit 0d8333b (or the current develop head) to be `completed success`. If it is red or missing, stop and report it. The fix goes through a branch from develop.
- `gh pr list --base main --head develop --state open`: expect no open PR yet. If one exists, reuse it in step 2.
- Commit: none (read-only step).

### 2. Open the "Release 1.0.0" PR from develop into main
- `gh pr create --base main --head develop --title 'Release 1.0.0' --body-file <tmpfile outside the repo>`
- The body contains:
  - "First tagged release of Shutdowner. Merging this publishes `v1.0.0` through the Release workflow, which builds `shutdowner.exe` and creates the tag and GitHub Release."
  - Version: 1.0.0, no bump. It was set in SD-2 (f168804 `chore: start versioning at 1.0.0`, plus 3463dfb, the release workflow). There is no earlier tag, so the version check passes.
  - A summary of the 1.0.0 changelog: web-page shutdown/restart/sleep/hibernate with a countdown, the Windows service and CLI flags, security (sessions, CSRF, rate limiting, loopback bind), scheduled actions, remote volume control, icons, the rotating log, CI/Release workflows, and the modern-standby sleep fix.
  - A note that `ci.yml` has no `pull_request` trigger, so the PR shows no checks of its own. The gates are green CI on develop and the CI job inside the release run.
  - It ends with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
- Do not merge. Report the PR URL to the human. The human merges it.
- Acceptance: `gh pr view --json title,baseRefName,headRefName,state` shows `Release 1.0.0`, `main`, `develop`, `OPEN`.

### 3. After the human merges: verify the Release run and the published release
- Wait for the PR to be merged (`gh pr view <n> --json state,mergeCommit`). Note the merge commit SHA.
- `gh run list --branch main --workflow release.yml --limit 3`, then `gh run watch <id>` or `gh run view <id>`. Expect the jobs Check version → CI → Publish release, all green.
- `gh release view v1.0.0 --json tagName,targetCommitish,assets,body,url`: expect `tagName` = `v1.0.0`, the asset `shutdowner.exe`, and a body equal to the CHANGELOG 1.0.0 section, starting "The first tagged release.".
- `git fetch origin --tags; git rev-list -n 1 v1.0.0`: this must equal the merge commit SHA.
- On failure: report the failing job and its log (`gh run view <id> --log-failed`). Do not tag or release by hand. Any fix goes through a branch from develop, its PR, and a new develop→main merge.
- Report the release URL, the run URL and the tag SHA.

## Final verification
- `gh release view v1.0.0` shows the release with `shutdowner.exe` and the 1.0.0 notes.
- `git rev-list -n 1 v1.0.0` equals the merge commit of the Release 1.0.0 PR.
- The Release workflow run on `main` is green in all three jobs.
