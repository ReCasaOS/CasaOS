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
| `unit` | a systemd service: `[A-Za-z0-9][A-Za-z0-9:_.@-]{0,63}.service`. |
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

The plan asks the box, and changes nothing. Its checks, in this order, are `catalogue`, `os`, `arch`, `recasaos`, `already_installed`, `foreign_unit`, `busy`, `disk` and `apt`; `recasaos` compares the number of the DISTRIBUTION (the tag that the installer writes to `/var/lib/casaos/fork-release`, `v0.5.29`) with `min_recasaos`, and not the version of the core. A check that was not made because an earlier one had already failed says `not_checked` and is not the reason of the refusal. A plan asked for by a user who is not root cannot look at the locks of dpkg and apt: `busy` then says `not_checked: needs root, the install checks it as root`, is not ok and is not the reason of a refusal either, and the rest of the plan is made (the units that update the machine, the flag of the upgrade of the operating system, the room, apt); A release of the distribution that is not known (no marker in `/var/lib/casaos/fork-release`, or one that is not a release) makes `recasaos` say `unknown`, and the module is refused. A plan is refused with the code of the first check that failed, except for these two checks, which say more:

| Refusal | Meaning |
|---|---|
| `disk_short`, `disk_unknown` | there is not room on `/var/lib/casaos/modules/tmp` or on `/usr`, or the room or the sizes cannot be told. |
| `apt_missing` | `apt-get` is not there. |
| `apt_failed` | apt gave no answer, or an answer that was not understood. When packages cannot be installed together (a hold, a conflict) the detail carries the lines of apt's `The following packages have unmet dependencies` block. |
| `apt_removes`, `apt_downgrades` | installing the packages would remove or downgrade a package. |
| `held_package` | a package that the install would change is on hold. |
| `package_not_exact` | apt would install something other than the packages that were asked for. apt reads a name with a `.` or a `+` in it as a regular expression when no package has exactly that name (`mod.lean` selects `modclean`), and a name that a package provides as that package; every package of the manifest must come back in the simulation under its own name, so a virtual package is refused too: a manifest names real packages. |

The simulation is `apt-get -s install --no-install-recommends <packages>` and the sizes come from `apt-get --print-uris install --no-install-recommends <packages>`, both in a sandbox and with the log of the planner turned off. The second says what apt would download and use, prints the addresses and ends, installing and downloading nothing. (`-o APT::Get::Trivial-Only=true` is not used for this: apt goes on with a transaction that has as many packages as names, which installs it.) The sandbox keeps the settings of the box that decide what is installed, among them `APT::Default-Release`.

## Installing and removing from the dashboard: the routes of the core and the job

The core runs the module assistant (`casaos module ...`) in its own process for what only reads, and as a transient unit for what changes the box. The routes are below `/v1/sys`, behind the token of the group like the others.

| Route | Does | Token |
|---|---|---|
| `GET /v1/sys/modules` | the list: the catalogue of this release joined with what is installed (`catalogue` with `ok` and, when it is not, `error`: `none`, `unreadable` or `invalid`, with `detail`; `modules`), as `casaos module list` prints it. A catalogue that is not there or does not pass is an empty list that says so, never a partial one. | the group's |
| `POST /v1/sys/modules/{name}/plan` | the plan of an install, made in the core, for at most 60 seconds, one plan at a time: the object of `casaos module plan` (checks, `refused`, packages, archive, `plan_id`). A plan that is refused is still a plan: 200, with `refused` set. | the access token in the header |
| `POST /v1/sys/modules/{name}/install` | starts the unit of the install for the plan the user read. Body `{"plan_id": "m1-..."}`. 202 with the status. | the access token in the header |
| `POST /v1/sys/modules/{name}/remove` | starts the unit of the remove. Body `{}`. 202 with the status. | the access token in the header |
| `GET /v1/sys/modules/{name}/status` | `{"job": ..., "status": ...}`: how the job of the module is going, and the object of `casaos module status`. | the group's |

What starts a unit or runs apt takes the access token in the `Authorization` header and no other: not the one in `?token=`, not the refresh token (401). `{name}` is checked with the rule of the name of a module before anything else, and a name that does not pass is a 400. The bodies are strict JSON, as those of the upgrade of the operating system are: one object, no field that the request does not have, no key twice, no key spelled otherwise, at most 64 KiB; what is wrong is a 400 `bad_request` that names the field (`name`, `plan_id` or `body`) and never repeats what was sent. Every answer is `{"success", "message", "data"}` and is not to be kept (`Cache-Control: no-store`).

A request that is refused is a 409 with the code in `data.error_code`, the English reason in `data.error` and what makes the code precise in `data.data`; `bad_request` is a 400 and `start_failed` a 500.

| Code | Meaning |
|---|---|
| `bad_request` | the name, the body or a field is not valid (`data.data.field`). |
| `not_root` | the core does not run as root. |
| `unsupported` | the host is not a Debian-family system with `apt-get` and `systemd-run`. |
| `busy` | something else is at work, `data.data.reason` says what: a job of a module (its log says it has not ended and it was seen alive less than 30 seconds ago, or its unit runs), a package update, an update of Docker, the update of ReCasaOS, the upgrade of the operating system (its unit, or its flag, which is read so that a flag that cannot be read is an open one), a package manager that holds dpkg's lock or a lock that cannot be looked at, or a run of the assistant by hand that holds its lock. Nothing is written when it is said. |
| `plan_changed` | the plan the box makes now is not the one with that `plan_id` (the archive, the tag, the packages or the system changed): read the new plan and confirm again. |
| `plan_timeout` | the plan could not be made in its time. |
| `not_installed` | a remove of a module of which nothing is on the box (no manifest, no list of files, no version). A module that an install left half done is something to remove. |
| `start_failed` | the log could not be written or systemd did not start the unit. Nothing was changed, and the log of the module says so (`failed`, code `start_failed`). |
| the codes of the plan | an install is refused with the code of the first check of its plan that fails: `catalogue`, `os`, `arch`, `recasaos`, `already_installed`, `foreign_unit`, `busy`, `disk_short`, `disk_unknown`, `apt_missing`, `apt_failed`, `apt_removes`, `apt_downgrades`, `held_package`, `package_not_exact`; `data.data.checks` is the list of the checks. |

