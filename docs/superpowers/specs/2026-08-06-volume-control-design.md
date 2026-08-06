# Shutdowner — Remote Volume Control

**Date:** 2026-08-06
**Status:** Approved design

## Purpose

Add a mute toggle and a volume slider to the dashboard, controlling the master
volume of the Windows PC's default playback device from the same
password-protected page that already shuts it down.

Scope is the default playback device only. Per-application mixing, input
devices, and device selection are out of scope.

## The constraint that shapes everything

Every action Shutdowner performs today works from session 0, where a
LocalSystem service lives: `shutdown.exe`, `SetSuspendState` and
`GetPwrCapabilities` are all session-agnostic. Nothing in the codebase has ever
needed to reach the interactive user's session.

Volume is different. Windows audio endpoints are per-session, and session 0
isolation means a service has no endpoint of its own. Calling
`IAudioEndpointVolume` from the service would fail, or move a volume nobody can
hear. The speakers belong to session 1.

Volume control therefore requires running code inside the logged-in user's
session. That single fact drives the whole design.

## Decisions

| Question | Decision |
|---|---|
| Session bridge | Spawn a helper into the active console session |
| Helper form | The same binary under a hidden `--audio-helper` subcommand |
| Audio API | `IAudioEndpointVolume` via raw COM |
| Freshness | Read on dashboard load and after each change, not on the status poll |
| Mute/slider | Moving the slider clears mute; muting preserves the level |
| Scope | Master volume, default playback device |
| Confirmation | None — volume is instantly reversible |
| Nobody signed in | Controls disable with a reason, like Hibernate |

## Architecture

```
  browser  ──▶  /api/volume  ──▶  volume.Controller        session 0, LocalSystem
                                        │
                          WTSGetActiveConsoleSessionId()   which session is at the screen?
                          WTSQueryUserToken(id)            a primary token for that user
                                        │
                          exec.CommandContext(self, "--audio-helper", op)
                            SysProcAttr{Token: tok, HideWindow: true}
                                        │
                                        ▼
                          shutdowner.exe --audio-helper    session 1, as the user
                            CoInitializeEx
                            → MMDeviceEnumerator
                            → GetDefaultAudioEndpoint(eRender, eMultimedia)
                            → IAudioEndpointVolume
                            prints one line of JSON, exits
```

### Why the spawn side is cheap

`syscall.SysProcAttr` on Windows carries a `Token` field, and
`syscall/exec_windows.go` calls `CreateProcessAsUser` whenever it is set. So
`os/exec` handles process creation, pipe setup, stdout capture and waiting. The
service-side code is roughly fifteen lines to obtain a token.

`WTSGetActiveConsoleSessionId`, `WTSQueryUserToken` and `DuplicateTokenEx` are
all present in `golang.org/x/sys/windows` v0.47.0, which is already a
dependency. **No fourth dependency is introduced.**

### Why the helper side is expensive

Go has no bindings for `IAudioEndpointVolume`, and the three-dependency rule
rules out adding one. The helper therefore does raw COM: hand-written GUIDs,
vtable structs, and `syscall.SyscallN` calls at fixed vtable offsets for
`GetMasterVolumeLevelScalar`, `SetMasterVolumeLevelScalar`, `GetMute` and
`SetMute`.

This is the riskiest code in the repository. It cannot be compiled and run
anywhere except the target machine — only cross-compiled and vetted. A wrong
vtable index does not fail to build; it calls the wrong method at runtime.

A cheaper variant was considered and rejected: sending `VK_VOLUME_MUTE` /
`VK_VOLUME_UP` / `VK_VOLUME_DOWN` through `keybd_event` is three lines instead
of two hundred, but moves volume only in fixed steps, cannot set an absolute
level, and cannot read the current one. That kills both the slider and the
read-on-load behaviour, leaving only a mute button.

## Package layout

