# Virt Module Statistics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `CasaOS-VirtManagement` samples every VM's CPU, memory, network and disk through libvirt, keeps 7 days of history, serves it under `/v2/virt_management/stats`, and raises `cpu_high`, `mem_high` and `vm_crashed` alerts.

**Architecture:** One background `service.Stats` ticks every 5 s on the existing `libvirt.Conn`: one `ConnectGetAllDomainStats` RPC for all domains, turned into `stats.Sample`s (rates from deltas against the previous reading) and handed to the shared `stats.Store`/`stats.Evaluator` of CasaOS-Common. The evaluator is wired once with `Store.SetOnMinute(ev.OnMinute)` (this module tracks no minute rollover), and `Evaluator.Resolve` ends the alerts of a VM that leaves running/paused or is forgotten. A second libvirt lifecycle subscription (the existing `Events.WatchLifecycle` is left alone) drives `vm_crashed`. Routes follow contract section 2; alerts leave as `resource:alert-firing|resolved` on the MessageBus.

**Tech Stack:** Go 1.26, `github.com/digitalocean/go-libvirt` (pinned `v0.0.0-20260814190004-1a83157e1858`), echo + oapi-codegen v1.12.4 (`api/virt_management/openapi.yaml`, generated code is not committed), `github.com/ReCasaOS/CasaOS-Common/pkg/stats` (tag v0.4.28).

**Spec:** `D:/clients/casaos/_stats-design/docs/superpowers/specs/2026-10-20-stats-design.md`. Binding contract: `D:/clients/casaos/_stats-design/docs/superpowers/plans/2026-10-20-stats-contract.md` (names and shapes never renamed here; additions are in "Extensions"). Mockup: `.../mockups/stats-netdata-style.html` (the page's concern, not this repo's).

## Global Constraints

