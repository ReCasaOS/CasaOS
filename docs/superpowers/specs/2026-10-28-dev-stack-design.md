# Local process stack — design

Status: approved in conversation on 2026-10-28, section by section.

This is layer B of the three-layer development setup. Layer A (`pnpm dev:mock` in CasaOS-UI) is separate. Layer C (Go fakes for Docker and mergerfs) is a later spec.

## Goal

A developer with the sibling checkouts on Linux can start the real Gateway, MessageBus, UserService, LocalStorage, AppManagement and CasaOS core against a user-writable runtime tree, then point the dashboard at that Gateway, without root, without systemd, and without writing `/etc/casaos` or `/var/run/casaos`.

That command from the core repo is `./dev/stack.sh`. From the dashboard repo it is `pnpm dev:stack`.

## Decisions

- Unprivileged `go run` of the sibling repositories, not docker-compose and not `install.sh`.
- Runtime and config live in `CasaOS/.dev-runtime/` (git-ignored).
- Gateway is the only production-code change: `CASAOS_CONFIG_PATH` is used for both creating and reading `gateway.ini`. The other services already take `-c`.
- Public port **8081**, bind **127.0.0.1**, `httpsport=0`. Not optional.
- Refuse to start as UID 0.
- File API confined to `.dev-runtime/data` via `[file] ManagementRoots`.
- First account is created in the browser (`FirstAdmin=web`). No mock session seed.
- Linux only. LocalStorage is skipped with a warning when its checkout is missing or `go run` cannot build it. AppManagement is skipped with a warning when its checkout is missing; if it starts without a Docker socket, compose calls fail at runtime.
- `pnpm dev` and `pnpm dev:mock` are unchanged.

## Architecture

```
./dev/stack.sh                          (CasaOS)
        │
        ├─ write .dev-runtime/etc/*.conf
        ├─ go run <parent>/CasaOS-Gateway -w .dev-runtime/www
        │     cwd=.dev-runtime/etc  CASAOS_CONFIG_PATH=.dev-runtime/etc
        ├─ cd CasaOS-MessageBus  && go run . -c .dev-runtime/etc/message-bus.conf
        ├─ cd CasaOS-UserService && go run . -c .dev-runtime/etc/user-service.conf
        ├─ cd CasaOS-LocalStorage && go run . -c …/local-storage.conf   (optional)
        ├─ cd CasaOS-AppManagement && go run . -c …/app-management.conf (optional)
        └─ cd CasaOS && go run . -c .dev-runtime/etc/casaos.conf

pnpm dev:stack                          (CasaOS-UI)
        │
        ├─ ../CasaOS/dev/stack.sh
        └─ vue-cli-service serve --mode stack     (port 8080)
                   │
                   │  proxy /v1 and /v2  (vue.config.js, unmodified)
                   ▼
              127.0.0.1:8081   Gateway
```

Sibling layout, from the CasaOS checkout:

```
<parent>/
  CasaOS/                 this repo; ./dev/stack.sh
  CasaOS-Gateway/
  CasaOS-MessageBus/
  CasaOS-UserService/
  CasaOS-LocalStorage/    optional
  CasaOS-AppManagement/   optional
  CasaOS-UI/              optional; pnpm dev:stack
```

CasaOS-Common is a Go module, not a process.

Each service still listens on loopback at a kernel-assigned port and registers its prefix with the Gateway management API, using `internal.secret` and `management.url` under `.dev-runtime/run`. That protocol does not change.

## Security

- `address=127.0.0.1` and `httpsport=0` are written into `gateway.ini` every start. The script does not take flags that open other interfaces or ports.
- UID 0: print `don't run the stack as root` and exit 1. No `sudo` path.
- `internal.secret` is 0600 owned by the developer. Any process of that user can call internal APIs. That is the same trust as "the user who started the stack".
- `[file] ManagementRoots` is the absolute path of `.dev-runtime/data`. Empty `ManagementRoots` (the sample default: the whole machine the process can see) is not used.
- The stack talks to the host Docker socket when AppManagement starts. That is the host's Docker, including any apps an installed CasaOS already runs. Document it; isolation is layer C.
- `.dev-runtime/` is git-ignored. It holds SQLite, the first account, and `internal.secret`.
- Child processes receive `CASAOS_CONFIG_PATH=.dev-runtime/etc` explicitly. A value already set in the developer's shell is not what Gateway uses.

