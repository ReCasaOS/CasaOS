# Relay log ("journal") Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One merged, live log of everything that passes through the relay (share connections, HTTP requests to the relay, relay events, service messages), kept 7 days with daily rotation, readable in `/admin/journal` and with `tail`/`jq` on the server.

**Architecture:** A new package `internal/journal` writes JSON lines to a daily-rotated file per writer (non-blocking, drops instead of slowing traffic, retention and size cap) and reads them back merged across writers (Caddy's own JSON access log included). `share` and `enrolment` write through it (and redirect their `log` output into it); `front` (Caddy) writes its access log as JSON to its own volume. `enrolment` serves `/admin/journal`: history pages and a Server-Sent Events stream.

**Tech Stack:** Go 1.26 stdlib only (`encoding/json`, `bufio`, `net/http` SSE), `html/template`, Caddy 2.10 `log` directive.

**Spec:** `docs/superpowers/specs/2026-11-13-journal-relais-design.md` (ReCasaOS/CasaOS, branch `docs/journal-relais`) — read it first.

**Worktree:** `D:\clients\casaos\_work\relay-journal`, branch `feat/relay-journal` from `origin/main` (91410f7, v0.7.0). Tests in Docker:

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v "D:/clients/casaos/_work/relay-journal:/src" -v relay-gomod:/go/pkg/mod -w /src golang:1.26 go test -race ./...
```

## Global Constraints

- Retention `LOG_KEEP` (Go duration, default `168h`, refused above `168h` or ≤ 0); one file per writer per UTC day; files older than `LOG_KEEP` deleted by their writer.
- Size cap per writer: `share` 1 GiB, `enrolment` 256 MiB (`LOG_MAX_SIZE`, bytes, one value per service, defaults as given); `front` ≈ 512 MiB through Caddy's `roll_size` × `roll_keep`.
- Never written: feed keys, `ADMIN_TOKEN`, `Authorization`, cookies, `X-Box-Signature` (and `X-Box-Key`).
- The address written is the real client: share's TCP peer; Caddy's `{client_ip}` (PROXY header from `share`).
- Writing never blocks traffic: a full buffer drops lines and later writes one line `{"src":"log","msg":"N lines not written"}`.
- Live stream to the browser at most 200 lines/s; one history scan at a time, at most 64 MiB read or 5 s per page.
- Share-app lines carry the host (SNI), never a path (no decryption).
- Public docs never describe an unfixed weakness; commits without AI trailer.

## Line format (all writers but Caddy)

`journal.Entry` (JSON, empty fields omitted):

| Field | Type | Meaning |
|---|---|---|
| `time` | RFC 3339 ms UTC | when |
| `src` | `share` \| `front` \| `relay` \| `log` | writer kind (`log` = a service's own message: errors, tunnel up/down…) |
| `svc` | `share` \| `enrolment` \| `front` | which service |
| `ip` | string | client address |
| `host` | string | SNI (share) or Host (front) |
| `act` | `pass` \| `cut` \| `close` | share decision / end |
| `reason`, `source` | string | for `cut` (protection reasons and sources) |
| `ms` | int | duration (close, front) |
| `in`, `out` | int | bytes from / to the client |
| `method`, `path`, `status` | string, string, int | front only |
| `event` | string | relay events: `ban_open`, `ban_lift`, `feed_ok`, `feed_fail`, `geo_ok`, `reports`, `protection`, `suspend`, `resume`, `manual_add`, `manual_del`, `allow_add`, `allow_del` |
| `msg` | string | human text for `log` and events |
| `count` | int | `reports` count, dropped lines |

Caddy lines are converted on read: `ts` → `time`, `request.client_ip` → `ip`, `request.host` → `host`, `request.method`, `request.uri` → `path`, `status`, `duration` (s) → `ms`, `size` → `out`, `bytes_read` → `in`; `src` and `svc` = `front`.

## Review Focus

1. A disk that fills or slows during an attack must not slow share connections — test in Task 1 (`TestWriteNeverBlocks`).
2. A Caddy line with an `Authorization` header must not show it in `/admin/journal` — test in Task 5 (`TestCaddyLineDropsHeaders`) and the Caddy config keeps headers out (Task 5).
3. A history search over 7 days of a busy relay must not stall enrolment — test in Task 2 (`TestPageStopsAtBudget`).
4. A day change while writing must not lose or duplicate lines — test in Task 1 (`TestRotatesAtMidnightUTC`).
5. An upgraded relay (existing volumes) must write without manual steps — Task 5 compose + image `/logs` ownership, checked by the e2e in Task 7.

---

### Task 1: `internal/journal` — writer

**Files:**
- Create: `internal/journal/entry.go`, `internal/journal/writer.go`, `internal/journal/writer_test.go`

**Interfaces:**
- Produces:
  - `type Entry struct` with the fields of "Line format" and their JSON tags.
  - `type Writer struct`; `func Open(dir, svc string, keep time.Duration, maxSize int64, now func() time.Time) (*Writer, error)` (creates `dir` 0750 if missing; file name `<svc>-YYYY-MM-DD.jsonl`, 0640).
  - `func (w *Writer) Write(e Entry)` — sets `time` (from `now`) and `svc` when empty; non-blocking (buffered channel of 4096; full → drop and count).
  - `func (w *Writer) Log() io.Writer` — each `Write` call becomes one `Entry{Src:"log", Msg: line without trailing newline}`; used with `log.SetOutput(io.MultiWriter(os.Stderr, w.Log()))`.
  - `func (w *Writer) Close() error` — flushes and closes.
  - Background loop: writes lines, rotates at UTC midnight, once a minute deletes this writer's files older than `keep` and, while their total size exceeds `maxSize`, the oldest of them (never today's file; today's is truncated to start over if alone above the cap, with a `log` line saying so); after drops writes `{"src":"log","count":N,"msg":"lines not written"}`.
  - A nil `*Writer` is a no-op for every method.
- [ ] **Step 1: Write the failing tests**: `TestWriteJSONLine` (fields and omitted empties), `TestRotatesAtMidnightUTC` (fake clock: lines before/after midnight in two files, none lost), `TestDeletesOlderThanKeep`, `TestSizeCapDeletesOldestFirst`, `TestWriteNeverBlocks` (a writer whose file writes block: 100 000 `Write` calls return within 1 s; then unblock → a dropped-count line), `TestLogWriterSplitsLines`, `TestNilWriterIsNoop`.
- [ ] **Step 2: Run them and see them fail** (`./internal/journal/`).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and see them pass.**
- [ ] **Step 5: Commit** — `journal: daily rotated JSON log writer`

### Task 2: `internal/journal` — reader

**Files:**
- Create: `internal/journal/reader.go`, `internal/journal/reader_test.go`, `internal/journal/caddy.go`

**Interfaces:**
- Consumes: Task 1.
- Produces:
  - `type Query struct { Src []string; Text string; Problems bool; From, To time.Time }` — `Problems`: `act=cut`, `status ≥ 400`, or `src=log` messages; `Text`: case-insensitive substring of ip, host, path, reason, source, event, msg.
  - `type Reader struct { Dirs []string; Now func() time.Time; MaxBytes int64; MaxTime time.Duration }` (defaults 64 MiB, 5 s); files recognised: `*.jsonl` (ours) and Caddy's `*.log` files (current and rolled).
  - `func (r *Reader) Page(ctx, q Query, before time.Time, n int) (entries []Entry, more bool, partial bool, err error)` — newest first, at most `n`, merged by `time` across files, reading files backwards; stops at the budget with `partial` true.
  - `func (r *Reader) Follow(ctx, q Query, out chan<- Entry)` — tails every file (and new files appearing at rotation) from now on, polling every 500 ms; sends matching entries; returns when ctx ends.
  - `func parseCaddy(line []byte) (Entry, bool)` per "Line format", never copying request headers.
  - A single process-wide semaphore: one `Page` at a time; a second concurrent call waits up to 1 s then returns an error `busy`.
- [ ] **Step 1: Write the failing tests**: `TestPageMergesWriters` (share, enrolment and a Caddy file interleaved by time), `TestPageFilters` (Src, Text, Problems), `TestPageBefore` (paging), `TestPageStopsAtBudget` (MaxBytes small → partial true), `TestFollowSeesNewLinesAndNewDayFile`, `TestCaddyLineDropsHeaders` (a Caddy line with `request.headers.Authorization` → Entry has no such data), `TestBusy`.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and see them pass.**
- [ ] **Step 5: Commit** — `journal: merged reader with filters, paging and follow`

### Task 3: `share` writes its lines

**Files:**
- Modify: `cmd/share/config.go` (`LOG_DIR` default `/logs`, empty = no journal; `LOG_KEEP`; `LOG_MAX_SIZE` default 1 GiB), `cmd/share/main.go` (open the writer, `log.SetOutput`, pass it down), `cmd/share/tunnels.go` (`toTunnel`: `pass` after the tunnel stream opens, `cut` where `ev.refused` is called, `close` after `sni.Forward` returns with duration and bytes counted by wrapping the visitor conn), `cmd/share/main.go` `accept` (the per-IP cap refusal: `cut`, reason `cap`, source `ip`, host empty), `Dockerfile` (`RUN mkdir /data /logs`, `COPY --chown=65532:65532 /logs /logs`), tests beside.

**Interfaces:**
- Consumes: `journal.Open`, `Writer.Write`, `Writer.Log`.
- Produces: share lines `{src:"share", ip, host, act, reason?, source?, ms?, in?, out?}`; `in` = bytes from the visitor, `out` = to the visitor. Tunnel up/down already logged by `internal/tunnel` with `log.Printf` → `src:"log"` lines.
- [ ] **Step 1: Write the failing tests** (with a `journal.Writer` on `t.TempDir()`): `TestJournalPassAndClose` (one connection through the rig: a `pass` line then a `close` line with `in`/`out` > 0), `TestJournalCutLines` (manual ban → `cut` `manual/address`; per-IP cap → `cut` `cap/ip`), `TestJournalNotForRelayHost` (relay host connection → no share line), `TestLogKeepAbove7DaysRefused` (config).
- [ ] **Step 2: Run them and see them fail** (`./cmd/share/`).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run `./cmd/share/` and see it pass.**
- [ ] **Step 5: Commit** — `share: write every visitor connection to the journal`

### Task 4: `enrolment` writes its events

**Files:**
- Modify: `cmd/enrolment/main.go` (env `LOG_DIR` default `/data/logs`, `LOG_KEEP`, `LOG_MAX_SIZE` default 256 MiB; open the writer; `log.SetOutput`; pass it to the pieces below), `cmd/enrolment/protect.go` (wires the hooks below to the writer; `geo_ok` when `Geo.Tick` reports a change), `internal/community/community.go` (`Engine.OnOpen func(store.CommunityBan)`, called for each ban opened, nil-safe), `internal/feeds/fetch.go` (`Fetcher.OnResult func(name string, count int, err error)`, called after each download attempt, nil-safe; `err` already stripped of the URL), `internal/server/protect_box.go` (`protection` with node and the two settings; `reports` with node and accepted count), `internal/admin/protect.go` and `protect_stats.go` (`manual_add`/`manual_del`, `allow_add`/`allow_del`, `ban_lift`, `suspend`/`resume`, feed added/edited/deleted as `msg`); each gets an optional `Journal *journal.Writer` in its Deps (nil-safe).

**Interfaces:**
- Consumes: Task 1.
- Produces: `src:"relay"` lines with `event`, `msg` (short English sentence), and `ip`/`host` when the event has one (a ban's key goes in `ip`). Never a feed's URL query or key: feeds are named by their name only.
- [ ] **Step 1: Write the failing tests**: `TestJournalBanOpen` (community tick opens a ban → line with `ip` and `msg` "2 fleets · 24 h"), `TestJournalFeedFailHasNoKey` (feed with a key fails → `feed_fail` line contains the feed name and not the key nor the URL), `TestJournalProtectionPut`, `TestJournalAdminManualAdd`.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run `go test -race ./...` and see it pass.**
- [ ] **Step 5: Commit** — `enrolment: relay events and messages in the journal`

### Task 5: `front` access log and compose

**Files:**
- Modify: `Caddyfile` (a snippet `(journal)` with `log { output file /logs/front.log { roll_size 64MiB  roll_keep 8  roll_keep_for {$LOG_KEEP:168h}  mode 0644 } format json }` imported in every site block; request headers left out of the log — use the `format` filter or `log_skip`/field removal so `request.headers` is not written at all; for the route `/admin/journal/stream` add `flush_interval -1` to its `reverse_proxy`), `e2e/Caddyfile.tls` and the `Caddyfile.tls` example in `docs/self-hosting.md` (same snippet), `compose.yaml` (volumes `share-logs` → share `/logs`, `front-logs` → front `/logs`; enrolment mounts `share-logs:/logs/share:ro` and `front-logs:/logs/front:ro`; env `LOG_KEEP` passed to the three; `LOG_MAX_SIZE` per service), `e2e/compose.e2e.yaml` if it overrides those services.
- Test: `internal/journal/caddy_test.go` gains `TestShippedCaddyfileLogsJSONWithoutHeaders`: it reads `Caddyfile` and `e2e/Caddyfile.tls` as text and asserts every site block imports `(journal)` and the snippet removes `request>headers`; the e2e of Task 7 checks a real line.

**Interfaces:**
- Produces: Caddy JSON lines in `/logs/front.log` (and rolled files) readable by uid 65532.
- [ ] **Step 1: Write the failing test** `TestShippedCaddyfileLogsJSONWithoutHeaders`.
- [ ] **Step 2: Run it and see it fail.**
- [ ] **Step 3: Implement**; check the config with `docker run --rm -v "$PWD/Caddyfile:/etc/caddy/Caddyfile" caddy:2.10.0-alpine caddy validate --config /etc/caddy/Caddyfile` (and the TLS one).
- [ ] **Step 4: Run `go test ./internal/journal/` and `caddy validate`; both pass.**
- [ ] **Step 5: Commit** — `front: JSON access log in the journal volume`

### Task 6: `/admin/journal`

**Files:**
- Create: `internal/admin/journal.go`, `internal/admin/journal.html`, `internal/admin/journal_test.go`
- Modify: `internal/admin/admin.go` (Deps `Journal *journal.Reader`; routes; header link "Journal" in `page.html` and `protect.html`), `cmd/enrolment/main.go` (`journal.Reader{Dirs: ["/data/logs", "/logs/share", "/logs/front"]}` from env `LOG_READ_DIRS`, comma-separated, default those three)

**Interfaces:**
- Consumes: Task 2.
- Produces:
  - `GET /admin/journal` — page with filters (`src` checkboxes: Visitors (share), Relay requests (front), Events (relay), Messages (log); `q` text; `problems` checkbox; range: last hour, 6 h, 24 h, 7 days) and the first page (200 lines); "Older" link (`before=<time>`); `partial` → "Searched N MiB, continue" link.
  - `GET /admin/journal/stream?<same filters>` — `text/event-stream`, one `data:` JSON entry per line, at most 200 events/s (beyond: one event `{"dropped":N}` per second), heartbeat comment every 15 s; ends when the client goes.
  - Page script: appends streamed lines at the top while "Live" is on and the range is "last hour"; "Pause" stops appending (keeps the connection, counts waiting lines); clicking an address or host sets `q`.
  - Rendering per the spec's example (one line per entry, colour by `act`/status/src), html/template escaping only.
- [ ] **Step 1: Write the failing tests**: `TestJournalPageShowsMergedLines`, `TestJournalFilters`, `TestJournalOlder`, `TestJournalPartialSearch`, `TestJournalStreamSendsNewLines` (write a line after connecting → received), `TestJournalStreamRateLimit` (1 000 lines at once → ≤ 200 `data:` in the first second and a `dropped` event), `TestJournalNeedsAuth`, `TestJournalEscapesHTML` (a host `<script>` in a line is escaped).
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run `./internal/admin/` and see it pass.**
- [ ] **Step 5: Commit** — `admin: live journal page`

### Task 7: Docs and end-to-end

**Files:**
- Modify: `docs/self-hosting.md` (env rows `LOG_DIR`, `LOG_KEEP`, `LOG_MAX_SIZE`, `LOG_READ_DIRS`; new section "Logs": what each writer writes, where, 7 days and caps, `tail -f` / `jq` examples, what is never written; the personal-data paragraph of "Protection of shared apps" says visitor addresses are kept 7 days in the logs), `README.md` (configuration list), `e2e/share_protect_test.go` or a new `e2e/journal_test.go`, `.github/workflows/ci.yml` (add the test name to `-run`)

- [ ] **Step 1: Write the test** `TestJournalShowsVisitorAndAPI`: a visitor connects to a box's share name and a box calls `PUT /v1/share/protection`; within 30 s `/admin/journal` (Basic auth with the e2e token) lists a `share` line with the visitor's address and host and a `front` line with `PUT /v1/share/protection`; the `front` line has no `Authorization` or `X-Box-Signature` text; the share and front log volumes exist and are written with no manual step.
- [ ] **Step 2: Run it** with the e2e command from `ci.yml` (Go 1.26 and Docker on the host), cleaning up after.
- [ ] **Step 3: Update the docs.**
- [ ] **Step 4: Run `go vet ./...` and `go test -race ./...` in Docker; clean.**
- [ ] **Step 5: Commit** — `docs, e2e: relay journal`
