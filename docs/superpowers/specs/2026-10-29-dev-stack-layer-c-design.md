# Local process stack layer C — design

Status: approved in conversation on 2026-10-29, section by section.

This is layer C of the three-layer development setup. Layer A is `pnpm dev:mock`. Layer B is `docs/superpowers/specs/2026-10-28-dev-stack-design.md`. Polish is `docs/superpowers/specs/2026-10-29-dev-stack-polish-design.md`. Combined README/CHANGELOG documentation for A+B+polish+C is a later pass, after this layer ships.

## Goal

A developer on the unprivileged local stack can open App Management and Storage without a host Docker daemon, without `/var/run/docker.sock`, and without mergerfs or FUSE. Compose install/list/start/stop persist as records. Disks come from a fixture. Merge stays off.

`./dev/stack.sh` and `pnpm dev:stack` stay the commands. The stack stays unprivileged, loopback-only, and confined by `[file] ManagementRoots`.

## Decisions

- Record-keeping Go fakes, not an isolated real Docker and not thin always-empty stubs.
- Never fall back to the host `/var/run/docker.sock` or a parent `DOCKER_HOST`.
- No mergerfs binary, no FUSE, no user-namespace bind of `/DATA`. `EnableMergerFS=false` stays.
- Production defaults do not change when the new env vars are unset.
- No new npm packages. TDD. No commit unless asked.

## Architecture

```
./dev/stack.sh
        │
        ├─ writeconf  (fake-disks.json, mkdir data/AppData)
        ├─ fake-docker  go run ./dev/fakedocker
        │     socket $DEVROOT/run/docker.sock
        │     data   $DEVROOT/lib/fake-docker
        └─ Go services (unchanged spawn order after the fake is listening)
              DOCKER_HOST=unix://$DEVROOT/run/docker.sock
              CASAOS_LSBLK_FILE=$DEVROOT/lib/fake-disks.json
              CASAOS_FAKE_STORAGE=1
              CASAOS_GIT_APPS_DATA=$DEVROOT/data/AppData

pnpm dev:stack → Vue --mode stack
        App Management talks to AppManagement → fake-docker
        Storage talks to LocalStorage → fixture lsblk
```

Gateway, ports, UID 0 refusal, sibling layout, rclone, file roots, DataPath, and ManagementRoots do not change.

## fake-docker

A `go run` program in the CasaOS module: `dev/fakedocker`. Flags: `-socket` (unix path) and `-data` (state directory). It is not a separate Go module.

HTTP over that unix socket, Docker Engine API dialect. API-Version `1.44` on `/_ping` (same as AppManagement’s negotiation test).

Contract (outcomes, not a frozen route list):

- `GET /_ping` → `OK`.
- Compose v5 `Up` of a one-service compose file with a named image (no `build:`) succeeds without pulling and without publishing host ports.
- That `Up` writes the compose YAML under `$data/<project>/`.
- Each service becomes one synthetic container in `running`.
- `List` / inspect / start / stop / `Down` see those records.
- Image inspect reports the named image as already present, so compose `Pull` does not touch the network.
- Bind mounts and networks are recorded, not created on the host.
- Any other Engine path: **501**.

Implementation grows the HTTP surface until a compose-v5 `Up` against the fake passes. That discovery test lives in CasaOS-AppManagement (it already has compose v5): `go run` the sibling `../CasaOS/dev/fakedocker` onto a temp socket, set `DOCKER_HOST`, call `Up`; skip if that directory is missing. CasaOS `go test ./dev/fakedocker/` speaks HTTP only, so CasaOS does not add the compose module.

State is JSON under `-data`. A new process on the same `-socket` and `-data` serves the same projects and containers.

No image pull, no containerd, no real published ports, no exec-in-container.

## LocalStorage hooks

When `CASAOS_LSBLK_FILE` is set and non-empty:

- `ExecLSBLK` returns that file’s bytes. Host `lsblk` is not run.
- `ExecLSBLKByPath(path)` returns a `{"blockdevices":[...]}` document containing only the matching device (compare `path` or `/dev/`+`name`). No match: empty `blockdevices` list, not a host call.

When the env is unset or empty, both functions run host `lsblk` as today.