## Gateway patch

Today `main.go` always stats and may create `/etc/casaos/gateway.ini` before `LoadConfig`. On a machine without that directory the process panics even when `CASAOS_CONFIG_PATH` points at a writable tree.

Move path resolution and create into `common`:

```go
func GatewayConfigFilePath() string {
    if p, ok := os.LookupEnv("CASAOS_CONFIG_PATH"); ok && p != "" {
        return filepath.Join(p, GatewayName+"."+GatewayConfigType)
    }
    return filepath.Join(constants.DefaultConfigPath, GatewayName+"."+GatewayConfigType)
}

func EnsureGatewayConfig(sample string) error { /* mkdir parent; create if missing */ }
```

`main` calls `EnsureGatewayConfig(_confSample)` then `LoadConfig`. Empty `CASAOS_CONFIG_PATH` is treated as unset.

When the env is unset, the path is still `/etc/casaos/gateway.ini`. Production systemd units do not set the env; behaviour there does not change.

`LoadConfig` keeps its search order (cwd, cwd/conf, env, `/etc/casaos`). Gateway is spawned with cwd `$DEVROOT/etc` and `CASAOS_CONFIG_PATH` the same directory, so a stray `gateway.ini` in the Gateway checkout cannot shadow the stack file. The stack writes `gateway.ini` before spawn, so create is a no-op.

## Runtime tree

`DEVROOT` is `CasaOS/.dev-runtime`, resolved to an absolute path.

| Path | Role |
|---|---|
| `$DEVROOT/etc/` | conf files |
| `$DEVROOT/run/` | `RuntimePath`: `internal.secret`, `management.url`, `*.url`, pid files |
| `$DEVROOT/lib/` | SQLite, app store cache, user data |
| `$DEVROOT/log/` | lumberjack files |
| `$DEVROOT/data/` | file-manager root (`ManagementRoots`) |
| `$DEVROOT/www/` | empty dir for Gateway `-w` (dashboard comes from `pnpm`, not this tree) |

Created with `mkdir -p` at the start of `writeconf`.

`.gitignore` in CasaOS gains `.dev-runtime/`.

## Conf files

`dev/writeconf.sh <DEVROOT> <CASAOS_REPO>` writes every file below. It does not copy a sample unchanged: the CasaOS sample has no `[common]` section, so a copy would leave the core on `/var/run/casaos`. Log and DB paths that default to `/var/log/casaos` and `/var/lib/casaos` are rewritten so lumberjack and SQLite do not need those directories.

`SHELL` is `$CASAOS_REPO/build/sysroot/usr/share/casaos/shell`.

### `gateway.ini`

```
[common]
runtimepath=<DEVROOT>/run

[gateway]
port=8081
address=127.0.0.1
tlscert=
tlskey=
httpsport=0
LogPath=<DEVROOT>/log
LogSaveName=gateway
LogFileExt=log
```

### `message-bus.conf`

```
[common]
RuntimePath=<DEVROOT>/run

[app]
LogPath=<DEVROOT>/log
LogSaveName=message-bus
LogFileExt=log
DBPath=<DEVROOT>/lib
```

(`DBPath` is required: the sample omits it and the binary then uses `/var/lib/casaos`.)

### `user-service.conf`

```
[common]
RuntimePath=<DEVROOT>/run

[app]
LogPath=<DEVROOT>/log
LogSaveName=user-service
LogFileExt=log
DBPath=<DEVROOT>/lib/db
UserDataPath=<DEVROOT>/lib
FirstAdmin=web
```

### `local-storage.conf`

