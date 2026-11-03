<p align="center"><img src="https://raw.githubusercontent.com/ReCasaOS/.github/main/assets/recasaos-banner.svg" alt="ReCasaOS" width="640"></p>

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/recasaos)

[Support ReCasaOS](https://github.com/ReCasaOS/.github/blob/main/FUNDING.md)

# ReCasaOS

> **Not affiliated with IceWhale.**
> This is an independent, community-maintained distribution of CasaOS. It is not produced, endorsed, sponsored, certified or supported by Shanghai IceWhale Technology Limited or by any of the upstream CasaOS maintainers.
>
> CASAOS is a trademark of Shanghai IceWhale Technology Limited. The name is used here only to identify the upstream project that this software is a distribution of, as permitted for referential use; no rights in the CasaOS name or logo are claimed, and no affiliation is implied.
>
> The original project is [IceWhaleTech/CasaOS](https://github.com/IceWhaleTech/CasaOS). Please do not report problems with this distribution to IceWhale — open them at [ReCasaOS/CasaOS/issues](https://github.com/ReCasaOS/CasaOS/issues).

A personal cloud for the home, on your own hardware: a dashboard, an app store, Docker apps, files and shares, backups, alerts, virtual machines. **ReCasaOS** is a maintained release of [CasaOS](https://github.com/IceWhaleTech/CasaOS) after upstream stopped shipping in 2025. It builds on [alvins82's fork](https://github.com/alvins82/CasaOS), which kept CasaOS installable on Docker 29 and Ubuntu 26, and goes on from there. This repository is the core service; the installer, the release pipeline and the long form of what changed live in [CasaOS-Install](https://github.com/ReCasaOS/CasaOS-Install#readme).

<p align="center">
    <a href="https://github.com/ReCasaOS/CasaOS-Install/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/ReCasaOS/CasaOS-Install?color=162453&style=flat-square&label=Distribution" /></a>
    <a href="https://github.com/ReCasaOS/CasaOS/stargazers"><img alt="Stars" src="https://img.shields.io/github/stars/ReCasaOS/CasaOS?color=162453&style=flat-square&label=Stars" /></a>
    <a href="https://github.com/ReCasaOS/CasaOS/blob/main/LICENSE"><img alt="Licence" src="https://img.shields.io/github/license/ReCasaOS/CasaOS?color=162453&style=flat-square&label=License" /></a>
    <a href="https://github.com/ReCasaOS/CasaOS/issues"><img alt="Issues" src="https://img.shields.io/github/issues/ReCasaOS/CasaOS?color=162453&style=flat-square&label=Issues" /></a>
    <a href="https://github.com/ReCasaOS/CasaOS/pulls"><img alt="Pull requests" src="https://img.shields.io/github/issues-pr/ReCasaOS/CasaOS?color=162453&style=flat-square&label=PRs" /></a>
    <br/><br/>
    <img alt="The settings panel, grouped under Display, System and Updates" src="https://raw.githubusercontent.com/ReCasaOS/CasaOS-Install/main/docs/screenshots/settings.png" width="260" />
    &nbsp;
    <img alt="Backups: destinations, schedules and history" src="https://raw.githubusercontent.com/ReCasaOS/CasaOS-Install/main/docs/screenshots/backups.png" width="440" />
</p>

<p align="center"><sub>The dashboard, rendered with sample data.</sub></p>

## Install

```sh
curl -fsSL https://github.com/ReCasaOS/CasaOS-Install/releases/latest/download/install.sh | sudo bash
```

One command for every supported system and architecture; the installer detects both at run time. Run it again to upgrade, or use the update button in the dashboard, which follows this distribution's releases.

**Migrating from IceWhale's or alvins82's installer** works the same way, in place, without uninstalling first. The installer stops and restarts the CasaOS services while upgrading, so back up what matters. Afterwards, `cat /var/lib/casaos/fork-release` prints the distribution release installed. Do not use `get.casaos.io/update` after migrating: it installs IceWhale's frozen component bundle over this one.

Everything the installer downloads is verified against a SHA-256 digest before extraction. A box sends anonymous statistics (which release it runs, how fast it updated, what kind of hardware), on by default; `--no-telemetry` on the install line turns them off, and [what is sent](https://github.com/ReCasaOS/CasaOS-Install#anonymous-statistics) is listed property by property. If the dashboard says you are on the latest version and never offers an update, run the install command once by hand: hosts installed before CasaOS-Install v0.4.42 polled a feed older than what they ran, and re-running the installer corrects it.

### Compatibility

Architectures: amd64, arm64, armv7.

Every release is installed from scratch by CI right after it is published, and exercised end to end (apps, backup and restore, storage, alerts), on Ubuntu 22.04 (amd64 and arm64), Ubuntu 24.04 and 26.04, and Debian 12 and 13. Debian 11 is out of support and its security repository still lists packages it no longer serves, so a fresh install would stop at the first one it needs from there: the installer says so and offers to take that repository from archive.debian.org (`--use-debian-archive` answers yes in advance), and CI installs it that way. Reported working by the community, not covered by CI: Raspberry Pi OS, Elementary 6.1, Armbian 22.04. Not fully tested: Alpine, OpenWrt, Arch.

Docker 28 and 29 are tested on every release, and the installer accepts Docker 20 or later (24.0.5 and 20.10.24 were also tried by hand on v0.5.16). The Docker 29 incompatibility that broke the upstream release is fixed here, without the `DOCKER_MIN_API_VERSION` workaround. ReCasaOS never updates Docker behind your back: see [Updating Docker](https://github.com/ReCasaOS/CasaOS-Install#updating-docker).

### Uninstall

```sh
sudo casaos-uninstall
```

## What ReCasaOS adds to CasaOS

- **Apps.** A Compose editor and a `.env` for every app, and your own stacks listed as apps. Deploy an app from a git repository, following a branch or its release tags, with a webhook to redeploy on a push and a rollback when a version does not start. Updates that say what they will do, and opt-in automatic updates at night, never during a backup.
- **Backups.** Apps and the box itself, to S3, SFTP, FTP, WebDAV, OneDrive, Google Drive, Dropbox or anything else rclone speaks, optionally encrypted, on a schedule with a retention, a history and a restore. A destination can be switched to a [restic](https://restic.net) repository: each backup then costs only what changed, and files keep their owners and permissions.
- **Alerts.** A notification on your phone or an e-mail (ntfy, Telegram, e-mail, Discord, Gotify, Matrix and more) when a backup fails, a disk fails or fills up, a RAID array loses a disk, an app crashes, a container, a virtual machine or the machine stays above a threshold, or an update pauses. Nothing is sent until you add a channel.
- **Storage and files.** Software RAID from the Storage manager: its state, an alert when it loses a disk, repair, creation (RAID 0, 1, 5, 6, 10) and RAID 1 to RAID 5. A trash in Files that keeps what you delete for thirty days. Shared folders that ask who can open them, with share accounts managed from the dashboard and Time Machine shares. Merged storage restored across reboots, and SMB discovery over mDNS and Windows Web Service Discovery.
- **Virtual machines and statistics.** The optional `virt` module creates and runs virtual machines from the dashboard, with the console in the browser. A *Statistics* app shows processor, memory, network, disk and health of every container, every virtual machine and the machine itself, live and over the last 7 days, with resource alerts.
- **Fleet.** One box can be the master of your other boxes, wherever they are: each joins with a code and keeps an outbound connection to it, so no port opens on any box. From the master's *Fleet* page you see every box, its OS, its containers and its virtual machines, and start, stop or update them, or update ReCasaOS and the system packages, on several boxes at once. Whoever signs in to the master can act on every box: turn on two-factor authentication there.
- **System.** The update button and the *System packages* update follow this distribution, and Docker is left out of the latter until you confirm. Optional modules are installed, updated and removed from the dashboard or with `casaos module`. The dashboard says when your Debian or Ubuntu release has run out of security support, and a wizard moves a Debian 11 machine to Debian 12 (beta, off by default).
- **Accounts, network and security.** Passwords stored with argon2id and two-factor authentication. Nothing on the message bus without signing in, a refresh token that opens no route, and no trust in the loopback address as an identity. HTTPS on 443 out of the box. Moving files can no longer delete them, and a leaked download link no longer gives write access. The dashboard no longer sends a machine fingerprint to a third party, and the installer no longer asks one where you are.
- **The dashboard.** Vue 3, a dark theme, a complete French translation, a grouped settings panel, and a home page that fits a phone.

The long form is in the [installer's README](https://github.com/ReCasaOS/CasaOS-Install#what-recasaos-adds-to-casaos), and every change, release by release, in its [changelog](https://github.com/ReCasaOS/CasaOS-Install/blob/main/CHANGELOG.md).

### Opt-in settings in this repository

- **A host that owns `/etc/samba/smb.conf` itself** (configuration management, image-built systems) sets `[server] SambaMainConfig = external` and puts `include = /etc/samba/smb.casa.conf` in its `[global]` section: CasaOS then writes only that file, and refuses a share change while the line is missing.
- **`[file] ManagementRoots`** confines the file manager to the directories it names: every path, upload, copy and move is checked, and links that lead out are refused.

## Community and contributing

Bug reports and requests go to [issues on this repository](https://github.com/ReCasaOS/CasaOS/issues), whichever component is involved. A vulnerability is reported privately, as [SECURITY.md](SECURITY.md) says. Pull requests are welcome on any of the component repositories under [ReCasaOS](https://github.com/orgs/ReCasaOS/repositories); each has its own release workflow, and the installer pins them.

If ReCasaOS is useful to you, a [star on GitHub](https://github.com/ReCasaOS/CasaOS) helps other people find it.

The upstream Discord, wiki and website belong to IceWhale and are not run by this distribution.

## Repositories

| Repository | Role |
|---|---|
| [CasaOS](https://github.com/ReCasaOS/CasaOS) | this one: system API, files, shares, updater |
| [CasaOS-UI](https://github.com/ReCasaOS/CasaOS-UI) | the dashboard (a submodule here) |
| [CasaOS-AppManagement](https://github.com/ReCasaOS/CasaOS-AppManagement) | apps, Compose, git deployments, backups, statistics, the app store client |
| [CasaOS-Gateway](https://github.com/ReCasaOS/CasaOS-Gateway) | reverse proxy in front of every service, HTTPS |
| [CasaOS-UserService](https://github.com/ReCasaOS/CasaOS-UserService) | accounts, login, two-factor |
| [CasaOS-MessageBus](https://github.com/ReCasaOS/CasaOS-MessageBus) | events between services |
| [CasaOS-LocalStorage](https://github.com/ReCasaOS/CasaOS-LocalStorage) | disks, volumes, merged storage, RAID |
| [CasaOS-VirtManagement](https://github.com/ReCasaOS/CasaOS-VirtManagement) | virtual machines, the optional `virt` module |
| [CasaOS-Install](https://github.com/ReCasaOS/CasaOS-Install) | the installer and the release bundle |

## Development

This repository builds `casaos`, the core service. Behind the gateway it serves the `/v1` API — system information and power actions, listening ports, the file manager (`/v1/file`, `/v1/folder`, `/v1/batch`, `/v1/image`), Samba shares and accounts (`/v1/samba`), cloud drivers, and status notifications from the other services (`/v1/notify`) — together with a `/v2` API generated from [`api/casaos/openapi.yaml`](api/casaos/openapi.yaml). It also polls this distribution's release feed and runs the installer when the dashboard asks for an update.

Its configuration is `/etc/casaos/casaos.conf`, created on first run from [the sample embedded in the binary](build/sysroot/etc/casaos/casaos.conf.sample). Its database and user data live under `/var/lib/casaos`, its logs under `/var/log/casaos`, and the shell helpers it shells out to under `/usr/share/casaos/shell`. The dashboard is the `UI` submodule and is built separately.

The only requirement is the Go version in [`go.mod`](go.mod); the Go build does not need the submodule. [DEVELOPING.md](DEVELOPING.md) describes a development environment.

```sh
CGO_ENABLED=0 GOOS=linux go build .
go test ./...
```

`TestPorts` reads the host's own listening sockets, so it fails in a container that has none.

On Linux, with the other component repositories cloned as siblings of this one (`../CasaOS-Gateway`, `../CasaOS-MessageBus`, `../CasaOS-UserService`, and optionally LocalStorage, AppManagement and UI), a user-writable process stack binds the Gateway at `127.0.0.1:8081` without root and without writing `/etc/casaos`. Compose talks to a record-keeping Engine under `.dev-runtime/`, not to `/var/run/docker.sock`. Storage lists fixture disks. The file API is confined to `.dev-runtime/data`. This is not `install.sh`. Commands, layout, environment variables and limits are in [dev/README.md](dev/README.md).

```sh
./dev/stack.sh
curl -sS http://127.0.0.1:8081/ping
```

Before the first run, if a sibling repo has no `codegen/` directory, run `go generate ./...` once in CasaOS, CasaOS-UserService, CasaOS-LocalStorage, and CasaOS-AppManagement (that output is gitignored; `stack.sh` does not generate it).

From CasaOS-UI beside this repo, `pnpm dev:stack` starts that script and the Vue dev server. rclone is optional; without it, Locations has no cloud drives.

Release packages for amd64, arm64 and armv7 are built on a tag by [`.github/workflows/release.yml`](.github/workflows/release.yml), not on a workstation.

## History

CasaOS was created by IceWhale as the pre-installed system of the ZimaBoard. Upstream development moved to their proprietary ZimaOS and the open-source project stopped receiving releases in 2025. [alvins82](https://github.com/alvins82/CasaOS) kept it installable — Docker 29, Ubuntu 26, merged storage fixes, SMB discovery — and this distribution started from his v0.4.39 in September 2026, adding the changes above and a release process that no longer depends on a workstation.

<details>
<summary>alvins82's fork changelog (August 2026)</summary>

- **2026-08-15 — [CasaOS Install v0.4.35](https://github.com/alvins82/CasaOS-Install/releases/tag/v0.4.35):** Superseded v0.4.34 with exact 64-character SHA-256 values for every architecture, correcting the amd64 and arm/v7 AppManagement package checksums.
- **2026-08-15 — [CasaOS Install v0.4.34](https://github.com/alvins82/CasaOS-Install/releases/tag/v0.4.34):** Corrected the installer SHA-256 values for the fork packages after v0.4.33 upgrades stopped during checksum verification, and republished the compatibility overlay with the matching release marker.
- **2026-08-15 — [CasaOS Install v0.4.33](https://github.com/alvins82/CasaOS-Install/releases/tag/v0.4.33):** Published the full fork bundle with [CasaOS v0.4.28](https://github.com/alvins82/CasaOS/releases/tag/v0.4.28) and [CasaOS-UI v0.4.30](https://github.com/alvins82/CasaOS-UI/releases/tag/v0.4.30), including the systemd power-action and shutdown UI fixes. PRs: [CasaOS #26](https://github.com/alvins82/CasaOS/pull/26) and [CasaOS-UI #15](https://github.com/alvins82/CasaOS-UI/pull/15).
- **2026-08-15 — [CasaOS v0.4.28](https://github.com/alvins82/CasaOS/releases/tag/v0.4.28):** Fixed Dashboard shutdown and restart actions on systemd-based hosts by using explicit systemd power targets, propagating command failures, and advancing the bundled UI to [CasaOS-UI v0.4.30](https://github.com/alvins82/CasaOS-UI/releases/tag/v0.4.30). PRs: [CasaOS #26](https://github.com/alvins82/CasaOS/pull/26) and [CasaOS-UI #15](https://github.com/alvins82/CasaOS-UI/pull/15).
- **2026-08-15 — [CasaOS-UI v0.4.30](https://github.com/alvins82/CasaOS-UI/releases/tag/v0.4.30):** Kept restart polling while stopping shutdown reloads and surfacing power API failures. PR: [CasaOS-UI #15](https://github.com/alvins82/CasaOS-UI/pull/15).
- **2026-08-14 — [CasaOS Install v0.4.32](https://github.com/alvins82/CasaOS-Install/releases/tag/v0.4.32):** Published the full fork bundle with [CasaOS-LocalStorage v0.4.25](https://github.com/alvins82/CasaOS-LocalStorage/releases/tag/v0.4.25), which restores persisted merged-storage mounts before creating default `/DATA` directories. PR: [CasaOS-LocalStorage #9](https://github.com/alvins82/CasaOS-LocalStorage/pull/9).
- **2026-08-14 — [CasaOS Install v0.4.31](https://github.com/alvins82/CasaOS-Install/releases/tag/v0.4.31):** Published the full fork bundle with [CasaOS v0.4.27](https://github.com/alvins82/CasaOS/releases/tag/v0.4.27), [CasaOS-UI v0.4.29](https://github.com/alvins82/CasaOS-UI/releases/tag/v0.4.29), and [CasaOS-LocalStorage v0.4.24](https://github.com/alvins82/CasaOS-LocalStorage/releases/tag/v0.4.24). The bundle adds host-level SMB zeroconf discovery through mDNS/DNS-SD and Windows Web Service Discovery without re-enabling SMB1, with optional discovery packages handled gracefully.
- **2026-08-14 — [CasaOS v0.4.27](https://github.com/alvins82/CasaOS/releases/tag/v0.4.27):** Added host-level SMB zeroconf discovery through Samba mDNS registration and an unprivileged `wsdd` service, with LAN-interface selection, administrator-config preservation, and lifecycle integration ([CasaOS #23](https://github.com/alvins82/CasaOS/pull/23)).
- **2026-08-13 — [CasaOS Install v0.4.30](https://github.com/alvins82/CasaOS-Install/releases/tag/v0.4.30):** Published the full fork bundle with [CasaOS v0.4.26](https://github.com/alvins82/CasaOS/releases/tag/v0.4.26), [CasaOS-UI v0.4.29](https://github.com/alvins82/CasaOS-UI/releases/tag/v0.4.29), and [CasaOS-LocalStorage v0.4.24](https://github.com/alvins82/CasaOS-LocalStorage/releases/tag/v0.4.24). It creates missing `Documents`, `Downloads`, `Gallery`, and `Media` directories in external merged storage while preserving system `AppData` behavior. The only merged PR since v0.4.29 is [CasaOS-LocalStorage #7](https://github.com/alvins82/CasaOS-LocalStorage/pull/7).
- **2026-08-13 — [CasaOS Install v0.4.29](https://github.com/alvins82/CasaOS-Install/releases/tag/v0.4.29):** Published the full fork bundle with [CasaOS v0.4.26](https://github.com/alvins82/CasaOS/releases/tag/v0.4.26), [CasaOS-UI v0.4.29](https://github.com/alvins82/CasaOS-UI/releases/tag/v0.4.29), and [CasaOS-LocalStorage v0.4.23](https://github.com/alvins82/CasaOS-LocalStorage/releases/tag/v0.4.23). The bundle includes the Files dialog, single-surface dashboard scrolling, corrected hidden-files icons, storage-volume rename controls, and the matching LocalStorage rename API. PRs: [CasaOS #21](https://github.com/alvins82/CasaOS/pull/21), [CasaOS-UI #11](https://github.com/alvins82/CasaOS-UI/pull/11), [CasaOS-UI #12](https://github.com/alvins82/CasaOS-UI/pull/12), [CasaOS-UI #13](https://github.com/alvins82/CasaOS-UI/pull/13), [CasaOS-UI #14](https://github.com/alvins82/CasaOS-UI/pull/14), and [CasaOS-LocalStorage #6](https://github.com/alvins82/CasaOS-LocalStorage/pull/6).
- **2026-08-13 — [CasaOS v0.4.26](https://github.com/alvins82/CasaOS/releases/tag/v0.4.26):** Advanced the UI submodule to the v0.4.29 component release so Files opens in an App Store-style dialog and the latest dashboard, hidden-files, and storage rename changes ship in the core bundle. PR: [CasaOS #21](https://github.com/alvins82/CasaOS/pull/21).
- **2026-08-13 — [CasaOS-UI v0.4.29](https://github.com/alvins82/CasaOS-UI/releases/tag/v0.4.29):** Opened Files in an App Store-style dialog, made the dashboard use one scroll surface, corrected the hidden-files eye glyphs, and added storage-volume rename controls. PRs: [CasaOS-UI #11](https://github.com/alvins82/CasaOS-UI/pull/11), [CasaOS-UI #12](https://github.com/alvins82/CasaOS-UI/pull/12), [CasaOS-UI #13](https://github.com/alvins82/CasaOS-UI/pull/13), and [CasaOS-UI #14](https://github.com/alvins82/CasaOS-UI/pull/14).
- **2026-08-13 — [CasaOS-LocalStorage v0.4.23](https://github.com/alvins82/CasaOS-LocalStorage/releases/tag/v0.4.23):** Added protected storage-volume renaming and immediate filesystem-label refresh after a successful rename. PR: [CasaOS-LocalStorage #6](https://github.com/alvins82/CasaOS-LocalStorage/pull/6).
- **2026-08-13 — [CasaOS Install v0.4.26](https://github.com/alvins82/CasaOS-Install/releases/tag/v0.4.26):** Published the full fork bundle with [CasaOS v0.4.25](https://github.com/alvins82/CasaOS/releases/tag/v0.4.25), [CasaOS-UI v0.4.28](https://github.com/alvins82/CasaOS-UI/releases/tag/v0.4.28), and [CasaOS-LocalStorage v0.4.22](https://github.com/alvins82/CasaOS-LocalStorage/releases/tag/v0.4.22). The installer verifies the bundled components, compatibility overlay, manifest, and component lock with SHA-256 checksums.
- **2026-08-13 — [CasaOS v0.4.25](https://github.com/alvins82/CasaOS/releases/tag/v0.4.25):** Added authenticated Debian-family system package updates with live progress and completion reconciliation, kept system storage out of merged `/DATA` branches while preserving `/DATA/AppData`, advanced fork update discovery, and documented the fork component releases. PRs: [CasaOS #16](https://github.com/alvins82/CasaOS/pull/16), [CasaOS #17](https://github.com/alvins82/CasaOS/pull/17), [CasaOS #18](https://github.com/alvins82/CasaOS/pull/18), [CasaOS #19](https://github.com/alvins82/CasaOS/pull/19), [CasaOS #20](https://github.com/alvins82/CasaOS/pull/20), [CasaOS-UI #7](https://github.com/alvins82/CasaOS-UI/pull/7), and [CasaOS-LocalStorage #4](https://github.com/alvins82/CasaOS-LocalStorage/pull/4).
- **2026-08-13 — [CasaOS-UI v0.4.28](https://github.com/alvins82/CasaOS-UI/releases/tag/v0.4.28):** Added the persistent Show Search Bar setting, removed sidebar clipping, showed physical disk ownership and accurate filesystem usage, and added the in-dashboard system package update flow with completion reconciliation. PRs: [CasaOS-UI #8](https://github.com/alvins82/CasaOS-UI/pull/8), [CasaOS-UI #9](https://github.com/alvins82/CasaOS-UI/pull/9), [CasaOS-UI #10](https://github.com/alvins82/CasaOS-UI/pull/10).
- **2026-08-13 — [CasaOS-LocalStorage v0.4.22](https://github.com/alvins82/CasaOS-LocalStorage/releases/tag/v0.4.22):** Reported nested filesystem usage accurately and retained physical disk ownership in storage entries. PR: [CasaOS-LocalStorage #5](https://github.com/alvins82/CasaOS-LocalStorage/pull/5).
- **2026-08-12 — [v0.4.25](https://github.com/alvins82/CasaOS-Install/releases/tag/v0.4.25):** Kept system storage out of merged `/DATA` storage while preserving system AppData at `/DATA/AppData`, so installations on flash drives can use external disks for media storage.
- **2026-08-12 — [v0.4.21](https://github.com/alvins82/CasaOS-Install/releases/tag/v0.4.21):** Fixed fresh one-line installs and CasaOS Storage Manager merges on mergerfs 2.40, including persistent multi-disk pools on Ubuntu 26. PRs: [LocalStorage #2](https://github.com/alvins82/CasaOS-LocalStorage/pull/2), [LocalStorage #3](https://github.com/alvins82/CasaOS-LocalStorage/pull/3), [installer #11](https://github.com/alvins82/CasaOS-Install/pull/11).
- **2026-08-12 — [CasaOS-UI v0.4.25](https://github.com/alvins82/CasaOS-UI/releases/tag/v0.4.25):** Added the dedicated merged-storage tab, framed in-page app dialogs like App Store modals, applied the existing CasaOS zoom transition, and retained the close button with responsive full-screen behavior on mobile. PRs: [CasaOS-UI #1](https://github.com/alvins82/CasaOS-UI/pull/1), [CasaOS-UI #3](https://github.com/alvins82/CasaOS-UI/pull/3).
- **2026-08-12 — [v0.4.21](https://github.com/alvins82/CasaOS-Install/releases/tag/v0.4.21):** Added the CasaOS-UI v0.4.21 in-page app launcher so installed apps open inside the dashboard with a close button. Also fixed fresh one-line installs and CasaOS Storage Manager merges on mergerfs 2.40, including persistent multi-disk pools on Ubuntu 26. PRs: [CasaOS-UI #2](https://github.com/alvins82/CasaOS-UI/pull/2), [CasaOS #14](https://github.com/alvins82/CasaOS/pull/14), [CasaOS-Install #12](https://github.com/alvins82/CasaOS-Install/pull/12), [LocalStorage #2](https://github.com/alvins82/CasaOS-LocalStorage/pull/2), [LocalStorage #3](https://github.com/alvins82/CasaOS-LocalStorage/pull/3), [installer #11](https://github.com/alvins82/CasaOS-Install/pull/11).
- **2026-08-12 — [v0.4.20](https://github.com/alvins82/CasaOS-Install/releases/tag/v0.4.20):** Fixed OneDrive identity resolution by falling back to `createdBy.user.displayName` when Microsoft Graph omits `createdBy.user.email`, and returning a clear error when neither field is available. PR: [CasaOS #2530](https://github.com/IceWhaleTech/CasaOS/pull/2530).
- **2026-08-11 — [v0.4.19](https://github.com/alvins82/CasaOS-Install/releases/tag/v0.4.19):** Fixed in-app updates by running the installer outside the CasaOS service process group and preserving upgrade logs. PRs: [CasaOS #9](https://github.com/alvins82/CasaOS/pull/9), [installer #10](https://github.com/alvins82/CasaOS-Install/pull/10).
- **2026-08-11 — [v0.4.18](https://github.com/alvins82/CasaOS-Install/releases/tag/v0.4.18):** Made release naming platform-neutral while retaining automatic distro detection and amd64, arm64, and armv7 packages in one installer. PRs: [CasaOS #8](https://github.com/alvins82/CasaOS/pull/8), [installer #9](https://github.com/alvins82/CasaOS-Install/pull/9).
- **2026-08-11 — [v0.4.17-ubuntu26.3](https://github.com/alvins82/CasaOS-Install/releases/tag/v0.4.17-ubuntu26.3):** Routed dashboard update discovery and installation through CasaOS-Install releases so upstream updates cannot replace fork patches. PRs: [CasaOS #7](https://github.com/alvins82/CasaOS/pull/7), [installer #8](https://github.com/alvins82/CasaOS-Install/pull/8).
- **2026-08-11 — [v0.4.17-ubuntu26.2](https://github.com/alvins82/CasaOS-Install/releases/tag/v0.4.17-ubuntu26.2):** Added Ubuntu 26.04 and Docker 24–29 compatibility, multi-architecture patched components, clean-install and reboot validation, and a single-command installer. PRs: [CasaOS #1](https://github.com/alvins82/CasaOS/pull/1), [AppManagement #1](https://github.com/alvins82/CasaOS-AppManagement/pull/1), [installer #2](https://github.com/alvins82/CasaOS-Install/pull/2).

</details>

## Upstream credits

CasaOS is the work of IceWhale and its contributors. Their copyright notices are kept throughout the source, as the licence requires.

The file-root confinement (`pkg/fileroots`) is derived from [EdmundFu-233/ReCasaOS](https://github.com/EdmundFu-233/ReCasaOS) `pkg/filesecurity`, Apache License 2.0.

## Licence

Apache License 2.0 — see [LICENSE](LICENSE). CasaOS and the CasaOS logo are marks of IceWhale; this distribution uses the name to say what it is a release of, and nothing more.
