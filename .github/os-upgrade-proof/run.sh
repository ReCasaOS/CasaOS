#!/usr/bin/env bash
# The proof of the upgrade of the operating system, on a machine that boots: a Debian 11 cloud image under QEMU/KVM, ReCasaOS installed in it by its own installer, the core of
# this checkout put in the place of the installed one, and the upgrade driven through the core's API from inside the guest, as one of the box's own services (from loopback, with
# the secret of the boot). Three legs, each one including the one before: smoke (the core answers, the switch, the readiness report, the plan), rehearsal (the refresh, a
# rehearsal, its discard: the sources are back byte for byte) and hop (the whole hop, the restart, the check after it).
#
# It runs on the runner, not in the guest: the guest is reached over ssh on localhost:2222. Everything it learns goes to $OUT, which the workflow uploads.
set -Eeuo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
WORK=${WORK:-${RUNNER_TEMP:-/tmp}/proof}
OUT=${OUT:-$PWD/proof-out}
LEG=${LEG:-$(tr -d '[:space:]' < "$HERE/LEG")}
CORE=${CORE:?the path of the core to put in the guest}
INSTALLER=${INSTALLER:-https://github.com/ReCasaOS/CasaOS-Install/releases/latest/download/install.sh}
IMAGE_DIRS=(
  "https://cloud.debian.org/images/cloud/bullseye/latest"
  "https://cdimage.debian.org/cdimage/cloud/bullseye/latest"
)
IMAGE_NAME=debian-11-genericcloud-amd64.qcow2
mkdir -p "$WORK" "$OUT"
KEY=$WORK/key
SSH=(ssh -i "$KEY" -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=10 -o ServerAliveInterval=15 -o ServerAliveCountMax=8 debian@127.0.0.1)

log() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" | tee -a "$OUT/proof.log"; }
die() { log "FAILED: $*"; exit 1; }

# g runs a command line in the guest; groot runs the script on its standard input as root.
g() { "${SSH[@]}" "$@"; }
groot() { "${SSH[@]}" sudo bash -s; }

collect() {
  local code=$?
  set +e
  log "collecting what the guest knows (exit $code)"
  if g true 2> /dev/null; then
    g 'sudo tar -czf - /var/lib/casaos/os-upgrade /var/backups/casaos-os-upgrade* /var/log/apt /var/log/dpkg.log /var/log/casaos /tmp/install.log 2>/dev/null' > "$OUT/guest-files.tar.gz"
    g 'sudo journalctl --no-pager -n 4000' > "$OUT/guest-journal.txt" 2> /dev/null
    g 'cat /etc/os-release; uname -r; sudo systemctl --failed --no-legend; sudo ls -la /etc/apt /etc/apt/sources.list.d; sudo cat /etc/apt/sources.list' > "$OUT/guest-facts.txt" 2> /dev/null
  fi
  [ ! -s "$WORK/qemu.pid" ] || kill "$(cat "$WORK/qemu.pid")" 2> /dev/null
  {
    echo "## OS upgrade proof, leg \`$LEG\`: exit $code"
    echo
    tail -n 60 "$OUT/proof.log" | sed 's/^/    /'
  } >> "${GITHUB_STEP_SUMMARY:-/dev/null}"
  exit "$code"
}
trap collect EXIT

# ---- the host ----------------------------------------------------------------------------------------------------------------------------------------------------------------

[ -e /dev/kvm ] || die "the runner has no /dev/kvm"
ssh-keygen -q -t ed25519 -N '' -f "$KEY"

fetch_image() {
  local dir
  for dir in "${IMAGE_DIRS[@]}"; do
    log "image from $dir"
    if curl -fsSL --retry 3 -o "$WORK/$IMAGE_NAME" "$dir/$IMAGE_NAME" && curl -fsSL --retry 3 -o "$WORK/SHA512SUMS" "$dir/SHA512SUMS"; then
      (cd "$WORK" && grep " $IMAGE_NAME\$" SHA512SUMS | sha512sum -c -) || die "the image does not match its sum"
      return 0
    fi
  done
  die "no image of Debian 11 could be fetched"
}

boot_guest() {
  cp "$WORK/$IMAGE_NAME" "$WORK/disk.qcow2"
  qemu-img resize -q "$WORK/disk.qcow2" 30G
  {
    echo '#cloud-config'
    echo 'hostname: proof'
    echo 'ssh_authorized_keys:'
    echo "  - $(cat "$KEY.pub")"
  } > "$WORK/user-data"
  printf 'instance-id: proof\nlocal-hostname: proof\n' > "$WORK/meta-data"
  cloud-localds "$WORK/seed.img" "$WORK/user-data" "$WORK/meta-data"
  qemu-system-x86_64 -machine q35,accel=kvm -cpu host -smp 4 -m 6144 -display none -serial "file:$OUT/console.log" \
    -drive "if=virtio,file=$WORK/disk.qcow2,format=qcow2" -drive "if=virtio,file=$WORK/seed.img,format=raw" \
    -netdev user,id=n0,hostfwd=tcp:127.0.0.1:2222-:22 -device virtio-net-pci,netdev=n0 \
    -daemonize -pidfile "$WORK/qemu.pid"
  local i
  for i in $(seq 1 90); do
    g true 2> /dev/null && { log "the guest answers over ssh"; return 0; }
    sleep 5
  done
  die "the guest did not answer over ssh in 7 minutes"
}

# ---- the guest, made ready ------------------------------------------------------------------------------------------------------------------------------------------

prepare_guest() {
  log "guest: $(g 'grep PRETTY_NAME /etc/os-release; uname -r')"
  scp -q -i "$KEY" -P 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR "$HERE/osu-api" debian@127.0.0.1:/tmp/osu-api
  groot << 'EOF'
set -eu
install -m 0755 /tmp/osu-api /usr/local/bin/osu-api
# the sources of a Debian 11 box today: the release and its updates where they are, the security archive where the owner is told to take it from
main=http://deb.debian.org/debian
if ! curl -fsI "$main/dists/bullseye/Release" > /dev/null; then main=http://archive.debian.org/debian; echo "bullseye main is on the archive"; fi
cat > /etc/apt/sources.list << LIST
deb $main bullseye main
deb $main bullseye-updates main
deb http://archive.debian.org/debian-security bullseye-security main
LIST
rm -f /etc/apt/sources.list.d/*
export DEBIAN_FRONTEND=noninteractive
if ! out=$(apt-get update 2>&1); then
  echo "$out"
  if echo "$out" | grep -qi 'expired'; then
    echo 'Acquire::Check-Valid-Until "false";' > /etc/apt/apt.conf.d/99proof-no-valid-until
    echo "a Release file of the archive has expired: the check of its date is off for this proof"
    apt-get update
  else
    exit 1
  fi
fi
apt-get install -y curl ca-certificates
EOF
}

install_casaos() {
  log "installing ReCasaOS with its own installer"
  g "curl -fsSL '$INSTALLER' -o /tmp/install.sh"
  g 'sudo bash /tmp/install.sh < /dev/null > /tmp/install.log 2>&1' || { g 'tail -n 60 /tmp/install.log' | tee -a "$OUT/proof.log"; die "the installer failed"; }
  g 'tail -n 8 /tmp/install.log' | tee -a "$OUT/proof.log"
  g 'sudo systemctl is-active casaos casaos-gateway casaos-message-bus' | tee -a "$OUT/proof.log" || true
}

put_core() {
  log "putting the core of this checkout in the place of the installed one"
  scp -q -i "$KEY" -P 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR "$CORE" debian@127.0.0.1:/tmp/casaos-new
  groot << 'EOF'
set -eu
systemctl stop casaos
cp -a /usr/bin/casaos /usr/bin/casaos.orig
install -m 0755 /tmp/casaos-new /usr/bin/casaos
systemctl start casaos
EOF
  local i
  for i in $(seq 1 60); do
    if apij GET /v1/sys/os/upgrade/status > "$OUT/status-first.json" 2> /dev/null; then log "the core of the checkout answers"; return 0; fi
    sleep 5
  done
  g 'sudo systemctl status casaos --no-pager -l; sudo journalctl -u casaos --no-pager -n 60' | tee -a "$OUT/proof.log" || true
  die "the core of the checkout does not answer"
}

# ---- the API ----------------------------------------------------------------------------------------------------------------------------------------------------------------

# apij METHOD PATH [JSON] prints the answer; it fails when the status is not 2xx (the answer is printed all the same).
apij() {
  local out code
  if [ $# -ge 3 ]; then
    out=$(printf '%s' "$3" | g "sudo osu-api $1 $2 -") || return 2
  else
    out=$(g "sudo osu-api $1 $2") || return 2
  fi
  code=${out##*HTTP }
  code=${code//[$'\n\r ']/}
  printf '%s\n' "${out%$'\n'HTTP *}"
  [[ $code == 2* ]]
}

# the data of an answer is under .data
state() { apij GET /v1/sys/os/upgrade/status | jq -r '(.data // .).state // "?"'; }

snapshot() { # snapshot NAME: the status, kept and summarized in the log
  local s
  s=$(apij GET /v1/sys/os/upgrade/status 2> /dev/null || true)
  printf '%s\n' "$s" > "$OUT/status-$1.json"
  printf '%s\n' "$s" | jq -c '(.data // .) | {state, reason, class, phase, attempt, block, pnr, failure, hint, barrier, findings, not_judged, actions}' 2> /dev/null | tee -a "$OUT/proof.log" || true
  printf '%s\n' "$s" | jq -r '(.data // .).log_tail // empty' 2> /dev/null | tail -n 25 | sed 's/^/    | /' | tee -a "$OUT/proof.log" || true
}

wait_state() { # wait_state SECONDS REGEX
  local end=$((SECONDS + $1)) want=$2 last="" cur
  while [ "$SECONDS" -lt "$end" ]; do
    cur=$(state 2> /dev/null || echo "?")
    [ "$cur" = "$last" ] || { log "state: $cur"; last=$cur; }
    if [[ $cur =~ ^($want)$ ]]; then return 0; fi
    case $cur in
      failed | needs_attention | interrupted | stopped_at_waypoint)
        snapshot "stopped-$cur"
        die "the run is $cur and not $want"
        ;;
    esac
    sleep 20
  done
  snapshot "timeout"
  die "no state of $want in $1 seconds (last: $last)"
}

must() { # must DESCRIPTION METHOD PATH [JSON]
  local what=$1
  shift
  local out
  if ! out=$(apij "$@"); then
    log "$what: refused or failed: $out"
    die "$what"
  fi
  printf '%s\n' "$out" > "$OUT/$(echo "$what" | tr -c 'A-Za-z0-9' '-').json"
  log "$what: ok"
}

# ---- the legs ----------------------------------------------------------------------------------------------------------------------------------------------------------------

make_plan() {
  log "making the plan (apt-get update in a sandbox, a simulated upgrade)"
  must "plan start" POST /v1/sys/os/upgrade/plan '{}'
  local end=$((SECONDS + 1800)) cur
  while [ "$SECONDS" -lt "$end" ]; do
    apij GET /v1/sys/os/upgrade/plan > "$OUT/plan.json" || true
    cur=$(jq -r '(.data // .).state // "?"' "$OUT/plan.json")
    case $cur in
      done) break ;;
      failed) jq -c '(.data // .).error' "$OUT/plan.json" | tee -a "$OUT/proof.log"; die "the plan failed" ;;
    esac
    sleep 20
  done
  [ "${cur:-}" = done ] || die "the plan did not end in 30 minutes"
  PLAN_ID=$(jq -r '(.data // .).plan.plan_id' "$OUT/plan.json")
  log "plan $PLAN_ID: $(jq -c '(.data // .).plan | {verdict, counts, removals: [.removals[]?.name], paused: [.paused[]?.file], problems: [.problems[]?.code]}' "$OUT/plan.json")"
}

start_body() { # start_body rehearsal(true|false)
  local plan accept paused acks host
  plan=$(jq -c '(.data // .).plan' "$OUT/plan.json")
  accept=$(printf '%s' "$plan" | jq -c '[.problems[]? | select(.code == "removal_manual") | .data.name]')
  paused=$(printf '%s' "$plan" | jq -c '[.paused[]?.file]')
  host=$(g hostname)
  acks='["no_interruption","no_way_back"]'
  [ "$paused" = '[]' ] || acks=$(printf '%s' "$acks" | jq -c '. + ["sources_paused"]')
  [ "$accept" = '[]' ] || acks=$(printf '%s' "$acks" | jq -c '. + ["removals_read"]')
  jq -nc --arg id "$PLAN_ID" --arg host "$host" --argjson rehearsal "$1" --argjson accept "$accept" --argjson paused "$paused" --argjson acks "$acks" \
    '{plan_id: $id, rehearsal: $rehearsal, safety_net: "reinstall_ok", hostname: $host, accept_removals: $accept, paused_sources: $paused, acks: $acks}'
}

apt_sum() { g 'sudo sh -c "cd /etc/apt && find . -type f | sort | xargs sha256sum | sha256sum"'; }

wait_new_boot() { # wait_new_boot BOOT_ID_BEFORE
  local end=$((SECONDS + 1200)) now
  while [ "$SECONDS" -lt "$end" ]; do
    now=$(g cat /proc/sys/kernel/random/boot_id 2> /dev/null || true)
    if [ -n "$now" ] && [ "$now" != "$1" ]; then log "the guest is up again on a new boot"; return 0; fi
    sleep 10
  done
  die "the guest did not come back in 20 minutes"
}

leg_smoke() {
  must "switch on" POST /v1/sys/os/upgrade/beta '{"enabled":true}'
  snapshot smoke-idle
  apij GET /v1/sys/os/readiness > "$OUT/readiness.json" || true
  jq -c '(.data // .) | {state, hop: .hop.to.codename, refusals: [.checks[]? | select(.severity == "refuse") | .code]}' "$OUT/readiness.json" | tee -a "$OUT/proof.log" || true
  make_plan
}

leg_rehearsal() {
  log "the refresh: bring Debian 11 to its last state"
  must "refresh start" POST /v1/sys/os/upgrade/refresh '{}'
  wait_state 3000 'refreshed|reboot_first'
  if [ "$(state)" = reboot_first ]; then
    local boot
    boot=$(g cat /proc/sys/kernel/random/boot_id)
    must "refresh restart" POST /v1/sys/os/upgrade/reboot '{}'
    wait_new_boot "$boot"
    sleep 30
    wait_state 600 'reboot_first|refreshed|resuming|verifying|succeeded'
  fi
  snapshot refresh-done
  must "refresh close" POST /v1/sys/os/upgrade/ack '{"action":"acknowledge"}'
  wait_state 120 'idle'

  make_plan
  local before
  before=$(apt_sum)
  log "the rehearsal"
  must "rehearsal start" POST /v1/sys/os/upgrade/start "$(start_body true)"
  wait_state 3000 'rehearsed'
  local sources
  sources=$(g 'sudo grep -rhc bookworm /etc/apt/sources.list /etc/apt/sources.list.d 2>/dev/null | awk "{ n += \$1 } END { print n + 0 }"' || echo 0)
  log "the sources of Debian 12 in place during the rehearsal: $sources lines"
  [ "${sources:-0}" -gt 0 ] || die "the rehearsal left the sources of Debian 11 in place"
  must "rehearsal discard" POST /v1/sys/os/upgrade/ack '{"action":"discard"}'
  wait_state 600 'rehearsal_done'
  [ "$(apt_sum)" = "$before" ] || die "/etc/apt is not what it was before the rehearsal"
  log "/etc/apt is byte for byte what it was"
  g 'sudo systemctl is-active apt-daily.timer apt-daily-upgrade.timer' | tee -a "$OUT/proof.log" || true
  must "rehearsal close" POST /v1/sys/os/upgrade/ack '{"action":"acknowledge"}'
  wait_state 120 'idle'
}

leg_hop() {
  log "the hop"
  must "hop start" POST /v1/sys/os/upgrade/start "$(start_body false)"
  wait_state 7200 'awaiting_reboot'
  snapshot awaiting-reboot
  local boot
  boot=$(g cat /proc/sys/kernel/random/boot_id)
  must "restart" POST /v1/sys/os/upgrade/reboot '{}'
  wait_new_boot "$boot"
  sleep 30
  wait_state 2400 'succeeded|succeeded_with_findings'
  snapshot hop-done
  g 'cat /etc/os-release | grep -E "^(PRETTY_NAME|VERSION_ID)="; uname -r; sudo systemctl --failed --no-legend; sudo docker ps --format "{{.Names}} {{.Status}}" 2>&1 | head -n 20' | tee -a "$OUT/proof.log"
  g 'grep -q "^VERSION_ID=\"12\"" /etc/os-release' || die "the guest does not run Debian 12"
  must "hop close" POST /v1/sys/os/upgrade/ack '{"action":"acknowledge"}'
}

# ---- go ---------------------------------------------------------------------------------------------------------------------------------------------------------------------

log "leg: $LEG"
fetch_image
boot_guest
prepare_guest
install_casaos
put_core
leg_smoke
case $LEG in
  smoke) ;;
  rehearsal) leg_rehearsal ;;
  hop) leg_rehearsal; leg_hop ;;
  *) die "unknown leg $LEG" ;;
esac
log "proof of leg $LEG: done"
