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
