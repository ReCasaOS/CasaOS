# Local process stack polish — design

Status: approved in conversation on 2026-10-29, section by section.

This is polish on layer B (`docs/superpowers/specs/2026-10-28-dev-stack-design.md`). Layer C (Go fakes for Docker and mergerfs) is a later spec.

## Goal

A developer on the unprivileged local stack can open Files, switch from `pnpm dev:mock` to `pnpm dev:stack` without leftover mock tokens, and not see a cloud-storage overlay when rclone is absent. AppManagement and CasaOS core stats write under the runtime tree, not `/var/lib/casaos`.

`./dev/stack.sh` and `pnpm dev:stack` stay the commands. The stack stays unprivileged, loopback-only, and confined by `[file] ManagementRoots`.

## Decisions

- Relocate and add small production hooks. Do not create `/DATA` or `/var/lib/casaos` on the host, and do not use a user namespace or bind mount.
- `ManagementRoots` stays the absolute `$DEVROOT/data`. The dashboard Files panel follows the first managed root instead of hard-coding `/DATA`.
- Mock JWT constants (`mock-access` / `mock-refresh`) are cleared when mock mode is off. A real token is not touched.
- rclone is optional, like LocalStorage. The stack never uses the host `/var/run/rclone/rclone.sock` or the developer's `RCLONE_CONFIG`.
- AppManagement gains `[app] DataPath` (default `/var/lib/casaos`). CasaOS core stats use the existing `DBPath` (`…/stats/core`). Production paths do not change when those keys keep their defaults.
- LocalStorage `mkdir /DATA` stays as today (log and continue). `gitAppsDataRoot=/DATA/AppData` and `os-upgrade/busy` stay for layer C / the box.
- No new npm dependency. TDD. No commit unless asked.

## Architecture

```
./dev/stack.sh
        │
        ├─ writeconf  (DataPath=$LIB unchanged ManagementRoots=$DATA)
        ├─ rclone rcd (optional) → $DEVROOT/run/rclone.sock
        │     RCLONE_CONFIG=$DEVROOT/lib/rclone.conf
        └─ Go services (unchanged spawn order)
              CasaOS: CASAOS_RCLONE_SOCKET=$DEVROOT/run/rclone.sock
                      host stats Dir=$DBPath/stats/core

pnpm dev:stack → Vue --mode stack
        Files rootPath = GET /v1/file/roots → roots[0] or /DATA
        clearMockSession() when VUE_APP_MOCK is not 1
```

Gateway, ports, UID 0 refusal, and sibling layout do not change.

## File roots

Authenticated `GET /v1/file/roots` on the existing `/v1/file` group (JWT, same as the other file routes). Gateway already registers `/v1/file`.

```
{ "success": 200, "message": "ok", "data": { "roots": ["/abs/path"] } }
```

`roots` is `fileroots.Default.Paths()`. Unconfined (`Default` nil) or a refusing configuration with no paths: JSON `[]`, never `null`.

CasaOS-UI:

- `src/service/file.js` gains `getRoots()`.
- `fileRootFrom(roots)` in `src/components/filebrowser/fileRoot.js`: missing, not an array, or empty → `/DATA`; otherwise `roots[0]`.
- `FilePanel` (filebrowser) calls `getRoots()` before the first listing and sets `rootPath`. Request failure → `/DATA` (correct on a box; on the stack Vue starts after CasaOS is ready, so the list is expected).
- A `#/files?path=` query still wins for that visit.
- Other filebrowser controls that treat `/DATA` as the files home use that same `rootPath` (MountList `getFileList`, the disk-added reload, NewFolderModal's "at root" check, TreeListItem's root comparison). Merge, VM, and AppData paths that are `/DATA` on a box stay `/DATA`.

Production with empty `ManagementRoots` keeps opening `/DATA`.

## Mock session

`mock/session.js` exports `clearMockSession(storage = localStorage)`:

- If `access_token` is `mock-access` or `refresh_token` is `mock-refresh`, remove `access_token`, `refresh_token`, `expires_at`, and `user`.
- Otherwise do nothing.

`src/main.js` calls it when `process.env.VUE_APP_MOCK !== '1'`, before `app.mount`, so the router does not send a mock JWT. `seedMockSession` stays behind `VUE_APP_MOCK === '1'`.

## rclone

