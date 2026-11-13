# Relay-side protection — core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Each box tells the relay whether to filter its visitors and whether it shares its bans, sends the bans its Gateway starts when the owner agreed, and gives the dashboard the relay's numbers and refusals.

**Architecture:** Three signed calls are added to `share.Relay` (`pkg/share/relay.go`). Two settings join `GuardState` in `share-guard.json`; they are pushed to the relay inside the existing `resync` and on every change, and the last answer is kept in memory for `GuardView`. A new loop in `Service.Run` reads the Gateway's bans once a minute and sends the new ones. Two routes join the `/v1/share/guard` group.

**Tech Stack:** Go 1.26, Echo, `pkg/share`, `route/v1`.

**Spec:** `docs/superpowers/specs/2026-11-13-protection-relais-design.md`. **Contract:** `docs/superpowers/plans/2026-11-13-protection-relais-contract.md` — read it first.

**Worktree:** `D:\clients\casaos\_work\casaos-relay-guard`, branch `feat/relay-protection` from `inkly/main`. Copy `codegen/` in (`go generate .`). Tests in Docker, one package at a time:

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v "D:/clients/casaos/_work/casaos-relay-guard:/src" -v core-gomod:/go/pkg/mod -w /src golang:1.26 go test -buildvcs=false -race -p 1 ./pkg/share/ ./route/v1/
```

## Global Constraints

- Every box (master or member) pushes its own settings and its own bans, signed with its own box key through `Relay.Signed`.
- Defaults: `relay_filter` true when absent from the file; `share_bans` false; `share_bans` turns on only through `PUT /v1/share/guard/relay` with `consent: true`.
- A relay answering 404 (or `ErrRelayNoShare`) on the new routes: `relay.supported` false, nothing retried until the next resync, no error shown, no alert.
- Bans sent: only reasons `waf scan rate code`, started less than 24 h ago, key not inside the box's allow list nor the app's override allow list. The box's own `block` list is never sent (it is not in the Gateway's bans).
- Sharing turned off: nothing more is sent, at once (the loop reads the setting each minute and on change).
- No IP in logs or alerts.

## Review Focus

1. A box upgraded from v0.5.138 (file without the new keys) must push `relay_filter: true, share_bans: false` — test in Task 2 (`TestOldFilePushesFilterOnShareOff`).
2. A relay that is down at boot must not block the Gateway pushes nor the hosts — test in Task 2 (`TestRelayDownDoesNotBlockResync`).
3. A Gateway too old (no guard) with sharing on must not spam the relay or crash — test in Task 3 (`TestNoGuardNoReports`).
4. A restart must not resend a whole day of bans in one burst above 200 — test in Task 3 (`TestReportsChunkedBy200`).
5. Turning sharing off while a send is due must not send it — test in Task 3 (`TestOffStopsAtOnce`).

---

### Task 1: Relay client calls

**Files:**
- Modify: `pkg/share/relay.go`, `pkg/share/relay_test.go`

**Interfaces:**
- Produces:
  - `type RelayProtection struct { Filter bool \`json:"relay_filter"\`; ShareBans bool \`json:"share_bans"\`; ShareBansAt *time.Time \`json:"share_bans_at"\`; CommunitySize int \`json:"community_size"\`; ContributedMonth int \`json:"contributed_month"\` }`.
  - `func (r *Relay) SetProtection(ctx, filter, shareBans bool) (RelayProtection, error)` — `PUT /v1/share/protection`.
  - `type BanReport struct { Key, Reason, App string; Since time.Time }` (JSON `key reason app since`); `func (r *Relay) ReportBans(ctx, bans []BanReport) error` — `POST /v1/share/bans`, 204 expected.
  - `func (r *Relay) Refusals(ctx) (json.RawMessage, error)` — `GET /v1/share/refusals`.
  - All three go through the existing `call`; any 404 on these paths (whatever its text) returns `ErrRelayNoShare`.
- [ ] **Step 1: Write the failing tests** (`fakeRelay`): `TestSetProtectionParses`, `TestSetProtection404IsUnsupported` (body `404 page not found`), `TestReportBansSendsJSON` (method, path, body), `TestRefusalsPassThrough`.
- [ ] **Step 2: Run them and see them fail** (`-run 'Protection|ReportBans|Refusals'`).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run `./pkg/share/` and see it pass.**
- [ ] **Step 5: Commit** — `feat(share): relay calls for filtering and ban sharing`

### Task 2: Settings, push, view and routes

**Files:**
- Modify: `pkg/share/guard.go` (`GuardState` gains `RelayFilter *bool \`json:"relay_filter,omitempty"\``, `ShareBans bool \`json:"share_bans"\``, `ShareBansAt time.Time \`json:"share_bans_at,omitzero"\``; method `func (g GuardState) relayFilter() bool`), `pkg/share/guard_api.go` (`GuardView.Relay GuardRelay`; `func (s *Service) SetRelay(ctx, filter *bool, shareBans *bool, consent bool) (GuardView, error)`; `func (s *Service) RelayRefusals(ctx) (json.RawMessage, error)`), `pkg/share/service.go` (`pushRelay(ctx)` called in `resync` after the Gateway guard push and before the hosts, errors logged and ignored; `relayInfo` + `relaySupported` kept under the service lock), `route/v1/share.go`, `route/v1.go` (`PUT /guard/relay` with `headerTokenOnly`, `GET /guard/relay-refusals`)
- Test: `pkg/share/guard_test.go`, `pkg/share/guard_api_test.go`, `pkg/share/service_test.go`, `route/v1/share_test.go`

**Interfaces:**
- Consumes: Task 1.
- Produces: `type GuardRelay struct { Supported bool \`json:"supported"\`; Filter bool \`json:"filter"\`; ShareBans bool \`json:"share_bans"\`; ShareBansAt *time.Time \`json:"share_bans_at"\`; CommunitySize int \`json:"community_size"\`; ContributedMonth int \`json:"contributed_month"\` }`; errors `ErrGuard{"consent_required"}`, `ErrGuard{"relay_unsupported"}` (both 400 through `shareFail`).
- [ ] **Step 1: Write the failing tests**:
  - `TestOldFilePushesFilterOnShareOff`: guard file without the new keys → the first resync sends `{"relay_filter":true,"share_bans":false}`.
  - `TestSetRelayNeedsConsent`: `share_bans: true` without consent → `consent_required`, nothing pushed, file unchanged.
  - `TestSetRelayConsentStamps`: with consent → file has `share_bans` true and `share_bans_at` = now; turning it off clears `share_bans_at`.
  - `TestSetRelayOnOldRelay`: relay 404 → `relay_unsupported`; the view has `relay.supported` false.
  - `TestRelayDownDoesNotBlockResync`: relay call errors → the Gateway guard and the hosts are still pushed in order.
  - `TestViewCarriesRelayNumbers`: relay answers `community_size` 12 → `GuardView.Relay.CommunitySize` 12.
  - Route tests: `PUT /v1/share/guard/relay` without the header token → 401; `GET /v1/share/guard/relay-refusals` passes the relay body through, `[]` when unsupported.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run `./pkg/share/ ./route/v1/` and see them pass.**
- [ ] **Step 5: Commit** — `feat(share): relay filtering and ban sharing settings`

### Task 3: Sending the bans

**Files:**
- Create: `pkg/share/reports.go`, `pkg/share/reports_test.go`
- Modify: `pkg/share/service.go` (`Run` starts `reportLoop`; `SetRelay` wakes it), `pkg/share/service_test.go` (`fakeManagement` serves `GET /v1/gateway/guard/bans` from a settable body)

**Interfaces:**
- Consumes: Task 1 `ReportBans`; `Gateway.GuardBans` (raw JSON of the Gateway's `[]Ban`: `key ip country asn as_org app reason since until strikes`).
- Produces:
  - `func pickReports(bans []gatewayBan, allow func(app, key string) bool, sent map[string]time.Time, now time.Time) []BanReport` — pure: reason filter, 24 h, allow lists, not in `sent` (key `key|since`).
  - `func (s *Service) reportOnce(ctx) error` — reads the setting, the bans, sends in chunks of 200, records sent pairs, forgets pairs older than 24 h.
  - `reportLoop`: every minute and on wake; does nothing while `share_bans` is false, the Gateway has no guard, or the relay is unsupported.
- [ ] **Step 1: Write the failing tests**:
  - `TestPickReportsFilters`: bans with reasons `waf`, `country`, `blocklist`; one 25 h old; one whose key is in the app's override allow list → only the `waf` one.
  - `TestReportsSentOnce`: two ticks with the same Gateway answer → one `POST /v1/share/bans`.
  - `TestReportsChunkedBy200`: 450 new bans → three posts (200, 200, 50).
  - `TestOffStopsAtOnce`: sharing on, bans waiting, `SetRelay(share_bans false)` → the next tick posts nothing.
  - `TestNoGuardNoReports`: Gateway 404 on guard → no relay call.
  - `TestRelayErrorRetriesNextTick`: first post 500 → the same bans are posted on the next tick.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run `./pkg/share/` and see it pass.**
- [ ] **Step 5: Commit** — `feat(share): send the box's bans to the relay when the owner agreed`
