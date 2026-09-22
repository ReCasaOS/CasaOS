# Anonymous Statistics — Installer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The installer (ReCasaOS/CasaOS-Install) names the release each run replaces, turns anonymous statistics off on request (`--no-telemetry`, `RECASAOS_TELEMETRY=0`), says at the end of every run whether they are on and how to turn them off, the README documents them, and the install check proves the whole chain without ever reaching PostHog.

**Architecture:** Three small functions in `install.sh`: `Previous_Release` and `Write_Telemetry_Markers` run between stopping the services and copying the release tree onto `/` and write the markers the core reads (`upgraded-from` always, `telemetry-off` on request), and `Telemetry_Notice` closes every run. `scripts/test-install-telemetry.sh` copies those functions out of `install.sh`, checks them against a temporary state directory and checks the lines of `install.sh` that call them; `release.yml` runs it on Linux before building. `install-check.yml` installs with `--no-telemetry`, checks the off state, then points the core at a Python capture server on the runner, with `eu.i.posthog.com` resolving to `127.0.0.1`, before turning statistics on and checks the two events it receives.

**Tech Stack:** bash 5 (`install.sh`, `set -e`, `getopts`), GitHub Actions (`release.yml`, `install-check.yml`), systemd drop-ins, Python 3 stdlib `http.server`, jq 1.6 (runner), Markdown.

**Spec:** D:/clients/casaos/CasaOS/docs/superpowers/specs/2026-09-22-anonymous-stats-design.md

## Global Constraints

- Repository: `D:/clients/casaos/get-digest` (ReCasaOS/CasaOS-Install, remote `inkly`). Branch `feat/telemetry` from `inkly/main` (`1e29866`, v0.5.0). Line numbers below are at `1e29866`. Anchor each edit on the quoted text.
- State files: `/var/lib/casaos/telemetry.json` (root 0600; `{"enabled": bool, "id": string, "last_sent": RFC 3339 string or absent, "notice_seen": bool}`), `/var/lib/casaos/telemetry-off` (marker written by the installer), `/var/lib/casaos/upgraded-from` (written by the installer: previous tag, `upstream` or `new`), `/var/lib/casaos/fork-release` (existing distribution marker, shipped in the release overlay).
- `--no-telemetry`, or the environment variable `RECASAOS_TELEMETRY=0`, creates `/var/lib/casaos/telemetry-off` (root, 0600) before the services start.
- `upgraded-from` (root, 0600) is written on every run, statistics on or off, before the release overlay replaces `fork-release`. The run logs it as `Previous release: <value>`.
- An upgrade never turns statistics back on. Without the flag, an existing choice is left as it is.
- A closing notice block at the end of every run, in English, linking `https://github.com/ReCasaOS/CasaOS-Install#anonymous-statistics`.
- README section title: `## Anonymous statistics` (anchor `#anonymous-statistics`).
- Core API: `GET /v1/sys/telemetry` → `data = {"enabled": bool, "notice_seen": bool, "preview": {"event": "heartbeat", "properties": {...}}}`. `PUT /v1/sys/telemetry` with body `{"enabled"?: bool, "notice_seen"?: bool}` → `data` = the same object, after the change. Both need the admin JWT (`Authorization: <token>`). Both answer the envelope `{success, message, data}`.
- Core env vars, for tests only: `CASAOS_TELEMETRY_ENDPOINT` (capture URL), `CASAOS_TELEMETRY_START_DELAY` (Go duration, `0s`).
- Events: `heartbeat` and `version_changed`. Body: `{"api_key","event","distinct_id","timestamp","properties"}`. Properties include `"$process_person_profile": false` and `"$lib": "recasaos-core"`, and never `$geoip_disable`.
- PostHog endpoint `https://eu.i.posthog.com/i/v0/e/`, key `phc_C5T8QbrhBttzgkWuKm2Xr9S4TLGh3sCVAZai2oaFEuhD`.
- No CI step may run the core with statistics on and the default endpoint.
- Ship order: core and dashboard are released first, with their own tags. The installer pins them in one distribution release, and that release's install check proves the whole chain. Tasks 4 and 5 are proven there, not before.
- Not in this plan (the controller writes them at release time): the `CHANGELOG.md` entry, the README "What is in" entry and Components table, and the `release/components.env` pins.
- The working tree is CRLF (`core.autocrlf=true`, LF in the index). Keep each file's line endings as they are. The self-check strips `\r` itself.
- No `__UPPER_CASE__` token may appear in `install.sh`: `fill_installer` refuses an installer with a placeholder left.
- Local runs: `bash scripts/test-install-telemetry.sh` in Git Bash. File modes and `install.sh`'s option parsing are checked on Linux only; `release.yml` runs them in CI. `install-check.yml` runs only in CI, against a published release. Docker, WSL and jq are not available locally; `python` is `C:/Python312/python`, and `python3` on the PATH is the Store stub.
- Inline Bash commands lose their backslashes in this harness. Any file with a backslash in it is written with the Write tool.
- Commits: `git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "..."`, after `git add <explicit paths>`. Never a `Co-Authored-By` trailer or any AI attribution.
- Forbidden: `git push`, `merge`, `rebase`, `tag`, `reset --hard`, `checkout --`, `clean`, `--no-verify`, and `rm -rf` on anything inside the repository.

---

## File structure

| File | Action | Responsibility |
|---|---|---|
| `install.sh` | Modify | `CASA_STATE_DIR`, `NO_TELEMETRY` from `RECASAOS_TELEMETRY` and `--no-telemetry`, `usage()`, `Previous_Release`, `Write_Telemetry_Markers` (called with the services stopped, before the copy onto `/`), `Telemetry_Notice` (last thing a run prints). |
| `scripts/test-install-telemetry.sh` | Create | Self-check: copies the three functions out of `install.sh`, runs them against a temporary `/var/lib/casaos`, checks from the line numbers that `Write_Telemetry_Markers` is called between the stop loop and the copy onto `/` and that `Telemetry_Notice` is the last line, and on Linux checks modes and the option parsing. |
| `.github/workflows/release.yml` | Modify | Runs the self-check before the bundle is assembled. |
| `.github/workflows/install-check.yml` | Modify | The header says how to check a tag older than the flag. Installs with `--no-telemetry`. One step checks the off state, the log line and the notice. A last step checks the capture: `/etc/hosts` safety net, drop-in, guard, `PUT`, `upgraded-from`, restart, both events. The artifact keeps `capture.jsonl`. |
| `README.md` | Modify | The permanent `## Anonymous statistics` section, between `## Install` and the first "What is in" entry. |
| `C:/Users/garyd/AppData/Local/Temp/claude/D--clients-StagevetV2/89b632dd-c1dd-4e14-bcc1-a7dd364b6926/scratchpad/stats-plans/workflow-check.py` | Create (outside the repo) | Local check of `install-check.yml`: writes each `run:` block out for `bash -n`, and runs the capture server as the workflow writes it. |

Where the overlay lands: the downloaded tarballs, the overlay included, are extracted into the temporary build tree (`install.sh` lines 845-850); nothing on the box changes there. The overlay's `var/lib/casaos/fork-release` lands on the box with `cp -rf "${SYSROOT_DIR}"/* /` (line 891). The services are stopped at lines 857-865 and started at lines 926-939. `Write_Telemetry_Markers` runs right after the stop loop, so it runs with the services stopped, before the copy and before any start; the self-check holds that order from the line numbers (Task 1), and holds `Telemetry_Notice` as the last line (Task 3). With `-p <build_dir>`, the download block is skipped and this path is the same. The in-app update re-runs the installer detached (`Detach_From_CasaOS_Service`, line 1025) and goes through the same function.

---

### Task 1: `upgraded-from` and the `Previous release` line

**Files:**
- Create: `scripts/test-install-telemetry.sh`
- Modify: `install.sh` (after line 77; before line 798 `# Download And Install CasaOS`; after line 869 `${sudo_cmd} rm -f /tmp/message-bus.sock`)
- Modify: `.github/workflows/release.yml` (after the `Checkout installer` step, lines 33-34)
- Test: `scripts/test-install-telemetry.sh`