```
[common]
RuntimePath=<DEVROOT>/run

[app]
LogPath=<DEVROOT>/log
LogSaveName=local-storage
LogFileExt=log
DBPath=<DEVROOT>/lib/db
ShellPath=<SHELL>

[server]
USBAutoMount=
EnableMergerFS=false
```

### `app-management.conf`

```
[common]
RuntimePath=<DEVROOT>/run

[app]
LogPath=<DEVROOT>/log
LogSaveName=app-management
LogFileExt=log
AppStorePath=<DEVROOT>/lib/appstore
AppsPath=<DEVROOT>/lib/apps

[server]
appstore = https://cdn.jsdelivr.net/gh/IceWhaleTech/CasaOS-AppStore@gh-pages/store/main.zip
appstore = https://github.com/bigbeartechworld/big-bear-casaos/archive/refs/heads/master.zip
```

### `casaos.conf`

```
[common]
RuntimePath=<DEVROOT>/run

[app]
PAGE_SIZE = 10
LogPath=<DEVROOT>/log
LogSaveName=log
LogFileExt=log
DBPath=<DEVROOT>/lib
ShellPath=<SHELL>
UserDataPath=<DEVROOT>/lib/conf

[server]
RunMode=release
SambaMainConfig=managed
ImageManaged=false

[file]
ManagementRoots=<DEVROOT>/data
```

All paths in the files are absolute.

## Orchestrator (`dev/stack.sh`)

No new language, no extra package. Bash. `set -euo pipefail`.

### Start

1. `uname -s` is Linux; otherwise exit 1 (`the local stack is Linux only`).
2. UID is not 0.
3. Resolve `CASAOS_REPO` (directory of the script, `..`) and `PARENT` (`$CASAOS_REPO/..`). Required siblings: `CasaOS-Gateway`, `CasaOS-MessageBus`, `CasaOS-UserService`. Missing one: exit 1, print the expected path.
4. If `GET http://127.0.0.1:8081/ping` is already HTTP 200, exit 1 (`gateway did not answer on 127.0.0.1:8081` is the busy-port message used when we started it; when we did not, the message is `127.0.0.1:8081 is already in use`). Do not start a second stack on top.
5. If 8081 is not answering: delete stale `$DEVROOT/run/*.url`, `$DEVROOT/run/*.pid`, `$DEVROOT/run/internal.secret`. Leave SQLite and conf in place.
6. Run `dev/writeconf.sh`. If it fails, do not spawn.
7. Spawn Gateway with cwd `$DEVROOT/etc`: `CASAOS_CONFIG_PATH=$DEVROOT/etc go run "$PARENT/CasaOS-Gateway" -w "$DEVROOT/www"`. `stdio` inherit. Record PID. Wait until `GET http://127.0.0.1:8081/ping` is 200 **and** `$DEVROOT/run/management.url` exists, or the process has exited. Timeout **120 seconds** (first `go run` compiles). On failure, stop children, print `gateway did not answer on 127.0.0.1:8081`, exit 1.
8. Spawn MessageBus: `cd "$PARENT/CasaOS-MessageBus" && go run . -c "$DEVROOT/etc/message-bus.conf"`. Wait for `$DEVROOT/run/message-bus.url` or process exit, 120s.
9. Spawn UserService the same way; wait for `user-service.url`.
10. LocalStorage: if the directory exists, spawn with `-c`. It does not write a url file. Ready: `GET http://127.0.0.1:8081/v1/disks` is no longer 404 (401/403/200 all mean the route is registered). If `go run` exits non-zero, or 120s pass with 404, print a warning and continue. If the directory is missing, warn and continue.
11. AppManagement: if the directory exists, spawn; wait for `app-management.url`. If `/var/run/docker.sock` is missing and `DOCKER_HOST` is empty, print one line that compose calls will fail. Missing directory: warn and continue.
12. Spawn CasaOS: `cd "$CASAOS_REPO" && go run . -c "$DEVROOT/etc/casaos.conf"`. Wait for `casaos.url`.
13. Print `stack on 127.0.0.1:8081` and wait. Do not daemonize.

