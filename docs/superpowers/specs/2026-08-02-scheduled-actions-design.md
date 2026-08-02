# Shutdowner — Scheduled Power Actions

**Date:** 2026-08-02
**Status:** Approved design

## Purpose

Let the operator choose *when* a power action runs, instead of always getting the
fixed countdown from `SHUTDOWNER_DELAY_SECONDS`. Two ways to say it:

- **Relative** — "shut down in 2 hours".
- **Absolute** — "sleep at 03:00".

Both apply to all four existing actions. Neither changes what the actions do, and
the existing one-tap flow keeps working exactly as it does today.

## Decisions

| Question | Decision |
|---|---|
| Are these two features? | One. Both resolve to a single absolute deadline. |
| Relationship to `SHUTDOWNER_DELAY_SECONDS` | A chosen time replaces it. Absent choice still means the configured delay. |
| Service restart | The schedule is persisted and restored. |
| Deadline passed while the service was down | Do not fire. Record it as missed and report it. |
| Deadline passed while the machine was asleep | Same rule, same code path. |
| Whose clock does `03:00` mean | The PC's. |
| Where the controls live | The existing confirm dialog, as a `When` row. |
| How many schedules at once | One, as today. `ErrConflict` is unchanged. |
| Furthest a schedule may reach | 7 days. |
| Recurrence | Out of scope. |

## The one rule that matters

The manager currently sleeps for a duration and executes when it wakes. That is
correct for 45 seconds and wrong for anything longer, because a duration is not
a deadline. Three separate situations break it:

- The service is restarted while a schedule is pending.
- Windows suspends the machine across the deadline.
- The deadline is hours away and Go's timer, which runs on the monotonic clock,
  stops advancing while the machine is suspended.

Rather than three special cases, there is one:

> The manager stores an absolute `firesAt`. A ticker compares the wall clock
> against it. **Fire only if the deadline is less than `MissedGrace` past.
> Otherwise the action is missed.**

`MissedGrace = 5 * time.Minute`. Everything below follows from this.

A missed action is never executed. Shutting the machine down because it was
asleep at 03:00 and someone opened the lid at 09:00 is the worst outcome
available, and it is the one a naive "fire on resume" would produce.

## API

`POST /api/action` gains two optional, mutually exclusive fields:

```jsonc
// unchanged — the configured delay, currently 45s
{ "action": "shutdown", "force": true }

// relative
{ "action": "shutdown", "force": true, "delaySeconds": 7200 }

// absolute, naive wall clock, interpreted in the server's location
{ "action": "sleep", "force": false, "at": "2026-08-03T03:00" }
```

`at` deliberately carries no offset. It is the value `<input
type="datetime-local">` produces, and it means what it says on the PC's clock.
Sending both fields is a 400; sending neither keeps today's behaviour, which is
what makes every existing test and the current client keep working untouched.

### Validation

| Rule | Response |
|---|---|
| Both `delaySeconds` and `at` present | 400, "give either delaySeconds or at, not both" |
| `delaySeconds` negative or > 604800 | 400, naming the 7-day limit |
| `at` unparseable | 400, naming the expected `2006-01-02T15:04` shape |
| `at` in the past | 400, quoting the PC's current local time |
| `at` more than 7 days out | 400, naming the limit |

`delaySeconds: 0` is legal and means "at the next tick", matching
`SHUTDOWNER_DELAY_SECONDS=0`, which the config already permits.

Quoting the PC's current time in the past-deadline error is what makes a
timezone mix-up diagnose itself: the operator sees immediately that the machine
disagrees with their phone.

### Status

`action.Pending` gains one field:

```go
type Pending struct {
    ID               string       `json:"id"`
    Action           power.Action `json:"action"`
    Force            bool         `json:"force"`
    RemainingSeconds int          `json:"remainingSeconds"`
    FiresAtLocal     string       `json:"firesAtLocal"` // RFC3339, server's offset
}
```

`RemainingSeconds` stays and stays authoritative for the countdown — it is
relative precisely so a client with a skewed clock still renders it correctly,
and that reasoning is unchanged. `FiresAtLocal` exists only so the UI can show
*"at 03:00"* next to it.

**The UI must read the wall-clock fields straight out of that string and must
not pass it through `Date()`.** The server has already formatted it in the PC's
timezone; re-parsing it in the browser converts it to the browser's, which is
the one thing this design decided against, and it would be invisible whenever
the two happen to match.

`action.Status` gains a parallel field for a missed action, so the UI can tell
"nothing scheduled" from "something was skipped":

```go
type Missed struct {
    Action     power.Action `json:"action"`
    WasDueAt   string       `json:"wasDueAt"` // RFC3339, server's offset
}

type Status struct {
    State   State    `json:"state"`
    Pending *Pending `json:"pending"`
    Missed  *Missed  `json:"missed"`
    Error   string   `json:"error"`
}
```

### Dismissing a missed action

`POST /api/dismiss` clears a missed record. Session- and CSRF-protected and
body-capped, like every other state-changing route. It is idempotent: with
nothing missed it still returns 200, because a second tab racing the first
should not produce an error the operator has to think about.

A missed record is also cleared by scheduling anything new. `StateMissed` is
schedulable, exactly as `StateFailed` already is — a skipped action must not
leave the machine unable to accept the next one.

## Manager changes

`internal/action` keeps its single-slot state machine. Changes:

- `Schedule(ctx, a, force, firesAt time.Time)` — takes a resolved deadline. All
  resolution happens before the manager sees it.
