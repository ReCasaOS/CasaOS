# OS upgrade measure, d11-12 (Debian 11 to Debian 12): MEASURED

## The box

| | |
|---|---|
| before | Debian GNU/Linux 11 (bullseye) |
| after the reboot | Debian GNU/Linux 12 (bookworm) |
| apt | apt 2.2.4 (amd64) |
| dpkg | Debian 'dpkg' package management program version 1.20.14 (amd64). |
| systemd | systemd 247 (247.3-7+deb11u8) |
| libc6 | 2.31-13+deb11u14 |
| kernel | 5.10.0-46-cloud-amd64 |
| bash | 5.1.4(1)-release |
| python3 | Python 3.9.2 |
| sh | /usr/bin/dash |
| tz | +0000 |
| cpus | 4 |
| memory_mb | 5943 |
| upgrader | none |
| cloud-init | /usr/bin/cloud-init 20.4.1 |
| Docker the box started on | 28.0.4 |
| witnesses started again at the baseline (their policy does not start them after a reboot) | p-no |

## What the core says about this system

`GET /v1/sys/os`, through the gateway with the loopback secret, asked by the guest twice: on the old release before the hop, and after the reboot of the hop (the core reads /etc/os-release at each call, so the second answer shows whether it follows the new release). Each answer is set against the guest's own os-release (before) or the target release (after), and its support state against the core's end-of-support table (pkg/ossupport, mirrored in this script, checked on 2026-10-09) for the day of the call.

| | before the hop | after the reboot |
|---|---|---|
| asked (UTC day of the guest's clock) | 2026-10-11 | 2026-10-11 |
| HTTP status, curl's exit | 200, 0 | 200, 0 |
| what the guest's os-release says | debian 11 bullseye (Debian GNU/Linux 11 (bullseye)) | debian 12 bookworm (Debian GNU/Linux 12 (bookworm)) |
| id, version_id, codename | debian 11 bullseye | debian 12 bookworm |
| pretty_name | Debian GNU/Linux 11 (bullseye) | Debian GNU/Linux 12 (bookworm) |
| arch | amd64 | amd64 |
| known, variant | True, none | True, none |
| support.state | security_ended | supported |
| support.ends (precision) | 2026-08-31 (day) | 2028-06-30 (day) |
| support.extended | Debian ELTS (Freexian) until 2031-06-30 (day) | none |
| support.source | https://www.debian.org/News/2026/20260831 | https://www.debian.org/News/2026/20260712 |
| next | debian 12 bookworm (report) | debian 13 trixie (report) |
| what the table says for the release it is judged against | security_ended: the free security updates end 2026-08-31 | supported: the free security updates end 2028-06-30 |

| verdict | result | what was compared |
|---|---|---|
| GET /v1/sys/os before the hop: the system and its support are the box's | **PASS** | identity debian 11 bullseye as the guest's os-release says, known, no variant; support.state security_ended as the table says for debian 11 on 2026-10-11 (the free security updates end 2026-08-31) |
| GET /v1/sys/os after the reboot: the system and its support are the target release's | **PASS** | identity debian 12 bookworm as the target release says, known, no variant; support.state supported as the table says for debian 12 on 2026-10-11 (the free security updates end 2028-06-30) |

## Disk

A hosted runner has about 14 GB; the guest's disk is a sparse 30 GB. Free space of the guest, in MB, as `df` said at each point (the runner's own `df` is in host-df-*.txt when the workflow wrote it):

| point | mount: free MB |
|---|---|
| start | /: 28138, /: 28138, /: 28138 |
| prepared | /: 26525, /: 26525, /: 26525 |
| before | /: 26760, /: 26760, /: 26760 |
| probed | /: 26759, /: 26759, /: 26759 |
| hop | /: 26132, /: 26132, /: 26132 |
| after | /: 26139, /: 26139, /: 26139 |

Lowest free space on a mount, after any step of the hop: 26132 MB.

The lowest free space seen in the guest is 26132 MB, and no mount fell below 300 MB (on /boot) or 1000 MB (elsewhere).

The runner's disk, host-df-start.txt:

    Filesystem      Size  Used Avail Use% Mounted on
    /dev/root       145G   59G   86G  41% /
    /dev/root       145G   59G   86G  41% /

The runner's disk, host-df-end.txt:

    Filesystem      Size  Used Avail Use% Mounted on
    /dev/root       145G   63G   82G  44% /
    /dev/root       145G   63G   82G  44% /