A required service whose `go run` exits, or whose ready file does not appear in 120s, stops every child already started (SIGTERM to each recorded PID, then `kill -- -PID` if still alive) and exits 1.

### Signals

On SIGINT or SIGTERM: SIGTERM every child, wait, exit 0. `.dev-runtime` is not deleted.

### Environment of children

Each `go run` is given:

- `CASAOS_CONFIG_PATH=$DEVROOT/etc` (Gateway; harmless on the others)
- `HOME`, `PATH`, `GOPATH`, `GOROOT`, `GOCACHE`, `GOMODCACHE`, `DOCKER_HOST` inherited
- Not a filtered `env -i`

## UI glue

New committed `.env.stack` in CasaOS-UI:

```
NODE_ENV=dev
VUE_APP_DEV_IP=127.0.0.1
VUE_APP_DEV_PORT=8081
```

No `VUE_APP_MOCK`. `seedMockSession` does not run. The first visit is `/#/welcome` until an account exists.

`package.json`: `"dev:stack": "node dev/stack.mjs"`.

`dev/stack.mjs` (no new npm dependency):

1. Resolve `../CasaOS/dev/stack.sh` from the UI root. Missing: exit 1 (`clone CasaOS next to this repository and run ./dev/stack.sh from there, or keep this checkout beside it`).
2. Spawn `bash ../CasaOS/dev/stack.sh` with `stdio: ['ignore', 'pipe', 'inherit']`, `detached: true`, cwd CasaOS. Forward the child's stdout to the terminal (same pattern as `mock/dev.mjs`).
3. Wait until stdout contains `stack on 127.0.0.1:8081`, or the child exits. Timeout **300 seconds** (six cold `go run` compiles). Do not start Vue on Gateway ping alone: ping is true before UserService and CasaOS have registered. Message on failure: `stack did not become ready on 127.0.0.1:8081`.
4. Spawn `pnpm exec vue-cli-service serve --mode stack`, `stdio: 'inherit'`, `detached: true`.
5. SIGINT/SIGTERM: kill Vue (process group), then the stack script (process group), exit 0.
6. Vue exit: kill the stack, exit with Vue's code. Stack exit while Vue is running: kill Vue, exit 1.

`vue.config.js` is not edited. `--mode stack` loads `.env.stack`; `NODE_ENV=dev` is required so `$baseIp` uses `VUE_APP_DEV_*`.

`pnpm dev:mock` and `pnpm dev:stack` both want UI port 8080. Running both at once is unsupported; the second Vue spawn fails.

## Tests

### Gateway (`common/config_test.go` or a new `common/ensure_config_test.go`)

TDD. Do not call `EnsureGatewayConfig` with the env unset (that would touch `/etc/casaos`).

| Case | Assert |
|---|---|
| `CASAOS_CONFIG_PATH` = temp dir, no file | `GatewayConfigFilePath()` is `temp/gateway.ini`. `EnsureGatewayConfig(sample)` creates that file, parent dir included, contents the sample. `/etc/casaos/gateway.ini` is not created. |
| `CASAOS_CONFIG_PATH` = temp dir, file already there | `EnsureGatewayConfig` does not overwrite. |
| env unset | `GatewayConfigFilePath()` is `/etc/casaos/gateway.ini`. |
| env set to `""` | same as unset. |

Existing `TestLoadConfigGatewayAddress` still passes.

### Conf writer (`dev/writeconf_test.go`)

`go test` in CasaOS, package under `dev/`. Runs `writeconf.sh` against `t.TempDir()` and a fake repo path.

Assert:

- `etc/gateway.ini` contains `port=8081`, `address=127.0.0.1`, `httpsport=0`, and `runtimepath=` the temp `run` dir (absolute).
- `etc/casaos.conf` contains `[common]`, `RuntimePath=` the temp `run` dir, and `ManagementRoots=` the temp `data` dir (absolute).
- `etc/message-bus.conf` contains `DBPath=` under the temp tree (not `/var/lib/casaos`).
- `etc/user-service.conf` contains `FirstAdmin=web`.
- `etc/local-storage.conf` contains `EnableMergerFS=false`.

