# Relay-side protection — relay Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The relay filters visitors of shared apps on their address before the box tunnel: operator bans, external lists, a community list fed by the boxes, with everything managed from `/admin`.

**Architecture:** `enrolment` holds all state in its SQLite store, downloads feeds and DB-IP, computes the community list every minute, compiles one filter snapshot and serves it on an internal route; it also serves the new signed box routes and a new `/admin/protection` page. `share` polls the snapshot, checks each TLS connection to a share name after the SNI peek, and posts accepted (key, box) pairs and refusal counts back to `enrolment`.

**Tech Stack:** Go 1.26, `net/http` mux patterns, `modernc.org/sqlite`, `html/template`, `net/netip`; new dependency `github.com/oschwald/maxminddb-golang/v2` (same as the Gateway).

**Spec:** `docs/superpowers/specs/2026-11-13-protection-relais-design.md`. **Contract:** `docs/superpowers/plans/2026-11-13-protection-relais-contract.md` (constants, JSON, reasons) — read it first.

**Worktree:** `D:\clients\casaos\_work\relay-protect`, branch `feat/share-protection` from `origin/main` (631bada). `D:\clients\casaos\recasaos-relay` is stale: do not use it. Tests in Docker (no cgo on Windows):

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v "D:/clients/casaos/_work/relay-protect:/src" -v relay-gomod:/go/pkg/mod -w /src golang:1.26 go test -race ./...
```

## Global Constraints

- Filters apply only to TLS connections whose SNI is a share name; the relay host, port 80 and the tunnel listener are never filtered (contract, "Precision").
- Order in `share` for a share-name connection: allow list → manual → suspended → external (box filter on) → community (box filter and sharing on). The existing caps keep running first, at accept.
- A refused connection is closed with nothing written.
- With no snapshot ever received, `share` lets everything through; with a stale one it keeps it.
- Every key that enters the store is canonical (`netip.ParsePrefix(...).Masked().String()`), never in the "Keys never kept" ranges.
- Nothing is filtered on a fresh relay: no feed enabled by default, no allow/manual entries, and boxes without a protection row get no external or community filtering.
- Feed API keys are never shown back in full (`••••` + last 4 characters) and never logged.
- No personal data in logs beyond what the existing cap log prints (one line a minute).
- Commits without any AI trailer.

## Review Focus

1. A box key reported by a box whose visitor never reached it through the relay must not count — test in Task 3 (`TestReportNeedsSeenConnection`).
2. Two boxes of the **same** fleet reporting one key must not open a ban — test in Task 3 (`TestSameFleetTwiceIsOneFleet`).
3. A feed answering 200 with an HTML error page must not wipe the last good copy — test in Task 2 (`TestFeedHTMLPageKeepsLastCopy`: zero valid lines → refused).
4. A country ban on the box owner's own country must not cut boxes from the relay — test in Task 7 (`TestRelayHostNeverFiltered`).
5. `enrolment` restarting must not make `share` let banned addresses through — test in Task 7 (`TestKeepsSnapshotWhenEnrolmentDown`).

---

### Task 1: Store — protection tables and their methods

**Files:**
- Create: `internal/store/protect.go`, `internal/store/protect_test.go`
- Modify: `internal/store/store.go` (append `protectSchema` to the `db.Exec` in `Open`; add the methods to `Store`)

**Interfaces:**
- Produces (all on `Store`; times as `UnixNano` like the rest):
  - Tables: `share_protection(node_id PK REFERENCES box_keys ON DELETE CASCADE, relay_filter INT, share_bans INT, share_bans_at INT)`, `share_seen(key, node_id, at, PK(key,node_id))`, `share_reports(node_id, fleet_id, key, reason, app, since, received, PK(node_id,key,since))`, `share_refusals(hour, name, key, reason, source, count, PK(hour,name,key,reason,source))`, `community_bans(key PK, kind, strike, since, until, reasons, fleets)` (`until` 0 = permanent), `community_strikes(key PK, strikes, last_end)`, `community_openers(key, since, fleet_id)`, `share_allow(id INTEGER PK, prefix, note, added)`, `share_manual(id INTEGER PK, kind, value, note, since, until)`, `share_suspended(name PK, added)`, `share_feeds(id INTEGER PK, name UNIQUE, url, auth_name, auth_value, format, column, every_s, max_entries, enabled, note, preset, last_try, last_ok, last_error, count)`.
  - `type Protection struct { RelayFilter, ShareBans bool; ShareBansAt time.Time }`; `GetProtection(ctx, nodeID) (Protection, bool, error)`; `SetProtection(ctx, nodeID string, p Protection) error` (deletes the node's reports when `ShareBans` goes off); `ProtectedNames(ctx) (map[string]Protection, error)` (share name → row).
  - `type Seen struct { Key, NodeID string }`; `RecordSeen(ctx, at time.Time, s []Seen) error`; `SeenBetween(ctx, key, nodeID string, from, to time.Time) (bool, error)`.
  - `type Report struct { NodeID, FleetID, Key, Reason, App string; Since time.Time }`; `AddReport(ctx, r Report, at time.Time) error` (duplicate → nil); `ReportsSince(ctx, t time.Time) ([]Report, error)`.
  - `type Refusal struct { Hour time.Time; Name, Key, Reason, Source string; Count int64 }`; `AddRefusals(ctx, []Refusal) error` (adds counts); `RefusalsOf(ctx, name string, since time.Time, limit int) ([]Refusal, error)`; `RefusalTotals(ctx, since time.Time) ([]Refusal, error)` (grouped by hour, reason, source; Key and Name empty).
  - `type CommunityBan struct { Key, Kind string; Strike int; Since, Until time.Time; Reasons string; Fleets int }`; `ActiveBans(ctx, now) ([]CommunityBan, error)`; `PermanentBans(ctx) ([]CommunityBan, error)`; `Strikes(ctx, key) (n int, lastEnd time.Time, err error)`; `OpenBan(ctx, b CommunityBan, openers []string) error`; `LiftBan(ctx, key string, now time.Time) error` (ends it now, keeps strikes); `EndExpired(ctx, now) error` (moves ended bans' end into `community_strikes.last_end`, deletes them); `ContributedSince(ctx, fleetID string, t time.Time) (int, error)`.
  - `type AllowEntry struct { ID int64; Prefix, Note string; Added time.Time }`, `AddAllow`, `DeleteAllow`, `AllowList`.
  - `type ManualBan struct { ID int64; Kind, Value, Note string; Since, Until time.Time }` (`Kind` ∈ `address range asn country`), `AddManual`, `DeleteManual`, `ManualBans(ctx, now)` (active only).
  - `SuspendName`, `ResumeName`, `SuspendedNames`.
  - `type Feed struct { ID int64; Name, URL, AuthName, AuthValue, Format string; Column, EverySec, MaxEntries int; Enabled bool; Note, Preset string; LastTry, LastOK time.Time; LastError string; Count int }`, `AddFeed`, `UpdateFeed`, `DeleteFeed`, `Feeds(ctx)`, `FeedResult(ctx, id int64, at time.Time, count int, errText string) error` (`errText` "" sets `last_ok` and `count`).
  - `FleetEligible(ctx, fleetID string, now time.Time) (bool, error)`: fleet `created_at` ≤ now − 7 days and at least one `share_names` row.
  - `PruneProtection(ctx, now time.Time) error`: retention of the contract.

- [ ] **Step 1: Write the failing tests** (`openTmp`, a fleet and a box key as in `store_test.go`):
  - `TestProtectionDefaultsAndRoundTrip`: no row → `ok` false; set `{true,true,t}` → read back equal.
  - `TestShareBansOffDeletesReports`: two reports for node 7, one for node 8; set node 7 `ShareBans` false → `ReportsSince` holds only node 8's.
  - `TestRefusalsAddUp`: same (hour,name,key,reason,source) twice with counts 2 and 3 → one row, 5; `RefusalsOf("other")` empty.
  - `TestStrikesSurviveEnd`: open strike 1 until t+24h; `EndExpired(t+25h)` → no active ban, `Strikes` = 1 with `lastEnd` t+24h.
  - `TestFleetEligible`: fleet created 6 days ago with a name → false; 8 days with no name → false; 8 days with a name → true.
  - `TestPruneProtection`: seen 25 h old, report 31 days old, refusal 8 days old, strike ended 31 days ago are gone; permanent ban and its strikes stay.
  - `TestDeleteBoxCascades`: deleting the box key drops its protection row.
- [ ] **Step 2: Run them and see them fail** (`./internal/store/ -run 'Protection|ShareBans|Refusals|Strikes|Eligible|Prune|Cascades'`).
- [ ] **Step 3: Implement** `internal/store/protect.go` (schema with `IF NOT EXISTS`, appended in `Open`).
- [ ] **Step 4: Run the whole `./internal/store/` and see it pass.**
- [ ] **Step 5: Commit** — `store: protection settings, reports, community bans and lists`

### Task 2: Feeds — parse, download, presets

**Files:**
- Create: `internal/feeds/parse.go`, `internal/feeds/fetch.go`, `internal/feeds/presets.go`, tests beside them.

**Interfaces:**
- Consumes: `store.Feed`, `Store.Feeds`, `Store.FeedResult` (Task 1).
- Produces:
  - `var ErrTooMany = errors.New("too many entries")`, `var ErrEmpty = errors.New("no address in the list")`.
  - `func Parse(r io.Reader, format string, column, max int) ([]netip.Prefix, error)` — `format` `text` (first field of the line; `#` and `;` start a comment, also after the address) or `csv` (1-based `column`, header lines that do not parse skipped); bare addresses become `/32` or `/128`; prefixes masked; invalid lines skipped; result sorted and deduplicated; more than `max` → `ErrTooMany`; zero → `ErrEmpty`.
  - `type Fetcher struct { Store store.Store; Dir string; Client *http.Client; Now func() time.Time; TotalMax int }`; `func (f *Fetcher) Tick(ctx)` (downloads each enabled feed whose `last_try + every_s` has passed; on success writes `<Dir>/<id>.txt` by rename then `FeedResult(count, "")`; on failure `FeedResult(_, err)` and the file stays); `func (f *Fetcher) Refresh(ctx, id int64) error` (same, now); `func (f *Fetcher) Load(id int64) ([]netip.Prefix, error)` (reads the file).
  - The total cap: before writing, sum of the other enabled feeds' `count` + new count > `TotalMax` → error `total cap reached`.
  - `type Preset struct { Key, Name, URL, Format, AuthName, Note string; Column, EverySec int; NeedsKey bool }`; `var Presets []Preset` with keys `firehol1`, `spamhaus-drop`, `blocklistde`, `et-compromised`, `tor-exits`, `abuseipdb`, `abuseipdb-free`, `crowdsec`. URLs, formats, intervals and notes: the contract's "Feed presets" table, verbatim. `crowdsec`: Basic auth (the operator pastes `user:password`; sent as `Authorization: Basic base64(...)`).
  - `Parse` skips entries broader than `/8` (IPv4) or `/19` (IPv6) (contract).
  - Header sending: `AuthName` `Authorization-Basic` means Basic as above; any other non-empty `AuthName` is sent as that header with `AuthValue`; AbuseIPDB also gets `Accept: text/plain`.
