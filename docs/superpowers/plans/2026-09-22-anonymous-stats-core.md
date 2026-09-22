# Anonymous Usage Statistics (Core) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The CasaOS core sends ReCasaOS's anonymous usage statistics (a daily `heartbeat` and a `version_changed` per install or upgrade) to PostHog EU, opt-out, and serves `GET`/`PUT /v1/sys/telemetry` so the dashboard can show exactly what is sent and switch it off.

**Architecture:** A new package `pkg/telemetry` holds everything: the state file (`telemetry.json`, the installer's `telemetry-off` marker), one `Properties()` function read from an injectable filesystem root (plus an injectable command runner and the Docker socket under that root), the PostHog sender, and the hourly check loop. Two echo handlers in `route/v1/telemetry.go`, registered in the existing v1 `sys` group behind the JWT, answer from `telemetry.Default`; `main.go` folds the off marker at startup and starts the loop.

**Tech Stack:** Go 1.26.8, echo v4, CasaOS-Common v0.4.27 (logger), `github.com/google/uuid` v1.5.0 (already a direct dependency), stdlib `net/http`, `net/http/httptest`, `math/rand/v2`, `encoding/json` (`omitzero`).

**Spec:** D:/clients/casaos/CasaOS/docs/superpowers/specs/2026-09-22-anonymous-stats-design.md

## Global Constraints

- Repository `D:/clients/casaos/CasaOS`, branch `feat/telemetry` (already checked out), module `github.com/ReCasaOS/CasaOS`, `go 1.26.8`, CasaOS-Common `v0.4.27`. No new dependency: the UUID comes from `github.com/google/uuid` (already in `go.mod`).
- State file `/var/lib/casaos/telemetry.json`, root, 0600, JSON `{"enabled": bool, "id": string, "last_sent": RFC 3339 string or absent, "notice_seen": bool}`.
- A missing `telemetry.json` means: enabled, no id yet (generated on first use), never sent, notice not seen. A malformed file means **disabled** ("when in doubt, do not send"); the next `PUT` rewrites it.
- `/var/lib/casaos/telemetry-off` (any content, written by the installer): at startup, before anything else of this feature runs, the core sets `"enabled": false` in `telemetry.json` (creating it if needed, keeping `id`) and deletes the marker.
- `/var/lib/casaos/upgraded-from` (written by the installer: previous tag, `upstream` or `new`): statistics off → deleted unsent; on → sent as `previous_distribution` in `version_changed`, deleted only after a successful send, kept on failure.
- `/var/lib/casaos/fork-release` (`common.FORK_RELEASE_FILE`): `distribution`, trimmed; `unknown` if absent.
- Identifier: a random UUID v4, PostHog's `distinct_id`, derived from nothing on the machine.
- Events: `heartbeat` (at most once every 24 hours) and `version_changed` (once per install or upgrade).
- Properties, both events: `distribution`, `core` (`"v" + common.VERSION`), `arch` (`runtime.GOARCH`, plus `GOARM` from the build info for `arm`, e.g. `arm-7`), `os` (`/etc/os-release` `ID` and `VERSION_ID`, ID alone without VERSION_ID, `unknown` if unreadable), `kernel` (uname release, major.minor), `virtualization` (`systemd-detect-virt` output, `unknown` if it cannot run), `model` (`/proc/device-tree/model`, else `/sys/class/dmi/id/product_name`; NUL and spaces trimmed, 64 characters max; `unknown` if empty or a placeholder `To Be Filled By O.E.M.`, `System Product Name`, `Default string`, `Not Specified`), `docker` (`GET /version` on `/var/run/docker.sock`, field `Version`, `unknown` if unavailable), `cpu_cores` (`runtime.NumCPU()`), `ram_gb` (`MemTotal` rounded to the nearest of 1, 2, 4, 8, 16, 32, 64, then `128+`), `disks` (entries of `/sys/block` whose resolved path is not under `/sys/devices/virtual/`, excluding `sr*` and `mmcblk*boot*`), `storage_tb` (sum of those disks' `size` × 512 bytes, buckets `<0.5`, `0.5-1`, `1-2`, `2-4`, `4-8`, `8-16`, `16-32`, `32+`, TB = 10^12 bytes), `raid` (`/proc/mdstat` lists an active `md` array); `version_changed` adds `previous_distribution`.
- PostHog properties on every event: `"$process_person_profile": false`, `"$lib": "recasaos-core"`. `$geoip_disable` is never sent.
- Never sent: IP address, hostname, MAC address, serial numbers, disk names, labels or paths, installed apps, user accounts, anything about the local network.
- Request: `POST https://eu.i.posthog.com/i/v0/e/`, `Content-Type: application/json`, body `{"api_key","event","distinct_id","timestamp","properties"}`, `timestamp` RFC 3339 UTC, key `phc_C5T8QbrhBttzgkWuKm2Xr9S4TLGh3sCVAZai2oaFEuhD`.
- Timeout 10 seconds. The standard proxy environment is honoured. A non-2xx answer or a transport error is a failure.
- Start: after a random delay of 0 to 60 minutes, the first check, then one check every hour.
- Check order: (1) off → delete `upgraded-from`, stop, no payload built, no connection; (2) `upgraded-from` exists → `version_changed`; (3) `last_sent` absent or at least 24 hours old → `heartbeat`, `last_sent` updated only after a successful send.
- Failures are logged at Info (event name and error), never retried faster than the next hourly check, never queued.
- One function builds the properties, used by the sender and by the preview API: what the owner sees is what is sent.
- Test-only environment variables: `CASAOS_TELEMETRY_ENDPOINT` (capture URL), `CASAOS_TELEMETRY_START_DELAY` (Go duration, e.g. `0s`). No CI step may run the core with statistics on and the default endpoint: every test sends to an `httptest` server or sends nothing.
- API in the existing v1 `sys` route group, JWT like every core route, `model.Result` envelope `{success, message, data}`: `GET /v1/sys/telemetry` → `data = {"enabled": bool, "notice_seen": bool, "preview": {"event": "heartbeat", "properties": {...}}}`, preview built even when statistics are off; `PUT /v1/sys/telemetry` with `{"enabled"?: bool, "notice_seen"?: bool}` (both optional, unknown fields ignored) → `data` = the same object as GET after the change; it changes nothing else.
- README section: `https://github.com/ReCasaOS/CasaOS-Install#anonymous-statistics` (linked from the package doc comment).
- `ram_gb` is a JSON string (`"8"`, `"128+"`, `"unknown"`), because of `"128+"`: the spec table's example `8` is sent, previewed and documented as `"8"`.
- Tests. CI runs them all on Linux: `.github/workflows/codecov.yml` runs `go test -race -failfast ./...` on ubuntu-22.04 for pushes and pull requests to `main`. Locally (Windows, Git Bash, no Docker, no WSL distro): `pkg/telemetry` builds and runs natively, so every red/green step of Tasks 1-5 runs `cd D:/clients/casaos/CasaOS && go test ./pkg/telemetry/` (no `-race`, which needs cgo) for real results. Exactly four of its tests are Linux-only and fail natively on Windows: `TestTheStateIsSavedAsA0600JSONFile` and `TestTheFirstCheckMakesAndKeepsTheID` (Windows reports the mode `-rw-rw-rw-`), `TestPropertiesOfABox` and `TestDisksAreThePhysicalBlockDevices` (the `:` of `pci0000:00` in the sysfs fixture paths, `The directory name is invalid`); any other failure is real. `route/` and `route/v1` build only on Linux (the `service` package uses `unix.Mount`), so Task 6's local gate is `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet <packages>`, which type-checks the test files. Every task also runs `gofmt -l`.
- Commit with `git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "..."`. Never a `Co-Authored-By` trailer or any AI attribution.
- Never run `git push`, `git merge`, `git pull`, `git tag`, `git checkout --`, `git reset --hard`, `git clean` or `rm -rf` in the repository. Ship order: this core is released first under its own tag; the installer pins it in the distribution release (other plans).

---

## File structure

| File | Action | Responsibility |
|---|---|---|
| `pkg/telemetry/telemetry.go` | Create | Package doc (README link, test-only variables), constants (key, endpoint, file paths, env names), the `Telemetry` type, `New`, `Default`, `path`. |
| `pkg/telemetry/state.go` | Create | `State` (telemetry.json), `load` (missing = enabled, malformed = disabled), `save` (synced temporary file renamed over it, 0600), `modify` (locked read-modify-write), `ApplyOffMarker`. |
| `pkg/telemetry/properties.go` | Create | `Properties()` and one helper per property, all read under `Root`. |
| `pkg/telemetry/send.go` | Create | `send`: one POST to PostHog's capture endpoint. |
| `pkg/telemetry/check.go` | Create | `Run` (start delay, hourly ticker), `startDelay`, `Check` (the three steps and their file effects). |
| `pkg/telemetry/status.go` | Create | `Status`, `Preview`, `Status()`, `Update()`: what the API answers and changes. |
| `pkg/telemetry/state_test.go` | Create | State tests; helpers `write`, `stateOnDisk`, `exists`. |
| `pkg/telemetry/properties_test.go` | Create | Property tests from fixtures; helpers `virt`, `disk`, mdstat fixtures. |
| `pkg/telemetry/send_test.go` | Create | Request shape and failure tests; helpers `capture`, `newCapture`, `fixture`, `asJSON`, `testNow`. |
| `pkg/telemetry/check_test.go` | Create | Cycle tests against `httptest`, start delay, loop; `TestMain` (logger), helper `saveState`. |
| `pkg/telemetry/status_test.go` | Create | Status, preview-equals-request, Update tests. |
| `route/v1/telemetry.go` | Create | `GetTelemetry`, `PutTelemetry` echo handlers. |
| `route/v1/telemetry_test.go` | Create | Handler tests against a fixture `telemetry.Default`. |
| `route/v1.go` | Modify (after line 140) | Register `GET` and `PUT /telemetry` in `v1SysGroup`. |
| `route/v1_test.go` | Modify (imports lines 3-12, append after line 81) | The routes exist and sit behind the token. |
| `main.go` | Modify (import after line 25, after lines 104-106) | Fold the off marker at startup, then start the loop. |

---

### Task 1: The state file

**Files:**
- Create: `pkg/telemetry/telemetry.go`
- Create: `pkg/telemetry/state.go`
- Test: `pkg/telemetry/state_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `const StateFile = "/var/lib/casaos/telemetry.json"`; unexported `apiKey`, `defaultEndpoint`, `endpointEnv = "CASAOS_TELEMETRY_ENDPOINT"`, `startDelayEnv = "CASAOS_TELEMETRY_START_DELAY"`, `offMarkerFile = "/var/lib/casaos/telemetry-off"`, `upgradedFromFile = "/var/lib/casaos/upgraded-from"`.
  - `type Telemetry struct { Root string; Endpoint string; Now func() time.Time; Command func(name string, args ...string) ([]byte, error); client *http.Client; mu sync.Mutex }`
  - `func New(root string) *Telemetry`, `var Default = New("/")`, `func (t *Telemetry) path(name string) string`
  - `type State struct { Enabled bool "json:enabled"; ID string "json:id"; LastSent time.Time "json:last_sent,omitzero"; NoticeSeen bool "json:notice_seen" }`
  - `func (t *Telemetry) load() State`, `func (t *Telemetry) save(s State) error`, `func (t *Telemetry) modify(change func(*State)) (State, error)`, `func (t *Telemetry) ApplyOffMarker() error`
  - Test helpers: `write(t, root, name, content string)`, `stateOnDisk(t, root string) map[string]any`, `exists(t, root, name string) bool`.

- [ ] **Step 1: Write the failing test**

Create `pkg/telemetry/state_test.go`:

```go
package telemetry

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// write creates name, an absolute path on a box, under root.
func write(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stateOnDisk is telemetry.json as JSON values, the way the other components read it.
func stateOnDisk(t *testing.T, root string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, StateFile))
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("telemetry.json is not JSON: %v\n%s", err, data)
	}
	return state
}

