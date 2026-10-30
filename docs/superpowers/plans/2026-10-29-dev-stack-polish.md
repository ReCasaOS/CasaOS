# Local process stack polish Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** On the unprivileged local stack, Files opens `.dev-runtime/data`, leftover mock JWTs are dropped, rclone is optional, and AppManagement plus CasaOS core stats write under `$DEVROOT/lib` instead of `/var/lib/casaos`.

**Architecture:** Relocate via small production hooks. CasaOS exposes `GET /v1/file/roots` and reads `CASAOS_RCLONE_SOCKET`; core stats `Dir` follows `DBPath`. AppManagement gains `[app] DataPath`. `stack.sh` optionally runs `rclone rcd` and always points children at the stack socket. The dashboard follows the first managed root and clears `mock-access` / `mock-refresh` when mock mode is off.

**Tech Stack:** Go (as in each repo's `go.mod`), bash, Vue 3 / vitest already in CasaOS-UI. No new Go or npm dependencies.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-10-29-dev-stack-polish-design.md`
- Work in the existing checkouts (`/home/ikanat/GIT/CasaOS`, `CasaOS-UI`, `CasaOS-AppManagement`). Sibling `../CasaOS-*` paths. Do not use a git worktree.
- Linux for `stack.sh` and `route/v1` file-roots tests (`//go:build linux`). Other `go test` and vitest run anywhere.
- No root. Do not create `/DATA` or `/var/lib/casaos` on the host. No user namespace or bind mount.
- `ManagementRoots` stays `$DEVROOT/data`. Ports, UID 0 refusal, Gateway, and `LoadConfig` stay as layer B.
- `gitAppsDataRoot=/DATA/AppData` and `osUpgradeBusyPath` stay. LocalStorage `mkdir /DATA` stays.
- No new npm or Go module dependencies.
- Layer C is out of scope.
- Do not commit unless the user asks. Skip every Commit step.

## File map

| File | Responsibility |
|---|---|
| CasaOS `route/v1/file_roots.go` | `GetFileRoots` + existing confinement helpers |
| CasaOS `route/v1.go` | register `GET /v1/file/roots` |
| CasaOS `pkg/utils/httper/drive.go` | `UseRcloneSocketFromEnv` |
| CasaOS `main.go` | call it; `hostStatsConfig.Dir` from `DBPath` |
| CasaOS `dev/writeconf.sh`, `dev/stack.sh` | `DataPath`; rclone spawn and env |
| AppManagement `model/sys_common.go`, `pkg/config/init.go` | `DataPath` field and default |
| AppManagement `service/datapath.go` | `ApplyDataPath` |
| AppManagement `route/v1/docker.go`, `main.go` | `DockerRootDirFilePath`; call `ApplyDataPath` before recover/stats |
| CasaOS-UI `mock/session.js` | `clearMockSession` |
| CasaOS-UI `src/main.js` | call it when not mock |
| CasaOS-UI `src/service/file.js` | `getRoots` |
| CasaOS-UI `src/components/filebrowser/fileRoot.js` | `fileRootFrom` |
| CasaOS-UI `src/components/filebrowser/FilePanel.vue` | resolve root before listing |
| CasaOS-UI filebrowser sidebar / NewFolderModal | `/DATA` home → `filePanel.rootPath` |
| CasaOS-UI `src/components/filebrowser/sidebar/apiError.js` | `apiErrorMessage`, `emptyCloudOnError` |

---

### Task 1: GET /v1/file/roots

**Files:**
- Modify: `/home/ikanat/GIT/CasaOS/route/v1/file_roots.go`
- Modify: `/home/ikanat/GIT/CasaOS/route/v1.go` (inside `v1FileGroup`, after `/trash` routes)
- Test: `/home/ikanat/GIT/CasaOS/route/v1/file_roots_test.go` (`//go:build linux`; reuse `confinedBox` and `call`)

**Interfaces:**
- Consumes: `fileroots.Default.Paths()`
- Produces: `func GetFileRoots(ctx echo.Context) error` — JSON `{ success: 200, message: "ok", data: { roots: []string } }`. Unconfined or nil paths → `roots: []` (not `null`).

Work in `/home/ikanat/GIT/CasaOS`.

- [ ] **Step 1: Write the failing test**

Append to `route/v1/file_roots_test.go`:

```go
func TestGetFileRootsConfined(t *testing.T) {
	data, _ := confinedBox(t)
	rec := call(t, GetFileRoots, http.MethodGet, "/v1/file/roots", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Success int `json:"success"`
		Data    struct {
			Roots []string `json:"roots"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Success != common_err.SUCCESS {
		t.Fatalf("success %d", result.Success)
	}
	if len(result.Data.Roots) != 1 || result.Data.Roots[0] != data {
		t.Fatalf("roots = %#v, want [%q]", result.Data.Roots, data)
	}
}