- `firesAt` becomes the source of truth; the stored `delay` moves out to the
  caller.
- A new `StateMissed`, alongside `idle`/`pending`/`executing`/`failed`.
- `WithAfterFunc` is replaced by `WithTicker`. Five call sites across the
  existing `manager_test.go` need updating; the tests themselves keep their
  shape, since firing is still driven manually rather than by waiting.
- `Restore()` loads a persisted schedule at startup and applies the miss rule.

The ticker runs at one second. A tick is a mutex acquisition and a time
comparison, so the cost is irrelevant next to the certainty of not having to
reason about monotonic clocks.

## Resolution

`internal/action/when.go`, a pure function so the rules are testable without an
HTTP server or a manager:

```go
// ResolveWhen turns a request's timing fields into an absolute deadline.
func ResolveWhen(now time.Time, delaySeconds *int, at string, fallback time.Duration) (time.Time, error)
```

`at` is parsed with `time.ParseInLocation(layout, at, now.Location())`, which is
the single line that implements the "PC's clock" decision.

## Persistence

`internal/action/store.go`. A `Store` interface behind the manager so tests get
a no-op or in-memory implementation and never touch a disk:

```go
type Store interface {
    Load() (State, error)
    Save(State) error
    Clear() error
}
```

The file implementation writes `schedule.json` beside the executable — the same
resolution `config.ExeDir` already does for `.env` and the log — at `0600`, via
a temp file and a rename so a crash mid-write cannot leave a half-written
schedule.

```jsonc
{
  "pending": { "id": "…", "action": "shutdown", "force": true,
               "firesAt": "2026-08-03T03:00:00+03:00" },
  "missed":  { "action": "sleep", "wasDueAt": "2026-08-02T03:00:00+03:00" }
}
```

`firesAt` is stored with its offset, so a DST change or a timezone change
between writing and reading resolves to the same instant rather than the same
wall-clock reading. This is the one place the design does *not* use naive local
time, and the difference matters: the operator's intent was captured at schedule
time, and replaying it against a shifted clock would move the deadline.

Startup, in order:

1. No file, or an unreadable or corrupt one → log a warning, start idle. A bad
   state file must never stop the service from starting; the whole point of the
   app is to be reachable.
2. A pending deadline within `MissedGrace` → re-arm it.
3. A pending deadline further past → convert it to a missed record.

The file is cleared when the action fires, is aborted, or is dismissed.

## UI

The confirm dialog gains a `When` fieldset above the existing hint text:

```
┌─ Shut down ──────────────────────┐
│ ☐ Close apps gracefully          │
│                                  │
│ When                             │
│   (•) Now  (45s countdown)       │
│   ( ) In   [ 2 ] [hours  ▾]      │
│   ( ) At   [2026-08-03 03:00]    │
│                                  │
│ Leave unchecked to force apps    │
│ closed. Unsaved work is lost.    │
│                                  │
│             [Cancel]  [Confirm]  │
└──────────────────────────────────┘
```

- `Now` is preselected, so the existing flow stays two taps.
- `In` is a number plus a minutes/hours select.
- `At` is `<input type="datetime-local">` with `min` and `max` set from the PC's
  current time, so the picker itself refuses out-of-range values.
- When the browser's UTC offset differs from the PC's — computed from the
  `localTime` the status endpoint already returns — the dialog says so and shows
  both readings. Silence here would be the failure mode.

The pending box formats long waits as `Shut down in 1h 59m · at 03:00` and keeps
plain seconds under a minute. A missed record renders in the existing error area
with a Dismiss button.

No inline script: everything lands in `app.js`, as the CSP requires.

## File layout

```
internal/action/manager.go    state machine, ticker, miss rule   (existing, edited)
internal/action/when.go       ResolveWhen                        (new)
internal/action/store.go      Store, the JSON file               (new)
internal/web/api_handlers.go  request fields, validation, dismiss (edited)
internal/web/static/app.js    When controls, formatting          (edited)
internal/web/templates/       dialog markup                      (edited)
```

`manager.go` is already around 250 lines; adding a codec and a file to it would
be the point where it stops being one thing.

## Error handling

| Situation | Behaviour |
|---|---|
| Corrupt or unreadable state file | Warn, start idle. Never fail startup. |
| `Save` fails while scheduling | Schedule anyway, warn in the log. An in-memory schedule that will not survive a restart still beats refusing the request. |
| Deadline passed while down | Missed record, reported until dismissed. |
| Action fails at fire time | Existing `StateFailed` path, unchanged. |
| Panic at fire time | Existing recover in `execute`, unchanged. |

## Testing

- `when_test.go` — table over both modes, absent fields, both fields, past `at`,
  the 7-day edge, and a fixed non-UTC location to pin the `ParseInLocation`
  behaviour.
- `manager_test.go` — injected clock and ticker: fires on time, does not fire
  past the grace window, records missed, abort still wins, restore re-arms,
  restore converts a stale deadline. No test waits on real time.
- `store_test.go` — round trip, absent file, corrupt file, atomic replace.
- `api_handlers_test.go` — each validation rule returns its documented status,
  and an absent `when` still produces the configured delay.
- Existing tests must pass with only the `WithAfterFunc` → `WithTicker` change.

## Out of scope

- **Recurrence.** "Every night at 03:00" is a different feature with its own
  state, its own UI and its own failure modes.
- **Multiple queued actions.** The single slot and `ErrConflict` stay.
- **Waking the machine.** Unchanged from the original spec: the app runs on the
  machine it controls.