**Interfaces:**
- Consumes: `Show <0|1|2|3> <message>` and `sudo_cmd` (install.sh). The stop loop and the `cp -rf "${SYSROOT_DIR}"/* /` in `DownloadAndInstallCasaOS`. The overlay's `build/sysroot/var/lib/casaos/fork-release` (`package_overlay` in `scripts/build-release-bundle.sh`, one line: the tag, then a newline). The core plan: reads `upgraded-from` with surrounding whitespace trimmed, as it does `fork-release`. It deletes the file after a successful `version_changed`, or unsent when statistics are off.
- Produces:
  - `readonly CASA_STATE_DIR=/var/lib/casaos` (install.sh global).
  - `Previous_Release()`: no arguments, builtins only. Prints one line: the content of `${CASA_STATE_DIR}/fork-release` with all whitespace removed, when that is not empty. Otherwise `upstream` when `command -v casaos` succeeds, and `new` when it does not.
  - `Write_Telemetry_Markers()`: no arguments. Calls `Show 2 "Previous release: <value>"`, which is `[ INFO ] Previous release: <value>` followed by two spaces in a non-TTY log. Creates `${CASA_STATE_DIR}` when missing. Writes `${CASA_STATE_DIR}/upgraded-from` = `<value>\n`, root 0600, replacing any previous file. Ends the run through `Show 1` if a write fails.
  - `scripts/test-install-telemetry.sh` with `INSTALL_SH` (the CR-stripped copy of `install.sh` every check reads), `FUNCTIONS` (the array of function names it copies out), `fail <message>`, `line_of <grep options> <text>` (the number of the one matching line of `INSTALL_SH`; fails on none or several), `reset <case>` (sets `CASA_STATE_DIR` and `BIN_DIR`), `fake_casaos`, `expect_mode <file> <mode>`.
  - The call line `    Write_Telemetry_Markers` (four spaces, alone on its line), below the line holding `systemctl stop "${SERVICE}"` and above the line holding `cp -rf "${SYSROOT_DIR}"/* /`.
  - release.yml step `Check the installer's statistics functions`.

- [ ] **Step 1: Branch**

```bash
cd D:/clients/casaos/get-digest && git status --short && git fetch inkly && git switch -c feat/telemetry inkly/main
```

Expected: an empty `git status --short` (stop and report if it is not), then `Switched to a new branch 'feat/telemetry'`.

- [ ] **Step 2: Write the failing test**

Create `scripts/test-install-telemetry.sh` with the Write tool:

```bash
#!/usr/bin/env bash
#
# Checks the anonymous-statistics parts of install.sh without installing
# anything: each function is copied out of install.sh as it stands and run
# against a temporary directory standing in for /var/lib/casaos, and the lines
# that call them are checked to be where the install needs them.
#
#   bash scripts/test-install-telemetry.sh
#
# Some checks need Linux and are skipped elsewhere: file modes (a Windows
# checkout holds none), and install.sh's own startup (it reads
# /etc/os-release). The release workflow runs this on Linux before anything
# is built.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

[[ "$(uname -s)" == Linux ]] || echo "note: not Linux, the Linux-only checks are skipped"

# install.sh as bash reads it: a Windows checkout has CRLF line endings.
INSTALL_SH="${WORK}/install.sh"
tr -d '\r' <"${ROOT}/install.sh" >"${INSTALL_SH}"
bash -n "${INSTALL_SH}" || fail "install.sh does not parse"

# The functions under test, as install.sh defines them.
FUNCTIONS=(Previous_Release Write_Telemetry_Markers)
for f in "${FUNCTIONS[@]}"; do
    eval "$(sed -n "/^${f}() {\$/,/^}\$/p" "${INSTALL_SH}")"
    declare -F "${f}" >/dev/null || fail "install.sh defines no ${f}()"
done

# line_of <grep options> <text>: the number of the one line of install.sh that
# matches; fails when there is none, or more than one.
line_of() {
    local found
    found="$(grep -n "$@" "${INSTALL_SH}" | cut -d: -f1)" || true
    [[ -n "${found}" && "${found}" != *$'\n'* ]] ||
        fail "install.sh has no single line matching '${@: -1}' (lines: ${found:-none})"
    echo "${found}"
}

# What those functions take from install.sh's globals.
sudo_cmd=""
Show() { echo "$2"; }

# reset <case>: a state directory that does not exist yet, and an empty
# directory to put first on PATH, for a fake casaos binary
reset() {
    CASA_STATE_DIR="${WORK}/$1/var/lib/casaos"
    BIN_DIR="${WORK}/$1/bin"
    mkdir -p "${BIN_DIR}"
}

fake_casaos() {
    printf '#!/bin/sh\necho v0.4.2\n' >"${BIN_DIR}/casaos"
    chmod +x "${BIN_DIR}/casaos"
}

# expect_mode <file> <mode>
expect_mode() {
    [[ "$(uname -s)" == Linux ]] || return 0
    local mode
    mode="$(stat -c '%a' "$1")"
    [[ "${mode}" == "$2" ]] || fail "$1 is ${mode}, expected $2"
}

# Previous_Release: the tag in fork-release, else upstream when a casaos binary
# is on PATH, else new. PATH holds BIN_DIR alone: it uses builtins only.
reset tag
mkdir -p "${CASA_STATE_DIR}"
printf 'v0.4.99\r\n' >"${CASA_STATE_DIR}/fork-release"
fake_casaos
got="$(PATH="${BIN_DIR}" Previous_Release)"
[[ "${got}" == v0.4.99 ]] || fail "fork-release v0.4.99 gave '${got}'"

reset upstream
fake_casaos
got="$(PATH="${BIN_DIR}" Previous_Release)"
[[ "${got}" == upstream ]] || fail "a casaos binary without fork-release gave '${got}'"

reset empty-marker
mkdir -p "${CASA_STATE_DIR}"
: >"${CASA_STATE_DIR}/fork-release"
fake_casaos
got="$(PATH="${BIN_DIR}" Previous_Release)"
[[ "${got}" == upstream ]] || fail "an empty fork-release beside a casaos binary gave '${got}'"

reset new
got="$(PATH="${BIN_DIR}" Previous_Release)"
[[ "${got}" == new ]] || fail "nothing installed gave '${got}'"
echo "ok: Previous_Release names the tag, upstream or new"

# Write_Telemetry_Markers: upgraded-from on every run, root's alone, and the
# line the install check looks for.
reset first-install
fake_casaos
out="$(PATH="${BIN_DIR}:${PATH}" Write_Telemetry_Markers)"
[[ "${out}" == *"Previous release: upstream"* ]] || fail "no 'Previous release: upstream' line in: ${out}"
[[ "$(cat "${CASA_STATE_DIR}/upgraded-from")" == upstream ]] || fail "upgraded-from is not 'upstream'"
expect_mode "${CASA_STATE_DIR}/upgraded-from" 600

reset rerun
mkdir -p "${CASA_STATE_DIR}"
printf 'v0.5.0\n' >"${CASA_STATE_DIR}/fork-release"
printf 'stale\n' >"${CASA_STATE_DIR}/upgraded-from"
chmod 644 "${CASA_STATE_DIR}/upgraded-from"
out="$(Write_Telemetry_Markers)"
[[ "${out}" == *"Previous release: v0.5.0"* ]] || fail "no 'Previous release: v0.5.0' line in: ${out}"
[[ "$(cat "${CASA_STATE_DIR}/upgraded-from")" == v0.5.0 ]] || fail "upgraded-from was not rewritten"
expect_mode "${CASA_STATE_DIR}/upgraded-from" 600
echo "ok: Write_Telemetry_Markers writes upgraded-from, 600, and logs it"

# Where install.sh calls it: after the loop that stops the services, before the
# release tree, the overlay's fork-release with it, is copied onto / (the loop
# that starts the services comes after that copy).
stop="$(line_of -F 'systemctl stop "${SERVICE}"')"
call="$(line_of -xF '    Write_Telemetry_Markers')"
copy="$(line_of -F 'cp -rf "${SYSROOT_DIR}"/* /')"
((stop < call && call < copy)) ||
    fail "Write_Telemetry_Markers is called at line ${call}, not between the services' stop (line ${stop}) and the copy onto / (line ${copy})"
echo "ok: install.sh writes the markers with the services stopped, before the copy onto /"
```