- Item id `vm:<uuid>` (libvirt UUID, canonical lower-case form), group `Machines virtuelles`, kind `vm`; route id is the item encoded for a URL (`vm%3A<uuid>`), one path segment.
- Sample keys (VM only): `cpu/total` (% of ONE core), `cpu/vcpus` (a count), `memory/assigned`, `memory/used` (bytes; `used` only when the guest balloon reports it), `network/rx:<nic>`, `network/tx:<nic>` (bytes/s), `network/dropped:<nic>` (packets/s), `disk/read:<disk>`, `disk/write:<disk>` (bytes/s), `disk/iops_read:<disk>`, `disk/iops_write:<disk>`, `alert/cpu_high`, `alert/mem_high`, `health/ok` (1 when the contract state is `running`, 0 otherwise, so the page's health chart exists for VMs; a VM that has been seen running keeps recording `health/ok` 0 while it is stopped, until it is forgotten and swept). A key that has no data is ABSENT, never 0.
- Alert defaults come from `stats.DefaultRules()` (cpu_high 90 % 10 min, mem_high 90 % 5 min). `cpu_high` is % of the allocation (cpu/total divided by vcpus); `mem_high` is used/assigned.
- Events: `resource:alert-firing`, `resource:alert-resolved`; properties `item`, `name`, `kind` (`vm`), `situation` (`cpu_high`|`mem_high`|`vm_crashed`), `value` (an integer string such as `"93"`; absent for `vm_crashed`), `minutes` (the rule's window, an integer string; absent for `vm_crashed`), `since` (RFC 3339), `source` (`virt_management`). Only a crash or a failed stop alerts; an asked shutdown never does.
- Errors: `404 unknown_item`, `400 bad_range`, `400 bad_rules` (message `bad_rules: rules[0].threshold`, contract section 2), `503 stats_unavailable`. No libvirt or guest text in any message.
- Routes sit behind the existing JWT middleware (`route/v2.go`); nothing public. The module stays optional: no new always-on dependency.
- State directory `/var/lib/casaos/stats/virt-management` (config `[stats] Path`); history survives restarts.
- Contract state of a VM (libvirt state name, as `StateName` spells it in `pkg/libvirt`, to `state`): `running` -> `running`; `blocked` -> `running`; `paused` -> `paused`; `shutdown` (being shut down) -> `running`; `shut-off` -> `stopped`; `crashed` -> `crashed`; `pmsuspended` -> `paused`; `no-state` or any other string -> `unknown`. A VM is sampled when its contract state is `running` or `paused`; `health/ok` is 1 only for `running`.
- No test uses the real clock (`stats.Clock` injected). Commits: no `Co-Authored-By`, no "Generated with", nothing about reviews or the owner.
- Budget: this module's sampling stays at or under 0.5 % of one core with 10 VMs (its own budget, not summed with the other services'), measured in Task 9.

## Review Focus

Failure modes the spec implies, most likely first; each has its test in the named task.
1. libvirtd restarts while sampling (RPC errors for a while, then a new connection): no VM is "forgotten", no rate spans the outage (a gap, not a spike), history continues. Task 4.
2. A VM restarts: its `cpu.time` and byte counters restart from 0, so cur < prev; must give no value for that tick, never 0 and never a negative or huge percent. Task 3.
3. A forced power-off, a migration, a save or a snapshot restore (Stopped details Destroyed, Migrated, Saved, FromSnapshot) must not raise `vm_crashed`; a crash raised then ignored by a deleted or restarted VM must resolve. Task 5.
4. Devices that appear mid-run or report nothing (hot-plugged NIC, empty CD-ROM, guest without balloon driver): series start when data starts, no zeros before; `vm_memory_used_unavailable` is noted. Tasks 3 and 6.
5. The stats directory is unwritable or full: sampling and the API keep running, `Record` errors are logged once per minute at most, series and alerts routes answer 503 `stats_unavailable` instead of panicking. Task 4.
6. A VM that was firing `cpu_high`/`mem_high` and is then stopped, crashes or is deleted resolves exactly once (`Resolve`). Task 4 `TestStatsStoppedVMResolvesItsFiringAlert`, `TestStatsDeletedVMResolvesItsFiringAlert`.
7. A failed snapshot (libvirtd away) or a VM missing from one snapshot resolves nothing and fires nothing again afterwards. Task 4 `TestStatsFailedSnapshotDoesNotResolve`.
8. Common's hourly `Sweep` never deletes this module's own files in the shared directory (`crashed.json`, `crashed.json.tmp`) nor the evaluator's `alerts.json`. Task 5 `TestStatsSweepKeepsOtherFiles`.
9. Every libvirt state maps to a contract state, none left unmapped. Task 3 `TestContractState`.

## File Structure

- `pkg/libvirt/stats.go` (+ `stats_test.go`): read libvirt's stats records into `DomainStats`. No policy.
- `service/stats_sample.go` (+ test): pure maths, two readings in, one `stats.Sample` out.
- `service/stats.go` (+ test): the `Stats` service (loop, names, items, series, alerts, state).
- `service/stats_crash.go` (+ test): lifecycle handling and `vm_crashed`, persisted in `crashed.json`.
- `service/events.go`: the two new event types in `EventTypes`.
- `route/v2/stats.go` (+ test), `api/virt_management/openapi.yaml`: the four routes.
- `pkg/config/{model,init}.go`, `build/sysroot/etc/casaos/virt-management.conf.sample`: `[stats] Path`.
- `main.go`: wiring. `pkg/libvirt/domain.go` + `testdata/domain_*.xml`: balloon stats period (Task 8).

Tests: pure packages and `service`/`route` unit tests run with plain `go test ./...` on Windows (if CasaOS-Common's `pkg/stats` turns out not to build on Windows, run the stats tests in WSL; root is not needed). Only `TestDriver*` tests need a libvirtd: WSL as root with libvirt installed, `sudo VIRT_TEST_LIBVIRT_URI=test:///default go test ./service -run '^TestDriver'` (what CI's last step does). `-race` needs cgo: CI runs it, locally optional.

---

### Task 1: Verify the libvirt calls, the test driver and the Common package

No production code; one probe test that stays.

**Files:**
- Create: `service/testdriver_stats_test.go`
- Modify: `go.mod`, `go.sum` (Common bump)

**Interfaces:**
- Produces: written answers (in the commit message) to the four checks below; later tasks branch on check B.

- [ ] **Step 0: Branch and worktree.** Do not work in a dirty checkout: `git -C D:/clients/casaos/CasaOS-VirtManagement status --short` (if the checkout is absent, clone `ReCasaOS/CasaOS-VirtManagement` there; the org remote is named `inkly` like the other ReCasaOS repos, check `git remote -v`). Then `git fetch inkly` and `git worktree add D:/clients/casaos/_stats-build/VirtManagement -b feat/resource-stats inkly/main`; every following command runs in that worktree. Baseline: `go generate ./... && go build ./... && go test ./... 2>&1 | tail -40`; write down what already fails, it is not yours.
- [ ] **Step 1: Re-verify the names in the module cache.** Run `go mod download github.com/digitalocean/go-libvirt` then grep the module dir (`go list -m -f '{{.Dir}}' github.com/digitalocean/go-libvirt`). Checked on 2026-10-20 at the pinned version, expected to still hold: `(*Libvirt).ConnectGetAllDomainStats(Doms []Domain, Stats uint32, Flags uint32) ([]DomainStatsRecord, error)`; `DomainStatsRecord{Dom Domain; Params []TypedParam}`; `TypedParam{Field string; Value TypedParamValue{D uint32; I interface{}}}` with `I` one of int32, uint32, int64, uint64, float64, string; group bits `DomainStatsState=1, CPUTotal=2, Balloon=4, VCPU=8, Interface=16, Block=32`; key constants `DomainStatsCPUTime` ("cpu.time", ns), `DomainStatsBalloonCurrent|Maximum|Available|Unused|Usable`, `DomainStatsVCPUCurrent`, `DomainStatsNetPrefix` + `DomainStatsNetSuffixRxBytes|RxPkts|RxDrop|TxBytes|TxPkts|TxDrop|Name`, `DomainStatsBlockPrefix` + `DomainStatsBlockSuffixName|RdBytes|RdReqs|WrBytes|WrReqs`; `LifecycleEvents(ctx)` yields `DomainEventLifecycleMsg{Dom, Event int32, Detail int32}`; `DomainEventType` Undefined=1, Started=2, Stopped=5, Crashed=8; `DomainEventStoppedDetailType` Shutdown=0, Destroyed=1, Crashed=2, Migrated=3, Saved=4, Failed=5, FromSnapshot=6; `DomainGetInfo(Dom) (state uint8, maxMem, memory uint64, nrVirtCPU uint16, cpuTime uint64, err)`. If `ConnectGetAllDomainStats` were absent: use `ConnectListAllDomains` + `DomainGetInfo` + `DomainMemoryStats` + `DomainInterfaceStats` + `DomainBlockStats` per domain (all present) and skip Task 2's RPC wrapper; if a name differs, fix the later tasks' signatures to the real ones before going on.
- [ ] **Step 2: Write `TestDriverAllDomainStats`** in `service/testdriver_stats_test.go`, reusing `testDriverVMs`' connection code (copy its connect block into a small helper `testDriverConn(t) *libvirt.Conn`; skip unless `VIRT_TEST_LIBVIRT_URI`). It calls `l.ConnectGetAllDomainStats(nil, 63, 0)`. If `libvirt.IsNoSupport(err)` is not available yet, check `errors.As` to `golibvirt.Error` with code `ErrNoSupport` inline. Assertions when supported: the record for the domain named `test` has a `state.state` param and a `cpu.time` param. When the driver answers NO_SUPPORT: `t.Skip` with the error text, and ALSO assert `DomainGetInfo` works for `test` and that two calls 1 s apart return a larger `cpuTime` (the driver's CPU time follows the wall clock; this is the property the fallback in Task 2 relies on).
- [ ] **Step 3: Run it** in WSL as root against libvirtd's test driver. Record **check B**: `ConnectGetAllDomainStats` supported by `test:///default` (yes/no) and which groups it fills. If NO: Task 2 also builds the `DomainGetInfo` fallback; if YES: it does not.
- [ ] **Step 4: Check C, Common.** Confirm tag `v0.4.28` of `github.com/ReCasaOS/CasaOS-Common` exists with `pkg/stats` of contract section 1 (`git ls-remote --tags inkly` in the Common checkout). If it does not exist yet, use a **local, uncommitted** `go mod edit -replace github.com/ReCasaOS/CasaOS-Common=D:/clients/casaos/CasaOS-Common` (the same allowance as the AppManagement and core plans), never commit that line; Task 9 refuses to finish while it is in `go.mod`. With the tag: `go get github.com/ReCasaOS/CasaOS-Common@v0.4.28 && go mod tidy`. Read Common's `pkg/stats` and answer in the commit message: (a) `stats.Point`, `SeriesData` and `Result` carry the JSON tags of contract section 1 (`[t,avg,min,max]`, `series` never null): if not, STOP and report to the Common plan; (b) `Store.SetOnMinute` fires once per item per written minute, with the minute's start, from the `Record` call that crosses the boundary (Task 4 never calls `OnMinute` itself); (c) `SetRules` returns `*stats.RuleError{Index, Field, Message}`; (d) `Items()` includes forgotten items (Task 4 lists only live VMs either way) and `Evaluator.Resolve` exists; (e) it builds on Windows.
- [ ] **Step 5: Check D, encoded ids.** Throwaway test (not committed): echo with `codegen.RegisterHandlersWithBaseURL` is not possible yet, so reduce to echo alone: route `GET /x/:id`, request `/x/vm%3A0d2e...`, assert `c.Param("id")` is `vm:0d2e...` after echo's own handling and note whether `url.PathUnescape` is also needed. Task 6 relies on this answer.
- [ ] **Step 6: Commit** `go.mod go.sum service/testdriver_stats_test.go`: `test: probe libvirt's test driver for domain stats; require Common <tag>`. The checks A-D answers go in the body.

### Task 2: Reading libvirt's stats