- [ ] **Step 1: Write the failing tests**:
  - `TestParseText`: lines `1.2.3.4`, `5.6.7.0/24 ; SBL123`, `# comment`, `2001:db8::1`, `junk`, `1.2.3.4` → `[1.2.3.4/32, 5.6.7.0/24, 2001:db8::1/128]`.
  - `TestParseCSVColumn`: header `ip,score` then `9.9.9.9,100` with column 1 → `[9.9.9.9/32]`.
  - `TestParseTooMany`: 3 lines, max 2 → `ErrTooMany`.
  - `TestParseSkipsHugePrefixes`: `0.0.0.0/0`, `10.0.0.0/7`, `2000::/3`, `1.0.0.0/8` → only `1.0.0.0/8`.
  - `TestPresetsAreHTTPS`: every preset URL starts with `https://`.
  - `TestFeedHTMLPageKeepsLastCopy`: a feed with a good copy; server answers `200 <html>…` → `ErrEmpty` recorded as `last_error`, `Load` still returns the old prefixes, `count` unchanged.
  - `TestFeed500KeepsLastCopy`: same with a 500.
  - `TestFeedNotDueIsSkipped`: `last_try` 10 minutes ago, `every_s` 3600 → no request.
  - `TestTotalCapRefusesWholeFeed`: other feed count 9, `TotalMax` 10, new list of 2 → refused, file untouched.
  - `TestAuthHeaders`: `Key: k` and `Accept: text/plain` for the AbuseIPDB preset; Basic for `crowdsec`.