CasaOS `pkg/utils/httper`: `UseRcloneSocketFromEnv()` — if `CASAOS_RCLONE_SOCKET` is set and non-empty, assign `rcloneSocket`; otherwise leave the default `/var/run/rclone/rclone.sock`. CasaOS `main` calls it after `config.InitSetup`. Existing drive tests still assign `rcloneSocket` directly.

`dev/stack.sh`:

1. Always export `CASAOS_RCLONE_SOCKET=$DEVROOT/run/rclone.sock` and `RCLONE_CONFIG=$DEVROOT/lib/rclone.conf` for children (overwrite a value already in the developer's shell).
2. If `rclone` is on `PATH`, spawn  
   `rclone rcd --rc-unix-socket $DEVROOT/run/rclone.sock --rc-no-auth --config $DEVROOT/lib/rclone.conf`  
   as another child (same `setsid` pattern). Wait until the socket path exists, 120s. Record the PID.
3. Missing binary, non-zero exit, or timeout: one warning, continue. The env vars stay pointed at the stack socket.
4. SIGINT/SIGTERM: rclone is signalled with the other children.

Unix socket permissions follow rclone's default; the directory `$DEVROOT/run` is already 0755 and owned by the developer. `--rc-no-auth` on that socket is the same trust as `internal.secret`.

## AppManagement DataPath

`model.APPModel` gains `DataPath string`. `pkg/config` default is `constants.DefaultDataPath` (`/var/lib/casaos`). The sample conf may omit the key.

`service.ApplyDataPath(root string)`: empty `root` → `constants.DefaultDataPath`. Then:

| Variable | Path under DataPath |
|---|---|
| `StatsDir` | `stats/app_management` |
| `gitAppsDir` | `git_apps` |
| `BackupLogPath` | `backups/runs.json` |
| `BackupHoldPath` | `backups/holding.json` |
| `BackupSchedulePath` | `backups/schedules.json` |
| `imageUpdateStatePath` | `image_updates.json` |

`route/v1` `dockerRootDirFilePath` becomes the exported variable `DockerRootDirFilePath`. `main` after `InitSetup` calls `service.ApplyDataPath(config.AppInfo.DataPath)` and sets `v1.DockerRootDirFilePath` to `filepath.Join(dataPath, "docker_root")` with the same resolved root `ApplyDataPath` used.

Not moved: `gitAppsDataRoot` (`/DATA/AppData`), `osUpgradeBusyPath` (`/var/lib/casaos/os-upgrade/busy`).

`writeconf.sh` adds `DataPath=$LIB` under `[app]` in `app-management.conf`.

Existing tests that assign `StatsDir` (and the other vars) after import still win for that test.

## CasaOS core stats

`hostStatsConfig` `Dir` is `filepath.Join(config.AppInfo.DBPath, "stats", "core")`. Production `DBPath` is `/var/lib/casaos`, so the directory is unchanged. The stack already sets `DBPath=$DEVROOT/lib`.

CasaOS `alerts.json`, `fork-release`, and OS-module paths stay `/var/lib/casaos/...`. GET alerts already falls back to defaults when the file is missing. Saving alerts on the stack can still fail; that is out of this spec.

## MountList

Every `catch` in `src/components/filebrowser/sidebar/MountList.vue` uses optional chaining (`error?.response?.message ?? error`) and does not throw. `getCloudStorage` on failure leaves `cloudStorageList` as `[]`. No toast and no overlay.

Helper `apiErrorMessage(error)` in `src/components/filebrowser/sidebar/apiError.js` so the catch is testable without mounting the sidebar.

## Error handling

- rclone missing / `rcd` exits / socket absent after 120s: warning, stack continues, cloud list empty.
- `GET /v1/file/roots` fails or `roots=[]`: FilePanel `/DATA`.
- `DataPath` omitted: AppManagement `/var/lib/casaos` (box).
- `clearMockSession`: only the mock constants.
- LocalStorage `/DATA` mkdir: existing log and continue.
- `StartStats` cannot mkdir under DataPath: existing 503, process stays up.

## Tests

TDD. No unit test for `stack.sh` or `dev/stack.mjs`.

| Place | Case | Assert |
|---|---|---|
| CasaOS `GET /v1/file/roots` | confined | `data.roots` is the configured paths |
| same | unconfined (`Default` nil) | `data.roots` is `[]` (not null) |
| CasaOS `httper.UseRcloneSocketFromEnv` | env unset or `""` | `rcloneSocket` stays `/var/run/rclone/rclone.sock` |
| same | `CASAOS_RCLONE_SOCKET=/tmp/x.sock` | that path |
| CasaOS `hostStatsConfig` | `DBPath` a temp dir | `Dir` is `temp/stats/core` |
| AppManagement `ApplyDataPath` | `DataPath=temp` | the six paths in the table above |
| same | empty string | paths under `/var/lib/casaos` |
| CasaOS `dev/writeconf_test.go` | | `app-management.conf` contains `DataPath=` the temp `lib`; `ManagementRoots=` the temp `data` |
| UI `clearMockSession` | `mock-access` or `mock-refresh` | the four keys are gone |
| same | a real JWT | unchanged |
| UI `fileRootFrom` | `[]`, missing, not an array | `/DATA` |
| same | `['/tmp/data']` | `/tmp/data` |
| same | two roots | `roots[0]` |
| UI `apiErrorMessage` | `{}` or `{ response: { message: 'x' } }` | no throw; `'x'` when present |
| UI MountList `getCloudStorage` | API reject | no throw, `cloudStorageList` `[]` |

Existing Gateway `./common/` tests, `go test ./dev/` in CasaOS, AppManagement `StartStats` tests, httper drive tests, and UI `seedMockSession` tests still pass.

## Documentation

CasaOS and CasaOS-UI READMEs: drop the sentence that Files may 403 because it opens `/DATA`. Say Files follows the managed roots (the stack's `.dev-runtime/data`). Note rclone is optional on the stack; without it, Locations has no cloud drives.

## Files

| Path | Action |
|---|---|
| CasaOS `route/v1.go`, `route/v1/file_roots.go` (handler) | `GET /v1/file/roots` |
| CasaOS `route/v1/file_roots_test.go` or a sibling test | roots cases |
| CasaOS `pkg/utils/httper/drive.go` | `UseRcloneSocketFromEnv` |
| CasaOS `pkg/utils/httper/drive_test.go` | env cases |
| CasaOS `main.go` | call it; `hostStatsConfig` Dir from `DBPath` |
| CasaOS `dev/writeconf.sh`, `dev/writeconf_test.go` | `DataPath` |
| CasaOS `dev/stack.sh` | rclone spawn, env |
| CasaOS `README.md` | Files / rclone |
| CasaOS-AppManagement `model/sys_common.go`, `pkg/config/init.go` | `DataPath` |
| CasaOS-AppManagement `service` (ApplyDataPath + vars) | relocate |
| CasaOS-AppManagement `route/v1/docker.go`, `main.go` | `DockerRootDirFilePath` |
| CasaOS-AppManagement ApplyDataPath test | the table |
| CasaOS-UI `mock/session.js`, `mock/session.spec.js` | `clearMockSession` |
| CasaOS-UI `src/main.js` | call it when not mock |
| CasaOS-UI `src/service/file.js` | `getRoots` |
| CasaOS-UI `src/components/filebrowser/fileRoot.js` + spec | `fileRootFrom` |
| CasaOS-UI `src/components/filebrowser/FilePanel.vue` | use it |
| CasaOS-UI `src/components/filebrowser/sidebar/apiError.js` + spec | `apiErrorMessage` |
| CasaOS-UI `src/components/filebrowser/sidebar/MountList.vue` | catch + empty list |
| CasaOS-UI `README.md` | Files / rclone |

## Out of scope

- Layer C: fake Docker, fake mergerfs, isolating the host Docker socket, a real `/DATA` mount
- User namespace / bind-mount of `/DATA` or `/var/lib/casaos`
- LocalStorage default directories under `/DATA`
- `gitAppsDataRoot`, `os-upgrade/busy`, CasaOS `alerts.json` / `fork-release` / modules
- Changing Gateway, ports, UID 0 refusal, or `ManagementRoots`
- Auto-creating the first account
- macOS and Windows
- New npm packages
- Committing `.dev-runtime`

## Verification (after implementation)

On Linux, not root, sibling checkouts:

```sh
# CasaOS
go test ./dev/ ./pkg/utils/httper/ ./route/v1/   # plus the host-stats test package

# CasaOS-AppManagement
go test ./service/ ./pkg/config/

# CasaOS-UI
pnpm exec vitest run mock/session.spec.js src/components/filebrowser
pnpm lint
```

Then `./dev/stack.sh` (or `pnpm dev:stack`): Files lists `$DEVROOT/data` without 403; a leftover `mock-access` is gone after reload; with rclone absent, Locations has no overlay. `go generate ./...` remains a one-time prerequisite when `codegen/` is missing.
