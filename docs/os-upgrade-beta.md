# Upgrading the operating system from the dashboard: the beta

This is the guide for the person who switches the feature on. It says what the feature does, how to run it, what to do when it stops, and, before all that, how much of it has been seen to work.

The feature takes a Debian 11 (bullseye) machine to Debian 12 (bookworm) with the procedure that Debian documents, run by a transient systemd unit that the core starts, and shows where it stands. It is **off by default**, and nothing changes on a machine that never switches it on.

**There is no way back from the point of no return, except a snapshot of the machine or a console to repair it from.** Debian does not document a downgrade. Take the snapshot before you start, and be where you can see the machine's screen or console when it restarts: a machine that does not come back cannot show its own dashboard.

## What is proven, and what is not

Every claim below carries one of three marks.

- **[M] measured** on a real guest that booted: the apt commands and their order (update, minimal upgrade, full upgrade, the simulation that finds nothing left), the transient unit with its inhibitor, the restart (the machine was away for 11 seconds, the core and the gateway answered 2 seconds after it came back, AppManagement after 5), the services that go on through the hop (ssh, smbd and cron restart once, at the replacement of libc6), and the access token surviving the hop.
- **[R] reviewed and run without a machine that booted**: the nine blocks of the hop, the guard that checks what apt would remove, the script that puts the sources back, the reading of apt's simulation, the heredoc discipline, the lines that stop a block. They were reviewed several times and run under dash with stand-in commands and in a Debian 11 chroot with the real apt.
- **[N] new, never run anywhere**: the driver that runs the blocks inside the unit, the log of markers and the reducer that reads it, the eight changes that make the text of the blocks fit a unit and the pass that gives each stop a class, the repair block, the check that the block runs inside the unit, the four blocks of the refresh (their apt commands are [M]; the whole of them was run for real in a Debian 11 root with its own apt, dpkg, dash and mawk, which is [R], and the glue that stops them, puts the sources and the timers back and says whether the machine wants a restart has never run on a machine that booted), the rehearsal, the call-off protocol, the checks before the restart and the judge after it, the flag that keeps backups and updates away and the readers of it, and taking a run over by hand.

So the order of the proof is: the commands are [M], the text of the blocks is [R], and the machinery around them is [N]. The first runs of this feature, on a machine that matters, are the first real runs of the machinery. This is why it is a beta, why it starts with a rehearsal that can be undone, and why nothing repairs itself after the point of no return: **the repair of a stopped hop is a click of yours, and no more than a click.**

### What a first run does not exercise

Said here so that nobody is surprised by it:

- the automatic repair after a power cut or a killed unit (the setting that allows it ships at zero tries: you click "go on");
- the repair block and the repair ladder of the full upgrade, unless something fails, in which case it is their first real run;
- dash through the replacement of dash and libc6 while the unit runs, and `Dpkg::Use-Pty=0` over a long run;
- what stopping the unit does through the inhibitor, and whether the shell of a block sees the unit's cgroup under both cgroup layouts (the gate of the blocks relies on it);
- the core not coming back after the restart: nothing reports it, and nothing starts the check without the core;
- a real power cut, network card names, firmware, DKMS, RAID, LUKS, BIOS against UEFI variants, arm64, slow disks, years of package history, third-party repositories;
- Samba and mergerfs beyond "they mount and answer", and apps that write into an empty mount point;
- Docker's repository after the hop (its source stays paused when it cannot be asked);
- the page (a later change), the French and English words of every state, class and finding, and the message that the page shows after fifteen minutes without an answer;
- AppManagement's side of the flag (see "What is held" below), which is another repository's release;
- the refresh of a repository whose release information changed (`stable` that became `oldstable`): the question and the option exist, and the exact text of apt for it was not reproduced here, only read from apt's own strings;
- the timers of apt under a real systemd in a refresh (the refresh was run for real in a root that has no systemd: every unit was inactive);
- Debian 12 to 13, and Ubuntu.

