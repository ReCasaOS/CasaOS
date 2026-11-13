# Shared-app protection — Gateway Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Filter every request that reaches a shared app through the Gateway's share listener: access codes, IP lists, country filter, rate limit, OWASP rules (Coraza + CRS), automatic bans, with a journal and ban list the core reads.

**Architecture:** A new package `pkg/guard` holds the pushed config, the persistent store (bans, strikes, journal), the filters and the access gate; `route.GatewayRoute.GetShareRoute()` wraps `serveHost` with `guard.Guard.Handler`. The core pushes the config through new internal routes under `/v1/gateway/guard`; nothing else of the Gateway (dashboard listener, path routes) is touched.

**Tech Stack:** Go 1.26, Echo (management API), `net/http`, `github.com/corazawaf/coraza/v3` v3.8.1, `github.com/corazawaf/coraza-coreruleset/v4` (CRS v4.25.x), `github.com/oschwald/maxminddb-golang/v2` v2.9.0, `gotest.tools/v3/assert`.

**Spec:** `docs/superpowers/specs/2026-11-12-protection-partage-design.md` (CasaOS repo). **Contract:** `docs/superpowers/plans/2026-11-12-protection-partage-contract.md` — every JSON shape, constant and route below is defined there; read it first.

**Worktree:** `D:\clients\casaos\_work\gateway-guard`, branch `feat/share-guard` from `inkly/main` (64eac83 or later). Tests need cgo for `-race`, absent on Windows: run them in Docker:

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v "D:/clients/casaos/_work/gateway-guard:/src" -v gateway-gomod:/go/pkg/mod -w /src golang:1.26 go test -buildvcs=false -race ./...
```

(The route test `TestTheManagementAPITakesTheTokenBareOrAsBearer` fails with `-count>1` on main already; run with `-count=1`.)

## Global Constraints

- Only the share listener is filtered. `GetRoute()` (dashboard, path routes) must behave exactly as before; `TestPublicListenerIgnoresPROXY` keeps passing.
- With no guard config, or `protection: false` and every app `open`, a shared app behaves exactly as on main (no extra header, no cookie, no body buffering).
- The visitor address is `r.RemoteAddr` (PROXY v2 from the core's tunnel); never read `X-Forwarded-For` or `X-Real-IP` in `pkg/guard`.
- The `recasaos_access` cookie never reaches an app: removed from the `Cookie` header before proxying, whatever its validity.
- Files: `guard.json` in the runtime path, `guard-state.json` in `DataPath`, both 0600, written by rename.
- Event `detail` and `path` never contain a cookie, a code, a query string or a header value.
- All copy shown to visitors exists in English and French, chosen by `Accept-Language` (`fr` prefix → French, else English).

## Review Focus

1. A visitor behind a shared address (family NAT) who mistypes the code a few times must not lock the whole household out for days: code-fail bans follow the strike durations (1 h first) — test in Task 4.
2. A large upload (2 GiB PUT to Nextcloud WebDAV) through `waf: "block"` must reach the app intact and not be held in memory: only 1 MiB is inspected — test in Task 6.
3. A WebSocket upgrade to a protected app with a valid cookie must work, and without one must get 401, not the HTML page — test in Task 4.
4. The Gateway restarted mid-ban must still refuse the banned key, and a corrupt `guard-state.json` must not stop the Gateway (start empty, log) — tests in Task 2.
5. IPv6 visitors from one `/64` share one rate counter and one ban; two different `/64`s do not — test in Task 3.

---

### Task 1: Guard config and its internal API

**Files:**
- Create: `pkg/guard/config.go`, `pkg/guard/config_test.go`
- Modify: `service/management.go` (hold the guard), `route/management_route.go` (routes), `route/management_route_test.go`, `common/config.go` (`ConfigKeyDataPath = "gateway.DataPath"`, default `/var/lib/casaos/gateway`), `main.go` (create the guard with runtime and data paths)

**Interfaces:**
- Produces:
  - `type Config struct { Secret []byte; Protection bool; Apps map[string]AppConfig; People []Person }` with JSON tags exactly as the contract (`secret` is base64 std in JSON).
  - `type AppConfig struct { App string; Access string; Policy Policy }`, `type Policy struct { WAF string; RulesOff []int; Allow, Block []string; Countries Countries; RatePerMin int; Ban BanRules }`, `type Countries struct { Mode string; List []string }`, `type BanRules struct { WAFHits, WAFWindowS, Scan404, ScanWindowS, RateStrikes, RateWindowS, CodeFails, CodeWindowS int }`, `type Person struct { ID string; Gen int; CodeHash string; Apps []string }`.
  - `func (c Config) Validate() error` — errors name the field (`apps["x"].policy.allow[0]: not an IP or CIDR`).
  - `type Guard struct` with `func New(runtimePath, dataPath string, now func() time.Time) (*Guard, error)` (loads `guard.json` if present), `func (g *Guard) SetConfig(c Config) error` (validate, swap atomically, write `guard.json`), `func (g *Guard) Status() Status` (`Status` = the contract's `GET /v1/gateway/guard` body), `func (g *Guard) Configured() bool`.
  - `service.Management.Guard *guard.Guard`.
  - Routes `GET /v1/gateway/guard`, `PUT /v1/gateway/guard` (contract).

- [ ] **Step 1: Write the failing tests**
  - `TestConfigValidate`: the contract's example body decodes and validates; each of `access: "x"`, `waf: "x"`, `countries.mode: "x"`, `countries.list: ["fr"]` (lowercase), `allow: ["300.1.1.1"]`, `rate_per_min: 0`, `secret` of 16 bytes, a person with `code_hash` not 64 hex → error naming the field.
  - `TestSetConfigPersists`: `SetConfig` then `New` on the same paths → same config, `Configured()` true, file mode 0600.
  - `TestGuardRoutes` (route package, `asService`): `GET /v1/gateway/guard` → 200 `{"version":1,"configured":false,...}`; `PUT` with the example → 204, then `GET` → `configured: true`; `PUT` with a bad body → 400 and the previous config kept; without the internal secret → refused like `/v1/gateway/hosts`.

- [ ] **Step 2: Run them and see them fail** (Docker command above, `-run 'TestConfigValidate|TestSetConfigPersists|TestGuardRoutes'`). Expected: compile errors, `guard` undefined.

- [ ] **Step 3: Implement** `pkg/guard/config.go` and the two routes, using the same auth group as `/hosts`. `geo`, `people_last_used` and `waves` in `Status` are empty until Tasks 2 and 5 fill them.

- [ ] **Step 4: Run the tests and see them pass.** Expected: PASS, and `go vet ./...` clean.

- [ ] **Step 5: Commit** — `feat(guard): pushed protection config and its internal API`

### Task 2: Store — bans, strikes, journal, persistence

**Files:**
- Create: `pkg/guard/store.go`, `pkg/guard/store_test.go`, `pkg/guard/key.go`, `pkg/guard/key_test.go`
- Modify: `route/management_route.go` (events and bans routes), `route/management_route_test.go`

**Interfaces:**
- Consumes: `Guard` (Task 1).
- Produces:
  - `func Key(remoteAddr string) (key, ip string)` — the contract's ban key.
  - `type Event struct` and `type Ban struct` with the contract's JSON fields.
  - On `Guard`: `func (g *Guard) Record(e Event)` (grouping rule of the contract), `func (g *Guard) Events(app, reason string, limit int) []Event`, `func (g *Guard) Banned(key string) (Ban, bool)`, `func (g *Guard) BanKey(key, ip, app, reason string) Ban` (strike count → duration from the contract), `func (g *Guard) Unban(key string)`, `func (g *Guard) Bans() []Ban`, `func (g *Guard) Flush() error`.
  - Routes `GET /v1/gateway/guard/events`, `GET /v1/gateway/guard/bans`, `DELETE /v1/gateway/guard/bans/:key`.
  - `Status().Waves` and `Status().PeopleLastUsed` filled from the store (`func (g *Guard) NoteUse(personID string)` records last use, kept in memory and in `guard-state.json`).

- [ ] **Step 1: Write the failing tests**
  - `TestKey`: `203.0.113.9:4444` → `203.0.113.9`; `[::ffff:203.0.113.9]:1` → `203.0.113.9`; `[2001:db8:1:2:aaaa::1]:1` and `[2001:db8:1:2:bbbb::9]:1` → `2001:db8:1:2::/64`.
  - `TestBanDurationsEscalate`: with a fake clock, three `BanKey` on one key, each after the previous ended → `until - since` = 1h, 24h, 168h; after 30 days without a ban, the next is 1h again.
  - `TestUnbanKeepsStrikes`: ban, `Unban`, `Banned` false, next `BanKey` → 24h.
  - `TestJournalGroupsAndTrims`: two identical events 10 s apart → one line `count: 2`; 61 s apart → two lines; events older than 7 days and beyond 10 000 lines are dropped, oldest first; `Events("jellyfin","scan",5)` filters and caps.
  - `TestStoreSurvivesRestart`: ban + events, `Flush`, new `Guard` on the same `dataPath` → ban still active, events present.
  - `TestCorruptStateStartsEmpty`: `guard-state.json` = `{` → `New` returns a guard (no error) with no bans, and the file is replaced at the next `Flush`.
  - `TestWaves`: 21 bans for `jellyfin` within 10 min → `Status().Waves == [{jellyfin 21}]`; 20 → empty.
  - Route test: `DELETE /v1/gateway/guard/bans/2001:db8::%2F64` → 204 and the ban is gone.

- [ ] **Step 2: Run them and see them fail.**

- [ ] **Step 3: Implement.** The store is in memory under one mutex; `Flush` writes `guard-state.json` by rename; `New` starts a goroutine-free design — the caller (main) runs `Flush` every 30 s when dirty and on shutdown (add both to `main.go`).

- [ ] **Step 4: Run the tests and see them pass.**

- [ ] **Step 5: Commit** — `feat(guard): bans with escalating durations, journal, persistence`

### Task 3: Filters on the share route — bans, lists, rate, scan

**Files:**
- Create: `pkg/guard/filter.go`, `pkg/guard/filter_test.go`
- Modify: `route/gateway_route.go` (`GetShareRoute` wraps `serveHost` with the guard), `share_test.go` (end-to-end through the share listener)

**Interfaces:**
- Consumes: Tasks 1–2.
- Produces: `func (g *Guard) Handler(next http.Handler) http.Handler` — steps 1, 2, 3, 5 and 8 of the contract's order now; steps 4, 6, 7 are hooks filled by Tasks 5, 6, 4 (`g.country`, `g.waf`, `g.gate` fields of type `func(w, r, app AppConfig) (handled bool)`, nil = pass). Answers: 403 and 429 with a small text body in the visitor's language (`Accept-Language`), `Retry-After` on 429.

- [ ] **Step 1: Write the failing tests** (`httptest` with `RemoteAddr` set, a fake clock)
  - `TestNoConfigPassesUntouched`: no config → the app gets the request, headers unchanged, no event.
  - `TestProtectionOffSkipsFilters`: `protection: false`, `block: ["203.0.113.9"]` → passes, no event.
  - `TestBlocklistAndAllowlist`: blocked IP → 403 + event `blocklist`; an IP in a blocked `/24` → 403; `allow` matching a blocked range → passes and is never rate-limited (601 requests in a minute pass).
  - `TestRateLimitThenBan`: `rate_per_min: 600` → request 601 in the minute is 429 + `Retry-After`; three such minutes within 10 min → banned (`reason: rate`), next request 403 `banned`.
  - `TestRateIsPerSlash64`: two IPs in one `/64` share the 600; an IP in another `/64` does not.
  - `TestScanBan`: the app answers 404 to 100 requests within 60 s → the key is banned with `reason: scan`; 99 → not; an `allow`ed IP with 200 404s → never banned.
  - `TestBanIsBoxWide`: banned on `jellyfin` → 403 on `nextcloud` too.
  - `TestBannedEventsAreThrottled`: 50 requests from a banned key in a minute → one journal line with `count` 50 (grouping).
  - End-to-end in `share_test.go`: through `serveShare` (PROXY v2 + TLS, existing helpers), a blocked visitor gets 403 and the backend sees nothing.

- [ ] **Step 2: Run them and see them fail.**

- [ ] **Step 3: Implement `Handler`.** Per-key rate: a fixed one-minute window per key and app is enough; scan and strike counts: sliding windows from the policy's `ban` rules. Capture the app's status with a `ResponseWriter` wrapper that keeps `http.Flusher`, `http.Hijacker` (WebSockets) and `io.ReaderFrom`.

- [ ] **Step 4: Run the tests and see them pass**, plus the whole module (`./...`).

- [ ] **Step 5: Commit** — `feat(guard): bans, IP lists, rate limit and scan detection on shared hosts`

### Task 4: Access gate — entry page, session cookie, 401, 503

**Files:**
- Create: `pkg/guard/gate.go`, `pkg/guard/gate_test.go`, `pkg/guard/page.go` (embedded HTML template, inline CSS, no external asset), `pkg/guard/session.go`, `pkg/guard/session_test.go`
- Modify: `pkg/guard/filter.go` (wire the gate hook), `share_test.go`

**Interfaces:**
- Consumes: Tasks 1–3; `Person`, `Config.Secret`.
- Produces: `func NormalizeCode(s string) string`, `func CodeHash(secret []byte, code string) string`, `func SignSession(secret []byte, personID string, gen int, issued time.Time) string`, `func ParseSession(secret []byte, token string) (personID string, gen int, issued time.Time, ok bool)` — all per the contract (the core imports nothing from here; it re-implements `NormalizeCode`/`CodeHash` and the two must agree on the contract's test vector below).

Test vector (both repos, also in the contract): secret = the 32 bytes `0x00…0x1f`, code `abcd-efgh-jkmn` → normalised `ABCDEFGHJKMN` → `CodeHash` = `499b46abdc484007465c1451c284ae4485de2006d52fd01cc6fa0f0c6eed12c2`.

- [ ] **Step 1: Write the failing tests**
  - `TestCodeHashVector`: the test vector above; `NormalizeCode("abcd efgh-jkmn ")` = `ABCDEFGHJKMN`; `NormalizeCode("0O1IL")` = `""`.
  - `TestSessionRoundTrip`, `TestSessionRejectsTamperedSig`, `TestSessionExpiresAfter30Days` (fake clock).
  - `TestGatePageForBrowser`: `access: "code"`, `GET /` with `Accept: text/html` and no cookie → 302 to `/.recasaos/acces?next=%2F`; `GET /.recasaos/acces` → 200 HTML containing the app name and a `code` field.
  - `TestGate401ForNonPage`: `Accept: application/json` → 401; `Upgrade: websocket` without cookie → 401.
  - `TestGoodCodeSetsCookieAndRedirects`: `POST /.recasaos/acces` with `code=abcd efgh jkmn&next=/web/` → 303 to `/web/`, `Set-Cookie` with the contract's attributes and `Domain=mybox.box.example.org`; the person's `NoteUse` recorded.
  - `TestNextMustBeLocal`: `next=https://evil.example/` or `//evil` → redirect to `/`.
  - `TestCodeForOtherApp`: a person whose `apps` lacks `jellyfin` → 403 page "This code does not open this app" / « Ce code n'ouvre pas cette app ».
  - `TestCodeFailsBan`: 5 wrong codes in 10 min → banned `reason: code`, first ban 1 h.
  - `TestRevokedGenRejected`: cookie with `gen` 1 while the person has `gen` 2 → treated as no cookie; deleted person → same.
  - `TestCookieStrippedBeforeApp`: valid cookie plus another cookie `a=b` → the app sees `Cookie: a=b` only; also with an invalid `recasaos_access`.
  - `TestSessionRefreshedAfter24h`: cookie issued 25 h ago → response carries a new `Set-Cookie`.
  - `TestNoSecret503`: `access: "code"` with an empty secret → 503 and the app sees nothing.
  - `TestGateWorksWithProtectionOff`: `protection: false`, `access: "code"` → gate still applies.
  - End-to-end in `share_test.go`: WebSocket through a protected host with a valid cookie reaches the backend's upgrade.

- [ ] **Step 2: Run them and see them fail.**

- [ ] **Step 3: Implement.** Compare `code_hash` with `subtle.ConstantTimeCompare` against every person. The entry page posts to itself; its own path is never proxied. Copy (English / French): title "Access code" / « Code d'accès », button "Open" / « Ouvrir », error "Wrong code" / « Code incorrect », "This code does not open this app" / « Ce code n'ouvre pas cette app ».

- [ ] **Step 4: Run the tests and see them pass** (`TestCodeHashVector` asserts the vector above).

- [ ] **Step 5: Commit** — `feat(guard): access codes per person with a signed session cookie`

### Task 5: Country and network lookup

**Files:**
- Create: `pkg/guard/geo.go`, `pkg/guard/geo_test.go`, `pkg/guard/testdata/` (two tiny MMDB files written by the test helper with `github.com/maxmind/mmdbwriter`, test-only dependency, or committed fixtures under 50 KB)
- Modify: `pkg/guard/filter.go` (country hook, enrich every event and ban with `country`, `asn`, `as_org`), `pkg/guard/config.go` (`Status().Geo`)

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces: `type Geo struct`, `func OpenGeo(dir string) *Geo` (never fails; missing files = unknown), `func (g *Geo) Lookup(ip net.IP) (country string, asn uint, org string)`, reload when a file's mtime changes (checked at most once a minute).

- [ ] **Step 1: Write the failing tests**
  - `TestLookupFromFixtures`: an IP in the fixture's FR range → `FR`, ASN 3215, `Orange`.
  - `TestMissingFilesAreUnknown`: empty dir → `""`, 0, `""`; `Status().Geo` both false.
  - `TestReloadOnMtime`: replace the country file → new answer after the next minute (fake clock).
  - `TestCountryBlock` / `TestCountryAllow`: `mode: "block", list: ["FR"]` → FR visitor 403 `country`; `mode: "allow", list: ["FR"]` → a DE visitor 403, FR passes; unknown country (no file) → passes in both modes.
  - `TestEventsCarryGeo`: a blocklist event has `country`, `asn`, `as_org` filled.

- [ ] **Step 2: Run them and see them fail.**

- [ ] **Step 3: Implement** with `maxminddb-golang/v2`; DB-IP Lite field names: country file `country.iso_code`, ASN file `autonomous_system_number`, `autonomous_system_organization`.

- [ ] **Step 4: Run the tests and see them pass.**

- [ ] **Step 5: Commit** — `feat(guard): country filter and network names from DB-IP Lite`

### Task 6: OWASP rules (Coraza + CRS)

**Files:**
- Create: `pkg/guard/waf.go`, `pkg/guard/waf_test.go`
- Modify: `pkg/guard/filter.go` (WAF hook), `pkg/guard/config.go` (rebuild the WAF when the config changes), `go.mod`

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces: `func buildWAF(apps map[string]AppConfig) (coraza.WAF, error)` — **one** instance for all hosts; nil when no app has `waf != "off"` (no CRS loaded at all).

Directives, in order: `Include @coraza.conf-recommended`; `SecRuleEngine On`; the contract's body limit, partial processing and `SecResponseBodyAccess Off`; `Include @crs-setup.conf.example`; `SecAction` setting `tx.blocking_paranoia_level=1` and `tx.allowed_methods` = CRS default plus the contract's WebDAV methods; then, for each host, phase-1 rules matching `REQUEST_HEADERS:Host` exactly that use `ctl:ruleEngine=DetectionOnly` (observe), `ctl:ruleEngine=Off` (off) and one `ctl:ruleRemoveById=<id>` per entry of `rules_off`; then `Include @owasp_crs/*.conf`. Generated rule ids for these host rules live in a reserved range (`9100000`–`9199999`).

- [ ] **Step 1: Write the failing tests**
  - `TestNoWAFWhenAllOff`: `buildWAF` → nil.
  - `TestBlockSQLi`: `waf: "block"`, `GET /?id=1'%20OR%20'1'='1` → 403, event `waf` with a `rule` in 942xxx and a `detail`.
  - `TestObserveLetsThrough`: same request with `observe` → the app answers, event `observed`.
  - `TestRuleOffPerHost`: `rules_off` holding the rule id from the previous test on `jellyfin` only → passes on jellyfin, 403 on `nextcloud`.
  - `TestWebDAVMethodsAllowed`: `PROPFIND /remote.php/dav/files/u/` with an XML body → reaches the app under `block`.
  - `TestLargeUploadStreams`: 64 MiB PUT under `block` → the app receives exactly 64 MiB with the same SHA-256; `runtime.MemStats.HeapInuse` grows by less than 16 MiB during the request.
  - `TestWAFHitsBan`: 10 blocked requests in 10 min → banned `reason: waf`.

- [ ] **Step 2: Run them and see them fail.**

- [ ] **Step 3: Implement** with `github.com/corazawaf/coraza/v3/http.WrapHandler`, the CRS embedded with `WithRootFS(mergefs.Merge(crs.FS, …))` as in coraza-coreruleset's README. Check that `ctl:ruleEngine` and `ctl:ruleRemoveById` from a phase-1 rule apply per transaction in Coraza v3.8.1 (the tests above prove it); if not, fall back to one WAF per distinct per-host directive set and say so in the PR.

- [ ] **Step 4: Run the tests and see them pass.**

- [ ] **Step 5: Measure memory** — build the Gateway for `linux/arm64`, run it on an ARM box or in `docker run --platform linux/arm64` with QEMU, push a config with one app on `block`, and record the resident memory (`/proc/<pid>/status` `VmRSS`) with the WAF off and on, after 1 000 requests. Write both numbers in the PR description. Expected: noted, no pass/fail threshold; the core's dashboard text quotes it.

- [ ] **Step 6: Commit** — `feat(guard): OWASP Core Rule Set through Coraza, per-app mode and rule removal`