The jobs make the box busy for what changes it: `casaos-module-*.service` is among the units that the packages window, the update of Docker, the update of ReCasaOS, the automatic updates, the backups and the upgrade of the operating system ask about before they start, and a module job is not started while any of them runs.

### The unit

An install is `systemd-run --quiet --no-block --collect --unit=casaos-module-<name>.service --property=Type=exec --setenv=CASAOS_MODULE_NONCE=<32 hex digits> -- /usr/bin/casaos module install <name> --yes --plan-id <id>`, and a remove is the same with `module remove <name> --yes`. The command line is closed: the name is a name of a module, the id is `m1-` and 32 hexadecimal digits, the nonce is 32 hexadecimal digits from the system's random source, and no word holds a dollar sign or a percent sign. The unit sets nothing else, `CASAOS_MODULE_TEST` never.

The core writes the first line of the log of the module before it starts the unit, holding the lock of the assistant (a log that a run is writing is not touched, and a run started by hand meanwhile is refused as busy):

```
CASAOS_MODULE_QUEUED <nonce> <name> <time>
```

The unit replaces the log with its own lines (see the grammar in `pkg/modulecmd/job.go`), which carry the same nonce. When systemd would not start the unit, the core closes the log itself: the queued line, `failed: start_failed`, what systemd said (as text, behind `| `), and `CASAOS_MODULE_RESULT <nonce> <name> failed - start_failed - <time>`. The core keeps nothing about a job in memory and writes no file but this log: the state is read from the log and from the unit each time, so a restart of the core in the middle of a job, or after it, changes nothing, and the core never starts a job again by itself.

### The state of a job

`data.job` of the status, and of the answer of an install and of a remove:

| Field | |
|---|---|
| `name` | the module. |
| `state` | `idle`, `running`, `succeeded`, `refused`, `failed` or `interrupted` (below). |
| `step` | where the job is: the step of the last `CASAOS_MODULE_STEP` line. An install goes `packages`, `download`, `verify`, `unpack`, `units`, `manifest`, `start`, `route`, `done`; a remove `stop`, `files`, `manifest`, `reload`, `done`; the rollback of an install that failed runs the steps of a remove behind `rollback_` (`rollback_stop`, ...). For a job that failed it is the step of the result (`""` when it failed before its first step); for one that succeeded it is `done`; for one that was refused or has not begun it is `""`. |
| `code` | why the job was refused or failed: the code of the result line (the codes of the plan, `plan_changed`, `busy`, `not_installed`, `checksum_mismatch`, `download_failed`, `download_too_large`, `packages_failed`, `held_package`, `unpack_path`, `file_exists`, `units_failed`, `manifest_invalid`, `start_failed`, `route_timeout`, `interrupted`, `internal_error`, ...); `""` otherwise. |
| `undo` | `rolled_back` or `rollback_incomplete` for an install that failed after it had changed the box (`casaos module remove` finishes an incomplete rollback), `""` otherwise. |
| `kept_packages` | the packages of the module that an install which failed leaves on the box, from the sentence `kept_packages:` that comes before the result: a list (empty when none), or `null` when it is not said or not known. Packages are never removed. |
| `started_at`, `ended_at` | the times of the first line and of the result line, RFC 3339 in UTC; `""` when there is none. |
| `log_tail` | the last 20 lines of the log, as text: printable ASCII, a URL reduced to its host, at most 200 characters. They come from tools and from the module; the page shows them as text. |

| State | When |
|---|---|
| `idle` | no job: the log has no line of a job of this module, and no unit runs. A log of a run by hand (nonce `-`) is not a job. |
| `running` | no result line, and the unit is active, or it was gone for less than 30 seconds since the last sign of life of the job (a marker, or the time the log was written), or systemd does not answer and the last sign of life is less than 30 minutes old. A time of the log more than 30 seconds in the future is not a sign of life. |
| `succeeded` | a `CASAOS_MODULE_RESULT ... ok done - - ...` line with the nonce of the job. |
| `refused` | a result line `refused`: nothing was changed; `code` says why. |
| `failed` | a result line `failed`: `step`, `code`, `undo`, `kept_packages`. |
| `interrupted` | no result line, and the unit is gone for more than 30 seconds (it was ended in the middle: killed, the machine was restarted): `step` is where it was. Nothing was undone: `casaos module remove` finishes the work. A job whose unit never came (and a unit that was refused before it could write: another run of the assistant held its lock) is `interrupted` with `step` `""`. |

A line is the word of the job only if it is a whole line (ended by a new line) of exactly the shape of a marker, its time in UTC to the second, with the nonce of the job and the name of the module, that does not begin with the `| ` of the output of a tool; the fields of a result line must agree with its verdict. The first result line is final: nothing after it changes the job, and a job that finished stays what it was for as long as its log is there (the next job of that module replaces the log). The nonce of the job is the one the core wrote; when it has to be found again after a restart it is the first marker of the module in the log that has one, and a marker with the nonce `-` never counts. A log is read whole up to 8 MiB, and beyond that by its first and last 4 MiB; of a line the first 4096 bytes are held.

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