- [ ] **Step 3: Run it and see it fail**

```bash
cd D:/clients/casaos/get-digest && bash scripts/test-install-telemetry.sh
```

Expected: exit 1, with
```
note: not Linux, the Linux-only checks are skipped
FAIL: install.sh defines no Previous_Release()
```

- [ ] **Step 4: Implement in `install.sh`**

(a) Below `readonly CASA_UNINSTALL_PATH=/usr/bin/casaos-uninstall` (line 77), add:

```bash
# The core's state: the overlay's fork-release marker, and the markers of the
# anonymous statistics (see Write_Telemetry_Markers).
readonly CASA_STATE_DIR=/var/lib/casaos
```

(b) Directly above `# Download And Install CasaOS` (line 798), add:

```bash
# What this run replaces, for the core's version_changed statistic: the tag the
# previous ReCasaOS release left in fork-release, "upstream" when a casaos
# binary is there without one (a box coming from IceWhale's CasaOS), "new"
# otherwise. Builtins only.
Previous_Release() {
    local previous=""

    if [[ -f "${CASA_STATE_DIR}/fork-release" ]]; then
        read -r previous <"${CASA_STATE_DIR}/fork-release" || true
        previous="${previous//[[:space:]]/}"
    fi

    if [[ -n "${previous}" ]]; then
        echo "${previous}"
    elif command -v casaos >/dev/null 2>&1; then
        echo upstream
    else
        echo new
    fi
}

# The markers of the anonymous statistics (README.md, "Anonymous statistics"),
# written with the services stopped and before the release overlay replaces
# fork-release. upgraded-from, on every run, names what this run replaces: the
# core sends it once as version_changed, or deletes it unsent when statistics
# are off. The dashboard's update button runs this same path.
Write_Telemetry_Markers() {
    local previous
    previous="$(Previous_Release)"
    Show 2 "Previous release: ${previous}"

    ${sudo_cmd} mkdir -p "${CASA_STATE_DIR}" || Show 1 "Failed to create ${CASA_STATE_DIR}"
    ${sudo_cmd} install -m 0600 /dev/null "${CASA_STATE_DIR}/upgraded-from" || Show 1 "Failed to write ${CASA_STATE_DIR}/upgraded-from"
    printf '%s\n' "${previous}" | ${sudo_cmd} tee "${CASA_STATE_DIR}/upgraded-from" >/dev/null || Show 1 "Failed to write ${CASA_STATE_DIR}/upgraded-from"
}
```

(c) In `DownloadAndInstallCasaOS`, replace

```bash
    ${sudo_cmd} rm -f /tmp/message-bus.sock

    MIGRATION_SCRIPT_DIR=
```

with

```bash
    ${sudo_cmd} rm -f /tmp/message-bus.sock

    Write_Telemetry_Markers

    MIGRATION_SCRIPT_DIR=
```

(only the three lines shown change; the rest of the `MIGRATION_SCRIPT_DIR=` line stays).

- [ ] **Step 5: Run it and see it pass**

```bash
cd D:/clients/casaos/get-digest && bash scripts/test-install-telemetry.sh
```

Expected: exit 0, with
```
note: not Linux, the Linux-only checks are skipped
ok: Previous_Release names the tag, upstream or new
ok: Write_Telemetry_Markers writes upgraded-from, 600, and logs it
ok: install.sh writes the markers with the services stopped, before the copy onto /
```

Check that the order check can fail: in a scratch copy, move the call below the copy and run the check against it.

```bash
cd D:/clients/casaos/get-digest && S=C:/Users/garyd/AppData/Local/Temp/claude/D--clients-StagevetV2/89b632dd-c1dd-4e14-bcc1-a7dd364b6926/scratchpad/stats-plans/misplaced && mkdir -p "$S/scripts" && cp scripts/test-install-telemetry.sh "$S/scripts/" && awk '$0 ~ /^    Write_Telemetry_Markers.?$/ { next } { print } index($0, "cp -rf") && index($0, "SYSROOT_DIR") { print "    Write_Telemetry_Markers" }' install.sh > "$S/install.sh" && bash "$S/scripts/test-install-telemetry.sh"; echo "exit $?"
```

Expected: the note, the two `ok:` lines of the functions, then `FAIL: Write_Telemetry_Markers is called at line N, not between the services' stop (line S) and the copy onto / (line C)`, where N is C + 1 (the numbers themselves depend on the lines added above), and `exit 1`. The command has no backslash, so it survives the harness. The scratch copy is outside the repository; nothing in the repository changes.

- [ ] **Step 6: Run the self-check in the release workflow**

In `.github/workflows/release.yml`, replace

```yaml
      - name: Checkout installer
        uses: actions/checkout@v4
```

with

```yaml
      - name: Checkout installer
        uses: actions/checkout@v4

      - name: Check the installer's statistics functions
        run: bash scripts/test-install-telemetry.sh
```

Check that it parses: `cd D:/clients/casaos/get-digest && npx -y js-yaml .github/workflows/release.yml > /dev/null && echo release-yaml-ok`. Expected: `release-yaml-ok`.

- [ ] **Step 7: Commit**

```bash
cd D:/clients/casaos/get-digest && git add install.sh scripts/test-install-telemetry.sh .github/workflows/release.yml && git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(install): name the release an install replaces, for the statistics" -m "Before the release tree is copied onto the box, the installer writes /var/lib/casaos/upgraded-from (root, 0600): the tag in fork-release, upstream when a casaos binary is there without one, new otherwise, and logs it as 'Previous release: <value>'. scripts/test-install-telemetry.sh checks it, and checks from the line numbers that the call sits between the stop loop and the copy onto /; the release workflow runs that on Linux first."
```

---

### Task 2: `--no-telemetry` and `RECASAOS_TELEMETRY=0`

**Files:**
- Modify: `install.sh` (after line 166 `CASA_DOWNLOAD_DOMAIN=...`; the end of `Write_Telemetry_Markers` from Task 1; `usage()` and the `getopts` loop, lines 998-1021)
- Modify: `scripts/test-install-telemetry.sh`
- Test: `scripts/test-install-telemetry.sh`

**Interfaces:**
- Consumes: `Write_Telemetry_Markers`, `CASA_STATE_DIR`, and the self-check helpers from Task 1. The core plan: at startup, before anything else of the feature runs, it sets `"enabled": false` in `telemetry.json` (keeping `id`) and deletes `telemetry-off` after that write.
- Produces:
  - `NO_TELEMETRY`, an install.sh global: `1` when `RECASAOS_TELEMETRY` is exactly `0` or `--no-telemetry` is given, `0` otherwise. It is set before `Detach_From_CasaOS_Service` and before any service is touched.
  - Option `--no-telemetry`. Any other long option ends the run with `usage 1`.
  - When `NO_TELEMETRY=1`, `Write_Telemetry_Markers` also writes `${CASA_STATE_DIR}/telemetry-off` (empty, root 0600) and logs `Anonymous statistics turned off.`. When it is 0, `telemetry-off` and `telemetry.json` are left untouched.
  - `usage()` lists `--no-telemetry` and the README link.

- [ ] **Step 1: Write the failing test**

In `scripts/test-install-telemetry.sh`, replace

```bash
# What those functions take from install.sh's globals.
sudo_cmd=""
Show() { echo "$2"; }
```

with

```bash
# What those functions take from install.sh's globals.
sudo_cmd=""
NO_TELEMETRY=0
Show() { echo "$2"; }
```

and replace the last line

```bash
echo "ok: install.sh writes the markers with the services stopped, before the copy onto /"
```

with

