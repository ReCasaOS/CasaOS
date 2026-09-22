# Anonymous usage statistics — design

Status: approved in conversation on 2026-09-22, section by section.

## Goal

Know two things about the ReCasaOS fleet that GitHub's download counters cannot tell:

1. **The fleet and its versions**: how many boxes are running (active in the last day, week,
   month), which distribution version they run, and how fast they update after a release.
2. **Hardware and system**: architecture, Linux distribution and version, kernel, Docker
   version, virtualisation, board or machine model, CPU cores, memory, disks, total storage,
   RAID, and the country.

Nothing else. Installed apps, feature usage, errors and anything about users are out of scope.

## Decisions (from the owner)

- Box-side collection, in the CasaOS core (approach A). No new component.
- Backend: **PostHog Cloud, EU region**. Project API key (public, write-only, meant to be
  embedded in clients): `phc_C5T8QbrhBttzgkWuKm2Xr9S4TLGh3sCVAZai2oaFEuhD`. Capture endpoint:
  `https://eu.i.posthog.com/i/v0/e/`.
- Consent: **opt-out, announced**. On by default; the installer, the dashboard and the README
  all say so, and there are three ways to turn it off.
- Country: derived by PostHog from the request's IP at ingestion, keeping country and
  continent only (see "PostHog project setup").

## What is sent

### Identifier

A random UUID v4 generated on first use and kept in `/var/lib/casaos/telemetry.json`
(root, 0600). It derives from nothing on the machine; a reinstall that removes
`/var/lib/casaos` produces a new one. It is PostHog's `distinct_id`.

### Events

- `heartbeat`: at most once every 24 hours. Counts active boxes.
- `version_changed`: once per install or upgrade (see "Send cycle"). Measures update speed.

### Properties (both events, plus `previous_distribution` on `version_changed`)

| Property | Example | Source |
|---|---|---|
| `distribution` | `v0.5.0` | `/var/lib/casaos/fork-release`, trimmed; `unknown` if absent |
| `previous_distribution` | `v0.4.99`, `new`, `upstream` | `/var/lib/casaos/upgraded-from` (see "Send cycle") |
| `core` | `v0.4.59` | `"v" + common.VERSION` |
| `arch` | `amd64`, `arm64`, `arm-7` | `runtime.GOARCH`, plus `GOARM` from the build info for `arm` |
| `os` | `ubuntu 24.04`, `debian 12`, `arch` | `/etc/os-release`: `ID` and `VERSION_ID` (ID alone when there is no VERSION_ID); `unknown` if unreadable |
| `kernel` | `6.8` | `uname` release, major.minor |
| `virtualization` | `none`, `kvm`, `lxc`, `wsl`, `unknown` | `systemd-detect-virt` output; `unknown` if it cannot run |
| `model` | `Raspberry Pi 5 Model B Rev 1.0`, `ZimaBoard` | `/proc/device-tree/model`, else `/sys/class/dmi/id/product_name`; NUL and spaces trimmed, 64 characters max; `unknown` if empty or a known placeholder (`To Be Filled By O.E.M.`, `System Product Name`, `Default string`, `Not Specified`) |
| `docker` | `28.3.1` | `GET /version` on `/var/run/docker.sock`, field `Version`; `unknown` if unavailable |
| `cpu_cores` | `4` | `runtime.NumCPU()` |
| `ram_gb` | `"8"` (a string, because of `128+`) | `MemTotal` from `/proc/meminfo`, rounded to the nearest of 1, 2, 4, 8, 16, 32, 64, then `128+` |
| `disks` | `3` | entries of `/sys/block` whose resolved path is not under `/sys/devices/virtual/`, excluding `sr*` and `mmcblk*boot*` |
| `storage_tb` | `4-8` | sum of those disks' sizes (`/sys/block/<d>/size` × 512 bytes), bucketed: `<0.5`, `0.5-1`, `1-2`, `2-4`, `4-8`, `8-16`, `16-32`, `32+` (TB, 10^12 bytes) |
| `raid` | `true` | `/proc/mdstat` lists an active `md` array |

PostHog-specific properties on every event: `$process_person_profile: false` (no person
profiles), `$lib: "recasaos-core"`. `$geoip_disable` is **not** sent, so that PostHog can
derive the country.

### Never sent

IP address (discarded by the project setting), hostname, MAC address, serial numbers, disk
names, labels or paths, installed apps, user accounts, anything about the local network.