## Durations

| phase | duration |
|---|---|
| prepare (Docker, ReCasaOS and the witnesses on the old release) | 96 s |
| refresh of the old release (the dashboard's step 'update the current release': apt update, a full upgrade, Docker's source paused) | 2 s |
|   of which apt update | 1 s |
|   of which the full upgrade | 1 s |
| baseline: waiting for the gateway, the core and AppManagement to answer 200 after the last reboot (the longest it will wait: 180 s) | 0 s (ok) |
| probes of the old release (sandbox, releaseinfo-change, or Canonical's metadata) | 30 s |
| the unit (the hop, without the reboot) | 143 s |
|   step update (exit 0) | 4 s |
|   step minimal_dl (exit 0) | 1 s |
|   step minimal (exit 0) | 63 s |
|   step full_dl (exit 0) | 1 s |
|   step full (exit 0) | 61 s |
|   step converge1 (exit 0) | 1 s |
| after the reboot: waiting for the gateway, the core and AppManagement to answer 200 (the longest it will wait: 180 s) | 0 s (ok) |

## The refresh of the old release

As the product's refresh step will do it before any hop (the release notes of the next release and the Ubuntu launcher want the box up to date): `apt-get update`, then a full upgrade of the CURRENT release with Docker's source paused, after the rehearsal reboot and just before the baseline.

dpkg's own count, before and after the refresh: 0 upgraded, 0 added, 0 removed (of 431 installed before, 431 after). apt's own summary: 0 upgraded, 0 new, 0 to remove, 0 not upgraded; need to get n/a; after this operation n/a.

Upgraded: none.

Added: none.

Removed: none.

Kernel images before the refresh `linux-image-5.10.0-46-cloud-amd64=5.10.262-1 linux-image-cloud-amd64=5.10.262-1 `, after it `linux-image-5.10.0-46-cloud-amd64=5.10.262-1 linux-image-cloud-amd64=5.10.262-1 `; the system asks for a reboot after it: no. The refresh's own answer about a reboot: `no`.

Still upgradable after the refresh: nothing.

## The sandbox plan (read-only, /etc/apt untouched)

`apt-get -o Dir::Etc::sourcelist=<tmp> -o Dir::Etc::sourceparts=<tmp> -o Dir::State::lists=<tmp> -o Dir::Cache=<tmp> -o Debug::NoLocking=true update --error-on=any`, then `-s upgrade` and `-s dist-upgrade`, on bullseye's own apt (2.2.4) against bookworm's InRelease. `paused`: Debian's sources only, as the hop leaves them. `kept`: Docker's source as it stands on the box.

| | update | -s upgrade | -s dist-upgrade | Docker lines in the simulation | size |
|---|---|---|---|---|---|
| paused | 0 | 0 | 0 | none | 214 MB |
| kept | 0 | 0 | 0 | Inst docker-ce-cli; Inst containerd.io; Inst docker-ce; Inst docker-ce-rootless-extras | 215 MB |

/etc/apt before and after the sandbox: identical. /var/lib/apt/lists: identical.

### How close the plan is to the transaction that was done

The plan is made on the old release, before the minimal upgrade (the notes say `dist-upgrade` may even fail there); the real transaction is the minimal upgrade followed by the full one.

| | upgraded | newly installed | to remove | need to get | disk |
|---|---|---|---|---|---|
| sandbox -s upgrade | 274 | 0 | 0 | n/a | n/a |
| real minimal upgrade | 274 | 0 | 0 | 80.4 MB | 19.9 MB |
| sandbox -s dist-upgrade | 384 | 75 | 2 | n/a | n/a |
| real full upgrade | 111 | 75 | 2 | 140.0 MB | 300.0 MB |
| real, both together | 385 | 75 | 2 | 220.4 MB | 319.9 MB |

Counts of the sandbox's dist-upgrade against the two real steps together: upgraded -1, newly installed +0, to remove +0.

### --allow-releaseinfo-change-{suite,version,label,codename,origin} on apt 2.2.4

A stored InRelease of bookworm gets one field changed (and a time of 0, so that apt asks for the file again), then `apt-get update --error-on=any` runs without the option, with the option of that field, and, after another change, with `--allow-releaseinfo-change`.

| field | without | with the field's option | with the general option | what apt does | as apt's own table says |
|---|---|---|---|---|---|
| suite | exit 0 | exit 0 | exit 0 | tolerated by default (no notice) | yes |
| version | exit 0 | exit 0 | exit 0 | tolerated by default (no notice) | yes |
| label | exit 100 | exit 0 | exit 0 | an error until it is accepted | yes |
| codename | exit 100 | exit 0 | exit 0 | an error until it is accepted | yes |
| origin | exit 100 | exit 0 | exit 0 | an error until it is accepted | yes |


## The packages

dpkg's own count, before and after the unit (before the reboot): 384 upgraded, 75 added, 2 removed (of 433 installed before, 506 after).

Removed: fuse, libsemanage1.

Added: cron-daemon-common, dbus-bin, dbus-daemon, dbus-session-bus-common, dbus-system-bus-common, firmware-linux-free, fuse3, gcc-12-base, libbpf1, libcbor0.8, libduktape207, libffi8, libfile-find-rule-perl, libfmt9, libfuse3-3, libicu72, libiscsi7, libjemalloc2, libldap-2.5-0, libldap-common, libnfs13, libntfs-3g89, libnuma1, libnumber-compare-perl, libperl5.36, libproc2-0, libpython3.11, libpython3.11-minimal, libpython3.11-stdlib, librbd1, libsemanage2, libsepol2, libsodium23, libssh-4, libssl3, libsystemd-shared, libtext-glob-perl, liburing2, linux-image-6.1.0-53-cloud-amd64, perl-modules-5.36, pkexec, polkitd, polkitd-pkla, python-babel-localedata, python3-anyio, python3-babel, python3-charset-normalizer, python3-click, python3-colorama, python3-h11, python3-h2, python3-hpack, python3-httpcore, python3-httpx, python3-hyperframe, python3-markdown-it, python3-mdurl, python3-netifaces, python3-rfc3986, python3-rfc3987 ... (15 more).

Installed by hand (the controls and Docker's engine) and what became of them:

| package | before | after the unit | after the reboot |
|---|---|---|---|
| osm-orphan | 1.0 | 1.0 | 1.0 |
| osm-orphan-held | 1.0 | 1.0 | 1.0 |
| docker-ce | 5:28.0.4-1~debian.11~bullseye | 5:28.0.4-1~debian.11~bullseye | 5:28.0.4-1~debian.11~bullseye |
| docker-ce-cli | 5:28.0.4-1~debian.11~bullseye | 5:28.0.4-1~debian.11~bullseye | 5:28.0.4-1~debian.11~bullseye |
| containerd.io | 1.7.29-1~debian.11~bullseye | 1.7.29-1~debian.11~bullseye | 1.7.29-1~debian.11~bullseye |
| docker-ce-rootless-extras | 5:28.0.4-1~debian.11~bullseye | 5:28.0.4-1~debian.11~bullseye | 5:28.0.4-1~debian.11~bullseye |
| docker-buildx-plugin | 0.37.0-1~debian.11~bullseye | 0.37.0-1~debian.11~bullseye | 0.37.0-1~debian.11~bullseye |
| docker-compose-plugin | 5.5.1-1~debian.11~bullseye | 5.5.1-1~debian.11~bullseye | 5.5.1-1~debian.11~bullseye |

- step `full` (exit 0): 111 upgraded, 75 new, 2 to remove, 0 not upgraded; need to get 140.0 MB; after this operation 300.0 MB.
- step `minimal` (exit 0): 274 upgraded, 0 new, 0 to remove, 111 not upgraded; need to get 80.4 MB; after this operation 19.9 MB.
- step `full_dl` (exit 0): 111 upgraded, 75 new, 2 to remove, 0 not upgraded; need to get 140.0 MB; after this operation 300.0 MB.
- step `minimal_dl` (exit 0): 274 upgraded, 0 new, 0 to remove, 111 not upgraded; need to get 80.4 MB; after this operation 19.9 MB.

apt's list of packages it was going to REMOVE in the full upgrade: fuse, libsemanage1.

## Which services were restarted while dpkg ran

A service whose InvocationID is not the same before and after the unit was started again in between (12 of 94 services present at both ends).

| unit | state before | state after |
|---|---|---|
| avahi-daemon.service | active | active |
| chrony.service | active | active |
| cron.service | active | active |
| e2scrub_reap.service | inactive | inactive |
| nmbd.service | active | active |
| resolvconf.service | active | active |
| rsyslog.service | active | active |
| smbd.service | active | active |
| ssh.service | active | active |
| systemd-journald.service | active | active |
| systemd-udevd.service | active | active |
| udisks2.service | active | active |

systemd's journal of the hop has 80 lines of Stopping/Starting; the first ones:

    2026-10-11T06:53:57.037417+0000 debian systemd[1]: Started Session 7 of user casa.
    2026-10-11T06:53:57.075516+0000 debian systemd[1]: Starting /usr/bin/systemd-inhibit --what=shutdown:sleep --who=casaos-os-upgrade-measure --why=an operating system upgra
    2026-10-11T06:53:57.077893+0000 debian systemd[1]: Started /usr/bin/systemd-inhibit --what=shutdown:sleep --who=casaos-os-upgrade-measure --why=an operating system upgrad
    2026-10-11T06:53:58.185920+0000 debian systemd[1]: Started Session 8 of user casa.
    2026-10-11T06:54:05.797259+0000 debian systemd[1]: Stopping Samba SMB Daemon...
    2026-10-11T06:54:05.799028+0000 debian systemd[1]: Stopped Samba SMB Daemon.
    2026-10-11T06:54:05.800801+0000 debian systemd[1]: Starting Samba SMB Daemon...
    2026-10-11T06:54:05.862700+0000 debian systemd[1]: Started Samba SMB Daemon.
    2026-10-11T06:54:05.879379+0000 debian systemd[1]: Stopping OpenBSD Secure Shell server...
    2026-10-11T06:54:05.880896+0000 debian systemd[1]: Stopped OpenBSD Secure Shell server.
    2026-10-11T06:54:05.882376+0000 debian systemd[1]: Starting OpenBSD Secure Shell server...
    2026-10-11T06:54:05.898209+0000 debian systemd[1]: Started OpenBSD Secure Shell server.

### libc6 2.31-13+deb11u14 to 2.36-9+deb12u14

dpkg's log: libc6 half-configured at 2026-10-11 06:54:05, installed at 2026-10-11 06:54:06.
Services named by libc6's postinst in apt's output: cron, smbd, ssh.

    Setting up libc6:amd64 (2.36-9+deb12u14) ...
    Checking for services that may need to be restarted...
    Checking init scripts...
    
    Restarting services possibly affected by the upgrade:
      smbd: restarting...done.
      ssh: restarting...done.
      cron: restarting...done.
    
    Services restarted successfully.
    (Reading database ... 25982 files and directories currently installed.)

## The witnesses

The database acknowledged 1990 writes; 1992 are in its file; **0 missing** (this finds a lost or rolled-back volume, not a power cut).

Longest silence between two acknowledged writes: 82.1 s (at 06:51:34).
Longest silence during the hop: 0.2 s.
Longest silence from the reboot to the containers being back: 12.4 s.

### The published ports, Docker and the gateway, in the samples of the poller (about one a second)

| window | docker | published port 18081 | host-network port 18082 | ssh | samba | gateway | core | AppManagement |
|---|---|---|---|---|---|---|---|---|
| the hop | never down for two samples | never down for two samples | never down for two samples | never down for two samples | never down for two samples | never down for two samples | never down for two samples | never down for two samples |

### After the reboot, seconds from the first sample of the box (06:56:39)

| | seconds |
|---|---|
| the box was away (last sample before, first after) | 11 s |
| ssh answers | 2 s |
| samba accepts | 2 s |
| Docker answers | 0 s |
| the gateway answers 200 | 2 s |
| the core answers 200 | 2 s |
| AppManagement answers 200 | 5 s |
| the published port answers 200 | 2 s |
| every container that starts by itself is running | 0 s |

From the request to reboot to the first sample of the poller: 17 s.

### Containers, by restart policy

| container | policy | before | after the unit | first look after the reboot | once settled | restarts |
|---|---|---|---|---|---|---|
| p-always | always | running | running | running | running | 0 |
| p-unless-stopped | unless-stopped | running | running | running | running | 0 |
| p-no | no | running | running | exited | exited | 0 |
| p-on-failure | on-failure | running | running | running | running | 0 |
| p-db | unless-stopped | running | running | running | running | 0 |
| p-web | unless-stopped | running | running | running | running | 0 |
| p-host | unless-stopped | running | running | running | running | 0 |

The containers whose policy starts them again by themselves: ALL running again.
The others (`no`, `on-failure`) that were not running again: p-no (the same list the Docker update calls not_returned).

## Health and the kernel

| | before | after the unit | after the reboot |
|---|---|---|---|
| dpkg --audit is empty | yes | yes | yes |
| apt-get check passes | yes | yes | yes |
| systemctl --failed | 0 | 0 | 0 |

Failed units that were not failed before: none.

| | before | after the unit | after the reboot |
|---|---|---|---|
| running kernel | 5.10.0-46-cloud-amd64 | 5.10.0-46-cloud-amd64 | 6.1.0-53-cloud-amd64 |
| /boot, bytes in files | 49214728 | 80419998 | 80419998 |
| /boot: size, used, free (MB) | 30033 2004 26760 | 30033 2632 26132 | 30033 2625 26139 |
| a reboot is asked for | no | yes | no |

Kernel images: before `linux-image-5.10.0-46-cloud-amd64=5.10.262-1 linux-image-cloud-amd64=5.10.262-1 `; after `linux-image-5.10.0-46-cloud-amd64=5.10.262-1 linux-image-6.1.0-53-cloud-amd64=6.1.187-1 linux-image-cloud-amd64=6.1.187-1 `.

`systemctl is-system-running --wait` after the reboot said: running (exit 0). systemd-analyze: Startup finished in 2.430s (kernel) + 6.978s (userspace) = 9.408s graphical.target reached after 6.563s in userspace.

The installer's dependencies (and wsdd, its optional one) that were installed and are not any more: none.

## ReCasaOS after the reboot

The after phase starts nothing: it reads the units first (`units-boot`), waits for the gateway, the core and AppManagement to answer 200 (the baseline's own wait, at most 180 s: AppManagement needs 11 to 13 s on Debian and 4 to 7 s on Ubuntu after a boot), and only then asks them. (The harness's own test checks that the phase has no start, restart or enable of a unit.)

| unit | before | first look | settled | restarted by systemd after the first look |
|---|---|---|---|---|
| casaos-app-management.service | active | active | active | no |
| casaos-gateway.service | active | active | active | no |
| casaos-local-storage.service | active | active | active | no |
| casaos-message-bus.service | active | active | active | no |
| casaos-user-service.service | active | active | active | no |
| casaos.service | active | active | active | no |

The wait after the reboot: 0 s of the 180 s it allows, result ok (the last answers: gateway 200, core 200, AppManagement 200). Asked again once the wait ended: gateway 200, core 200, core with the loopback secret 200, AppManagement with the token of the baseline's login 200.


### The sources, before and after the unit's rewrite

    --- before
    +++ after
    @@ -2,7 +2,7 @@
    -deb http://deb.debian.org/debian bullseye main
    -deb-src http://deb.debian.org/debian bullseye main
    -deb http://archive.debian.org/debian-security bullseye-security main
    -deb-src http://archive.debian.org/debian-security bullseye-security main
    -deb http://deb.debian.org/debian bullseye-updates main
    -deb-src http://deb.debian.org/debian bullseye-updates main
    -==> /etc/apt/sources.list.d/docker.list <==
    +deb http://deb.debian.org/debian bookworm main
    +deb-src http://deb.debian.org/debian bookworm main
    +deb http://deb.debian.org/debian-security bookworm-security main
    +deb-src http://deb.debian.org/debian-security bookworm-security main
    +deb http://deb.debian.org/debian bookworm-updates main
    +deb-src http://deb.debian.org/debian bookworm-updates main
    +==> /etc/apt/sources.list.d/docker.list.casaos-paused <==

Sources paused (renamed .casaos-paused) for the hop: /etc/apt/sources.list.d/docker.list.

## The go/no-go table (os-upgrade-design.md, section 8)

| Question | Answer | What was measured |
|---|---|---|
| The sandbox plan works (else one-phase confirmation, the guards staying in the unit) | **GO** | update, -s upgrade and -s dist-upgrade exit 0/0/0, /etc/apt and the lists untouched, and the sandbox saw bookworm |
| The docker-ce built for bullseye survives the hop (its source paused) | **GO** | the paused simulation removes no engine package; after the reboot docker-ce/-cli/containerd.io are the same installed versions; the engine answers 28.0.4 (before 28.0.4) |
| The core, the gateway and AppManagement come back by themselves | **GO** | the gateway, the core and AppManagement all answered 200 within the wait (it took 0 s of the 180 s it allows); all 6 casaos units active, none restarted by systemd after the first look; seconds after the first sample of the box: gateway 2 s, core 2 s, AppManagement 5 s; asked again once the wait ended: gateway 200, core 200, AppManagement 200 |
| The transient unit survives the upgrade of systemd, dbus and libc6 | **GO** | it was the same process (pid 9990) from START to DONE; upgraded meanwhile: systemd 247.3-7+deb11u8 -> 252.39-1~deb12u2, dbus 1.12.28-0+deb11u1 -> 1.14.10-1~deb12u1, libc6 2.31-13+deb11u14 -> 2.36-9+deb12u14; the manager was 247.3-7+deb11u8, is 252.39-1~deb12u2 |
| A token issued before the hop is still accepted afterwards (the browser session survives the reboot) | **GO** | the token of the baseline: accepted; a new login afterwards: accepted |
| The product can rely on the block inhibitor to keep root's reboot out (the design does not count it as a guard) | **NO-GO** | reboot: NOT REFUSED (the box went down and came back with another boot); --force: NOT REFUSED (the box went down and came back with another boot) |
| The hop did what it set out to (Debian 11 to Debian 12) | **GO** | os-release says debian 12 (bookworm); dpkg --audit and apt-get check clean; the simulation converged to 0/0/0/0: yes |

## Other measurements

| What | Result |
|---|---|
| the refresh of the old release | 0 upgraded, 0 added, 0 removed, in 2 s; a reboot is not needed |
| packages still upgradable after the refresh | none |
| the sandbox's counts against the real transaction (upgraded, new, removed) | (384, 75, 2) against (385, 75, 2): differences (1, 0, 0) |
| `apt-get --without-new-pkgs upgrade` (the notes' spelling, for apt) on apt 2.2.4 | accepted |
| --allow-releaseinfo-change-{suite,version,label,codename,origin} on apt 2.2.4 | as apt's own table says: suite and version are tolerated by default, label, codename and origin are errors until they are accepted, and the field's option and the general switch both accept the change |
| protected names of the design's guard that the hop removed | none |
| ssh, samba and Docker/containerd restarted during the hop | yes: cron, samba, ssh |
|   ssh bounced during the hop | yes |
|   samba bounced during the hop | yes |
|   docker bounced during the hop | no |
|   containerd bounced during the hop | no |
| services libc6 2.31-13+deb11u14 -> 2.36-9+deb12u14 restarted (its postinst, non-interactive) | cron, smbd, ssh |
| database: longest silence during the hop | 0.2 s |
| database: longest silence from the reboot to the containers being back | 12.4 s |
| reboot after the hop, request to the box answering | 17 s |
| containers that start by themselves, back after | 1 s |
| the installer's dependencies that the hop removed | none |
| root's `systemctl reboot` under systemd-inhibit --what=shutdown --mode=block | NOT REFUSED (the box went down and came back with another boot) |
| root's `systemctl reboot --force` under systemd-inhibit --what=shutdown --mode=block | NOT REFUSED (the box went down and came back with another boot) |
| the paused-source simulation removes an engine package | no Remv or Purg of docker-ce, docker-ce-cli, containerd.io or a docker plugin |
| the kept-source simulation removes an engine package | no Remv or Purg of an engine package |
| the frozen Docker repository (bullseye's) with the source kept: the plan includes Docker | yes: containerd.io, docker-ce, docker-ce-cli, docker-ce-rootless-extras |
| the same with the source paused | no |
| Docker after the reboot | answers 28.0.4 (before 28.0.4); packages: containerd.io=1.7.29-1~debian.11~bullseye docker-buildx-plugin=0.37.0-1~debian.11~bullseye docker-ce=5:28.0.4-1~debian.11~bullseye docker-ce-cli=5:28.0.4-1~debian.11~bullseye docker-ce-rootless-extras |

## What could not be decided

- nothing that this leg was meant to decide was left open
- whatever the answers above: a cloud image in QEMU does not prove the names of real network cards, firmware, DKMS drivers, RAID/LVM/LUKS or ZFS roots, real BIOS/UEFI boot, a real power cut, slow disks, arm64 or third-party repositories (os-upgrade-design.md, section 8)