No unit test for `stack.sh` or `dev/stack.mjs` (process supervisors; same choice as `mock/dev.mjs`).

No new vitest in CasaOS-UI.

## Documentation

CasaOS `README.md` Development: after the existing `go test` block, the local stack — Linux, sibling checkouts, `./dev/stack.sh`, `curl http://127.0.0.1:8081/ping`, then from CasaOS-UI `pnpm dev:stack`. One sentence: the stack is not a replacement for `install.sh`; it uses the host Docker socket; do not run it as root; the file manager sees only `.dev-runtime/data`.

CasaOS-UI `README.md` Development: a third path next to the box and `pnpm dev:mock` — `pnpm dev:stack` when the Go checkouts sit beside this repo. Welcome creates the first account. `pnpm dev:mock` remains the path without Go services.

CasaOS-Gateway README: `CASAOS_CONFIG_PATH` is the directory that contains `gateway.ini`, and is also where a missing file is created.

## Files

| Path | Action |
|---|---|
| CasaOS-Gateway `common/config.go` | `GatewayConfigFilePath`, `EnsureGatewayConfig` |
| CasaOS-Gateway `main.go` | use those; stop hard-coding `/etc/casaos` for create |
| CasaOS-Gateway `common/ensure_config_test.go` | the cases above |
| CasaOS `dev/writeconf.sh` | conf writer |
| CasaOS `dev/writeconf_test.go` | asserts on generated files |
| CasaOS `dev/stack.sh` | orchestrator |
| CasaOS `.gitignore` | `.dev-runtime/` |
| CasaOS `README.md` | Development |
| CasaOS-UI `.env.stack` | create |
| CasaOS-UI `dev/stack.mjs` | Vue + stack supervisor |
| CasaOS-UI `package.json` | `dev:stack` |
| CasaOS-UI `README.md` | Development |
| CasaOS-UI `vue.config.js` | unchanged |
| CasaOS-UI `src/main.js` | unchanged (mock seed stays mock-only) |

## Out of scope

- Layer C: fake Docker, fake mergerfs, `CASAOS_INTEGRATION`
- Samba, USB automount, installer, OS modules, self-signed HTTPS
- macOS and Windows
- docker-compose of the services
- Changing Gateway `LoadConfig` search order
- Auto-creating the first account (`-bootstrap-admin`)
- Hot reload of Go (each change is Ctrl-C and `./dev/stack.sh` again)
- Message-bus stub (the real bus runs)
- Isolating Docker from a CasaOS already installed on the same host
- Committing `.dev-runtime`

## Verification (after implementation)

On Linux, Node 24, Go 1.21+, sibling checkouts, not root:

```sh
# CasaOS-Gateway
go test ./common/

# CasaOS
go test ./dev/
./dev/stack.sh
```

`./dev/stack.sh` prints `stack on 127.0.0.1:8081`. `curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8081/ping` is `200`. `GET http://127.0.0.1:8081/v1/users/status` is JSON with `initialized` false on a fresh `.dev-runtime`.

A second terminal `./dev/stack.sh` exits 1 and does not take over 8081.

`id` 0 (or `sudo ./dev/stack.sh`) exits 1 with `don't run the stack as root`.

Ctrl-C returns the shell; nothing listens on 8081.

```sh
# CasaOS-UI, while the stack is up, or as the one command:
pnpm dev:stack
```

Webpack prints 8080. Opening that URL shows `/#/welcome`, not the mock dashboard and not `/#/login`. Creating an account reaches `/#/`. The file manager cannot list `$HOME`; it can list files created under `CasaOS/.dev-runtime/data`.

`pnpm exec vitest run` in CasaOS-UI is unchanged (including the mock specs). `pnpm dev:mock` still uses 8099, not 8081.
