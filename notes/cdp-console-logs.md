# CDP console log capture — findings

Research and empirical testing done while implementing `rodney logs`.

## The three CDP mechanisms

### 1. `Runtime.consoleAPICalled` (Runtime domain)

- Fires for every `console.log/warn/error/debug/info/...` call made by JavaScript
  running in the page, including calls made via `Runtime.evaluate`
- **Live only** — no replay of past events
- When `Runtime.enable` is called in a new CDP session, Chrome does *not* replay
  previous `consoleAPICalled` events to that session
- This is what rodney uses for all console capture

### 2. `Log.entryAdded` (Log domain)

- Fires for **browser-generated** log entries: network errors, CSP violations,
  mixed-content warnings, deprecation notices, intervention messages, etc.
- Does **not** fire for JavaScript `console.*` API calls (those go exclusively
  to `Runtime.consoleAPICalled`)
- `Log.enable` does replay buffered browser-level entries to new sessions

### 3. `Console.messageAdded` (Console domain — deprecated)

- The older, deprecated predecessor to the Runtime + Log split
- Replays collected messages on `Console.enable`, per the CDP spec
- In practice (Chrome ~124+) empirically found to replay 0 messages
- Playwright never uses this domain

## Key discovery: `Runtime.consoleAPICalled` IS broadcast cross-session

Empirically confirmed: `Runtime.consoleAPICalled` events are broadcast to **all**
CDP sessions that have called `Runtime.enable` on the same target, regardless of
which session triggered the event. This includes events triggered by
`Runtime.evaluate`.

This was confirmed by observing double-writes to the NDJSON log file when both
a `_logger` subprocess and an in-process EachEvent handler were listening
simultaneously — both received the same event.

Consequence: the background `_logger` subprocess **can** capture console calls
made through `rodney js`, as long as it has called `Runtime.enable` before those
calls happen.

## Timing constraint: inline scripts race against subscription

`Runtime.consoleAPICalled` is live-only — events fired before a session calls
`Runtime.enable` are not replayed. This creates a race for pages that call
`console.*` synchronously in inline scripts:

```html
<script>console.log("fires during page load")</script>
```

If the `_logger` process is polling for new pages (100ms intervals), it will
almost always miss inline-script console calls because the page loads before
`_logger` subscribes.

## Current implementation: `_logger` subprocess with `Target.targetCreated`

`rodney start --logs` spawns a `_logger` subprocess immediately. It uses
`Target.setDiscoverTargets` + `EachEvent(TargetTargetCreated)` to detect new
pages the moment Chrome creates their target — microseconds after creation,
before navigation begins.

To close the remaining race for inline scripts, `cmdOpen` and `cmdNewPage`
use a blank-page-first strategy when `--logs` is active:

1. Create a blank page (`browser.MustPage("")`) — triggers `TargetTargetCreated`
   in `_logger` immediately
2. `_logger` calls `RuntimeEnable` on the blank page; `RuntimeEnable` is a
   synchronous CDP call that blocks until Chrome acknowledges — after it returns,
   Chrome will send events for any subsequent console call on this target
3. `_logger`'s `trackPage` opens the `.ndjson` log file *after* `RuntimeEnable`
   returns — the file's appearance on disk is an exact ready signal
4. `cmdOpen` polls (`waitForLogger`) for the log file to appear (5ms intervals,
   500ms timeout) — typically resolves in ~10–15ms
5. `cmdOpen` navigates to the real URL — `RuntimeEnable` persists across
   same-target navigations, so all inline scripts are captured

`rodney logs` requires `rodney start --logs`; it errors with a helpful message
otherwise.

### What `_logger` captures

| Source | Captured |
|--------|----------|
| Inline `<script>` on page load | ✓ (via blank-page-first) |
| `setTimeout` / async callbacks | ✓ |
| User interaction handlers | ✓ |
| `rodney js "console.log(...)"` | ✓ (cross-session broadcast confirmed) |

### What `_logger` cannot capture

- Console calls that fired **before** `_logger` subscribed on an existing page
  (e.g. if `rodney open` was called without `--logs` and later logs are enabled
  — the page's runtime was never enabled in the logger session)

## Considered and rejected approaches

### In-process capture in `cmdJS`

Briefly implemented: `cmdJS` registered an `EachEvent` handler in-process before
evaluating, wrote to the NDJSON file directly. Removed because:
- When combined with `_logger`, every event was written twice (both sessions
  receive the broadcast)
- Adding `s.Logs` guards made the logic conditional and error-prone
- `_logger` alone is sufficient since the cross-session broadcast works

### JS interceptor (`window.__rodney_logs`)

Briefly implemented: `rodney logs` injected `console.*` overrides that buffered
to `window.__rodney_logs`; subsequent invocations read and cleared the buffer.

Rejected because:
- Mutates the page's JavaScript environment
- Only captures messages after the first `rodney logs` invocation
- Feels wrong for a debugging tool to modify what it observes

### Chrome `--enable-logging` file

Chrome supports `--enable-logging --log-level=0` which writes all JavaScript
`console.*` calls to `<userDataDir>/chrome_debug.log`.

Rejected because:
- All levels map to `INFO:CONSOLE` — no severity info
- Single file for entire browser session, not per-page
- Requires baking the flag into `rodney start`

## Log file location

`<stateDir>/logs/<targetID>.ndjson` — one file per page target. Files persist
until the state directory is cleaned manually or a new session is started.
TargetIDs change each Chrome session, so old files from prior sessions are inert.