## Switching it on, and off

The switch is a file: `/etc/casaos/os-upgrade.beta`. Either make it, or send, with your access token in the `Authorization` header:

```
POST /v1/sys/os/upgrade/beta   {"enabled": true}
```

It gates only what **begins** something: the start. Everything that acts on a run that is open (the status, the restart, going on, calling off, your word, taking over, the text of the run) works with the switch off, so that a machine that is half upgraded is never trapped behind it. For the same reason the switch cannot be turned off while a run is open: that answers `409 run_open`.

## Before you start

1. Take a snapshot of the machine, with its memory if the hypervisor allows, and make sure that you can reach its console.
2. Bring Debian 11 to its last state (the last point release and the security updates): `POST /v1/sys/os/upgrade/refresh`, or the packages window, or apt by hand. The refresh is a run of its own (`refreshing`, then `refreshed`, or `reboot_first` when the machine wants a restart, and the `reboot` button is then valid): it keeps your sources as they are, puts the ones that are not Debian's aside while it works (Docker's included), runs `apt-get update` and `apt-get dist-upgrade` with the phased updates, **removes nothing** (a package that the current release wants to remove stops it, `plan`), and puts the sources and the timers of apt back when it ends or stops. It stops with `identity` and the hint `releaseinfo_change` when apt refuses a repository whose release information changed (the suite `stable` that became `oldstable` when Debian 12 came out): say that the change is wanted by sending the refresh again with `{"accept_releaseinfo_change": true}`, which adds the one option `--allow-releaseinfo-change` to its `apt-get update`, and nothing else. If your sources still say `security.debian.org` for bullseye its download stops with `network` (the packages are no longer there: seen for real on 2026-10-14): use `archive.debian.org/debian-security` first. Restart once if the machine wants it (the readiness report refuses a machine that waits for a restart), and **make the plan after the refresh**: the start refuses a plan that is older than it (`plan_older_than_refresh`).
3. Read `GET /v1/sys/os/readiness`: it only reads, and each refusal has a code and a fix.
4. Make the plan, `POST /v1/sys/os/upgrade/plan`, and read what it says: the sources that will be rewritten and the ones that will be put aside (third-party repositories, Docker's included), the packages that will be removed, the containers that will not come back after a restart (restart policy `no`), the room and the download. A plan is good for 24 hours, and the start refuses a plan that the box has moved away from.
5. Have a recent backup of your data. The backups of AppManagement protect the data and not the system.

## The way through

1. **Rehearse first** (recommended, and needed for the first run on a machine). `POST /v1/sys/os/upgrade/start` with `"rehearsal": true` runs the half of the hop that can be undone (blocks 0 to 4): the checks, the timers of apt stopped, the notes of the machine and the copy of `/etc`, the sources of Debian 12 written and apt-get updated against them, and the download of the minimal upgrade. It stops with the sources of Debian 12 in place, for no more than 30 minutes (`rehearsed`). Run no apt by hand while it is. `ack` with `discard` puts the machine back, byte for byte; so does the end of the half hour, and so does a restart of the machine. The plan is still good afterwards. A rehearsal never reaches the point of no return, and needs neither the safety net nor the acknowledgement of "no way back".
2. **Start the hop**: `POST /v1/sys/os/upgrade/start`, with the plan's id, the safety net you have (`snapshot`, `console_and_backup` or `reinstall_ok`), the name of the machine typed again, the removals and the sources you accept, and the acknowledgements: `no_interruption` and `no_way_back` always, `sources_paused` when the plan puts sources aside, `removals_read` when it has removals to accept. A refusal says why, with a code. The unit runs the blocks one after the other: 0, the checks that this is the machine of the plan and that it is ready; 1, the timers of apt stopped; 2, the state of the machine written down; 3, the sources of Debian 12 written (a copy of `/etc/apt`, the plan's sources, the third-party ones put aside), `apt-get update`, and the simulation read again against the plan; 4, the download of the minimal upgrade; 5, the point of no return: the minimal upgrade, then the protected packages; 6, the full upgrade until nothing is left; 7, the checks that the machine will start (the kernel, its initramfs, grub, sshd, the resolver, a time daemon). The last block, 8, runs after the restart.
3. **Call it off before block 5 if you want to**: `POST /v1/sys/os/upgrade/abort` stops the unit and puts the machine back. Block 5 writes the marker of the point of no return, and after it nothing can be called off.
4. **The machine waits for its restart** (`awaiting_reboot`). The core never restarts it by itself. It checks, and keeps checking, what stands before the restart (a mount of `/etc/fstab` whose disk is not there, and the names of the network cards, which can change with the new kernel): while one of the checks is red, the restart is not offered, and the status says which. You are told once when it waits, again after an hour, after a day and then every day, and each time that backups and automatic updates stay paused while it waits.
5. **Restart**: `POST /v1/sys/os/upgrade/reboot`. The status carries, before the restart, what you need if the machine does not come back (`after_reboot_hint`: where the backup folder is, the log, the commands to look at it from a console, the text of the last block). The machine is away for a few minutes, and it is the core, and not the unit, that is there when it comes back.
6. **The check** (`resuming`, `verifying`). The core sees a new boot, stops the units that change packages by themselves, waits for the machine to be ready (ten minutes at most: the check starts anyway after that, and says what was not ready), and starts the last block, which checks that the machine was restarted, that it runs Debian 12 on the kernel that block 7 looked at and that its sources name Debian 12, shows what to look at, and puts the timers of apt back. Then the core judges the machine against the notes it took before.
7. **The result**: `succeeded`, `succeeded_with_findings` (something to look at, each finding with its code), or `failed` (the check found something that is wrong). `POST /v1/sys/os/upgrade/ack` with `acknowledge` closes the run: backups and automatic updates go on, and the backup folder of the hop is moved into the run's folder.