// exists reports whether name, an absolute path on a box, exists under root.
func exists(t *testing.T, root, name string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(root, name))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

func TestNoStateFileMeansEnabledNeverSentNoticeNotSeen(t *testing.T) {
	s := New(t.TempDir()).load()
	if !s.Enabled || s.ID != "" || !s.LastSent.IsZero() || s.NoticeSeen {
		t.Fatalf("load() with no file = %+v, want enabled, no id, never sent, notice not seen", s)
	}
}

func TestAMalformedStateFileMeansDisabled(t *testing.T) {
	for _, content := range []string{
		"",
		"{",
		"enabled",
		`{"enabled": "yes"}`,
		`{"enabled": true, "last_sent": "yesterday"}`,
	} {
		root := t.TempDir()
		write(t, root, StateFile, content)
		if s := New(root).load(); s.Enabled {
			t.Errorf("load() of %q = %+v, want disabled", content, s)
		}
	}
}

func TestTheStateIsSavedAsA0600JSONFile(t *testing.T) {
	root := t.TempDir()
	// A file made by hand, readable by all: saving makes it 0600 again.
	write(t, root, StateFile, `{"enabled":true}`)
	tel := New(root)
	want := State{
		Enabled:    true,
		ID:         "0b0f4d8e-6d7a-4f55-9a53-5b8c1f3e2a10",
		LastSent:   time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC),
		NoticeSeen: true,
	}
	if err := tel.save(want); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(root, StateFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("telemetry.json mode = %v, want 0600", info.Mode().Perm())
	}
	onDisk := stateOnDisk(t, root)
	if onDisk["enabled"] != true || onDisk["id"] != want.ID || onDisk["last_sent"] != "2026-09-22T10:00:00Z" || onDisk["notice_seen"] != true {
		t.Fatalf("telemetry.json = %v", onDisk)
	}
	if got := tel.load(); !got.Enabled || got.ID != want.ID || !got.LastSent.Equal(want.LastSent) || !got.NoticeSeen {
		t.Fatalf("load() after save = %+v, want %+v", got, want)
	}
	// The rename leaves nothing behind.
	entries, err := os.ReadDir(filepath.Dir(filepath.Join(root, StateFile)))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d files next to telemetry.json, want it alone", len(entries))
	}
}

