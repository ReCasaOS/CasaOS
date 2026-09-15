# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.4.54] - 2026-09-15

### Fixed

- **Built without UPX, this time for real.** 0.4.53 said the binaries were no longer packed with UPX. They were: the change never reached `.goreleaser.yaml`, so every amd64 and armv7 build since kept going through UPX 3.96. A packed binary exits with status 127 when its stub cannot unpack itself, which is how app-management failed its first start on the install checks of v0.4.83 and v0.4.88, with nothing logged. The hooks are gone now.

## [0.4.53] - 2026-09-13

### Fixed

- **A token stays valid while user-service restarts** (Common v0.4.25). The signing key was asked of user-service every ten seconds and every token failed the moment it did not answer, restarting or held still for a backup of the box. The key last seen serves until it answers again.
- **Built without UPX.** On the amd64 leg of the v0.4.83 install check a first start exited 127, the UPX stub's own code, once in a run of five services starting at once; the same binary ran by hand. The binaries are static and stripped already.

## [0.4.52] - 2026-09-13

### Fixed

- **Loopback is not an identity.** Any request from 127.0.0.1 skipped the token. Loopback is not this box's services alone: a container on the host network, or any local account, reaches the same addresses. A request skips the token only when it is one of this box's services, which come from loopback with the secret the gateway writes for this boot (`/var/run/casaos/internal.secret`, readable by root only); the dashboard always had a token. The hand-kept list of root-privileged routes that already refused loopback is gone with the exemption itself: every route here acts as root, the file manager on any path. `casaos-cli`, which relied on the exemption, needs a token now.
- **`/v1/sys/debug` is behind the token.** The bug-report template (OS, version, disks, the configuration) sat outside the group, readable by anyone on the network.

### Changed

- echo 4.15 with echo-jwt, x/crypto 0.57, x/net 0.59, Go 1.26 (1.21 has been out of support since 2024).

## [0.4.50] - 2026-09-10

### Changed

- **The API documentation says whose project this is.** `api/casaos/openapi.yaml` is embedded verbatim by `//go:embed` and served at `/doc`, so every URL in it is plaintext inside the shipped binary and every image in it is fetched by the reader's browser. It opened with IceWhale's banner, hosted in `IceWhaleTech/logo`, and closed by sending anyone with a problem to IceWhale's repository and Discord. The banner is gone — there is no ReCasaOS banner to put in its place, and a broken image is better than someone else's — and the link now points at this distribution's own issues.
- The new-issue chooser no longer sends questions about this distribution to IceWhale's discussions or their Discord, and the `[Alpha Only]` bug form, whose only instruction was to join that Discord first, is removed. The App Request link stays where it is: adding an app to the catalogue is a request to the catalogue's curator, and the catalogue is IceWhale's.

### Removed

- `delete-old-service.sh`, which shipped into `/usr/share/casaos/shell` on every box and queried IceWhale's release API from a script nothing has invoked since the single-binary days.

### Added

- A test that reads every file the installer ships and fails on any that names IceWhale, with the catalogue, the icon CDN, the cloud OAuth host and the migration lists excused by name. The old guard checked four files it was handed; this one asks the opposite question, so a file that nobody thought to add has to be excused on purpose rather than merely overlooked. Run against the previous commit it names `delete-old-service.sh`.

## [0.4.49] - 2026-09-10

### Changed

- The Go module is now `github.com/ReCasaOS/CasaOS` and it builds against `github.com/ReCasaOS/CasaOS-Common v0.4.23`. The repositories moved to the ReCasaOS organisation, and a module path is not a URL that follows a redirect: the tool compares the path declared in `go.mod` with the path it was asked for. Every log line, every stack trace and `go version -m` on a running box now name the project where it actually lives.
- `.goreleaser.yaml` follows the same rename, including the `-X github.com/ReCasaOS/CasaOS/drivers/...` flags that address package variables by import path. Left on the old path they would have become silent no-ops. They are inert either way: the release workflow builds with `go build` and its own `-ldflags`, and has not run GoReleaser for some time — so the cloud-drive credentials those flags exist to inject have never reached a released binary, and `client_id` ships as its default, `"private build"`. That is unchanged by this release and is recorded here because it was found while checking the rename.
- `FORK_RELEASE_VERSION` is a floor, not a second copy of the distribution tag, and this component no longer ships with every distribution release because of it. It is the distribution this binary was BUILT for; `/var/lib/casaos/fork-release`, which the installer writes on every install and every upgrade, is the truth, and wherever the two differ that marker exists and wins. A host with no marker has not run a recent installer, so it is running the binary that shipped with the distribution the constant names — which makes the constant the honest answer for exactly the hosts that fall back to it. This supersedes the note on 0.4.46, which read the two as equal and made every distribution release drag a release of this component along for one string.
- Nothing else changed. The App Store catalogue, the icon CDN, the cloud OAuth host, the migration entries and the upstream copyright headers are untouched.