Docker's engine is never touched: its apt source is put aside for the hop, and put back by the last block only when it can be asked and publishes the new release. When it cannot, the source stays paused and the judge says so (`docker_source_unreachable`), which is a warning and not a failure. Update Docker afterwards with the button that does it.

## What is held while an upgrade is open

While a run is open, and until you close it, the machine is held, because dpkg rewrites files that other things read:

- the automatic updates of the core, the update button, the packages window and the update of Docker refuse, and so do the readiness report and the plan job (they say that the machine is busy);
- the page's restart and shutdown are refused with `os_upgrade_open` while the unit works (they work again when the machine waits for its restart or is stopped), and the upgrade's own restart is not one of them;
- the alerts about the apps' containers are kept quiet, since containers stop and start during the hop, and for ten minutes after the run lets go of the machine;
- the backups of AppManagement skip their cycle while the flag file `/var/lib/casaos/os-upgrade/busy` is there, **once the matching change of AppManagement is installed**: it is released apart from this one. Until then, do not run a backup by hand during a run.

The flag is read by everything that reads it as "there unless proven absent": a folder that cannot be read counts as an upgrade that is open. A run that is left waiting for its restart for days keeps all of this closed, and the page and the reminders say so; the choice of releasing it after some time is yours (see the end).

Not held: new app operations that you start yourself during a run, and `apt` typed over ssh. Do not do either: the hashes of the sources, the comparison of the removals and the dpkg lock catch some of it, not all.

## What each state means, and what to do

The status says the state as a code, and `actions` is the list of buttons that are valid now. Whatever the page shows, it is that list.

