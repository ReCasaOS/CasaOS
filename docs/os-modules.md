# Modules of ReCasaOS: the manifest, and what the upgrade of the operating system does with a module

A module is a component that is installed on its own: a service of its own, packages of its own, a release of its own. The core shows it only when it is installed and answers through the gateway. This page says what the core reads of a module, how far it trusts it, and what the upgrade of the operating system does about it today.

## The manifest

Each module ships `module.json` in its release archive, and the installer copies it to `/etc/casaos/modules/<name>.json`. The installer reads it to install; the core reads it too, to know that the module is there and what it needs around an upgrade.

```json
{
  "schema": 1,
  "name": "virt",
  "title": "Virtual machines",
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
| `title` | 1 to 64 characters, no control character. |
| `component` | `[A-Za-z][A-Za-z0-9._-]{0,63}`. |
| `unit` | a systemd service: `[A-Za-z0-9][A-Za-z0-9:_.@-]{0,100}.service`. |
| `gateway_route` | `/v<digits>/<name>`, lower case, digits and underscores. |
| `packages` | `common`, `amd64`, `arm64`; at most 32 each; Debian package names, none twice. |
| `min_recasaos` | three numbers: `0.5.23`. |
| `os` | at least one, at most 16: `debian:12`, `ubuntu:24.04`. |
| `os_upgrade` | optional; see below. |

The file is data, and the core trusts it only as far as it validates it: every value is checked against a fixed pattern, a field that is not in the table is an error, a key that comes twice is an error, the file has 64 KiB at most and is a regular file (a link is refused), and nothing in it is ever run as a command, put in a shell or opened as a path. A manifest that does not pass is a module that cannot be told, and the report says so.

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