## [0.4.48] - 2026-09-09

### Changed

- `FORK_RELEASE_VERSION` carries `v0.4.58`, the distribution this binary ships in. Nothing else changed.

## [0.4.47] - 2026-09-09

### Changed

- `FORK_RELEASE_VERSION` carries `v0.4.57`, the distribution this binary ships in. Nothing else changed; this component ships with every distribution release because that constant is the fallback the dashboard reads when `/var/lib/casaos/fork-release` is missing, and that marker holds the distribution's tag.

## [0.4.46] - 2026-09-09

### Changed

- `FORK_RELEASE_VERSION` carries `v0.4.56`. It is the fallback the dashboard reads when `/var/lib/casaos/fork-release` is missing, and that file holds the distribution release, so the constant has to move with the distribution rather than with this component. Every distribution release ships this component for that reason; the installer stops and reinstalls every service on each run anyway, so it costs no restart that was not already happening.

## [0.4.45] - 2026-09-07

### Changed

- The Go directive is unchanged at `go 1.21`, but the shared library it now points at brings in `orca-zhang/ecache`, whose package `init()` starts a goroutine that sleeps in a loop for the lifetime of the process. Nothing here calls the cache it backs; the goroutine exists on import alone. The leak check in `service` was taught to ignore it.
- The Go module path is now `github.com/inkly/CasaOS` and the shared library dependency is `github.com/inkly/CasaOS-Common v0.4.22`, so every log line, stack trace and `go version -m` record on the shipped binary names this distribution rather than IceWhale. Only module paths were rewritten: issue links, the App Store and release URLs, and the upstream credits are untouched, and the generated API clients are byte-identical.

### Fixed

- `FORK_RELEASE_VERSION` carries the distribution release again, not this component's own tag. It is the fallback `CurrentVersion()` returns when `/var/lib/casaos/fork-release` is missing, and that file holds the distribution tag, which is also what `version.json` announces. The two matched while the numbering coincided and had drifted since, so a box without the marker compared 0.4.44 against a feed announcing 0.4.54 and was offered an update it already had, at every poll.

## [0.4.44] - 2026-09-07

### Changed

- The message-bus client is generated from this distribution's own tag (`inkly/CasaOS-MessageBus` at `v0.4.19`) instead of IceWhale's live `main` branch. `go generate` runs on every release tag, so until now a rename, a deletion or an edit upstream would have changed this binary or failed the build; the regenerated output is byte-identical.
- `.github/sync_openapi.yml` is gone. It called an unpinned reusable workflow hosted in an IceWhale repository and passed it a repository token. It sat outside `.github/workflows/`, so GitHub never ran it, but it was one `git mv` away from running.
- The install-time migration script no longer geo-locates the host. `__get_download_domain` curled `ipconfig.io/country`, falling back to `ifconfig.io/country_code`, at the top level of `build/scripts/migration/script.d/03-migrate-casaos.sh` — and `install.sh` runs every script in that directory on every install and every upgrade, so both third-party services were contacted each time regardless of whether a migration applied. Migration tools are fetched from `https://github.com/`, and the domain is a constant rather than a setting: what it points at is downloaded and run as root without verification. The migration lists are unchanged.

## [0.4.43] - 2026-09-06

### Removed

- [Config] The HTTP helper `httper.OasisGet`, which fetched a bearer token from IceWhale's `api.casaos.io` before every request it made, is gone: nothing in the codebase called it. The `ServerApi` field of the server configuration model, whose only reader was that helper, goes with it, as do the `ServerApi` and `Handshake` lines of the sample configuration that pointed at `api.casaos.io` and `socket.casaos.io`. An installed `/etc/casaos/casaos.conf` that still carries the two keys keeps working unchanged; they are simply ignored.

### Fixed

- [Notify] `go vet` reported two lock copies in the notification service: `GetList` took its receiver by value and `GetSystemTempMap` returned the `sync.Map` it holds by value, so each copied a map that embeds a mutex. Both now go through a pointer; the callers only range over the map, so nothing observable changes.

## [0.4.42] - 2026-09-04

### Fixed