| State | What it is | What you do |
|---|---|---|
| `idle` | nothing is open | start a rehearsal or a hop |
| `refreshing` | the unit brings Debian 11 to its last state | wait; do not run apt. `abort` while it works |
| `refreshed` | Debian 11 is as new as it gets, and the sources and the timers are back | `acknowledge`, then make the plan |
| `reboot_first` | the refresh ended, and the machine wants a restart (a kernel, or reboot-required) | `reboot` (or restart it yourself), then `acknowledge`, then make the plan |
| `rehearsing`, `running` | the unit works | wait; do not run apt, do not restart. `abort` while it is before block 5 |
| `rehearsed` | the sources of Debian 12 are in place, for at most 30 minutes | `ack` with `discard`. The core puts the machine back by itself when the time is over or the machine restarts |
| `unwinding` | the unit puts the machine back | wait |
| `rehearsal_done` | the machine is as it was | `ack` with `acknowledge` to close it, then start the hop with the same plan |
| `awaiting_reboot` | all the blocks are done, and the machine waits | `reboot` when the checks are green, or `manual` to finish by hand |
| `rebooting`, `reboot_stalled` | the restart was asked for (stalled: more than 15 minutes ago and the machine has not come back) | wait; for a stalled one, look at the screen of the machine; `reboot` can ask again |
| `resuming`, `verifying` | the machine has restarted and is being checked | wait |
| `succeeded`, `succeeded_with_findings` | done | read the findings, then `acknowledge` |
| `failed` before the point of no return | a block stopped and the machine was put back; `reason` is the class of the stop | read the line of the unit's output in `failure`; `acknowledge`, or `discard` to drop the plan |
| `failed` with `reason` `verify` | the machine restarted and the check found an error | read the findings; `resume` (the check again), `manual` or `acknowledge` |
| `failed` with `reason` `start` after the point of no return | the unit could not be started | `resume` or `manual` |
| `interrupted` | the unit was lost (a power cut, `kill`). Before the point of no return the core puts the machine back by itself | after it: `resume`, `manual`, or `acknowledge` with the word |
| `stopped_at_waypoint` | a block stopped after the point of no return; `class` says why | `resume` when the class allows it, `manual`, or `acknowledge` with the word |
| `needs_attention` | systemd does not answer, an unwinding was partial, or the run's folder cannot be read | `manual`, or `acknowledge` (with the word when the machine is half upgraded) |
| `manual`, `manual_done` | you took the run over; the core no longer acts | finish by hand from the run's text, then `acknowledge` |

A run that left the machine half upgraded is let go of only with the word that the status gives in `ack_word`, typed in `word`: closing it drops the flag that keeps backups and updates away from a machine that dpkg has begun to change.

## When it stops

Before the point of no return every stop is undone: the sources are put back, the timers of apt are started again, and the folder of the backup moves into the run's folder. The machine is as it was and the run is `failed`.

After the point of no return the core does nothing by itself but the check after the restart. The class of the stop says what it was:

| Class | Meaning | `resume`? |
|---|---|---|
| `busy` | another package manager runs, or the lock is held | yes |
| `network` | a download or an update did not go through | yes |
| `apt_failed`, `dpkg_broken`, `unknown` | apt or dpkg stopped, or something nobody foresaw | yes |
| `disk` | there is not enough room | yes, after making room |
| `barrier_kernel`, `barrier_initrd`, `barrier_grub`, `barrier_sshd`, `barrier_resolver`, `barrier_other` | what stands before the restart is not as it must be | yes, after fixing what the line says |
| `identity`, `tools` | the machine is not what the block expects, or a tool is missing | yes, after looking |
| `guard`, `converge`, `unit`, `integrity`, `plan` | a stop that says the repair itself is not to be trusted | no: take the run over by hand |
| `abort` | the owner's call-off | |

`resume` starts a unit of its own with the blocks that remain, and ends again at `awaiting_reboot`. When the stop was in the minimal or the full upgrade it first mends what dpkg left half done: it stops the units of apt, configures what dpkg began, and lets apt repair only if what apt would remove is among the removals that the plan names.

**Taking the run over.** `ack` with `manual` stops the core from acting: nothing is launched from then on. The run's own text is `GET /v1/sys/os/upgrade/runbook?run=current`: the same blocks that the unit ran, to be read and pasted into a console one at a time, over the same backup folder and the same `done-*` files, with the table "IF YOU STOPPED" that says what to do after each block. It is kept in the run's folder as `manual-runbook.sh`, and holds your sources: root reads it, and the call takes your access token in the header and no other. The status says where you stand (`manual`: the blocks done, the next one, whether the point of no return is behind, the backup folder).