```bash
echo "ok: install.sh writes the markers with the services stopped, before the copy onto /"

# --no-telemetry or RECASAOS_TELEMETRY=0 (both set NO_TELEMETRY): telemetry-off,
# root's alone. Without either, an earlier choice is left exactly as it is.
reset turned-off
out="$(NO_TELEMETRY=1 Write_Telemetry_Markers)"
[[ -e "${CASA_STATE_DIR}/telemetry-off" ]] || fail "NO_TELEMETRY=1 wrote no telemetry-off"
expect_mode "${CASA_STATE_DIR}/telemetry-off" 600
[[ "${out}" == *"Anonymous statistics turned off."* ]] || fail "NO_TELEMETRY=1 did not say so: ${out}"

reset kept
mkdir -p "${CASA_STATE_DIR}"
: >"${CASA_STATE_DIR}/telemetry-off"
printf '{"enabled":false,"id":"kept","notice_seen":true}\n' >"${CASA_STATE_DIR}/telemetry.json"
Write_Telemetry_Markers >/dev/null
[[ -e "${CASA_STATE_DIR}/telemetry-off" ]] || fail "a run without the flag removed telemetry-off"
[[ "$(cat "${CASA_STATE_DIR}/telemetry.json")" == '{"enabled":false,"id":"kept","notice_seen":true}' ]] ||
    fail "a run without the flag touched telemetry.json"
echo "ok: telemetry-off only when asked, an earlier choice kept"

# The option itself, through install.sh's own parsing.
if [[ -r /etc/os-release ]]; then
    bash "${ROOT}/install.sh" -h >"${WORK}/usage.txt" 2>/dev/null || fail "install.sh -h failed"
    grep -e '--no-telemetry' "${WORK}/usage.txt" >/dev/null || fail "usage does not name --no-telemetry"
    grep -F 'https://github.com/ReCasaOS/CasaOS-Install#anonymous-statistics' "${WORK}/usage.txt" >/dev/null ||
        fail "usage does not link the README section"
    bash "${ROOT}/install.sh" --no-telemetry -h >/dev/null 2>&1 || fail "--no-telemetry is refused"
    if bash "${ROOT}/install.sh" --no-such-option >/dev/null 2>&1; then
        fail "an unknown long option is accepted"
    fi
    echo "ok: install.sh takes --no-telemetry and refuses other long options"
fi
```

- [ ] **Step 2: Run it and see it fail**

```bash
cd D:/clients/casaos/get-digest && bash scripts/test-install-telemetry.sh
```

Expected: exit 1, after the three Task 1 `ok:` lines, with `FAIL: NO_TELEMETRY=1 wrote no telemetry-off`.

- [ ] **Step 3: Implement in `install.sh`**

(a) Below `CASA_DOWNLOAD_DOMAIN="${CASA_DOWNLOAD_DOMAIN:-https://github.com/}"` (line 166), add:

```bash
# Anonymous statistics are turned off by --no-telemetry, or by
# RECASAOS_TELEMETRY=0 on the install line -- curl <install.sh> | sudo
# RECASAOS_TELEMETRY=0 bash. Without either, the owner's earlier choice stands.
NO_TELEMETRY=0
if [[ "${RECASAOS_TELEMETRY:-}" == "0" ]]; then
    NO_TELEMETRY=1
fi
```

(b) In `Write_Telemetry_Markers` (Task 1), replace its last line and closing brace

```bash
    printf '%s\n' "${previous}" | ${sudo_cmd} tee "${CASA_STATE_DIR}/upgraded-from" >/dev/null || Show 1 "Failed to write ${CASA_STATE_DIR}/upgraded-from"
}
```

with

```bash
    printf '%s\n' "${previous}" | ${sudo_cmd} tee "${CASA_STATE_DIR}/upgraded-from" >/dev/null || Show 1 "Failed to write ${CASA_STATE_DIR}/upgraded-from"

    # telemetry-off only when asked; the core folds it into telemetry.json at
    # its next start. Without the flag nothing here touches the owner's choice.
    if ((NO_TELEMETRY)); then
        ${sudo_cmd} install -m 0600 /dev/null "${CASA_STATE_DIR}/telemetry-off" || Show 1 "Failed to turn anonymous statistics off"
        Show 0 "Anonymous statistics turned off."
    fi
}
```

(c) Replace `usage()` and the `getopts` loop, lines 999-1021. In the file, the old heredoc lines are indented with tabs (`cat <<-EOF`). Copy the old text exactly as the Read tool shows it. The new text:

```bash
usage() {
    cat <<EOF
Usage: install.sh [options]
Valid options are:
    -p <build_dir>          Specify build directory (Local install)
    --no-telemetry          Turn anonymous statistics off (same as RECASAOS_TELEMETRY=0)
    -h                      Show this help message and exit

Anonymous statistics: https://github.com/ReCasaOS/CasaOS-Install#anonymous-statistics
EOF
    exit "$1"
}

# "-:" makes getopts hand over a long option: --no-telemetry arrives as "-",
# with OPTARG=no-telemetry.
while getopts ":p:h-:" arg; do
    case "$arg" in
    p)
        BUILD_DIR=$OPTARG
        ;;
    h)
        usage 0
        ;;
    -)
        case "$OPTARG" in
        no-telemetry)
            NO_TELEMETRY=1
            ;;
        *)
            usage 1
            ;;
        esac
        ;;
    *)
        usage 1
        ;;
    esac
done
```

- [ ] **Step 4: Run it and see it pass**

```bash
cd D:/clients/casaos/get-digest && bash scripts/test-install-telemetry.sh
```

Expected: exit 0, with
```
note: not Linux, the Linux-only checks are skipped
ok: Previous_Release names the tag, upstream or new
ok: Write_Telemetry_Markers writes upgraded-from, 600, and logs it
ok: install.sh writes the markers with the services stopped, before the copy onto /
ok: telemetry-off only when asked, an earlier choice kept
```
On Linux (the release workflow) the output also has `ok: install.sh takes --no-telemetry and refuses other long options`, and the modes are checked.

- [ ] **Step 5: Commit**

```bash
cd D:/clients/casaos/get-digest && git add install.sh scripts/test-install-telemetry.sh && git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(install): --no-telemetry and RECASAOS_TELEMETRY=0 turn anonymous statistics off" -m "Either writes /var/lib/casaos/telemetry-off (root, 0600) with the services stopped; the core folds it into telemetry.json at its next start. Without them the installer leaves the owner's earlier choice alone. usage() names the option and links the README section."
```

---

### Task 3: The closing notice and the README section

**Files:**
- Modify: `install.sh` (after `Welcome_Banner`, lines 975-992; after line 1066 `Welcome_Banner`)
- Modify: `README.md` (between line 22, the end of `## Install`, and line 24, `## What is in v0.5.0`)
- Modify: `scripts/test-install-telemetry.sh`
- Test: `scripts/test-install-telemetry.sh`

**Interfaces:**
- Consumes: `NO_TELEMETRY` (Task 2), `CASA_STATE_DIR` (Task 1), `GREEN_LINE`, `aCOLOUR`, `COLOUR_RESET`, `sudo_cmd` (install.sh). From the core plan: in `telemetry.json`, `enabled` is a JSON boolean. From the dashboard plan: the settings switch is labelled "Anonymous usage statistics".
- Produces:
  - `Telemetry_Notice()`: no arguments; the last thing every successful run prints, called by the last non-empty line of `install.sh`, which is exactly `Telemetry_Notice`. Its first line of substance is exactly `Anonymous statistics are on.` or `Anonymous statistics are off.`. It says off when `NO_TELEMETRY=1`, when `telemetry-off` exists, or when `telemetry.json` holds `"enabled": false` (spaces allowed around the colon). Both forms print `What they contain: https://github.com/ReCasaOS/CasaOS-Install#anonymous-statistics`.
  - README anchor `#anonymous-statistics`. The installer and the dashboard link to it.

- [ ] **Step 1: Write the failing test**

In `scripts/test-install-telemetry.sh`, replace

```bash
FUNCTIONS=(Previous_Release Write_Telemetry_Markers)
```

with

