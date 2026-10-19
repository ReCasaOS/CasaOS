# Modules of ReCasaOS: the manifest, and what the upgrade of the operating system does with a module

A module is a component that is installed on its own: a service of its own, packages of its own, a release of its own. The core shows it only when it is installed and answers through the gateway. This page says what the core reads of a module, how far it trusts it, and what the upgrade of the operating system does about it today.

## The manifest

Each module ships `module.json` in its release archive, and the installer copies it to `/etc/casaos/modules/<name>.json`. The installer reads it to install; the core reads it too, to know that the module is there and what it needs around an upgrade.

```json
{
  "schema": 1,
  "name": "virt",
  "title": "Virtual machines",
  "description": "Create and run virtual machines with libvirt and QEMU.",
  "component": "CasaOS-VirtManagement",
  "unit": "casaos-virt-management.service",
  "gateway_route": "/v2/virt_management",
  "packages": { "common": ["qemu-utils", "libvirt-daemon-system"], "amd64": ["qemu-system-x86", "ovmf"], "arm64": ["qemu-system-arm", "qemu-efi-aarch64"] },
  "min_recasaos": "0.5.23",
  "os": ["debian:12", "debian:13", "ubuntu:24.04", "ubuntu:26.04"],
  "os_upgrade": {
    "stop_units": ["casaos-virt-management.service"],
    "stop_timeout_seconds": 300,
    "maintenance": {
      "prepare": "/v2/virt_management/maintenance/prepare",
      "status": "/v2/virt_management/maintenance/status",
      "resume": "/v2/virt_management/maintenance/resume"
    }
  }
}
```

| Field | Rule |
|---|---|
| `schema` | `1`. A change of the format is a new number. |
| `name` | `[a-z][a-z0-9-]{0,31}`, and the name of the file (`virt.json`). |
| `title` | 1 to 60 printable characters (no control character, no mark that turns the text round), with at least one letter, number, punctuation mark or symbol that shows: a title of spaces, of Braille blanks or of Hangul fillers is refused. |
| `description` | optional, at most 300 printable characters; the dashboard shows it as text. |
| `component` | `[A-Za-z][A-Za-z0-9._-]{0,63}`. |
| `unit` | a systemd service: `[A-Za-z0-9][A-Za-z0-9:_.@-]{0,63}.service`, and not one of the core's: not `casaos-module-<anything>` (the unit of a job of a module, which a remove would stop with the job), not the transient units that the rest of the box waits for while they run (`casaos-update`, `casaos-package-update`, `casaos-docker-update`, `casaos-os-upgrade`, `casaos-os-upgrade-measure`), not the services of the core (`casaos`, `casaos-gateway`, `casaos-user-service`, `casaos-message-bus`, `casaos-local-storage`, `casaos-app-management`, `casaos-wsdd`, `rclone`, the instances of `usb-mount@`) and not the units of apt that make the box busy (`apt-daily`, `apt-daily-upgrade`), all of them with `.service`. A manifest, and so a catalogue, that says one is refused. |
| `gateway_route` | `/v<digits>/<name>`, lower case, digits and underscores, the name at most 40 characters, and not a name that the core or a service of the distribution answers on, under any version: `sys`, `users`, `usb`, `disks`, `storage`, `cloud`, `recover`, `other`, `zt`, `samba`, `file`, `folder`, `image`, `notify`, `gateway`, `casaos`, `app_management`, `message_bus`, `local_storage`, `port`, `driver`, `batch`, `test`, `health`, `apps`, `container`. |
| `packages` | `common`, `amd64`, `arm64`; at most 32 each and 40 in all; Debian package names (lower case, two to 64 characters, not ending in `-`, `+` or `.deb`, which apt reads as orders and as a file), none twice in a list. |
| `min_recasaos` | three numbers: `0.5.23`. |
| `os` | at least one, at most 16: `debian:12`, `ubuntu:24.04`. |
| `os_upgrade` | optional; see below. `stop_units` can only be the module's own `unit`. |

The file is data, and the core trusts it only as far as it validates it: every value is checked against a fixed pattern, a field that is not in the table is an error, a key that comes twice is an error, the file has 64 KiB at most and is a regular file (a link is refused), and nothing in it is ever run as a command, put in a shell or opened as a path. A manifest that does not pass is a module that cannot be told, and the report says so.

Strictness is the rule for every file of this kind: a manifest, and a catalogue, with a field the reader does not know is refused whole, and a new field is a new `schema`.

## The catalogue

Each release of ReCasaOS carries the list of the modules it can install, `/usr/share/casaos/modules/catalogue.json`. Nothing that a module's author publishes reaches a box by itself: the box installs what the catalogue names, at the tag it names, and only the archive whose sha256 it gives. The file is read as a whole, and if one rule is broken anywhere in it there is no module at all and an error, never the entries that happened to pass.

```json
{
  "schema": 1,
  "modules": [
    {
      "name": "virt",
      "component": "CasaOS-VirtManagement",
      "tier": "community",
      "tag": "v0.1.0",
      "commit": "0123456789abcdef0123456789abcdef01234567",
      "assets": {
        "amd64": { "url": "https://github.com/ReCasaOS/CasaOS-VirtManagement/releases/download/v0.1.0/linux-amd64-casaos-virt-management-v0.1.0.tar.gz", "sha256": "<64 hexadecimal digits>" },
        "arm64": { "url": "https://github.com/ReCasaOS/CasaOS-VirtManagement/releases/download/v0.1.0/linux-arm64-casaos-virt-management-v0.1.0.tar.gz", "sha256": "<64 hexadecimal digits>" }
      },
      "manifest": { "schema": 1, "name": "virt", "...": "the manifest of the module, whole, as above" }
    }
  ]
}
```

