#!/usr/bin/env bash
set -euo pipefail

if [[ "$(uname -s)" != "Linux" ]]; then
	echo "the local stack is Linux only" >&2
	exit 1
fi
if [[ "$(id -u)" -eq 0 ]]; then
	echo "don't run the stack as root" >&2
	exit 1
fi

HERE=$(cd "$(dirname "$0")" && pwd)
CASAOS_REPO=$(cd "$HERE/.." && pwd)
PARENT=$(cd "$CASAOS_REPO/.." && pwd)
DEVROOT="$CASAOS_REPO/.dev-runtime"
WRITECONF="$HERE/writeconf.sh"

need() {
	if [[ ! -d "$PARENT/$1" ]]; then
		echo "missing $PARENT/$1" >&2
		exit 1
	fi
}
need CasaOS-Gateway
need CasaOS-MessageBus
need CasaOS-UserService

http_code() {
	curl -sS -o /dev/null -w '%{http_code}' --max-time 1 "$1" || echo 000
}

if [[ "$(http_code http://127.0.0.1:8081/ping)" == "200" ]]; then
	echo "127.0.0.1:8081 is already in use" >&2
	exit 1
fi

mkdir -p "$DEVROOT/run"
rm -f "$DEVROOT/run/"*.url "$DEVROOT/run/"*.pid "$DEVROOT/run/internal.secret"

bash "$WRITECONF" "$DEVROOT" "$CASAOS_REPO"

# cwd $DEVROOT/etc is inside the CasaOS module, so `go run` of a sibling
# fails with "outside main module". A workspace at DEVROOT is found first.
gowork_ver=$(go env GOVERSION)
gowork_ver=${gowork_ver#go}
printf 'go %s\n\nuse %s\n' "$gowork_ver" "$PARENT/CasaOS-Gateway" >"$DEVROOT/go.work"

PIDS=()
REQUIRED=()

stop() {
	local code=$1
	trap - INT TERM
	local pid
	for pid in "${PIDS[@]+"${PIDS[@]}"}"; do
		kill -TERM -- "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
	done
	for pid in "${PIDS[@]+"${PIDS[@]}"}"; do
		wait "$pid" 2>/dev/null || true
	done
	exit "$code"
}
trap 'stop 0' INT TERM

spawn() {
	local cwd=$1
	shift
	(
		cd "$cwd"
		export CASAOS_CONFIG_PATH="$DEVROOT/etc"
		exec setsid go run "$@"
	) &
	PIDS+=("$!")
}

alive() {
	kill -0 "$1" 2>/dev/null
}

wait_file() {
	local path=$1 pid=$2 label=$3
	local deadline=$((SECONDS + 120))
	while (( SECONDS < deadline )); do
		if ! alive "$pid"; then
			echo "$label exited" >&2
			stop 1
		fi
		if [[ -f "$path" ]]; then
			return 0
		fi
		sleep 0.5
	done
	if [[ "$label" == gateway ]]; then
		echo "gateway did not answer on 127.0.0.1:8081" >&2
	else
		echo "$label did not become ready" >&2
	fi
	stop 1
}

wait_ping() {
	local pid=$1
	local deadline=$((SECONDS + 120))
	while (( SECONDS < deadline )); do
		if ! alive "$pid"; then
			echo "gateway did not answer on 127.0.0.1:8081" >&2
			stop 1
		fi
		if [[ "$(http_code http://127.0.0.1:8081/ping)" == "200" ]]; then
			return 0
		fi
		sleep 0.5
	done
	echo "gateway did not answer on 127.0.0.1:8081" >&2
	stop 1
}

spawn "$DEVROOT/etc" "$PARENT/CasaOS-Gateway" -w "$DEVROOT/www"
REQUIRED+=("${PIDS[-1]}")
wait_ping "${PIDS[-1]}"
wait_file "$DEVROOT/run/management.url" "${PIDS[-1]}" gateway

spawn "$PARENT/CasaOS-MessageBus" . -c "$DEVROOT/etc/message-bus.conf"
REQUIRED+=("${PIDS[-1]}")
wait_file "$DEVROOT/run/message-bus.url" "${PIDS[-1]}" message-bus

spawn "$PARENT/CasaOS-UserService" . -c "$DEVROOT/etc/user-service.conf"
REQUIRED+=("${PIDS[-1]}")
wait_file "$DEVROOT/run/user-service.url" "${PIDS[-1]}" user-service

if [[ -d "$PARENT/CasaOS-LocalStorage" ]]; then
	spawn "$PARENT/CasaOS-LocalStorage" . -c "$DEVROOT/etc/local-storage.conf"
	ls_pid="${PIDS[-1]}"
	ls_ok=0
	deadline=$((SECONDS + 120))
	while (( SECONDS < deadline )); do
		if ! alive "$ls_pid"; then
			echo "warning: LocalStorage exited; skipping" >&2
			break
		fi
		code=$(http_code http://127.0.0.1:8081/v1/disks)
		if [[ "$code" != "404" && "$code" != "000" ]]; then
			ls_ok=1
			break
		fi
		sleep 0.5
	done
	if [[ "$ls_ok" -eq 0 ]] && alive "$ls_pid"; then
		echo "warning: LocalStorage did not register /v1/disks; continuing" >&2
	fi
else
	echo "warning: missing $PARENT/CasaOS-LocalStorage; skipping" >&2
fi

if [[ -d "$PARENT/CasaOS-AppManagement" ]]; then
	if [[ ! -S /var/run/docker.sock && -z "${DOCKER_HOST:-}" ]]; then
		echo "warning: no Docker socket; compose calls will fail" >&2
	fi
	spawn "$PARENT/CasaOS-AppManagement" . -c "$DEVROOT/etc/app-management.conf"
	REQUIRED+=("${PIDS[-1]}")
	wait_file "$DEVROOT/run/app-management.url" "${PIDS[-1]}" app-management
else
	echo "warning: missing $PARENT/CasaOS-AppManagement; skipping" >&2
fi

spawn "$CASAOS_REPO" . -c "$DEVROOT/etc/casaos.conf"
REQUIRED+=("${PIDS[-1]}")
wait_file "$DEVROOT/run/casaos.url" "${PIDS[-1]}" casaos

echo "stack on 127.0.0.1:8081"

while true; do
	for pid in "${REQUIRED[@]}"; do
		if ! alive "$pid"; then
			echo "a required process exited" >&2
			stop 1
		fi
	done
	sleep 1
done