When `CASAOS_FAKE_STORAGE=1`:

- `ExecuteCommand` for `mount`, `umount`, `parted`, `partprobe`, `e2label`, `sfdisk` returns `nil, nil` and does not exec.
- Other names still exec. `ExecuteCommand("lsblk", …)` is not redirected to the fixture (`lsblk --pairs` is a different format). Disk listing uses `ExecLSBLK` only.

When `CASAOS_FAKE_STORAGE` is unset, those commands exec as today.

`EnableMergerFS=false` stays in `local-storage.conf`. Merge is not mounted. “Merge” on the stack means directories under `$DEVROOT/data` if a caller creates them; there is no FUSE device. LocalStorage `mkdir /DATA` on start stays log-and-continue.

## gitAppsDataRoot

`CASAOS_GIT_APPS_DATA` non-empty → `gitAppsDataRoot` is that absolute path. Unset or empty → `/DATA/AppData`.

`ApplyDataPath` still does not touch `gitAppsDataRoot` (existing test). A separate `ApplyGitAppsDataRoot(v string)` is called from AppManagement `main` after `ApplyDataPath`.

`writeconf` / `stack.sh` create `$DEVROOT/data/AppData`.

## stack.sh

Before any child:

1. Wipe `$DEVROOT/run/docker.sock` with the other stale runtime files (`*.url`, `*.pid`, `internal.secret`). Do not wipe `rclone.sock` (unchanged).
2. `writeconf` writes `$DEVROOT/lib/fake-disks.json` and `mkdir` `$DEVROOT/data/AppData`.
3. Export, overwriting the parent shell:
   - `DOCKER_HOST=unix://$DEVROOT/run/docker.sock`
   - `CASAOS_LSBLK_FILE=$DEVROOT/lib/fake-disks.json`
   - `CASAOS_FAKE_STORAGE=1`
   - `CASAOS_GIT_APPS_DATA=$DEVROOT/data/AppData`
4. Spawn `go run ./dev/fakedocker -socket $DEVROOT/run/docker.sock -data $DEVROOT/lib/fake-docker` (`setsid`, same as other children). Wait until the socket path exists, 120s. Record the PID. Missing binary, non-zero exit, or timeout: one warning, continue. `DOCKER_HOST` stays the stack socket. Do not inspect or use `/var/run/docker.sock`.
5. Spawn the rest of the stack as today (LocalStorage optional, AppManagement optional, rclone optional).

Remove the warning that compose will fail when host `/var/run/docker.sock` is absent. AppManagement no longer needs the host socket.

SIGINT/SIGTERM: fake-docker is signalled with the other children.

Unix socket lives in `$DEVROOT/run` (0755, developer-owned). Same trust as `internal.secret`.

## Fake disks fixture

`lsblk -J` shape: `{"blockdevices":[...]}`. Two disks, both `IsDiskSupported` (`tran` `sata`):

| Role | `path` | Notes |
|---|---|---|
| system | `/dev/vda` | child `/dev/vda1`, `fstype` `ext4`, `mountpoint` `/` |
| data | `/dev/vdb` | child `/dev/vdb1`, `fstype` `ext4`, `mountpoint` `""` (unmounted spare) |

Sizes are small integers (bytes). Serials and UUIDs are stable strings so Storage can show two rows. The fixture is rewritten every `writeconf`; it is not edited at runtime when fake `parted` no-ops.

## Error handling

- fake-docker missing / exits / no socket in 120s: one warning, stack continues. `DOCKER_HOST` remains the stack socket. Compose and container list fail at the client (connection error or 501). No overlay. No host Docker.
- Unknown Engine path: 501. AppManagement surfaces it as a compose/install error.
- `CASAOS_LSBLK_FILE` unset: host `lsblk`. On the stack, `writeconf` always writes the file; if that write fails, `writeconf` already exits (`set -e`).
- File set but unreadable at process time: `ExecLSBLK` returns nil (same as today’s `lsblk` exec error); `/v1/disks` empty.
- `CASAOS_FAKE_STORAGE` unset: real `mount`/`parted`.
- `CASAOS_GIT_APPS_DATA` unset: `/DATA/AppData`.
- Parent `DOCKER_HOST` pointing at the host daemon: overwritten. The host socket is never a fallback.