- [ ] **Step 2: Run them and see them fail** (`./internal/feeds/`).
- [ ] **Step 3: Implement** the three files.
- [ ] **Step 4: Run them and see them pass.**
- [ ] **Step 5: Commit** — `feeds: download and parse external block lists`

### Task 3: Community list

**Files:**
- Create: `internal/community/community.go`, `internal/community/community_test.go`

**Interfaces:**
- Consumes: Task 1 store methods.
- Produces:
  - `func Key(s string) (string, bool)` — canonical address key or false (contract rules, "keys never kept").
  - `type Engine struct { Store store.Store; Now func() time.Time; Allowed func(netip.Prefix) bool }` (`Allowed` true when the relay allow list covers the prefix; set by Task 5).
  - `func (e *Engine) Accept(ctx, ref store.BoxRef, items []Item) error` with `type Item struct { Key, Reason, App string; Since time.Time }` — applies the acceptance rules (seen, eligible fleet, `share_bans` on, field rules already checked by the route) and `AddReport`.
  - `func (e *Engine) Tick(ctx) error` — `EndExpired`; then for each key with reports from ≥ 2 distinct fleets in the last 24 h, not allowed and with no active ban: strike = previous strikes + 1 if the last end is less than 30 days ago, else 1; until = since + {24 h, 7 days, permanent}[min(strike,3)−1]; `OpenBan` with the reporting fleets as openers and the distinct reasons joined by `,`. Then ranges: group active address bans by `/24` or `/48`; 5 or more and no active range ban and the range not allowed → open a range ban the same way (strikes keyed by the range).
  - `func FlaggedASNs(bans []store.CommunityBan, asnOf func(netip.Addr) (uint, string)) []ASNFlag` with `type ASNFlag struct { ASN uint; Org string; Ranges int }` — 10 or more active range bans.
