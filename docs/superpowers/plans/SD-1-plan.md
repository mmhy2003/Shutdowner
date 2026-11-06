# SD-1: Implementation plan for the Shutdowner Knowledge Base

## Goal
Fill the empty **SD — Shutdowner** KB space with 11 linked pages: a root index and 10 pages in 4 folders. Every key claim is checked against the code at a recorded commit.

## Architecture
This task only writes documentation. It produces no code. Each step reads the repo files it lists without changing them (Read tool, `rg`, `grep`, `git log/show`, `ls`). It checks every claim it is about to write, then saves **one** KB page with `kb_write`, or two pages if the content has to be split. Pages 1–10 come first. In their "Related pages" sections, other pages are named in bold by their exact title. Page 0 (the index) comes next. A final pass turns every bold name into a real `[Title](document_id)` link and runs the verification.

## Fixed facts (all steps)
- **KB space:** `SD — Shutdowner`, `space_id = d57d03bf-37de-4b2b-80cd-87898b77754a`. When this plan was written it held only the folder `Designs & Plans` (id `f97a13e9-39e3-47c3-bf11-d08bf11e79d4`). **Do not touch that folder.**
- **Folder paths** (`folder_path` in `kb_write`; a missing folder is created): `Product`, `Architecture`, `Features`, `Operations & Status`. The index sits at the root, so it has no `folder_path`.
- **Exact page titles** (keep them identical everywhere):
  0. `Shutdowner: Start here` (root)
  1. `Product overview & users` (Product)
  2. `System architecture & package map` (Architecture)
  3. `Security model` (Architecture)
  4. `HTTP API & web UI` (Architecture)
  5. `Power actions & countdown` (Features)
  6. `Scheduled actions` (Features)
  7. `Volume control` (Features)
  8. `Install, configure & run` (Operations & Status)
  9. `Build, test & development workflow` (Operations & Status)
  10. `Manual verification & known issues` (Operations & Status)
- **Commit:** run `git rev-parse HEAD` at the start of each step and record the full SHA in Sources. When this plan was written it was `f162a7ae47d60c2598ae0c8079a426c7168a2967`.
- **The repository is read-only.** Make no edits, create no files and make no commits. The "commit" of a step is the saved KB page. Report its exact title and `document_id` in `complete_step`, because steps 11 and 12 need them.
- **Read-only command rules:** combine commands only with `;` or `|`. Do not use `&&`, `||`, redirection, `$`, parentheses, braces, backslashes or double quotes. Put patterns in single quotes. Prefer the Read tool for whole files.
- **No secrets.** List env vars **by name only** (`SHUTDOWNER_PASSWORD_HASH`, `SHUTDOWNER_SESSION_SECRET`, `SHUTDOWNER_LISTEN`, `SHUTDOWNER_DELAY_SECONDS`, `SHUTDOWNER_SESSION_TTL`, `SHUTDOWNER_LOG_FILE`). Never copy a hash, secret or token value from `.env.example`, the tests or the docs. Placeholder shapes such as `'$2a$12$…'` are not allowed either.
- **Docs vs. code:** the code wins. Write each finding as `- **<doc path>** says X; the code (<code path>) does Y.` One case is already known: the volume spec originally had `Set` return nothing. The code now returns the level the device actually reached (commits `d8b2fa3`, `523cc36`). Check this against `internal/volume/controller.go`.
- **Windows-only code** (`*_windows.go`) is described from reading the source and is marked "not covered by automated tests".
- **Size:** aim for 600–1,800 words. If a page goes well beyond 1,800 words, split it into `<Title> (1/2)` and `<Title> (2/2)` in the same folder, and report both IDs.

## Page template (pages 1–10)
```markdown
# <Exact page title>

> <One-sentence summary. It is reused word for word in the index map.>

## <content sections listed in the step>

## Docs vs. code
- <each finding, or "None found at the recorded commit.">

## Related pages
- **<Exact page title>**: <why it is related>

## Sources
- Files read: <paths>
- Commit: <full SHA>
```

## Verified anchors (checked while this plan was written; re-check them in each step)
- Routes are in `internal/web/server.go` `Routes()`:
  - `GET /{$}` (dashboard) and `GET`/`POST /login`;
  - `POST /logout`, `GET /api/status`, `POST /api/action|abort|dismiss`, `GET`/`POST /api/volume`;
  - `GET /healthz` (no auth) and `GET /favicon.ico`, `GET /static/`.
  - Every POST is wrapped in `limitBody(maxRequestBody)`, which is 4 KiB (`internal/web/middleware.go:33`). The whole tree is wrapped in `securityHeaders(recoverPanic(...))`.
  - The session cookie is named `shutdowner_session`.
