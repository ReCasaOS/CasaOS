# Stats: shared package `pkg/stats` (CasaOS-Common) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `github.com/ReCasaOS/CasaOS-Common/pkg/stats` (contract section 1, tag v0.4.28): a bounded, crash-safe, lock-free-readable ring-file store of per-minute avg/min/max, a 15-minute live ring, range queries with downsampling, a unit table, and an alert-rule evaluator, so AppManagement, virt and the core only measure and call it.

**Architecture:** One file per item (`<sha256(item)[:32 hex]>.ring`): two CRC'd header copies written alternately, one column of minute stamps, then three float32 columns (avg, min, max) per series, each column a ring of 10 080 slots, appended on demand up to 48 series. A minute is committed by writing all values, fsync, then the 4-byte stamp last; readers trust a slot only if its stamp equals the expected minute before and after reading it, so they need no lock. Every file operation goes through a small `fsys` seam so tests can kill, tear or fail any write. The evaluator keeps rules and active alerts in a separate `alerts.json` written by tmp + fsync + rename.

**Tech Stack:** Go 1.26.8, stdlib only (`os`, `hash/crc32`, `crypto/sha256`, `encoding/binary`, `encoding/json`, `math`), `testify` (already in `go.mod`). No new dependency, no build tag, no mmap, no goroutine.

**Spec:** `D:/clients/casaos/_stats-design/docs/superpowers/specs/2026-10-20-stats-design.md` (sections "Les mesures", "Les alertes", decisions 2, 4, 7). Contract (binding names and shapes): `D:/clients/casaos/_stats-design/docs/superpowers/plans/2026-10-20-stats-contract.md` section 1 and section 4. Mockup (not used by this package): `.../mockups/stats-netdata-style.html`.

Repository: `D:/clients/casaos/CasaOS-Common` (module `github.com/ReCasaOS/CasaOS-Common`, `go 1.26.8`, current tag v0.4.27 at `af9a6b6`, which is also `inkly/main`; the remote is named `inkly` and the new branch starts from `inkly/main`). Existing test style: testify `require`/`assert`, external `_test` packages, `t.TempDir()` (see `utils/file/file_test.go`, `pkg/mod_management/sdk_test.go`). CI (`.github/workflows/codecov.yml`) runs `go test -race -failfast ./...` on Linux with Go 1.26.

## Global Constraints

- Contract names and shapes are never renamed; additions are listed in "Extensions" below.
- 10 080 minutes in the ring; live = 15 min at 5 s (180 slots); periods: `live` 5 s x 180, `15m` 15 x 60 s, `1h` 60 x 60 s, `24h` 288 x 300 s, `7d` 336 x 1800 s; buckets = average of minute averages, minimum of minute minimums, maximum of minute maximums.
- Every series of a minute has avg, min and max. A missing key means "no data", never 0; a gap is a `Point` with Avg, Min and Max all nil.
- Series keys `chart/name` exactly as in the contract; the package treats `item` as opaque.
- Bounded: 48 series per item, 256 live items (forgotten items do not count) plus at most 256 forgotten items kept, key <= 63 bytes, item name <= 256 bytes; file size is a pure function of the series count.
- The package deletes, in its directory, only `*.ring.tmp` and `*.ring.bad`; every other file there (the Evaluator's `alerts.json`, `alerts.json.tmp`, `alerts.json.bad`, a service's own files) is never touched by `Open` or `Sweep`.
- Atomic on abrupt stop: at worst the minute being written is lost. Readable without a lock. Versioned header (`FormatVersion = 1`).
- No test reads the real clock; time comes from `Clock` (`FakeClock`). All time maths uses `Unix()`; `time.Local` must not matter.
- Windows host and Linux both supported: never rename or remove a file while this package holds it open; directory fsync is best effort.
- Commits: conventional style like the repo (`feat(stats): ...`), no `Co-Authored-By`, no "Generated with", nothing about reviews or the owner.
- Run tests with `go test ./pkg/stats/...` from `D:/clients/casaos/CasaOS-Common` (Windows host is enough). Linux-only: `-race` (needs cgo, check `go env CGO_ENABLED` and `gcc --version`; otherwise run from WSL: `cd /mnt/d/clients/casaos/CasaOS-Common && go test -race ./pkg/stats/...`) and `TestDiskFullTmpfs` (WSL as root, needs `mount -t tmpfs`; it skips itself elsewhere).

## Review Focus

Failure modes the spec implies that no spec sentence tests; each has its test in the named task.