func TestANeverSentStateHasNoLastSentAndAnEmptyID(t *testing.T) {
	root := t.TempDir()
	if err := New(root).save(State{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	onDisk := stateOnDisk(t, root)
	if _, ok := onDisk["last_sent"]; ok {
		t.Fatalf("telemetry.json = %v, want no last_sent", onDisk)
	}
	if id, ok := onDisk["id"]; !ok || id != "" {
		t.Fatalf("telemetry.json = %v, want an empty id until the first send", onDisk)
	}
}

func TestTheOffMarkerTurnsStatisticsOffAtStartup(t *testing.T) {
	cases := []struct {
		name, state, wantID string
	}{
		{"a box that had a state keeps its id", `{"enabled":true,"id":"kept-id","last_sent":"2026-09-21T10:00:00Z","notice_seen":true}`, "kept-id"},
		{"a box with no state yet", "", ""},
		{"a box with a malformed state", "{", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.state != "" {
				write(t, root, StateFile, tc.state)
			}
			write(t, root, offMarkerFile, "")

			if err := New(root).ApplyOffMarker(); err != nil {
				t.Fatal(err)
			}

			onDisk := stateOnDisk(t, root)
			if onDisk["enabled"] != false || onDisk["id"] != tc.wantID {
				t.Fatalf("telemetry.json = %v, want enabled false and id %q", onDisk, tc.wantID)
			}
			if exists(t, root, offMarkerFile) {
				t.Fatal("the telemetry-off marker is still there")
			}
		})
	}
}

func TestNoOffMarkerLeavesTheStateAlone(t *testing.T) {
	root := t.TempDir()
	if err := New(root).ApplyOffMarker(); err != nil {
		t.Fatal(err)
	}
	if exists(t, root, StateFile) {
		t.Fatal("telemetry.json was written with no marker")
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Local (Windows, Git Bash), natively, then type-checked for Linux:

```bash
cd D:/clients/casaos/CasaOS && go test ./pkg/telemetry/ ; GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./pkg/telemetry/
```

Expected: both FAIL to compile the tests, `undefined: New`, `undefined: StateFile`, `undefined: State`, `undefined: offMarkerFile`; `go test` ends with `FAIL	github.com/ReCasaOS/CasaOS/pkg/telemetry [build failed]`.

Linux (CI, or any Linux shell with Go 1.26): `go test -race ./pkg/telemetry/ -run 'State|OffMarker' -v` fails with the same compile errors.

- [ ] **Step 3: Write the minimal implementation**

Create `pkg/telemetry/telemetry.go`:

```go
// Package telemetry sends ReCasaOS's anonymous usage statistics to PostHog's EU
// cloud: a heartbeat at most once a day, which counts the boxes, and a
// version_changed event once per install or upgrade, which measures how fast
// they update. What is sent, what never is, and the three ways to turn it off:
// https://github.com/ReCasaOS/CasaOS-Install#anonymous-statistics
//
// Two environment variables are for the install check only, never for a box:
// CASAOS_TELEMETRY_ENDPOINT replaces the capture URL, and
// CASAOS_TELEMETRY_START_DELAY (a Go duration, e.g. 0s) the random start delay.
package telemetry

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

const (
	// apiKey is the PostHog project's key: public and write-only, made to be
	// embedded in clients.
	apiKey          = "phc_C5T8QbrhBttzgkWuKm2Xr9S4TLGh3sCVAZai2oaFEuhD"
	defaultEndpoint = "https://eu.i.posthog.com/i/v0/e/"

	endpointEnv   = "CASAOS_TELEMETRY_ENDPOINT"
	startDelayEnv = "CASAOS_TELEMETRY_START_DELAY"

	// StateFile holds the choice, the id and the last heartbeat (root, 0600).
	StateFile = "/var/lib/casaos/telemetry.json"
	// offMarkerFile is left by the installer's --no-telemetry.
	offMarkerFile = "/var/lib/casaos/telemetry-off"
	// upgradedFromFile is written by the installer on every run: the previous
	// release's tag, "upstream" or "new".
	upgradedFromFile = "/var/lib/casaos/upgraded-from"
)

// Telemetry is the statistics of one box.
type Telemetry struct {
	// Root is the filesystem everything is read from and written to: "/" on a
	// box, a fixture directory in tests.
	Root string
	// Endpoint is PostHog's capture URL.
	Endpoint string
	Now      func() time.Time
	// Command runs a program and returns its standard output: systemd-detect-virt.
	Command func(name string, args ...string) ([]byte, error)

	client *http.Client
	mu     sync.Mutex // serialises the read-modify-writes of telemetry.json
}

// Default is the box's own: the API answers from it and main runs its checks.
var Default = New("/")

// New reads the box under root and sends to PostHog, or to
// CASAOS_TELEMETRY_ENDPOINT when the install check sets it.
func New(root string) *Telemetry {
	endpoint := os.Getenv(endpointEnv)
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	return &Telemetry{
		Root:     root,
		Endpoint: endpoint,
		Now:      time.Now,
		Command: func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).Output()
		},
		// No Transport: http.DefaultTransport, whose proxy comes from the
		// environment (HTTPS_PROXY, NO_PROXY).
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// path is name, an absolute path on a box, under Root.
func (t *Telemetry) path(name string) string {
	return filepath.Join(t.Root, name)
}
```

Create `pkg/telemetry/state.go`:

```go
package telemetry

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// State is telemetry.json.
type State struct {
	Enabled    bool      `json:"enabled"`
	ID         string    `json:"id"`
	LastSent   time.Time `json:"last_sent,omitzero"`
	NoticeSeen bool      `json:"notice_seen"`
}

// load reads telemetry.json. No file is the default: enabled, no id yet, never
// sent, notice not seen. A file that cannot be read or parsed is disabled: when
// in doubt, send nothing. The next PUT rewrites it.
func (t *Telemetry) load() State {
	data, err := os.ReadFile(t.path(StateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return State{Enabled: true}
	}
	var s State
	if err != nil || json.Unmarshal(data, &s) != nil {
		return State{}
	}
	return s
}

// save writes telemetry.json to a temporary file, syncs it to disk and renames
// it over the old one: after a power cut (common on single-board boxes) the file
// is the old state or the new one, never an empty one that load would read as
// disabled. os.CreateTemp creates it 0600, whatever the old file's mode.
func (t *Telemetry) save(s State) error {
	path := t.path(StateFile)
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".telemetry-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // nothing left to remove once renamed
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// modify loads, changes and saves telemetry.json in one step, between the API
// and the check.
func (t *Telemetry) modify(change func(*State)) (State, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.load()
	change(&s)
	return s, t.save(s)
}

// ApplyOffMarker folds the installer's telemetry-off marker into telemetry.json:
// statistics off, the id kept, the marker deleted. main runs it at startup,
// before the API serves and before the first check.
func (t *Telemetry) ApplyOffMarker() error {
	marker := t.path(offMarkerFile)
	if _, err := os.Stat(marker); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if _, err := t.modify(func(s *State) { s.Enabled = false }); err != nil {
		return err
	}
	return os.Remove(marker)
}
```

- [ ] **Step 4: Run it and see it pass**

```bash
cd D:/clients/casaos/CasaOS && gofmt -l pkg/telemetry && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./pkg/telemetry/ && go test ./pkg/telemetry/
```

Expected: `gofmt` and `vet` print nothing. The native `go test` fails on exactly one test, Linux-only: `--- FAIL: TestTheStateIsSavedAsA0600JSONFile` with `telemetry.json mode = -rw-rw-rw-, want 0600`; every other test passes. (The `fsync` added to `save` is not observable in a test; the rename test checks that nothing is left next to the file.) Linux/CI: `go test -race ./pkg/telemetry/ -run 'State|OffMarker' -v` prints `ok  	github.com/ReCasaOS/CasaOS/pkg/telemetry`.

- [ ] **Step 5: Commit**

```bash
cd D:/clients/casaos/CasaOS && git add pkg/telemetry/telemetry.go pkg/telemetry/state.go pkg/telemetry/state_test.go && git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(telemetry): keep the statistics' choice in telemetry.json" -m "A missing file is enabled, a malformed one is disabled, and the installer's telemetry-off marker is folded in at startup, keeping the id."
```

---

### Task 2: The properties of a box

**Files:**
- Create: `pkg/telemetry/properties.go`
- Test: `pkg/telemetry/properties_test.go`

**Interfaces:**
- Consumes: `Telemetry`, `New`, `path`, `write` (Task 1); `common.FORK_RELEASE_FILE`, `common.VERSION`.
- Produces:
  - `func (t *Telemetry) Properties() map[string]any` with exactly these keys and Go types: `distribution` string, `core` string, `arch` string, `os` string, `kernel` string, `virtualization` string, `model` string, `docker` string, `cpu_cores` int, `ram_gb` string (`"1"`, `"2"`, `"4"`, `"8"`, `"16"`, `"32"`, `"64"`, `"128+"` or `"unknown"`), `disks` int, `storage_tb` string, `raid` bool, `$process_person_profile` false, `$lib` `"recasaos-core"`.
  - `const unknown = "unknown"`; `func (t *Telemetry) fileValue(name string) string` (reused by Task 4 for `previous_distribution`).
  - Unexported helpers: `buildSettings`, `arch`, `osRelease`, `kernel`, `virtualization`, `model`, `docker`, `ramGB`, `ramBucket`, `disks`, `storageTB`, `raid`.
  - Test helpers: `virt(out string, err error) func(string, ...string) ([]byte, error)`, `disk(t, root, name, device string, sectors uint64)`, constants `mdstatActive`, `mdstatInactive`, `mdstatNone`.

- [ ] **Step 1: Write the failing test**

Create `pkg/telemetry/properties_test.go`:

```go
package telemetry

import (
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/common"
)

// virt stands for systemd-detect-virt.
func virt(out string, err error) func(string, ...string) ([]byte, error) {
	return func(name string, _ ...string) ([]byte, error) {
		if name != "systemd-detect-virt" {
			return nil, errors.New("unexpected command " + name)
		}
		return []byte(out), err
	}
}

// disk lays a block device out as sysfs does: the device under /sys/devices
// with its size in 512-byte sectors, and /sys/block/<name> a relative link to it.
func disk(t *testing.T, root, name, device string, sectors uint64) {
	t.Helper()
	write(t, root, filepath.Join("/sys", device, "size"), strconv.FormatUint(sectors, 10)+"\n")
	link := filepath.Join(root, "/sys/block", name)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", device), link); err != nil {
		t.Fatal(err)
	}
}

const (
	mdstatActive   = "Personalities : [raid1]\nmd0 : active raid1 sdb1[1] sda1[0]\n      976630464 blocks super 1.2 [2/2] [UU]\n\nunused devices: <none>\n"
	mdstatInactive = "Personalities : \nmd127 : inactive sdb[0](S)\n      976631512 blocks super 1.2\n\nunused devices: <none>\n"
	mdstatNone     = "Personalities : \nunused devices: <none>\n"
)

func TestPropertiesOfABox(t *testing.T) {
	root := t.TempDir()
	write(t, root, common.FORK_RELEASE_FILE, "v0.5.0\n")
	write(t, root, "/etc/os-release", "PRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\nID=ubuntu\nVERSION_ID=\"24.04\"\n")
	write(t, root, "/proc/sys/kernel/osrelease", "6.8.0-45-generic\n")
	write(t, root, "/proc/device-tree/model", "Raspberry Pi 5 Model B Rev 1.0\x00")
	write(t, root, "/proc/meminfo", "MemTotal:        8010052 kB\nMemFree:         1234567 kB\n")
	write(t, root, "/proc/mdstat", mdstatActive)
	disk(t, root, "sda", "devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda", 3907029168) // 2 TB
	tel := New(root)
	tel.Command = virt("none\n", errors.New("exit status 1"))

	want := map[string]any{
		"distribution":            "v0.5.0",
		"core":                    "v" + common.VERSION,
		"arch":                    arch(runtime.GOARCH, buildSettings()),
		"os":                      "ubuntu 24.04",
		"kernel":                  "6.8",
		"virtualization":          "none",
		"model":                   "Raspberry Pi 5 Model B Rev 1.0",
		"docker":                  "unknown",
		"cpu_cores":               runtime.NumCPU(),
		"ram_gb":                  "8",
		"disks":                   1,
		"storage_tb":              "2-4",
		"raid":                    true,
		"$process_person_profile": false,
		"$lib":                    "recasaos-core",
	}
	if got := tel.Properties(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Properties() =\n%v\nwant\n%v", got, want)
	}
}

func TestAnEmptyBoxIsUnknownEverywhere(t *testing.T) {
	tel := New(t.TempDir())
	tel.Command = virt("", errors.New(`exec: "systemd-detect-virt": executable file not found in $PATH`))
	got := tel.Properties()
	for _, key := range []string{"distribution", "os", "kernel", "virtualization", "model", "docker", "ram_gb"} {
		if got[key] != "unknown" {
			t.Errorf("%s = %v, want unknown", key, got[key])
		}
	}
	if got["disks"] != 0 || got["storage_tb"] != "<0.5" || got["raid"] != false {
		t.Errorf("disks, storage_tb, raid = %v, %v, %v; want 0, <0.5, false", got["disks"], got["storage_tb"], got["raid"])
	}
}

func TestArch(t *testing.T) {
	armv7 := []debug.BuildSetting{{Key: "GOARCH", Value: "arm"}, {Key: "GOARM", Value: "7"}}
	cases := []struct {
		goarch   string
		settings []debug.BuildSetting
		want     string
	}{
		{"amd64", []debug.BuildSetting{{Key: "GOARCH", Value: "amd64"}, {Key: "GOAMD64", Value: "v1"}}, "amd64"},
		{"arm64", nil, "arm64"},
		{"arm", armv7, "arm-7"},
		{"arm", nil, "arm"},
	}
	for _, tc := range cases {
		if got := arch(tc.goarch, tc.settings); got != tc.want {
			t.Errorf("arch(%q, %v) = %q, want %q", tc.goarch, tc.settings, got, tc.want)
		}
	}
}

func TestOSRelease(t *testing.T) {
	cases := []struct {
		name, content, want string
	}{
		{"ubuntu", "NAME=\"Ubuntu\"\nID=ubuntu\nID_LIKE=debian\nVERSION_ID=\"24.04\"\n", "ubuntu 24.04"},
		{"debian", "PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nVERSION_ID=\"12\"\nID=debian\n", "debian 12"},
		{"arch has no VERSION_ID", "NAME=\"Arch Linux\"\nID=arch\nBUILD_ID=rolling\n", "arch"},
		{"single quotes", "ID='fedora'\nVERSION_ID='40'\n", "fedora 40"},
		{"no ID", "NAME=\"Something\"\n", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "/etc/os-release", tc.content)
			if got := New(root).osRelease(); got != tc.want {
				t.Fatalf("osRelease() = %q, want %q", got, tc.want)
			}
		})
	}
	if got := New(t.TempDir()).osRelease(); got != "unknown" {
		t.Errorf("osRelease() with no /etc/os-release = %q, want unknown", got)
	}
}

func TestKernelIsMajorMinor(t *testing.T) {
	cases := []struct {
		release, want string
	}{
		{"6.8.0-45-generic\n", "6.8"},
		{"5.15.167.4-microsoft-standard-WSL2\n", "5.15"},
		{"6.12.20+rpt-rpi-2712\n", "6.12"},
		{"6.10\n", "6.10"},
		{"", "unknown"},
		{"not a version\n", "unknown"},
	}
	for _, tc := range cases {
		root := t.TempDir()
		write(t, root, "/proc/sys/kernel/osrelease", tc.release)
		if got := New(root).kernel(); got != tc.want {
			t.Errorf("kernel() of %q = %q, want %q", tc.release, got, tc.want)
		}
	}
	if got := New(t.TempDir()).kernel(); got != "unknown" {
		t.Errorf("kernel() with no osrelease = %q, want unknown", got)
	}
}

func TestVirtualization(t *testing.T) {
	cases := []struct {
		name, out string
		err       error
		want      string
	}{
		{"a virtual machine", "kvm\n", nil, "kvm"},
		{"a container", "lxc\n", nil, "lxc"},
		{"WSL", "wsl\n", nil, "wsl"},
		// It exits 1 when it prints none: the output counts, not the status.
		{"bare metal", "none\n", errors.New("exit status 1"), "none"},
		{"not installed", "", errors.New(`exec: "systemd-detect-virt": executable file not found in $PATH`), "unknown"},
	}
	for _, tc := range cases {
		tel := New(t.TempDir())
		tel.Command = virt(tc.out, tc.err)
		if got := tel.virtualization(); got != tc.want {
			t.Errorf("%s: virtualization() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestModel(t *testing.T) {
	long := strings.Repeat("x", 80)
	cases := []struct {
		name, deviceTree, dmi, want string
	}{
		{"a device-tree board", "Raspberry Pi 5 Model B Rev 1.0\x00", "", "Raspberry Pi 5 Model B Rev 1.0"},
		{"a DMI machine", "", "ZimaBoard\n", "ZimaBoard"},
		{"the device tree first", "Raspberry Pi 4 Model B Rev 1.5\x00", "Other\n", "Raspberry Pi 4 Model B Rev 1.5"},
		{"an empty device tree falls back to DMI", "\x00", "ZimaBoard\n", "ZimaBoard"},
		{"placeholder OEM", "", "To Be Filled By O.E.M.\n", "unknown"},
		{"placeholder System Product Name", "", "System Product Name\n", "unknown"},
		{"placeholder Default string", "", "Default string\n", "unknown"},
		{"placeholder Not Specified", "", "Not Specified\n", "unknown"},
		{"a placeholder in another case", "", "not specified\n", "unknown"},
		{"64 characters at most", "", long + "\n", long[:64]},
		{"nothing", "", "", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.deviceTree != "" {
				write(t, root, "/proc/device-tree/model", tc.deviceTree)
			}
			if tc.dmi != "" {
				write(t, root, "/sys/class/dmi/id/product_name", tc.dmi)
			}
			if got := New(root).model(); got != tc.want {
				t.Fatalf("model() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDocker(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "/var/run/docker.sock")
	if err := os.MkdirAll(filepath.Dir(socket), 0o755); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Version":"28.3.1","ApiVersion":"1.51","Os":"linux"}`))
	}))

	if got := New(root).docker(); got != "28.3.1" {
		t.Fatalf("docker() = %q, want 28.3.1", got)
	}
	if got := New(t.TempDir()).docker(); got != "unknown" {
		t.Fatalf("docker() with no socket = %q, want unknown", got)
	}
}

func TestRAMIsRoundedToTheNearestSize(t *testing.T) {
	const gib = 1 << 20 // in kB, as /proc/meminfo counts
	cases := []struct {
		kib  uint64
		want string
	}{
		{gib / 2, "1"},
		{gib*3/2 - 1, "1"},
		{gib * 3 / 2, "2"},
		{3*gib - 1, "2"},
		{3 * gib, "4"},
		{6*gib - 1, "4"},
		{6 * gib, "8"},
		{8010052, "8"}, // an 8 GB box
		{12*gib - 1, "8"},
		{12 * gib, "16"},
		{24 * gib, "32"},
		{48 * gib, "64"},
		{96*gib - 1, "64"},
		{96 * gib, "128+"},
		{512 * gib, "128+"},
	}
	for _, tc := range cases {
		if got := ramBucket(tc.kib); got != tc.want {
			t.Errorf("ramBucket(%d kB) = %q, want %q", tc.kib, got, tc.want)
		}
	}
}

func TestRAMFromMeminfo(t *testing.T) {
	root := t.TempDir()
	write(t, root, "/proc/meminfo", "MemTotal:        3884096 kB\nMemFree:          123456 kB\n")
	if got := New(root).ramGB(); got != "4" {
		t.Fatalf("ramGB() = %q, want 4", got)
	}
	if got := New(t.TempDir()).ramGB(); got != "unknown" {
		t.Fatalf("ramGB() with no meminfo = %q, want unknown", got)
	}
}