```bash
FUNCTIONS=(Previous_Release Write_Telemetry_Markers Telemetry_Notice)
```

replace

```bash
sudo_cmd=""
NO_TELEMETRY=0
Show() { echo "$2"; }
```

with

```bash
sudo_cmd=""
NO_TELEMETRY=0
GREEN_LINE="-----"
COLOUR_RESET=""
aCOLOUR=('' '' '' '' '')
Show() { echo "$2"; }
```

and replace the end of the file

```bash
    echo "ok: install.sh takes --no-telemetry and refuses other long options"
fi
```

with

```bash
    echo "ok: install.sh takes --no-telemetry and refuses other long options"
fi

# Telemetry_Notice: on unless this run turned them off, that request still
# waits for the core, or the core has them off; the README section either way.
README_URL="https://github.com/ReCasaOS/CasaOS-Install#anonymous-statistics"
expect_notice() { # <on|off> <case>
    local out
    out="$(Telemetry_Notice)"
    [[ "${out}" == *"Anonymous statistics are $1."* ]] || fail "$2: the notice does not say $1: ${out}"
    [[ "${out}" == *"${README_URL}"* ]] || fail "$2: the notice does not link ${README_URL}"
}
reset notice
expect_notice on "nothing on the box yet"
NO_TELEMETRY=1 expect_notice off "--no-telemetry"
mkdir -p "${CASA_STATE_DIR}"
: >"${CASA_STATE_DIR}/telemetry-off"
expect_notice off "telemetry-off waiting for the core"
rm "${CASA_STATE_DIR}/telemetry-off"
printf '{"enabled":false,"id":"x","notice_seen":true}\n' >"${CASA_STATE_DIR}/telemetry.json"
expect_notice off "the core has them off"
printf '{\n  "enabled": false\n}\n' >"${CASA_STATE_DIR}/telemetry.json"
expect_notice off "the core has them off, indented"
printf '{"enabled":true,"id":"x","notice_seen":false}\n' >"${CASA_STATE_DIR}/telemetry.json"
expect_notice on "the core has them on"

# And it closes the run: install.sh's last line calls it.
last="$(grep -v '^[[:space:]]*$' "${INSTALL_SH}" | tail -n 1)"
[[ "${last}" == Telemetry_Notice ]] || fail "install.sh ends with '${last}', not the Telemetry_Notice call"
echo "ok: the closing notice says on or off, links the README, and ends install.sh"
```

- [ ] **Step 2: Run it and see it fail**

```bash
cd D:/clients/casaos/get-digest && bash scripts/test-install-telemetry.sh
```

Expected: exit 1 with `FAIL: install.sh defines no Telemetry_Notice()`.

- [ ] **Step 3: Implement in `install.sh`**

(a) Directly below the `Welcome_Banner` function, whose last lines are

```bash
    echo -e " ${COLOUR_RESET}${aCOLOUR[1]}Uninstall       ${COLOUR_RESET}: casaos-uninstall"
    echo -e "${COLOUR_RESET}"
}
```

add:

```bash

# The closing block of every run: whether this box sends anonymous statistics,
# what they contain, and how to turn them off. Off when this run turned them
# off, when that request still waits for the core, or when the core has them off.
Telemetry_Notice() {
    echo -e "${GREEN_LINE}"
    if ((NO_TELEMETRY)) || [[ -e "${CASA_STATE_DIR}/telemetry-off" ]] ||
        ${sudo_cmd} grep -Eqs '"enabled"[[:space:]]*:[[:space:]]*false' "${CASA_STATE_DIR}/telemetry.json"; then
        echo -e " ${aCOLOUR[1]}Anonymous statistics are off.${COLOUR_RESET}"
        echo -e " Upgrades keep them off; the dashboard's settings can turn them on."
    else
        echo -e " ${aCOLOUR[1]}Anonymous statistics are on.${COLOUR_RESET}"
        echo -e " Once a day this box tells the ReCasaOS maintainers which release it runs"
        echo -e " and on what hardware, through PostHog (EU), which keeps the country of"
        echo -e " the connection, never its address. No apps, files, accounts or names."
        echo -e " Turn them off in the dashboard's settings (Anonymous usage statistics),"
        echo -e " or run the installer again with --no-telemetry or RECASAOS_TELEMETRY=0."
    fi
    echo -e " What they contain: https://github.com/ReCasaOS/CasaOS-Install#anonymous-statistics"
    echo -e "${GREEN_LINE}"
    echo -e "${COLOUR_RESET}"
}
```

(b) Replace the end of the script

```bash
# Step 10: Clear Term and Show Welcome Banner
Welcome_Banner
```

with

```bash
# Step 10: Clear Term and Show Welcome Banner
Welcome_Banner

# Step 11: Anonymous statistics, on or off, and how to turn them off
Telemetry_Notice
```

- [ ] **Step 4: Add the README section**

In `README.md`, replace the line `## What is in v0.5.0` with the block below. The block ends with that same line, so it is kept.

````markdown
## Anonymous statistics

A ReCasaOS box sends anonymous statistics, so that the maintainers know how many boxes are running, which release they run, how fast they update after a release, and on what hardware: what GitHub's download counters cannot tell. They are **on by default**, and they say so: at the end of every install and upgrade, in a notice the dashboard shows once, and here. Any one of the three ways below turns them off.