```
internal/volume/
    controller.go      Controller interface, State, sentinel errors
    apply.go           the slider-unmutes rule and clamping — pure
    protocol.go        the service↔helper JSON line — pure
    fake.go            test double; also backs the --fake-volume dev flag
    unsupported.go     //go:build !windows
    windows.go         //go:build windows — token acquisition and spawn
    helper_windows.go  //go:build windows — COM; runs only in the user session
    helper_other.go    //go:build !windows — stub so cmd/ still builds
```

`volume` depends on nothing but the standard library and `x/sys/windows`. `web`
depends on `volume`. `action` and `power` are untouched — audio is not a power
concern, and adding it to `power.Controller` would hand every existing consumer
of `power.Fake` methods it has no use for.

### Interface

```go
type State struct {
    Level int  `json:"level"`   // 0-100
    Muted bool `json:"muted"`
}

type Controller interface {
    Get(ctx context.Context) (State, error)
    Set(ctx context.Context, s State) error
    // Available reports whether a console session is attached. It is
    // deliberately outside Get/Set because it must be cheap enough for the
    // 3-second status poll: it queries the session manager and never spawns a
    // helper. See the note on optimism under /api/status.
    Available() bool
}

var (
    ErrNoSession   = errors.New("volume: nobody is signed in at the PC")
    ErrUnsupported = errors.New("volume: not supported on this platform")
)
```

The `!windows` build returns `ErrUnsupported` from `Get` and `Set`, and `false`
from `Available`, so the dashboard degrades to disabled controls during Linux
development rather than erroring.

`Set` takes a whole `State` rather than separate `SetLevel` and `SetMute`
because dragging the slider while muted must change both, and one `Set` is one
process spawn instead of two. Given that a spawn is the expensive operation,
this halves the cost of the most common interaction.

### The interaction rule lives in Go, not JavaScript

```go
// Apply resolves a partial request against the current state. A level change
// clears mute — that is the whole rule, in one place, so the browser and the
// server can never disagree about it.
func Apply(current State, level *int, muted *bool) State
```

The client sends intent, the server resolves it and returns the resulting
state, the client renders what came back. One source of truth.

Clamping is part of `Apply`: levels below 0 become 0, above 100 become 100.

### Helper wire protocol

The helper prints exactly one line of JSON to stdout and exits 0:

```json
{"level":45,"muted":false}
```

or, on failure:

```json
{"error":"no default playback device"}
```

Exit codes are not used to signal audio failures — the JSON is authoritative —
so that a helper that cannot initialise COM is distinguishable from a helper
that could not be launched at all.

`protocol.go` holds `EncodeResult`, `DecodeResult`, `FormatHelperArgs` and
`ParseHelperArgs`, all pure and all tested on Linux. This is the same
containment strategy `buildShutdownArgs` and `caps.go` already use.

## HTTP API

| Method | Path | Gates | Behaviour |
|---|---|---|---|
| GET | `/api/volume` | session | Returns the current state |
| POST | `/api/volume` | limitBody + session + CSRF | Applies a partial change, returns the result |

POST body, with pointer fields so "not specified" is distinguishable from zero:

```go
type volumeRequest struct {
    Level *int  `json:"level"`
    Muted *bool `json:"muted"`
}
```

At least one field must be present; a body specifying neither is a 400. Both may
be present, in which case an explicit `muted` wins over the unmute that a level
change would otherwise imply — the caller said what it wanted, so the rule does
not get to override it.

Responses:

- **200** with `{"level":N,"muted":bool}`
- **400** malformed body, or neither field specified
- **503** `ErrNoSession` — nobody is signed in; a temporary condition, not a failure
- **500** helper timed out, could not be launched, or returned an error payload

`/api/status` gains one field:

```json
"audioAvailable": true
```

derived from `WTSGetActiveConsoleSessionId` alone — a cheap session-manager
query with no process spawn, safe at the existing 3-second cadence.

It is deliberately optimistic: a console sitting at the lock screen reports an
attached session even though `WTSQueryUserToken` would fail. The 503 is the
authoritative answer; this flag exists only to drive the disabled state, in the
same way `capabilities.hibernate` does. It also makes the UI self-healing —
signing in at the PC re-enables the controls on the next poll, with no refresh.

## User interface