- Config is in `internal/config/config.go`:
  - default listen `127.0.0.1:8080`;
  - default delay 45 s, range 0–3600;
  - default session TTL `168h`;
  - default log file `shutdowner.log` beside the executable;
  - the session secret is hex and must decode to at least 32 bytes;
  - the hash is checked against a bcrypt shape and its cost;
  - the listen address must be loopback or `localhost` unless `--allow-public-bind` is passed.
- CLI flags are in `cmd/shutdowner/main.go`: `--config`, `--console`, `--fake-power`, `--fake-volume`, `--allow-public-bind`, `--init`, `--hash-password`, `--install-service`, `--uninstall-service`, plus the internal `--audio-helper` (`volume.HelperFlag`).
  - `serviceArgs()` passes on only `--config` (made absolute) and `--allow-public-bind`.
  - The minimum password length is 12.
  - `schedule.json` sits beside the `.env` file.
- Rate limits (`internal/auth/ratelimit.go`): 5 per IP and 20 globally per 15 minutes.
- Logs (`internal/logging/rotate.go`): the file rotates at `DefaultMaxBytes`, which must be checked and is expected to be 5 MB, and `DefaultKeep = 2`.
- `action.TickInterval` = 1 s.
- Makefile targets: `test`, `vet` (host and `GOOS=windows`), `fmt`, `build-windows` (`dist/shutdowner.exe`, `CGO_ENABLED=0`, `-ldflags "-s -w"`), `run-dev` (`--console --fake-power --config ./.env.dev`), `icon` (`tools/mkicon` ×3 plus `akavel/rsrc@v0.10.2` → `cmd/shutdowner/rsrc_windows_amd64.syso`), `clean`.
- README sections: Build, Install on the Windows PC, Configuration, Command line, Development, How it behaves (Volume, Scheduling), Security notes (`.env` file permissions on Windows), Verifying a real install.

## File map
No repository files are created or modified. The KB pages are created as follows:

| # | KB folder | Page | Main sources |
|---|---|---|---|
| 1 | Product | Product overview & users | README.md, the 3 specs (Purpose/Decisions/Out of scope) |
| 2 | Architecture | System architecture & package map | cmd/shutdowner/*, internal/winsvc, internal/config, all internal/* package headers, go.mod |
| 3 | Architecture | Security model | internal/auth/*, internal/web/middleware.go, auth_handlers.go, config.go, README Security notes |
| 4 | Architecture | HTTP API & web UI | internal/web/* (handlers, templates, static/app.js) |
| 5 | Features | Power actions & countdown | internal/power/*, internal/action/manager.go |
| 6 | Features | Scheduled actions | internal/action/when.go, store.go, manager.go, the scheduled-actions spec |
| 7 | Features | Volume control | internal/volume/*, internal/web/volume_handlers.go, the volume spec |
| 8 | Operations & Status | Install, configure & run | README, .env.example (names only), main.go, config.go, internal/logging |
| 9 | Operations & Status | Build, test & development workflow | Makefile, tools/mkicon, *_test.go, git log |
| 10 | Operations & Status | Manual verification & known issues | README "Verifying a real install", the specs' "Not covered" sections, the Docs vs. code findings of pages 1–9 |
| 0 | (root) | Shutdowner: Start here | pages 1–10 |

## Steps

### 1. Write "Product overview & users"
(See the step detail. It is reproduced in the steps array.)

### 2. Write "System architecture & package map"
### 3. Write "Security model"
### 4. Write "HTTP API & web UI"
### 5. Write "Power actions & countdown"
### 6. Write "Scheduled actions"
### 7. Write "Volume control"
### 8. Write "Install, configure & run"
### 9. Write "Build, test & development workflow"
### 10. Write "Manual verification & known issues"
### 11. Write the index "Shutdowner: Start here"
### 12. Link pass and final verification

The full details of each step are in the `steps` array. Each one lists the sources to read, the sections to write, the claims to check (the "test") and the acceptance criteria.

## Final verification
1. `kb_list(space_id)` and `kb_list` for each folder show exactly 11 new pages (more if a page was split) with the exact titles above. `Designs & Plans` is unchanged.
2. `kb_read` on every page: each `[Title](id)` link resolves to a page with that title, and no bold placeholder is left in "Related pages".
3. The index map's summaries match each page's blockquote word for word.
4. `kb_search` for `$2a$`, `$2b$`, `SESSION_SECRET='`, `password=` and 64-character hex strings within the SD space finds no secret values.
5. Each page's Sources records the full commit SHA.
