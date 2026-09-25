# Push alerts — design

Status: approved in conversation on 2026-09-25 (events, channels, approach A, sections 1 and 2).

## Goal

The owner learns that something needs them (a backup failed, a disk is failing or full, an app
crashed, an update paused) on their phone or by mail, without opening the dashboard.

## Decisions (from the owner)

- Four categories: **backups**, **disks**, **updates**, **apps** (operations and runtime).
- Channels are **service URLs in the Shoutrrr format** (`ntfy://`, `telegram://`, `smtp://`,
  `discord://`, `gotify://`, `pushover://`, `matrix://`, …), sent with the Go Shoutrrr library;
  the dashboard has guided forms for ntfy, Telegram and e-mail, and an advanced URL field for the
  rest.
- **Approach A**: one alert hub in the CasaOS core, fed by the message bus, by AppManagement's new
  Docker watch, by polling LocalStorage for disks, and in-process by the automatic updates.

## Core (CasaOS): `pkg/alerts`

### Configuration

`/var/lib/casaos/alerts.json`, root, 0600 (URLs carry tokens), written atomically
(`pkg/utils/file.WriteFileAtomic`):

```json
{
  "channels": [{"id": "<random>", "name": "Phone", "url": "ntfy://ntfy.sh/my-box-topic"}],
  "categories": {"backups": true, "disks": true, "updates": true, "apps": true},
  "disk_threshold": 90
}
```

- Missing file: no channel, every category on, threshold 90. Nothing is sent without a channel.
- A malformed file loads as the defaults with no channel (nothing sent); the next `PUT` rewrites
  it.
- Every alert goes to every channel. `disk_threshold` is a percentage, 50 to 99.
- The Shoutrrr module: the maintained one with current releases (`containrrr/shoutrrr` or its
  maintained fork `nicholas-fedor/shoutrrr`), `govulncheck` clean; a URL Shoutrrr cannot parse is
  a 400 at `PUT`.

### Alerts, noise and resolution

- An alert has a **key** (`backup:<app>:<destination>`, `app:<name>:<operation>`,
  `app:<name>:runtime`, `disk:<serial>:smart`, `disk:<serial>:missing`, `storage:<mount>:full`,
  `update:<version>`), a category, a title and a sentence.
- **Dedupe**: a key already sent is not sent again for 6 hours; repeats are counted and the next
  message says so ("3 times since 06:00").
- **Resolution**: a condition that clears (disk back under the threshold, SMART passing again,
  disk back, app running and healthy for 10 minutes) sends one "resolved" message, only if its
  alert was sent.
- This state lives in memory: a core restart forgets it (at worst a reminder comes early).
- **Message**: title `ReCasaOS · <hostname> · <category>`, one plain sentence saying what and
  where, and the dashboard's address when the core knows it. Nothing more sensitive than an app,
  destination or disk name; never a token, a path under a user's home, or an environment value.
- **Sending**: through Shoutrrr, 15 seconds per channel, never blocking the source. A failure is
  logged and kept as `last_failure {at, channel, error}` for the dashboard.

### Sources

- **Message bus** (the core subscribes like an internal service, with the per-boot secret, the
  way the other services reach the bus; reconnects with backoff):
  - `backup:error`: category backups, key `backup:<app>:<destination>`, says backup or restore
    (the event's kind) and the error;
  - `app:install-error`, `app:update-error`, `app:start-error`, `app:stop-error`,
    `app:restart-error`, `app:uninstall-error`, `app:apply-changes-error`,
    `app:git-build-error`, `app:git-deploy-error`: category apps, key `app:<name>:<operation>`;
  - `app:container-died`, `app:container-unhealthy`, `app:container-restarting` (new, below):
    category apps, key `app:<name>:runtime`; resolved when AppManagement publishes
    `app:container-healthy` for that app.
- **Disks**: the core asks LocalStorage every hour, and once 5 minutes after it starts, for its
  disks and storages (the routes the dashboard uses): a disk whose SMART status goes from
  `passed` to `failed`; a storage above `disk_threshold`; a disk seen at the previous poll that
  is gone. Each with its key and its resolution. LocalStorage unreachable: nothing is concluded.
- **Updates**: `pkg/autoupdate` tells the hub in-process when an automatic update succeeds, fails
  or pauses (category updates, key `update:<version>`). With automatic updates off, a release
  newer than the one installed gives one alert per version (checked with the existing version
  check, at most once a day).

### API

- `GET /v1/sys/alerts` → `{channels: [{id, name, service, host}], categories, disk_threshold,
  last_failure}`: URLs are **never** returned; `service` is the scheme, `host` the host part
  (ntfy server, SMTP host), enough to recognise a channel.
- `PUT /v1/sys/alerts` with `{channels?, categories?, disk_threshold?}`: `channels` replaces the
  list; an entry with an `id` and no `url` keeps its stored URL; a new entry needs a `url`.
- `POST /v1/sys/alerts/test` with `{channel_id?}`: sends a test message to that channel, or to
  all; answers per channel `{id, ok, error}`.
- Behind the JWT like every `/v1/sys` route.

## AppManagement: the Docker watch

- A goroutine follows the Docker event stream (reconnecting with backoff) for containers that
  belong to an app (compose project label):
  - `die` with a non-zero exit code while no operation holds the app (the `Begin` registry):
    publish `app:container-died` with `app:name`, `docker:container:name`, the exit code;
  - `health_status: unhealthy`: publish `app:container-unhealthy`;
  - more than 3 `start` events of one container within 10 minutes: publish
    `app:container-restarting` (once per episode);
  - after one of those, the container running and not unhealthy for 10 minutes: publish
    `app:container-healthy`.
- The four event types are registered with the message bus like the existing ones.
- Tests: the decisions as pure functions over a sequence of events and the registry state.

## Dashboard (CasaOS-UI)

- In the settings, a row "Alerts" opening a modal:
  - the channels: add with a guided form (ntfy: server, default `https://ntfy.sh`, and topic;
    Telegram: bot token and chat id; e-mail: SMTP host, port, user, password, from, to) that
    builds the Shoutrrr URL, or "Advanced URL"; each listed by name, service and host; rename,
    remove, "Test";
  - the four categories and the disk threshold;
  - the last failed send, when there is one.
- The URL is never shown back; an existing channel is edited by name or replaced.
- Strings in English and French. A core without `/v1/sys/alerts` shows no row.

## Tests

- **Core (Go)**: dedupe and repeat count, resolution only after a sent alert, the 6-hour window;
  masking (no URL in any answer); `PUT` keeping a stored URL; Shoutrrr URL validation; each
  source's event → alert mapping; a failing channel logged and kept, never blocking; the
  configuration file (defaults, malformed, 0600).
- **AppManagement (Go)**: the Docker watch decisions (exit code, operation in progress, restart
  count window, healthy again).
- **Dashboard (vitest)**: the guided forms produce the right URLs; the masked list; keeping a
  channel; Test; no row against an older core.
- **Install check**: a step "Alerts": a local HTTP server on the runner plays ntfy (a `ntfy://`
  URL with `?scheme=http` pointing at it); add the channel, `POST …/test` arrives; a backup to a
  broken destination sends a backups alert; a compose app whose container exits with code 1 sends
  an apps alert.

## Out of scope

- Per-channel categories, quiet hours, alert history in the dashboard.
- Notifying about the owner's own actions (an app they stopped, a backup that succeeded).