An entry carries `component` (the repository of the module, in the ReCasaOS organisation) and the full `manifest`, which is the text of the `module.json` of the archive: the box needs it before it has the archive, to say whether the system is supported, which packages the module needs and what to show of it. The manifest must pass every rule above, its `name` is the entry's, and its `component` is the entry's `component`.

| Field | Rule |
|---|---|
| `name` | as in the manifest, once in the catalogue. |
| `component` | as in the manifest. |
| `tier` | `core` (maintained here and proven on a real machine) or `community` (proven in CI against a test double, and said so to whoever installs it). |
| `tag` | `v<number>.<number>.<number>`. |
| `commit` | 40 lower-case hexadecimal digits. |
| `assets` | `amd64` and `arm64`, at least one; each has a `url` and a `sha256` (64 lower-case hexadecimal digits). The `url` is exactly `https://github.com/ReCasaOS/<component>/releases/download/<tag>/linux-<arch>-casaos-<name>-<tag>.tar.gz` (no port, no user, no query, no other host, no other organisation) and at most 512 characters. |

The file has 1 MiB at most and 64 modules at most.

## What an archive may write

The archive of a module is checked against the sha256 of the catalogue, which proves it is the archive the catalogue names and not that what is in it is harmless. The unpack reads every entry as hostile data and writes three kinds of file, each directly in its directory, and nothing else:

| Where | What |
|---|---|
| `/usr/bin/<name>` | a program: a file, or a link to a file beside it. |
| `/usr/lib/systemd/system/` or `/lib/systemd/system/` | the unit of the manifest, exactly its `unit`, as a file. No other name: not another service, not a timer, not a template, not a drop-in directory (`<unit>.d/`) or a directory of wants. On a system with a merged `/usr` the two are one directory, and an archive that names the unit under both is refused. |
| `/etc/casaos/<name>.sample` | the sample of the module's configuration: a file whose name ends in `.sample`. The configuration itself is the administrator's, and an archive that carries one is refused. |

`module.json`, `LICENSE` and `NOTICE` at the root of the archive are read and not written to the box. `/var/lib/casaos` and `/usr/share/casaos` are not in the list at all: the core keeps its state there (alerts, telemetry, the choices of the automatic updates, the catalogue, the lists of the modules), and nothing a module has to ship goes in them.

Every other path, a subdirectory of an allowed directory included, an entry that is not a file, a directory or a link, a link that points outside its own directory, a name with a character outside `A-Za-z0-9._-+@:`, is refused whole with `unpack_path` before anything is written. A file that is there and was not written by an earlier install of the same module is never overwritten (`file_exists`), including one that appears after the check: the file is put in place by a rename that does not replace. The modes are the archive's, without the setuid, setgid and sticky bits and without write for the group and the others.