func TestDisksAreThePhysicalBlockDevices(t *testing.T) {
	root := t.TempDir()
	disk(t, root, "sda", "devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda", 3907029168)       // 2 TB
	disk(t, root, "nvme0n1", "devices/pci0000:00/0000:00:1d.0/0000:3d:00.0/nvme/nvme0/nvme0n1", 1953525168)            // 1 TB
	disk(t, root, "mmcblk0", "devices/platform/emmc2bus/fe340000.mmc/mmc_host/mmc0/mmc0:0001/block/mmcblk0", 61071360) // 31 GB
	// Left out: an eMMC boot partition, an optical drive, and the virtual devices.
	disk(t, root, "mmcblk0boot0", "devices/platform/emmc2bus/fe340000.mmc/mmc_host/mmc0/mmc0:0001/block/mmcblk0/mmcblk0boot0", 8192)
	disk(t, root, "sr0", "devices/pci0000:00/0000:00:17.0/ata2/host1/target1:0:0/1:0:0:0/block/sr0", 2097151)
	disk(t, root, "loop0", "devices/virtual/block/loop0", 1000000)
	disk(t, root, "zram0", "devices/virtual/block/zram0", 1000000)
	disk(t, root, "md0", "devices/virtual/block/md0", 1000000)

	count, size := New(root).disks()
	if count != 3 {
		t.Fatalf("disks = %d, want 3", count)
	}
	if want := uint64(3907029168+1953525168+61071360) * 512; size != want {
		t.Fatalf("size = %d bytes, want %d", size, want)
	}
	if got := storageTB(size); got != "2-4" {
		t.Fatalf("storageTB(%d) = %q, want 2-4", size, got)
	}
	if count, size := New(t.TempDir()).disks(); count != 0 || size != 0 {
		t.Fatalf("disks() with no /sys/block = %d, %d; want 0, 0", count, size)
	}
}

func TestStorageBuckets(t *testing.T) {
	const tb = 1_000_000_000_000
	cases := []struct {
		size uint64
		want string
	}{
		{0, "<0.5"},
		{tb/2 - 1, "<0.5"},
		{tb / 2, "0.5-1"},
		{tb - 1, "0.5-1"},
		{tb, "1-2"},
		{2*tb - 1, "1-2"},
		{2 * tb, "2-4"},
		{4*tb - 1, "2-4"},
		{4 * tb, "4-8"},
		{8 * tb, "8-16"},
		{16 * tb, "16-32"},
		{32*tb - 1, "16-32"},
		{32 * tb, "32+"},
		{100 * tb, "32+"},
	}
	for _, tc := range cases {
		if got := storageTB(tc.size); got != tc.want {
			t.Errorf("storageTB(%d) = %q, want %q", tc.size, got, tc.want)
		}
	}
}

func TestRAID(t *testing.T) {
	cases := []struct {
		name, mdstat string
		want         bool
	}{
		{"an active array", mdstatActive, true},
		{"an inactive array", mdstatInactive, false},
		{"no array", mdstatNone, false},
	}
	for _, tc := range cases {
		root := t.TempDir()
		write(t, root, "/proc/mdstat", tc.mdstat)
		if got := New(root).raid(); got != tc.want {
			t.Errorf("%s: raid() = %v, want %v", tc.name, got, tc.want)
		}
	}
	if New(t.TempDir()).raid() {
		t.Error("raid() with no /proc/mdstat = true, want false")
	}
}
```

- [ ] **Step 2: Run it and see it fail**

```bash
cd D:/clients/casaos/CasaOS && go test ./pkg/telemetry/ ; GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./pkg/telemetry/
```

Expected: both FAIL to compile the tests, `tel.Properties undefined`, `undefined: arch`, `undefined: buildSettings`, `undefined: ramBucket`, `undefined: storageTB`; `go test` ends with `[build failed]`. Linux/CI: `go test -race ./pkg/telemetry/ -run 'Properties|EmptyBox|Arch|OSRelease|Kernel|Virtualization|Model|Docker|RAM|Disks|Storage|RAID' -v` fails with the same compile errors.

- [ ] **Step 3: Write the minimal implementation**

Create `pkg/telemetry/properties.go`:

```go
package telemetry

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/common"
)

const unknown = "unknown"

// Properties is what every event carries. The sender and the preview the
// dashboard shows both build it here: what the owner sees is what is sent.
func (t *Telemetry) Properties() map[string]any {
	disks, size := t.disks()
	return map[string]any{
		"distribution":            t.fileValue(common.FORK_RELEASE_FILE),
		"core":                    "v" + common.VERSION,
		"arch":                    arch(runtime.GOARCH, buildSettings()),
		"os":                      t.osRelease(),
		"kernel":                  t.kernel(),
		"virtualization":          t.virtualization(),
		"model":                   t.model(),
		"docker":                  t.docker(),
		"cpu_cores":               runtime.NumCPU(),
		"ram_gb":                  t.ramGB(),
		"disks":                   disks,
		"storage_tb":              storageTB(size),
		"raid":                    t.raid(),
		"$process_person_profile": false,
		"$lib":                    "recasaos-core",
	}
}

// fileValue is a file's trimmed content, or "unknown" when it is absent or empty.
func (t *Telemetry) fileValue(name string) string {
	data, err := os.ReadFile(t.path(name))
	if value := strings.TrimSpace(string(data)); err == nil && value != "" {
		return value
	}
	return unknown
}

func buildSettings() []debug.BuildSetting {
	if info, ok := debug.ReadBuildInfo(); ok {
		return info.Settings
	}
	return nil
}

// arch is GOARCH, with GOARM on arm: "arm-7", as the release names its armv7 build.
func arch(goarch string, settings []debug.BuildSetting) string {
	if goarch != "arm" {
		return goarch
	}
	for _, setting := range settings {
		if setting.Key == "GOARM" && setting.Value != "" {
			return "arm-" + setting.Value
		}
	}
	return goarch
}

// osRelease is ID and VERSION_ID from /etc/os-release, ID alone when there is
// no VERSION_ID.
func (t *Telemetry) osRelease() string {
	data, err := os.ReadFile(t.path("/etc/os-release"))
	if err != nil {
		return unknown
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			values[key] = strings.Trim(value, `"'`)
		}
	}
	switch {
	case values["ID"] == "":
		return unknown
	case values["VERSION_ID"] == "":
		return values["ID"]
	default:
		return values["ID"] + " " + values["VERSION_ID"]
	}
}

var majorMinor = regexp.MustCompile(`^\d+\.\d+`)

// kernel is the running kernel's release, major.minor: what uname -r answers,
// read where the kernel keeps it.
func (t *Telemetry) kernel() string {
	data, _ := os.ReadFile(t.path("/proc/sys/kernel/osrelease"))
	if version := majorMinor.FindString(strings.TrimSpace(string(data))); version != "" {
		return version
	}
	return unknown
}

// virtualization is what systemd-detect-virt prints. It exits 1 when it prints
// "none", so its output counts, not its exit status.
func (t *Telemetry) virtualization() string {
	out, _ := t.Command("systemd-detect-virt")
	if value := strings.TrimSpace(string(out)); value != "" {
		return value
	}
	return unknown
}

// modelPlaceholders are what firmware answers when nobody named the machine.
var modelPlaceholders = []string{"To Be Filled By O.E.M.", "System Product Name", "Default string", "Not Specified"}

// model is the board's device-tree model, else the DMI product name.
func (t *Telemetry) model() string {
	for _, name := range []string{"/proc/device-tree/model", "/sys/class/dmi/id/product_name"} {
		data, _ := os.ReadFile(t.path(name))
		value := strings.Trim(string(data), "\x00 \t\r\n")
		if value == "" {
			continue
		}
		if runes := []rune(value); len(runes) > 64 {
			value = strings.TrimSpace(string(runes[:64]))
		}
		for _, placeholder := range modelPlaceholders {
			if strings.EqualFold(value, placeholder) {
				return unknown
			}
		}
		return value
	}
	return unknown
}

// docker is the engine's version, from GET /version on its socket.
func (t *Telemetry) docker() string {
	socket := t.path("/var/run/docker.sock")
	client := &http.Client{
		Timeout: 5 * time.Second,
		// Its own transport, and so no proxy: this request never leaves the box.
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		}},
	}
	defer client.CloseIdleConnections()
	resp, err := client.Get("http://docker/version")
	if err != nil {
		return unknown
	}
	defer resp.Body.Close()
	var version struct{ Version string }
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&version) != nil || version.Version == "" {
		return unknown
	}
	return version.Version
}

// ramGB is MemTotal rounded to the nearest of 1, 2, 4 ... 64 GiB, "128+" past
// that. A string, because of "128+".
func (t *Telemetry) ramGB() string {
	data, err := os.ReadFile(t.path("/proc/meminfo"))
	if err != nil {
		return unknown
	}
	for _, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "MemTotal:" {
			if kib, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
				return ramBucket(kib)
			}
		}
	}
	return unknown
}

// ramBucket takes kB as /proc/meminfo counts them (KiB). The boundary between
// two sizes is their midpoint, 1.5 times the smaller; a midpoint goes up.
func ramBucket(kib uint64) string {
	gib := float64(kib) / (1 << 20)
	for _, size := range []float64{1, 2, 4, 8, 16, 32, 64} {
		if gib < size*1.5 {
			return strconv.Itoa(int(size))
		}
	}
	return "128+"
}

// disks counts the physical block devices and sums their sizes: the entries of
// /sys/block that do not resolve under /sys/devices/virtual/ (loop, zram, dm,
// md), less optical drives (sr*) and eMMC boot partitions (mmcblk*boot*).
func (t *Telemetry) disks() (count int, size uint64) {
	dir := t.path("/sys/block")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, entry := range entries {
		name := entry.Name()
		if skip, _ := filepath.Match("sr*", name); skip {
			continue
		}
		if skip, _ := filepath.Match("mmcblk*boot*", name); skip {
			continue
		}
		resolved, err := filepath.EvalSymlinks(filepath.Join(dir, name))
		if err != nil || strings.Contains(resolved, "/sys/devices/virtual/") {
			continue
		}
		count++
		data, _ := os.ReadFile(filepath.Join(dir, name, "size"))
		sectors, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		size += sectors * 512
	}
	return count, size
}

// storageTB buckets a size in TB of 10^12 bytes.
func storageTB(size uint64) string {
	const tb = 1_000_000_000_000
	switch {
	case size < tb/2:
		return "<0.5"
	case size < tb:
		return "0.5-1"
	case size < 2*tb:
		return "1-2"
	case size < 4*tb:
		return "2-4"
	case size < 8*tb:
		return "4-8"
	case size < 16*tb:
		return "8-16"
	case size < 32*tb:
		return "16-32"
	default:
		return "32+"
	}
}