**Files:**
- Create: `pkg/libvirt/stats.go`, `pkg/libvirt/stats_test.go`

**Interfaces:**
- Produces:
  - `const AllStatsGroups uint32 = 63`
  - `type DomainStats struct { UUID, Name, State string; CPUTimeNs *uint64; VCPUs *int; MemAssignedKiB, MemAvailableKiB, MemUnusedKiB, MemUsableKiB *uint64; NICs []NICStats; Disks []DiskStats }` (`State` as `StateName`; nil = the domain did not report it)
  - `type NICStats struct { Index int; RxBytes, RxPkts, RxDrop, TxBytes, TxPkts, TxDrop uint64 }`
  - `type DiskStats struct { Name string; RdBytes, RdReqs, WrBytes, WrReqs uint64 }`
  - `func ParseDomainStats(rec golibvirt.DomainStatsRecord) DomainStats`
  - `func AllDomainStats(l *golibvirt.Libvirt) ([]DomainStats, error)` (one `ConnectGetAllDomainStats(nil, AllStatsGroups, 0)`; flags 0 so shut-off domains come back with their state only)
  - `func IsNoSupport(err error) bool` (libvirt `ErrNoSupport` or `ErrOperationUnsupported`, in the style of `IsNotFound` in `pool.go:157`)
  - only if check B said NO: `func InfoDomainStats(l *golibvirt.Libvirt) ([]DomainStats, error)` using `ConnectListAllDomains(1, 0)` + `DomainGetInfo` (fills State, CPUTimeNs, VCPUs, MemAssignedKiB from `memory`).

- [ ] **Step 1: Failing tests** in `pkg/libvirt/stats_test.go`, records built by hand from `golibvirt.TypedParam`:
  - `TestParseDomainStatsFull`: params `state.state`=int32(1), `cpu.time`=uint64(12_500_000_000), `vcpu.current`=uint32(2), `balloon.current`=uint64(1048576), `balloon.available`=uint64(1000000), `balloon.unused`=uint64(400000), `balloon.usable`=uint64(500000), `net.count`=1, `net.0.name`="vnet3", `net.0.rx.bytes`=100, `net.0.tx.bytes`=200, `net.0.rx.drop`=1, `block.count`=2, `block.0.name`="vda", `block.0.rd.bytes`=10, `block.0.wr.bytes`=20, `block.0.rd.reqs`=1, `block.0.wr.reqs`=2, `block.1.name`="sda" with no byte counters. Expect `State=="running"`, `*CPUTimeNs==12_500_000_000`, `*VCPUs==2`, `*MemAssignedKiB==1048576`, one NIC with Index 0 and the values above, ONE disk (`vda`; `sda` is skipped for lack of counters). The record's `Dom` gives `UUID` (canonical string) and `Name`.
  - `TestParseDomainStatsShutOff`: only `state.state`=int32(5) gives `State=="shut-off"`, all pointers nil, no NICs or disks.
  - `TestParseDomainStatsValueTypes`: the same counter delivered as int32, uint32, int64, uint64 parses to the same number; a negative int64 counts as absent.
  - `TestIsNoSupport`: true for `golibvirt.Error{Code: uint32(golibvirt.ErrNoSupport)}` wrapped with `fmt.Errorf("%w")`, false for `ErrNoDomain` and nil.