### Request

```
POST https://eu.i.posthog.com/i/v0/e/
Content-Type: application/json

{"api_key":"phc_…","event":"heartbeat","distinct_id":"<uuid>",
 "timestamp":"<RFC 3339 UTC>","properties":{…}}
```

Timeout 10 seconds. The standard proxy environment is honoured. A non-2xx answer or a
transport error is a failure.

## Send cycle

- **Start**: after a random delay of 0 to 60 minutes (spreads boxes that restart together
  after an update), the core runs the first check, then one check every hour.
- **Check**:
  1. If statistics are off: delete `/var/lib/casaos/upgraded-from` if present, and stop. No
     payload is built, no network connection is made.
  2. If `/var/lib/casaos/upgraded-from` exists: send `version_changed` with its content as
     `previous_distribution`. Delete the file only after a successful send; on failure keep it
     for the next check. A content equal to the running `distribution` (a repair re-run, or an
     upgrade that failed before the copy onto `/`) is deleted unsent: nothing changed.
  3. If `last_sent` is absent, at least 24 hours old, or at least 24 hours in the future (a
     clock that was once far off): send `heartbeat`. Update `last_sent` only after a successful
     send, to the time the check started, so that the next day's hourly check is due.
- **Failures** are logged at Info (event name and error), never retried faster than the next
  hourly check, never queued.
- One function builds the properties, used by the sender and by the preview API: what the
  owner sees is what is sent.

### `upgraded-from`, written by the installer

`install.sh` decides what it is replacing **before** it unpacks the release overlay (the
overlay carries the new `fork-release` marker):