**Who receives them.** [PostHog](https://posthog.com) Cloud, in its EU region. The CasaOS core posts them to `https://eu.i.posthog.com/i/v0/e/`; the dashboard sends nothing.

**When.** Two events: `heartbeat`, at most once every 24 hours, which counts the boxes running, and `version_changed`, once after an install or an upgrade, which measures how fast boxes update. A failed send waits for the next hourly check; nothing is queued.

**Which box.** A random UUID, made on first use and kept in `/var/lib/casaos/telemetry.json` (root only). It derives from nothing on the machine; a reinstall that removes `/var/lib/casaos` makes a new one.

**What is sent.** These properties, on both events, `previous_distribution` on `version_changed` only. *See what is sent*, in the dashboard's settings, shows the values this box would send, built by the code that sends them.

| Property | Example | Source |
|---|---|---|
| `distribution` | `v0.5.0` | `/var/lib/casaos/fork-release`, trimmed; `unknown` if absent |
| `previous_distribution` | `v0.4.99`, `new`, `upstream` | `/var/lib/casaos/upgraded-from`, written by the installer: the release it replaced, `new` on a machine without CasaOS, `upstream` on a box coming from IceWhale's CasaOS |
| `core` | `v0.4.59` | `"v" + common.VERSION` |
| `arch` | `amd64`, `arm64`, `arm-7` | `runtime.GOARCH`, plus `GOARM` from the build info for `arm` |
| `os` | `ubuntu 24.04`, `debian 12`, `arch` | `/etc/os-release`: `ID` and `VERSION_ID` (ID alone when there is no VERSION_ID); `unknown` if unreadable |
| `kernel` | `6.8` | `uname` release, major.minor |
| `virtualization` | `none`, `kvm`, `lxc`, `wsl`, `unknown` | `systemd-detect-virt` output; `unknown` if it cannot run |
| `model` | `Raspberry Pi 5 Model B Rev 1.0`, `ZimaBoard` | `/proc/device-tree/model`, else `/sys/class/dmi/id/product_name`; NUL and spaces trimmed, 64 characters max; `unknown` if empty or a known placeholder (`To Be Filled By O.E.M.`, `System Product Name`, `Default string`, `Not Specified`) |
| `docker` | `28.3.1` | `GET /version` on `/var/run/docker.sock`, field `Version`; `unknown` if unavailable |
| `cpu_cores` | `4` | `runtime.NumCPU()` |
| `ram_gb` | `8` | `MemTotal` from `/proc/meminfo`, rounded to the nearest of 1, 2, 4, 8, 16, 32, 64, then `128+` |
| `disks` | `3` | entries of `/sys/block` whose resolved path is not under `/sys/devices/virtual/`, excluding `sr*` and `mmcblk*boot*` |
| `storage_tb` | `4-8` | sum of those disks' sizes (`/sys/block/<d>/size` × 512 bytes), bucketed: `<0.5`, `0.5-1`, `1-2`, `2-4`, `4-8`, `8-16`, `16-32`, `32+` (TB, 10^12 bytes) |
| `raid` | `true` | `/proc/mdstat` lists an active `md` array |

Every event also carries `$process_person_profile: false`, so PostHog makes no person profile, and `$lib: "recasaos-core"`. PostHog derives the **country** from the address the event comes from: the project discards that address, and keeps the country and the continent only, every finer location being dropped before anything is stored.

**Never sent:** IP address (discarded by the project setting), hostname, MAC address, serial numbers, disk names, labels or paths, installed apps, user accounts, anything about the local network.

### Turning them off

Any one of these; an upgrade never turns them back on.

- **With the installer**: `--no-telemetry`, or `RECASAOS_TELEMETRY=0`, on an install or on an upgrade.

  ```bash
  curl -fsSL https://github.com/ReCasaOS/CasaOS-Install/releases/latest/download/install.sh | sudo bash -s -- --no-telemetry
  curl -fsSL https://github.com/ReCasaOS/CasaOS-Install/releases/latest/download/install.sh | sudo RECASAOS_TELEMETRY=0 bash
  ```

- **In the dashboard**: the *Anonymous usage statistics* switch in the settings. It takes effect at once.
- **By hand**: stop the core (`sudo systemctl stop casaos`), set `"enabled": false` in `/var/lib/casaos/telemetry.json` (or create the file holding `{"enabled": false}` if it is not there yet), and start it again (`sudo systemctl start casaos`).

## What is in v0.5.0
````

- [ ] **Step 5: Run it and see it pass**

```bash
cd D:/clients/casaos/get-digest && bash scripts/test-install-telemetry.sh && grep -c '^## Anonymous statistics' README.md && grep -c '^## What is in v0.5.0' README.md
```

Expected: exit 0, with
```
note: not Linux, the Linux-only checks are skipped
ok: Previous_Release names the tag, upstream or new
ok: Write_Telemetry_Markers writes upgraded-from, 600, and logs it
ok: install.sh writes the markers with the services stopped, before the copy onto /
ok: telemetry-off only when asked, an earlier choice kept
ok: the closing notice says on or off, links the README, and ends install.sh
1
1
```

- [ ] **Step 6: Commit**

```bash
cd D:/clients/casaos/get-digest && git add install.sh scripts/test-install-telemetry.sh README.md && git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(install): close every run on anonymous statistics; the README says what is sent" -m "The last block of every run says whether this box sends anonymous statistics, what they contain, and how to turn them off, linking the README's new 'Anonymous statistics' section; the self-check holds its call as install.sh's last line. The section gives the properties, what is never sent, PostHog EU, and the three ways to turn them off."
```

---

### Task 4: Install check: install with `--no-telemetry`, statistics off

**Files:**
- Modify: `.github/workflows/install-check.yml` (header comment, lines 13-15; install step, lines 91-95; new step after `First user, then a token`, which ends at line 184)
- Create (outside the repo): `C:/Users/garyd/AppData/Local/Temp/claude/D--clients-StagevetV2/89b632dd-c1dd-4e14-bcc1-a7dd364b6926/scratchpad/stats-plans/workflow-check.py`
- Test: the new step, in CI, on the distribution release (see Global Constraints: ship order). Locally, `workflow-check.py` and `bash -n`.

**Interfaces:**
- Consumes: `install.sh --no-telemetry` (Task 2). The `Previous release: new` line (Task 1). The notice line `Anonymous statistics are off.` and the README URL (Task 3). `GET /v1/sys/telemetry` from the core. The env `CASA_URL`, `CASA_TOKEN` (earlier steps) and `CASAOS_RELEASE_TAG` (`components.lock`, line 69).
- Produces: the install step runs `sudo -E bash install.sh --no-telemetry`, so the core never runs with statistics on before Task 5's step. The header comment sends a check of an older tag to that tag's own workflow (`Use workflow from`). New step `Statistics are off after an install with --no-telemetry`. The helper `workflow-check.py <workflow.json> <out dir> [capture]`.

- [ ] **Step 1: Write the local workflow check**

Create `C:/Users/garyd/AppData/Local/Temp/claude/D--clients-StagevetV2/89b632dd-c1dd-4e14-bcc1-a7dd364b6926/scratchpad/stats-plans/workflow-check.py` with the Write tool. If the file is already there, read it and keep it when it matches:

```python
"""Local check of install-check.yml, which only runs for real against a release.

    npx -y js-yaml <install-check.yml> > install-check.json
    python workflow-check.py install-check.json <out dir> [capture]
    for f in <out dir>/step-*.sh; do bash -n "$f" || echo "FAIL: $f"; done

Writes every `run:` block to <out dir>/step-<n>.sh for `bash -n`. With
`capture`, also runs the capture server of the statistics step exactly as the
workflow writes it, posts the two events the core would send, and reads them
back; it fails when the workflow has no capture server.
"""
import json
import pathlib
import subprocess
import sys
import time
import urllib.error
import urllib.request

workflow = json.load(open(sys.argv[1], encoding="utf-8"))
steps = workflow["jobs"]["install-check"]["steps"]
out = pathlib.Path(sys.argv[2])
out.mkdir(parents=True, exist_ok=True)
for old in out.glob("step-*.sh"):
    old.unlink()

marker = "cat > capture.py <<'PY'\n"
capture_source = None
for i, step in enumerate(steps):
    run = step.get("run")
    if not run:
        continue
    (out / f"step-{i:02d}.sh").write_text(run, encoding="utf-8", newline="\n")
    if marker in run:
        capture_source = run.split(marker, 1)[1].split("\nPY\n", 1)[0]
print(f"ok: {len(steps)} steps, run blocks written to {out}")

if sys.argv[3:] != ["capture"]:
    sys.exit(0)

if not capture_source:
    sys.exit("FAIL: no capture server in the workflow")
server_file = out / "capture.py"
server_file.write_text(capture_source + "\n", encoding="utf-8", newline="\n")
captured = out / "capture.jsonl"
captured.unlink(missing_ok=True)
server = subprocess.Popen([sys.executable, str(server_file), "18099", str(captured)],
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
try:
    for _ in range(40):
        try:
            urllib.request.urlopen("http://127.0.0.1:18099/", timeout=1)
        except urllib.error.HTTPError:
            break  # a GET answers 501: the server is up
        except OSError:
            time.sleep(0.25)
    for event in ("version_changed", "heartbeat"):
        body = json.dumps({"api_key": "phc_test", "event": event, "distinct_id": "x",
                           "timestamp": "2026-09-22T00:00:00Z", "properties": {}}).encode()
        request = urllib.request.Request("http://127.0.0.1:18099/i/v0/e/", data=body,
                                         headers={"Content-Type": "application/json"})
        assert urllib.request.urlopen(request, timeout=5).status == 200
    events = [json.loads(line)["event"] for line in captured.read_text().splitlines()]
    assert events == ["version_changed", "heartbeat"], events
    print("ok: the capture server records what is posted to it")
finally:
    server.kill()
```

- [ ] **Step 2: See the current workflow without the check**

```bash
cd D:/clients/casaos/get-digest && grep -c 'install.sh --no-telemetry' .github/workflows/install-check.yml; grep -c 'Statistics are off after an install' .github/workflows/install-check.yml
```

Expected: `0` and `0`. The real failing run cannot be reproduced locally, because install-check only installs a published release. Against v0.5.0, the edited workflow would stop at the install step: v0.5.0's `install.sh` answers `--no-telemetry` with its usage and exit 1.

- [ ] **Step 3: Install with the flag**

In `.github/workflows/install-check.yml`, replace the end of the header comment

```yaml
# Called by release.yml after every publish, and runnable by hand against any
# tag -- which publishes nothing again. Red does not unpublish anything; it is
# the alarm.
```

with

```yaml
# Called by release.yml after every publish, and runnable by hand against any
# tag -- which publishes nothing again. Red does not unpublish anything; it is
# the alarm. The install step passes --no-telemetry, which the installers
# released before the anonymous statistics refuse: to check one of those tags
# by hand, dispatch with "Use workflow from" set to that same tag, whose own
# install-check.yml installs without the flag.
```

then replace

```yaml
      - name: Run the installer as the README says
        run: |
          set -euo pipefail
          sudo -E bash install.sh 2>&1 | tee install.log
```

with

```yaml
      # With --no-telemetry, as the README gives it: no step may run the core
      # with anonymous statistics on and the default endpoint, PostHog. They are
      # turned on once, in the last step, with the core pointed at a capture
      # server on this runner.
      - name: Run the installer as the README says
        run: |
          set -euo pipefail
          sudo -E bash install.sh --no-telemetry 2>&1 | tee install.log
```

- [ ] **Step 4: Check the off state**

Replace

```yaml
          echo "::add-mask::${token}"
          echo "CASA_TOKEN=${token}" >> "$GITHUB_ENV"
```

with

```yaml
          echo "::add-mask::${token}"
          echo "CASA_TOKEN=${token}" >> "$GITHUB_ENV"

      # The install above ran with --no-telemetry on a machine that never had
      # CasaOS: the installer named what it replaced, closed on the statistics
      # being off, and the core came up with them off. Its preview is what it
      # would send, built by the code that sends.
      - name: Statistics are off after an install with --no-telemetry
        run: |
          set -euo pipefail
          grep -E '\] Previous release: new *$' install.log
          grep -F 'Anonymous statistics are off.' install.log
          grep -F 'https://github.com/ReCasaOS/CasaOS-Install#anonymous-statistics' install.log
          state="$(curl -fsS "${CASA_URL}/v1/sys/telemetry" -H "Authorization: ${CASA_TOKEN}")"
          echo "${state}" | jq -c '.data'
          jq -e --arg tag "${CASAOS_RELEASE_TAG}" \
            '.data.enabled == false and .data.preview.event == "heartbeat" and .data.preview.properties.distribution == $tag' \
            <<<"${state}" >/dev/null
```

- [ ] **Step 5: Run the local checks**

```bash
cd C:/Users/garyd/AppData/Local/Temp/claude/D--clients-StagevetV2/89b632dd-c1dd-4e14-bcc1-a7dd364b6926/scratchpad/stats-plans && npx -y js-yaml D:/clients/casaos/get-digest/.github/workflows/install-check.yml > install-check.json && python workflow-check.py install-check.json runs && for f in runs/step-*.sh; do bash -n "$f" || echo "FAIL: $f"; done; grep -c 'install.sh --no-telemetry' D:/clients/casaos/get-digest/.github/workflows/install-check.yml; grep -c '"Use workflow from" set to that same tag' D:/clients/casaos/get-digest/.github/workflows/install-check.yml
```

Expected: `ok: 29 steps, run blocks written to runs`, no `FAIL:` line, then `1` and `1`.

- [ ] **Step 6: Commit**

```bash
cd D:/clients/casaos/get-digest && git add .github/workflows/install-check.yml && git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "ci(install-check): install with --no-telemetry, statistics off and the previous release named" -m "The core never runs here with statistics on and the default endpoint. The new step checks that the install log says 'Previous release: new' and closes on the statistics being off, and that GET /v1/sys/telemetry answers enabled false with a preview naming this release. The header says how to check an older tag by hand: dispatch from that tag, whose own workflow installs without the flag."
```

---

### Task 5: Install check: statistics reach a capture server on the runner, never PostHog

**Files:**
- Modify: `.github/workflows/install-check.yml` (new step after `A RAID array is one storage`, which ends at line 870 `echo "the array is one storage: ${array}"`; `Keep the installer log` paths, lines 910-912)
- Test: the new step, in CI, on the distribution release. Locally, `workflow-check.py ... capture` and `bash -n`.

**Interfaces:**
- Consumes: Task 4's install with `--no-telemetry` and its step asserting `enabled == false`. From the core: `CASAOS_TELEMETRY_ENDPOINT`, `CASAOS_TELEMETRY_START_DELAY`, `PUT /v1/sys/telemetry`, the event body. The core must also send a `Content-Length` (a Go `bytes.Reader` body sets it), run its first check right after start when the delay is `0s`, and send `version_changed` before `heartbeat` when `upgraded-from` exists. The core's release build, `CGO_ENABLED=0` with `-tags "musl netgo osusergo"` (`D:/clients/casaos/CasaOS/.github/workflows/release.yml`, lines 53-60), uses Go's own resolver, which reads `/etc/hosts`. The runner env `CASA_URL`, `CASA_TOKEN`, `CASAOS_RELEASE_TAG`, `CASAOS_TAG` (`components.lock`).
- Produces:
  - Step `Statistics reach a local capture server, never PostHog`. It is the last step before `Diagnostics`, so no later step restarts the core.
  - Drop-in `/etc/systemd/system/casaos.service.d/telemetry-check.conf`.
  - Capture server at `http://127.0.0.1:18099/i/v0/e/`.
  - `capture.jsonl` in the uploaded artifact.
  - The guard, in order: the install ran with `--no-telemetry` and Task 4 checked it off. Then `/etc/hosts` gets `127.0.0.1 eu.i.posthog.com`, so a core that ignored `CASAOS_TELEMETRY_ENDPOINT` (read once too early, or fallen back to the default) would fail to connect on this runner and the step would go red, never reaching PostHog with the real key. Then the core is restarted with the drop-in, and `/proc/<MainPID>/environ` must hold the capture endpoint. Only then is `PUT {"enabled":true}` sent. This is why there are two restarts where the spec lists one: with the spec's order (PUT, then restart), the running core would hold statistics on with the default endpoint between the PUT and the restart.

- [ ] **Step 1: Run the capture check and see it fail**

```bash
cd C:/Users/garyd/AppData/Local/Temp/claude/D--clients-StagevetV2/89b632dd-c1dd-4e14-bcc1-a7dd364b6926/scratchpad/stats-plans && npx -y js-yaml D:/clients/casaos/get-digest/.github/workflows/install-check.yml > install-check.json && python workflow-check.py install-check.json runs capture
```

Expected: `ok: 29 steps, run blocks written to runs`, then `FAIL: no capture server in the workflow`, exit 1.

- [ ] **Step 2: Add the step**

In `.github/workflows/install-check.yml`, replace

```yaml
          echo "the array is one storage: ${array}"
```

with

```yaml
          echo "the array is one storage: ${array}"

      # Statistics on, sent to a capture server on this runner, never to PostHog.
      # PostHog's host resolves to this runner, as a net under the endpoint
      # check: a core that ignored the test endpoint fails here instead. The
      # core is restarted with the test endpoint first, and the endpoint is
      # read back from the running process's environment before statistics are
      # turned on. upgraded-from is then written as the installer writes it (the
      # core's first check, with statistics off, deleted the installer's), and a
      # second restart sends at once (CASAOS_TELEMETRY_START_DELAY=0s).
      - name: Statistics reach a local capture server, never PostHog
        run: |
          set -euo pipefail
          endpoint=http://127.0.0.1:18099/i/v0/e/
          cat > capture.py <<'PY'
          import http.server, sys

          class Capture(http.server.BaseHTTPRequestHandler):
              def do_POST(self):
                  body = self.rfile.read(int(self.headers["Content-Length"]))
                  with open(sys.argv[2], "ab") as out:
                      out.write(body + b"\n")
                  self.send_response(200)
                  self.send_header("Content-Type", "application/json")
                  self.end_headers()
                  self.wfile.write(b'{"status":"Ok"}')

          http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), Capture).serve_forever()
          PY
          : > capture.jsonl
          python3 capture.py 18099 capture.jsonl > capture.log 2>&1 &
          server=$!
          trap 'kill "${server}" 2>/dev/null || true; echo "::group::captured"; cat capture.jsonl; echo "::endgroup::"; echo "::group::casaos"; sudo journalctl -u casaos.service --no-pager -o cat -n 40; echo "::endgroup::"' EXIT
          for _ in $(seq 1 20); do
            curl -s -o /dev/null http://127.0.0.1:18099/ && break
            sleep 0.5
          done

          # The core is built with CGO_ENABLED=0 and netgo: Go's own resolver
          # reads /etc/hosts first, so its default endpoint would be 127.0.0.1:443.
          echo '127.0.0.1 eu.i.posthog.com' | sudo tee -a /etc/hosts >/dev/null

          sudo mkdir -p /etc/systemd/system/casaos.service.d
          printf '[Service]\nEnvironment=CASAOS_TELEMETRY_ENDPOINT=%s\nEnvironment=CASAOS_TELEMETRY_START_DELAY=0s\n' "${endpoint}" \
            | sudo tee /etc/systemd/system/casaos.service.d/telemetry-check.conf
          sudo systemctl daemon-reload
          sudo systemctl restart casaos.service
          pid="$(systemctl show -p MainPID --value casaos.service)"
          sudo cat "/proc/${pid}/environ" | tr '\0' '\n' | grep -Fx "CASAOS_TELEMETRY_ENDPOINT=${endpoint}"
          for _ in $(seq 1 30); do
            curl -fsS -o /dev/null "${CASA_URL}/v1/sys/telemetry" -H "Authorization: ${CASA_TOKEN}" && break
            sleep 2
          done

          curl -fsS -X PUT "${CASA_URL}/v1/sys/telemetry" -H "Authorization: ${CASA_TOKEN}" \
            -H 'content-type: application/json' -d '{"enabled":true}' | jq -e '.data.enabled == true' >/dev/null
          sudo install -m 0600 /dev/null /var/lib/casaos/upgraded-from
          printf 'new\n' | sudo tee /var/lib/casaos/upgraded-from >/dev/null
          sudo systemctl restart casaos.service

          for _ in $(seq 1 60); do
            jq -s -e 'map(.event) | index("heartbeat") != null and index("version_changed") != null' capture.jsonl >/dev/null 2>&1 && break
            sleep 2
          done
          events="$(jq -s -c . capture.jsonl)"
          arch="$(dpkg --print-architecture)"
          os="$(. /etc/os-release && echo "${ID} ${VERSION_ID}")"
          echo "expecting distribution ${CASAOS_RELEASE_TAG}, core ${CASAOS_TAG}, arch ${arch}, os ${os}"
          jq -e --arg tag "${CASAOS_RELEASE_TAG}" --arg core "${CASAOS_TAG}" --arg arch "${arch}" --arg os "${os}" '
            map(select(.event == "heartbeat")) | length >= 1 and all(
              .api_key == "phc_C5T8QbrhBttzgkWuKm2Xr9S4TLGh3sCVAZai2oaFEuhD"
              and .properties.distribution == $tag and .properties.core == $core
              and .properties.arch == $arch and .properties.os == $os
              and .properties["$process_person_profile"] == false
              and .properties["$lib"] == "recasaos-core"
              and (.properties | has("$geoip_disable") | not))' <<<"${events}" >/dev/null
          jq -e --arg tag "${CASAOS_RELEASE_TAG}" '
            map(select(.event == "version_changed")) | length == 1 and all(
              .properties.previous_distribution == "new" and .properties.distribution == $tag)' <<<"${events}" >/dev/null
          echo "heartbeat and version_changed captured, from this release, on this machine"
```

Then, in the `Keep the installer log` step, replace

```yaml
            install.log
            backup-files.txt
```

with

```yaml
            install.log
            backup-files.txt
            capture.jsonl
```

- [ ] **Step 3: Run the local checks and see them pass**

```bash
cd C:/Users/garyd/AppData/Local/Temp/claude/D--clients-StagevetV2/89b632dd-c1dd-4e14-bcc1-a7dd364b6926/scratchpad/stats-plans && npx -y js-yaml D:/clients/casaos/get-digest/.github/workflows/install-check.yml > install-check.json && python workflow-check.py install-check.json runs capture && for f in runs/step-*.sh; do bash -n "$f" || echo "FAIL: $f"; done; grep -c "echo '127.0.0.1 eu.i.posthog.com' | sudo tee -a /etc/hosts" D:/clients/casaos/get-digest/.github/workflows/install-check.yml; echo checked
```

Expected:
```
ok: 30 steps, run blocks written to runs
ok: the capture server records what is posted to it
1
checked
```
with no `FAIL:` line. The jq expressions run only on the runner, where jq 1.6 is present. In CI, the step must print the captured events and end with `heartbeat and version_changed captured, from this release, on this machine` on all three legs.

- [ ] **Step 4: Commit**

```bash
cd D:/clients/casaos/get-digest && git add .github/workflows/install-check.yml && git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "ci(install-check): statistics reach a capture server on the runner, never PostHog" -m "eu.i.posthog.com resolves to 127.0.0.1 on the runner, a drop-in points the core at a Python capture server with no start delay, and the running core's environment is checked before statistics are turned on. upgraded-from is written as the installer writes it, and after a restart the step checks the heartbeat (this release, this core, the runner's arch and os, no person profile, GeoIP left on) and a version_changed from 'new'."
```

---

## After the tasks

The controller does the release, not the implementer. The core and the dashboard are tagged first. `release/components.env` pins them, and the controller writes the `CHANGELOG.md` entry that announces the statistics first, the README "What is in" entry and the Components table. The tag's `release.yml` runs the self-check on Linux (modes and option parsing included), then publishes, then calls `install-check.yml`. Its two statistics steps, green on the three legs, prove the whole chain: installer flag, markers, notice, core API, sender and properties. Before that release, the owner completes the PostHog project setup (spec, "PostHog project setup"), because the README states the IP discard and the GeoIP filtering as facts.

## Spec coverage

| Spec requirement (installer, README, install check) | Task |
|---|---|
| `--no-telemetry` creates `/var/lib/casaos/telemetry-off` (root, 0600) before the services start | 2 (inside `Write_Telemetry_Markers`, whose call Task 1's line-number check holds between the stop loop and the copy onto `/`) |
| `RECASAOS_TELEMETRY=0` does the same | 2 |
| An upgrade never turns statistics back on; without the flag an existing choice is left as it is | 2 (test `kept`) |
| `upgraded-from` decided before the overlay replaces `fork-release`: previous tag, else `upstream` when a `casaos` binary exists, else `new` | 1 (the values; the call's place checked from the line numbers, and seen failing on a misplaced copy) |
| `upgraded-from` written root 0600 on every run, statistics on or off | 1 |
| Log line `Previous release: <value>` | 1 |
| The dashboard's update button goes through the same path | 1 (called inside `DownloadAndInstallCasaOS`, which the detached run executes) |
| Closing notice at the end of every install or upgrade, in English: on or off, what they contain (README link), how to turn them off | 3 (its call checked as `install.sh`'s last line) |
| `usage()` names the option | 2 |
| README `## Anonymous statistics`: what is sent (the table), never sent, PostHog EU, the three ways to turn them off | 3 |
| Install check 1: install with `--no-telemetry`, log says `Previous release: new`, GET answers `enabled: false` with a preview whose `distribution` is the tag | 4 |
| Install check 2: a local capture server in Python | 5 |
| Install check 3: drop-in with `CASAOS_TELEMETRY_ENDPOINT` and `CASAOS_TELEMETRY_START_DELAY=0s`, PUT enabled, `new` written to `upgraded-from`, core restarted | 5 |
| Install check 4: `heartbeat` with `distribution` = tag, the runner's `arch` and `os`; `version_changed` with `previous_distribution: new` | 5 |
| No CI step runs the core with statistics on and the default endpoint | 4 (install flag, off asserted), 5 (`eu.i.posthog.com` → `127.0.0.1` in `/etc/hosts`, endpoint checked in the running core before the PUT; last step) |
| Install check runnable by hand against an older tag | 4 (header comment: dispatch from that tag) |
| CHANGELOG entry announcing it first | controller, at release time (not in this plan) |
| Shipped together in one distribution release | After the tasks |