// raid reports whether /proc/mdstat lists an active md array: "md0 : active raid1 ...".
func (t *Telemetry) raid() bool {
	data, _ := os.ReadFile(t.path("/proc/mdstat"))
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && strings.HasPrefix(fields[0], "md") && fields[1] == ":" && fields[2] == "active" {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run it and see it pass**

```bash
cd D:/clients/casaos/CasaOS && gofmt -l pkg/telemetry && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./pkg/telemetry/ && GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go vet ./pkg/telemetry/ && go test ./pkg/telemetry/
```

Expected: `gofmt` and both `vet` runs print nothing. The native `go test` fails on exactly three tests, all Linux-only: `TestTheStateIsSavedAsA0600JSONFile` (`mode = -rw-rw-rw-`), `TestPropertiesOfABox` and `TestDisksAreThePhysicalBlockDevices` (`mkdir ...\sys\devices\pci0000:00: The directory name is invalid.`); every other test passes, `TestDocker` included (Windows has Unix sockets). Linux/CI: `go test -race ./pkg/telemetry/ -v` prints `ok  	github.com/ReCasaOS/CasaOS/pkg/telemetry`.

- [ ] **Step 5: Commit**

```bash
cd D:/clients/casaos/CasaOS && git add pkg/telemetry/properties.go pkg/telemetry/properties_test.go && git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(telemetry): collect the anonymous properties of a box" -m "One function reads them under an injectable root, for the sender and the preview alike: versions, arch, os, kernel, virtualization, model, docker, cores, memory, disks, storage and RAID, bucketed where the spec says so."
```

---

### Task 3: The PostHog sender

**Files:**
- Create: `pkg/telemetry/send.go`
- Test: `pkg/telemetry/send_test.go`

**Interfaces:**
- Consumes: `Telemetry` fields `Endpoint`, `Now`, `client`; `apiKey`, `endpointEnv`, `New` (Task 1); `Properties`, `virt`, `write` (Tasks 1-2).
- Produces:
  - `func (t *Telemetry) send(ctx context.Context, event, id string, properties map[string]any) error` (non-nil on transport error or non-2xx).
  - Test helpers: `var testNow = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)`; `type capture` with `all() []captured` and `events() []string`; `type captured struct { method, contentType string; body map[string]any }`; `newCapture(t, status int) (*capture, string)`; `fixture(t, endpoint string) (*Telemetry, string)` (root with `fork-release` `v0.5.0` and `os-release` `debian 12`, `Now` = `testNow`, `Command` = `virt("none\n", nil)`); `asJSON(t, v any) any`.

- [ ] **Step 1: Write the failing test**

Create `pkg/telemetry/send_test.go`:

```go
package telemetry

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/common"
)

var testNow = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

// capture stands for PostHog: it records every request and answers a status.
type capture struct {
	mu       sync.Mutex
	requests []captured
}

type captured struct {
	method, contentType string
	body                map[string]any
}

func newCapture(t *testing.T, status int) (*capture, string) {
	t.Helper()
	c := &capture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("the request body is not JSON: %v", err)
		}
		c.mu.Lock()
		c.requests = append(c.requests, captured{r.Method, r.Header.Get("Content-Type"), body})
		c.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return c, server.URL
}

func (c *capture) all() []captured {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]captured(nil), c.requests...)
}

// events is the event name of every request, in order.
func (c *capture) events() []string {
	var names []string
	for _, r := range c.all() {
		name, _ := r.body["event"].(string)
		names = append(names, name)
	}
	return names
}

// fixture is a box with a release marker and an os-release, sending to
// endpoint at testNow.
func fixture(t *testing.T, endpoint string) (*Telemetry, string) {
	t.Helper()
	root := t.TempDir()
	write(t, root, common.FORK_RELEASE_FILE, "v0.5.0\n")
	write(t, root, "/etc/os-release", "ID=debian\nVERSION_ID=\"12\"\n")
	tel := New(root)
	tel.Endpoint = endpoint
	tel.Now = func() time.Time { return testNow }
	tel.Command = virt("none\n", nil)
	return tel, root
}

// asJSON is v as the capture server decodes it: numbers become float64.
func asJSON(t *testing.T, v any) any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestTheRequestIsWhatPostHogExpects(t *testing.T) {
	c, endpoint := newCapture(t, http.StatusOK)
	tel, _ := fixture(t, endpoint)
	properties := tel.Properties()

	if err := tel.send(context.Background(), "heartbeat", "0b0f4d8e-6d7a-4f55-9a53-5b8c1f3e2a10", properties); err != nil {
		t.Fatal(err)
	}

	requests := c.all()
	if len(requests) != 1 {
		t.Fatalf("%d requests, want 1", len(requests))
	}
	r := requests[0]
	if r.method != http.MethodPost || r.contentType != "application/json" {
		t.Fatalf("request = %s with Content-Type %q, want POST application/json", r.method, r.contentType)
	}
	if keys := slices.Sorted(maps.Keys(r.body)); !reflect.DeepEqual(keys, []string{"api_key", "distinct_id", "event", "properties", "timestamp"}) {
		t.Fatalf("body keys = %v", keys)
	}
	if r.body["api_key"] != "phc_C5T8QbrhBttzgkWuKm2Xr9S4TLGh3sCVAZai2oaFEuhD" {
		t.Fatalf("api_key = %v", r.body["api_key"])
	}
	if r.body["event"] != "heartbeat" || r.body["distinct_id"] != "0b0f4d8e-6d7a-4f55-9a53-5b8c1f3e2a10" || r.body["timestamp"] != "2026-09-22T12:00:00Z" {
		t.Fatalf("event, distinct_id, timestamp = %v, %v, %v", r.body["event"], r.body["distinct_id"], r.body["timestamp"])
	}
	sent, _ := r.body["properties"].(map[string]any)
	if sent["$process_person_profile"] != false || sent["$lib"] != "recasaos-core" {
		t.Fatalf("$process_person_profile, $lib = %v, %v", sent["$process_person_profile"], sent["$lib"])
	}
	if _, ok := sent["$geoip_disable"]; ok {
		t.Fatal("$geoip_disable is sent: PostHog could not derive the country")
	}
	if want := asJSON(t, properties); !reflect.DeepEqual(any(sent), want) {
		t.Fatalf("properties sent =\n%v\nwant\n%v", sent, want)
	}
}

func TestAnAnswerOutside2xxIsAFailure(t *testing.T) {
	cases := []struct {
		status int
		fails  bool
	}{
		{http.StatusOK, false},
		{http.StatusNoContent, false},
		{http.StatusBadRequest, true},
		{http.StatusUnauthorized, true},
		{http.StatusInternalServerError, true},
		{http.StatusServiceUnavailable, true},
	}
	for _, tc := range cases {
		_, endpoint := newCapture(t, tc.status)
		tel, _ := fixture(t, endpoint)
		err := tel.send(context.Background(), "heartbeat", "id", map[string]any{})
		if (err != nil) != tc.fails {
			t.Errorf("status %d: send() error = %v, want a failure: %v", tc.status, err, tc.fails)
		}
	}
}

func TestATransportErrorIsAFailure(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close() // nothing listens there any more
	tel, _ := fixture(t, server.URL)
	if err := tel.send(context.Background(), "heartbeat", "id", map[string]any{}); err == nil {
		t.Fatal("send() to a closed server succeeded")
	}
}

func TestTheEndpointIsPostHogEUUnlessTheInstallCheckSetsOne(t *testing.T) {
	t.Setenv("CASAOS_TELEMETRY_ENDPOINT", "")
	if got := New("/").Endpoint; got != "https://eu.i.posthog.com/i/v0/e/" {
		t.Fatalf("Endpoint = %q, want PostHog EU", got)
	}
	t.Setenv("CASAOS_TELEMETRY_ENDPOINT", "http://127.0.0.1:8999/capture")
	if got := New("/").Endpoint; got != "http://127.0.0.1:8999/capture" {
		t.Fatalf("Endpoint = %q, want the test setting", got)
	}
}

func TestTheClientWaitsTenSecondsThroughTheEnvironmentProxy(t *testing.T) {
	client := New("/").client
	if client.Timeout != 10*time.Second {
		t.Fatalf("Timeout = %v, want 10s", client.Timeout)
	}
	// A nil Transport is http.DefaultTransport, whose Proxy is http.ProxyFromEnvironment.
	if client.Transport != nil {
		t.Fatalf("Transport = %T, want nil (http.DefaultTransport)", client.Transport)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

```bash
cd D:/clients/casaos/CasaOS && go test ./pkg/telemetry/ ; GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./pkg/telemetry/
```

Expected: both FAIL to compile the tests, `tel.send undefined (type *Telemetry has no field or method send)`; `go test` ends with `[build failed]`. Linux/CI: `go test -race ./pkg/telemetry/ -run 'Request|2xx|Transport|Endpoint|Client' -v` fails with the same compile error.

- [ ] **Step 3: Write the minimal implementation**

Create `pkg/telemetry/send.go`:

```go
package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// send posts one event to PostHog's capture endpoint. A transport error or an
// answer outside 2xx is a failure: the caller logs it and waits for the next check.
func (t *Telemetry) send(ctx context.Context, event, id string, properties map[string]any) error {
	body, err := json.Marshal(map[string]any{
		"api_key":     apiKey,
		"event":       event,
		"distinct_id": id,
		"timestamp":   t.Now().UTC().Format(time.RFC3339),
		"properties":  properties,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s answered %s", t.Endpoint, resp.Status)
	}
	return nil
}
```

- [ ] **Step 4: Run it and see it pass**

```bash
cd D:/clients/casaos/CasaOS && gofmt -l pkg/telemetry && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./pkg/telemetry/ && go test ./pkg/telemetry/
```

Expected: `gofmt` and `vet` print nothing. The native `go test` fails on the same three Linux-only tests as in Task 2 (`TestTheStateIsSavedAsA0600JSONFile`, `TestPropertiesOfABox`, `TestDisksAreThePhysicalBlockDevices`) and nothing else: every test of `send_test.go` passes. Linux/CI: `go test -race ./pkg/telemetry/ -v` prints `ok  	github.com/ReCasaOS/CasaOS/pkg/telemetry`.

- [ ] **Step 5: Commit**

```bash
cd D:/clients/casaos/CasaOS && git add pkg/telemetry/send.go pkg/telemetry/send_test.go && git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(telemetry): send an event to PostHog's capture endpoint" -m "10 s timeout, the proxy from the environment, and anything but a 2xx is a failure. CASAOS_TELEMETRY_ENDPOINT replaces the URL for the install check."
```

---

### Task 4: The hourly check and its loop

**Files:**
- Create: `pkg/telemetry/check.go`
- Test: `pkg/telemetry/check_test.go`

**Interfaces:**
- Consumes: `load`, `modify`, `save`, `path`, `upgradedFromFile`, `startDelayEnv` (Task 1); `Properties`, `fileValue` (Task 2); `send` (Task 3); test helpers `write`, `exists`, `fixture`, `newCapture`, `testNow` (Tasks 1-3); `logger.Info` from CasaOS-Common; `uuid.NewString`.
- Produces:
  - `func (t *Telemetry) Check(ctx context.Context)`
  - `func (t *Telemetry) Run(ctx context.Context)` (blocks until `ctx` is done; `main.go` runs it in a goroutine)
  - `func startDelay() time.Duration`
  - Log lines at Info: `telemetry: send failed` with fields `event` and `error`; `telemetry: cannot save telemetry.json`; `telemetry: cannot delete upgraded-from`.
  - Test helpers: `TestMain` (initialises the logger for the whole package), `saveState(t, tel *Telemetry, s State)`.

- [ ] **Step 1: Write the failing test**

Create `pkg/telemetry/check_test.go`:

```go
package telemetry

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/google/uuid"
)

func TestMain(m *testing.M) {
	// Check logs its failures through CasaOS-Common's logger, nil until initialised.
	logger.LogInitConsoleOnly()
	m.Run()
}

func saveState(t *testing.T, tel *Telemetry, s State) {
	t.Helper()
	if err := tel.save(s); err != nil {
		t.Fatal(err)
	}
}

func TestNothingIsBuiltOrSentWhenStatisticsAreOff(t *testing.T) {
	for _, tc := range []struct{ name, state string }{
		{"turned off", `{"enabled":false,"id":"","notice_seen":true}`},
		{"a malformed state", "{"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, endpoint := newCapture(t, http.StatusOK)
			tel, root := fixture(t, endpoint)
			write(t, root, StateFile, tc.state)
			write(t, root, upgradedFromFile, "v0.4.99\n")
			commands := 0
			tel.Command = func(string, ...string) ([]byte, error) {
				commands++
				return []byte("none\n"), nil
			}

			tel.Check(context.Background())

			if n := len(c.all()); n != 0 {
				t.Fatalf("%d requests, want none", n)
			}
			if commands != 0 {
				t.Fatal("the properties were built")
			}
			if exists(t, root, upgradedFromFile) {
				t.Fatal("upgraded-from was kept")
			}
			if data, _ := os.ReadFile(filepath.Join(root, StateFile)); string(data) != tc.state {
				t.Fatalf("telemetry.json = %s, want it untouched", data)
			}
		})
	}
}

func TestNoHeartbeatWithinADay(t *testing.T) {
	c, endpoint := newCapture(t, http.StatusOK)
	tel, _ := fixture(t, endpoint)
	lastSent := testNow.Add(-23 * time.Hour)
	saveState(t, tel, State{Enabled: true, ID: "kept-id", LastSent: lastSent})

	tel.Check(context.Background())

	if events := c.events(); len(events) != 0 {
		t.Fatalf("sent %v, want nothing", events)
	}
	if got := tel.load().LastSent; !got.Equal(lastSent) {
		t.Fatalf("last_sent = %v, want %v", got, lastSent)
	}
}

func TestAHeartbeatOnceLastSentIsADayOld(t *testing.T) {
	c, endpoint := newCapture(t, http.StatusOK)
	tel, _ := fixture(t, endpoint)
	saveState(t, tel, State{Enabled: true, ID: "kept-id", LastSent: testNow.Add(-24 * time.Hour)})

	tel.Check(context.Background())

	requests := c.all()
	if len(requests) != 1 || requests[0].body["event"] != "heartbeat" || requests[0].body["distinct_id"] != "kept-id" {
		t.Fatalf("requests = %v, want one heartbeat from kept-id", requests)
	}
	if got := tel.load().LastSent; !got.Equal(testNow) {
		t.Fatalf("last_sent = %v, want %v", got, testNow)
	}
}

func TestTheFirstCheckMakesAndKeepsTheID(t *testing.T) {
	c, endpoint := newCapture(t, http.StatusOK)
	tel, root := fixture(t, endpoint)

	tel.Check(context.Background()) // no telemetry.json: enabled, a heartbeat is due

	state := tel.load()
	if id, err := uuid.Parse(state.ID); err != nil || id.Version() != 4 {
		t.Fatalf("id = %q, want a UUID v4", state.ID)
	}
	if !state.Enabled || !state.LastSent.Equal(testNow) {
		t.Fatalf("state = %+v, want enabled and last_sent %v", state, testNow)
	}
	requests := c.all()
	if len(requests) != 1 || requests[0].body["distinct_id"] != state.ID {
		t.Fatalf("requests = %v, want one from %s", requests, state.ID)
	}
	info, err := os.Stat(filepath.Join(root, StateFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("telemetry.json mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestAFailedSendKeepsLastSentAndUpgradedFrom(t *testing.T) {
	c, endpoint := newCapture(t, http.StatusInternalServerError)
	tel, root := fixture(t, endpoint)
	lastSent := testNow.Add(-48 * time.Hour)
	saveState(t, tel, State{Enabled: true, ID: "kept-id", LastSent: lastSent})
	write(t, root, upgradedFromFile, "v0.4.99\n")

	tel.Check(context.Background())

	if events := c.events(); !reflect.DeepEqual(events, []string{"version_changed", "heartbeat"}) {
		t.Fatalf("tried %v, want version_changed then heartbeat", events)
	}
	if got := tel.load().LastSent; !got.Equal(lastSent) {
		t.Fatalf("last_sent = %v, want it unchanged at %v", got, lastSent)
	}
	if data, err := os.ReadFile(filepath.Join(root, upgradedFromFile)); err != nil || string(data) != "v0.4.99\n" {
		t.Fatalf("upgraded-from = %q, %v; want it kept", data, err)
	}
}

func TestASentVersionChangedDeletesUpgradedFrom(t *testing.T) {
	c, endpoint := newCapture(t, http.StatusOK)
	tel, root := fixture(t, endpoint)
	saveState(t, tel, State{Enabled: true, ID: "kept-id", LastSent: testNow.Add(-48 * time.Hour)})
	write(t, root, upgradedFromFile, "v0.4.99\n")

	tel.Check(context.Background())

	requests := c.all()
	if len(requests) != 2 {
		t.Fatalf("%d requests, want version_changed and heartbeat", len(requests))
	}
	changed, _ := requests[0].body["properties"].(map[string]any)
	if requests[0].body["event"] != "version_changed" || changed["previous_distribution"] != "v0.4.99" || changed["distribution"] != "v0.5.0" {
		t.Fatalf("first request = %v, want version_changed from v0.4.99 to v0.5.0", requests[0].body)
	}
	heartbeat, _ := requests[1].body["properties"].(map[string]any)
	if _, ok := heartbeat["previous_distribution"]; requests[1].body["event"] != "heartbeat" || ok {
		t.Fatalf("second request = %v, want a heartbeat without previous_distribution", requests[1].body)
	}
	if exists(t, root, upgradedFromFile) {
		t.Fatal("upgraded-from was kept after a successful send")
	}
	if got := tel.load().LastSent; !got.Equal(testNow) {
		t.Fatalf("last_sent = %v, want %v", got, testNow)
	}
}

func TestStartDelay(t *testing.T) {
	t.Setenv("CASAOS_TELEMETRY_START_DELAY", "0s")
	if got := startDelay(); got != 0 {
		t.Fatalf("startDelay() with 0s = %v, want 0", got)
	}
	t.Setenv("CASAOS_TELEMETRY_START_DELAY", "90s")
	if got := startDelay(); got != 90*time.Second {
		t.Fatalf("startDelay() with 90s = %v, want 90s", got)
	}
	for _, value := range []string{"", "soon", "-5s"} {
		t.Setenv("CASAOS_TELEMETRY_START_DELAY", value)
		for range 100 {
			if got := startDelay(); got < 0 || got >= time.Hour {
				t.Fatalf("startDelay() with %q = %v, want 0 to 60 minutes", value, got)
			}
		}
	}
}

func TestRunChecksOnceTheStartDelayIsOver(t *testing.T) {
	t.Setenv("CASAOS_TELEMETRY_START_DELAY", "0s")
	c, endpoint := newCapture(t, http.StatusOK)
	tel, _ := fixture(t, endpoint)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tel.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(10 * time.Second)
	for len(c.events()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	if events := c.events(); !reflect.DeepEqual(events, []string{"heartbeat"}) {
		t.Fatalf("sent %v, want one heartbeat", events)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

```bash
cd D:/clients/casaos/CasaOS && go test ./pkg/telemetry/ ; GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./pkg/telemetry/
```

Expected: both FAIL to compile the tests, `tel.Check undefined`, `tel.Run undefined`, `undefined: startDelay`; `go test` ends with `[build failed]`. Linux/CI: `go test -race ./pkg/telemetry/ -run 'Off|Heartbeat|LastSent|FirstCheck|Failed|VersionChanged|StartDelay|Run' -v` fails with the same compile errors.

- [ ] **Step 3: Write the minimal implementation**

Create `pkg/telemetry/check.go`:

```go
package telemetry

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"math/rand/v2"
	"os"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Run checks once after a random delay of up to an hour, which spreads the
// boxes an update restarted together, then every hour until ctx is done.
func (t *Telemetry) Run(ctx context.Context) {
	delay := time.NewTimer(startDelay())
	defer delay.Stop()
	select {
	case <-delay.C:
	case <-ctx.Done():
		return
	}
	hourly := time.NewTicker(time.Hour)
	defer hourly.Stop()
	for {
		t.Check(ctx)
		select {
		case <-hourly.C:
		case <-ctx.Done():
			return
		}
	}
}

// startDelay is random, from 0 to 60 minutes, unless the install check sets
// CASAOS_TELEMETRY_START_DELAY (a Go duration).
func startDelay() time.Duration {
	if delay, err := time.ParseDuration(os.Getenv(startDelayEnv)); err == nil && delay >= 0 {
		return delay
	}
	return rand.N(time.Hour)
}

// Check is one hourly pass:
//  1. statistics off: delete upgraded-from, build nothing, send nothing;
//  2. upgraded-from present: send version_changed, delete the file only once sent;
//  3. last_sent absent or a day old: send heartbeat, record last_sent only once sent.
//
// A failure is logged and waits for the next check: nothing is retried sooner
// or queued.
func (t *Telemetry) Check(ctx context.Context) {
	upgradedFrom := t.path(upgradedFromFile)
	state := t.load()
	if !state.Enabled {
		if err := os.Remove(upgradedFrom); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logger.Info("telemetry: cannot delete upgraded-from", zap.Error(err))
		}
		return
	}

	_, err := os.Stat(upgradedFrom)
	changed := err == nil
	// As the spec says: absent or at least 24 hours old. A last_sent in the
	// future (a clock that ran ahead) waits for the clock to catch up.
	due := state.LastSent.IsZero() || t.Now().Sub(state.LastSent) >= 24*time.Hour
	if !changed && !due {
		return
	}

	if state.ID == "" {
		// Made on first use and kept before it is sent: an id that could not be
		// kept would count the box again at every check.
		if state, err = t.modify(func(s *State) {
			if s.ID == "" {
				s.ID = uuid.NewString()
			}
		}); err != nil {
			logger.Info("telemetry: cannot save telemetry.json", zap.Error(err))
			return
		}
		if !state.Enabled {
			return // turned off in the meantime
		}
	}

	properties := t.Properties()
	if changed {
		event := maps.Clone(properties)
		event["previous_distribution"] = t.fileValue(upgradedFromFile)
		if err := t.send(ctx, "version_changed", state.ID, event); err != nil {
			logger.Info("telemetry: send failed", zap.String("event", "version_changed"), zap.Error(err))
		} else if err := os.Remove(upgradedFrom); err != nil {
			logger.Info("telemetry: cannot delete upgraded-from", zap.Error(err))
		}
	}
	if due {
		if err := t.send(ctx, "heartbeat", state.ID, properties); err != nil {
			logger.Info("telemetry: send failed", zap.String("event", "heartbeat"), zap.Error(err))
		} else if _, err := t.modify(func(s *State) { s.LastSent = t.Now().UTC().Truncate(time.Second) }); err != nil {
			logger.Info("telemetry: cannot save telemetry.json", zap.Error(err))
		}
	}
}
```

- [ ] **Step 4: Run it and see it pass**

```bash
cd D:/clients/casaos/CasaOS && gofmt -l pkg/telemetry && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./pkg/telemetry/ && go test ./pkg/telemetry/
```

Expected: `gofmt` and `vet` print nothing. The native `go test` fails on exactly the four Linux-only tests: `TestTheStateIsSavedAsA0600JSONFile` and `TestTheFirstCheckMakesAndKeepsTheID` (`mode = -rw-rw-rw-, want 0600`), `TestPropertiesOfABox` and `TestDisksAreThePhysicalBlockDevices` (`The directory name is invalid.`); every other test passes. The two `info	telemetry: send failed` lines (`version_changed`, then `heartbeat`, `answered 500 Internal Server Error`) are `TestAFailedSendKeepsLastSentAndUpgradedFrom` logging as intended, not failures. Linux/CI: `go test -race ./pkg/telemetry/ -v` prints `ok  	github.com/ReCasaOS/CasaOS/pkg/telemetry`.

- [ ] **Step 5: Commit**

```bash
cd D:/clients/casaos/CasaOS && git add pkg/telemetry/check.go pkg/telemetry/check_test.go && git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(telemetry): check hourly, after a random start delay" -m "Off deletes upgraded-from and sends nothing; upgraded-from goes out as version_changed and is deleted once sent; a heartbeat at most once a day, last_sent recorded once sent. Failures are logged at Info and wait for the next check."
```

---

### Task 5: State and preview for the API

**Files:**
- Create: `pkg/telemetry/status.go`
- Test: `pkg/telemetry/status_test.go`

**Interfaces:**
- Consumes: `load`, `modify` (Task 1); `Properties` (Task 2); test helpers `fixture`, `newCapture`, `asJSON`, `stateOnDisk`, `write`, `testNow` (Tasks 1-3), `saveState`, `Check` (Task 4).
- Produces (the JSON is the API contract the dashboard plan relies on):
  - `type Status struct { Enabled bool "json:enabled"; NoticeSeen bool "json:notice_seen"; Preview Preview "json:preview" }`
  - `type Preview struct { Event string "json:event"; Properties map[string]any "json:properties" }`
  - `func (t *Telemetry) Status() Status`
  - `func (t *Telemetry) Update(enabled, noticeSeen *bool) (Status, error)`

- [ ] **Step 1: Write the failing test**

Create `pkg/telemetry/status_test.go`:

```go
package telemetry

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"
)

// unused is an endpoint these tests never send to.
const unused = "http://127.0.0.1:9/unused"

func TestStatusIsTheStateAndTheHeartbeatAsItWouldBeSent(t *testing.T) {
	tel, _ := fixture(t, unused)
	status := tel.Status()
	if !status.Enabled || status.NoticeSeen {
		t.Fatalf("Status() = %+v, want enabled and notice not seen with no telemetry.json", status)
	}
	if status.Preview.Event != "heartbeat" || !reflect.DeepEqual(status.Preview.Properties, tel.Properties()) {
		t.Fatalf("preview = %+v, want the heartbeat's properties", status.Preview)
	}
}

func TestThePreviewIsBuiltWhenStatisticsAreOff(t *testing.T) {
	tel, _ := fixture(t, unused)
	saveState(t, tel, State{Enabled: false})
	status := tel.Status()
	if status.Enabled || status.Preview.Properties["distribution"] != "v0.5.0" {
		t.Fatalf("Status() = %+v, want disabled with a preview of v0.5.0", status)
	}
}

func TestThePreviewIsWhatIsSent(t *testing.T) {
	c, endpoint := newCapture(t, http.StatusOK)
	tel, _ := fixture(t, endpoint)

	tel.Check(context.Background()) // no telemetry.json: a heartbeat is due

	requests := c.all()
	if len(requests) != 1 {
		t.Fatalf("%d requests, want one heartbeat", len(requests))
	}
	if want := asJSON(t, tel.Status().Preview.Properties); !reflect.DeepEqual(requests[0].body["properties"], want) {
		t.Fatalf("properties sent =\n%v\npreview\n%v", requests[0].body["properties"], want)
	}
}

func TestUpdateChangesEnabledAndNoticeSeenAndNothingElse(t *testing.T) {
	tel, _ := fixture(t, unused)
	lastSent := testNow.Add(-time.Hour)
	saveState(t, tel, State{Enabled: true, ID: "kept-id", LastSent: lastSent})
	off, seen := false, true

	status, err := tel.Update(&off, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.NoticeSeen || status.Preview.Event != "heartbeat" {
		t.Fatalf("Update(enabled=false) = %+v", status)
	}
	if s := tel.load(); s.Enabled || s.NoticeSeen || s.ID != "kept-id" || !s.LastSent.Equal(lastSent) {
		t.Fatalf("state after Update(enabled=false) = %+v", s)
	}

	status, err = tel.Update(nil, &seen)
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || !status.NoticeSeen {
		t.Fatalf("Update(notice_seen=true) = %+v", status)
	}
	if s := tel.load(); s.Enabled || !s.NoticeSeen || s.ID != "kept-id" || !s.LastSent.Equal(lastSent) {
		t.Fatalf("state after Update(notice_seen=true) = %+v", s)
	}

	if _, err := tel.Update(nil, nil); err != nil {
		t.Fatal(err)
	}
	if s := tel.load(); s.Enabled || !s.NoticeSeen || s.ID != "kept-id" || !s.LastSent.Equal(lastSent) {
		t.Fatalf("state after an empty Update = %+v", s)
	}
}

func TestUpdateRewritesAMalformedState(t *testing.T) {
	tel, root := fixture(t, unused)
	write(t, root, StateFile, "{")
	seen := true

	if _, err := tel.Update(nil, &seen); err != nil {
		t.Fatal(err)
	}

	onDisk := stateOnDisk(t, root)
	if onDisk["enabled"] != false || onDisk["notice_seen"] != true {
		t.Fatalf("telemetry.json = %v, want disabled (it was malformed) and notice seen", onDisk)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

```bash
cd D:/clients/casaos/CasaOS && go test ./pkg/telemetry/ ; GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./pkg/telemetry/
```

Expected: both FAIL to compile the tests, `tel.Status undefined`, `tel.Update undefined`; `go test` ends with `[build failed]`. Linux/CI: `go test -race ./pkg/telemetry/ -run 'Status|Preview|Update' -v` fails with the same compile errors.

- [ ] **Step 3: Write the minimal implementation**

Create `pkg/telemetry/status.go`:

```go
package telemetry

// Status is what GET and PUT /v1/sys/telemetry answer in data.
type Status struct {
	Enabled    bool    `json:"enabled"`
	NoticeSeen bool    `json:"notice_seen"`
	Preview    Preview `json:"preview"`
}

// Preview is the heartbeat as it would be sent now. It is built even when the
// statistics are off: it is local and sends nothing.
type Preview struct {
	Event      string         `json:"event"`
	Properties map[string]any `json:"properties"`
}

func (t *Telemetry) Status() Status {
	s := t.load()
	return Status{
		Enabled:    s.Enabled,
		NoticeSeen: s.NoticeSeen,
		Preview:    Preview{Event: "heartbeat", Properties: t.Properties()},
	}
}

// Update sets whichever of enabled and notice_seen is given, and nothing else;
// it rewrites a malformed telemetry.json. Turning the statistics off holds from
// the next check.
func (t *Telemetry) Update(enabled, noticeSeen *bool) (Status, error) {
	if _, err := t.modify(func(s *State) {
		if enabled != nil {
			s.Enabled = *enabled
		}
		if noticeSeen != nil {
			s.NoticeSeen = *noticeSeen
		}
	}); err != nil {
		return Status{}, err
	}
	return t.Status(), nil
}
```

- [ ] **Step 4: Run it and see it pass**

```bash
cd D:/clients/casaos/CasaOS && gofmt -l pkg/telemetry && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./pkg/telemetry/ && go test ./pkg/telemetry/
```

Expected: `gofmt` and `vet` print nothing. The native `go test` fails on the same four Linux-only tests as in Task 4 and nothing else: every test of `status_test.go` passes. Linux/CI: `go test -race ./pkg/telemetry/ -v` prints `ok  	github.com/ReCasaOS/CasaOS/pkg/telemetry`.

- [ ] **Step 5: Commit**

```bash
cd D:/clients/casaos/CasaOS && git add pkg/telemetry/status.go pkg/telemetry/status_test.go && git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(telemetry): state and preview for the API" -m "The preview is the heartbeat built by the sender's own function, even when the statistics are off; Update changes enabled and notice_seen and nothing else."
```

---

### Task 6: GET and PUT /v1/sys/telemetry, started with the core

**Files:**
- Create: `route/v1/telemetry.go`
- Test: `route/v1/telemetry_test.go`
- Modify: `route/v1.go` (insert after line 140, `v1SysGroup.GET("/entry", v1.GetSystemEntry)`)
- Modify: `route/v1_test.go` (import block lines 3-12; append after line 81)
- Modify: `main.go` (import after line 25 `"github.com/ReCasaOS/CasaOS/pkg/sqlite"`; after lines 104-106, the `if *versionFlag { return }` block of `main()`)

**Interfaces:**
- Consumes: `telemetry.Default`, `telemetry.New`, `telemetry.StateFile`, `telemetry.Status`, `(*Telemetry).Status`, `(*Telemetry).Update`, `(*Telemetry).ApplyOffMarker`, `(*Telemetry).Run` (Tasks 1-5); `model.Result`, `common_err.SUCCESS`, `common_err.CLIENT_ERROR`, `common_err.SERVICE_ERROR`, `common_err.GetMsg`.
- Produces:
  - `func GetTelemetry(ctx echo.Context) error`, `func PutTelemetry(ctx echo.Context) error` in package `v1`.
  - Routes `GET /v1/sys/telemetry`, `PUT /v1/sys/telemetry` in the JWT-protected `v1SysGroup` (the gateway already forwards `/v1/sys`).
  - HTTP: `200 {"success":200,"message":"ok","data":{"enabled":bool,"notice_seen":bool,"preview":{"event":"heartbeat","properties":{...}}}}`; a body that does not decode (e.g. `{"enabled":"yes"}`) → `400 {"success":400,"message":"..."}`; a state that cannot be saved → `500 {"success":500,"message":"..."}`; no token → `401`.
  - Startup: `telemetry.Default.ApplyOffMarker()` first in `main()`, then `go telemetry.Default.Run(context.Background())`; if the marker cannot be folded, the loop is not started.

- [ ] **Step 1: Write the failing tests**

Create `route/v1/telemetry_test.go`:

```go
package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/pkg/telemetry"
	"github.com/labstack/echo/v4"
)

// useTelemetryFixture points the handlers at a box under a temporary root,
// whose distribution is v0.5.0. Nothing is ever sent from here.
func useTelemetryFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	release := filepath.Join(root, common.FORK_RELEASE_FILE)
	if err := os.MkdirAll(filepath.Dir(release), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, []byte("v0.5.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture := telemetry.New(root)
	fixture.Command = func(string, ...string) ([]byte, error) { return []byte("none\n"), nil }
	original := telemetry.Default
	telemetry.Default = fixture
	t.Cleanup(func() { telemetry.Default = original })
	return root
}

func callTelemetry(t *testing.T, method, body string) (int, telemetry.Status) {
	t.Helper()
	request := httptest.NewRequest(method, "/v1/sys/telemetry", strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	context := echo.New().NewContext(request, recorder)
	handler := GetTelemetry
	if method == http.MethodPut {
		handler = PutTelemetry
	}
	if err := handler(context); err != nil {
		t.Fatalf("%s /v1/sys/telemetry: %v", method, err)
	}
	var response struct {
		Success int              `json:"success"`
		Data    telemetry.Status `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v\n%s", err, recorder.Body.String())
	}
	if response.Success != recorder.Code {
		t.Fatalf("success = %d, status = %d", response.Success, recorder.Code)
	}
	return recorder.Code, response.Data
}

func telemetryStateOnDisk(t *testing.T, root string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, telemetry.StateFile))
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("telemetry.json is not JSON: %v\n%s", err, data)
	}
	return state
}

func TestGetTelemetryAnswersTheStateAndAPreview(t *testing.T) {
	useTelemetryFixture(t)

	code, status := callTelemetry(t, http.MethodGet, "")

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !status.Enabled || status.NoticeSeen {
		t.Fatalf("data = %+v, want enabled and notice not seen with no telemetry.json", status)
	}
	properties := status.Preview.Properties
	if status.Preview.Event != "heartbeat" || properties["distribution"] != "v0.5.0" || properties["$lib"] != "recasaos-core" {
		t.Fatalf("preview = %+v, want the heartbeat of v0.5.0", status.Preview)
	}
}

func TestPutTelemetryChangesEnabledAndNoticeSeenAndNothingElse(t *testing.T) {
	root := useTelemetryFixture(t)
	if err := os.WriteFile(filepath.Join(root, telemetry.StateFile), []byte(`{"enabled":true,"id":"kept-id","last_sent":"2026-09-21T10:00:00Z","notice_seen":false}`), 0o600); err != nil {
		t.Fatal(err)
	}

	code, status := callTelemetry(t, http.MethodPut, `{"enabled":false}`)
	if code != http.StatusOK || status.Enabled || status.NoticeSeen || status.Preview.Event != "heartbeat" {
		t.Fatalf("PUT enabled=false: %d %+v", code, status)
	}
	want := map[string]any{"enabled": false, "id": "kept-id", "last_sent": "2026-09-21T10:00:00Z", "notice_seen": false}
	if got := telemetryStateOnDisk(t, root); !reflect.DeepEqual(got, want) {
		t.Fatalf("telemetry.json = %v, want %v", got, want)
	}

	code, status = callTelemetry(t, http.MethodPut, `{"notice_seen":true,"something_else":1}`)
	if code != http.StatusOK || status.Enabled || !status.NoticeSeen {
		t.Fatalf("PUT notice_seen=true: %d %+v", code, status)
	}
	want["notice_seen"] = true
	if got := telemetryStateOnDisk(t, root); !reflect.DeepEqual(got, want) {
		t.Fatalf("telemetry.json = %v, want %v", got, want)
	}
}

func TestPutTelemetryRefusesABodyThatIsNotTheState(t *testing.T) {
	root := useTelemetryFixture(t)

	code, _ := callTelemetry(t, http.MethodPut, `{"enabled":"yes"}`)

	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	if _, err := os.Stat(filepath.Join(root, telemetry.StateFile)); !os.IsNotExist(err) {
		t.Fatalf("telemetry.json was written: %v", err)
	}
}
```

In `route/v1_test.go`, replace the import block (lines 3-12) with:

```go
import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/labstack/echo/v4"
)
```

and append at the end of the file (after line 81):

```go

func TestTheTelemetryRoutesAreSysRoutesBehindTheToken(t *testing.T) {
	e, ok := InitV1Router().(*echo.Echo)
	if !ok {
		t.Fatal("InitV1Router() is not an *echo.Echo")
	}
	registered := map[string]bool{}
	for _, r := range e.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if !registered[method+" /v1/sys/telemetry"] {
			t.Errorf("%s /v1/sys/telemetry is not registered", method)
		}
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, httptest.NewRequest(method, "/v1/sys/telemetry", nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s /v1/sys/telemetry without a token = %d, want 401", method, recorder.Code)
		}
	}
}
```

- [ ] **Step 2: Run them and see them fail**

```bash
cd D:/clients/casaos/CasaOS && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./route/ ./route/v1/
```

Expected: FAIL to type-check `route/v1`, `undefined: GetTelemetry`, `undefined: PutTelemetry` (vet stops at that package). Linux/CI: `go test -race ./route/ ./route/v1/ -run 'Telemetry' -v` fails the same way; once the handlers exist and before Step 4 registers them, `TestTheTelemetryRoutesAreSysRoutesBehindTheToken` fails with `GET /v1/sys/telemetry is not registered`.

- [ ] **Step 3: Write the handlers**

Create `route/v1/telemetry.go`:

```go
package v1

import (
	"net/http"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/telemetry"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/labstack/echo/v4"
)

// GetTelemetry answers whether the anonymous statistics are on, whether the
// dashboard's notice was seen, and the heartbeat exactly as it would be sent now.
//
// @Summary anonymous statistics: state and preview
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/telemetry [get]
func GetTelemetry(ctx echo.Context) error {
	return ctx.JSON(http.StatusOK, model.Result{
		Success: common_err.SUCCESS,
		Message: common_err.GetMsg(common_err.SUCCESS),
		Data:    telemetry.Default.Status(),
	})
}

// PutTelemetry sets enabled and notice_seen, each optional; other fields are
// ignored. It answers the new state, as GET does.
//
// @Summary anonymous statistics: turn them on or off, mark the notice seen
// @Accept application/json
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/telemetry [put]
func PutTelemetry(ctx echo.Context) error {
	var body struct {
		Enabled    *bool `json:"enabled"`
		NoticeSeen *bool `json:"notice_seen"`
	}
	if err := ctx.Bind(&body); err != nil {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}
	status, err := telemetry.Default.Update(body.Enabled, body.NoticeSeen)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}
	return ctx.JSON(http.StatusOK, model.Result{
		Success: common_err.SUCCESS,
		Message: common_err.GetMsg(common_err.SUCCESS),
		Data:    status,
	})
}
```

- [ ] **Step 4: Register the routes and start the statistics with the core**

In `route/v1.go`, after line 140 (`v1SysGroup.GET("/entry", v1.GetSystemEntry)`), insert:

```go
			// Anonymous statistics: their state, the heartbeat as it would be
			// sent, and the switch (dashboard notice and settings).
			v1SysGroup.GET("/telemetry", v1.GetTelemetry)
			v1SysGroup.PUT("/telemetry", v1.PutTelemetry)
```

In `main.go`, after line 25 (`"github.com/ReCasaOS/CasaOS/pkg/sqlite"`), insert:

```go
	"github.com/ReCasaOS/CasaOS/pkg/telemetry"
```

In `main.go`, replace the start of `main()` (lines 103-106):

```go
func main() {
	if *versionFlag {
		return
	}
```

with:

```go
func main() {
	if *versionFlag {
		return
	}

	// The installer's --no-telemetry leaves a marker: fold it into
	// telemetry.json before the API serves and before the first check. If it
	// cannot be folded, the checks do not start: an opt-out is never lost.
	if err := telemetry.Default.ApplyOffMarker(); err != nil {
		logger.Error("anonymous statistics stay off: cannot apply the telemetry-off marker", zap.Error(err))
	} else {
		go telemetry.Default.Run(context.Background())
	}
```

(`context`, `logger` and `zap` are already imported by `main.go`.)

- [ ] **Step 5: Run everything and see it pass**

```bash
cd D:/clients/casaos/CasaOS && gofmt -l main.go route pkg/telemetry && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet . ./route/... ./pkg/telemetry/ && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./... && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./... && GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build ./...
```

Expected: no output, exit 0 (the three release architectures build). Linux/CI: `go test -race -failfast ./...` passes, including `ok  	github.com/ReCasaOS/CasaOS/pkg/telemetry`, `ok  	github.com/ReCasaOS/CasaOS/route` and `ok  	github.com/ReCasaOS/CasaOS/route/v1`.

- [ ] **Step 6: Commit**

```bash
cd D:/clients/casaos/CasaOS && git add route/v1/telemetry.go route/v1/telemetry_test.go route/v1.go route/v1_test.go main.go && git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(telemetry): GET and PUT /v1/sys/telemetry, started with the core" -m "The dashboard reads the state and the exact heartbeat, and turns the statistics off or marks its notice seen. At startup the core folds the installer's telemetry-off marker, then starts the hourly checks."
```

---

## After the tasks

The implementer stops at the six commits: no push, no tag. The owner opens the pull request to `main` (CI's `codecov.yml` runs the Linux tests there), releases the core under its own tag, and the installer plan pins that tag; its install check (`--no-telemetry`, then a local capture server with `CASAOS_TELEMETRY_ENDPOINT` and `CASAOS_TELEMETRY_START_DELAY=0s`) proves the chain end to end.

## Spec coverage

| Spec requirement (core) | Task |
|---|---|
| UUID v4 id in `telemetry.json`, generated on first use, root 0600 | 1 (save 0600), 4 (`TestTheFirstCheckMakesAndKeepsTheID`) |
| Missing file = enabled / never sent / notice not seen; malformed = disabled; next PUT rewrites | 1, 5 (`TestUpdateRewritesAMalformedState`) |
| `telemetry-off` folded at startup: enabled false, id kept, marker deleted | 1 (`ApplyOffMarker`), 6 (`main.go`) |
| Properties table: distribution, core, arch (+GOARM), os, kernel, virtualization, model (+placeholders, 64 chars), docker, cpu_cores, ram_gb buckets, disks, storage_tb buckets, raid | 2 |
| `$process_person_profile: false`, `$lib: "recasaos-core"`, no `$geoip_disable` | 2, 3 (`TestTheRequestIsWhatPostHogExpects`) |
| Request: POST, JSON, `api_key`/`event`/`distinct_id`/`timestamp`/`properties`, endpoint and key | 3 |
| Timeout 10 s, proxy from environment, non-2xx and transport errors fail | 3 |
| `CASAOS_TELEMETRY_ENDPOINT` | 1 (`New`), 3 (test) |
| Start delay 0-60 min or `CASAOS_TELEMETRY_START_DELAY`, then hourly | 4 (`Run`, `startDelay`) |
| Check step 1: off → delete `upgraded-from`, nothing built, nothing sent | 4 (`TestNothingIsBuiltOrSentWhenStatisticsAreOff`) |
| Check step 2: `version_changed` with `previous_distribution`, delete after success, keep on failure | 4 |
| Check step 3: heartbeat when `last_sent` absent or ≥ 24 h (a future `last_sent` waits, as written), `last_sent` updated only after success | 4 |
| `ram_gb` bucket, sent as the JSON string `"8"` (because of `"128+"`) | 2 (`TestPropertiesOfABox`, `TestRAMIsRoundedToTheNearestSize`) |
| Failures logged at Info with event name and error, never queued | 4 |
| One function builds the properties for sender and preview | 2 (`Properties`), 5 (`TestThePreviewIsWhatIsSent`) |
| `GET /v1/sys/telemetry` → enabled, notice_seen, preview (built when off) | 5, 6 |
| `PUT /v1/sys/telemetry` → changes enabled and notice_seen and nothing else, answers the new state | 5, 6 |
| Admin token required, like every core route | 6 (`TestTheTelemetryRoutesAreSysRoutesBehindTheToken`) |
| Test-only variables documented as such | 1 (package doc) |
| No CI step runs with statistics on and the default endpoint | 3-6 (every sending test uses `httptest`; handlers never send) |
| README / installer / dashboard / PostHog setup / CHANGELOG | Out of this repository: CasaOS-Install and CasaOS-UI plans |