- [ ] **Step 1: Write the failing tests** (fake clock, real store from `store.Open(t.TempDir()+"/r.db")`, fleets 8 days old with a name, seen rows recorded):
  - `TestTwoFleetsOpenADayBan`: fleets A and B report `203.0.113.7/32` → one ban, strike 1, until +24 h, reasons `scan,waf`.
  - `TestSameFleetTwiceIsOneFleet`: two boxes of fleet A → no ban.
  - `TestReportNeedsSeenConnection`: B's box never saw the key → no ban.
  - `TestYoungFleetDoesNotCount`: B created 6 days ago → no ban.
  - `TestSharingOffDoesNotCount`: B's `share_bans` false → no ban.
  - `TestRepeatEscalates`: ban 1 ends; new reports 10 days later → strike 2, 7 days; again → strike 3, permanent (`Until` zero).
  - `TestRepeatAfter31DaysStartsOver`: second ban 31 days after the first ended → strike 1.
  - `TestReportDuringBanDoesNotExtend`: report while active → same `Until`.
  - `TestFiveInA24OpensRange`: 5 address bans in `198.51.100.0/24` → range ban `198.51.100.0/24`, strike 1; 4 → none.
  - `TestIPv6RangeIs48`: 5 `/64` keys in one `/48` → range ban `/48`.
  - `TestAllowListWins`: key inside an allowed prefix → no ban; range allowed → no range ban.
  - `TestKeyRejectsPrivate`: `10.1.2.3`, `100.64.1.1`, `fe80::1`, `::1` → false; `2001:db8:1:2:3::9` → `2001:db8:1:2::/64`.
  - `TestFlaggedASNs`: 10 ranges mapped to AS64500 → one flag with 10; 9 → none.
