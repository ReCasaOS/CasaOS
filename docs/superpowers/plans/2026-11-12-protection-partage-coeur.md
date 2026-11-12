# Shared-app protection — core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep the protection settings and the people with their codes, push the effective config to the Gateway before its hosts, serve the dashboard's `/v1/share/guard` API, keep the DB-IP files current, and raise alerts for ban waves and a missing country database.

**Architecture:** Everything lives in `pkg/share` next to the sharing service: a guard state file beside `share.json`, pure merge and code functions, a Gateway client extension, and a push step inside the existing `resync`. Routes are added to `route/v1/share.go` under the existing `/v1/share` group. Alerts go through a hook on `share.Service` wired in `main.go` to a new `alerts.Hub` method.

**Tech Stack:** Go 1.26, Echo, the core's `model.Result` envelope, `pkg/share`, `pkg/alerts`.

**Spec:** `docs/superpowers/specs/2026-11-12-protection-partage-design.md`. **Contract:** `docs/superpowers/plans/2026-11-12-protection-partage-contract.md` (JSON shapes, constants, routes, test vector) — read it first.

**Worktree:** `D:\clients\casaos\_work\casaos-guard`, branch `feat/share-guard` from `inkly/main`. Copy `codegen/` in (generated, ignored by git: `go generate .`). `service` does not build on Windows: run tests in Docker, one package at a time (the whole suite is killed for memory):

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v "D:/clients/casaos/_work/casaos-guard:/src" -v core-gomod:/go/pkg/mod -w /src golang:1.26 go test -buildvcs=false -race -p 1 ./pkg/share/ ./route/v1/
```

## Global Constraints

- The Gateway config is pushed **before** any host, at every resync and at start; a write to the guard pushes at once (contract, "Core order of pushes").
- `share-guard.json`: `/var/lib/casaos/fleet/share-guard.json` under `Service.Root`, 0600, written by rename; it holds `code_hash`, never a code. A code appears only in the answer of the request that created or regenerated it.
- The HMAC secret is 32 random bytes created once; it never appears in an API answer or a log.
- With the Gateway answering 404 on `/v1/gateway/guard`: every guard write answers 400 `gateway_unsupported`; `PUT /v1/share/guard/apps/:name` with `access: "code"` is refused even if the rest of the call would be valid.
- Nothing is downloaded while `protection` is false.
- New alert sentences name the app only, never an IP.

## Review Focus

1. Unsharing an app must remove it from every person and from the guard's apps, and push the Gateway config before the host delete — test in Task 2.
2. A box upgraded with apps already shared must keep serving them exactly as before (no guard file yet → defaults, `protection: false`, every app `open`) — test in Task 1 and Task 2.
3. Two dashboard tabs writing at once must not lose a person: guard writes take the service's `ops` lock like the other share writes — test in Task 3.
4. A DB-IP download cut halfway must leave the previous file in place — test in Task 4.
5. A Gateway restart (tmpfs kept, `configured` true) followed by a box reboot (tmpfs gone) must end with the config pushed before any host — test in Task 2.

---

### Task 1: Guard state, policy merge, people and codes

**Files:**
- Create: `pkg/share/guard.go`, `pkg/share/guard_test.go`

**Interfaces:**
- Produces:
  - `const GuardFile = "share-guard.json"`.
  - `type Policy`, `type Countries`, `type BanRules` — same fields and JSON tags as the Gateway's (contract).
  - `type PolicyOverride struct { WAF *string; RulesOff []int; Allow, Block []string; Countries *Countries; RatePerMin *int; Ban *BanRules }`.
  - `type GuardApp struct { Access string; Override *PolicyOverride }`, `type Person struct { ID, Name string; Apps []string; CodeHash string; Gen int; CreatedAt time.Time }`, `type GuardState struct { Secret []byte; Protection bool; Box Policy; Apps map[string]GuardApp; People []Person }`.
  - `func DefaultPolicy() Policy` (contract values), `func Effective(box Policy, o *PolicyOverride) Policy` (lists add, deduplicated; others replace), `func (p Policy) Validate() error` (error text `bad_policy:<field>`).
  - `func NormalizeCode(s string) string`, `func CodeHash(secret []byte, code string) string`, `func NewCode() string` (contract alphabet, `crypto/rand`, formatted `XXXX-XXXX-XXXX`), `func newPersonID() string` (`p_` + 8 hex).
  - `func loadGuard(path string) GuardState` (missing or unreadable → defaults with a fresh secret, saved on first write), `func saveGuard(path string, g GuardState) error`.

- [ ] **Step 1: Write the failing tests**
  - `TestCodeHashVector`: the contract's vector (`499b46ab…12c2`).
  - `TestNewCodeShape`: 1 000 codes match `^[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{4}-[…]{4}-[…]{4}$` and are all different.
  - `TestEffectiveMerges`: box `allow: [A]`, override `allow: [B, A]` → `[A, B]`; override `waf: "block"` replaces `"off"`; override `countries` replaces the box's whole object; nil override → box unchanged.
  - `TestDefaultPolicyValues`: equals the contract's defaults field by field.
  - `TestPolicyValidate`: `rate_per_min: 0`, `countries.list: ["fr"]`, `allow: ["x"]`, `ban.scan_404: 0` → `bad_policy:<field>`.
  - `TestLoadGuardMissing`: no file → `Protection` false, 32-byte secret, `Box == DefaultPolicy()`.
  - `TestSaveGuardNoCode`: a saved state's file contains `code_hash` and no `code` key; mode 0600.

- [ ] **Step 2: Run them and see them fail** (Docker command, `./pkg/share/ -run 'Guard|Code|Effective|Policy'`).

- [ ] **Step 3: Implement** `pkg/share/guard.go`.

- [ ] **Step 4: Run the tests and see them pass.**

- [ ] **Step 5: Commit** — `feat(share): protection settings, people and access codes`

### Task 2: Push to the Gateway, before the hosts

**Files:**
- Modify: `pkg/share/gateway.go` (new calls), `pkg/share/gateway_test.go`, `pkg/share/service.go` (`resync`, `Share`, `unshareLocked`, `Status`), `pkg/share/service_test.go`

**Interfaces:**
- Consumes: Task 1.
- Produces:
  - `type GatewayGuardConfig` = the contract's `PUT /v1/gateway/guard` body (fields and JSON tags as there, `secret` base64 std); `type GuardGatewayStatus` = the contract's `GET /v1/gateway/guard` body.
  - On `Gateway`: `func (g Gateway) GuardStatus(ctx) (GuardGatewayStatus, bool /*supported*/, error)`, `func (g Gateway) SetGuard(ctx, cfg GatewayGuardConfig) error`, `func (g Gateway) GuardEvents(ctx, app, reason string, limit int) (json.RawMessage, error)`, `func (g Gateway) GuardBans(ctx) (json.RawMessage, error)`, `func (g Gateway) Unban(ctx, key string) error` — paths and bodies per the contract.
  - `func buildGatewayGuard(st State, g GuardState) GatewayGuardConfig` — one entry per shared app keyed by `<label>.<st.Host>` (the same host `SetHost` registers), `policy = Effective(g.Box, app.Override)`, `access` default `"open"`, people with only their shared apps.
  - On `Service`: `func (s *Service) pushGuard(ctx) error` (called with the ops lock held), field `guardPath()` = `<Root>/var/lib/casaos/fleet/share-guard.json`.

- [ ] **Step 1: Write the failing tests** (the existing fake Gateway in `service_test.go` records calls in order)
  - `TestResyncPushesGuardBeforeHosts`: two shared apps, fresh Gateway → the recorded calls start with `PUT /v1/gateway/guard`, then the two `PUT /v1/gateway/hosts`.
  - `TestShareAppPushesGuardFirst`: `Share(ctx,"jellyfin")` → `PUT /v1/gateway/guard` (with the new host in `apps`) before `PUT /v1/gateway/hosts`.
  - `TestUnshareDropsAppFromPeople`: a person with `["jellyfin","nextcloud"]`, unshare `jellyfin` → saved person has `["nextcloud"]`, guard `Apps` has no `jellyfin`, and the guard push comes before `DELETE /v1/gateway/hosts/...`.
  - `TestUpgradedBoxKeepsAppsOpen`: share state with apps and no guard file → pushed config has `protection: false` and every app `access: "open"`.
  - `TestOldGatewayStillServesHosts`: the fake Gateway answers 404 on `/v1/gateway/guard` → hosts are still registered (no guard push error stops the resync), and `GuardStatus` reports unsupported.
  - `TestBuildGatewayGuardHosts`: host key equals the host used by `SetHost` for the same app.

- [ ] **Step 2: Run them and see them fail.**

- [ ] **Step 3: Implement.** A Gateway 404 on the guard route is "unsupported": skip the push, go on with the hosts (an old Gateway has no protected app, since the API refused them).

- [ ] **Step 4: Run the tests and see them pass**, plus the whole `./pkg/share/`.

- [ ] **Step 5: Commit** — `feat(share): push the protection config to the Gateway before its hosts`

### Task 3: The `/v1/share/guard` API

**Files:**
- Create: `pkg/share/guard_api.go` (service methods), `pkg/share/guard_api_test.go`
- Modify: `route/v1/share.go` (handlers), `route/v1.go` (routes in `v1ShareGroup`, writes with `headerTokenOnly`), `route/v1/share_test.go`

**Interfaces:**
- Consumes: Tasks 1–2.
- Produces on `Service` (each write: ops lock, validate, save, `pushGuard`, return the view):
  - `func (s *Service) GuardView(ctx) GuardView` — the contract's `GET /v1/share/guard` body (merges the Gateway's `people_last_used` and `waves`; `geo` from Task 4's status, empty until then).
  - `SetProtection(ctx, on bool) (GuardView, error)`, `SetBoxPolicy(ctx, p Policy) (GuardView, error)`, `SetAppGuard(ctx, name, access string, o *PolicyOverride) (GuardView, error)`, `AddPerson(ctx, name string, apps []string) (PersonView, string /*code*/, error)`, `UpdatePerson(ctx, id, name string, apps []string) (PersonView, error)`, `NewPersonCode(ctx, id string) (string, error)`, `DeletePerson(ctx, id string) error`, `GuardEvents`, `GuardBans`, `Unban` (relay to `Gateway`).
  - Errors are the contract's codes (`ErrGuard{Code string}` with `Error() == Code`); handlers map them to 400 with that message.

- [ ] **Step 1: Write the failing tests**
  - `TestAddPersonReturnsCodeOnce`: `AddPerson` → code matches the shape; `GuardView` and the saved file never contain it; `CodeHash(secret, code)` equals the saved hash.
  - `TestNewPersonCodeBumpsGen`: gen 1 → 2, old hash replaced, pushed config carries gen 2.
  - `TestAddPersonUnknownApp`: an app not shared → `unknown_app:<name>`.
  - `TestBadName`: `""`, 41 characters, `"a\nb"` → `bad_name`.
  - `TestSetAppGuardNotShared`: → `not_shared`.
  - `TestCodeRefusedOnOldGateway`: Gateway 404 on guard → `SetAppGuard(...,"code",nil)` → `gateway_unsupported`, state unchanged.
  - `TestConcurrentAddPerson`: 20 parallel `AddPerson` → 20 people saved.
  - Route tests (`route/v1/share_test.go`, the existing fake `Share`): `POST /v1/share/guard/people` without the header token → refused like `PUT /v1/share/apps/:name`; with it → 200 and `data.code`; `GET /v1/share/guard` → `data.people[0]` has no `code` and no `code_hash`; `DELETE /v1/share/guard/bans/2001:db8::%2F64` reaches the Gateway with the key unescaped once.

- [ ] **Step 2: Run them and see them fail.**

- [ ] **Step 3: Implement.** `/v1/share` is already a reserved route group for `pkg/osmodule`; no change there. Add swagger comments in the style of the existing share handlers.

- [ ] **Step 4: Run the tests and see them pass** (`./pkg/share/ ./route/v1/ ./route/ ./pkg/osmodule/`).

- [ ] **Step 5: Commit** — `feat(share): API for protection, people and the Gateway's journal`

### Task 4: DB-IP Lite downloads

**Files:**
- Create: `pkg/share/geo.go`, `pkg/share/geo_test.go`
- Modify: `pkg/share/service.go` (`Run` starts the loop), `pkg/share/guard_api.go` (`GuardView.Geo`)

**Interfaces:**
- Consumes: Tasks 1, 3.
- Produces: `type GeoFetcher struct { Dir string; BaseURL string; Client *http.Client; Now func() time.Time }` (defaults `<Root>/var/lib/casaos/geo`, `https://download.db-ip.com/free`), `func (f *GeoFetcher) Update(ctx) error` (both files), `func (f *GeoFetcher) Status() GeoStatus{Country, ASN bool; UpdatedAt time.Time; Error string}`, and a loop in `Run`: when `protection` is true, update if a file is missing or older than 32 days, check every 6 hours, retry after 1 hour on failure.

Download URL: `<BaseURL>/dbip-<country|asn>-lite-YYYY-MM.mmdb.gz` for the current UTC month; on 404 try the previous month. Write to `<name>.tmp`, gunzip, check the MMDB metadata marker (`\xab\xcd\xefMaxMind.com` present in the last 128 KiB), rename to the contract's file names, mode 0644.

- [ ] **Step 1: Write the failing tests** (`httptest` server serving small gzipped files containing the marker)
  - `TestUpdateWritesBothFiles`: both files present after `Update`, `Status()` both true.
  - `TestPreviousMonthFallback`: 404 for this month, 200 for the previous → files written.
  - `TestCutDownloadKeepsOldFile`: server closes after half the body → old file unchanged, `Status().Error` set, no `.tmp` left.
  - `TestNotAnMMDB`: body without the marker → refused, old file kept.
  - `TestNoDownloadWhileProtectionOff`: the loop with `protection: false` makes no request (server counts 0).

- [ ] **Step 2: Run them and see them fail.**

- [ ] **Step 3: Implement.** HTTP timeout 2 minutes per file; `User-Agent: ReCasaOS/<version>`.

- [ ] **Step 4: Run the tests and see them pass.**

- [ ] **Step 5: Commit** — `feat(share): keep DB-IP Lite country and network files current`

### Task 5: Alerts — ban waves and missing country data

**Files:**
- Create: `pkg/alerts/share.go`, `pkg/alerts/share_test.go`
- Modify: `pkg/share/service.go` (field `Alert func(key, sentence string, problem bool)`; checked at each resync), `main.go` (`share.Default.Alert = alerts.Default.ShareGuardProblem`)

**Interfaces:**
- Consumes: Tasks 2–4.
- Produces: `func (h *Hub) ShareGuardProblem(key, sentence string, problem bool)` (category `Apps`, same shape as `FleetBackupProblem`). Keys and sentences:
  - `share:wave:<app>`: "The shared app <app> is under attack: <n> addresses were banned in 10 minutes." / resolved: "Resolved: the attack on the shared app <app> has calmed down."
  - `share:geo`: raised only when protection is on and some app's effective policy has `countries.mode != "none"` while `geo.country` is false: "Country filtering of shared apps is not working: the country database could not be downloaded." / resolved: "Resolved: country filtering of shared apps works again."

- [ ] **Step 1: Write the failing tests**
  - `TestShareGuardProblemUsesApps`: raise → sent in category `Apps` with the sentence; resolve → resolution sent.
  - `TestResyncRaisesWave`: fake Gateway `waves: [{jellyfin 23}]` → `Alert("share:wave:jellyfin", …, true)`; next resync with no wave → `false`.
  - `TestGeoAlertOnlyWhenNeeded`: countries `none` → never raised; `block` with no country file → raised.

- [ ] **Step 2: Run them and see them fail.**

- [ ] **Step 3: Implement.** The resync already runs every 10 minutes; check after the push.

- [ ] **Step 4: Run the tests and see them pass** (`./pkg/alerts/ ./pkg/share/`), then `go build ./... && go vet ./...` in Docker.

- [ ] **Step 5: Commit** — `feat(alerts): ban waves on shared apps and missing country data`
