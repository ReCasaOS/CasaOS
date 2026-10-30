# Local development stack

An unprivileged Linux process stack that runs the CasaOS services from sibling checkouts, without `install.sh`, without systemd, and without writing `/etc/casaos` or `/var/run/casaos`. The dashboard talks to a Gateway on `127.0.0.1:8081`. Compose talks to a record-keeping fake Engine, not to `/var/run/docker.sock`. Storage lists two fixture disks. The file API is confined to `.dev-runtime/data`.

This is not a replacement for [CasaOS-Install](https://github.com/ReCasaOS/CasaOS-Install). Do not run it as root. Linux only.

## Three ways to run the dashboard

From [CasaOS-UI](https://github.com/ReCasaOS/CasaOS-UI), next to this repository:

| Command | What the UI talks to | Needs |
|---|---|---|
| `pnpm dev:mock` | an in-repo stub on port 8099 | Node 24, pnpm 9. No Go. |
| `pnpm dev:stack` | this stack (Gateway on 8081) | sibling Go checkouts, Linux, not root |
| `pnpm dev` | a real CasaOS box | `.env.dev.local` with `VUE_APP_DEV_IP` |

`pnpm dev:mock` and `pnpm dev:stack` both serve Vue on 8080; do not run them at the same time.

The stub answers the home page, the app grid, the VM tiles, stats, login and refresh. It does not run Docker, serve files, share disks, or speak the message-bus WebSocket. Sign-in is as `casaos`.

The stack is the real Gateway, MessageBus, UserService, core, and (when cloned) LocalStorage and AppManagement. The first visit is `/#/welcome`: create the account there. There is no mock session.

## Prerequisites

- Linux, not UID 0.
- Go as declared in each component's `go.mod`.
- For the dashboard: Node 24 and pnpm 9.0.6 (`engines` also allows 20 and 22; 26 is untested).
- Sibling checkouts under one parent:

```
<parent>/
  CasaOS/                  this repo; ./dev/stack.sh
  CasaOS-Gateway/          required
  CasaOS-MessageBus/       required
  CasaOS-UserService/      required
  CasaOS-LocalStorage/     optional; without it, Storage is empty
  CasaOS-AppManagement/    optional; without it, the App Store is empty
  CasaOS-UI/               optional; pnpm dev:stack
```

CasaOS-Common is a Go module, not a process.

Before the first run, if a sibling has no `codegen/` directory, run `go generate ./...` once in CasaOS, CasaOS-UserService, CasaOS-LocalStorage, and CasaOS-AppManagement. That output is gitignored; `stack.sh` does not generate it. The first `go generate` needs network (oapi-codegen and the MessageBus spec).

## Start

From this repository:

```sh
./dev/stack.sh
curl -sS http://127.0.0.1:8081/ping
```

Ready is the line `stack on 127.0.0.1:8081`. `GET /v1/users/status` on a fresh `.dev-runtime` is JSON with `initialized` false.

From CasaOS-UI, one command starts the stack and Vue:

```sh
pnpm dev:stack
```

Open `http://localhost:8080/`. Webpack proxies `/v1` and `/v2` to 8081.

A second `./dev/stack.sh` while 8081 already answers `/ping` exits 1 (`127.0.0.1:8081 is already in use`) and does not take over the port. `sudo ./dev/stack.sh` exits 1 (`don't run the stack as root`).

SIGINT or SIGTERM on the stack process stops the children. Nothing should still listen on 8081. Go code does not hot-reload: change a service, Ctrl-C, run `./dev/stack.sh` again. Vue HMR is unchanged.

`.dev-runtime/` is gitignored. It holds config, SQLite, the first account, `internal.secret`, fake-docker state, and files created in Files. Delete it to start from welcome again.

## What the stack provides

**Gateway.** `port=8081`, `address=127.0.0.1`, `httpsport=0`. The only public listener. Services still bind loopback at a kernel-assigned port and register a prefix, using `internal.secret` and `management.url` under `.dev-runtime/run`.

**Files.** `[file] ManagementRoots` is `.dev-runtime/data`. `GET /v1/file/roots` returns that list; the dashboard Files panel opens there, not `/DATA`. Paths outside it are 403. rclone is optional: if it is on `PATH`, the stack starts `rclone rcd` on `.dev-runtime/run/rclone.sock`; without it, Locations has no cloud drives (no overlay).

**Compose.** `DOCKER_HOST` is `unix://.dev-runtime/run/docker.sock`, always, even if the parent shell pointed at `/var/run/docker.sock`. `dev/fakedocker` is a record-keeping Engine: ping, version, image inspect as present (no pull), compose Up/List/start/stop/Down as records, YAML copied under `.dev-runtime/lib/fake-docker/<project>/`. Bind mounts and networks are recorded, not created on the host. No published ports, no `exec`. Any other Engine path is **501**. AppManagement surfaces that as a compose/install error. If fake-docker fails to start, `DOCKER_HOST` still points at the stack socket; compose fails at the client. There is no fallback to the host daemon.

**Storage.** `CASAOS_LSBLK_FILE` is `.dev-runtime/lib/fake-disks.json`: two SATA disks (`/dev/vda` system, `/dev/vdb` unmounted spare). `CASAOS_FAKE_STORAGE=1` makes `mount`, `umount`, `parted`, `partprobe`, `e2label` and `sfdisk` no-ops. `EnableMergerFS=false`. There is no FUSE and no merge mount. LocalStorage may log `mkdir /DATA` permission denied and continue; that is expected without root.

**App data.** `CASAOS_GIT_APPS_DATA` is `.dev-runtime/data/AppData`. AppManagement `DataPath` is `.dev-runtime/lib`, so catalogues and installed compose files stay out of `/var/lib/casaos`.

Production defaults do not change when those environment variables and config keys are unset.

## Environment `stack.sh` exports

| Variable | Value | If unset in production |
|---|---|---|
| `CASAOS_CONFIG_PATH` | `.dev-runtime/etc` (directory that contains `gateway.ini`) | `/etc/casaos` |
| `DOCKER_HOST` | `unix://.dev-runtime/run/docker.sock` | the host Docker daemon |
| `CASAOS_LSBLK_FILE` | `.dev-runtime/lib/fake-disks.json` | host `lsblk` |
| `CASAOS_FAKE_STORAGE` | `1` | real mount/parted |
| `CASAOS_GIT_APPS_DATA` | `.dev-runtime/data/AppData` | `/DATA/AppData` |
| `CASAOS_RCLONE_SOCKET` | `.dev-runtime/run/rclone.sock` | the packaged rclone socket |

A parent `DOCKER_HOST` is overwritten. The host `/var/run/docker.sock` is never inspected.

## Growing the fake Engine

The fake is additive. When a new AppManagement call hits a path that returns 501, implement that path on `dev/fakedocker` so the record-keeping contract still holds. Do not point `DOCKER_HOST` at a real daemon to “just make it work”.

`go test ./dev/fakedocker/` speaks HTTP only. Compose v5 `Up` against the fake lives in CasaOS-AppManagement (`TestComposeUpAgainstFakeDocker`); it skips if `../CasaOS/dev/fakedocker` is missing.

## What it does not do

- Samba, USB automount, installer, OS modules, self-signed HTTPS
- Image pulls, containerd, published host ports, exec-in-container
- mergerfs / FUSE / `EnableMergerFS=true`
- Isolating a CasaOS already installed on the same host (the stack never opens that host's Docker socket; it also does not stop those services)
- macOS and Windows
- Auto-creating the first account

## Tests

```sh
# CasaOS-Gateway
go test ./common/

# CasaOS
go test ./dev/ ./dev/fakedocker/ ./pkg/utils/httper/ ./pkg/hoststats/

# CasaOS-LocalStorage
go test ./pkg/utils/command/

# CasaOS-AppManagement
go test ./service/ -count=1

# CasaOS-UI
pnpm exec vitest run
```

Then `./dev/stack.sh` (or `pnpm dev:stack`): Storage lists the two fixture disks, not the host's; an install records YAML under `.dev-runtime/lib/fake-docker/`; `lsof` for the developer user does not show AppManagement on `/var/run/docker.sock`.
