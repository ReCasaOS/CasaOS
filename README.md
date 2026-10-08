# CasaOS

> **Not affiliated with IceWhale.**
> This is an independent, community-maintained distribution of CasaOS. It is not produced, endorsed, sponsored, certified or supported by Shanghai IceWhale Technology Limited or by any of the upstream CasaOS maintainers.
>
> CASAOS is a trademark of Shanghai IceWhale Technology Limited. The name is used here only to identify the upstream project that this software is a distribution of, as permitted for referential use; no rights in the CasaOS name or logo are claimed, and no affiliation is implied.
>
> The original project is [IceWhaleTech/CasaOS](https://github.com/IceWhaleTech/CasaOS). Please do not report problems with this distribution to IceWhale — open them at [ReCasaOS/CasaOS/issues](https://github.com/ReCasaOS/CasaOS/issues).

A personal cloud for the home: a dashboard, an app store, Docker apps, files and shares on your own hardware. This repository is the core service of **ReCasaOS**, a maintained release of the project after upstream [IceWhaleTech/CasaOS](https://github.com/IceWhaleTech/CasaOS) stopped shipping in 2025.

<p align="center">
    <a href="https://github.com/ReCasaOS/CasaOS-Install/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/ReCasaOS/CasaOS-Install?color=162453&style=flat-square&label=Distribution" /></a>
    <a href="https://github.com/ReCasaOS/CasaOS/blob/main/LICENSE"><img alt="Licence" src="https://img.shields.io/github/license/ReCasaOS/CasaOS?color=162453&style=flat-square&label=License" /></a>
    <a href="https://github.com/ReCasaOS/CasaOS/issues"><img alt="Issues" src="https://img.shields.io/github/issues/ReCasaOS/CasaOS?color=162453&style=flat-square&label=Issues" /></a>
    <a href="https://github.com/ReCasaOS/CasaOS/pulls"><img alt="Pull requests" src="https://img.shields.io/github/issues-pr/ReCasaOS/CasaOS?color=162453&style=flat-square&label=PRs" /></a>
    <br/><br/>
    <kbd>
      <picture>
          <source media="(prefers-color-scheme: dark)" srcset="snapshot-dark.jpg">
          <source media="(prefers-color-scheme: light)" srcset="snapshot-light.jpg">
          <img alt="CasaOS dashboard" src="snapshot-light.jpg">
      </picture>
    </kbd>
</p>

## Install

```sh
curl -fsSL https://github.com/ReCasaOS/CasaOS-Install/releases/latest/download/install.sh | sudo bash
```

One command for every supported system and architecture; the installer detects both at run time. Run it again to upgrade, or use the update button in the dashboard, which follows this distribution's releases.

**Migrating from IceWhale's or alvins82's installer** works the same way, in place, without uninstalling first. The installer stops and restarts the CasaOS services while upgrading, so back up what matters. Afterwards, `cat /var/lib/casaos/fork-release` prints the distribution release installed. Do not use `get.casaos.io/update` after migrating: it installs IceWhale's frozen component bundle over this one.

**If the dashboard reports you are on the latest version and never offers an update**, run the install command once by hand. Up to v0.4.41, four files shipped here still carried the previous fork's release URLs, and one of them rewrote `/etc/casaos/casaos.conf` on every install, so a host installed from this distribution polled a feed whose newest release is older than what it was already running. From CasaOS-Install v0.4.42 onwards, re-running the installer corrects the configuration in place, and the update button follows this distribution on its own afterwards.

Everything the installer downloads is verified against a SHA-256 digest before extraction. What is in each release, and how a release is built, is described in [CasaOS-Install](https://github.com/ReCasaOS/CasaOS-Install#readme).

### Compatibility

Architectures: amd64, arm64, armv7.

Tested: Debian 12, Ubuntu Server 20.04 and 26.04, Raspberry Pi OS. Reported working by the community: Elementary 6.1, Armbian 22.04. Not fully tested: Alpine, OpenWrt, Arch.

Docker 28 and 29 are tested on every release, and the installer accepts Docker 20 or later (24.0.5 and 20.10.24 were also tried by hand on v0.5.16). The Docker 29 incompatibility that broke the upstream release is fixed here, without the `DOCKER_MIN_API_VERSION` workaround.

### Uninstall

```sh
sudo casaos-uninstall
```

## What this distribution changes

Relative to upstream, through alvins82's fork and then here. The full list per release is in [CHANGELOG.md](CHANGELOG.md).

- **Sharing** — Samba shares can be restricted to an account, managed from the dashboard and separate from the CasaOS login; shares convert between guest and account access in place. Upstream shares were world-writable, with files created as root, and Windows 10 and 11 refuse guest SMB entirely. Hosts advertise themselves over mDNS and Windows discovery without SMB1.
- **Apps** — The `docker-compose.yml` of an installed app is editable from its settings, with validation before apply. Apps open inside CasaOS or in a new tab, per app. System package updates run from the dashboard.
- **Storage** — External disks form the merged `/DATA` while the system disk stays out of it; volumes can be renamed; merge mounts are restored across reboots and slow disks.
- **Network** — The gateway serves HTTPS with a certificate you supply.
- **Security** — Routes that act as root on the host require a token even from loopback. The dashboard no longer sends a machine fingerprint to a third party, and the core no longer carries the helper and configuration entries that pointed at IceWhale's `api.casaos.io`. The install-time migration script no longer geo-locates the host either: it asked `ipconfig.io`, then `ifconfig.io`, for the country on every install and every upgrade in order to pick a download mirror. Migration tools come from GitHub, and the domain is a constant rather than a setting: what it points at is downloaded and run as root without verification.
- **Updater** — Update discovery and installation follow this distribution's releases rather than IceWhale's frozen bundle, in the binary, in the shipped configuration samples, and in the setup script that writes the running host's configuration.
- **Platform** — Docker 29 and Ubuntu 26 support; every component is built and released from CI, with digests published alongside.
- **Packaging** — The Go module is `github.com/ReCasaOS/CasaOS`, built against the forked `github.com/ReCasaOS/CasaOS-Common`, so log lines, stack traces and `go version -m` on the shipped binary name this distribution instead of IceWhale.

## Features

- A dashboard designed for the home: no forms, no configuration files to learn
- An app store with one-click installs — Nextcloud, Home Assistant, AdGuard, Jellyfin, the *arr stack and more — and any Docker image beyond it
- File management and network shares that need no technical background
- Widgets for what you care about: resource usage, app status, storage

## Community and contributing

Bug reports and requests go to [issues on this repository](https://github.com/ReCasaOS/CasaOS/issues), whichever component is involved. Pull requests are welcome on any of the component repositories under [ReCasaOS](https://github.com/orgs/ReCasaOS/repositories); each has its own release workflow, and the installer pins them.

The upstream Discord, wiki and website belong to IceWhale and are not run by this distribution.

## Repositories

| Repository | Role |
|---|---|
| [CasaOS](https://github.com/ReCasaOS/CasaOS) | this one: system API, files, shares, updater |
| [CasaOS-UI](https://github.com/ReCasaOS/CasaOS-UI) | the dashboard (a submodule here) |
| [CasaOS-AppManagement](https://github.com/ReCasaOS/CasaOS-AppManagement) | apps, Compose, the app store client |
| [CasaOS-Gateway](https://github.com/ReCasaOS/CasaOS-Gateway) | reverse proxy in front of every service |
| [CasaOS-UserService](https://github.com/ReCasaOS/CasaOS-UserService) | accounts and login |
| [CasaOS-MessageBus](https://github.com/ReCasaOS/CasaOS-MessageBus) | events between services |
| [CasaOS-LocalStorage](https://github.com/ReCasaOS/CasaOS-LocalStorage) | disks, volumes, merged storage |
| [CasaOS-Install](https://github.com/ReCasaOS/CasaOS-Install) | the installer and the release bundle |

## Development

This repository builds `casaos`, the core service. Behind the gateway it serves the `/v1` API — system information and power actions, listening ports, the file manager (`/v1/file`, `/v1/folder`, `/v1/batch`, `/v1/image`), Samba shares and accounts (`/v1/samba`), cloud drivers, and status notifications from the other services (`/v1/notify`) — together with a `/v2` API generated from [`api/casaos/openapi.yaml`](api/casaos/openapi.yaml). It also polls this distribution's release feed and runs the installer when the dashboard asks for an update.

Its configuration is `/etc/casaos/casaos.conf`, created on first run from [the sample embedded in the binary](build/sysroot/etc/casaos/casaos.conf.sample). Its database and user data live under `/var/lib/casaos`, its logs under `/var/log/casaos`, and the shell helpers it shells out to under `/usr/share/casaos/shell`. The dashboard is the `UI` submodule and is built separately.

Go 1.21 or newer is the only requirement; the Go build does not need the submodule.

```sh
CGO_ENABLED=0 GOOS=linux go build .
go test ./...
```

`TestPorts` reads the host's own listening sockets, so it fails in a container that has none.

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

CasaOS is the work of IceWhale and its contributors. Their copyright notices are kept throughout the source, as the licence requires, and their contributor list is kept here as it stood.

<!-- ALL-CONTRIBUTORS-LIST:START - Do not remove or modify this section -->
<!-- prettier-ignore-start -->
<!-- markdownlint-disable -->
<table>
  <tr>
    <td align="center"><a href="https://github.com/jerrykuku"><img src="https://avatars.githubusercontent.com/u/9485680?v=4?s=100" width="100px;" alt=""/><br /><sub><b>老竭力</b></sub></a><br /><a href="https://github.com/IceWhaleTech/CasaOS/commits?author=jerrykuku" title="Code">💻</a> <a href="https://github.com/IceWhaleTech/CasaOS/commits?author=jerrykuku" title="Documentation">📖</a> <a href="#ideas-jerrykuku" title="Ideas, Planning, & Feedback">🤔</a> <a href="#infra-jerrykuku" title="Infrastructure (Hosting, Build-Tools, etc)">🚇</a> <a href="#maintenance-jerrykuku" title="Maintenance">🚧</a> <a href="#platform-jerrykuku" title="Packaging/porting to new platform">📦</a> <a href="#question-jerrykuku" title="Answering Questions">💬</a> <a href="https://github.com/IceWhaleTech/CasaOS/pulls?q=is%3Apr+reviewed-by%3Ajerrykuku" title="Reviewed Pull Requests">👀</a></td>
    <td align="center"><a href="https://github.com/LinkLeong"><img src="https://avatars.githubusercontent.com/u/13556972?v=4?s=100" width="100px;" alt=""/><br /><sub><b>link</b></sub></a><br /><a href="https://github.com/IceWhaleTech/CasaOS/commits?author=LinkLeong" title="Code">💻</a> <a href="https://github.com/IceWhaleTech/CasaOS/commits?author=LinkLeong" title="Documentation">📖</a> <a href="#ideas-LinkLeong" title="Ideas, Planning, & Feedback">🤔</a> <a href="#infra-LinkLeong" title="Infrastructure (Hosting, Build-Tools, etc)">🚇</a> <a href="#maintenance-LinkLeong" title="Maintenance">🚧</a> <a href="#question-LinkLeong" title="Answering Questions">💬</a> <a href="https://github.com/IceWhaleTech/CasaOS/pulls?q=is%3Apr+reviewed-by%3ALinkLeong" title="Reviewed Pull Requests">👀</a></td>
    <td align="center"><a href="https://github.com/tigerinus"><img src="https://avatars.githubusercontent.com/u/7172560?v=4?s=100" width="100px;" alt=""/><br /><sub><b>太戈</b></sub></a><br /><a href="https://github.com/IceWhaleTech/CasaOS/commits?author=tigerinus" title="Code">💻</a> <a href="https://github.com/IceWhaleTech/CasaOS/commits?author=tigerinus" title="Documentation">📖</a> <a href="#ideas-tigerinus" title="Ideas, Planning, & Feedback">🤔</a> <a href="#infra-tigerinus" title="Infrastructure (Hosting, Build-Tools, etc)">🚇</a> <a href="#maintenance-tigerinus" title="Maintenance">🚧</a> <a href="#mentoring-tigerinus" title="Mentoring">🧑‍🏫</a> <a href="#security-tigerinus" title="Security">🛡️</a> <a href="#question-tigerinus" title="Answering Questions">💬</a> <a href="https://github.com/IceWhaleTech/CasaOS/pulls?q=is%3Apr+reviewed-by%3Atigerinus" title="Reviewed Pull Requests">👀</a></td>
    <td align="center"><a href="https://github.com/Lauren-ED209"><img src="https://avatars.githubusercontent.com/u/8243355?v=4?s=100" width="100px;" alt=""/><br /><sub><b>Lauren</b></sub></a><br /><a href="#ideas-Lauren-ED209" title="Ideas, Planning, & Feedback">🤔</a> <a href="#fundingFinding-Lauren-ED209" title="Funding Finding">🔍</a> <a href="#projectManagement-Lauren-ED209" title="Project Management">📆</a> <a href="#question-Lauren-ED209" title="Answering Questions">💬</a> <a href="https://github.com/IceWhaleTech/CasaOS/commits?author=Lauren-ED209" title="Tests">⚠️</a></td>
    <td align="center"><a href="https://JohnGuan.Cn"><img src="https://avatars.githubusercontent.com/u/3358477?v=4?s=100" width="100px;" alt=""/><br /><sub><b>John Guan</b></sub></a><br /><a href="#blog-JohnGuan" title="Blogposts">📝</a> <a href="#content-JohnGuan" title="Content">🖋</a> <a href="https://github.com/IceWhaleTech/CasaOS/commits?author=JohnGuan" title="Documentation">📖</a> <a href="#ideas-JohnGuan" title="Ideas, Planning, & Feedback">🤔</a> <a href="#eventOrganizing-JohnGuan" title="Event Organizing">📋</a> <a href="#mentoring-JohnGuan" title="Mentoring">🧑‍🏫</a> <a href="#question-JohnGuan" title="Answering Questions">💬</a> <a href="https://github.com/IceWhaleTech/CasaOS/pulls?q=is%3Apr+reviewed-by%3AJohnGuan" title="Reviewed Pull Requests">👀</a></td>
    <td align="center"><a href="https://blog.tippybits.com"><img src="https://avatars.githubusercontent.com/u/17506770?v=4?s=100" width="100px;" alt=""/><br /><sub><b>David Tippett</b></sub></a><br /><a href="https://github.com/IceWhaleTech/CasaOS/commits?author=dtaivpp" title="Documentation">📖</a> <a href="#ideas-dtaivpp" title="Ideas, Planning, & Feedback">🤔</a> <a href="#question-dtaivpp" title="Answering Questions">💬</a></td>
    <td align="center"><a href="https://github.com/zarevskaya"><img src="https://avatars.githubusercontent.com/u/60230221?v=4?s=100" width="100px;" alt=""/><br /><sub><b>Skaya</b></sub></a><br /><a href="#mentoring-zarevskaya" title="Mentoring">🧑‍🏫</a> <a href="#question-zarevskaya" title="Answering Questions">💬</a> <a href="#tutorial-zarevskaya" title="Tutorials">✅</a> <a href="#translation-zarevskaya" title="Translation">🌍</a></td>
  </tr>
  <tr>
    <td align="center"><a href="https://github.com/AuthorShin"><img src="https://avatars.githubusercontent.com/u/4959043?v=4?s=100" width="100px;" alt=""/><br /><sub><b>AuthorShin</b></sub></a><br /><a href="https://github.com/IceWhaleTech/CasaOS/commits?author=AuthorShin" title="Tests">⚠️</a> <a href="https://github.com/IceWhaleTech/CasaOS/issues?q=author%3AAuthorShin" title="Bug reports">🐛</a> <a href="#question-AuthorShin" title="Answering Questions">💬</a> <a href="#ideas-AuthorShin" title="Ideas, Planning, & Feedback">🤔</a></td>
    <td align="center"><a href="https://github.com/baptiste313"><img src="https://avatars.githubusercontent.com/u/93325157?v=4?s=100" width="100px;" alt=""/><br /><sub><b>baptiste313</b></sub></a><br /><a href="#translation-baptiste313" title="Translation">🌍</a></td>
    <td align="center"><a href="https://github.com/DrMxrcy"><img src="https://avatars.githubusercontent.com/u/58747968?v=4?s=100" width="100px;" alt=""/><br /><sub><b>DrMxrcy</b></sub></a><br /><a href="https://github.com/IceWhaleTech/CasaOS/commits?author=DrMxrcy" title="Tests">⚠️</a> <a href="#ideas-DrMxrcy" title="Ideas, Planning, & Feedback">🤔</a> <a href="#question-DrMxrcy" title="Answering Questions">💬</a></td>
    <td align="center"><a href="https://github.com/Joooost"><img src="https://avatars.githubusercontent.com/u/12090673?v=4?s=100" width="100px;" alt=""/><br /><sub><b>Joooost</b></sub></a><br /><a href="#ideas-Joooost" title="Ideas, Planning, & Feedback">🤔</a></td>
    <td align="center"><a href="https://potyarkin.ml"><img src="https://avatars.githubusercontent.com/u/334908?v=4?s=100" width="100px;" alt=""/><br /><sub><b>Vitaly Potyarkin</b></sub></a><br /><a href="#ideas-sio" title="Ideas, Planning, & Feedback">🤔</a></td>
    <td align="center"><a href="https://github.com/bearfrieze"><img src="https://avatars.githubusercontent.com/u/1023813?v=4?s=100" width="100px;" alt=""/><br /><sub><b>Bjørn Friese</b></sub></a><br /><a href="#ideas-bearfrieze" title="Ideas, Planning, & Feedback">🤔</a></td>
    <td align="center"><a href="https://github.com/Protektor-Desura"><img src="https://avatars.githubusercontent.com/u/1195496?v=4?s=100" width="100px;" alt=""/><br /><sub><b>Protektor</b></sub></a><br /><a href="https://github.com/IceWhaleTech/CasaOS/issues?q=author%3AProtektor-Desura" title="Bug reports">🐛</a> <a href="#ideas-Protektor-Desura" title="Ideas, Planning, & Feedback">🤔</a> <a href="#question-Protektor-Desura" title="Answering Questions">💬</a></td>
  </tr>
  <tr>
    <td align="center"><a href="https://github.com/llwaini"><img src="https://avatars.githubusercontent.com/u/59589857?v=4?s=100" width="100px;" alt=""/><br /><sub><b>llwaini</b></sub></a><br /><a href="#projectManagement-llwaini" title="Project Management">📆</a> <a href="https://github.com/IceWhaleTech/CasaOS/commits?author=llwaini" title="Tests">⚠️</a> <a href="#tutorial-llwaini" title="Tutorials">✅</a></td>
    <td align="center"><a href="https://github.com/CorrectRoadH"><img src="https://avatars.githubusercontent.com/u/29306285?v=4?s=100" width="100px;" alt=""/><br /><sub><b>CorrectRoadH</b></sub></a><br /><a href="https://github.com/IceWhaleTech/CasaOS/commits?author=correctroadh" title="Code">💻</a> <a href="https://github.com/IceWhaleTech/CasaOS/commits?author=correctroadh" title="Documentation">📖</a></td>
    <td align="center"><a href="https://github.com/zhanghengxin"><img src="https://avatars.githubusercontent.com/u/24197448?v=4?s=100" width="100px;" alt=""/><br /><sub><b>zhanghengxin</b></sub></a><br /><a href="https://github.com/IceWhaleTech/CasaOS/commits?author=zhanghengxin" title="Code">💻</a> <a href="https://github.com/IceWhaleTech/CasaOS/commits?author=zhanghengxin" title="Documentation">📖</a></td>
  </tr>
</table>

<!-- markdownlint-restore -->
<!-- prettier-ignore-end -->

<!-- ALL-CONTRIBUTORS-LIST:END -->

## Licence

Apache License 2.0 — see [LICENSE](LICENSE). CasaOS and the CasaOS logo are marks of IceWhale; this distribution uses the name to say what it is a release of, and nothing more.