One row below the action buttons and above the pending row:

```
├──────────────────────────────────────┤
│  🔊   ├────────●──────────┤    45%   │
├──────────────────────────────────────┤
```

- A mute toggle button carrying `aria-pressed`, its icon reflecting muted state.
- `<input type="range" min="0" max="100" step="1">` with an `aria-label`.
- A percentage readout.

The slider updates its readout on `input` for live feedback while dragging, but
only sends on `change`, which fires on release. That is the difference between
one request per drag and forty.

When `audioAvailable` is false, both controls render disabled with the reason as
a tooltip, matching how an unavailable Hibernate button already behaves. The
slider sits at 0 and the readout shows an em dash rather than a percentage —
showing "0%" would assert a level the app has not actually read.

The dashboard fetches `GET /api/volume` once on load, and again after each
successful change (using the state the POST returned, so no second request is
needed). It does not fetch on the status poll.

There is no confirm dialog. Volume is instantly reversible, and gating it behind
the same ceremony as a shutdown would be absurd.

## Command line

Two additions, both mirroring existing flags:

```
shutdowner.exe --audio-helper <op>   internal: run the audio operation in this
                                     session and print one line of JSON. Spawned
                                     by the service; not for direct use.
shutdowner.exe --fake-volume         development only: serve a fake volume that
                                     lives in memory, so the UI can be exercised
                                     on a machine with no Windows audio stack.
```

`--fake-volume` is the audio counterpart of the existing `--fake-power`, and
like it, logs a warning at startup so a fake controller is never silently in
play.

## Error handling

- The helper runs under `exec.CommandContext` with a **5-second timeout**, so a
  hung COM call fails one request rather than wedging the process.
- A volume failure is contained: it never affects the power actions, the
  countdown state machine, or the status poll.
- `HideWindow` is set on the spawn. A console window flashing on the user's
  screen every time the slider moves would make the feature worse than its
  absence.
- Helper stdout that does not parse is reported as a 500 with the raw line
  logged, not silently treated as zero volume.

## Testing

### Covered on Linux

- **apply**: a level change clears mute; muting preserves the level; unmuting
  restores the same level; clamping at −5, 0, 50, 100 and 105; both fields set
  at once; neither field set.
- **protocol**: encode/decode round-trip; error payload; malformed line; empty
  line; trailing bytes after the JSON; argument formatting and parsing.
- **web**: GET returns the current state; POST with a level; POST with mute;
  POST with both, asserting the explicit `muted` wins; 400 on `{}`; 400 on
  malformed JSON; 503 when the controller returns `ErrNoSession`; 500 on an
  arbitrary controller error; session required on both routes; CSRF required on
  POST; the body cap applies; `audioAvailable` reflects the controller and
  appears in the status payload.
- **fake**: records calls, returns configured state and errors, and reports
  configurable availability so the disabled-controls path is testable.

### Not covered by automated tests

`windows.go` (token acquisition and spawn) and `helper_windows.go` (COM). Both
stay deliberately thin, with every branch worth reasoning about extracted into
`apply.go` and `protocol.go`.

### Manual checklist additions

1. The dashboard shows the PC's real volume on load.
2. The slider sets an absolute level; verify the number at the PC.
3. Mute silences; unmute returns to exactly the previous level.
4. Dragging the slider while muted unmutes.
5. No console window flashes on the PC's screen when the slider moves.
6. With the screen locked but a user still signed in, the controls still work.
7. After signing out entirely, the controls disable with the reason, and
   re-enable on the next poll after signing back in.
8. After a fast-user-switch, changes target the newly active session rather
   than the previous one.
9. `CreateProcessAsUser` succeeds with the NULL `lpDesktop` that Go's `exec`
   passes. If it fails with access-denied, the fallback is a raw
   `CreateProcessAsUser` specifying `winsta0\default`.

Items 6, 7 and 8 are the ones most likely to reveal problems; fast-user-switch
is the least certain of the three.

## Out of scope

- Per-application volume mixing
- Input devices and device selection
- Volume as a scheduled action
- Any long-running user-session agent
