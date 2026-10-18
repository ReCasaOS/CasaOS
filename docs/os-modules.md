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

The install keeps two lists in `/var/lib/casaos/modules/`: `<name>.files`, the files and links it put down, one absolute path to a line, the unit last; and `<name>.dirs`, the directories it had to make because the system had none (the allowed directory and the ones above it, when the system lacks them). Both are written, whole, before the first directory is made and the first file put in place, so that a process that is killed half way leaves lists that name everything that may be there. A remove deletes exactly the files of the first and then the directories of the second that are empty, the deepest first; a rollback of an install that stopped deletes what the unpack says it wrote, and not what it was going to write.

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