- [ ] **Step 2: Run them and see them fail** (`./internal/community/`).
- [ ] **Step 3: Implement** `community.go`.
- [ ] **Step 4: Run them and see them pass.**
- [ ] **Step 5: Commit** — `community: shared ban list with strikes and ranges`

### Task 4: DB-IP on the relay

**Files:**
- Create: `internal/geo/geo.go`, `internal/geo/geo_test.go`, `internal/geo/testdata/` (two small MMDB files written by the test with `github.com/maxmind/mmdbwriter` in a `_test.go` helper, or committed fixtures — builder's choice)
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Produces:
  - `type DB struct { Dir string; Client *http.Client; Now func() time.Time; BaseURL string }` (`BaseURL` default `https://download.db-ip.com/free/`).
  - `func (d *DB) Tick(ctx) error` — once a day: if the files are missing or from an earlier month, download `dbip-{country,asn}-lite-YYYY-MM.mmdb.gz`, gunzip, open it to check it, rename into place; failure keeps the old files. Then reopen readers.
  - `func (d *DB) ASN(a netip.Addr) (uint, string)`, `func (d *DB) Country(a netip.Addr) string`.
  - `func (d *DB) CountryPrefixes(cc string) []netip.Prefix`, `func (d *DB) ASNPrefixes(asn uint) []netip.Prefix` — walk the database networks; nil when the file is missing.
  - `func (d *DB) Version() string` — file month, part of the snapshot version.
- [ ] **Step 1: Write the failing tests**: `TestCountryAndASNLookup`, `TestCountryPrefixes` (fixture with two networks in `XX` → both), `TestDownloadKeepsOldOnBadGzip`, `TestNoFilesMeansNoPrefixes`.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement** `geo.go`.
- [ ] **Step 4: Run them and see them pass.**
- [ ] **Step 5: Commit** — `geo: DB-IP Lite country and ASN on the relay`

### Task 5: Snapshot and the internal routes

**Files:**
- Create: `internal/server/protect_internal.go`, `internal/server/protect_internal_test.go`, `internal/protect/snapshot.go`, `internal/protect/snapshot_test.go`
- Modify: `internal/server/server.go` (`Deps` gains `Protect *protect.Compiler`; register the two routes next to the other internal ones), `internal/server/internal.go` only if a helper is shared

**Interfaces:**
- Consumes: Tasks 1–4.
- Produces:
  - `type Snapshot` with the contract's JSON fields; `type Compiler struct { Store store.Store; Feeds *feeds.Fetcher; Geo *geo.DB; Now func() time.Time }`; `func (c *Compiler) Build(ctx) (body []byte, version string, err error)` (manual `asn`/`country` expanded with the geo DB; the version is the sha256 of the body without it); `func (c *Compiler) Current(ctx) ([]byte, string, error)` (cached, rebuilt when `Invalidate()` was called or at most every 30 s); `func (c *Compiler) Invalidate()`; `func (c *Compiler) Allowed(p netip.Prefix) bool` (for `community.Engine`).
  - Routes: `GET /internal/share/filter` (304 on matching `If-None-Match`), `POST /internal/share/events` (body ≤ 1 MiB; `RecordSeen` with keys passed through `community.Key`; `AddRefusals`; 400 on bad JSON).
- [ ] **Step 1: Write the failing tests**:
  - `TestSnapshotHasEverySource`: one allow, one manual address, one manual country (fixture → 2 prefixes), one enabled feed copy, one disabled feed, one active community ban, a name with filter on and sharing off, a suspended name → JSON equals the expected object; disabled feed absent; `names.mybox.community` false.
  - `TestFilterRoute304`: second GET with the ETag → 304.
  - `TestFilterRouteRefusesProxiedRequest`: request carrying `X-Client-IP` → 404 (as the other internal routes).
  - `TestEventsRecordSeenAndRefusals`: posts one seen and two refusals → store has them; private key in `seen` dropped.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and see them pass.**
- [ ] **Step 5: Commit** — `server: filter snapshot and share events`

### Task 6: Box routes

**Files:**
- Create: `internal/server/protect_box.go`, `internal/server/protect_box_test.go`
- Modify: `internal/server/server.go` (`Deps` gains `Community *community.Engine`), `internal/server/share.go` (`boxAuth` takes a body limit: `maxShareBody` stays the default, `/v1/share/bans` passes 32 KiB), `internal/server/server.go` (routes under `cfg.shareEnabled`)

**Interfaces:**
- Consumes: Tasks 1, 3, 5 (`Compiler.Invalidate` after a settings change).
- Produces: `PUT /v1/share/protection`, `POST /v1/share/bans`, `GET /v1/share/refusals` exactly as the contract.
- [ ] **Step 1: Write the failing tests** (`setupShare`, `signedRequest`):
  - `TestProtectionPutAndAnswer`: PUT `{true,false}` → 200 with `share_bans_at` null and `community_size` 0; PUT `{true,true}` → `share_bans_at` set.
  - `TestProtectionOffPurgesReports`.
  - `TestBansRejectsBadItems`: reason `x`, app `../a`, key `garbage`, `since` 2 days ago, 201 items → 400 each.
  - `TestBansDropsSilently`: a valid item from a box with sharing off → 204, no report stored.
  - `TestRefusalsOnlyOwnName`: refusals for `mybox` and `other` → the box of `mybox` sees only its own; a box without a name → `[]`.
  - `TestContributedMonth`: a ban opened this month with the box's fleet as opener → 1; last month → 0.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run `./internal/server/` and see it pass.**
- [ ] **Step 5: Commit** — `server: box routes for relay filtering and ban sharing`

### Task 7: `share` — filter check

**Files:**
- Create: `cmd/share/filter.go`, `cmd/share/filter_test.go`
- Modify: `cmd/share/main.go` (`handleTLS`: decide after `route`, only when `box != ""`; poller started next to `Revalidate`)

**Interfaces:**
- Consumes: the contract's snapshot JSON.
- Produces:
  - `type prefixSet` (sorted, merged `[from,to]` address ranges per family; `contains(netip.Addr) bool` by binary search), `func newPrefixSet([]string) prefixSet` (bad entries skipped).
  - `type filter struct { allow, manual… }` built from a snapshot; `func (f *filter) decide(a netip.Addr, name string) (reason, source string)` — `""` = pass; order and sources from the contract; `manual` source = which group matched; `external` source = the feed name.
  - `type filterHolder struct` with `get() *filter` (atomic pointer, nil before the first snapshot) and `func (h *filterHolder) poll(ctx, enrolmentURL string)` (GET with `If-None-Match`; 304 or error keeps the current one).
  - `func remoteAddr(c net.Conn) (netip.Addr, bool)`.
- [ ] **Step 1: Write the failing tests**:
  - `TestPrefixSetContains`: v4 and v6 ranges, edges, overlapping entries merged.
  - `TestDecideOrder`: an address in allow and manual → pass; in manual → `manual/country`; name suspended → `suspended`; feed + filter off → pass; feed + filter on → `external/FireHOL level 1`; community + filter on + community off → pass; community + both on → `community/address`; name absent from `names` → only manual and suspended apply.
  - `TestNoSnapshotLetsThrough`: holder empty → `handleTLS` reaches the tunnel.
  - `TestKeepsSnapshotWhenEnrolmentDown`: holder with a snapshot; enrolment returns 500 then is closed → still refused.
  - `TestRelayHostNeverFiltered`: a manual `0.0.0.0/0` ban → a connection to `RELAY_HOST` still reaches front; one to a share name is closed with no byte written.
  - `BenchmarkDecide500k`: 500 000 random `/32` entries; report `b.ReportAllocs()` and the heap size of the built set in the log (the number goes in the PR).
- [ ] **Step 2: Run them and see them fail** (`./cmd/share/`).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and see them pass.**
- [ ] **Step 5: Commit** — `share: filter visitors of shared apps from the relay snapshot`

### Task 8: `share` — report accepted connections and refusals

**Files:**
- Create: `cmd/share/events.go`, `cmd/share/events_test.go`
- Modify: `cmd/share/main.go`, `cmd/share/tunnels.go` (`toTunnel` records the accepted pair with the tunnel's `NodeID`; the per-box cap refusal counts `cap/ip-box`), `cmd/share/conncap.go` (the per-IP refusal counts `cap/ip` with name `""`), `cmd/share/tunnels_test.go` (`fakeEnrolment` serves `/internal/share/events` and `/internal/share/filter`)

**Interfaces:**
- Produces: `type events struct` with `seen(key, nodeID string)` (dropped if sent less than 10 minutes ago or key ""), `refused(name, key, reason, source string)` (counted per hour), `flush(ctx, url string)` (POST, chunks of 2 000 items; on failure keeps at most 10 000 pending items, oldest dropped). Flushed by the existing 15 s tick.
- [ ] **Step 1: Write the failing tests**: `TestSeenOncePerTenMinutes`, `TestRefusalsCountedPerHour`, `TestFlushRetriesThenDropsOldest`, `TestCapRefusalCounted` (the per-IP cap refusal reaches the post as `cap/ip`).
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run `./cmd/share/` and see it pass.**
- [ ] **Step 5: Commit** — `share: report accepted visitors and refusals to enrolment`

### Task 9: `/admin/protection` — lists and settings

**Files:**
- Create: `internal/admin/protect.go`, `internal/admin/protect.html`, `internal/admin/protect_test.go`
- Modify: `internal/admin/admin.go` (`Deps` gains `Feeds *feeds.Fetcher`, `Geo *geo.DB`, `Protect *protect.Compiler`; routes; a "Protection" link in `page.html`'s header)

**Interfaces:**
- Consumes: Tasks 1, 2, 4, 5.
- Produces (all POST under the existing `guard`, CSRF field `csrf`, redirect 303 to `/admin/protection#<section>`; every write calls `Protect.Invalidate()`):
  - `GET /admin/protection` — sections in this order: overview, external lists, community list, manual bans, shared names, relay allow list, connection caps. Same visual language as `page.html` (cards, chips, the existing CSS variables and `chart.go` for the 7-day chart). "IP Geolocation by DB-IP" link under the sections that show a country or a network.
  - Feeds: `/admin/protection/feeds` (add; fields name, url, format, column, every_s, max_entries, auth_name, auth_value, note), `/feeds/presets/{key}` (adds the preset disabled; a preset needing a key opens its edit form), `/feeds/{id}` (edit; an empty `auth_value` keeps the stored one), `/feeds/{id}/toggle`, `/feeds/{id}/refresh` (runs `Fetcher.Refresh` with a 60 s timeout), `/feeds/{id}/delete`.
  - Manual: `/admin/protection/manual` (kind, value, hours — 0 permanent, note; value checked: prefix, ASN number, two-letter country), `/manual/{id}/delete`.
  - Allow: `/admin/protection/allow`, `/allow/{id}/delete`.
- [ ] **Step 1: Write the failing tests** (`setup` from `admin_test.go`, Basic auth + CSRF):
  - `TestProtectionPageSections`: GET → 200, the seven section ids present, no feed key in the HTML (`auth_value` `secret-key-1234` shows `••••1234`).
  - `TestAddFeedAndPreset`: POST feed → listed disabled; preset `spamhaus-drop` → listed with its URL, disabled.
  - `TestFeedEditKeepsKey`: edit with empty `auth_value` → stored key unchanged.
  - `TestManualBanValidation`: country `fr` (lowercase) → 400; `XX` → stored uppercase; ASN `AS64500` → stored `64500`; prefix `garbage` → 400.
  - `TestWritesNeedCSRF`: any POST without `csrf` → 403.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run `./internal/admin/` and see it pass.**
- [ ] **Step 5: Commit** — `admin: protection page with lists, manual bans and allow list`

### Task 10: `/admin/protection` — community, names, overview

**Files:**
- Modify: `internal/admin/protect.go`, `internal/admin/protect.html`, `internal/admin/protect_test.go`

**Interfaces:**
- Consumes: Tasks 1, 3, 4, 9.
- Produces:
  - Overview: today's refusals by reason (`RefusalTotals`), 7-day chart by reason, number of boxes sharing their bans, number of active community bans.
  - Community: active bans (key, kind, reasons, fleets, since, until, strike) with `/admin/protection/community/lift` and `/community/allow` (form field `key`, the key is not in the path); permanent bans in their own table with the same buttons; flagged ASNs (`FlaggedASNs`) with `/admin/protection/asn/{asn}/ban` (adds a permanent manual ASN ban).
  - Names: each share name with today's refusals by reason, its `relay_filter` / `share_bans`, `/admin/protection/names/{name}/suspend` and `/resume`, and the existing revoke form.
  - Caps: today's `cap` refusals by key (top 50) and by name, the values in force (passed from `share`'s env through enrolment env `CONNS_PER_IP`/`CONNS_PER_IP_PER_BOX` read for display only, defaults 256/64).
- [ ] **Step 1: Write the failing tests**: `TestLiftKeepsStrikes`, `TestAllowFromCommunity` (key moves to the allow list, ban lifted), `TestBanFlaggedASN`, `TestSuspendResume` (snapshot `suspended` follows), `TestOverviewCounts`.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run `./internal/admin/` and see it pass.**
- [ ] **Step 5: Commit** — `admin: community list, shared names and refusal overview`

### Task 11: Wiring, retention, docs

**Files:**
- Modify: `cmd/enrolment/main.go` (build `geo.DB`, `feeds.Fetcher`, `protect.Compiler`, `community.Engine`; one janitor goroutine every minute: `Fetcher.Tick`, `Engine.Tick`, `Geo.Tick` (daily inside), `PruneProtection`, `Compiler.Invalidate` when something changed; same start/stop pattern as `gc.Run`), `internal/server/server.go` (`ConfigFromEnv`: `FILTER_MAX_ENTRIES` default 1000000), `compose.yaml` (enrolment env `FILTER_MAX_ENTRIES`, `CONNS_PER_IP`, `CONNS_PER_IP_PER_BOX`), `docs/self-hosting.md` (env tables; a "Protection" section: what is filtered, nothing on by default, presets and their licenses, AbuseIPDB free limits, the 24 h connection record and 30-day reports as personal data, DB-IP attribution), `README.md` (configuration list)
- Test: `cmd/enrolment/main_test.go` if one exists, else `internal/server/config_test.go` for `FILTER_MAX_ENTRIES`

- [ ] **Step 1: Write the failing test** `TestFilterMaxEntriesFromEnv` (unset → 1 000 000; `0` → error).
- [ ] **Step 2: Run it and see it fail.**
- [ ] **Step 3: Implement** the wiring, env and docs.
- [ ] **Step 4: Run `go test -race ./...` and `go vet ./...`; both clean.**
- [ ] **Step 5: Commit** — `enrolment: run feeds, community list and DB-IP; document relay protection`

### Task 12: End-to-end

**Files:**
- Create: `e2e/share_protect_test.go`
- Modify: `e2e/sharebox.go` only for a helper `(b *fakeBox) setProtection(filter, share bool)` and `(b *fakeBox) reportBan(key, reason string)`; `e2e/compose.e2e.yaml` (enrolment `FILTER_MAX_ENTRIES` small is not needed; nothing else unless the fleet-age rule blocks the test — then a test-only env `COMMUNITY_MIN_FLEET_AGE` read by enrolment, default `168h`, documented as test-only)

- [ ] **Step 1: Write the test** `TestCommunityBanCutsSharingBoxOnly`: four fleets A–D with one box each and a share name, all with sharing on except D. A visitor container connects to boxes A and B (so the relay sees it); A and B report it; after the next community tick and snapshot poll, the visitor's connection to box C (filter and sharing on) is closed with no byte, while box D (filter on, sharing off) still answers; box A lifts nothing and `/admin/protection` lists the ban with 2 fleets.
- [ ] **Step 2: Run it** with the e2e command from `ci.yml`, adding `TestCommunityBanCutsSharingBoxOnly` to the `-run` list in `ci.yml`.
- [ ] **Step 3: Commit** — `e2e: a community ban cuts only boxes that share`