- `/var/lib/casaos/fork-release` exists: its content (the previous tag);
- else a `casaos` binary is already installed: `upstream` (a box coming from IceWhale's CasaOS);
- else: `new`.

It writes that value to `/var/lib/casaos/upgraded-from` (root, 0600) on every run, whether
statistics are on or off (the core deletes it unsent when they are off), and prints it in its
log (`Previous release: <value>`). The dashboard's update button runs `install.sh`, so it goes
through the same path.

## Consent and interface

### Storage

`/var/lib/casaos/telemetry.json`, root, 0600:

```json
{"enabled": true, "id": "<uuid>", "last_sent": "<RFC 3339>", "notice_seen": false}
```

A missing file means: enabled, no id yet (generated on first use), never sent, notice not seen.
A malformed file means **disabled** (when in doubt, do not send); the next `PUT` rewrites it.

`/var/lib/casaos/telemetry-off` (any content) is how the installer turns statistics off without
editing JSON: at startup, before anything else of this feature runs, the core sets
`"enabled": false` in `telemetry.json` (creating it if needed, keeping `id`) and deletes the
marker.

### Installer

- A clear block at the end of every install or upgrade, in English like the rest of the
  installer: anonymous statistics are on, what they contain (link to the README section), and
  how to turn them off.
- `--no-telemetry`, or the environment variable `RECASAOS_TELEMETRY=0`, creates
  `/var/lib/casaos/telemetry-off` (root, 0600) before the services start; the core folds it into
  `telemetry.json` at startup.
- An upgrade never turns statistics back on: without the flag, an existing choice is left as it is.

### Dashboard

- **Notice**, once per box: after login, when statistics are on and `notice_seen` is false, a
  discreet notice: "ReCasaOS sends anonymous statistics (versions, hardware, country)", with two
  actions, *See what is sent* and *Turn off*. Closing it, or either action, sets `notice_seen`.
  Upgraded boxes see it too: that is how they learn about the change.
- **Settings**: a switch "Anonymous usage statistics", and a *See what is sent* link that opens a
  modal with the exact JSON properties, built at that moment by the same function as the sender,
  and one sentence naming PostHog (EU) and linking the README section.
- All strings in the language files (English and French at least).

### Core API (admin token required, like every core route)

- `GET /v1/sys/telemetry` → `{"enabled": bool, "notice_seen": bool, "preview": {event: "heartbeat", properties: {…}}}`.
  The preview is built even when statistics are off (it is local and sends nothing).
- `PUT /v1/sys/telemetry` with `{"enabled"?: bool, "notice_seen"?: bool}` → the new state.
  Turning statistics off takes effect at once: the next check sends nothing.

### Documentation

- The distribution README gets a section "Anonymous statistics": what is sent (the table
  above), what is never sent, who receives it (PostHog, EU), and the three ways to turn it off
  (installer flag or variable, dashboard settings, editing `telemetry.json`).
- The CHANGELOG entry of the release that ships this announces it first.

## Test-only settings

Two environment variables on the core, documented as for tests only:

- `CASAOS_TELEMETRY_ENDPOINT`: replaces the capture URL.
- `CASAOS_TELEMETRY_START_DELAY`: replaces the random start delay (a Go duration, e.g. `0s`).

## Tests

### Core (Go, unit)

- Properties from fixtures under an injectable root: os-release variants, device-tree and DMI
  models (placeholders included), `/sys/block` with physical, virtual, optical and boot
  partitions, `/proc/mdstat` with and without an active array, `/proc/meminfo`; the bucket
  boundaries of `ram_gb` and `storage_tb`; kernel parsing.
- Cycle, against an `httptest` server: statistics off → zero requests and `upgraded-from`
  deleted; `last_sent` younger than 24 h → no heartbeat; older → heartbeat and `last_sent`
  updated; server error → `last_sent` unchanged and `upgraded-from` kept; success →
  `upgraded-from` deleted.
- Missing `telemetry.json` → enabled; malformed → disabled; `telemetry-off` present at
  startup → `"enabled": false` written, `id` kept, marker deleted.
- Request shape: `api_key`, `event`, `distinct_id`, `timestamp`, `$process_person_profile: false`,
  no `$geoip_disable`, properties equal to the preview.
- API: GET returns the state and a preview; PUT changes `enabled` and `notice_seen` and nothing else.

### Dashboard (vitest)

- The notice shows only when enabled and not seen; each action sends `notice_seen`; *Turn off*
  also sends `enabled: false`.
- The settings switch sends `enabled`; the preview modal renders the API's properties.

### Install check (never PostHog)

1. Install with `--no-telemetry`. Assert the install log says `Previous release: new` (the
   runner is a fresh machine), and `GET /v1/sys/telemetry` answers `"enabled": false` with a
   preview whose `distribution` is the release tag.
2. Start a local capture server (a few lines of Python) on the runner.
3. Add a systemd drop-in to `casaos.service` with `CASAOS_TELEMETRY_ENDPOINT` pointing at it and
   `CASAOS_TELEMETRY_START_DELAY=0s`; restart the core and check its environment carries both;
   only then turn statistics on through `PUT /v1/sys/telemetry`; write `new` to
   `/var/lib/casaos/upgraded-from` as the installer does (the core's first check may already
   have consumed the installer's file while statistics were off); restart the core again.
4. Assert the capture server received a `heartbeat` with `distribution` equal to the release tag,
   the runner's `arch` and `os`, and a `version_changed` with `previous_distribution: new`.

No CI step may run the core with statistics on and the default endpoint.

## PostHog project setup (owner, once)

1. Project on the **EU** cloud (the key above).
2. Settings → "Discard client IP data": on.
3. Data pipelines → Transformations: the **GeoIP** transformation, followed by a **Property
   filter** that removes `$geoip_city_name`, `$geoip_subdivision_1_code`,
   `$geoip_subdivision_1_name`, `$geoip_subdivision_2_code`, `$geoip_subdivision_2_name`,
   `$geoip_postal_code`, `$geoip_latitude`, `$geoip_longitude`, `$geoip_time_zone` and
   `$geoip_accuracy_radius`, keeping `$geoip_country_code`, `$geoip_country_name`,
   `$geoip_continent_code` and `$geoip_continent_name`.
4. Insights to create: daily/weekly/monthly unique `distinct_id` on `heartbeat`; breakdowns by
   `distribution`, `arch`, `os`, `model`, `$geoip_country_code`; `version_changed` over time by
   `distribution` (update speed after a release).

## Components and release

- **CasaOS (core)**: the telemetry package, the hourly check, the two API routes, the test-only
  settings.
- **CasaOS-UI**: the notice, the settings switch and preview, the API client, the strings.
- **CasaOS-Install**: `--no-telemetry` / `RECASAOS_TELEMETRY`, the closing notice block,
  `upgraded-from`, the README section, the install-check steps.
- Shipped together in one distribution release.

## Out of scope

- Installed apps, feature usage, errors, crash reports.
- Person profiles, sessions, anything browser-side (the dashboard sends nothing to PostHog).
- A public statistics page (PostHog's own dashboards serve the owner).