- [ ] **Step 2: Run** `go test ./pkg/libvirt -run 'Stats|NoSupport'`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement** the signatures above in `pkg/libvirt/stats.go`. A small unexported `uintOf(v golibvirt.TypedParamValue) (uint64, bool)` handles the value types; NIC and disk indexes come from the key `net.<N>.…` / `block.<N>.…`; collect by index in a map, then emit sorted by index.
- [ ] **Step 4: Run** the package tests. Expected: PASS. Mutation check: change `AllStatsGroups` to 31 and see no test notice (it is checked by Task 7's driver test) so also add `TestAllStatsGroups` asserting the value equals `uint32(State|CPUTotal|Balloon|VCPU|Interface|Block)` built from the named constants; then break it by hand, see it fail, revert.
- [ ] **Step 5: Commit** `pkg/libvirt/stats.go pkg/libvirt/stats_test.go`: `libvirt: read domain stats with one call`.

### Task 3: Turning two readings into a sample

**Files:**
- Create: `service/stats_sample.go`, `service/stats_sample_test.go`

**Interfaces:**
- Consumes: `libvirt.DomainStats`, `libvirt.NICStats`, `libvirt.DiskStats` (Task 2), `stats.Sample` (Common).
- Produces:
  - `const statsInterval = 5 * time.Second` and `const statsMaxGap = 3 * statsInterval`
  - `type statsReading struct { At time.Time; S libvirt.DomainStats }`
  - `func vmSample(prev *statsReading, cur statsReading) stats.Sample`
  - `func contractState(libvirtState string) string` (the mapping in Global Constraints) and `func healthOnlySample() stats.Sample` (`{health/ok: 0}`, the whole sample of a recorded VM that is no longer running or paused)

Rules `vmSample` implements: gauges are always computed when present (`cpu/vcpus`, `memory/assigned` = `MemAssignedKiB*1024`, `memory/used`). `memory/used` = Available − Usable when both are present, else Available − Unused when both are present, else absent; also absent when the subtraction would be negative. Rates exist only if `prev != nil` and `0 < cur.At−prev.At <= statsMaxGap`, and per counter only if `cur >= prev`. `cpu/total` = ΔcpuTimeNs / (Δseconds × 1e9) × 100. NIC key suffix is `nic<Index>` (the host tap name `vnetN` changes at every restart), disk suffix is the libvirt target name (`vda`). A NIC or disk present in `cur` but not in `prev` yields no keys that tick. `alert/cpu_high` = `cpu/total` ÷ vcpus when both exist and vcpus > 0 (not clamped); `alert/mem_high` = used ÷ assigned × 100 when both exist. `health/ok` is set on every sample: 1 when `contractState(cur.S.State) == "running"`, else 0 (a paused VM is 0).

- [ ] **Step 1: Failing tests** (`stats_sample_test.go`, helper `reading(at, DomainStats)`):
  - `TestVMSampleCPUPercent`: prev cpu 10e9 ns, cur 12.5e9 ns 5 s later, vcpus 2 → `cpu/total==50`, `alert/cpu_high==25`, `cpu/vcpus==2`.
  - `TestVMSampleFirstReadingHasNoRates`: prev nil → no `cpu/total`, no `network/*`, no `disk/*`, no `alert/cpu_high`; `cpu/vcpus` and `memory/assigned` present.
  - `TestVMSampleCounterReset`: cur cpu below prev → key `cpu/total` absent (assert with `_, ok := s["cpu/total"]; ok == false`), and the same for a NIC counter.
  - `TestVMSampleGap`: prev 60 s older → no rates at all, gauges kept.
  - `TestVMSampleNoBalloon`: no available/unused/usable → no `memory/used`, no `alert/mem_high`, `memory/assigned` present.
  - `TestVMSampleUsedFromUsable` (available 1048576 KiB, usable 262144 KiB → `memory/used == 786432*1024`, `alert/mem_high == 75` with assigned 1048576 KiB) and `TestVMSampleUsedFallsBackToUnused`.
  - `TestVMSampleNetworkAndDisk`: after 5 s, rx +5000 → `network/rx:nic0==1000`; tx, `network/dropped:nic0` = (Δrx.drop+Δtx.drop)/5; `disk/read:vda`, `disk/write:vda`, `disk/iops_read:vda`, `disk/iops_write:vda` likewise.
  - `TestVMSampleNewNICMidway`: a second NIC only in `cur` → no `nic1` keys, `nic0` unaffected.
  - `TestVMSampleHealth`: state `running` → `health/ok == 1`; `paused` → 0; `healthOnlySample()` is exactly `{health/ok: 0}`.
  - `TestContractState` (Review Focus 9): table `running`→`running`, `blocked`→`running`, `paused`→`paused`, `shutdown`→`running`, `shut-off`→`stopped`, `crashed`→`crashed`, `pmsuspended`→`paused`, `no-state`→`unknown`, `""`→`unknown`, `"garbage"`→`unknown`.
- [ ] **Step 2: Run** `go test ./service -run VMSample`. Expected: FAIL.
- [ ] **Step 3: Implement** `vmSample` and the constants in `service/stats_sample.go` (no libvirt calls, no clock).
- [ ] **Step 4: Run** the same command. Expected: PASS. Mutation checks, one by one, each must turn a named test red: remove the `cur >= prev` guard (CounterReset), remove the `statsMaxGap` guard (Gap), make a missing balloon give 0 (NoBalloon).
- [ ] **Step 5: Commit**: `virt: compute VM sample rates from two readings`.

### Task 4: The Stats service

**Files:**
- Create: `service/stats.go`, `service/stats_test.go`
- Modify: `pkg/config/model.go`, `pkg/config/init.go` (`mapTo("stats", StatsInfo)`), `build/sysroot/etc/casaos/virt-management.conf.sample`

**Interfaces:**
- Consumes: Tasks 2 and 3; `stats.Open`, `Store.Record|Query|Items|Forget|Sweep|Close`, `Store.SetOnMinute`, `stats.NewEvaluator(store, dir, emit)`, `Evaluator.Resolve|Rebuild|Rules|SetRules|Active` (`OnMinute` only through the hook).
- Produces (all JSON tags are the contract's field names):
  - `config.StatsModel{ Path string }`, `config.StatsInfo` default `/var/lib/casaos/stats/virt-management`; sample file gets a `[stats]` section with a comment.
  - `type StatsSource interface { Snapshot() ([]libvirt.DomainStats, error); Describe(uuid string) (title, detail string, err error) }` and `func NewConnSource(conn *libvirt.Conn) StatsSource` (Snapshot = `libvirt.AllDomainStats`; on `IsNoSupport` it switches for good to `InfoDomainStats` only if Task 2 built it; Describe = lookup by UUID, `DomainGetXMLDesc`, `libvirt.ParseDomainXML`, `titleOf` for the title and the description for the detail, else an OS hint such as the XML's OS type and architecture if the parser exposes one, else empty; the detail is text cut to 200 characters).
  - `type StatsPublisher interface { Publish(name string, properties map[string]string) }` (`*Events` satisfies it).
  - `type StatsOptions struct { Dir string; Clock stats.Clock; Source StatsSource; Publisher StatsPublisher }`
  - `func NewStats(o StatsOptions) (*Stats, error)` (opens the Store and the Evaluator in `Dir` and calls `store.SetOnMinute(ev.OnMinute)` once); `func (s *Stats) Tick()`; `func (s *Stats) Run(ctx context.Context)` (ticker of `statsInterval`, `Sweep` every hour, `Close` on `ctx.Done`)
  - `type StatsItem struct { ID, Name, Kind, Group string; State string; StartedAt *time.Time; Detail *string; CPUPercent, MemoryUsed, MemoryLimit *float64; Alerts int }` with json `id,name,kind,group,state,started_at,detail,cpu_percent,memory_used,memory_limit,alerts`; `func (s *Stats) Notes() []string` (list-level notes, `stats_capacity` for an hour after the item cap was hit)
  - `func (s *Stats) Items() []StatsItem`
  - `type StatsSeries struct { ID string; stats.Result; Scale StatsScale; Notes []string }` (json `id`, then `range,step_seconds,from,to,series` flattened from the embedded `stats.Result` through Common's own tags, then `scale`, `notes`; no `StatsSeriesData`, no duplicate points type; `StatsScale{ CPU string; CPUAllocation, MemoryLimit *float64 }` with `cpu` always `"percent_of_one_core"`)
  - `func (s *Stats) Series(id string, r stats.Range) (StatsSeries, error)`
  - `type StatsAlert struct { Key string; Since time.Time; Value *float64 }`, `func (s *Stats) Alerts(id string) ([]stats.Rule, []StatsAlert, error)`, `func (s *Stats) SetRules(id string, rules []stats.Rule) error`
  - `var ErrUnknownItem, ErrStatsUnavailable` (errors.New; Task 6 maps them to 404 and 503).

Behaviour decisions: item ids are `"vm:" + UUID`. `Tick` takes one `Snapshot`; on error it returns without touching state (no Forget, no Resolve, readings kept; the 15 s gap rule in Task 3 drops stale baselines). For each domain whose contract state is `running` or `paused` it computes `vmSample(prev, reading)` and calls `Store.Record(id, now, sample)`; the Evaluator hears about minutes only through the hook set in `NewStats`: `Stats` tracks no minute rollover and never calls `OnMinute`. A domain in any other state (stopped, crashed, unknown) drops its reading and, if it has been recorded before (it is in `Store.Items()` or was recorded since the start), records `healthOnlySample()` instead of nothing; a domain never seen running records nothing. At the first tick that sees a recorded VM leave `running`/`paused` (including `crashed`), `Evaluator.Resolve(id)` is called once. A UUID absent from two consecutive successful snapshots is `Resolve`d (if not already) and `Forget`-ed and dropped from Items (its series still answers for 7 days by id). The title and the detail are fetched when a UUID is first seen, on a `defined` lifecycle event (Task 5 calls `s.refreshTitle(uuid)`), and every 5 min; a failed lookup keeps the old name and detail, or the libvirt name and no detail. `started_at` is set by a lifecycle `Started` event (Task 5) or by the first snapshot that shows the VM `running` after one that showed it otherwise; for a VM already running when the service starts it stays `null` (libvirt gives no start time) and the page then shows no "since". Alert events carry the properties of Global Constraints: `value` is `strconv.FormatFloat(v, 'f', 0, 64)` and `minutes` is the rule's `Minutes` read with `Evaluator.Rules`. `stats.ErrTooManyItems` from `Record` is logged once an hour and puts `stats_capacity` in `Notes()` for the next hour. `Items()`: `cpu_percent` = last `cpu/total`, `memory_used` = last `memory/used` (null without it), `memory_limit` = last `memory/assigned`, `alerts` = active evaluator alerts + the crash alert; sorted by lowercased name. `Series` fills `scale.cpu_allocation` with the vCPU count, `scale.memory_limit` with assigned memory, drops every `alert/*` series, and adds the note `vm_memory_used_unavailable` when the domain is running or paused and no `memory/used` series has a non-nil point in the range. Id not live and not in `Store.Items()` → `ErrUnknownItem`; a `Store` error → `ErrStatsUnavailable`. At `NewStats`, call `Evaluator.Rebuild` for every stored `vm:` item. One mutex guards the maps (CI runs `-race`). `Record` errors are logged at most once per minute.

- [ ] **Step 1: Failing tests** (`stats_test.go`; fake `StatsSource` with a queue of snapshots and errors, fake clock with `Advance`, fake publisher that records calls, real `stats.Store` on `t.TempDir()`):
  - `TestStatsTickRecordsRunningVM`: two ticks 5 s apart (cpu 0 then 5e9 ns, 1 vcpu); `Series("vm:<uuid>", stats.RangeLive)` has chart `cpu` name `total` whose last point avg is 100; `Items()` shows state `running`, group `Machines virtuelles`, kind `vm`, `cpu_percent` 100.
  - `TestStatsStoppedVMKeepsListedWithoutSamples`: a shut-off domain that was never seen running is listed with state `stopped`, `cpu_percent` nil, and `Store.Items()` does not contain it.
  - `TestStatsStoppedVMRecordsHealthZero`: a VM seen running, then shut off: `Series` shows `health/ok` 1 then 0 (a 0 point, not a gap) and a gap on `cpu/total` for the stopped period.
  - `TestStatsStoppedVMResolvesItsFiringAlert` (Review Focus 6): `cpu_high` firing, the VM becomes shut-off (and, in a second case, `crashed`): at the first tick that sees it, exactly one `resource:alert-resolved`; `Items()[0].Alerts` is 0; later ticks publish nothing.
  - `TestStatsDeletedVMResolvesItsFiringAlert`: firing, the VM disappears from two successive snapshots: exactly one `resolved`, then `Forget`; the series still answers.
  - `TestStatsFailedSnapshotDoesNotResolve` (Review Focus 7): firing, then five snapshot errors spanning three minutes, then the VM is back and high: no `resolved` in between, no second `firing`; then low: exactly one `resolved`.
  - `TestStatsStartedAtAndDetail`: a `Started` event or a not-running to running transition sets `started_at`; a VM already running at the start has `started_at` null; the detail from `Describe` is cut to 200 characters and shown; a failed `Describe` keeps the previous detail.
  - `TestStatsCapacityNote`: `Record` returning `stats.ErrTooManyItems` is logged once in an hour and `Notes()` is `["stats_capacity"]` for that hour.
  - `TestStatsLibvirtdOutage` (Review Focus 1): ticks, then 3 snapshot errors spanning 40 s, then a snapshot with a much larger cpu.time: no `Forget` (item still in `Items()`), and the first point after the outage has no `cpu/total` (assert the series has a nil avg or no point at that time, not a spike).
  - `TestStatsForgetsAfterTwoSnapshotsWithoutTheVM`: absent from one successful snapshot → still listed; from two → not listed, `Series` still answers (history kept).
  - `TestStatsRenameKeepsHistory`: same UUID, new title from `Describe` after the 5-min refresh → `Items()[0].Name` changes, `Series` still has the earlier points.
  - `TestStatsMemoryNote`: a VM without balloon → `Series(...).Notes` equals `[]string{"vm_memory_used_unavailable"}` and no `memory/used` series; with balloon → no note.
  - `TestStatsSeriesHidesAlertKeys`: no returned series has chart `alert`.
  - `TestStatsUnknownItem`: `Series("vm:00000000-0000-0000-0000-000000000000", …)` → `errors.Is(err, ErrUnknownItem)`; so is an id without the `vm:` prefix.
  - `TestStatsCPUHighAlert`: 1 vcpu VM at 100 % for 10 minutes of ticks → the publisher received exactly one `resource:alert-firing` with `item`, `name`, `kind=vm`, `situation=cpu_high`, `source=virt_management`, `value` (an integer string such as `100`), `minutes=10`, `since`; dropping to 10 % for one minute → exactly one `resource:alert-resolved`; `Items()[0].Alerts` is 1 while firing and 0 after.
  - `TestStatsMemHighAlert`: same for `mem_high` at 95 % for 5 minutes (`situation=mem_high`, `minutes=5`).
  - `TestStatsUnwritableDir` (Review Focus 5): construct the service with `Dir` pointing at a path under a regular file; `NewStats` returns an error that `Run`'s caller can log, and a second service whose store is closed makes `Series` return `ErrStatsUnavailable`; `Tick` on it does not panic.
  - `TestStatsRebuildAtStart`: 9 firing-minutes recorded, service dropped and recreated on the same dir with a fake clock, one more firing minute → still exactly one `firing` over the whole run (the evaluator's persisted state, not this module's, guarantees it).
- [ ] **Step 2: Run** `go test ./service -run '^TestStats' -v`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement** `service/stats.go` and the config pieces per the signatures and decisions above. Add `StatsInfo` to the same place `StorageInfo` is declared (`pkg/config/init.go:13-35`).
- [ ] **Step 4: Run** the same command, then `go vet ./... && gofmt -l .`. Expected: PASS, no output. Mutation checks: Forget after one missed snapshot (ForgetsAfterTwo), record on error snapshots (Outage), leave the `alert/` filter out (HidesAlertKeys), emit `resolved` without a prior `firing` (CPUHighAlert), skip `Resolve` when a VM leaves running (StoppedVMResolves...), call `Resolve` on a failed snapshot (FailedSnapshotDoesNotResolve).
- [ ] **Step 5: Commit** `service/stats.go service/stats_test.go pkg/config build/sysroot/etc/casaos/virt-management.conf.sample`: `virt: sample VM statistics and keep their history`.

### Task 5: `vm_crashed` from libvirt lifecycle events

**Files:**
- Create: `service/stats_crash.go`, `service/stats_crash_test.go`
- Modify: `service/events.go` (EventTypes), `service/stats.go` (calls)

**Interfaces:**
- Consumes: `Stats` internals (Task 4), `golibvirt.DomainEventLifecycleMsg`.
- Produces:
  - `const EventResourceFiring = "resource:alert-firing"`, `EventResourceResolved = "resource:alert-resolved"`; both added to `EventTypes` with properties `item, name, kind, situation, value, since, source, minutes`, same `eventType()` helper (`service/events.go:42`).
  - `type LifecycleEvent struct { UUID string; Event golibvirt.DomainEventType; Detail int32 }`
  - `func (s *Stats) HandleLifecycle(ev LifecycleEvent)`
  - `func (s *Stats) WatchLifecycle(ctx context.Context, l *golibvirt.Libvirt)` (same shape as `Events.WatchLifecycle`, `service/events.go:128`: subscribe with `l.LifecycleEvents(ctx)`, convert, call `HandleLifecycle`; registered by `main.go` with `conn.OnConnect`, so every reconnection resubscribes)
  - state file `<Dir>/crashed.json` (`{"<uuid>":{"since":"<RFC 3339>","name":"..."}}`), written through a temp file and `os.Rename`.

Rules: firing when `(Stopped, Crashed=2)`, `(Stopped, Failed=5)` or `(Crashed, any)`; never for Stopped details Shutdown, Destroyed, Migrated, Saved, FromSnapshot, Recreated, nor for Shutdown/Suspended/Resumed events. Already firing for the UUID → no second event. Resolved on `Started` (which also sets the item's `StartedAt`), on `Undefined` (also calls `Evaluator.Resolve` for the cpu/mem alerts, then `Forget`), and during `Tick` when a firing UUID is reported `running`, or absent from two consecutive successful snapshots (covers events missed while libvirtd was down). A `Defined` event calls `refreshTitle`. The crash alert appears in `Items().Alerts` and in `Alerts()` with key `vm_crashed` and a nil value. Firing state is loaded from `crashed.json` at `NewStats`.

- [ ] **Step 1: Failing tests**:
  - `TestCrashTable` (Review Focus 3), table over `(Event, Detail)` → fires? : `(Stopped,2)` yes, `(Stopped,5)` yes, `(Crashed,0)` yes; `(Stopped,0)`, `(Stopped,1)`, `(Stopped,3)`, `(Stopped,4)`, `(Stopped,6)`, `(Shutdown,0)`, `(Suspended,0)` no. A firing publishes `resource:alert-firing` with `situation=vm_crashed`, no `value` key, `kind=vm`, `source=virt_management`, `name` = the VM's title.
  - `TestCrashFiresOnce`: `(Crashed,0)` then `(Stopped,2)` → one firing.
  - `TestCrashResolvedOnStart`, `TestCrashResolvedOnUndefine` (also asserts the item is forgotten but its series still answers), `TestCrashResolvedWhenTickSeesRunning`, `TestCrashResolvedAfterTwoSnapshotsWithoutVM`.
  - `TestCrashNoResolvedWithoutFiring`: `Started` for a VM that never crashed publishes nothing.
  - `TestCrashSurvivesRestart`: fire, rebuild the service on the same dir, `Alerts()` still lists `vm_crashed` with the original `since`, and a later `Started` publishes `resolved` once.
  - `TestCrashShownInItemsAndAlerts`: `Items()[0].Alerts == 1` and `Alerts()` returns `Key=="vm_crashed"`, `Value==nil`.
  - `TestEventTypesIncludeResource`: both names are in `EventTypes` with the eight properties.
  - `TestStatsSweepKeepsOtherFiles` (Review Focus 8): the hourly sweep (the unexported `s.sweep(now)` that `Run` calls) with the fake clock two hours later leaves `crashed.json`, a planted `crashed.json.tmp` and `alerts.json` in place.
- [ ] **Step 2: Run** `go test ./service -run 'Crash|EventTypes'`. Expected: FAIL.
- [ ] **Step 3: Implement** per the signatures; keep `HandleLifecycle` pure on the `Stats` maps (the libvirt subscription is a thin loop).
- [ ] **Step 4: Run** the same command plus `go test ./service`. Expected: PASS. Mutation checks: treat `Destroyed` as a crash (CrashTable), skip the `crashed.json` write (SurvivesRestart), resolve without checking the firing set (NoResolvedWithoutFiring).
- [ ] **Step 5: Commit**: `virt: alert when a VM crashes or fails to stop`.

### Task 6: The four routes

**Files:**
- Create: `route/v2/stats.go`, `route/v2/stats_test.go`
- Modify: `api/virt_management/openapi.yaml`, `route/v2/virt_management.go` (field `stats *service.Stats`), `route/v2.go` (`InitV2Router(conn, vms, st)`)

**Interfaces:**
- Consumes: `Stats.Items|Series|Alerts|SetRules` and the two errors (Task 4).
- Produces: operations `listStats` (`GET /stats`), `getStatsSeries` (`GET /stats/{id}`, query `range`), `getStatsAlerts` (`GET /stats/{id}/alerts`), `setStatsAlerts` (`PUT /stats/{id}/alerts`), tag `Stats methods`; path parameter `StatsID` is `type: string` (NOT `format: uuid`); `range` is a plain string (NOT an `enum`) and rule fields carry no min/max, so the handler, not the request validator, produces `bad_range` / `bad_rules`. Bodies and errors per contract: 200 returns the contract JSON bare (`{"items": […], "notes": […]}`, the Series object, `{"rules":[…],"active":[…]}`), errors are `BaseResponse{message: "<code>"}`.
  - `func (v *VirtManagement) ListStats|GetStatsSeries|GetStatsAlerts|SetStatsAlerts(ctx echo.Context, …) error` (exact parameter lists are what `go generate` emits from the yaml).

Handler rules: a missing `range` means `live`; `stats.ParseRange` false → 400 `bad_range`; `ErrUnknownItem` → 404 `unknown_item`; `ErrStatsUnavailable` → 503 `stats_unavailable`; `PUT` with a rule key other than `cpu_high`/`mem_high`, `threshold` outside 1..100 or `minutes` outside 1..120 → 400 with message `bad_rules: rules[<i>].<field>`: Common validates, the handler does not re-implement the bounds, it maps the `*stats.RuleError` from `SetRules` (`errors.As`, `Index` and `Field`) to that one message. The JSON field names of a rule are `key, enabled, threshold, minutes`. If check D showed echo hands the encoded id, `url.PathUnescape` it. No error message contains text from libvirt.

- [ ] **Step 1: Failing tests** (`route/v2/stats_test.go`; build an echo with `codegen.RegisterHandlersWithBaseURL(e, v, "/v2/virt_management")` on a `VirtManagement` whose `stats` is a real `service.Stats` over fakes exported through a tiny test helper in `service` (`service.NewTestStats(t)` in `service/stats_export_test.go` is not reachable from another package, so put the fakes in `service/statstest/statstest.go`, a non-test helper package, and use it from both):
  - `TestStatsListRoute`: `GET /v2/virt_management/stats` → 200, body has exactly the top-level keys `items` and `notes`, and `items` has one object whose keys are exactly `id,name,kind,group,state,started_at,detail,cpu_percent,memory_used,memory_limit,alerts`.
  - `TestStatsSeriesRouteEncodedID`: `GET /v2/virt_management/stats/vm%3A<uuid>?range=15m` → 200, `range=="15m"`, `scale.cpu=="percent_of_one_core"`, `series[0].points[0]` is a 4-element JSON array.
  - `TestStatsSeriesBadRange`: `?range=2d` → 400 body `{"message":"bad_range"}`; `?range=` empty also 400.
  - `TestStatsSeriesUnknown`: `vm%3A00000000-0000-0000-0000-000000000000` → 404 `unknown_item`.
  - `TestStatsAlertsGetPut`: `GET …/alerts` returns the two default rules (`cpu_high` 90/10, `mem_high` 90/5, enabled); `PUT` with `cpu_high` threshold 80 → 200 with the new value, a following `GET` agrees; `PUT` `threshold: 0` → 400 `bad_rules: rules[0].threshold`; unknown key → 400; unknown item → 404.
  - `TestStatsUnavailable`: a closed store → 503 `stats_unavailable`.
  - `TestStatsRoutesNeedToken`: through `route.InitV2Router`, `GET /v2/virt_management/stats` without Authorization from a non-loopback `RemoteAddr` → 401 (same behaviour as `/vms`).
- [ ] **Step 2: Run** `go generate ./... && go test ./route/... -run Stats`. Expected: FAIL.
- [ ] **Step 3: Edit the yaml** (paths, parameter `StatsID`, schemas `StatsItem`, `StatsSeries`, `StatsRule`, `StatsAlert`, tag group) and write `route/v2/stats.go` per the rules above.
- [ ] **Step 4: Run** `go generate ./... && go vet ./... && go test ./...`. Expected: PASS. Mutation: map `ErrUnknownItem` to 500 (SeriesUnknown fails), drop the `ParseRange` check (BadRange fails).
- [ ] **Step 5: Commit**: `virt: serve VM statistics and alert settings`.

### Task 7: Wiring, reconnection, and the test driver

**Files:**
- Modify: `main.go:67-98`
- Modify: `service/testdriver_stats_test.go`

**Interfaces:**
- Consumes: everything above. Produces: the running service.

Wiring: after `events := service.NewEvents(...)` create `st, err := service.NewStats(service.StatsOptions{Dir: config.StatsInfo.Path, Clock: realClock{}, Source: service.NewConnSource(conn), Publisher: events})`; on error log and carry on with the stats routes answering 503 (the module must still serve VMs); `conn.OnConnect(st.WatchLifecycle)`; `go st.Run(ctx)`; pass `st` to `route.InitV2Router`. `realClock` is a three-line type in `main.go` implementing `stats.Clock`.

- [ ] **Step 1: Extend the driver tests** (all skip without `VIRT_TEST_LIBVIRT_URI`):
  - `TestDriverStatsSampling`: real `NewConnSource` on the test driver, `Tick` twice 1 s apart with a fake clock stepping 5 s, then `Series("vm:<uuid of the domain named test>", stats.RangeLive)`. If check B said supported: assert a `cpu/total` point exists; if it fell back to `DomainGetInfo`: assert `cpu/total` exists and `memory/assigned` equals the domain's memory. Either way assert no `memory/used` series and the note `vm_memory_used_unavailable` (the driver has no balloon).
  - `TestDriverStatsNoVM`: create nothing, `ForceOff` and `Delete` the `test` domain if present is NOT allowed (the driver's domain is shared with other tests); instead assert that a `Stats` built over a fake source with an empty snapshot writes no item file in `Dir` (`Store.Items()` empty, directory has no per-item files) — the pure version of the "no VM" case for the proof.
  - `TestDriverStatsLifecycle`: create a VM with `vms.Create`, start it, `HandleLifecycle` fed by the real subscription (`WatchLifecycle` in a goroutine with a fake publisher): `ChangeState(ForceOff)` publishes nothing; `Delete` leaves no alert. (The test driver cannot make a VM crash; the crash path is covered by Task 5's table.)
- [ ] **Step 2: Run** in WSL as root: `sudo VIRT_TEST_LIBVIRT_URI=test:///default go test ./service -run '^TestDriverStats' -v`. Expected: PASS (or documented SKIP lines for what the driver does not do). Also `go build ./...` on Windows.
- [ ] **Step 3: Wire `main.go`** as above. Then the reconnection check by hand in WSL: run the service with `[libvirt] URI = test:///default`, `systemctl restart libvirtd`, watch the log: sampler logs one error line per outage not per tick, resumes within `libvirt.RetryInterval` (10 s) and `GET /stats/{id}?range=live` shows a gap, not a straight line across it.
- [ ] **Step 4: Commit**: `virt: start the statistics sampler with the service`.

### Task 8: Balloon statistics for new VMs

`memory/used` only exists if the guest balloon reports; libvirt/QEMU report nothing until a stats period is set on the memballoon device, and `BuildDomainXML` (`pkg/libvirt/domain.go:300`) defines none. Without this, VMs created by this module would never have a memory-used curve. Existing VMs are NOT touched (no `DomainSetMemoryStatsPeriod`, no redefinition): they show the `vm_memory_used_unavailable` note until the owner edits them.

**Files:**
- Modify: `pkg/libvirt/domain.go` (`devicesXML` gets `Balloon *balloonXML \`xml:"memballoon"\``, set in `BuildDomainXML`), `pkg/libvirt/testdata/domain_bios_disk.xml`, `domain_uefi_iso.xml`, `domain_uefi_arm64.xml`, `pkg/libvirt/domain_test.go`

**Interfaces:**
- Produces: `<memballoon model='virtio'><stats period='5'/></memballoon>` in every new domain's XML.

- [ ] **Step 1:** Update the three golden files first (the test `TestBuildDomainXML`, `domain_test.go:34`, compares against them) and add `TestBuildDomainXMLHasBalloonStats` asserting the output contains `<memballoon model="virtio"><stats period="5">` in the encoder's own quoting (read the golden to copy the exact form).
- [ ] **Step 2:** Run `go test ./pkg/libvirt -run BuildDomainXML`. Expected: FAIL.
- [ ] **Step 3:** Add the element. The comment at `domain.go:284` already counts the balloon among the five PCI devices, so `pcieRootPorts` stays 12.
- [ ] **Step 4:** Run the package tests. Expected: PASS. Then in WSL as root the driver lifecycle tests: `sudo VIRT_TEST_LIBVIRT_URI=test:///default go test ./service -run '^TestDriverLifecycle'` (the test driver must still accept the definition; if it rejects `memballoon`, report it and drop this task, the sampler works without).
- [ ] **Step 5: Commit**: `libvirt: ask the balloon for guest memory statistics on new VMs`.

### Task 9: Budget, docs, release

**Files:**
- Create: `service/stats_bench_test.go`
- Modify: `README.md`, `common/constants.go`

- [ ] **Step 1:** `BenchmarkTick10VMs`: a fake source with 10 running domains (4 vcpus, 2 NICs, 2 disks each), real `Store` in `b.TempDir()`, fake clock. Run `go test ./service -run xxx -bench Tick10VMs -benchmem`. Report ns/op in the commit message; the pass line is under 5 ms per tick (0.1 % of a core every 5 s) for parsing, maths and `Record`. The RPC itself (QEMU monitor queries per domain) is measured on the real machine in the module proof: `top`-style CPU of `casaos-virt-management` over 5 minutes with 10 defined VMs, at most 0.5 % of one core (the module's own budget); report it.
- [ ] **Step 2:** README: add the four routes to the table, the state directory `/var/lib/casaos/stats/virt-management` and `[stats] Path`, the events `resource:alert-*`, the note that memory used needs a balloon driver and that VMs made before this version lack the period (Task 8). Run `go test ./...` once, `gofmt -l .`, `go vet ./...`, and `grep -n "replace.*CasaOS-Common" go.mod` must print nothing (the local `replace` of Task 1 is gone and `go.mod` points at the released tag).
- [ ] **Step 3:** `common/constants.go`: `VirtManagementVersion` is `"0.1.0"` while the tags reach `v0.1.3`; set it to the version about to be tagged (`0.2.0`, a minor: new API surface). `module.json` is unchanged (no stats field; `min_recasaos` stays `0.5.23` because events nobody listens to are harmless on an older core; raise it only if the owner wants alerts guaranteed).
- [ ] **Step 4: Commit** `service/stats_bench_test.go README.md common/constants.go`: `virt: document the statistics routes; bump the version to 0.2.0`.
- [ ] **Step 5: Release (not done by this plan).** Pushing the branch, merging it, tagging `v0.2.0` and the GitHub release (workflow `.github/workflows/release.yml` runs `go generate`, `go test ./...` then GoReleaser on a `v*.*.*` tag, producing `linux-{amd64,arm64}-casaos-virt-management-v0.2.0.tar.gz`) are covered by the owner's standing orders. The separate, later step in the INSTALLER repo is the catalogue pin of `virt` to `v0.2.0` with the archives' sha256; this plan does not do it.

### Task 10: Handoff for the module-proof worker (separate worker, installer repo)

Not implemented here. The installer repo's `scripts/module-proof.sh` (+ `module-proof-legs.json`, `module-proof-report.py`, `test-module-proof.py`) boots a guest per leg, installs this module with `URI = test:///default`, and judges cases M1-M11, U1-U9. Add a case group **S** after M5 (module installed and running), using the module version that has stats (`PROOF_MODULE_VERSION` ≥ 0.2.0):

- **S1 routes answer:** with the box token through the gateway, `GET /v2/virt_management/stats` lists `vm:<uuid>` of the driver's domain `test` with group `Machines virtuelles`; `GET …/stats/<id>?range=15m` has `step_seconds` 60 and every series point is a 4-array; `range=2d` is 400 `bad_range`; an unknown id is 404 `unknown_item`; no token is 401; `PUT …/alerts` with `threshold: 0` is 400 `bad_rules…` and with `cpu_high` 80 is 200 and read back. Wait at least 2 minutes after the start for the first minute record (raise the leg `timeout` of 360 s accordingly).
- **S2 history survives a reboot:** before the restart of M10, note the newest point time of `range=1h` and the byte size of the files under `/var/lib/casaos/stats/virt-management/`; after the guest restarts and the service is back, `range=1h` still contains the earlier points (time < boot time) and the file sizes are unchanged (ring files have a fixed size: this also proves "bounded").
- **S3 no sample without a VM:** a leg or step where libvirt has no domain (the test driver always has `test`: either undefine it with `virsh -c test:///default` is NOT possible across connections, so use the config `[libvirt] URI` pointing at a driver instance built from an empty XML, e.g. `test:///<path to an empty test XML>`, prepared in the kit) shows `{"items":[]}` and no per-item file in the stats directory after 3 minutes.
- **S4 outage:** `systemctl restart libvirtd` on the guest: within 30 s the stats route answers 200 again and the series shows a gap (a null point) at the outage, never a spike; the service unit stays active.
- **S5 remove and invariants:** M6's "what is gone and what stays" and M11's invariance lists must name `/var/lib/casaos/stats/virt-management` (the owner decides if module removal keeps history, like VM disks: the sample assumes it stays, as `/DATA/VM` does); the CPU of `casaos-virt-management` over the 3-minute window is recorded in `$OUT` (judged at or under 0.5 % of one core, informational on a test driver).
- **Caveats to say in the proof's report:** the test driver has no balloon, so `vm_memory_used_unavailable` is the expected note (a PASS, not a gap); `vm_crashed` cannot be provoked on the test driver (covered by unit tests); cpu/total on the fallback path follows the driver's wall-clock CPU time (about 100 %).

---

## Extensions (beyond the contract)

1. Series name suffixes for VMs: NIC `nic<N>` (index, not the tap name), disk = libvirt target (`vda`); `disk/iops_read:<disk>` and `disk/iops_write:<disk>` also carry the disk suffix (the contract only shows it for read/write bytes).
2. `memory/used` = balloon available − usable (else available − unused); `memory/assigned` = `balloon.current`.
3. `scale.cpu_allocation` for a VM is the vCPU count (cores), `scale.memory_limit` the assigned bytes; the contract does not give the unit of `cpu_allocation`.
4. `Alert.value` is null for `vm_crashed`; `Items().alerts` and `GET …/alerts` also count `vm_crashed` although the Evaluator does not know it.
5. Error bodies reuse the module's `BaseResponse` `{ "message": "<code>" }`; success bodies are bare per contract (the module's other routes wrap in `data`).
6. `[stats] Path` config key and `<Dir>/crashed.json` (this module's, not Common's).
7. Needs from Common (all in the contract, confirmed in Task 1): `SetOnMinute` semantics, `Point`/`Result` JSON, `*stats.RuleError`, `Evaluator.Resolve`.
8. Task 8 (memballoon stats period on new VMs) is not in the spec; it is the precondition of the spec's "memory used when the balloon reports it".
9. `health/ok` for VMs (spec decision 14): 1 running, 0 otherwise; a VM seen running keeps recording `health/ok` 0 while stopped, until forgotten and swept.
10. `started_at` (observed start, null when unknown) and `detail` (description or OS hint, at most 200 characters) in the item (spec decision 13).
11. Events carry `minutes` and an integer `value` string; the cpu/mem alerts of a VM that leaves running/paused or is forgotten end through `Evaluator.Resolve`; the module never calls `OnMinute`.
12. The libvirt-to-contract state mapping (Global Constraints) and the list note `stats_capacity` (256-item cap).

## Self-review

- Spec coverage: one RPC for all VMs (T1, T2), cpu from deltas (T3), memory without fake zeros (T3, T4), per-NIC and per-disk (T3), 5 s live + minute history in Common (T4 uses it), lifecycle crash and resolved (T5), cpu/mem alerts and their resolution on stop, delete or failed call (T3, T4), libvirtd restart (T4, T7), start/stop/delete/rename (T4, T5), test driver (T1, T7), routes (T6), budget (T9), release and catalogue pin named (T9), proof (T10).
- Types checked across tasks: `DomainStats`, `statsReading`, `vmSample`, `StatsItem`, `StatsSeries`, `StatsAlert`, `ErrUnknownItem`, `ErrStatsUnavailable`, `LifecycleEvent`, `HandleLifecycle`, `WatchLifecycle` are used with one spelling.
- Not done by design: listing deleted VMs (their series answers by id for 7 days; adding a names file would list them), changing running VMs, per-vCPU detail.
