# Automatic updates of ReCasaOS — design

Status: approved in conversation on 2026-09-24 (scope, timing, failure handling, approach A,
sections 1 to 3); the dashboard and test sections follow the same decisions and were accepted
with "go dev".

## Goal

An owner who wants it can let the box install new ReCasaOS releases by itself, at night, a
little after they come out, without ever interrupting a backup or an app operation, and see
what happened in the dashboard.

## Decisions (from the owner)

- **Opt-in**: off by default; the installer never turns it on.
- **Every release** is installed, not only patch releases.
- **At night, after a delay**: inside a window of the box's local time (03:00 to 05:00 by
  default, adjustable), and only once the release is at least **48 hours** old.
- **On failure**: one new attempt the next night; after a second failure for the same version,
  automatic updates pause for that version until the owner resumes them or a newer release
  appears.
- **Approach A**: a small scheduler in the CasaOS core that starts exactly the update the
  dashboard's button starts (`install.sh` in the detached `casaos-update` unit).

## Core (CasaOS): `pkg/autoupdate`

Built like `pkg/telemetry`: a state file, an hourly check, a status for the API.

### State

`/var/lib/casaos/autoupdate.json`, root, 0600:

```json
{
  "enabled": false,
  "window_start": "03:00",
  "window_end": "05:00",
  "last": {"version": "v0.5.8", "started_at": "<RFC 3339>", "result": "running|succeeded|failed"},
  "failures": {"version": "v0.5.8", "count": 1}
}
```

- A missing file means the defaults (off, 03:00–05:00, nothing attempted). A file that cannot be
  read or parsed means **off**: when in doubt, nothing is started. The next `PUT` rewrites it.
- The window is `HH:MM` in the box's local time (`time.Local`), may wrap midnight
  (`23:00`–`01:00`), and lasts at least one hour; anything else is a 400.
- The 48-hour delay is fixed, not a setting.
- Written atomically (temporary file, then rename), like `telemetry.json`.

### The cycle

- `Run(ctx)`: one check after a random delay of 0 to 10 minutes, then one per hour. Test
  overrides (environment of `casaos.service`, never set in production):
  `CASAOS_AUTOUPDATE_START_DELAY` and `CASAOS_AUTOUPDATE_INTERVAL` (Go durations), and
  `CASAOS_AUTOUPDATE_VERSION_URL` / `CASAOS_AUTOUPDATE_INSTALLER_URL`, which also accept an
  `http://127.0.0.1:<port>/...` URL (loopback only) so the install check can serve them.
- Each check first **settles the last attempt** (below), then starts an update when every
  condition holds:
  1. `enabled`;
  2. now is inside the window;
  3. the latest release (`version.json` at `FORK_VERSION_URL`, or the configured
     `UpdateVersionUrl`) is newer than `/var/lib/casaos/fork-release` (the comparison the button
     uses, `version.IsNeedUpdate`);
  4. its `published_at` is at least 48 hours ago; a `version.json` without `published_at` is
     never installed automatically;
  5. it is not paused: `failures.version` is that version and `failures.count >= 2` means paused;
  6. no update runs already: the `casaos-update` unit is not active (`systemctl is-active`);
  7. no attempt started in the current occurrence of the window (at most one per night);
  8. AppManagement answers that no operation is in progress (below). No answer, or any
     operation listed, means not now; the next hourly check tries again while the window lasts.
- **Starting**: `last` becomes `{version, started_at: now, result: "running"}`, saved before
  anything runs, then the detached update starts exactly as the button's
  (`UpdateSystemVersion`: the same unit, installer and log `/var/log/casaos/upgrade.log`).
  A start that fails is `failed` at once.
- **Settling** (at every check, and at the core's start, while `last.result` is `running`):
  - `fork-release` equals `last.version`: `succeeded`; `failures` is cleared;
  - otherwise, once `casaos-update` is no longer active: `failed`, and `failures` counts it
    (a new version resets the count to 1);
  - the core restarted by the update settles it on its first check.
- A newer release than the paused one is tried normally (its own count). `resume` clears
  `failures`. An update done by hand needs nothing special: `fork-release` moves, and a paused
  version older than it no longer matters.
- Logged at Info: every start, success, failure and pause, with the version.

### API

- `GET /v1/sys/autoupdate` → `{enabled, window: {start, end}, state, next, last}`:
  - `state`: `off`, `up_to_date`, `waiting` (a newer release that is too recent, outside the
    window, or held by an operation), `updating` (`last.result` is `running`), `paused`;
  - `next`: `{version, not_before}` (the release aimed at and the earliest time it may start:
    the later of `published_at + 48h` and the next window opening), or null;
  - `last`: `{version, started_at, result}` or null.
- `PUT /v1/sys/autoupdate` with any of `{enabled, window_start, window_end, resume}`; answers
  the same view. Behind the JWT like every `/v1/sys` route.

## AppManagement: operations in progress

- `GET /v2/app_management/operations` → `{"operations": [{"app": "<name>", "kind": "<kind>"}]}`,
  read from the registry that already guards every app (`Begin`: backup, restore, install,
  update, a git app's check, build or deployment, a settings change). An empty list means
  nothing runs. Behind the JWT like every route; the core calls it as an internal request (the
  per-boot secret), which the JWT skipper already accepts.
- In the OpenAPI schema and its codegen, with the enum-constant count unchanged.

## Installer (CasaOS-Install)

- The release workflow writes `published_at` (RFC 3339, UTC, the time it builds the release)
  into `version.json` beside `version` and `change_log`; older cores ignore it.
- Nothing changes in `install.sh`: an automatic update is the button's update.

## Dashboard (CasaOS-UI)

- In the settings, below the update row: a switch "Update automatically". When on:
  - the window, two hour selects ("between 03:00 and 05:00");
  - the note "A new release is installed at night, two days after it comes out. Nothing starts
    while a backup or an app operation runs.";
  - the state line: "Up to date", "v0.5.8 will be installed after <not_before>", "Updating…",
    or "Paused: v0.5.8 failed twice" with "See the log" (the existing upgrade log view) and
    "Try again" (`resume`).
- After an automatic update, a notice once per browser: "ReCasaOS updated itself to v0.5.8
  last night", with the release notes link; remembered in `localStorage` by version.
- A core older than this feature (no `/v1/sys/autoupdate`) shows no row.
- Strings in English and French.

## Tests

- **Core (Go)**: the decision as a pure function of now, window, versions, `published_at`,
  pause, running unit, attempt this night and AppManagement's answer (every condition on and
  off, a window across midnight, DST-safe local times); settling (success, failure, second
  failure pauses, newer release resets, resume); the state file (defaults, malformed means off,
  0600, atomic); the API (validation 400s, view); the detached start reused, not copied.
- **AppManagement (Go)**: the route lists what `Begin` holds and nothing once released.
- **Dashboard (vitest)**: the switch and the window, the state lines, "Try again", the
  one-time notice, no row against an older core.
- **Install check**: a step "Automatic update": serve on the runner a `version.json` advertising
  a higher version published three days ago, and an installer that only writes that version to
  `fork-release`; point the core at them with the test overrides (short interval, no start
  delay); turn the feature on with a window around now; assert the state goes to `updating` then
  `up_to_date` with `last.result` `succeeded`; then serve a failing installer for a still higher
  version and assert `failed`. Restore `fork-release` and the core's environment afterwards.

## Out of scope

- Rolling back an update that installed but misbehaves.
- Choosing which releases to skip, or staying on patch releases.
- Updating the operating system's packages automatically.
