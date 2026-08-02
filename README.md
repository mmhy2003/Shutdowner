# Shutdowner

Shut down, restart, sleep or hibernate a Windows PC from a web page, over the
internet, behind a password.

A single Go binary runs as a Windows service on the target PC and serves a small
web UI. Cloudflare Tunnel puts it on a subdomain without opening any port on
your router: the app itself only ever listens on `127.0.0.1`.

Waking the machine is out of scope — the app runs on the PC it controls, so it
cannot start one that is already off.

## Build

```
make build-windows      # produces dist/shutdowner.exe
make test               # runs the suite on any platform
```

## Install on the Windows PC

1. Copy `shutdowner.exe` to `C:\Program Files\Shutdowner\`.
2. Open an **elevated** Command Prompt in that directory.
3. `shutdowner.exe --init` — writes `.env` with a fresh session secret.
4. `shutdowner.exe --hash-password` — type a long password twice, then paste the
   printed line into `.env` as `SHUTDOWNER_PASSWORD_HASH='<hash>'`.
5. `shutdowner.exe --install-service` — installs, sets auto-start and
   restart-on-failure, and starts the service.
6. Install [cloudflared](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/),
   create a tunnel, and route your subdomain to `http://127.0.0.1:8080`.

To remove it: `shutdowner.exe --uninstall-service`.

## Configuration

`.env` lives beside the executable. Paths are resolved from the executable's
location, never the working directory, because a service starts in
`C:\Windows\System32`. Values in `.env` must be single-quoted, because the
file is read with godotenv, which otherwise expands `$VAR` in unquoted
values — and a bcrypt hash is mostly dollar signs.

| Key | Default | Meaning |
|---|---|---|
| `SHUTDOWNER_PASSWORD_HASH` | — | Required. bcrypt hash from `--hash-password`. |
| `SHUTDOWNER_SESSION_SECRET` | — | Required. 32 random bytes, hex. Changing it signs every device out. |
| `SHUTDOWNER_LISTEN` | `127.0.0.1:8080` | Must be loopback unless `--allow-public-bind`. |
| `SHUTDOWNER_DELAY_SECONDS` | `45` | Countdown before an action fires. 0-3600. |
| `SHUTDOWNER_SESSION_TTL` | `168h` | How long a login lasts. |
| `SHUTDOWNER_LOG_FILE` | `shutdowner.log` beside the exe | Rotates at 5 MB, keeps 2. |

## Command line

```
shutdowner.exe                      run; detects service vs console context
shutdowner.exe --init               write a starter .env; refuses to overwrite
shutdowner.exe --hash-password      prompt for a password, print its hash
shutdowner.exe --install-service    install and start the service
shutdowner.exe --uninstall-service  stop and remove the service
shutdowner.exe --console            run in the foreground, log to stdout
shutdowner.exe --config PATH        alternate .env location
shutdowner.exe --allow-public-bind  permit a non-loopback listen address
shutdowner.exe --fake-power         development only: log actions, do not execute
```

## How it behaves

- Clicking an action opens a confirm dialog, then starts a countdown you can
  abort. The countdown is owned by the app, not by `shutdown /t`, because
  Windows cannot cancel a pending sleep or hibernate — this way Abort works
  identically for all four actions.
- "Close apps gracefully" is **unchecked** by default, meaning `/f` is passed.
  Without it, an app with unsaved work blocks the shutdown and the PC silently
  stays on. Unsaved work is lost when forcing.
- Hibernate is disabled in the UI when the machine does not support it, which is
  common after `powercfg /h off`.
- After a shutdown fires, the page shows "PC is offline" rather than a
  connection error. Being unreachable is the confirmation.

## Security notes

- The password is stored hashed. That does not protect against someone who can
  already read `.env` — they own the machine — but it does protect against the
  leak paths that actually happen: screenshots, backups, an accidental commit.
- Sessions are stateless signed cookies, so they survive the reboots this app
  exists to perform.
- Five failed logins per IP per 15 minutes, and 20 globally, then HTTP 429.
- The listener refuses a non-loopback address unless you explicitly opt in.

## Verifying a real install

Automated tests cover everything except the Win32 calls and the service
wrapper, which cannot run off Windows. After installing, walk this list:

1. `--install-service`, reboot the PC, and confirm the service is running before
   anyone logs in.
2. Reach the subdomain from a phone on cellular with Wi-Fi off.
3. Six wrong passwords in a row produce a rate-limit message.
4. Each of the four actions completes.
5. Abort cancels each of the four actions.
6. With `powercfg /h off`, the hibernate button is disabled and the API rejects
   the action.
7. With an unsaved Notepad open: a forced shutdown completes; a graceful one is
   blocked by Windows and the PC is still up afterwards.
8. `shutdowner.log` is written, and rotates once it passes 5 MB.