- [Updater] The in-app updater pointed at another fork's release feed. Four shipped files still carried `alvins82/CasaOS-Install` URLs, and one of them — the setup script packaged into the installer's compatibility overlay — wrote them into `/etc/casaos/casaos.conf` on every install and upgrade, where the Go resolvers prefer them over this distribution's built-in URLs. An installed host therefore polled a feed whose latest release is older than what it was running, reported itself up to date forever, and would have fetched the other fork's `install.sh` had the update ever fired. The corrected setup script reaches a host through the installer's compatibility overlay, so existing installations need one manual re-run of the install command (`curl -fsSL https://github.com/ReCasaOS/CasaOS-Install/releases/latest/download/install.sh | sudo bash`) once CasaOS-Install v0.4.42 is published; after that the dashboard updater tracks this distribution on its own.
- [Gateway] The health check the gateway runs against its own listener retried forever instead of ten times: the countdown ran through an unsigned integer, so the last decrement wrapped around. A listener that never answered left the gateway probing it once a second for good, rather than returning the error its caller expects.

## [0.4.41] - 2026-09-04

### Added

- [Sharing] A share can be marked as a Time Machine destination. It gets Apple's SMB extensions (`vfs objects = catia fruit streams_xattr`, `fruit:time machine = yes`) and smbd advertises it over mDNS, so it shows up in Time Machine preferences on Macs on the network. Other shares are untouched, and a host without Samba's `vfs_fruit` module (`samba-vfs-modules` on Debian and Ubuntu) is told so instead of getting a share that refuses every connection ([CasaOS #1030](https://github.com/IceWhaleTech/CasaOS/issues/1030)).

### Fixed

- [Logging] journald no longer receives an access-log line for the internal system-status posts CasaOS-LocalStorage sends to loopback every 5 seconds; the dashboard telemetry rate is unchanged, and remote requests to those routes, like every route outside `/v1/notify/`, are still logged ([CasaOS #2211](https://github.com/IceWhaleTech/CasaOS/issues/2211)).
- [Dashboard] The updater panel showed the version twice over (`vv0.4.40`): `current_version` answered with the release tag, which carries its own `v`, while the dashboard adds one itself. The API now reports the bare version, as it always did upstream.

### Security

- [API] The file manager API (`/v1/file`, `/v1/folder`, `/v1/batch`, `/v1/image`) now requires a token from loopback as well: any local process, including a container on the host network, could read, write and delete files as root without one. Reported as [CasaOS #2566](https://github.com/IceWhaleTech/CasaOS/pull/2566), whose "path traversal" framing is wrong — the paths are absolute by design — and whose sanitizer is not adopted, because confining the file manager to a fixed set of roots would break browsing `/mnt` and `/media`, i.e. every USB drive and cloud mount.

## [0.4.40] - 2026-09-04

First release of the inkly distribution, cut from alvins82's v0.4.28.

### Added

- [Sharing] Restrict a Samba share to a dedicated account. Accounts are created through the API, are separate from the CasaOS login, have no shell and cannot log in to the host. Shares convert between guest and account access in place ([CasaOS #33](https://github.com/alvins82/CasaOS/pull/33)).
- [Sharing] `map to guest = never` when a protected share exists, and patched in place on hosts CasaOS already configured, so a rejected login prompts for a password instead of failing silently.
- [Sharing] The generated Samba configuration is checked with `testparm` before smbd is restarted, and rolled back if rejected; a bad configuration used to take network discovery down with it ([CasaOS #33](https://github.com/alvins82/CasaOS/pull/33)).

### Changed

- [Build] `.goreleaser.yaml` publishes to `inkly` instead of upstream; the UI submodule follows `inkly/CasaOS-UI`; the in-app updater follows `inkly/CasaOS-Install` releases.
- [Build] Eight inherited workflows that pushed to IceWhale infrastructure or needed its secrets are removed. `codecov.yml` and `release.yml` stay.

### Fixed

- [Sharing] Share paths are confined to the data roots and resolved through symlinks before any ownership change ([CasaOS #33](https://github.com/alvins82/CasaOS/pull/33)).
- [Sharing] The duplicate-name check queried the path column with a basename and never matched; a second share with the same folder name silently vanished from the generated configuration.

### Security

- [API] Routes that act as root on the host — system package updates, Samba accounts, share creation — require a token even from loopback. Any local process, including a container on the host network, could reach them without one ([CasaOS #32](https://github.com/alvins82/CasaOS/pull/32), [CasaOS #33](https://github.com/alvins82/CasaOS/pull/33)).

## [0.4.28] - 2026-08-15

### Changed

- [System] Use systemd's explicit `poweroff` and `reboot` targets for Dashboard power actions on systemd-based hosts ([CasaOS #26](https://github.com/alvins82/CasaOS/pull/26)).
- [Build] Advance the CasaOS-UI submodule to the CasaOS UI `v0.4.30` release, including the shutdown/restart flow fix ([CasaOS-UI #15](https://github.com/alvins82/CasaOS-UI/pull/15)).

### Fixed

- [System] Return power-command failures from the API instead of reporting success when the host remains running ([CasaOS #26](https://github.com/alvins82/CasaOS/pull/26)).

## [0.4.27] - 2026-08-14

### Added

- [Sharing] Advertise CasaOS Samba hosts through mDNS/DNS-SD and Windows Web Service Discovery so macOS, Linux, and Windows clients can discover the host without SMB1 ([CasaOS #23](https://github.com/alvins82/CasaOS/pull/23)).

### Changed

- [Sharing] Bind Windows discovery to default-route LAN interfaces, support an optional `/etc/casaos/smb-discovery.conf` interface override, and keep discovery aligned with the `smbd` lifecycle.
- [Updater] Advance the fork release fallback to `v0.4.31` so installations without a release marker discover the current fork installer.

### Fixed

- [Sharing] Preserve administrator-managed Samba configurations and make unavailable Avahi/wsdd packages degrade to normal SMB sharing without blocking installation or upgrades.

## [0.4.26] - 2026-08-13

### Added

- [Files] Open the Files app in an App Store-style dialog on desktop while retaining edge-to-edge behavior on smaller screens ([CasaOS #21](https://github.com/alvins82/CasaOS/pull/21); [CasaOS-UI #11](https://github.com/alvins82/CasaOS-UI/pull/11)).
- [Storage] Add storage-volume rename controls backed by the protected LocalStorage rename endpoint ([CasaOS-UI #14](https://github.com/alvins82/CasaOS-UI/pull/14); [CasaOS-LocalStorage #6](https://github.com/alvins82/CasaOS-LocalStorage/pull/6)).

### Changed

- [Dashboard] Keep the sidebar in normal document flow so the dashboard has one scroll surface and lower widgets remain reachable ([CasaOS-UI #12](https://github.com/alvins82/CasaOS-UI/pull/12)).
- [Build] Update the CasaOS-UI submodule to the CasaOS UI `v0.4.29` release.

### Fixed

- [Files] Use the CasaOS icon font's `eye-outline` and `eye-off-outline` glyphs for the hidden-files toggle ([CasaOS-UI #13](https://github.com/alvins82/CasaOS-UI/pull/13)).
- [Storage] Refresh filesystem labels after volume renames so Storage Manager reflects the new label immediately ([CasaOS-LocalStorage #6](https://github.com/alvins82/CasaOS-LocalStorage/pull/6)).

## [0.4.25] - 2026-08-13

### Added

- [System] Added authenticated Debian-family system package updates in Settings, including package review, confirmation, live progress, reboot status, and completion reconciliation ([CasaOS #20](https://github.com/alvins82/CasaOS/pull/20); [CasaOS-UI #10](https://github.com/alvins82/CasaOS-UI/pull/10)).

### Changed

- [Storage] Keep the system storage branch out of `/DATA` merged storage. External disks now provide the mergerfs branches while system AppData remains available at `/DATA/AppData`, allowing CasaOS to be installed on a flash drive without consuming it as media storage ([CasaOS #19](https://github.com/alvins82/CasaOS/pull/19); [CasaOS-UI #7](https://github.com/alvins82/CasaOS-UI/pull/7); [CasaOS-LocalStorage #4](https://github.com/alvins82/CasaOS-LocalStorage/pull/4)).
- [Updater] Advance the fork release fallback to `v0.4.25` so installations without a release marker still discover the current fork installer ([CasaOS #19](https://github.com/alvins82/CasaOS/pull/19)).
- [Build] Update the CasaOS-UI submodule to the merged system-package-update UI commit ([CasaOS #20](https://github.com/alvins82/CasaOS/pull/20)).
- [Docs] Record the fork UI release and related storage/app-launcher changes in the README ([CasaOS #16](https://github.com/alvins82/CasaOS/pull/16); [CasaOS #17](https://github.com/alvins82/CasaOS/pull/17); [CasaOS #18](https://github.com/alvins82/CasaOS/pull/18)).

### Fixed

- [Storage] Preserve existing system data during merge initialization and restore it safely when the merged view is removed.

## [0.4.21] - 2026-08-12

### Added

- [App] Open installed apps inside the CasaOS dashboard in a full-screen iframe with a close button; first-launch readiness checks remain in the current tab ([CasaOS-UI #2](https://github.com/alvins82/CasaOS-UI/pull/2)).

### Changed

- [Build] Updated the CasaOS-UI submodule to the fork commit containing the in-page app launcher.

## [0.4.20] - 2026-08-12

### Added

- [File] Added a persisted eye toggle for showing or hiding dot-prefixed files and folders in the main file browser. Visible-item totals now include hidden entries when enabled.

### Changed

- [Build] Switched the tracked UI submodule and build paths to the `alvins82/CasaOS-UI` fork ([CasaOS #12](https://github.com/alvins82/CasaOS/pull/12)).

### Fixed

- [OneDrive] Fall back to `createdBy.user.displayName` when Microsoft Graph omits `createdBy.user.email`, and return a clear error when neither identity field is available ([CasaOS #2530](https://github.com/IceWhaleTech/CasaOS/pull/2530)).

## [0.4.3]

### Added

- [Disk] Now usb also supports merging to


### Changed

- [File] Solve the installation dependency problem, make the installation more smoothly
- [File] Change the default permissions of the sharing folder

### Fixed

- [System] Fixed  not see wlan iface ([#909](https://github.com/IceWhaleTech/CasaOS/issues/909))
- [System] Terminal font issue fix ([#929](https://github.com/IceWhaleTech/CasaOS/issues/929))
- [File] Fixed the problem of not being able to launch after mounting

### Removed


## [0.4.2]

### Added

- [App] Increase the display of progress during the installation process
- [App] Label whether the current app supports x86 or Pi devices
- [App] Support single app version upgrade
- [File] Support mounting of Google Drive and Dropbox cloud drives
- [System] Support Mint Linux

### Changed

- [File] Optimize the download speed of a single file

### Fixed

- [Share] Fix the samba permission issue 
- [Disk] Fix the problem of disk mount point plus 1 after upgrade ([#770](https://github.com/IceWhaleTech/CasaOS/issues/770))
- [File] Fix the problem of file permission change caused by modifying files in casaos ([#829](https://github.com/IceWhaleTech/CasaOS/issues/829))
- [Share] Fix the problem of files being deleted due to samba uninstallation failure ([#843](https://github.com/IceWhaleTech/CasaOS/issues/843))



## [0.4.1] - 2023-1-19


### Added
- [Disk] Added disk merging feature in storage management (beta) that allows for multiple disks to be merged into a single storage space
- [System] Added option for startpage.com search engine
- [APP] Added app cloning feature in the app's context menu.
### Changed
- [APP] Improved app installation process, including display of the installation process, checks for successful installation, and prompts
- [System] Binary sizes are 40%~60% smaller (thanks to upx)
- [App] Optimization of install and update for certain country.
- [All] Lots of bug fixes

## [0.4.0] - 2022-12-13
### Added

- [Developer] Included `casaos-cli` command tool for debugging
- [Developer] Added message bus for events and actions - Use `casaos-cli message-bus` to manage.
- [Disk] Disk notification in Dashboard
- [System] Restart/shutdown directly from CasaOS Dashboard
### Changed

- [General] CasaOS new logo!
- [App] Redesign of Featured App
- [App] Now you can choose to delete userdata along with app uninstallation

### Security

- [System] Fixed a shell injection issue for better security

### Fixed

- [System] Re-instate default zone0 for CPU Temp ([#694](https://github.com/IceWhaleTech/CasaOS/issues/694))
- [Disk] Fixed storage name with extra `-1` after rebooting ([#698](https://github.com/IceWhaleTech/CasaOS/issues/698))
- [Disk] Fixed disk check so it does not impact disk going into idle ([#704](https://github.com/IceWhaleTech/CasaOS/issues/704))

## [0.3.8] 2022-11-21

### Added
- [System] Add system announcement
- [App] Allow to turn off the display of "Existing Docker Apps" in the settings.

### Changed
- [System] Improve the feedback function, you can submit feedback in the bottom right corner of WebUI.

### Fixed
- [System] Fix CPU Temp for other platforms ([#661](https://github.com/IceWhaleTech/CasaOS/issues/661))

## [0.3.7.1] 2022-11-04

### Fixed

- Fix memory leak issue ([#658](https://github.com/IceWhaleTech/CasaOS/issues/658)[#646](https://github.com/IceWhaleTech/CasaOS/issues/646))
- Solve the problem of local application import failure ([#490](https://github.com/IceWhaleTech/CasaOS/issues/490))

## [0.3.7] 2022-10-28

### Added
- [Storage] Disk merge (Beta), you can merge multiple disks into a single storage space (currently you need to enable this feature from the command line)

### Changed
- [Files] Changed the cache file storage location, now the file upload size is not limited by the system disk capacity.
- [Scripts] Updated installation and upgrade scripts to support more Debian-based Linux distributions.
- [Engineering] Refactored Local Storage into a standalone service as part of CasaOS modularization.

### Fixed
- [Apps] App list update mechanism improved, now you can see the latest apps in App Store immediately.
- [Storage] Fixed a lot of known issues

### Added
- [Storage] Disk merge (Beta), you can merge multiple disks into a single storage space (currently you need to enable this feature from the command line)

### Changed
- [Files] Changed the cache file storage location, now the file upload size is not limited by the system disk capacity.
- [Scripts] Updated installation and upgrade scripts to support more Debian-based Linux distributions.
- [Engineering] Refactored Local Storage into a standalone service as part of CasaOS modularization.

### Fixed
- [Apps] App list update mechanism improved, now you can see the latest apps in App Store immediately.
- [Storage] Fixed a lot of known issues


## [0.3.6] - 2022-09-06

###  Added
- [System] Added power and temperature info to performance widget (Intel)
- [Apps] Custom links can be added to Apps section

### Fixed
- [Apps] Fixed the problem of not being able to modify some App settings ([#510](https://github.com/IceWhaleTech/CasaOS/issues/510))

### Changed
- [System] Architecture optimization. Improved performance.

## [0.3.5] - 2022-08-23

### Added

- [File] Mount the shared samba
- [File] File sharing via Samba
- [System] You can share casaos on Twitter, facebook, reddit

### Changed

- [Disk] Support for mounting existing data disks

### Fixed

- [App] fixed uninstalling imported docker container apps results in wiping ALL your config data from them ([#360](https://github.com/IceWhaleTech/CasaOS/issues/360))

## [0.3.4] - 2022-07-29(UTC)

### Added

- SSH adds port-side options and prompts for connection status. ([#286](https://github.com/IceWhaleTech/CasaOS/issues/286))

### Changed

- Normalize all routes
- Application names now support spaces ([#211](https://github.com/IceWhaleTech/CasaOS/issues/211))

### Removed

- Removed  casaos connect

### Security

- Adjustment of authentication method

### Fixed

- Fixed storage format and remove password error issues ([#344](https://github.com/IceWhaleTech/CasaOS/issues/344) [#357](https://github.com/IceWhaleTech/CasaOS/issues/357))

## [0.3.3] - 2022-07-08(UTC)

### Added

- [System]Add interface call log
- Adding Developing file ([#311](https://github.com/IceWhaleTech/CasaOS/pull/311))
- [App] add new tips for app section.
- [System] UI Configurable function modules: support turning off the search bar and recommended apps module in the settings.
- [System] Custom wallpapers: two new preset wallpapers, support for custom uploads, support for setting images from Files as wallpapers, Also support right click on dashboard to change wallpaper.

### Changed

- [App] Cache app store index and category data
- [System] casaos master program adapted to FHS standards
- [App] Update casaos icons.
- [System] Update translation.

### Removed

- [System] Remove upnp function module
- [System] Remove ddns function module
- [System] Remove search function module
- [System] Remove zerotier function module
- [System] Remove task function module
- [System] Remove file share function module

### Fixed

- [Disk] Fixed hard drive won't hibernate problem ([#202](https://github.com/IceWhaleTech/CasaOS/issues/202))
- [File] Fixed the backspace key that causes the folder to rewind ([#252](https://github.com/IceWhaleTech/CasaOS/issues/252))
- [App] Fixed app logo is not loading when imported. ([#320](https://github.com/IceWhaleTech/CasaOS/issues/320))

## [0.3.2.1] - 2022-06-16(UTC)

### Changed

- [System] Adjusted the display style.

### Fixed

- [System] Fixed the issue of widgets displaying wrongly on mobile devices.
- [App] Fix the problem of application opening failure on non-80 ports ([#283](https://github.com/IceWhaleTech/CasaOS/issues/283) [#280](https://github.com/IceWhaleTech/CasaOS/issues/280))
- [System] Modify port failure problem ([#282](https://github.com/IceWhaleTech/CasaOS/issues/282))
- [App]Modify environment variables disappearing problem([#284](https://github.com/IceWhaleTech/CasaOS/issues/284))
- [System]Fix no update alert([#278](https://github.com/IceWhaleTech/CasaOS/issues/278))
- [System] Fixed some bugs of application cpu usage and memory staging([#272](https://github.com/IceWhaleTech/CasaOS/issues/272))
- [App] Fixed plex and HA network mode error issues ([#299](https://github.com/IceWhaleTech/CasaOS/issues/299))
- [App] Fix application terminal not working ([#266](https://github.com/IceWhaleTech/CasaOS/issues/266))

## [0.3.2] - 2022-06-10

### Added

- [Files] Files can now be selected multiple files and downloaded, deleted, moved, etc.
- [Apps] Support to modify the application opening address.([#204](https://github.com/IceWhaleTech/CasaOS/issues/204))

### Changed

- [Apps] Hide the display of non-essential environment variables in the application.([#196](https://github.com/IceWhaleTech/CasaOS/issues/196))
- [System] Network, disk, cpu, memory, etc. information is modified to be pushed via socket.
- [System] Optimize opening speed.([#214](https://github.com/IceWhaleTech/CasaOS/issues/214))
- [Language] Update language pack [zarevskaya](https://github.com/zarevskaya) [patrickhilker](https://github.com/patrickhilker)
- [System] Interface path adjustment

### Removed

- [Files] Remove the online preview function of PDF files

### Fixed

- [System] Fixed the problem that sync data cannot submit the device ID ([#68](https://github.com/IceWhaleTech/CasaOS/issues/68))
- [Files] Fixed the code editor center alignment display problem.([#210](https://github.com/IceWhaleTech/CasaOS/issues/210))
- [Files] Fixed the problem of wrong name when downloading files.([#240](https://github.com/IceWhaleTech/CasaOS/issues/240))
- [System] Fixed the network display as a negative number problem.([#224](https://github.com/IceWhaleTech/CasaOS/issues/224))
- [System] Fixed the problem of wireless network card traffic display.([#222](https://github.com/IceWhaleTech/CasaOS/issues/222))


## [0.3.1.1] - 2022-05-17

### Fixed

- Fix the data loss problem when importing local applications

## [0.3.1] - 2022-05-16

### Added

- CasaConnect and file add image thumbnail function
- Import of docker applications
- List support custom sorting function
- CasaConnect gives priority to LAN connections
- USB auto-mount switch (Raspberry Pi is off by default)
- Application custom installation supports Docker Compose configuration import in YAML format
- You will see the new version changelog from the next version
- Added live preview for icons in custom installed applications

### Changed

- Application data is no longer saved to the database
- Optimize app store speed issues
- Optimize the way WebUI is filled in
- Image preview has been completely upgraded and now supports switching between all images in the same folder, as well as dragging, zooming, rotating and resetting.
- Added color levels to the CPU and RAM charts
- Optimized the display of the Connect friends list right-click menu
- Change the initial display directory to /DATA

### Removed

- Historical Application Data

### Fixed

- Fixed the problem that some Docker CLI commands failed to import
- Fix the problem that the application is not easily recognized in /DATA/AppData directory and docker command line after installation, it will be shown as application name
- Fix Pi-hole installation failure
- Fixed the issue that the app could not be updated using WatchTower
- Fixed the problem that the task status was lost after closing Files when there was an upload task

## [0.3.0] - 2022-04-08

### Added

- Add CasaConnect function, now you can share private files peer-to-peer with your friends.
- Add a widget for network traffic monitoring.
- 12 new popular apps added to App Center

### Changed

- Updated the sidebar of Files.
- Updated the initial directory of Files to the Root directory.
- Armbian 22.02 armhf/arm64/amd64 platform tests passed [@igorpecovnik ](https://github.com/igorpecovnik)
- Elementary OS 6.1 Jólnir amd64 platform tests passed [@alvarosamudio ](https://github.com/alvarosamudio)

### Fixed

- Fix an issue in Files where the backspace button would trigger a return to the previous level of the directory when creating a folder.
- Fix the display problem of application list in CPU widget.
- Fix the problem that the ipv6 of the application cannot be opened

### Removed

- Interfaces related to "zerotier"

## [0.2.10] - 2022-03-10

### Added

- Added CasaOS own file manager, now you can browse, upload, download files from the system, even edit code online, preview photos and videos through it. It will appear in the first position of Apps.
- Added CPU core count display and memory capacity display.

### Changed

- Optimized the rendering performance of the home page.
- Optimized the internationalization display of the time widget.
- Show the icon of the stopped application as gray.
- Unify the animation of the drop-down menu.
- Optimize the display of the application drop-down menu.
- Replaced the default font to optimize the display.

### Fixed

- Fix the problem of failed to create storage space

## [0.2.9] - 2022-02-18

### Added

- Add a simple notification function

### Changed

- Custom installation of new parameters(Capabilities,Hostname,Privileged)
- Update front-end translation [@SemVer](https://github.com/zarevskaya) [@koboldMaki](https://github.com/koboldMaki) [@sgastol](https://github.com/sgastol) [@delki8](https://github.com/delki8)

- Modify the default location and name of the usb mount

### Fixed

- Fix the problem of being indexed by search engines
- Fix some style display issues
- Solve hard drive can't be formatted, can't finish adding storage

## [0.2.8] - 2022-01-30

### Added

- Add USB disk device display

### Changed

- Update translation [@baptiste313](https://github.com/baptiste313) [@thueske](https://github.com/thueske)
- Compatible with more types of drives

### Fixed

- Fix the language initialization bug
- Fix the problem that the login page could not be displayed
- Fix missing translated content

## [0.2.7] - 2022.01.26

### Changed

- Apply multilingual support

### Security

- Fix an injectable execution bug

## [0.2.6] - 2022.01.26

### Added

- Add a bug report panel.
- App Store apps start supporting multiple languages

### Fixed

- Fix a disk that cannot be formatted under certain circumstances

## [0.2.5] - 2022.01.24

### Added

- Storage Manager

### Changed

- Update Disk widget
- Update language files [@ImOstrovskiy](https://github.com/ImOstrovskiy) [@baptiste313](https://github.com/baptiste313)

### Fixed

- File synchronization issues
- Fix the app store classification problem

## [0.2.4] - 2021.12.30

### Changed

- Brand new App Store
- Optimize request method

### Fixed

- Fix Sync panel width display error.
- Fix App panel width display error.

## [0.2.3] - 2021.12.11

### Added

- Add detailed CPU and memory statistics.
- Add the multi-language function and add Chinese translation.
- Add the function to modify the search engine.
- Add the function of modifying the WebUI port

### Changed

- Update update script
- Preprocessing usb automounting

### Fixed

- Volume path problem when customizing the installation of applications
- Fix Cpu and Ram usage display error
- Fix translation errors
- Fixed an error when importing and exporting appfile.

## [0.2.2] - 2021.12.02

### Changed

- UI adjustment

### Fixed

- Fix the problem of data display error when manually installing apps
- Fix some spelling problems
- Fix the bug of synchronization module

## [0.2.1] - 2021.11.25

### Fixed

- Fix Sync display error
- Fix Sync Downoad url error
- Fix Smart Block display error
- Fix widgets settings dispaly error
- Fix  application installation path error

## [0.2.0] - 2021.11.25

### Added

- Add sync function


## [0.1.11] - 2021.11.10

### Changed

- Adaptation of cell phone terminals
- Optimize user experience
- Replaced the default background
- Optimized the display performance and fixed some bugs

### Fixed

- Resolve application installation path errors

## [0.1.10] - 2021.11.04

### Added

- Add application terminal
- Add application logs
- Add system logs
- Add App Store for installation

## [0.1.9] - 2021.11.01 [YANKED]

## [0.1.8] - 2021.10.27

### Added

- Add system terminal
- Add the ability to modify the user name and password

### Changed

- Experience optimization
- Improve single user management function
- Fixed Disk widget display error
- Fixed Username display error after change
- Adaptation for mobile access

## [0.1.7] - 2021.10.22

### Added

- Add user authentication module, Login page and initialization page.

### Fixed

- Fix the problem that the application could not start after the system restarted.
- Home storage space data display exception
- Script override causes application loss after installation
- Fix docker network error

## [0.1.6] - 2021.10.19

### Added

- Add app icon auto-fill via docker image name.
- Add a file selector for app install.

### Changed

- Modify import reminder.
- Optimize the application installation process

### Fixed

- Fixed an issue with the app were it would disappear when the app was modified.
- Fixed device selector default dir to /dev

## [0.1.5] - 2021.10.15

### Added

- Add CPU RAM Status with widget
- Add Disk Info with widget
- Realize automatic loading of widgets

### Changed

- Enhance the Docker cli import experience and automatically fill in the folders that need to be mounted

### Removed

- Remove Weather widget.

### Fixed

- AppFile upload does not pass verification
- The setting menu of the app is displayed abnormally when the browser window is too narrow
- The port is occupied and the program cannot start
- Fix display bugs when windows size less than 1024px

## [0.1.4] - 2021.09.30

### Added

- Import and export of application configuration files
- Automatic parsing of docker commands

### Changed

- Improve the program release process
- Application installation process UX/UI optimization

### Fixed

- Authentication failure during the operation, resulting in the need to re-login

## [0.1.3] - 2021.09.29 [YANKED]

## [0.1.2] - 2021.09.28

### Fixed

- Application modification and new creation failure issues

## [0.1.1] - 2021.09.27

## [0.1.0] - 2021.09.26

### Added

- Application Center