## The calls

All are under `/v1/sys`, behind the token of the dashboard. Every `POST` takes the access token in the `Authorization` header (not in the query, not the refresh token). A body is one JSON object, with no field besides the ones below, at most 64 KiB; anything else is `400` with `error_code` `bad_request`. A refusal is `409` with the code in `data.error_code`, the English reason in `data.error`, and what makes the code precise in `data.data`.

| Call | Body | Answers |
|---|---|---|
| `GET /os/upgrade/status` | | 200 the status, always; safe to poll every few seconds |
| `POST /os/upgrade/refresh` | `accept_releaseinfo_change` (true or false, false when not said) | 202 the status; 409 `beta_off`, `not_root`, `identity_unsupported`, `hop_not_planned`, `run_open`, `manual_run_found`, `readiness_refuses` (only `busy`, `apps`, `broken_dpkg`, `held`, `disk_root`, `disk_archives`, `disk_boot` and the checks of the sources: the others are the hop's), `busy`, `apps_busy`, `no_room`, `source_unrecognized` (`file`, `line`, `reason`, `code`: the sources are in a form that cannot be written, or a repository could not be told: run `apt-get update` first), `runbook_unsafe`, `runbook_too_large`; 500 `start_failed` (nothing was changed) |
| `POST /os/upgrade/start` | `plan_id`, `rehearsal`, `safety_net`, `hostname`, `accept_removals`, `paused_sources`, `acks` | 202 the status; 409 `beta_off`, `not_root`, `identity_unsupported`, `hop_not_planned`, `run_open`, `manual_run_found`, `no_plan`, `plan_changed`, `plan_blocked`, `plan_older_than_refresh`, `bad_safety_net`, `hostname_mismatch`, `acks_missing`, `removals_mismatch`, `paused_mismatch`, `sources_changed`, `readiness_refuses`, `busy`, `apps_busy`, `no_room`, `runbook_unsafe`, `runbook_too_large`; 500 `start_failed` (nothing was changed) |
| `POST /os/upgrade/reboot` | `{}` | 202; 409 `no_run`, `run_unreadable`, `not_awaiting_reboot`, `barrier_failed`, `unit_active`, `dpkg_locked`; 500 `reboot_failed` |
| `POST /os/upgrade/resume` | `{}` | 202; 409 `no_run`, `run_unreadable`, `not_resumable`, `unit_active`, `busy` |
| `POST /os/upgrade/abort` | `{}` | 202; 409 `no_run`, `run_unreadable`, `not_abortable` (`point_of_no_return`, `past_block`, `not_running`) |
| `POST /os/upgrade/ack` | `action` (`acknowledge`, `discard`, `manual`), `word` | 200 the status; 409 `no_run`, `run_unreadable`, `not_terminal`, `word_required`, `bad_word`, `unit_active` |
| `POST /os/upgrade/beta` | `enabled` (true or false, always said) | 200 `{"beta_enabled": …}`; 409 `run_open` |
| `GET /os/upgrade/runbook?run=current` | | 200 `{plan_id, hop, text, blocks, from_run}`; 409 `no_run`, `run_unreadable` |

The status, always: `beta_enabled`, `state` (and `reason`, `cause`), `run_id`, `kind`, `hop`, `plan_id`, `phase`, `attempt`, `block`, `pnr` (the point of no return is behind), `reversible`, `half_upgraded`, `busy`, `since`, `last_activity_at`, `expires_at`, `failure` (stage, class, status, the line of the unit's output that said it, a hint), `reboot`, `barrier`, `findings`, `not_returned` (containers that did not come back), `manual`, `actions`, `ack_word`, `after_reboot_hint`, `boot_unknown`, `ignored_markers`, `log_tail` (the end of the unit's output, with the addresses of your sources reduced to their host). Every text is a code; the line of the unit's output is detail and nothing reads it.

## Where things are

- The run's folder, `/var/lib/casaos/os-upgrade/<run id>/` (root only): `run.json`, `plan.json` (with the sources, passwords included, so it is private), `driver.sh`, `blocks/`, `lists/`, `SHA256SUMS`, `manual-runbook.sh`, `markers` (the log that the state is read from), `output.log`, `facts-before.json`, `bk-path`, `abort`, `barrier.json`, `verify.json`, and `bk-aborted/` or `bk-done/` once the backup folder has been moved. The last three runs are kept.
- Beside them: `current` (the latest run), `busy` (the flag), `last-refresh`, `notified` (what the owner has been told).
- The backup folder of a hop: `/var/backups/casaos-os-upgrade-<plan id>`: a copy of `/etc/apt`, the notes of the machine (listening ports, failed units, containers, holds, kernel), the restore script, and one `done-<block>` file for each block that ended.
- The unit: `casaos-os-upgrade.service`, transient. `systemctl status casaos-os-upgrade` and `journalctl -u casaos-os-upgrade` say what it is doing; `output.log` says what apt says.

A run is read from its folder and the unit's own files, never from what the core holds in memory, so the core can be restarted, updated or lost at any moment without touching a unit that runs.

## The first beta, in two steps

**Beta 0, which can be undone** (take a snapshot anyway): switch it on; read the readiness report and the plan on your real sources first (if they still say `security.debian.org` for bullseye, `apt-get update` meets the archive gap seen on 2026-10-09: the bullseye security index lists packages whose files are gone, and the plan, which rewrites the sources to bookworm, does not); rehearse and discard the rehearsal; start another and call it off while it runs; restart the machine while it is rehearsed. Make the plan after the refresh, not before it; and since the refresh keeps your own sources, look at them first: a `security.debian.org` line for bullseye, or a repository whose release information changed, stops it with the code that says so. This exercises the driver under the real `systemd-run` and dash, the refresh on your real backlog (ordinary dpkg, perhaps a restart, so the boot id for real), the readiness, the plan and the two hashes on your real sources, the copy of the sources and the restore on your real files, `apt-get update` against the real mirrors, the comparison of the simulation with your own removal set (does `fuse` really go), the size of the download, the timers stopping and starting, the flag against your own auto updates and backups, the core restarting with a run open, and the alerts.

**Beta 1, with the snapshot**: the full hop. It exercises everything else: the point of no return, the restart of ssh at libc6, the kernel, initramfs and grub on your `/boot`, the checks of `/etc/fstab` and of the names of the network cards before the restart, the restart onto bookworm, the check, the judge, and taking the run over by hand if a step fails. Write down the durations of each step, the bytes downloaded, the free space before and after, the versions of dash, mawk, grep and coreutils, `systemctl --failed` before and after, the containers and the ports.

## What is yours to decide

- when the flag is switched on, and that it stays off elsewhere;
- the snapshot before the hop, and whether you are at the console: the page says "no way back except your snapshot or console", and you are the only one who can make that true;
- whether a run left in `awaiting_reboot` for days should release backups and automatic updates after some time (today it never does: the run stays closed until you close it);
- when the automatic repair (zero tries today) goes to two, for the classes `busy` and `network` only, after the runs that kill the unit and cut the power have passed on a guest, and when it widens;
- when the hop's row in the table of supported systems goes from `report` to `beta` and, later, to `stable` (nothing in the engine reads it yet);
- whether to update Docker after the hop;
- the French and English sentence of each state, class and finding;
- whether the release of AppManagement that skips its backups goes out with this one.

The numbers that the engine decides with (how long it waits for a unit that has gone quiet, for a restart, for the machine to be ready, how often it looks, how long a rehearsal lives) are guesses until a real guest has printed real ones; they are all in one file, `pkg/osrun/timing.go`.