## Tests

TDD. No unit test for `stack.sh` itself (same as B/polish). `writeconf` covers the fixture file. Stack env behaviour is in Verification.

| Place | Case | Assert |
|---|---|---|
| CasaOS `dev/fakedocker` | ping | `GET /_ping` → `OK` |
| same | Up then List | one-service project recorded; container `running`; YAML under `-data` |
| same | Down | project and containers gone |
| same | unknown path | 501 |
| same | restart process | same `-data` still lists the project |
| CasaOS-AppManagement | compose v5 `Up` against the fake socket | no image pull; project running |
| LocalStorage `ExecLSBLK` | `CASAOS_LSBLK_FILE` set | file bytes; host `lsblk` not invoked (PATH stub would fail the test if called) |
| same | env unset | PATH stub `lsblk` is invoked |
| LocalStorage `ExecLSBLKByPath` | path in fixture | one matching device |
| LocalStorage `ExecuteCommand` | `CASAOS_FAKE_STORAGE=1`, `parted` | `nil, nil`; PATH stub not called |
| same | env unset, `parted` | PATH stub called |
| AppManagement `ApplyGitAppsDataRoot` | `/tmp/x` | `gitAppsDataRoot` that path |
| same | `""` | `/DATA/AppData` |
| AppManagement `ApplyDataPath` | existing test | `gitAppsDataRoot` still `/DATA/AppData` until `ApplyGitAppsDataRoot` |
| CasaOS `dev/writeconf_test.go` | | `lib/fake-disks.json` exists, two `blockdevices`, `data/AppData` directory |

PATH stubs live in `t.TempDir()` prepended to `PATH`. No real Docker, `lsblk`, `parted`, or FUSE.

## Files

| Path | Action |
|---|---|
| CasaOS `dev/fakedocker/` (`main`, HTTP handlers, state, tests) | fake Engine |
| CasaOS `dev/stack.sh` | spawn fake-docker, env, wipe socket, drop host-sock warning |
| CasaOS `dev/writeconf.sh`, `dev/writeconf_test.go` | fixture JSON, `data/AppData` |
| CasaOS-LocalStorage `pkg/utils/command/command_helper.go` + tests | `CASAOS_LSBLK_FILE`, `CASAOS_FAKE_STORAGE` |
| CasaOS-AppManagement `service` `ApplyGitAppsDataRoot` + test | env hook |
| CasaOS-AppManagement `main.go` | call it after `ApplyDataPath` |
| CasaOS-AppManagement compose-v5 test against fake socket | discovery / contract |

## Out of scope

- Isolated real Docker / rootless dockerd / image pulls / published ports
- mergerfs, FUSE, EnableMergerFS=true, relocating LocalStorage `mkdir /DATA`
- `os-upgrade/busy`, CasaOS `alerts.json` / `fork-release`
- SelectShareModal `/DATA` (UI leftover from polish)
- Combined A+B+polish+C README/CHANGELOG (explicitly after this layer)
- Changing Gateway, ports, UID 0 refusal, or ManagementRoots
- macOS and Windows
- New npm packages
- Committing `.dev-runtime`

## Verification (after implementation)

On Linux, not root, sibling checkouts:

```sh
# CasaOS
go test ./dev/ ./dev/fakedocker/

# CasaOS-LocalStorage
go test ./pkg/utils/command/

# CasaOS-AppManagement
go test ./service/
```

Then `./dev/stack.sh` (or `pnpm dev:stack`):

- Storage lists two disks from the fixture, not the host.
- App Management compose list talks to `$DEVROOT/run/docker.sock`, not `/var/run/docker.sock`.
- Installing a one-service compose app records YAML under `.dev-runtime/lib/fake-docker/` and shows running.
- `ss` / `lsof` on the developer user does not show a connection to `/var/run/docker.sock` from AppManagement.
- Parent `DOCKER_HOST=unix:///var/run/docker.sock ./dev/stack.sh` still uses the stack socket.

`go generate ./...` remains a one-time prerequisite when `codegen/` is missing.