The install keeps two lists in `/var/lib/casaos/modules/`: `<name>.files`, the files and links it put down, the unit last, one to a line behind a first line `casaos-module-files 1` (`f <sha256> <path>` for a file, `l <target> <path>` for a link; two comma-separated contents only for a file that an install is replacing, so that whichever is on the disk when a kill comes is the module's); and `<name>.dirs`, the directories it had to make because the system had none (the allowed directory and the ones above it, when the system lacks them). Both are written, whole, before the first directory is made and the first file put in place, so that a process that is killed half way leaves lists that name everything that may be there. A remove deletes exactly the files of the first that still hold what it says (a path that was planned and never written, or that somebody has changed or replaced, is left, and said: `left: <path> (changed)`), and then the directories of the second that are empty, the deepest first; the lists of an earlier format, which name paths and nothing to check them against, make a remove delete none of their files and name them. A rollback of an install that stopped deletes what the unpack says it wrote, in the same way, and not what it was going to write; an install that ends rewrites the list to exactly what it wrote.

## The plan of an install

The plan asks the box, and changes nothing. Its checks, in this order, are `catalogue`, `os`, `arch`, `recasaos`, `already_installed`, `foreign_unit`, `busy`, `dpkg_state`, `disk` and `apt`; `recasaos` compares the number of the DISTRIBUTION (the tag that the installer writes to `/var/lib/casaos/fork-release`, `v0.5.29`) with `min_recasaos`, and not the version of the core. A check that was not made because an earlier one had already failed says `not_checked` and is not the reason of the refusal. A plan asked for by a user who is not root cannot look at the locks of dpkg and apt: `busy` then says `not_checked: needs root, the install checks it as root`, is not ok and is not the reason of a refusal either, and the rest of the plan is made (the units that update the machine, the flag of the upgrade of the operating system, the room, apt); A release of the distribution that is not known (no marker in `/var/lib/casaos/fork-release`, or one that is not a release) makes `recasaos` say `unknown`, and the module is refused. A plan is refused with the code of the first check that failed, except for these two checks, which say more:

| Refusal | Meaning |
|---|---|
| `dpkg_state` | `dpkg --audit` could not be run or gave no answer (the check says `unreadable`). A dpkg that lists packages it has not finished (it was interrupted) is not a refusal: the check is not ok, its detail is `interrupted: ` and the names of the packages (printable ASCII, cut), and the plan still has its id, because the install finishes them (see below) and a refusal would keep the dashboard from ever starting that install. A box without `dpkg` says `not_checked: dpkg is not there`. |
| `disk_short`, `disk_unknown` | there is not room on `/var/lib/casaos/modules/tmp` or on `/usr`, or the room or the sizes cannot be told. |
| `apt_missing` | `apt-get` is not there. |
| `apt_failed` | apt gave no answer, or an answer that was not understood. When packages cannot be installed together (a hold, a conflict) the detail carries the lines of apt's `The following packages have unmet dependencies` block. |
| `apt_removes`, `apt_downgrades` | installing the packages would remove or downgrade a package. |
| `held_package` | a package that the install would change is on hold. |
| `package_not_exact` | apt would install something other than the packages that were asked for. apt reads a name with a `.` or a `+` in it as a regular expression when no package has exactly that name (`mod.lean` selects `modclean`), and a name that a package provides as that package; every package of the manifest must come back in the simulation under its own name, so a virtual package is refused too: a manifest names real packages. |

The simulation is `apt-get -s install --no-install-recommends <packages>` and the sizes come from `apt-get --print-uris install --no-install-recommends <packages>`, both in a sandbox and with the log of the planner turned off. The second says what apt would download and use, prints the addresses and ends, installing and downloading nothing. (`-o APT::Get::Trivial-Only=true` is not used for this: apt goes on with a transaction that has as many packages as names, which installs it.) The sandbox keeps the settings of the box that decide what is installed, among them `APT::Default-Release`.

### dpkg and the `packages` step

Before the step asks apt for anything, the install runs `dpkg --audit` (English, no terminal, one minute at most; it reads the status file, takes no lock and writes nothing). When it lists packages (an install that was killed in the middle of a package leaves them `unpacked` or `half-configured`, and apt then refuses everything with `dpkg was interrupted`), the install says `dpkg was interrupted: <packages>; running dpkg --configure -a`, runs `dpkg --configure -a --force-confold` (no question, the configuration files that are there kept, ten minutes at most), asks the audit again and, when it lists nothing, says `dpkg_repaired: <packages>` and goes on to apt. A dpkg that is still interrupted (the configure failed, could not be run, or the audit still lists packages) ends the step `failed` with the code `dpkg_broken` and the reason in the log; apt is not run. A dpkg that cannot be asked at all (no `dpkg`, or an audit that gave no answer) does not stop the install: apt says what it finds.

When the install is ended while apt-get works (SIGTERM of `systemctl stop`, SIGINT, SIGHUP), apt-get alone is sent SIGINT, which it answers by finishing the dpkg run it is in, and is waited for for as long as it takes, at most five minutes. SIGTERM would not do: apt-get dies of it and dpkg, which had apt-get for its status pipe, dies of SIGPIPE between two packages, leaving packages `unpacked` and `half-configured`. When apt-get has not left after the five minutes it is stopped like every command (SIGTERM to its group, twenty seconds, SIGKILL); the five minutes and the twenty seconds are within what the unit gives the assistant to stop (`TimeoutStopSec=420`). The run ends `failed` with the code `interrupted`, as before.

## Installing and removing from the dashboard: the routes of the core and the job

The core runs the module assistant (`casaos module ...`) in its own process for what only reads, and as a transient unit for what changes the box. The routes are below `/v1/sys`, behind the token of the group like the others.

| Route | Does | Token |
|---|---|---|
| `GET /v1/sys/modules` | the list: the catalogue of this release joined with what is installed (`catalogue` with `ok` and, when it is not, `error`: `none`, `unreadable` or `invalid`, with `detail`; `modules`; each module has `update` when it is installed, the catalogue has it and its installed version can be read: see "Updating an installed module"), as `casaos module list` prints it. A catalogue that is not there or does not pass is an empty list that says so, never a partial one. | the group's |
| `POST /v1/sys/modules/{name}/plan` | the plan of an install, or with the body `{"mode": "update"}` of the update of a module that is installed (no body is an install), made in the core, for at most 60 seconds, one plan at a time: the object of `casaos module plan` (checks, `refused`, packages, archive, `plan_id`). A plan that is refused is still a plan: 200, with `refused` set. | the access token in the header |
| `POST /v1/sys/modules/{name}/install` | starts the unit of the install for the plan the user read. Body `{"plan_id": "m1-..."}`; with `"mode": "update"` as well it starts the unit of the update of a module that is installed, for the plan of an update (`plan_id` is the id of the plan of that mode; a `mode` that is not there is `install`). 202 with the status. | the access token in the header |
| `POST /v1/sys/modules/{name}/remove` | starts the unit of the remove. Body `{}`. 202 with the status. | the access token in the header |
| `GET /v1/sys/modules/{name}/status` | `{"job": ..., "status": ...}`: how the job of the module is going, and the object of `casaos module status`. | the group's |

What starts a unit or runs apt takes the access token in the `Authorization` header and no other: not the one in `?token=`, not the refresh token (401). `{name}` is checked with the rule of the name of a module before anything else, and a name that does not pass is a 400. The bodies are strict JSON, as those of the upgrade of the operating system are: one object, no field that the request does not have, no key twice, no key spelled otherwise, at most 64 KiB; what is wrong is a 400 `bad_request` that names the field (`name`, `plan_id` or `body`) and never repeats what was sent. Every answer is `{"success", "message", "data"}` and is not to be kept (`Cache-Control: no-store`).

A request that is refused is a 409 with the code in `data.error_code`, the English reason in `data.error` and what makes the code precise in `data.data`; `bad_request` is a 400 and `start_failed` a 500.

| Code | Meaning |
|---|---|
| `bad_request` | the name, the body or a field is not valid (`data.data.field`). |
| `not_root` | the core does not run as root. |
| `unsupported` | the host is not a Debian-family system with `apt-get` and `systemd-run`. |
| `busy` | something else is at work, and `data.data.reason` always says what, in English: a job of a module (its log says it has not ended and it was seen alive less than 30 seconds ago, or its unit runs; the log of a unit that has emptied it and has not said a word yet counts, for as long as it was written lately), a job whose unit systemd would not say anything of and whose log does not say that it ended, a package update, an update of Docker, the update of ReCasaOS, the upgrade of the operating system (its unit, or its flag, which is read so that a flag that cannot be read is an open one), a package manager that holds dpkg's lock or a lock that cannot be looked at, a run of the assistant by hand that holds its lock, another start of a job that this one waited for no longer than its request lasted, or, for a remove, the assistant finding a package manager or the upgrade at work. An install whose plan finds the box busy (a package manager or the upgrade took it after the first look) says the same, and carries `data.data.checks` as well. Nothing is written when it is said. |
| `plan_changed` | the plan the box makes now is not the one with that `plan_id` (the archive, the tag, the packages or the system changed): read the new plan and confirm again. |
| `plan_timeout` | the plan could not be made in its time. |
| `not_installed` | a remove of a module of which nothing is on the box: no manifest, no list of files (`.files`), no list of directories (`.dirs`), no version. A module that an install left half done is something to remove. |
| `start_failed` | the program of the unit, `/usr/bin/casaos`, is not a regular file that can be run (the detail names it); or the log could not be written; or systemd answered that it did not start the unit. Nothing was changed. When systemd did not start the unit the log of the module says so (`failed`, code `start_failed`), and when the log itself could not be written the log cannot, and only the core's own log and this answer record it. |
| the codes of the plan | an install is refused with the code of the first check of its plan that fails: `catalogue`, `os`, `arch`, `recasaos`, `already_installed`, `foreign_unit`, `busy`, `disk_short`, `disk_unknown`, `apt_missing`, `apt_failed`, `apt_removes`, `apt_downgrades`, `held_package`, `package_not_exact`; `data.data.checks` is the list of the checks. |

The jobs make the box busy for what changes it: `casaos-module-*.service` is among the units that the packages window, the update of Docker, the update of ReCasaOS, the automatic updates, the readiness report and the start of the upgrade of the operating system ask about before they start (the backups do not ask), and a module job is not started while any of them runs. A start of a job looks at the box twice, once before its plan and once after it, with a time of its own for each, and the second look, the log and the launch are made holding the lock that the starts of a package update and of an update of Docker hold: none of them looks at the box while another is between its look and its unit.

### The unit

An install is `systemd-run --quiet --no-block --collect --unit=casaos-module-<name>.service --property=Type=exec --property=KillMode=mixed --property=TimeoutStopSec=420 --setenv=CASAOS_MODULE_NONCE=<32 hex digits> -- /usr/bin/casaos module install <name> --yes --plan-id <id>`, and a remove is the same with `module remove <name> --yes`. The command line is closed: the name is a name of a module, the id is `m1-` and 32 hexadecimal digits, the nonce is 32 hexadecimal digits from the system's random source, and no word holds a dollar sign or a percent sign. The unit has three properties and no other: the type; a kill mode that asks the assistant alone to stop when the unit is stopped (`systemctl stop`, a shutdown), so that apt and dpkg are asked to leave by the assistant, in its own way, and are not given a SIGTERM beside it; and 420 seconds to stop, which is above what the assistant needs (its rollback has 5 minutes, and a command that it ends is given 20 seconds to leave, twice), where the 90 seconds of a unit that sets none would cut the rollback and write no result. The unit sets nothing else, `CASAOS_MODULE_TEST` never. The core starts nothing before it has looked at `/usr/bin/casaos`: a file that is not there, that is not a regular file (a link to one is) or that cannot be run is `start_failed`, whose detail names the program.

The core writes the first line of the log of the module before it starts the unit, holding the lock of the assistant (a log that a run is writing is not touched, and a run started by hand meanwhile is refused as busy):

```
CASAOS_MODULE_QUEUED <nonce> <name> <kind> <time>
```

`<kind>` is `install`, `remove` or `update`: what the core started, which is the verb of the unit it is about to start (an install and a remove have steps and a result in common, so the markers of the unit cannot say which of them it is). The line is read as strictly as the other markers: one blank between the fields, none empty, the nonce and the time as the others have them, and a kind that is neither word, a line with no kind, or a field more, is not a marker at all (it is no word of the job's, and the job begins with the first line that is).

The unit keeps this first line and replaces the rest of the log with its own lines (see the grammar in `pkg/modulecmd/job.go`), which carry the same nonce. It keeps it only when it is the line of the run the unit is: a whole line, of this module, with the nonce the unit was given, whose kind is the verb the unit runs. Any other first line is replaced with the rest, so that a run started by hand leaves no kind behind. The kind is read from the first line of the log and from no other. When `systemd-run` reports an error, the call may have queued the unit all the same (its wait for the bus ended, or its reply was lost), so the core asks systemd whether it has the unit: if it has, or if it does not answer, the log is left as it is and the answer is 202 with the job as the log says it (running, or whatever the unit has written already, a result included); only when systemd answered that it has no such unit does the core close the log itself, and then only if the log is still exactly the line it wrote (a unit that began meanwhile holds the lock of the assistant or has replaced the log, and its word is not overwritten). The log is then the queued line, `failed: start_failed`, what systemd said (as text, behind `| `, three lines at most), and `CASAOS_MODULE_RESULT <nonce> <name> failed - start_failed - <time>`, and the answer is the 500 `start_failed`. The core keeps nothing about a job in memory and writes no file but this log: the state is read from the log and from the unit each time, so a restart of the core in the middle of a job, or after it, changes nothing, and the core never starts a job again by itself.

### The state of a job

`data.job` of the status, and of the answer of an install and of a remove:

| Field | |
|---|---|
| `name` | the module. |
| `kind` | what the job is: `install`, `remove` or `update`, as the core started it, read from the first line of the log (the queued line, above). `""` when the log does not say: it does not begin with a queued line of the job (a job that was started by hand with a nonce of its own), or the first line is not as the core writes it. A job has its kind in every state, a finished one included, for as long as its log is there, and the same after a restart of the core. |
| `state` | `idle`, `running`, `succeeded`, `refused`, `failed` or `interrupted` (below). |
| `step` | where the job is: the step of the last `CASAOS_MODULE_STEP` line. An install goes `packages`, `download`, `verify`, `unpack`, `units`, `manifest`, `start`, `route`, `done`; a remove `stop`, `files`, `manifest`, `reload`, `done`; an update `packages`, `download`, `verify`, `unpack`, `backup`, `stop`, `swap`, `units`, `start`, `route`, `done`; the rollback of an install that failed runs the steps of a remove behind `rollback_` (`rollback_stop`, ...), that of an update `rollback_restore`, `rollback_units`, `rollback_start`, and the recovery of an update that was ended `recover_restore`, `recover_units`, `recover_start`. For a job that failed it is the step of the result (`""` when it failed before its first step); for one that succeeded it is `done`; for one that was refused or has not begun it is `""`. |
| `code` | why the job was refused or failed: the code of the result line (the codes of the plan, `plan_changed`, `busy`, `not_installed`, `checksum_mismatch`, `download_failed`, `download_too_large`, `packages_failed`, `dpkg_broken`, `held_package`, `unpack_path`, `file_exists`, `units_failed`, `manifest_invalid`, `start_failed`, `route_timeout`, `interrupted`, `internal_error`, and for an update `up_to_date`, `downgrade`, `version_unknown`, `files_unverified`, `files_unreadable`, `unit_changed`, `backup_failed`, `swap_failed`, `unit_not_stopped`, `recovery_incomplete`, ...); `""` otherwise. |
| `undo` | `rolled_back` or `rollback_incomplete` for an install or an update that failed after it had changed the box (`casaos module remove` finishes an incomplete rollback of an install; the next run of the module finishes that of an update), `""` otherwise. |
| `kept_packages` | the packages of the module that an install which failed leaves on the box, from the sentence `kept_packages:` that comes before the result: a list (empty when none), or `null` when it is not said or not known. Packages are never removed. |
| `started_at`, `ended_at` | the times of the first line and of the result line, RFC 3339 in UTC; `""` when there is none. |
| `log_tail` | the last 20 lines of the log that are not marker lines (`CASAOS_MODULE_...`: they carry the nonce, which is in no answer of the core), as text: printable ASCII, a URL reduced to its host, at most 200 characters. They are the sentences of the assistant and the output of the tools and of the module; the page shows them as text. |

| State | When |
|---|---|
| `idle` | no job: there is no log, or the log has no line of a job of this module, and no unit runs. A log of a run by hand (nonce `-`) is not a job. A log with nothing in it is not this: see `running` and `interrupted`. |
| `running` | no result line, and the unit is active, or it was gone for less than 30 seconds since the last sign of life of the job (a marker, or the time the log was written), or systemd does not answer and the last sign of life is less than 30 minutes old. A time of the log more than 30 seconds in the future is not a sign of life. A log that is empty is the log of a unit that has truncated it and is making its plan, which takes up to a minute, and says nothing before it (a unit that the core started keeps the first line it wrote, and its log is not empty: the line is a sign of life by its time): it is `running` while it was written lately by these rules. |
| `succeeded` | a `CASAOS_MODULE_RESULT ... ok done - - ...` line with the nonce of the job. |
| `refused` | a result line `refused`: nothing was changed; `code` says why. |
| `failed` | a result line `failed`: `step`, `code`, `undo`, `kept_packages`. |
| `interrupted` | no result line, and the unit is gone for more than 30 seconds (it was ended in the middle: killed, the machine was restarted): `step` is where it was. Nothing was undone: `casaos module remove` finishes the work. A job whose unit never came (and a unit that was refused before it could write: another run of the assistant held its lock) is `interrupted` with `step` `""`, and so is an empty log whose unit is gone and that was not written lately (a unit killed between the truncate and its first line). |

A line is the word of the job only if it is a whole line (ended by a new line) of exactly the shape of a marker, its time in UTC to the second, with the nonce of the job and the name of the module, that does not begin with the `| ` of the output of a tool; the fields of a result line must agree with its verdict. The first result line is final: nothing after it changes the job, and a job that finished stays what it was for as long as its log is there (the next job of that module replaces the log). The nonce of the job is the one the core wrote; when it has to be found again after a restart it is the first marker of the module in the log that has one, and a marker with the nonce `-` never counts. A log is read whole up to 8 MiB, and beyond that by its first and last 4 MiB; of a line the first 4096 bytes are held.

### To be proved on a real guest

The tests of the core put a fake in the place of systemd, and what follows is what the fake takes for granted. It has not been seen on a real system, and the proof on a guest with systemd has to check each point before this page says that it holds:

- `systemctl show 'casaos-module-*.service' --property=ActiveState --value`, which every change of the box asks before it starts, with a pattern: the answer is nothing at all, and the exit status is 0, when no unit matches; for several units it is their states, in blocks that an empty line separates (the reading takes the words of the answer and does not depend on the separator); while an install runs the unit is `active` in it.
- `systemctl show <unit> --property=LoadState --value` for the unit of a job: `loaded` while it runs, and `not-found` once it has ended and been collected. The core asks it when `systemd-run` reports an error, to know whether the unit was queued all the same.
- the log of a job that the dashboard started, read on the guest once the unit has run: its first line is the `CASAOS_MODULE_QUEUED` line with the kind (`install` or `remove`) and the next is the first `CASAOS_MODULE_STEP` line, and `GET /v1/sys/modules/<name>/status` says the same kind while the job runs and after it has ended.
- `systemd-run` accepts `--property=KillMode=mixed` and `--property=TimeoutStopSec=420`, `systemctl show casaos-module-<name>.service --property=KillMode --property=TimeoutStopSec` says `mixed` and `7min`, and a `systemctl stop` of the unit during an install lets the assistant roll back (a `failed` result with `rolled_back`) before systemd kills what is left.
- the update: `systemctl stop <unit>` of the running unit is followed by `systemctl is-active <unit>` saying `inactive` (the unit stays enabled: `is-enabled` says `enabled`), `systemctl start <unit>` brings it back, the swap of the program of a running unit by a rename (the new file is used by the next start, the old one by the process that holds it) and of the unit file followed by `daemon-reload` leave the unit startable, and a `systemctl stop casaos-module-<name>.service` during each step of an update ends in `rolled_back` with the old version running. The proof is the one of the real guest, with a module of two tags; the tests of the core run it on a Linux file system and with the stubs of `systemctl` and `apt-get`.

## Updating an installed module

An installed module is updated to the tag the catalogue of this release names, by `casaos module update <name> [--yes] [--plan-id <id>]`, which the core runs as the unit of a job like an install. Nothing is planned for a module the catalogue does not have, and a module is never taken back to an earlier tag (`downgrade`). Versions are compared as numbers: `v0.1.9` is earlier than `v0.1.10`.

### What the list says

Each module of `GET /v1/sys/modules` (and of `casaos module list`) that is installed, that the catalogue has and whose installed version can be read (`/var/lib/casaos/modules/<name>.version` holds a tag) has

```json
"update": { "available": true, "installed": "v0.1.2", "latest": "v0.1.3" }
```

`available` is true only when the catalogue's tag is strictly later than the installed one. The field is not there when the module is not installed, when the catalogue does not have it or when the installed version cannot be read (`installed.version` is then `null`): the list never claims an update that it cannot show to be one. The other fields are as they are.

### The plan of an update

`casaos module plan <name> --update`, and `POST /v1/sys/modules/{name}/plan` with the body `{"mode": "update"}`, give the object of the plan of an install (same strictness: it reads and writes nothing, apt is simulated in the sandbox, 60 seconds, one plan at a time) with these differences:

- `mode` is `"update"` (an install plan has no `mode`), `tag` is the catalogue's tag, and `update` is `{"installed": "<tag>", "latest": "<tag>", "added": [...], "dropped": [...]}`: `added` are the packages apt says it would install (those of the new manifest that are not installed, and what they need; `packages.to_install` is the same list with the sizes), `[]` when everything is there; `dropped` are those the installed manifest named and the new one no longer does, said as text and never removed.
- The checks, in this order, are `catalogue`, `os`, `arch`, `recasaos`, `installed`, `newer`, `foreign_unit`, `busy`, `dpkg_state`, `disk` and `apt`. `os`, `arch`, `recasaos`, `busy`, `dpkg_state` and `apt` are those of an install: apt is asked about every package of the new manifest (the ones that are there come back as the newest version, the ones that are not are `added`), and never removes or downgrades (`apt_removes`, `apt_downgrades`, `held_package` and `package_not_exact` refuse as for an install). The others:

| Check | Ok when | Refusal (code of the plan) |
|---|---|---|
| `installed` | a manifest that reads is at `/etc/casaos/modules/<name>.json` and the list of files `<name>.files` records what each file holds | `not_installed` (detail `not_installed`); `manifest_invalid` (`manifest_broken`, `unreadable`); `files_unverified` (the list is of the earlier format, which names paths and nothing to check them against: the module is removed and installed again); `files_unreadable` (`files_missing`, `files_unreadable`) |
| `newer` | the catalogue's tag is later than the installed one | `up_to_date` (same tag), `downgrade` (the installed tag is later), `version_unknown` (the installed version cannot be read; the administrator removes the module and installs it). Not made (`not_checked`) when `installed` failed. |
| `foreign_unit` | the unit file that is on the box is the module's own (the one its list of files records), and the new manifest names the unit the installed one does | `foreign_unit` (`not_owned <path>`, `owned_by <module>`: somebody else's), `unit_changed` |
| `disk` | both file systems have room for the archive three times over (the archive, its tar and its unpacked files), a copy of the files the update replaces (their size as the files are on the box) and what apt downloads and keeps | `disk_short`, `disk_unknown` (the size of the files to keep cannot be told) |

A plan that is refused has no `plan_id`. The id of an update is not the id of an install of the same archive: it also binds the mode and the installed tag, so a plan read for one is `plan_changed` for the other.

### The job

`casaos module update <name> --yes --plan-id <id>` runs as `casaos-module-<name>.service`, with the properties, the busy rules, the log `module-<name>.log` and the grammar of an install. The queued line says `update`, the job's `kind` is `update`, and a status says so while it runs and after it has ended. Without `--yes` the program shows what it will do and asks; the plan is made again once it holds the lock of the assistant, and it must be the plan whose id was given (`plan_changed`).

Steps, in this order, each a `CASAOS_MODULE_STEP` marker with its `begin` and its `done` or `failed`:

| Step | What it does | The box |
|---|---|---|
| `packages` | installs the packages of the new manifest that are not installed: `apt-get install` of the whole list with `--no-upgrade` (a package that is there is left as it is), after `dpkg --audit` and its repair as for an install; nothing when apt said there was nothing to install; never removes one | packages |
| `download`, `verify` | the archive and its sha256, as for an install | unchanged |
| `unpack` | checks the archive against the box as it is (the files that still hold what the list says are the module's own; a file of the new version that is there and is not the module's, a file the administrator changed included, refuses the update with `file_exists`), unpacks it into the staging folder `tmp/stage` with the same allow-list and limits as an install, and checks the manifest it carries (`manifest_invalid`, `manifest_mismatch`) | unchanged |
| `backup` | copies every file and link of the list, as it is found, into `tmp/backup-<name>/` (`copies/<n>`, and `old.files.actual`: the list with what each held, the sha256 of the copy; `old.owners`: the user and the group of each), with the list of files, the list of directories, the manifest and the version, `new.files` and `new.dirs` (what the swap is going to put down), and `units.state`: for the module's unit and for every other unit of the old list, whether it was running (`is-active`) and enabled (`is-enabled`), asked before anything is stopped; `COMPLETE` is written last | unchanged |
| `stop` | stops the units that were running (`systemctl stop`, then `is-active` must say `inactive` or `failed`, else `unit_not_stopped`); a unit that was not running is not touched, and none is disabled | the units that ran are stopped |
| `swap` | makes the directories the new version needs, puts each new file in place by a rename, the unit last, each after it looked again at the old file it replaces (it must still hold what the old list says, else the swap fails with `file_exists` and is rolled back; a path that was free and is not any more fails the same way), deletes the files of the old version that the new one does not have when they still hold what the old list says and no other module's list has them (a file the administrator changed is left, `left: <path> (changed)` is said at the step `unpack`, and it is no longer the module's; one that another module's list has is kept: `kept, it is the list of <module> too`), then writes the new `.files`, `.dirs`, manifest and version, each whole or not at all, in that order | the new version is in place |
| `units` | `systemctl daemon-reload` | |
| `start` | `systemctl start` of every unit that was running, and `systemctl enable` of a unit that was enabled and is not any more; no other unit is started or enabled, and the update enables none by `enable --now`. When no unit was running the step is one marker, `start skipped` (below) | the units that ran run |
| `route` | waits up to a minute for the gateway to answer on the module's route; one marker, `route skipped`, when the module's unit was not running | |

The configuration of the module that is not in the list of files (`/etc/casaos/<name>.conf`; the `.sample` files are samples) and `/DATA` are never touched. The packages are never removed. A module that its administrator had stopped is still stopped when the update is over: `start` and `route` are then written as one marker each with the phase `skipped` and no `begin` (`CASAOS_MODULE_STEP <nonce> <name> start skipped <time>`), and the job still ends `succeeded`. A run ends with the `done` marker and `CASAOS_MODULE_RESULT <nonce> <name> ok done - - <time>`; the backup folder is deleted before that (its `COMPLETE` first, and made durable, so that a deletion that is ended half way leaves a folder that nobody puts anything back from), and `list` then shows the new version with `update.available` false.

A run that is refused (`refused`, exit 2) has changed nothing. The codes of a job that failed are those of the plan and of an install, and `backup_failed` (a file of the list that is not a file nor a link, or a copy that cannot be made), `swap_failed`, `unit_not_stopped`, `unpack_corrupt`, `unpack_path`, `unpack_limits`, `file_exists`, `manifest_invalid`, `manifest_mismatch`, `start_failed`, `route_timeout`, `interrupted` and `recovery_incomplete`.

### Rollback and recovery

A failure before the backup is complete changes nothing on the box: the backup folder is dropped and the result says `failed` with the code of the step and `rolled_back`. A failure after it (an interruption by SIGTERM included, which ends the step it is in) is put back from the backup, in the steps `rollback_restore`, `rollback_units` and `rollback_start`; what there is nothing to put back for is not touched, so a unit that was never stopped is neither stopped nor started:

- `rollback_restore` first looks at every path and decides what is to be done: a path that holds the new file or is gone is put back from the backup (its copy is read and its sha256 checked first, and a copy that is not what was saved is a path that cannot be put back), a path that only the new version has is taken away when it holds what the new list says, and one that holds neither the old content nor the new one is somebody else's and is left. If every path can be done the units that were running are stopped (only when there is something to do), the files are put back with their mode and, as root, their user and group (the extended attributes and any setuid bit are not put back: a module may not ship them), the temporary names (`.cos-*`) of every path of either version and of the lists are taken away, and the list of files, the list of directories, the manifest and the version are written as they were; the directories the swap made are removed when empty. If any path cannot be done nothing is changed: the lists, the manifest and the version stay the ones the box has, the backup is kept whole, and the log says so with the way out (put the files of the old version back by hand from `copies/`, which `old.files.actual` names, and delete the folder; or delete the folder to keep what is on the box; then run the command again). It can be done again and gives the same box.
- `rollback_units` has systemd read its units again and `rollback_start` starts the units that were running and enables the ones that were enabled and are not; both are made when the restore could not be, so that the unit that ran is running.

The result is `failed <step> <code> rolled_back`, and the box, but for the log, is byte for byte what it was. If something could not be put back the result says `rollback_incomplete`, the backup folder is **kept** (the log says where: `/var/lib/casaos/modules/tmp/backup-<name>`), and the next run of the module tries again and says the same until the way out above is taken.

A job that is cut by `systemctl stop` (SIGTERM) rolls back as above, within `TimeoutStopSec`. A job that is killed (SIGKILL, the power of the machine) leaves no result: its status is `interrupted` and never `succeeded`, and the backup folder is the one copy of the old files. The next `casaos module update <name>`, `casaos module remove <name>` or `casaos module install <name>` that takes the lock puts the old version back before it does anything else, in the steps `recover_restore`, `recover_units` and `recover_start`, as above: a backup folder without `COMPLETE` is dropped (it was cut while it copied, when nothing of the box had been changed, or while it was deleted, when there was nothing left to put back); a complete one is put back, and the folder deleted by taking `COMPLETE` away first. An update that was interrupted between `swap` and its end is therefore rolled back, never finished and never left with half the files of each version; the update, if it was the run, then goes on from the old version. A recovery that cannot be finished ends the run `failed`, with the step it was in, the code `recovery_incomplete` and `rollback_incomplete`; the backup is kept. A folder that has `COMPLETE` and cannot be read (a member is missing, a list does not pass its parser) cannot be told from a box whose update is over: nothing is put back from it, it is kept, and the run says `the backup in <folder> cannot be read` and `delete the folder <folder> by hand` when the update is over and the box is as it should be, or put the files of the old version back by hand and delete it; the assistant never does it by itself, since a wrong guess puts the old version over the new. (The assistant itself never leaves such a folder: it takes `COMPLETE` away first when it deletes one.) The area of the download, which every run empties at its start and at its end, keeps the `backup-<name>` folders (directories, with the name of a module) and nothing else.

### The request field

`mode` is the field of the body of `POST /v1/sys/modules/{name}/install` and, optionally, of `POST /v1/sys/modules/{name}/plan`: `"install"` (the same as no field) or `"update"`; anything else, an empty string included, is a 400 `bad_request` with `data.data.field` `mode`. The body of the install route is read as strictly as the others (one object, no other field, no key twice, 64 KiB). The plan route took no body and ignored whatever it was sent, and still ignores it unless it is a JSON object (of at most 64 KiB) that has a `mode` key: then `mode` is a string, once, `install` or `update`, and a `mode` that is `null`, a number, twice or another word is a 400. `POST .../install` with `"mode": "update"` starts the unit `casaos module update <name> --yes --plan-id <id>` and answers 202 with a status whose job has `kind` `update`; it is refused with the codes of the update plan (`catalogue`, `os`, `arch`, `recasaos`, `not_installed`, `manifest_invalid`, `files_unreadable`, `files_unverified`, `version_unknown`, `up_to_date`, `downgrade`, `foreign_unit`, `unit_changed`, `busy`, `dpkg_state`, `disk_short`, `disk_unknown`, `apt_missing`, `apt_failed`, `apt_removes`, `apt_downgrades`, `held_package`, `package_not_exact`, each with `data.data.checks`), `plan_changed`, `plan_timeout`, `busy`, `not_root`, `unsupported` and `start_failed`, with the statuses of an install.

## What a module says about an upgrade of the operating system

`os_upgrade` is declarative, with a small HTTP contract and no hook that runs code:

- `stop_units`: the units the core stops once the module says it is ready; `stop_timeout_seconds`: how long it waits for that (1 to 3600).
- `maintenance`: three paths under the module's own `gateway_route`, called through the gateway with the internal secret. `prepare` (POST, idempotent) asks the module to get ready (a virtualization module shuts its machines down, by ACPI and then after a timeout). `status` answers `{"state": "ready" | "preparing" | "failed", "detail": "..."}`. `resume` is called once, after the restart, when the unit is back and the module has checked what it depends on.

## What the upgrade does today

**It does not make these calls yet.** The readiness report has a check, `modules`, that refuses the upgrade of a machine with any module installed, and names the module:

- `invalid`: the manifest cannot be read or does not pass (the file is named, never its content);
- `no_os_upgrade`: the module has not said what it needs around an upgrade;
- `calls_not_run_yet`: the module has said it, and the upgrade does not make the calls, so a machine whose virtual machines are running is not upgraded under them.

The change that makes the calls (prepare, wait for ready, stop the units, resume after the restart) sets one constant, and the check then accepts a module that has declared them. Until then the answer to a module's author is: declare `os_upgrade` as above, and expect a refusal that names your module.

A machine that has `libvirt-daemon` installed is refused by the check of server software with data of its own (`host_services`), module or not.