func TestGetFileRootsUnconfined(t *testing.T) {
	previous := fileroots.Default
	fileroots.Default = nil
	t.Cleanup(func() { fileroots.Default = previous })
	rec := call(t, GetFileRoots, http.MethodGet, "/v1/file/roots", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Data struct {
			Roots []string `json:"roots"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.Roots == nil {
		t.Fatal("roots is null, want []")
	}
	if len(result.Data.Roots) != 0 {
		t.Fatalf("roots = %#v, want []", result.Data.Roots)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./route/v1/ -run 'TestGetFileRoots' -count=1`

Expected: FAIL, `GetFileRoots` undefined.

- [ ] **Step 3: Write minimal implementation**

Add to `route/v1/file_roots.go`:

```go
// GetFileRoots answers the configured [file] ManagementRoots. Empty: the
// file API is unconfined and the dashboard keeps /DATA.
func GetFileRoots(ctx echo.Context) error {
	roots := []string{}
	if fileroots.Default != nil {
		if p := fileroots.Default.Paths(); len(p) > 0 {
			roots = p
		}
	}
	return ctx.JSON(http.StatusOK, model.Result{
		Success: common_err.SUCCESS,
		Message: common_err.GetMsg(common_err.SUCCESS),
		Data:    map[string][]string{"roots": roots},
	})
}
```

In `route/v1.go`, inside the `v1FileGroup` block, after `v1FileGroup.DELETE("/trash/all", v1.EmptyTrash)`:

```go
			v1FileGroup.GET("/roots", v1.GetFileRoots)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./route/v1/ -run 'TestGetFileRoots|TestTheFileAPIStaysInItsRoots' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

Skip: do not commit unless the user asks.

---

### Task 2: rclone socket env and core stats Dir

**Files:**
- Modify: `/home/ikanat/GIT/CasaOS/pkg/utils/httper/drive.go`
- Modify: `/home/ikanat/GIT/CasaOS/pkg/utils/httper/drive_test.go`
- Modify: `/home/ikanat/GIT/CasaOS/main.go` (`config.InitSetup` then `httper.UseRcloneSocketFromEnv()`; `hostStatsConfig` `Dir`)
- Create: `/home/ikanat/GIT/CasaOS/main_hoststats_test.go` (package `main`)

**Interfaces:**
- Consumes: `CASAOS_RCLONE_SOCKET`, `config.AppInfo.DBPath`
- Produces:
  - `func UseRcloneSocketFromEnv()` — non-empty env assigns `rcloneSocket`; unset or `""` leaves `/var/run/rclone/rclone.sock`
  - `hostStatsConfig().Dir` = `filepath.Join(config.AppInfo.DBPath, "stats", "core")`

Work in `/home/ikanat/GIT/CasaOS`.

- [ ] **Step 1: Write the failing tests**

Append to `pkg/utils/httper/drive_test.go`:

```go
func TestUseRcloneSocketFromEnv(t *testing.T) {
	previous := rcloneSocket
	t.Cleanup(func() {
		rcloneSocket = previous
	})

	t.Setenv("CASAOS_RCLONE_SOCKET", "")
	rcloneSocket = "/var/run/rclone/rclone.sock"
	UseRcloneSocketFromEnv()
	if rcloneSocket != "/var/run/rclone/rclone.sock" {
		t.Fatalf("empty env: %q", rcloneSocket)
	}

	if err := os.Unsetenv("CASAOS_RCLONE_SOCKET"); err != nil {
		t.Fatal(err)
	}
	rcloneSocket = "/var/run/rclone/rclone.sock"
	UseRcloneSocketFromEnv()
	if rcloneSocket != "/var/run/rclone/rclone.sock" {
		t.Fatalf("unset: %q", rcloneSocket)
	}

	t.Setenv("CASAOS_RCLONE_SOCKET", "/tmp/x.sock")
	UseRcloneSocketFromEnv()
	if rcloneSocket != "/tmp/x.sock" {
		t.Fatalf("set: %q", rcloneSocket)
	}
}
```

Add `os` to the imports in `drive_test.go` if it is not already there.

Create `/home/ikanat/GIT/CasaOS/main_hoststats_test.go`:

```go
package main

import (
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/config"
)

func TestHostStatsDirFollowsDBPath(t *testing.T) {
	prev := config.AppInfo.DBPath
	t.Cleanup(func() { config.AppInfo.DBPath = prev })
	dir := t.TempDir()
	config.AppInfo.DBPath = dir
	got := hostStatsConfig()
	want := filepath.Join(dir, "stats", "core")
	if got.Dir != want {
		t.Fatalf("Dir = %q, want %q", got.Dir, want)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/utils/httper/ -run TestUseRcloneSocketFromEnv -count=1`

Expected: FAIL, `UseRcloneSocketFromEnv` undefined.

Run: `go test . -run TestHostStatsDirFollowsDBPath -count=1`

Expected: FAIL, `Dir` is still `/var/lib/casaos/stats/core`.

- [ ] **Step 3: Write minimal implementation**

In `pkg/utils/httper/drive.go`, add `os` to imports and:

```go
// UseRcloneSocketFromEnv points rclone's remote control at
// CASAOS_RCLONE_SOCKET when that env is set and non-empty.
func UseRcloneSocketFromEnv() {
	if p, ok := os.LookupEnv("CASAOS_RCLONE_SOCKET"); ok && p != "" {
		rcloneSocket = p
	}
}
```

In `main.go`, immediately after `config.InitSetup(*configFlag, _confSample)`:

```go
	httper.UseRcloneSocketFromEnv()
```

(`httper` is already imported as `github.com/ReCasaOS/CasaOS/pkg/utils/httper`, or add that import.)

In `hostStatsConfig`, change the `Dir` line:

```go
		Dir:   filepath.Join(config.AppInfo.DBPath, "stats", "core"),
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/utils/httper/ -count=1`

Expected: PASS (including `TestTheRepliesOfRcloneAreNotLogged`).

Run: `go test . -run TestHostStatsDirFollowsDBPath -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

Skip: do not commit unless the user asks.

---

### Task 3: AppManagement DataPath

**Files:**
- Modify: `/home/ikanat/GIT/CasaOS-AppManagement/model/sys_common.go` (`APPModel`)
- Modify: `/home/ikanat/GIT/CasaOS-AppManagement/pkg/config/init.go` (`AppInfo` default)
- Create: `/home/ikanat/GIT/CasaOS-AppManagement/service/datapath.go`
- Create: `/home/ikanat/GIT/CasaOS-AppManagement/service/datapath_test.go`
- Modify: `/home/ikanat/GIT/CasaOS-AppManagement/route/v1/docker.go` (`dockerRootDirFilePath` → exported var)
- Modify: `/home/ikanat/GIT/CasaOS-AppManagement/main.go` (call `ApplyDataPath` after `InitSetup`, before `LoadImageUpdates`)

**Interfaces:**
- Consumes: `config.AppInfo.DataPath`, `constants.DefaultDataPath` (`/var/lib/casaos`)
- Produces:
  - `model.APPModel.DataPath string`
  - `func ApplyDataPath(root string) string` — empty `root` → `constants.DefaultDataPath`; sets the six service vars; returns the resolved root
  - `var DockerRootDirFilePath string` in package `v1`, default `/var/lib/casaos/docker_root`

| Variable | Path under resolved root |
|---|---|
| `StatsDir` | `stats/app_management` |
| `gitAppsDir` | `git_apps` |
| `BackupLogPath` | `backups/runs.json` |
| `BackupHoldPath` | `backups/holding.json` |
| `BackupSchedulePath` | `backups/schedules.json` |
| `imageUpdateStatePath` | `image_updates.json` |

Do not change `gitAppsDataRoot` or `osUpgradeBusyPath`.

Work in `/home/ikanat/GIT/CasaOS-AppManagement`.

- [ ] **Step 1: Write the failing test**

Create `service/datapath_test.go`:

```go
package service

import (
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/constants"
)

func TestApplyDataPath(t *testing.T) {
	prevStats, prevGit, prevLog, prevHold, prevSched, prevImg := StatsDir, gitAppsDir, BackupLogPath, BackupHoldPath, BackupSchedulePath, imageUpdateStatePath
	t.Cleanup(func() {
		StatsDir, gitAppsDir, BackupLogPath, BackupHoldPath, BackupSchedulePath, imageUpdateStatePath = prevStats, prevGit, prevLog, prevHold, prevSched, prevImg
	})

	root := t.TempDir()
	got := ApplyDataPath(root)
	if got != root {
		t.Fatalf("ApplyDataPath = %q, want %q", got, root)
	}
	want := map[string]string{
		"StatsDir":             filepath.Join(root, "stats", "app_management"),
		"gitAppsDir":           filepath.Join(root, "git_apps"),
		"BackupLogPath":        filepath.Join(root, "backups", "runs.json"),
		"BackupHoldPath":       filepath.Join(root, "backups", "holding.json"),
		"BackupSchedulePath":   filepath.Join(root, "backups", "schedules.json"),
		"imageUpdateStatePath": filepath.Join(root, "image_updates.json"),
	}
	gotPaths := map[string]string{
		"StatsDir":             StatsDir,
		"gitAppsDir":           gitAppsDir,
		"BackupLogPath":        BackupLogPath,
		"BackupHoldPath":       BackupHoldPath,
		"BackupSchedulePath":   BackupSchedulePath,
		"imageUpdateStatePath": imageUpdateStatePath,
	}
	for name, w := range want {
		if gotPaths[name] != w {
			t.Errorf("%s = %q, want %q", name, gotPaths[name], w)
		}
	}
	if gitAppsDataRoot != "/DATA/AppData" {
		t.Errorf("gitAppsDataRoot changed: %q", gitAppsDataRoot)
	}
	if osUpgradeBusyPath != "/var/lib/casaos/os-upgrade/busy" {
		t.Errorf("osUpgradeBusyPath changed: %q", osUpgradeBusyPath)
	}
}

func TestApplyDataPathEmptyUsesDefault(t *testing.T) {
	prev := StatsDir
	t.Cleanup(func() { StatsDir = prev })
	got := ApplyDataPath("")
	if got != constants.DefaultDataPath {
		t.Fatalf("root = %q, want %q", got, constants.DefaultDataPath)
	}
	want := filepath.Join(constants.DefaultDataPath, "stats", "app_management")
	if StatsDir != want {
		t.Fatalf("StatsDir = %q, want %q", StatsDir, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./service/ -run TestApplyDataPath -count=1`

Expected: FAIL, `ApplyDataPath` undefined.

- [ ] **Step 3: Write minimal implementation**

In `model/sys_common.go`, add `DataPath string` to `APPModel` (after `AppsPath`).

In `pkg/config/init.go`, add to the `AppInfo` literal:

```go
		DataPath:     constants.DefaultDataPath,
```

(`constants` is already imported.)

Create `service/datapath.go`:

```go
package service

import (
	"path/filepath"

	"github.com/ReCasaOS/CasaOS-Common/utils/constants"
)

// ApplyDataPath points stats, git-app state, backups and image-update
// cache at root. Empty root is /var/lib/casaos. gitAppsDataRoot and
// os-upgrade/busy stay on the box paths.
func ApplyDataPath(root string) string {
	if root == "" {
		root = constants.DefaultDataPath
	}
	StatsDir = filepath.Join(root, "stats", "app_management")
	gitAppsDir = filepath.Join(root, "git_apps")
	BackupLogPath = filepath.Join(root, "backups", "runs.json")
	BackupHoldPath = filepath.Join(root, "backups", "holding.json")
	BackupSchedulePath = filepath.Join(root, "backups", "schedules.json")
	imageUpdateStatePath = filepath.Join(root, "image_updates.json")
	return root
}
```

In `route/v1/docker.go`, replace the const:

```go
var (
	DockerRootDirFilePath             = "/var/lib/casaos/docker_root"
	dockerDaemonConfigurationFilePath = "/etc/docker/daemon.json"
)
```

Leave `dockerDaemonConfigurationFilePath` as a `const`. Replace the three `dockerRootDirFilePath` uses in that file with `DockerRootDirFilePath`.

In `main.go`, immediately after `config.InitSetup(*configFlag, _confSample)` and `config.InitGlobal(*configFlag)`, before `service.NewService` / `LoadImageUpdates`:

```go
		root := service.ApplyDataPath(config.AppInfo.DataPath)
		v1.DockerRootDirFilePath = filepath.Join(root, "docker_root")
```

Package `v1` is `github.com/ReCasaOS/CasaOS-AppManagement/route/v1`. Import it if `main` does not already. `filepath` is already imported.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./service/ -run 'TestApplyDataPath|TestStartStats' -count=1`

Expected: PASS.

Run: `go test ./route/v1/ -count=1`

Expected: PASS (renamed var).

- [ ] **Step 5: Commit**

Skip: do not commit unless the user asks.

---

### Task 4: writeconf DataPath and stack rclone

**Files:**
- Modify: `/home/ikanat/GIT/CasaOS/dev/writeconf.sh` (`[app]` of `app-management.conf`)
- Modify: `/home/ikanat/GIT/CasaOS/dev/writeconf_test.go`
- Modify: `/home/ikanat/GIT/CasaOS/dev/stack.sh` (export env; optional rclone)
- Modify: `/home/ikanat/GIT/CasaOS/README.md` (Development: Files / rclone)

**Interfaces:**
- Consumes: Task 3 `DataPath`; Task 2 `CASAOS_RCLONE_SOCKET`
- Produces: `app-management.conf` contains `DataPath=$LIB`. Parent shell exports `CASAOS_RCLONE_SOCKET=$DEVROOT/run/rclone.sock` and `RCLONE_CONFIG=$DEVROOT/lib/rclone.conf` (overwrites the developer's values). Optional `rclone rcd` child; missing/fail/timeout: warning, continue. SIGTERM with the other PIDs.

Work in `/home/ikanat/GIT/CasaOS`.

- [ ] **Step 1: Write the failing test**

In `dev/writeconf_test.go`, after the LocalStorage asserts, add:

```go
	am, err := os.ReadFile(filepath.Join(root, "etc", "app-management.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(am), "DataPath="+lib) {
		t.Errorf("app-management.conf DataPath\n%s", am)
	}
```

(`lib` is already `filepath.Join(root, "lib")`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dev/ -count=1`

Expected: FAIL, `app-management.conf DataPath` missing.

- [ ] **Step 3: Write minimal implementation**

In `dev/writeconf.sh`, inside the `app-management.conf` `[app]` block, after `AppsPath=$LIB/apps`:

```
DataPath=$LIB
```

In `dev/stack.sh`, after `bash "$WRITECONF"` and before `PIDS=()`:

```bash
export CASAOS_RCLONE_SOCKET="$DEVROOT/run/rclone.sock"
export RCLONE_CONFIG="$DEVROOT/lib/rclone.conf"
```

After LocalStorage handling and before AppManagement, add:

```bash
if command -v rclone >/dev/null 2>&1; then
	(
		exec setsid rclone rcd --rc-unix-socket "$DEVROOT/run/rclone.sock" --rc-no-auth --config "$DEVROOT/lib/rclone.conf"
	) &
	PIDS+=("$!")
	rclone_pid="${PIDS[-1]}"
	rclone_ok=0
	deadline=$((SECONDS + 120))
	while (( SECONDS < deadline )); do
		if ! alive "$rclone_pid"; then
			echo "warning: rclone exited; continuing" >&2
			break
		fi
		if [[ -S "$DEVROOT/run/rclone.sock" || -e "$DEVROOT/run/rclone.sock" ]]; then
			rclone_ok=1
			break
		fi
		sleep 0.5
	done
	if [[ "$rclone_ok" -eq 0 ]] && alive "$rclone_pid"; then
		echo "warning: rclone did not create its socket; continuing" >&2
	fi
else
	echo "warning: rclone not on PATH; cloud drives will be empty" >&2
fi
```

Do not add rclone to `REQUIRED`. `stop` already signals every PID in `PIDS`.

In `README.md` Development, replace the sentence that Files may 403 because it opens `/DATA` with: the dashboard Files panel follows ManagementRoots (the stack's `.dev-runtime/data`). Add: rclone is optional; without it, Locations has no cloud drives.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./dev/ -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

Skip: do not commit unless the user asks.

---

### Task 5: clearMockSession

**Files:**
- Modify: `/home/ikanat/GIT/CasaOS-UI/mock/session.js`
- Modify: `/home/ikanat/GIT/CasaOS-UI/mock/session.spec.js`
- Modify: `/home/ikanat/GIT/CasaOS-UI/src/main.js` (before `app.mount`)

**Interfaces:**
- Consumes: `access_token` / `refresh_token` in `localStorage`
- Produces: `export function clearMockSession(storage = localStorage)` — if `access_token === 'mock-access'` or `refresh_token === 'mock-refresh'`, remove `access_token`, `refresh_token`, `expires_at`, `user`; otherwise no-op. `main.js` calls it when `process.env.VUE_APP_MOCK !== '1'`. `seedMockSession` stays behind `VUE_APP_MOCK === '1'`.

Work in `/home/ikanat/GIT/CasaOS-UI`.

- [ ] **Step 1: Write the failing test**

Append to `mock/session.spec.js` (keep the existing `memoryStorage` helper):

```js
import { seedMockSession, clearMockSession } from './session.js'

describe('clearMockSession', () => {
	it('removes the four keys when access_token is mock-access', () => {
		const storage = memoryStorage()
		seedMockSession(storage)
		clearMockSession(storage)
		expect(storage.getItem('access_token')).toBeNull()
		expect(storage.getItem('refresh_token')).toBeNull()
		expect(storage.getItem('expires_at')).toBeNull()
		expect(storage.getItem('user')).toBeNull()
	})

	it('removes the four keys when refresh_token is mock-refresh', () => {
		const storage = memoryStorage({
			access_token: 'other',
			refresh_token: 'mock-refresh',
			expires_at: '1',
			user: '{}',
		})
		clearMockSession(storage)
		expect(storage.getItem('access_token')).toBeNull()
		expect(storage.getItem('refresh_token')).toBeNull()
		expect(storage.getItem('expires_at')).toBeNull()
		expect(storage.getItem('user')).toBeNull()
	})

	it('leaves a real JWT alone', () => {
		const storage = memoryStorage({
			access_token: 'eyJhbGciOiJIUzI1NiJ9.e30.x',
			refresh_token: 'real-refresh',
			expires_at: '9',
			user: '{"id":1}',
		})
		clearMockSession(storage)
		expect(storage.getItem('access_token')).toBe('eyJhbGciOiJIUzI1NiJ9.e30.x')
		expect(storage.getItem('refresh_token')).toBe('real-refresh')
		expect(storage.getItem('expires_at')).toBe('9')
		expect(storage.getItem('user')).toBe('{"id":1}')
	})
})
```

Change the existing import line to include `clearMockSession` (do not duplicate the import).

- [ ] **Step 2: Run test to verify it fails**

Run: `pnpm exec vitest run mock/session.spec.js`

Expected: FAIL, `clearMockSession` is not exported.

- [ ] **Step 3: Write minimal implementation**

In `mock/session.js`:

```js
export function clearMockSession(storage = localStorage) {
	if (storage.getItem('access_token') !== 'mock-access' && storage.getItem('refresh_token') !== 'mock-refresh') {
		return
	}
	storage.removeItem('access_token')
	storage.removeItem('refresh_token')
	storage.removeItem('expires_at')
	storage.removeItem('user')
}
```

In `src/main.js`, change the import and the block before `app.mount`:

```js
import { seedMockSession, clearMockSession } from '../mock/session.js'
```

```js
if (process.env.VUE_APP_MOCK === '1') {
	seedMockSession()
} else {
	clearMockSession()
}
app.mount('#app')
```

- [ ] **Step 4: Run test to verify it passes**

Run: `pnpm exec vitest run mock/session.spec.js`

Expected: PASS.

- [ ] **Step 5: Commit**

Skip: do not commit unless the user asks.

---

### Task 6: Files root from ManagementRoots

**Files:**
- Create: `/home/ikanat/GIT/CasaOS-UI/src/components/filebrowser/fileRoot.js`
- Create: `/home/ikanat/GIT/CasaOS-UI/src/components/filebrowser/fileRoot.spec.js`
- Modify: `/home/ikanat/GIT/CasaOS-UI/src/service/file.js` (`getRoots`)
- Modify: `/home/ikanat/GIT/CasaOS-UI/src/components/filebrowser/FilePanel.vue`
- Modify: `/home/ikanat/GIT/CasaOS-UI/src/components/filebrowser/sidebar/TreeList.vue`
- Modify: `/home/ikanat/GIT/CasaOS-UI/src/components/filebrowser/sidebar/TreeListItem.vue`
- Modify: `/home/ikanat/GIT/CasaOS-UI/src/components/filebrowser/sidebar/MountList.vue` (`/DATA` home only; catch blocks are Task 7)
- Modify: `/home/ikanat/GIT/CasaOS-UI/src/components/filebrowser/modals/NewFolderModal.vue`

**Interfaces:**
- Consumes: Task 1 `GET /v1/file/roots` as `res.data.data.roots`
- Produces:
  - `export function fileRootFrom(roots)` — missing / not an array / empty → `'/DATA'`; otherwise `roots[0]`
  - `file.getRoots()` → `api.get('/file/roots')`
  - `FilePanel.rootPath` and `fileRootReady` after `resolveFileRoot`
  - filebrowser controls that treated `/DATA` as the files home use `filePanel.rootPath`

Merge, VM, and AppData `/DATA` paths stay.

Work in `/home/ikanat/GIT/CasaOS-UI`.

- [ ] **Step 1: Write the failing test**

Create `src/components/filebrowser/fileRoot.spec.js`:

```js
import { describe, expect, it } from 'vitest'
import { fileRootFrom, homeFolders } from './fileRoot.js'

describe('fileRootFrom', () => {
	it('returns /DATA when roots is missing, not an array, or empty', () => {
		expect(fileRootFrom()).toBe('/DATA')
		expect(fileRootFrom(null)).toBe('/DATA')
		expect(fileRootFrom({})).toBe('/DATA')
		expect(fileRootFrom([])).toBe('/DATA')
	})

	it('returns the first root', () => {
		expect(fileRootFrom(['/tmp/data'])).toBe('/tmp/data')
		expect(fileRootFrom(['/a', '/b'])).toBe('/a')
	})
})

describe('homeFolders', () => {
	it('prefixes children with the given root', () => {
		const folders = homeFolders('/tmp/data')
		expect(folders[0].path).toBe('/tmp/data')
		expect(folders[0].name).toBe('data')
		expect(folders.map(f => f.path)).toEqual([
			'/tmp/data',
			'/tmp/data/Documents',
			'/tmp/data/Downloads',
			'/tmp/data/Gallery',
			'/tmp/data/Media',
		])
	})
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `pnpm exec vitest run src/components/filebrowser/fileRoot.spec.js`

Expected: FAIL, cannot find `fileRoot.js` or `fileRootFrom`.

- [ ] **Step 3: Write minimal implementation**

Create `src/components/filebrowser/fileRoot.js`:

```js
export function fileRootFrom(roots) {
	if (!Array.isArray(roots) || roots.length === 0) {
		return '/DATA'
	}
	return roots[0]
}

export function homeFolders(root) {
	const name = root === '/DATA' ? 'DATA' : root.split('/').filter(Boolean).pop() || root
	return [
		{ name, icon: 'data-outline', pack: 'casa', path: root, visible: true, selected: true, extensions: null },
		{ name: 'Documents', icon: 'files-outline', pack: 'casa', path: `${root}/Documents`, visible: true, selected: true, extensions: null },
		{ name: 'Downloads', icon: 'downloads-outline', pack: 'casa', path: `${root}/Downloads`, visible: true, selected: true, extensions: null },
		{ name: 'Gallery', icon: 'gallery-outline', pack: 'casa', path: `${root}/Gallery`, visible: true, selected: true, extensions: null },
		{ name: 'Media', icon: 'media-outline', pack: 'casa', path: `${root}/Media`, visible: true, selected: true, extensions: null },
	]
}
```

In `src/service/file.js`, add:

```js
	getRoots() {
		return api.get(`${PREFIX}/roots`)
	},
```

In `FilePanel.vue` script: import `fileRootFrom`. Add `fileRootReady: false` next to `rootPath: '/DATA'`. Add method:

```js
		async resolveFileRoot() {
			try {
				const res = await this.$api.file.getRoots()
				this.rootPath = fileRootFrom(res?.data?.data?.roots)
			} catch {
				this.rootPath = '/DATA'
			}
			this.fileRootReady = true
		},
```

Change `mounted` to `async mounted`. First lines:

```js
		await this.resolveFileRoot()
		this.$EventBus.$emit(events.RELOAD_FILE_LIST)
```

Then the existing `this.init()` / `#/files?path=` logic. `events` is already imported.

In `TreeList.vue`, import `homeFolders`. Replace the hardcoded `initFolders` array with a computed:

```js
		initFolders() {
			return homeFolders(this.filePanel.rootPath || '/DATA')
		},
```

Remove `initFolders` from `data()`. In `created`, do not call `getNewList` until `this.filePanel.fileRootReady`. Add a watch:

```js
	watch: {
		'filePanel.fileRootReady'(ready) {
			if (ready) {
				this.getNewList()
			}
		},
	},
```

In `created`, after shortcuts: `if (this.filePanel.fileRootReady) { this.getNewList() }`. In `mounted`, `this.dataList = [...this.initFolders, ...this.shortcutList]` still works (computed).

In `TreeListItem.vue` `isActived`, replace `this.item.path != '/DATA'` with `this.item.path != this.filePanel.rootPath`.

In `MountList.vue`:
- template `@click="filePanel.getFileList(filePanel.rootPath)"`
- `isActived`: `this.$store.state.currentPath === this.filePanel.rootPath`
- `goToDataFolder`: `this.filePanel.getFileList(this.filePanel.rootPath)`

In `NewFolderModal.vue`, `inject: ['filePanel']` and:

```js
		isRootorDATA() {
			return this.currentPath === '/' || this.currentPath === this.filePanel.rootPath
		},
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `pnpm exec vitest run src/components/filebrowser/fileRoot.spec.js`

Expected: PASS.

- [ ] **Step 5: Commit**

Skip: do not commit unless the user asks.

---

### Task 7: MountList errors and README

**Files:**
- Create: `/home/ikanat/GIT/CasaOS-UI/src/components/filebrowser/sidebar/apiError.js`
- Create: `/home/ikanat/GIT/CasaOS-UI/src/components/filebrowser/sidebar/apiError.spec.js`
- Modify: `/home/ikanat/GIT/CasaOS-UI/src/components/filebrowser/sidebar/MountList.vue` (all six `error.reponse` catches)
- Modify: `/home/ikanat/GIT/CasaOS-UI/README.md`

**Interfaces:**
- Consumes: thrown API errors
- Produces:
  - `export function apiErrorMessage(error)` — `error?.response?.message ?? error`; never throws
  - `export function emptyCloudOnError(error)` — logs `apiErrorMessage(error)`, returns `[]`
  - `getCloudStorage` catch: `this.cloudStorageList = emptyCloudOnError(error)`
  - other catches: `console.log(apiErrorMessage(error))`

Work in `/home/ikanat/GIT/CasaOS-UI`.

- [ ] **Step 1: Write the failing test**

Create `src/components/filebrowser/sidebar/apiError.spec.js`:

```js
import { describe, expect, it, vi } from 'vitest'
import { apiErrorMessage, emptyCloudOnError } from './apiError.js'

describe('apiErrorMessage', () => {
	it('does not throw on {} and reads response.message', () => {
		expect(() => apiErrorMessage({})).not.toThrow()
		expect(apiErrorMessage({})).toEqual({})
		expect(apiErrorMessage({ response: { message: 'x' } })).toBe('x')
	})
})

describe('emptyCloudOnError', () => {
	it('returns [] and does not throw', () => {
		const log = vi.spyOn(console, 'log').mockImplementation(() => {})
		expect(emptyCloudOnError({})).toEqual([])
		expect(emptyCloudOnError({ response: { message: 'down' } })).toEqual([])
		log.mockRestore()
	})
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `pnpm exec vitest run src/components/filebrowser/sidebar/apiError.spec.js`

Expected: FAIL, cannot find `apiError.js`.

- [ ] **Step 3: Write minimal implementation**

Create `src/components/filebrowser/sidebar/apiError.js`:

```js
export function apiErrorMessage(error) {
	return error?.response?.message ?? error
}

export function emptyCloudOnError(error) {
	console.log(apiErrorMessage(error))
	return []
}
```

In `MountList.vue`, import those two. Replace every `console.log(error.reponse.message)` with `console.log(apiErrorMessage(error))`. In `getCloudStorage` catch:

```js
			} catch (error) {
				this.cloudStorageList = emptyCloudOnError(error)
			}
```

In `README.md` Development, replace the Files `/DATA` 403 sentence with: Files follows the managed roots (CasaOS `.dev-runtime/data` on the local stack). rclone is optional; without it, Locations has no cloud drives.

- [ ] **Step 4: Run tests to verify they pass**

Run: `pnpm exec vitest run src/components/filebrowser mock/session.spec.js`

Expected: PASS.

Run: `pnpm lint` (or the repo's existing lint script).

Expected: 0 errors.

- [ ] **Step 5: Commit**

Skip: do not commit unless the user asks.

---

## Self-review (against the spec)

| Spec item | Task |
|---|---|
| `GET /v1/file/roots`, empty `[]` | 1 |
| FilePanel + `fileRootFrom` + filebrowser `/DATA` home | 6 |
| `clearMockSession` / `main.js` | 5 |
| `UseRcloneSocketFromEnv` + CasaOS `main` | 2 |
| `hostStatsConfig` Dir from `DBPath` | 2 |
| `DataPath` + `ApplyDataPath` + `DockerRootDirFilePath` | 3 |
| writeconf `DataPath=$LIB` | 4 |
| stack rclone spawn, env, warnings, SIGTERM | 4 |
| MountList `apiErrorMessage` / empty cloud list | 7 |
| READMEs | 4 (CasaOS), 7 (UI) |
| No `stack.sh` unit test | 4 (manual only) |
| `gitAppsDataRoot` / `os-upgrade` unchanged | 3 asserts |
| Layer C out | no task |

## Verification (after all tasks)

```sh
# CasaOS
go test ./dev/ ./pkg/utils/httper/ ./route/v1/ -count=1
go test . -run TestHostStatsDirFollowsDBPath -count=1

# CasaOS-AppManagement
go test ./service/ ./pkg/config/ ./route/v1/ -count=1

# CasaOS-UI
pnpm exec vitest run mock/session.spec.js src/components/filebrowser
pnpm lint
```

Then `./dev/stack.sh` or `pnpm dev:stack`: Files lists `$DEVROOT/data` without 403; leftover `mock-access` is gone after reload; rclone absent → Locations has no overlay.
