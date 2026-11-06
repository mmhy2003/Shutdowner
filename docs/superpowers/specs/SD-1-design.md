# SD-1: Shutdowner Knowledge Base (design)

## Goal
Fill the empty **SD — Shutdowner** KB space (`space_id = d57d03bf-37de-4b2b-80cd-87898b77754a`) with about 10 linked pages. Together they explain what Shutdowner is, how it is built, how each feature works, and how to install, run, build and verify it. Every key claim is checked against the code at a recorded commit (currently `f162a7ae47d60c2598ae0c8079a426c7168a2967`).

## Decisions
- **Coverage: Standard, about 10 pages.** The human chose this over Compact (5 pages, too shallow for the security model and the volume helper) and Detailed (about 16 pages with one page per package and a per-endpoint reference, which is too much for a repo of about 5k lines).
- **Same conventions as the existing SalsaWeb, OdooShield and SuperLiveWeb KBs**, so the spaces read alike:
  - a root "Start here" index with a page map and a glossary;
  - folders by concern;
  - every page has a one-sentence summary, a "Docs vs. code" section, a "Related pages" section and a "Sources" section with the commit SHA;
  - links use the form `[Title](document_id)`.
- **The code wins over the docs.** When the README or a spec disagrees with the code, the page states what the code does and records the difference under "Docs vs. code".
- **No secrets.** Env vars are listed by name only. The pages contain no hash, secret, token or password value.
- **Documentation only.** The repository is not changed. Pages are written with `kb_write`.

## Architecture: page set
Root (no folder):
0. **Shutdowner: Start here**: what it is, the page map with each page's summary, a glossary (action, countdown, deadline, missed action, schedule.json, audio helper, session 0, fake power/volume, loopback bind), and reading orders for a developer and for an operator.

`Product/`
1. **Product overview & users**: the purpose (power control of one personal Windows PC over the internet) and who uses it. Covers the four actions, scheduling and volume, and the non-goals, such as waking the PC. Also explains why a countdown the app owns is used instead of `shutdown /t`.

`Architecture/`
2. **System architecture & package map**: the single binary and how it runs as a service or in a console (`cmd/shutdowner`, `internal/winsvc`); the request path from Cloudflare Tunnel to `127.0.0.1` to `net/http`; what each of the 10 `internal/*` packages does and how they depend on each other; how `.env` and the log path are resolved from the executable's location; and the interfaces with Windows and non-Windows builds (`power`, `volume`, `sysinfo`).
3. **Security model**:
   - bcrypt password hashing;
   - stateless HMAC session cookies and their TTL;
   - per-session CSRF tokens;
   - the sliding-window login rate limiter (5 per IP and 20 globally per 15 minutes) and its use of `CF-Connecting-IP`;
   - response headers and limits on request bodies;
   - the loopback-only listener and what `--allow-public-bind` weakens;
   - the ACL caveat for `.env` on Windows.
4. **HTTP API & web UI**:
   - the routes in `internal/web/server.go` (dashboard, login and logout, `/api/status`, `/api/action`, `/api/abort`, `/api/dismiss`, `/api/volume` GET and POST, `/healthz`, static files);
   - the request and response shapes at the level of fields;
   - the embedded templates and static assets;
   - the client polling loop in `app.js`, including the "PC is offline" behaviour.

`Features/`
5. **Power actions & countdown**:
   - `internal/power`: the use of shutdown.exe and powrprof, the arguments built, and capability detection for hibernate and for sleep on modern standby machines;
   - the fake controller;
   - the manager state machine in `internal/action`, abort, and the graceful versus forced (`/f`) option.
6. **Scheduled actions**: now, after a delay, or at a set time; resolving a schedule into a deadline (`when.go`, 7-day cap, PC clock against browser clock); the tick loop; persistence in `schedule.json` (`store.go`), which is saved when the action is claimed; restore at startup; and the rule that an action whose deadline passed more than 5 minutes ago is skipped and reported, along with dismissing it.
7. **Volume control**: why the service runs in session 0 and must start a copy of itself into the signed-in user's session (`helper_windows.go`, `CreateProcessAsUser`); the `--audio-helper` JSON wire protocol; COM step-based volume; the interaction rule that dragging the slider while muted unmutes; how helper failures are classified; and the disabled state when nobody is signed in.

`Operations & Status/`
8. **Install, configure & run**: the install steps; the `.env` keys (names, defaults, ranges) and why values are single-quoted for godotenv; all CLI flags, including which ones `--install-service` passes on; the Cloudflare Tunnel setup; and the log file, which rotates at 5 MB and keeps 2 files (`internal/logging`).
9. **Build, test & development workflow**:
   - Makefile targets (`test`, `vet` for both platforms, `build-windows`, `run-dev`, `icon`) and the icon/favicon/logo pipeline through `tools/mkicon` and rsrc;
   - which tests run on Linux, using the fakes, and what cannot be tested off Windows;
   - the workflow of a dated spec, then a TDD plan, then one commit per task, merged into `main`;
   - the history of the three features from git (2026-08-01/02, 08-02, 08-06/07);
   - the absence of a version scheme, tags and CI.
10. **Manual verification & known issues**: the 18-item checklist for a real install, with the riskiest items (lock screen, sign-out, fast user switching); known limits and risks (the `.env` ACL, the `--allow-public-bind` trade-offs, the dashboard logo `height` that must be kept in sync by hand, no CI and no versioning); and every "Docs vs. code" finding collected in one place.

If a page goes well beyond about 1,800 words, it is split into "(1/2)" and "(2/2)" pages in the same folder.

## Data and interfaces
- KB folders, created by `kb_write` through `folder_path`: `Product`, `Architecture`, `Features`, `Operations & Status`. The index sits at the root.
- Page template: title, a one-sentence summary in a blockquote (reused word for word in the index map), content sections, "Docs vs. code", "Related pages" and "Sources" (files read and the commit SHA).
- Pages 1–10 are written first. Page 0 comes next. A final pass replaces the bold placeholders in "Related pages" with real document-ID links.

## Error handling and edge cases
- If a spec describes behaviour that a later commit changed (for example, `Set` now returns the volume the device actually reached), the page describes the current code and records the difference.
- Code that runs only on Windows and cannot be run here is described from source reading and marked "not covered by automated tests".

## Testing (verification)
- Each page's key claims, such as numbers, routes, flags, defaults, limits and file paths, are checked with `rg` or file reads against the recorded commit.
- A final check confirms that:
  - all 11 pages exist;
  - every link resolves through `kb_read`;
  - the index map matches each page's summary;
  - a search finds no secret-looking values on any page.

## Out of scope
- Any change to the repository.
- A reference page per package or per endpoint.
- Copying the plans in `docs/superpowers/plans/` task by task (they are summarised in the feature history only).
- Waking the PC (Wake-on-LAN), which the product itself does not do.