1. Clock steps backwards or jumps from 1970 to now (NAS without RTC at boot): recording must not freeze or corrupt a slot. Task 4.
2. `/proc` and Docker glitches: NaN, +-Inf and huge values in a `Sample` are treated as absent, never stored as 0 or as garbage; a negative value is stored as given (services clamp negative rate deltas themselves). Task 4.
3. Hostile item names (`../x`, `a/b`, `app:x:y:1`, 300 bytes, empty, unicode, two names differing by case on a case-insensitive disk) never escape the directory and never collide. Task 2 and 4.
4. A brand-new or empty item must serialize as `"series": []`, not `null`, and a query must not fail. Task 5.
5. A rule edited while an alert is firing (disabled, threshold raised, minutes changed) or a metric that vanishes mid-alert (container stopped) must resolve exactly once and never leave a zombie "firing". Task 8.
6. A stopped or deleted item that was firing resolves through `Evaluator.Resolve`, once, and later minutes emit nothing. Task 8 `TestResolveStoppedItem`, `TestResolveThenOnMinuteEmitsNothing`.
7. A failed measurement (a minute in which the item had no `Record` at all, because the service's Stats call failed) neither fires nor resolves: the run restarts, the active alert stays until a committed minute shows otherwise or `Resolve` is called. Task 7 `TestSkippedMinuteDoesNotResolveFiring`.
8. A `.tmp` or `.bad` that belongs to another file (`alerts.json.tmp`, `alerts.json.bad`, a service's `crashed.json.tmp`) survives `Open` and `Sweep`; only `*.ring.tmp` and `*.ring.bad` are deleted. Task 4 `TestOpenQuarantine`, Task 6 `TestSweepKeepsForeignFiles`.
9. The item cap counts live items only: forgetting frees a slot, and forgotten items are themselves bounded. Task 6 `TestLiveCapIgnoresForgotten`, `TestForgottenAreBounded`.

---

## File Structure

All in `D:/clients/casaos/CasaOS-Common/pkg/stats/` (package `stats`):

| File | Responsibility |
|---|---|
| `types.go` | `Range`, `ParseRange`, `Sample`, `Point` (+JSON), `SeriesData`, `Result` (+JSON tags), errors, constants |
| `clock.go` | `Clock`, `SystemClock`, `FakeClock` |
| `units.go` | `Unit`, `SplitKey` |
| `fsys.go` | `file`/`fsys` seam, `osFS` |
| `ring.go` | on-disk format: create, open, validate, header A/B, addSeries, writeMinute, readWindow, quarantine |
| `live.go` | in-memory 5 s ring |
| `store.go` | `Store`, `Open`, `Record`, minute accumulator, `Items`, `Forget`, `Sweep`, `Close`, `SetOnMinute`, `Quarantined` |
| `query.go` | `Query`, grid and downsampling |
| `rules.go` | `Rule`, `DefaultRules`, validation, `RuleError` |
| `evaluator.go` | `Evaluator`, state file, `Rebuild` |
| `README.md` | format, limits, measured cost (filled in Task 11) |
| `*_test.go` | one per file above, plus `crash_test.go`, `bounds_test.go`, `bench_test.go`, `helpers_test.go` |

## Extensions (additions to the contract)

- E1 `func (s *Store) SetOnMinute(fn func(item string, minute time.Time))` (listed in the contract): called once per item per committed minute, after the commit succeeded, outside every lock, on the `Record` goroutine; never when the commit failed and never for a minute in which the item had no `Record`. It is the **only** wiring of the Evaluator: `store.SetOnMinute(ev.OnMinute)` once; services never call `OnMinute` themselves and keep no minute-rollover tracking. `emit` must not block.
- E2 `func (e *Evaluator) Resolve(item string)` (listed in the contract): ends all active alerts of an item that stopped (container not running, VM not running or paused, item forgotten) with one `resolved` each. Services call it at the tick that notices, before or together with `Forget`.
- E3 `func (e *Evaluator) Prune()`: drops settings and state of items no longer in `Store.Items()`.
- E4 `SystemClock{}` and `FakeClock` (`NewFakeClock(t time.Time) *FakeClock`, `Now`, `Set`, `Advance`), exported so the other repos' tests share them.
- E5 `func (s *Store) Quarantined() []string`: file names moved aside to `*.bad` at `Open`, for the service to log.
- E6 Exported errors: `ErrUnknownItem` (route 404 `unknown_item`), `ErrBadRange` (400 `bad_range`), `ErrBadItem`, `ErrBadKey`, `ErrSeriesCap`, `ErrTooManyItems`, `ErrOutOfOrder`, `ErrClosed`; `*RuleError{Index int; Field, Message string}` for 400 `bad_rules`. Services map any other error to 503 `stats_unavailable`.
- E7 Exported constants `Minutes = 10080`, `LiveSlots = 180`, `MaxSeries = 48`, `MaxItems = 256` (live items), `MaxForgotten = 256`, `MaxKeyLen = 63`, `MaxItemLen = 256`, `FormatVersion = 1`, `MaxFileSize`; func `SplitKey(key string) (chart, name string)`.
- E8 JSON (in the contract): `Point` marshals as `[t, avg, min, max]` with `null`; `SeriesData` has tags `chart, name, unit, points`; `Result` has `range, step_seconds, from, to, series` (`series` is `[]`, never `null`); `Rule` has lower-case json tags `key`, `enabled`, `threshold`, `minutes`. Services embed these types as they are and write no duplicate DTO for them.
- E9 Query semantics the contract leaves open: series of chart `alert` are never returned; a series with no data in the window is omitted; `live` points have Min = Max = Avg; `Series` is never nil. Unit vocabulary (Task 1) is `percent`, `bytes`, `bytes_per_second`, `packets_per_second`, `ops_per_second`, `count`, `flag`.
- E10 Floats are stored as float32 (relative error <= 6e-8): 12 bytes per series per minute.
- E11 `Evaluator.OnMinute` semantics for a minute that was never written: when `minute` is more than one minute after the item's `last`, the skipped minutes reset the run but do not end an active alert (see Review Focus 7); a minute that was written without the `alert/<key>` series resets the run and ends the alert.
- E12 `func (e *Evaluator) LastError() error`: the error of the last write of `alerts.json`, nil once a later write succeeds.

Contract points that do not hold as written: see "Deviations" at the end.

---

### Task 1: Branch, verification, types, clock, units

**Files:**
- Create: `pkg/stats/types.go`, `pkg/stats/clock.go`, `pkg/stats/units.go`, `pkg/stats/helpers_test.go`
- Test: `pkg/stats/types_test.go`, `pkg/stats/units_test.go`

**Interfaces:**
- Produces: contract types `Range`, `RangeLive..Range7d`, `ParseRange`, `Sample`, `Point`, `SeriesData`, `Result`, `Clock`; E4 clocks; E6 errors; E7 constants; `Unit(chart, name string) string`; `SplitKey(key string) (chart, name string)`.
- Test helpers in `helpers_test.go` (used by every later task): `t0 = time.Date(2026,1,1,0,0,0,0,time.UTC)`; `func minuteAt(i int) time.Time` (= t0 + i minutes); `func feedMinute(t, s *Store, item string, minute time.Time, perSample func(k int) Sample)` (12 `Record` calls 5 s apart, then one more call at the next minute's first second so the minute commits; document that); `func f(v float64) *float64`.

- [ ] **Step 1: Verify the working ground.** In `D:/clients/casaos/CasaOS-Common`: `git status --short` must be clean; `git fetch inkly`; `git rev-parse inkly/main` and `git log -1 --format=%H v0.4.27` both equal `af9a6b6ffccb6a14151d507b1167f54208e2ee99` (v0.4.27 is `inkly/main` itself); `git switch -c feat/v0.4.28 inkly/main` (branch from `inkly/main`, nothing else). Run `go version` (expect go1.26.8), `go env CGO_ENABLED`, `go doc hash/crc32.ChecksumIEEE`, `go doc os.File.WriteAt`, `go test ./... 2>&1 | tail` once to know the baseline (note failures that pre-exist; do not fix them). Confirm `pkg/stats` does not exist.
- [ ] **Step 2: Write failing tests.**
  - `TestParseRange`: `"live","15m","1h","24h","7d"` -> `(Range, true)`; `"","1d","LIVE","7D"` -> `("", false)`.
  - `TestPointJSON`: `Point{T:60, Avg:f(1.5), Min:f(1), Max:f(2)}` marshals to `[60,1.5,1,2]`; gap `Point{T:60}` to `[60,null,null,null]`; both round-trip through `json.Unmarshal`.
  - `TestResultJSON`: `Result{Range: Range1h, StepSeconds: 60, From: 0, To: 60, Series: []SeriesData{{Chart: "cpu", Name: "user", Unit: "percent", Points: []Point{{T: 60, Avg: f(1)}}}}}` marshals to `{"range":"1h","step_seconds":60,"from":0,"to":60,"series":[{"chart":"cpu","name":"user","unit":"percent","points":[[60,1,null,null]]}]}`; `Rule` marshals as `{"key":..,"enabled":..,"threshold":..,"minutes":..}`.
  - `TestFakeClock`: `Now()` returns start; `Advance(90*time.Second)` moves exactly; `Set` replaces.
  - `TestSplitKey`: `"cpu/user"` -> `("cpu","user")`; `"network/rx:eth0"` -> `("network","rx:eth0")`; `"fs/free_percent:/mnt/data"` -> `("fs","free_percent:/mnt/data")`; `"nochart"` -> `("nochart","")`.
  - `TestUnit` (table, `Unit(chart,name)` strips the `:suffix` of name): `cpu/user|system|iowait|throttled|total` -> `percent`; `cpu/vcpus` -> `count`; `memory/rss|cache|swap|limit|assigned|used` -> `bytes`; `network/rx:eth0`, `network/tx:eth0` -> `bytes_per_second`; `network/dropped:eth0` -> `packets_per_second`; `disk/read`, `disk/write`, `disk/read:vda` -> `bytes_per_second`; `disk/iops_read`, `disk/iops_write` -> `ops_per_second`; `fs/free_percent:/` -> `percent`; `pids/count`, `pids/fds`, `restarts/count` -> `count`; `health/ok` -> `flag`; `alert/cpu_high`, `alert/mem_high` -> `percent`; unknown -> `""`.
- [ ] **Step 3: Run `go test ./pkg/stats/ -run 'ParseRange|PointJSON|FakeClock|SplitKey|Unit'`; expect build failure (package missing).**
- [ ] **Step 4: Implement** the files above exactly as the contract and Extensions state. `Point` JSON via `MarshalJSON`/`UnmarshalJSON`. `Unit` is a map lookup on `chart+"/"+name-before-first-colon`.
- [ ] **Step 5: Run the same command; expect PASS. Run `gofmt -l pkg/stats` (empty) and `go vet ./pkg/stats/`.**
- [ ] **Step 6: Commit** `feat(stats): types, clock and unit table`.

---

### Task 2: Filesystem seam and ring file format

**Files:**
- Create: `pkg/stats/fsys.go`, `pkg/stats/ring.go`
- Test: `pkg/stats/ring_test.go` (package `stats`, internal)

**Interfaces:**
- Produces (`fsys.go`): `type file interface { io.ReaderAt; io.WriterAt; Sync() error; Stat() (os.FileInfo, error); Close() error }`; `type fsys interface { OpenFile(name string, flag int, perm os.FileMode) (file, error); Rename(a, b string) error; Remove(name string) error; ReadDir(dir string) ([]os.DirEntry, error); MkdirAll(dir string, perm os.FileMode) error; SyncDir(dir string) error }`; `var osFS fsys` (`SyncDir` ignores errors, nil on Windows).
- Produces (`ring.go`):
  - `func ringPath(dir, item string) string` = `dir/<hex(sha256(item))[:32]>.ring`.
  - `type minuteCell struct { Avg, Min, Max float64; OK bool }`; `type minuteValue struct { Avg, Min, Max float64 }` .
  - `func createRing(fs fsys, path, item string) (*ring, error)` (writes `path+".tmp"` fully, fsync, rename, SyncDir).
  - `func openRing(fs fsys, path string) (*ring, error)` returns `errCorrupt` or `errVersion` (both wrapped with detail) for anything it cannot trust.
  - `func (r *ring) Item() string`, `Keys() []string` (snapshot published atomically after the header is durable), `ForgottenAt() int64`.
  - `func (r *ring) addSeries(key string) (int, error)`: appends a NaN-filled avg/min/max column triple, fsync, then writes the other header copy, fsync; `ErrSeriesCap` at 48, `ErrBadKey` for empty or > 63 bytes.
  - `func (r *ring) setForgotten(at int64) error` (0 clears).
  - `func (r *ring) writeMinute(minute int64, vals map[int]minuteValue) error`: for every series index below `len(Keys())` writes avg/min/max (NaN when absent from `vals`), `Sync`, then the stamp (uint32, little-endian) last. `slot = minute % Minutes`.
  - `func (r *ring) readWindow(fromMin, toMin int64) (map[string][]minuteCell, error)`: `toMin-fromMin+1` cells per series (max 10 080), reading only the needed segments (two when the window wraps), accepting a slot only if its stamp equals the expected minute before and after the values were read.
  - `func (r *ring) newestMinute() (int64, error)` (max stamp, 0 if empty).
  - `func ringFileSize(nseries int) int64` = `8192 + 40320*(1+3*nseries)`.
- Layout (document it as the comment at the top of `ring.go`): bytes 0..4095 header copy A, 4096..8191 copy B, then the stamp column (10 080 x uint32), then for series i columns avg, min, max (10 080 x float32 each). Header: magic `RCSTATS\x00`, version u32, minutes u32 (10 080), forgottenAt i64 (Unix seconds, 0 = alive), name length u16 + 256 bytes, nseries u32, 48 key slots of 64 bytes (1 length byte + 63), seq u64, CRC32-IEEE of everything before it. The writer rewrites the copy with the lower seq. A gap is float32 NaN.

- [ ] **Step 1: Write failing tests** (all on `t.TempDir()` with `osFS`):
  - `TestRingPathSafe`: items `"../x"`, `"a/b"`, `"app:x:y:1"`, `"Ünï"`, `"A"` vs `"a"` yield names matching `^[0-9a-f]{32}\.ring$` inside `dir`, all distinct.
  - `TestCreateOpenRoundTrip`: create, close, open: `Item()` equal, `Keys()` empty, file size == `ringFileSize(0)`, no `.tmp` left.
  - `TestAddSeriesAndSize`: add `cpu/user`, `memory/rss`: size == `ringFileSize(2)`; reopen: keys in insertion order; adding `cpu/user` again returns the same index and does not grow the file.
  - `TestWriteReadMinute`: write minute 100 with series 0 = {1.5, 1, 2}, series 1 absent; `readWindow(99,101)`: series 0 cell 100 OK with those values, cells 99 and 101 `!OK`; series 1 cell 100 `!OK` (NaN, not 0).
  - `TestLaterSeriesIsGapForEarlierMinutes`: write minute 100 with 1 series, then add a second series, write minute 101: second series at minute 100 is `!OK`.
  - `TestFloat32Precision`: values `17179869184` and `1.5e10` come back within 1e-6 relative.
  - `TestWrapAndOverwrite`: write minutes 1..10 080+5 on one series (use a `noSync` wrapper around `osFS`, defined in `helpers_test.go`); `readWindow(6, 10085)` has all cells OK; minute 1..5 requested via `readWindow(1,5)` are `!OK` (stamp now says 10 081..10 085).
  - `TestHeaderAlternates`: after 3 `addSeries`, both header copies are valid and their seq differ by 1; corrupt copy A's CRC (flip a byte via `os.WriteFile`-style patch): `openRing` still succeeds with the three keys.
  - `TestOpenRejects`: table of file mutations -> error kind: empty file, 5 000-byte file, bad magic, both headers corrupt, version 2 with valid CRC (`errVersion`), minutes != 10 080, nseries 49, file shorter than `ringFileSize(nseries)`, name length > 256. Extra trailing bytes are accepted.
  - `TestForgotten`: `setForgotten(123)` survives reopen; `setForgotten(0)` clears.
  - `TestRejectedNames`: `createRing` with empty name and a 257-byte name returns `ErrBadItem`.
- [ ] **Step 2: Run `go test ./pkg/stats/ -run 'Ring|Header|Forgotten|Float32|Wrap|Later|Open|Create|AddSeries|WriteRead|Rejected'`; expect build failure.**
- [ ] **Step 3: Implement `fsys.go` and `ring.go`** to the interfaces above. All integers little-endian via `encoding/binary`. `readWindow` computes segments from `minute % Minutes` (one `ReadAt` per segment per column) and rechecks the stamp segment after the value segments.
- [ ] **Step 4: Run the same command; expect PASS.**
- [ ] **Step 5: Mutation checks** (each must make a named test fail; revert after): drop the post-read stamp recheck (no test yet, covered in Task 5); write the stamp before the values (`TestWriteReadMinute` is blind to order, so Task 9 covers it: note it here and do not rely on this task for it); fill new columns with zero instead of NaN (`TestLaterSeriesIsGapForEarlierMinutes` fails); skip the CRC check (`TestOpenRejects`/`TestHeaderAlternates` fail).
- [ ] **Step 6: Commit** `feat(stats): ring file format and filesystem seam`.

---

### Task 3: Live ring

**Files:**
- Create: `pkg/stats/live.go`
- Test: `pkg/stats/live_test.go` (internal)

**Interfaces:**
- Produces: `type liveRing struct{ ... }` guarded by its own `sync.RWMutex` (never held across I/O); `func newLiveRing() *liveRing`; `func (l *liveRing) put(at time.Time, smp Sample)` (slot = `at.Unix()/5`, ring index `slot % LiveSlots`, last put in a slot wins, per-series `[LiveSlots]float64` with NaN = absent, series created on first appearance, at most `MaxSeries`); `func (l *liveRing) snapshot(now time.Time) (from, to int64, series map[string][]float64)` (regular grid of 180 slots ending at `floor(now/5)*5`, NaN where there is no sample, entries older than the grid dropped); `func (l *liveRing) reset()`.

- [ ] **Step 1: Write failing tests:**
  - `TestLiveGridShape`: after one `put`, `snapshot` returns `to-from == 179*5`, 180 values, `to % 5 == 0`.
  - `TestLiveExactValues`: `put(t0+5s, {"cpu/user": 0.1+0.2})` returns exactly `0.30000000000000004` (no float32 rounding in live).
  - `TestLiveMissingIsNaN`: puts at slots 0, 1, 3 only: slot 2 is NaN, not 0.
  - `TestLiveWindowSlides`: puts every 5 s for 20 minutes then snapshot at the end: the oldest value in the grid is the one 15 min back; earlier ones are gone; length stays 180.
  - `TestLiveSameSlotLastWins`: two puts at `t0+1s` and `t0+3s`: the second value is kept.
  - `TestLiveSeriesCap`: a 49th distinct key is ignored without panic.
  - `TestLiveAbsentKeyOverwritesStale`: a series present in slot k but absent when the ring wraps onto slot k reads NaN, not the old value.
- [ ] **Step 2: Run; expect build failure.**
- [ ] **Step 3: Implement `live.go`.** Keep a stamp array so wrapped slots are distinguished from the current ones.
- [ ] **Step 4: Run `go test ./pkg/stats/ -run Live`; expect PASS.**
- [ ] **Step 5: Mutation:** snapshot ignoring the stamp (stale values after wrap) must fail `TestLiveAbsentKeyOverwritesStale`.
- [ ] **Step 6: Commit** `feat(stats): in-memory live ring`.

---

### Task 4: Store write path

**Files:**
- Create: `pkg/stats/store.go`
- Test: `pkg/stats/store_test.go` (external package `stats_test` unless it needs `open(dir, clock, fsys)`; the unexported `func open(dir string, clock Clock, fs fsys) (*Store, error)` is what `Open` calls with `osFS`)

**Interfaces:**
- Consumes: `ring` and `liveRing` (Tasks 2, 3).
- Produces: `type Store struct{...}`; `func Open(dir string, clock Clock) (*Store, error)` (creates `dir` with `MkdirAll(0o755)`, deletes only leftover `*.ring.tmp` files and never any other file of `dir`, opens every `*.ring`, quarantining untrusted ones to `<name>.bad` (i.e. `<hex>.ring.bad`), replacing an older `.bad` for the same file; closes nothing it still needs); `func (s *Store) Record(item string, at time.Time, smp Sample) error`; `func (s *Store) Items() []string` (sorted, includes forgotten items); `func (s *Store) SetOnMinute(fn func(item string, minute time.Time))`; `func (s *Store) Quarantined() []string`; `func (s *Store) Close() error` (closes handles; later calls return `ErrClosed`).
- `Record` rules: validate item (`ErrBadItem`: empty or > 256 bytes); clear forgotten flag if set; create ring on first Record (`ErrTooManyItems` when 256 live items already exist; forgotten items do not count, and reviving a forgotten item counts as making a live one); drop sample values that are NaN, +-Inf, or beyond float32 range (treated as absent); `put` into the live ring; accumulate sum/min/max/count per series for the sample's minute (`at.Unix()/60`); when a sample belongs to a later minute than the accumulator, commit the previous minute first (`addSeries` for new keys, then `writeMinute` with avg = sum/count of the samples where the series was present, absent series stored as NaN), then, only if the commit succeeded, call the `onMinute` hook (once per item per committed minute), then start the new accumulator. Per-item write mutex held during I/O; the store-level mutex only guards the item map; no lock is held while `onMinute` runs. Unknown over-cap or over-long keys are skipped and reported as `errors.Join(ErrSeriesCap / ErrBadKey, ...)` after everything else was recorded. Clock handling: `at` earlier than the item's last sample by <= 60 s -> `ErrOutOfOrder`, nothing changes; earlier by more than 60 s -> a clock step: drop the pending accumulator, `live.reset()`, accept. A commit error is returned but the in-memory accumulator is dropped and the live ring is unaffected.

- [ ] **Step 1: Write failing tests** (`FakeClock`, `feedMinute`):
  - `TestMinuteAverageMinMax`: 12 samples of `cpu/user` = 10..21 in minute 0, commit: via the ring (Task 5 will expose it through `Query`; here assert through `s.items[item].ring.readWindow` in an internal test) avg 15.5, min 10, max 21.
  - `TestSeriesAbsentInSomeSamples`: `network/rx:eth0` present in 3 of 12 samples (values 3, 6, 9): avg 6, min 3, max 9; present in none: `!OK`.
  - `TestCommitOnlyOnRollover`: after 12 samples inside one minute no cell is OK; the 13th sample (next minute) commits the first; `SetOnMinute` fired once with that minute's start time; an item that is not recorded during the following minute produces no further call.
  - `TestPartialMinuteLostOnCloseIsOnlyLastMinute`: close with 7 pending samples, reopen: earlier minutes intact, the pending one absent.
  - `TestRecordRejectsBadSamples` (Review Focus 2): a sample with NaN, +Inf, -Inf, 1e300 for `cpu/user` and 5 for `cpu/system`: stored cpu/user is a gap, cpu/system is 5; a negative value `-3` is stored as -3.
  - `TestClockStepBackwards` (Review Focus 1): record minutes 0..4, then a sample 2 h earlier: no error, pending dropped, live ring reset; recording continues from there and later `Query` shows the new minutes; the earlier committed minutes are not corrupted. A sample only 20 s earlier returns `ErrOutOfOrder` and changes nothing. Jump from `time.Unix(0,0)+hours` to 2026 records normally.
  - `TestSeriesAppearsLater`: series `disk/read` first seen in minute 3: minutes 0..2 are gaps (not 0).
  - `TestHostileItemNames` (Review Focus 3): items `"../x"`, `"a/b"`, `"A"`, `"a"`, `"app:p:s:1"`, 256-byte name accepted; each creates exactly one file in `dir` and `Items()` returns the original strings; empty and 257-byte -> `ErrBadItem`; nothing is created outside `dir`.
  - `TestForgottenRevivesOnRecord`: covered in Task 6 (placeholder: not here).
  - `TestOpenQuarantine`: a garbage `.ring` file present at `Open`: `Quarantined()` lists it, `<name>.bad` exists with the original bytes, `Open` returns no error, the item can be recorded afresh; a leftover `x.ring.tmp` is deleted, while `alerts.json`, `alerts.json.tmp`, `alerts.json.bad` and `notes.tmp` placed in `dir` are untouched (Review Focus 8).
  - `TestItemCap`: 256 live items fine (one tiny series each, `noSync`), the 257th returns `ErrTooManyItems` and creates no file.
  - `TestCloseThenRecord`: `ErrClosed`.
- [ ] **Step 2: Run `go test ./pkg/stats/ -run 'Minute|Series|Record|Clock|Hostile|Quarantine|ItemCap|Close'`; expect build failure.**
- [ ] **Step 3: Implement `store.go`.**
- [ ] **Step 4: Run the same; expect PASS.**
- [ ] **Step 5: Mutation:** average over 12 samples instead of present ones (`TestSeriesAbsentInSomeSamples`); store NaN input as 0 (`TestRecordRejectsBadSamples`); remove the 60 s clock-step branch (`TestClockStepBackwards`).
- [ ] **Step 6: Commit** `feat(stats): store write path with minute commit`.

---

### Task 5: Query and downsampling

**Files:**
- Create: `pkg/stats/query.go`
- Test: `pkg/stats/query_test.go` (external)

**Interfaces:**
- Produces: `func (s *Store) Query(item string, r Range) (Result, error)`. Errors `ErrUnknownItem`, `ErrBadRange`. Takes no lock held by writers: only the item-map read lock for lookup, then `ReadAt`s and the live ring's own read lock.
- Grid: `live`: `to = floor(now/5)*5`, 180 points, step 5, from the live ring. Minute ranges: `lastMin = floor(now/60)*60 - 60`; `15m` 15 points `[lastMin-14*60 .. lastMin]`, `1h` 60 points; `24h`: `to = floor(lastMin/300)*300`, 288 points step 300; `7d`: `to = floor(lastMin/1800)*1800`, 336 points step 1800. `Result.From`/`To` = first and last point `T`; `Result.StepSeconds` = 5, 60, 60, 300, 1800. Series sorted by key; `Chart`/`Name` from `SplitKey`, `Unit` from `Unit`; series of chart `alert` and series with no data in the window omitted; `Series` never nil. A bucket averages the minute averages present, takes the min of mins and the max of maxes; a bucket with no minute is a gap. Live points have Avg = Min = Max.

- [ ] **Step 1: Write failing tests:**
  - `TestRangeShapes` (table, fake clock at `t0 + 20 days`, 3 days of minute data): point counts live 180, 15m 15, 1h 60, 24h 288, 7d 336; `StepSeconds`; consecutive `T` differ by the step; `To % step == 0`; `To-From == (n-1)*step`.
  - `TestMinuteRangeValues`: the minute from `TestMinuteAverageMinMax` appears in `15m` as `[T, 15.5, 10, 21]` with `T` == minute start Unix.
  - `TestSpikeSurvivesBucket`: 24h range, one minute has samples avg 10 / min 5 / max 100 while the other four minutes of its 5-min bucket are 10/10/10: bucket Max == 100, Min == 5, Avg == 10 (mean of minute averages, not of maxes); same check in a 7d bucket.
  - `TestBucketAverageIsMeanOfMinuteAverages`: minute k has avg k for 60 minutes: 7d bucket covering minutes 0..29 has Avg 14.5, Min 0, Max 29.
  - `TestGapsAreNilNotZero`: a 3-minute pause: those three `1h` points have Avg, Min, Max all nil; a 7d bucket half covered averages only the present minutes; a fully empty bucket is nil. `memory/used` recorded only in minutes 5..8 is nil elsewhere; a series never recorded is absent from `Series`.
  - `TestAlertSeriesHidden`: `alert/cpu_high` recorded, not in `Result.Series`.
  - `TestUnitsAndNames`: `network/rx:eth0` -> `Chart "network", Name "rx:eth0", Unit "bytes_per_second"`; series sorted ascending by `Chart+"/"+Name`.
  - `TestLiveQuery`: records every 5 s for 20 min; `live` has 180 points, exact values (float64), Min == Max == Avg, a skipped sample is a nil point.
  - `TestRingWrap7d`: 10 080 + 30 minutes of data (noSync fs): `7d` last bucket has data; the minutes older than the ring are gaps; the window crossing the slot wrap returns correct values for every minute (value = minute index).
  - `TestEmptyItemJSON` (Review Focus 4): item recorded once (nothing committed): `Query(item, Range7d)` returns no error, `json.Marshal(res)` contains `"series":[]` and never `null` for series.
  - `TestUnknownAndBadRange`: `ErrUnknownItem`; `Range("bogus")` -> `ErrBadRange`.
  - `TestTimeZoneIrrelevant`: feeding samples whose `time.Time` carry `time.FixedZone("x", 19800)` produces the same Result as UTC.
  - `TestQueryDoesNotWaitForWriter` (internal, uses the fault fs from Task 9's `helpers_test.go` blocking hook, defined here as `blockingFS` in `helpers_test.go`): a goroutine commits a minute and is parked inside `WriteAt`; `Query` of the same item returns within 1 s with the previously committed data and no torn value. Release the writer afterwards.
  - `TestQueryNeverSeesTornSlot` (run with `-race` in Task 12): one goroutine commits 3 000 minutes (noSync) while another queries `15m` in a loop; every non-nil point value equals `float64(minuteIndex)` of its own `T` (both series carry the minute index).
- [ ] **Step 2: Run `go test ./pkg/stats/ -run 'Range|Spike|Bucket|Gaps|Alert|Units|Live|Wrap|Empty|Unknown|TimeZone|Query'`; expect build failure.**
- [ ] **Step 3: Implement `query.go`.**
- [ ] **Step 4: Run the same; expect PASS.**
- [ ] **Step 5: Mutation:** bucket Avg computed as mean of maxes (`TestSpikeSurvivesBucket`); missing -> 0 (`TestGapsAreNilNotZero`); drop the stamp recheck in `readWindow` (`TestQueryNeverSeesTornSlot`, may need `-race` or repetition `-count=20`).
- [ ] **Step 6: Commit** `feat(stats): range queries and downsampling`.

---

### Task 6: Forget, Sweep, retention

**Files:**
- Modify: `pkg/stats/store.go` (add `Forget`, `Sweep`)
- Test: `pkg/stats/store_forget_test.go`

**Interfaces:**
- Produces: `func (s *Store) Forget(item string, at time.Time) error` (stores `at.Unix()` in the header; `ErrUnknownItem` if missing; the item stays in `Items()` and queryable); `func (s *Store) Sweep(now time.Time) error`. Sweep deletes (after closing the handle, because of Windows) every item whose `forgottenAt + 7 days <= now`, or whose newest stamp + 7 days <= now (data stopped without `Forget`), every leftover `*.ring.tmp`, and every `*.ring.bad` older than 7 days (mtime), and no other file of the directory; removes the item from the map and its live ring. A `Record` on a forgotten item clears the flag first. `Forget` also enforces `MaxForgotten`: when more than 256 items are forgotten, those forgotten longest ago are removed at once (handle closed first).

- [ ] **Step 1: Write failing tests:**
  - `TestForgetKeepsHistory7Days`: forget at `t`, `Sweep(t+7d-1s)` keeps it and `Query` still works; `Sweep(t+7d)` removes file and `Items()` entry; the call returns nil.
  - `TestForgetSurvivesRestart`: forget, `Close`, `Open`, `Sweep(t+8d)` removes it.
  - `TestRecordRevivesForgotten`: forget, record again, `Sweep(t+8d)` keeps it (newest stamp is recent) and the flag is cleared after reopen.
  - `TestSweepStaleWithoutForget`: item with last data at `t`, never forgotten: `Sweep(t+7d+1min)` removes it; an item with recent data is kept.
  - `TestSweepCleansTmpAndOldBad`: `x.ring.tmp` removed at once; `x.ring.bad` with mtime 8 days old removed, 1 day old kept (set mtime with `os.Chtimes`).
  - `TestSweepReopenable`: after removal the same item name can be recorded again from scratch with an empty history.
  - `TestSweepKeepsForeignFiles` (Review Focus 8): `alerts.json`, `alerts.json.tmp`, `alerts.json.bad` (mtime 8 days old) and `notes.tmp` in the directory survive `Sweep` at any time.
  - `TestLiveCapIgnoresForgotten` (Review Focus 9): 256 live items; `Forget` one: a new item records fine; reviving the forgotten one now returns `ErrTooManyItems`.
  - `TestForgottenAreBounded`: forget 300 items over time (recreating live ones as needed): at most 256 forgotten files remain, the ones forgotten longest ago are gone.
  - `TestForgetUnknown`: `ErrUnknownItem`.
- [ ] **Step 2: Run `-run 'Forget|Sweep'`; expect fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run; expect PASS.**
- [ ] **Step 5: Mutation:** use `<` instead of `<=` at the 7-day edge; delete without closing the handle (fails on the Windows host, which is the point).
- [ ] **Step 6: Commit** `feat(stats): forget and sweep`.

---

### Task 7: Rules and Evaluator core

**Files:**
- Create: `pkg/stats/rules.go`, `pkg/stats/evaluator.go`
- Test: `pkg/stats/rules_test.go`, `pkg/stats/evaluator_test.go` (external, `FakeClock`, capturing `emit`)

**Interfaces:**
- Produces: contract `Rule` (with json tags, E8), `DefaultRules()` = `[{cpu_high true 90 10} {mem_high true 90 5}]`, `Event`, `Evaluator`, `NewEvaluator(store *Store, dir string, emit func(Event)) (*Evaluator, error)`, `Rules(item) []Rule` (always both keys, order cpu_high, mem_high; defaults unless overridden; returns a copy), `Active(item) []Event` (state `firing`), `OnMinute(item string, minute time.Time)` (called by the Store's `SetOnMinute` hook, never by a service), `type RuleError`.
- State file `<dir>/alerts.json`: `{"version":1,"items":{"<item>":{"rules":[...],"active":[{"key","since","value"}],"runs":{"cpu_high":n},"last":<unix minutes>}}}`, written by tmp + fsync + rename (+ `SyncDir`) on every change of rules, active or runs; only items that differ from defaults/idle are stored. Missing file = defaults. Unreadable, invalid or wrong-version file = defaults, original renamed to `alerts.json.bad`, no error returned. `emit` is called after the state is saved, outside the evaluator lock.
- `OnMinute(item, minute)`: for each enabled rule read the average of `alert/<key>` for that minute (internal ring read). A value >= Threshold increments the run, else (below, or no data) the run is 0 and an active alert resolves. When `minute` is more than one minute after the item's `last`, the skipped minutes (no `Record` at all) reset the run to 0 but do not end an active alert: a failed measurement is not evidence that the situation ended (E11). At run == Minutes (>=) with no active alert: emit `Event{Item, Key, State:"firing", Value: value of this minute, Since: start of the first minute of the run}`. Resolved: `Event{State:"resolved", Value: this minute's value (0 if none), Since: the firing Since}`. At most one firing and one resolved per episode.

- [ ] **Step 1: Write failing tests:**
  - `TestDefaultRules`: exactly `cpu_high 90 10 true`, `mem_high 90 5 true`.
  - `TestCpuHighFiresOnTenthMinute`: 9 minutes at 95: no event; the 10th: one `firing`, Key `cpu_high`, Value 95, Since == minute 1's start; minutes 11..15 at 95: still exactly one event total; then one minute at 50: one `resolved` with Since == the firing Since; further low minutes: no event.
  - `TestMemHighFiresOnFifthMinute` (90/5).
  - `TestStreakBroken`: 9 x 95, one 50, 9 x 95: no event.
  - `TestThresholdBoundary`: exactly 90.0 counts, 89.99 does not.
  - `TestGapResetsRun`: 9 x 95, one minute with no `alert/` series, one 95: no event. A firing alert followed by a committed minute without the `alert/` series resolves.
  - `TestSkippedMinuteIsGap`: `OnMinute` called for minute 0..8 then 10 (minute 9 skipped), 95 each: no firing.
  - `TestSkippedMinuteDoesNotResolveFiring` (Review Focus 7): firing, then `OnMinute` for a minute three minutes later with nothing recorded in between: no `resolved`, `Active` still lists it, the run restarted at 1; a later low minute resolves it once.
  - `TestDisabledRuleSilent`.
  - `TestStateSurvivesRestart`: firing, drop the evaluator, `NewEvaluator` on the same dir: `Active(item)` returns the same Since and Value; the next high minute emits nothing; the next low minute emits one resolved. Runs also persist: 7 high minutes, restart, 3 more high minutes -> firing on the 10th total.
  - `TestCorruptStateFile`: garbage, `{"version":2}`, empty file: `NewEvaluator` succeeds with defaults and `alerts.json.bad` holds the original bytes.
  - `TestEmitOutsideLock`: an `emit` that calls `ev.Active(item)` and `ev.Rules(item)` does not deadlock.
  - `TestRulesReturnsCopy`: mutating the returned slice does not change the evaluator.
  - `TestStateFileAtomic`: after each change `alerts.json` parses and no `.tmp` remains.
- [ ] **Step 2: Run `-run 'Rule|CpuHigh|MemHigh|Streak|Threshold|Gap|Skipped|Disabled|State|Corrupt|Emit'`; expect fail.**
- [ ] **Step 3: Implement.** Reading one minute: add unexported `func (s *Store) minuteValue(item, key string, minute int64) (avg float64, ok bool)` (uses `readWindow(minute, minute)` on the one series).
- [ ] **Step 4: Run; expect PASS.**
- [ ] **Step 5: Mutation:** `>` instead of `>=` (`TestThresholdBoundary`); keep the run across a gap (`TestGapResetsRun`); emit before saving (no test: skip); emit twice on every high minute (`TestCpuHighFiresOnTenthMinute`); treat skipped minutes as low values (`TestSkippedMinuteDoesNotResolveFiring`).
- [ ] **Step 6: Commit** `feat(stats): alert rules and evaluator`.

---

### Task 8: Rebuild, SetRules, Resolve, Prune

**Files:**
- Modify: `pkg/stats/evaluator.go`, `pkg/stats/rules.go`
- Test: `pkg/stats/evaluator_rebuild_test.go`

**Interfaces:**
- Produces: `func (e *Evaluator) SetRules(item string, rules []Rule) error`; `func (e *Evaluator) Rebuild(item string)`; E2 `Resolve(item string)`; E3 `Prune()`.
- Validation (all or nothing, state untouched on error): key must be `cpu_high` or `mem_high`, no duplicate key, Threshold finite and 1..100, Minutes 1..120. Error is `*RuleError{Index, Field ("key"|"threshold"|"minutes"), Message}`. Keys not mentioned keep their current rule.
- After a successful `SetRules`: reconcile that item against history using the same routine as `Rebuild`; events follow the rules below.
- `Rebuild(item)`: for each rule, count the trailing consecutive minutes with `alert/<key>` average >= Threshold, walking back from the item's newest committed minute, at most `rule.Minutes` (cap 120); the walk only starts if that newest minute is within 2 minutes of the clock's last complete minute, else the run is 0 (item was down). Then reconcile: persisted active and run still >= Minutes -> keep it silently (Since from the state); persisted active but run < Minutes -> `resolved`; no active and run >= Minutes -> `firing` (Since = first minute of the run); the persisted run counter is replaced by the rebuilt one. A disabled rule: active -> `resolved`, run 0.
- `Resolve(item)`: one `resolved` per active alert, runs cleared. `Prune()`: removes items absent from `store.Items()`.

- [ ] **Step 1: Write failing tests:**
  - `TestSetRulesValidation` (table, each expects `*RuleError` via `errors.As` with the Field, and `Rules(item)` unchanged afterwards): threshold 0, 101, NaN, +Inf -> `threshold`; minutes 0, 121 -> `minutes`; key `"disk_high"` -> `key`; duplicate `cpu_high` -> `key`. Accepted: `{cpu_high,true,1,1}` and `{mem_high,true,100,120}`; a one-element list keeps the other rule's value.
  - `TestRebuildContinuesStreak`: history of 7 high minutes ending at the clock's last complete minute, fresh evaluator with no state, `Rebuild`: no event; 3 more minutes via `OnMinute` -> exactly one firing, Since == minute of the first high minute of the 10.
  - `TestRebuildFiresWhenStateLost`: history of 12 high minutes, no state file: `Rebuild` emits one firing.
  - `TestRebuildKeepsActiveSilently`: state says active, history still high: no event; later low minute -> one resolved.
  - `TestRebuildResolvesAfterDowntime`: state active, newest committed minute is 5 minutes older than the clock's last complete minute: one resolved. With a 2-minute gap (restart in progress): kept, no event.
  - `TestRebuildNoHistoryNoSeries`: item without `alert/` series: nothing happens, no panic.
  - `TestDisableWhileFiring` (Review Focus 5): firing, `SetRules` disabling it -> exactly one resolved; later high minutes emit nothing; `Active` is empty and the state file shows it.
  - `TestRaiseThresholdWhileFiring`: firing at 95, threshold set to 99 with the last minutes at 95 -> one resolved. Lowering to 50 with 12 high minutes of history and no active -> one firing.
  - `TestMinutesChangedWhileFiring`: firing with Minutes 10, `SetRules` Minutes 120 while history only has 15 high minutes -> resolved; back to 10 -> firing again (a new episode, Since = 10 minutes back).
  - `TestResolveStoppedItem`: two active alerts (cpu, mem): `Resolve` emits two resolved, a second `Resolve` emits none.
  - `TestResolveThenOnMinuteEmitsNothing` (Review Focus 6): after `Resolve`, high minutes emit nothing until a fresh run of `Minutes` completes.
  - `TestPrune`: item forgotten and swept from the store: `Prune` drops it from `alerts.json`; items present are kept; the file does not grow with dead items.
- [ ] **Step 2: Run `-run 'SetRules|Rebuild|Disable|Raise|Minutes|Resolve|Prune'`; expect fail.**
- [ ] **Step 3: Implement.** One private `reconcile(item, now)` shared by `Rebuild` and `SetRules`.
- [ ] **Step 4: Run; expect PASS.**
- [ ] **Step 5: Mutation:** skip the 2-minute grace (`TestRebuildResolvesAfterDowntime` second half); `SetRules` applying a valid first rule before rejecting a later one (`TestSetRulesValidation`); `Resolve` not clearing runs.
- [ ] **Step 6: Commit** `feat(stats): rule changes, rebuild from history, resolve and prune`.

---

### Task 9: Crash safety, corruption, disk full

**Files:**
- Create: `pkg/stats/crash_test.go` (internal), `pkg/stats/tmpfs_linux_test.go` (`//go:build linux` file suffix is enough, no extra tag)
- Modify: `pkg/stats/helpers_test.go` (add `faultFS`)

**Interfaces:**
- `faultFS` (test only) wraps `osFS` and counts mutating operations in order: `OpenFile` with `O_CREATE|O_TRUNC`, `WriteAt`, `Sync`, `Rename`, `Remove`. Fields: `killAt int` (the n-th op panics with a sentinel `errKilled` before doing anything), `tornAt int` (the n-th `WriteAt` of more than 4 bytes writes only its first half, then panics), `failFrom int` + `failErr error` (every mutating op from the n-th on returns `failErr` after writing nothing), `ops int` (count), plus the blocking hook used in Task 5. Assumption written in the test file: aligned 4-byte writes are atomic (sector granularity), which is why the stamp is 4 bytes.
- Scenario `crashScenario(t, fs fsys, dir string, progress *int)`: `open`, record minutes 0..2 (series `cpu/user`, `memory/rss`) committing each (progress incremented after each successful commit), then a sample set adding a third series `disk/read` and committing minute 3, then `Forget` at minute 3. Values are `minute*10+seriesIndex+1` so any point can be validated.

- [ ] **Step 1: Write failing tests:**
  - `TestKillAtEveryWriteStep`: for `n := 1; ; n++`: fresh dir, run the scenario with `killAt = n`, recover the panic; if no panic occurred the loop ends. After each kill reopen with `osFS` and assert: `Open` returns nil and `Quarantined()` is empty (a kill never needs quarantine); every point of `Query(7d)` is nil or exactly the value written for its minute and series; minutes whose commit had returned before the kill (`progress`) are fully intact; the in-flight minute is all-or-nothing across series (all its series present, or all nil); the store accepts a new `Record`/commit afterwards and the new minute is readable; `alerts`-less. Assert the loop ran at least 40 iterations (the scenario has more write steps than that) so a miscounting fault fs cannot pass silently, and log the total.
  - `TestTornWriteAtEveryWriteStep`: same loop with `tornAt = n` over writes of more than 4 bytes (headers, NaN column fills, creation image); same invariants. Torn header: the other copy wins; torn creation image: only `.tmp` exists and is removed at `Open`.
  - `TestStampIsWrittenLast`: record the sequence of `WriteAt` offsets of one commit; the last write is inside the stamp column and a `Sync` precedes it.
  - `TestHeaderUpdateOrder`: when adding a series, the column image is written and synced before the header copy is written.
  - `TestCorruptedRegions`: flip a byte in the stamp of minute 1: only minute 1 becomes a gap; truncate the file to `ringFileSize(1)` while the header says 2 series: item quarantined, new ring starts, `.bad` equals the truncated bytes; version bytes changed to 2 with a recomputed CRC: quarantined, `.bad` byte-identical to the original (data kept for a downgrade); flip a byte inside a value of a committed minute: documented limit, the test asserts the value changes but nothing else breaks (no CRC on values).
  - `TestDiskFullDuringCommit`: `failFrom` set at the first write of minute 2's commit with `syscall.ENOSPC`: `Record` returns an error `errors.Is(err, syscall.ENOSPC)`; `SetOnMinute` is not called for minute 2; minute 2 absent, minutes 0..1 intact; clear the fault: the next minute commits and queries correctly; live ring unaffected during the failure; the same for failure in `addSeries` (series not registered, retried at the next minute) and in `createRing` (no `.ring` file, no `.tmp`, item retried on the next `Record`).
  - `TestEvaluatorStateKillAtEveryStep`: loop kill on every mutating op of an `alerts.json` save (needs the evaluator to take an `fsys`: unexported `newEvaluator(store, dir, emit, fs fsys)`; `NewEvaluator` calls it with `osFS`): after each kill a new evaluator loads either the old or the new state, never garbage, and `alerts.json.bad` is not created.
  - `TestDiskFullTmpfs` (`tmpfs_linux_test.go`): skip unless `os.Geteuid()==0` and `mount -t tmpfs -o size=1m tmpfs <dir>` succeeds; fill the volume, record until `Record` errors with ENOSPC, assert the process did not crash, earlier minutes still read, free space, the store recovers; unmount in cleanup.
- [ ] **Step 2: Run `go test ./pkg/stats/ -run 'Kill|Torn|Stamp|HeaderUpdate|Corrupted|DiskFull'`; expect build failure or fail.**
- [ ] **Step 3: Implement** only what the tests force: the `fsys` plumbing already exists; add `newEvaluator`, ensure `Record` returns wrapped (`%w`) errors, ensure a failed commit leaves in-memory state consistent. Fix real defects the loops reveal in `ring.go`/`store.go` rather than weakening the tests.
- [ ] **Step 4: Run the same; expect PASS (the kill loops take a few seconds).**
- [ ] **Step 5: Mutation:** write the stamp before the values (`TestStampIsWrittenLast` and `TestKillAtEveryWriteStep` both fail); skip the `Sync` before the stamp (`TestStampIsWrittenLast`); write both header copies in place instead of alternating (`TestTornWriteAtEveryWriteStep`).
- [ ] **Step 6: On Linux (WSL as root) run `go test ./pkg/stats/ -run DiskFullTmpfs -v` once; expect PASS, not SKIP.**
- [ ] **Step 7: Commit** `test(stats): crash, corruption and disk-full safety`.

---

### Task 10: Size bounds

**Files:**
- Create: `pkg/stats/bounds_test.go`
- Modify: `pkg/stats/types.go` (`MaxFileSize = ringFileSize(MaxSeries)` as an exported constant expression), `pkg/stats/README.md` (start it)

- [ ] **Step 1: Write failing tests** (noSync fs unless stated; real 10 080 slots):
  - `TestFileSizeIsFunctionOfSeriesCount`: item with 3 series: size == `ringFileSize(3)` (= 8192 + 40320*10 = 411 392 bytes) after 1 minute and after 3 x 10 080 minutes of writes (wrapping twice); never changes in between (sample the size every 1 000 minutes).
  - `TestMaxFileSize`: `MaxFileSize == 5 854 592` (8192 + 40320*145); a typical 24-series item is 2 951 552 bytes (assert `ringFileSize(24)`).
  - `TestSeriesCap`: 60 distinct keys in one `Record`: the first 48 (sorted order irrelevant: assert exactly 48 stored) recorded, the call returns an error satisfying `errors.Is(err, ErrSeriesCap)`; file size == `MaxFileSize`; repeating for 100 minutes does not grow it; the other 48 series still read correctly. A 64-byte key -> `errors.Is(err, ErrBadKey)`, others recorded.
  - `TestItemCapDirectory`: 256 items x 1 series: `len(ReadDir) == 256` rings, total bytes <= `256*MaxFileSize`; item 257 -> `ErrTooManyItems`; forgetting all 256, creating 256 others and forgetting those too never leaves more than 256 forgotten files (directory <= 512 files).
  - `TestMemoryBounded`: 20 000 `Record` calls (3 items, 24 series, fake time) then `runtime.GC` + `ReadMemStats`: heap growth between calls 10 000 and 20 000 < 2 MiB; each live ring holds at most 180 slots (internal assertion); `runtime.NumGoroutine()` unchanged by `Open`/`Record`/`Close`.
  - `TestBadFilesBounded`: quarantining the same item's file three times leaves one `.bad`.
  - `TestAlertsFileBounded`: 200 `SetRules` on 50 items then `Prune` after sweeping 40 of them: `alerts.json` holds 10 items and is below 64 KiB.
- [ ] **Step 2: Run `-run 'FileSize|MaxFileSize|SeriesCap|ItemCapDirectory|MemoryBounded|BadFilesBounded|AlertsFileBounded'`; expect fail where behavior or constants are missing.**
- [ ] **Step 3: Implement the gaps found** (constant, any unbounded structure the tests expose). Write `pkg/stats/README.md` section "Format and limits" with the layout summary and these numbers (typical item ~2.8 MiB, maximum 5.6 MiB, worst directory (256 live + 256 forgotten) x 5.6 MiB = 2.8 GiB, realistic 41 items ~ 115 MiB: spec decision 10).
- [ ] **Step 4: Run; expect PASS.**
- [ ] **Step 5: Mutation:** allow growth past `MaxSeries` (`TestSeriesCap`); skip the item cap (`TestItemCapDirectory`).
- [ ] **Step 6: Commit** `test(stats): size and memory bounds`.

---

### Task 11: Cost of Record for 50 items at 5 s

**Files:**
- Create: `pkg/stats/bench_test.go`
- Modify: `pkg/stats/README.md` (section "Measured cost")

**Interfaces:** one benchmark op = one 5 s tick = 50 `Record` calls, one per item, each with a 24-key Sample (`cpu/user,system,iowait,throttled`, `memory/rss,cache,swap,limit`, `network/rx:eth0,tx:eth0,dropped:eth0,rx:eth1,tx:eth1,dropped:eth1`, `disk/read,write,iops_read,iops_write`, `pids/count,pids/fds`, `restarts/count`, `health/ok`, `alert/cpu_high`, `alert/mem_high`), time advanced by 5 s from a `FakeClock`, items pre-created before `b.ResetTimer`, so one tick in twelve commits a minute for all 50 items (the heavy tick).

- [ ] **Step 1: Write the benchmarks and test:**
  - `BenchmarkRecordTick/nofsync` and `/fsync` (sub-benchmarks; `nofsync` uses the Task 9-style wrapper with a no-op `Sync`; `fsync` uses `osFS`). Each reports `ns/op`, `B/op`, `allocs/op` (`b.ReportAllocs`) and custom metrics `worst-tick-ns` (maximum single tick) and `cpu-percent-of-core` (= mean tick ns / 5e9 * 100) via `b.ReportMetric`.
  - `TestRecordTickBudget` (skips under `-short` and under `-race`): 1 simulated hour = 720 ticks, no-fsync: mean tick < 5 ms (20 % of the 25 ms that 0.5 % of a core allows per 5 s: Common is only a part of a service's own 0.5 % budget, services are not summed) and worst tick < 250 ms; fsync variant: worst tick < 2 s, no assertion on the mean. Failure messages print the measured numbers.
- [ ] **Step 2: Run `go test ./pkg/stats/ -run xxx -bench RecordTick -benchmem -count 3`; record the numbers.**
- [ ] **Step 3: If the no-fsync mean exceeds the budget**, profile (`-cpuprofile`) and fix the cause in `store.go` (typical: per-call map allocation, per-series syscall count in the commit path); if the fsync worst tick exceeds 2 s on the owner's NAS class disks, the documented upgrade is committing minutes from a per-store goroutine instead of the `Record` caller; do not do it unless measured.
- [ ] **Step 4: Run `go test ./pkg/stats/ -run RecordTickBudget -v`; expect PASS.** Run the benchmark once more in WSL (ext4 path, not `/mnt/d`) and keep both result sets.
- [ ] **Step 5: Write the numbers** (host OS, disk type, mean/worst tick, allocs, % of a core, both fsync variants, Windows and Linux) into `README.md` under "Measured cost", dated.
- [ ] **Step 6: Commit** `test(stats): measure the cost of Record for 50 items`.

---

### Task 12: Full suite, README, tag

**Files:**
- Modify: `pkg/stats/README.md` (usage: wiring example for a service in about 15 lines: `Open`, `SetOnMinute(ev.OnMinute)`, `Rebuild` per item at start, `Record` every 5 s, `Resolve` when an item stops being measured then `Forget` on removal, `Sweep` hourly, `Prune` after; services never call `OnMinute`; the Extensions list)

- [ ] **Step 1: Run the whole package once:** `go test ./pkg/stats/... > $SCRATCH/stats.log 2>&1` then read the log; expect `ok`. Then `go vet ./...`, `gofmt -l .` (expect empty), `go build ./...`, `go test ./... 2>&1 | tail` (compare with the baseline from Task 1; no new failure).
- [ ] **Step 2: Race run (Linux):** `go test -race -count=3 ./pkg/stats/...` from WSL (or the host if CGO works). Expect PASS, in particular `TestQueryNeverSeesTornSlot`, `TestQueryDoesNotWaitForWriter`.
- [ ] **Step 3: Self-check** that the diff only touches `pkg/stats/` and that `go.mod`/`go.sum` are unchanged (`git diff --stat v0.4.27`).
- [ ] **Step 4: Commit** `docs(stats): usage and extensions`.
- [ ] **Step 5: Tag and release.** Do not merge, push or tag on your own: the owner's standing orders cover merging and tagging. When they are given: tags in this repo are annotated with a one-line subject (see `git for-each-ref refs/tags --sort=-v:refname --count=3 --format='%(refname) %(subject)'`); create `v0.4.28` (or the next free tag, in which case every plan's `v0.4.28` is changed to it: check `git tag --sort=-v:refname | head -3` and `git ls-remote --tags inkly`) with the subject `shared stats store: 7-day ring files, live ring, range queries, alert evaluator`, on the head of `feat/v0.4.28`, and push the branch and the tag to remote `inkly`. The GitHub latest release is v0.4.21 although tags reach v0.4.27, so a GitHub release is created (`gh release create v0.4.28 --notes ...`) only if the standing orders ask for it. Consumers then run `go get github.com/ReCasaOS/CasaOS-Common@v0.4.28` once the tag is on the remote.

---

## Self-review (done)

- Spec/contract coverage: Store API (Tasks 4, 5, 6), ring file with avg/min/max for every series, 10 080 minutes, versioned header, atomic, bounded, lock-free read (Tasks 2, 5, 9, 10), live ring 15 min at 5 s (3), periods and downsampling (5), Unit table (1), Rules/Evaluator with state file, consecutive minutes, Rebuild, SetRules validation, Resolve, single OnMinute wiring through `SetOnMinute` (4, 7, 8), test clock (1), crash safety (9), size bounds (10), Record cost (11), tag (12).
- Names are the same in every task (`readWindow`, `writeMinute`, `minuteCell`, `ringFileSize`, `open`, `newEvaluator`, `faultFS`, `noSync`).
- Proportion: the plan fixes files, signatures, values and assertions; bodies are left to the implementer.

## Deviations (contract/spec points that do not hold against the real code or its own numbers)

1. Size: the first draft said "0,5 a 1 Mo par element", which does not hold for avg + min + max on every series. A container with about 24 series (2 interfaces) is 2.8 MiB, the cap (48 series) is 5.6 MiB, about 115 MiB for 41 items. The spec and the contract now say about 3 Mo per element (spec decision 10); the plan keeps decision 2.
2. The first draft of the contract had no way for `Store` to tell the service a minute was committed, yet `Evaluator.OnMinute(item, minute)` must run per minute: hence E1 (`SetOnMinute`, now in the contract as the single wiring). A stopped or forgotten item never commits another minute, so an active alert would stay "firing" forever: hence E2 (`Resolve`, in the contract) and E3.
3. Release flow: v0.4.27 is `inkly/main` itself and the new branch starts there; the latest GitHub release of the repo is v0.4.21, so "tag + GitHub release" is in practice "annotated tag on the branch head"; Task 12 says so.
4. `README.md` of the repo says "Go 1.21"; `go.mod` says 1.26.8 and CI uses 1.26. The plan follows `go.mod`.
