#!/usr/bin/env bash
# Proof of the closed-by-default accounts on a real stack: see README.md.
#   ACCOUNTS_PROOF_THROWAWAY=1 run.sh GATEWAY_BASE_URL
#   run.sh --dry-run [GATEWAY_BASE_URL]
# --dry-run prints every request it would send and sends none. ACCOUNTS_PROOF_RELEASE=1 counts a SKIP as a FAIL.
set -uo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
DRY=0
if [[ ${1:-} == --dry-run ]]; then
	DRY=1
	shift
fi
# no default: the proof writes to the stack it is given, so that stack is named every time
BASE=${1:-${ACCOUNTS_PROOF_BASE_URL:-}}
if [[ -z $BASE ]]; then
	if ((DRY)); then
		BASE=http://localhost:80
	else
		echo "give the Gateway's base URL of a throwaway stack: run.sh http://localhost:PORT" >&2
		exit 2
	fi
fi
BASE=${BASE%/}
if ((!DRY)) && [[ ${ACCOUNTS_PROOF_THROWAWAY:-} != 1 ]]; then
	echo "set ACCOUNTS_PROOF_THROWAWAY=1 to say $BASE is a throwaway stack: the proof creates and deletes accounts and apps and sends every route" >&2
	exit 2
fi
RELEASE=${ACCOUNTS_PROOF_RELEASE:-0}

PARENT=$(cd "$HERE/../../.." && pwd)
GATEWAY_REPO=${ACCOUNTS_PROOF_GATEWAY_REPO:-$PARENT/CasaOS-Gateway}
UI_REPO=${ACCOUNTS_PROOF_UI_REPO:-$PARENT/CasaOS-UI}
ADMIN_USER=${ACCOUNTS_PROOF_ADMIN_USER:-proofadmin}
ADMIN_PASSWORD=${ACCOUNTS_PROOF_ADMIN_PASSWORD:-}
TIMEOUT=${ACCOUNTS_PROOF_TIMEOUT:-10}
CACHE_WAIT=${ACCOUNTS_PROOF_CACHE_WAIT:-6} # the role cache of Common lasts 5 s
AM_URL=${ACCOUNTS_PROOF_AM_URL:-}          # app management's own address, for the direct half of check c
PY=python3
command -v python3 >/dev/null 2>&1 || PY=python
py() { "$PY" -I "$HERE/routes.py" "$@"; }

SHOWN=proofshown
HIDDEN=proofhidden
UNKNOWN=proofnone
STAMP=$(date +%s)
USER_NAME=proofuser$STAMP
USER_PASSWORD=${ACCOUNTS_PROOF_USER_PASSWORD:-$("$PY" -I -c 'import secrets;print(secrets.token_urlsafe(12))')}

GW=$GATEWAY_REPO/route
for f in "$GW/testdata/routes.txt" "$GW/testdata/ui-calls.txt" "$GW/allowtable.go" "$HERE/routes.py"; do
	[[ -f $f ]] || {
		echo "missing $f (set ACCOUNTS_PROOF_GATEWAY_REPO to the Gateway checkout)" >&2
		exit 2
	}
done
case $BASE in
http://localhost* | http://127.0.0.1* | "http://[::1]"*) ;;
*)
	[[ ${ACCOUNTS_PROOF_ALLOW_REMOTE:-} == 1 ]] || {
		echo "refusing $BASE: the proof creates accounts and apps and sends every route; use a throwaway local stack, or set ACCOUNTS_PROOF_ALLOW_REMOTE=1" >&2
		exit 2
	}
	;;
esac
if ((!DRY)) && [[ -z $ADMIN_PASSWORD ]]; then
	echo "set ACCOUNTS_PROOF_ADMIN_PASSWORD" >&2
	exit 2
fi
((DRY)) && ADMIN_PASSWORD=dry-run-password

WORK=$(mktemp -d)
BODY=$WORK/body
trap 'rm -rf "$WORK"' EXIT
cat "$GW/testdata/routes.txt" "$HERE/extras.txt" >"$WORK/routes.all"

# ---- requests -------------------------------------------------------------------------------

REQ_CODE=000
EXTRA_H=()
# req METHOD TARGET [TOKEN [DATA [CONTENT_TYPE]]]: TARGET is a path on BASE or a full URL. The
# status lands in REQ_CODE (000 for no answer) and the body in $BODY.
req() {
	local method=$1 target=$2 token=${3:-} data=${4-} ctype=${5:-application/json} url h
	case $target in http*) url=$target ;; *) url=$BASE$target ;; esac
	local args=(-sS -o "$BODY" -w '%{http_code}' --max-time "$TIMEOUT" -X "$method")
	[[ -n $token ]] && args+=(-H "Authorization: $token")
	[[ -n $data ]] && args+=(-H "Content-Type: $ctype" --data-binary "$data")
	for h in ${EXTRA_H[@]+"${EXTRA_H[@]}"}; do args+=(-H "$h"); done
	if ((DRY)); then
		printf 'DRY %-6s %s%s%s\n' "$method" "$url" "${token:+  [token]}" "${data:+  [body]}"
		REQ_CODE=000
		: >"$BODY"
		return 0
	fi
	REQ_CODE=$(curl "${args[@]}" "$url" 2>/dev/null) || true
	[[ -n $REQ_CODE ]] || REQ_CODE=000
}
# a write with no meaning of its own: the route is reached, the service refuses the empty body
reqw() { # METHOD TARGET [TOKEN]
	case $1 in
	POST | PUT | PATCH | DELETE) req "$1" "$2" "${3:-}" '{}' ;;
	*) req "$1" "$2" "${3:-}" ;;
	esac
}
jget() { py jget "$1" <"$BODY"; }
sleep_s() { ((DRY)) || sleep "$1"; }
# a call that must be let through is not: refused, failed on the server, or not answered
bad_status() { [[ $1 == 000 || $1 == 401 || $1 == 403 || $1 -ge 500 ]]; }

# ---- results --------------------------------------------------------------------------------

RES=()
FAILS=0
record() { # ID STATUS REASON
	local status=$2
	# for a release, a check that could not run proves nothing: it fails
	[[ $status == SKIP && $RELEASE == 1 ]] && status=FAIL && set -- "$1" FAIL "skipped in release mode: $3"
	((DRY)) && status=DRY
	[[ $status == FAIL ]] && FAILS=$((FAILS + 1))
	RES+=("$1|$status|$3")
}
# verdict ID TOTAL OKTEXT [FAILURE...]: PASS when there is no failure
verdict() {
	local id=$1 total=$2 ok=$3
	shift 3
	if (($# == 0)); then record "$id" PASS "$ok"; else
		# the report line keeps three; every failure is printed here, so a run can be read without replaying it
		printf "$id FAIL: %s\n" "$@"
		record "$id" FAIL "$# of $total: ${*:1:3}"
	fi
}
report() {
	local line a b c
	echo
	printf '%-8s %-6s %s\n' CHECK STATUS REASON
	for line in "${RES[@]}"; do
		IFS='|' read -r a b c <<<"$line"
		printf '%-8s %-6s %s\n' "$a" "$b" "$c"
	done
	echo
	if ((DRY)); then echo "dry run: nothing was sent"; elif ((FAILS)); then echo "$FAILS check(s) FAILED"; else echo "no check failed"; fi
}

# ---- setup: accounts, two apps, one hidden --------------------------------------------------

ATOK=
UTOK=
UREFRESH=
UID1=2
L_ACCESS=
L_REFRESH=
L_ID=
NEW_ID=
login() { # NAME PASSWORD: sets L_ACCESS, L_REFRESH, L_ID
	if ((DRY)); then
		req POST /v1/users/login '' '{"username":"…","password":"…"}'
		L_ACCESS=dry-token L_REFRESH=dry-refresh L_ID=1
		return 0
	fi
	local try
	for try in 1 2 3; do
		req POST /v1/users/login '' "$(py jobj username "$1" password "$2")"
		# user-service gives an address 5 sign-ins, then one a minute; the proof signs in more than that
		[[ $REQ_CODE == 429 ]] || break
		echo "sign-in of $1 answered 429, waiting a minute" >&2
		sleep 61
	done
	[[ $REQ_CODE == 200 ]] || return 1
	L_ACCESS=$(jget data.token.access_token)
	L_REFRESH=$(jget data.token.refresh_token)
	L_ID=$(jget data.user.id)
	[[ -n $L_ACCESS ]]
}
make_user() { # NAME PASSWORD: sets NEW_ID
	req POST /v1/users "$ATOK" "$(py jobj username "$1" password "$2")"
	if ((DRY)); then
		NEW_ID=9
		return 0
	fi
	[[ $REQ_CODE == 200 ]] || return 1
	req GET /v1/users "$ATOK"
	NEW_ID=$(py jfind data username "$1" id <"$BODY")
	[[ -n $NEW_ID ]]
}
install_app() { # NAME
	local compose=${ACCOUNTS_PROOF_COMPOSE_FILE:-$HERE/compose.template.yaml} i
	req POST '/v2/app_management/compose?check_port_conflict=false' "$ATOK" "$(sed "s/@NAME@/$1/g" "$compose")" application/yaml
	((DRY)) && return 0
	[[ $REQ_CODE == 200 ]] || {
		echo "install of $1 answered $REQ_CODE: $(head -c 300 "$BODY")" >&2
		return 1
	}
	for i in $(seq 1 30); do
		req GET /v2/app_management/compose "$ATOK"
		grep -q "\"$1\"" "$BODY" && return 0
		sleep 2
	done
	echo "$1 is not in the app list after 60 s" >&2
	return 1
}
setup() {
	local init key
	req GET /v1/users/status
	init=$(jget data.initialized)
	key=$(jget data.key)
	if [[ $init != True && -n $key ]]; then
		req POST /v1/users/register '' "$(py jobj username "$ADMIN_USER" password "$ADMIN_PASSWORD" key "$key")"
		[[ $REQ_CODE == 200 ]] || {
			echo "register of the first account answered $REQ_CODE" >&2
			return 1
		}
	elif [[ $init != True ]] && ((!DRY)); then
		echo "the box has no account and gives no key: create the first one on the machine (first_admin is local), then rerun" >&2
		return 1
	fi
	login "$ADMIN_USER" "$ADMIN_PASSWORD" || {
		echo "admin sign-in answered $REQ_CODE" >&2
		return 1
	}
	ATOK=$L_ACCESS
	make_user "$USER_NAME" "$USER_PASSWORD" || {
		echo "creating the user answered $REQ_CODE" >&2
		return 1
	}
	UID1=$NEW_ID
	login "$USER_NAME" "$USER_PASSWORD" || {
		echo "user sign-in answered $REQ_CODE" >&2
		return 1
	}
	UTOK=$L_ACCESS
	UREFRESH=$L_REFRESH
	install_app "$SHOWN" && install_app "$HIDDEN" || return 1
	req PUT "/v2/app_management/visibility/$HIDDEN" "$ATOK" "{\"hidden_for\":[${UID1//[^0-9]/}]}"
	((DRY)) || [[ $REQ_CODE == 200 ]] || {
		echo "hiding $HIDDEN answered $REQ_CODE" >&2
		return 1
	}
	req GET "/v2/app_management/compose/$HIDDEN" "$ATOK"
	((DRY)) || [[ $REQ_CODE == 200 ]] || {
		echo "the owner cannot read $HIDDEN ($REQ_CODE): check c would compare nothing" >&2
		return 1
	}
}

# ---- a: the table ---------------------------------------------------------------------------

check_a() {
	py plan "$WORK/routes.all" "$GW/allowtable.go" "$GW/testdata/ui-calls.txt" >"$WORK/plan.tsv"
	local m p e n=0 total=0 skipped=0 timeouts=0 service_refused=0
	local bad_user=() bad_admin=()
	while IFS=$'\t' read -r m p e; do
		[[ $e == denied ]] || continue
		total=$((total + 1))
		reqw "$m" "$p" "$UTOK"
		[[ $REQ_CODE == 403 ]] || bad_user+=("$m $p -> $REQ_CODE")
		if [[ $m == GET ]]; then
			n=$((n + 1))
			req GET "$p" "$ATOK"
			[[ $REQ_CODE == 000 ]] && timeouts=$((timeouts + 1))
			if [[ $REQ_CODE == 401 || $REQ_CODE == 403 ]]; then
				# a service's own refusal of a request with no meaning (admin-refusals.txt), with its words
				local want
				want=$(awk -F'\t' -v p="$p" '$1 == "GET " p {print $2; exit}' "$HERE/admin-refusals.txt")
				if [[ -n $want ]] && grep -qF "$want" "$BODY"; then
					service_refused=$((service_refused + 1))
				else
					bad_admin+=("GET $p -> $REQ_CODE $(head -c 120 "$BODY")")
				fi
			fi
		else
			skipped=$((skipped + 1))
		fi
	done <"$WORK/plan.tsv"
	verdict a-user "$total" "$total routes outside the table answer 403 to a user with no right" ${bad_user[@]+"${bad_user[@]}"}
	verdict a-admin "$n" "$n GET routes outside the table are not refused to the admin by the Gateway ($service_refused refused by the service itself, as admin-refusals.txt says; $timeouts without an answer in ${TIMEOUT}s; the $skipped write routes are not sent as admin)" ${bad_admin[@]+"${bad_admin[@]}"}
}

# ---- b: what the dashboard calls ------------------------------------------------------------

check_b() {
	py uicalls "$GW/testdata/ui-calls.txt" "$SHOWN" >"$WORK/ui.tsv"
	local m p k n=0 data right
	local bad=()
	while IFS=$'	' read -r m p k; do
		n=$((n + 2)) # with the token, and without
		data='{}'
		case "$m $p" in
		"POST /v1/users/login") data=$(py jobj username "$USER_NAME" password "$USER_PASSWORD") ;;
		"POST /v1/users/refresh") data=$(py jobj refresh_token "$UREFRESH") ;;
		esac
		if [[ $m == GET ]]; then req GET "$p" "$UTOK"; else req "$m" "$p" "$UTOK" "$data"; fi
		case $k in
		public | base) bad_status "$REQ_CODE" && bad+=("$m $p -> $REQ_CODE") ;;
		# a module's line opens only with its right: this user has none
		*) [[ $REQ_CODE == 403 ]] || bad+=("$m $p ($k) for a user with no right -> $REQ_CODE, wanted 403") ;;
		esac
		if [[ $k == public ]]; then
			if [[ $m == GET ]]; then req GET "$p"; else req "$m" "$p" '' "$data"; fi
			bad_status "$REQ_CODE" && bad+=("$m $p with no token -> $REQ_CODE")
		else
			reqw "$m" "$p"
			[[ $REQ_CODE == 401 ]] || bad+=("$m $p with no token -> $REQ_CODE, wanted 401")
		fi
	done <"$WORK/ui.tsv"
	verdict b "$n" "$n requests of the dashboard's calls: public and base lines reach their service for the user (never 401, 403, 5xx or silence), module lines answer 403 to a user with no right; all but public answer 401 with no token" ${bad[@]+"${bad[@]}"}

	# each right alone: the lines of its module reach their service, those of the other modules stay refused
	bad=()
	n=0
	for right in $(cut -f3 "$WORK/ui.tsv" | grep '^module:' | sort -u | sed 's/^module://'); do
		req PUT "/v1/users/$UID1/rights" "$ATOK" "{\"rights\":[\"$right\"]}"
		((DRY)) || [[ $REQ_CODE == 200 ]] || {
			bad+=("granting $right answered $REQ_CODE")
			continue
		}
		sleep_s "$CACHE_WAIT"
		while IFS=$'	' read -r m p k; do
			[[ $k == module:* ]] || continue
			n=$((n + 1))
			reqw "$m" "$p" "$UTOK"
			if [[ $k == "module:$right" ]]; then
				bad_status "$REQ_CODE" && bad+=("$m $p with $right -> $REQ_CODE")
			else
				[[ $REQ_CODE == 403 ]] || bad+=("$m $p ($k) with $right alone -> $REQ_CODE, wanted 403")
			fi
		done <"$WORK/ui.tsv"
	done
	req PUT "/v1/users/$UID1/rights" "$ATOK" '{"rights":[]}'
	((DRY)) || [[ $REQ_CODE == 200 ]] || bad+=("taking the rights back answered $REQ_CODE")
	sleep_s "$CACHE_WAIT"
	verdict b-rights "$n" "with each right alone, $n module calls: those of the right reach their service, the others answer 403" ${bad[@]+"${bad[@]}"}
}

# ---- c: hidden is unknown -------------------------------------------------------------------

compare_app_routes() { # LABEL PREFIX
	local label=$1 prefix=$2 mh ph mu pu q ch cu n=0 refused=0 alike=0
	local bad=()
	py approutes "$WORK/routes.all" "$HIDDEN" >"$WORK/ah.tsv"
	py approutes "$WORK/routes.all" "$UNKNOWN" >"$WORK/au.tsv"
	while IFS=$'	' read -r mh ph <&3 && IFS=$'	' read -r mu pu <&4; do
		for q in '' '?range=24h' '?check=true'; do
			# a read, the owner first: a route that answers the owner alike for an app that exists and one that does
			# not tells nothing about existence, so the user's answers on it prove nothing either. Never a write as
			# the owner: it would change or remove the hidden app.
			if [[ $mh == GET ]]; then
				req GET "$prefix$ph$q" "$ATOK"
				ch=$REQ_CODE
				cp "$BODY" "$WORK/h.body"
				req GET "$prefix$pu$q" "$ATOK"
				cu=$REQ_CODE
				if ! ((DRY)) && py same "$ch" "$WORK/h.body" "$HIDDEN" "$cu" "$BODY" "$UNKNOWN"; then
					alike=$((alike + 1))
					continue
				fi
			fi
			reqw "$mh" "$prefix$ph$q" "$UTOK"
			ch=$REQ_CODE
			cp "$BODY" "$WORK/h.body"
			reqw "$mu" "$prefix$pu$q" "$UTOK"
			cu=$REQ_CODE
			cp "$BODY" "$WORK/u.body"
			n=$((n + 1))
			[[ $ch == 403 && $cu == 403 ]] && refused=$((refused + 1))
			if [[ $ch == 000 || $ch -ge 500 ]] || ! py same "$ch" "$WORK/h.body" "$HIDDEN" "$cu" "$WORK/u.body" "$UNKNOWN"; then
				bad+=("$mh $ph$q -> $ch vs $cu")
			fi
		done
	done 3<"$WORK/ah.tsv" 4<"$WORK/au.tsv"
	if ((!DRY && n == 0)) && ((${#bad[@]} == 0)); then
		record "$label" SKIP "every route answered the owner alike for the hidden app and an unknown name: nothing was compared"
		return
	fi
	verdict "$label" "$n" "$n requests where the owner tells the hidden app from an unknown name: the user cannot ($refused refused by the Gateway for both, which proves the refusal only; $alike skipped, alike for the owner too)" ${bad[@]+"${bad[@]}"}
}
check_c() {
	compare_app_routes c-gw ''
	if [[ -n $AM_URL ]]; then
		compare_app_routes c-direct "${AM_URL%/}"
	else
		record c-direct SKIP "set ACCOUNTS_PROOF_AM_URL (the address in app-management.url) to compare on the routes the Gateway refuses"
	fi
}

# ---- d: no events of a hidden app -----------------------------------------------------------

check_d() {
	local bad=() p
	EXTRA_H=('Connection: Upgrade' 'Upgrade: websocket' 'Sec-WebSocket-Version: 13' 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==')
	req GET '/v2/message_bus/socket.io/?EIO=4&transport=websocket' "$UTOK"
	[[ $REQ_CODE == 403 ]] || bad+=("websocket, token in the header -> $REQ_CODE")
	req GET "/v2/message_bus/socket.io/?EIO=4&transport=websocket&token=$UTOK"
	[[ $REQ_CODE == 403 ]] || bad+=("websocket, token in the query -> $REQ_CODE")
	EXTRA_H=()
	req GET '/v2/message_bus/socket.io/?EIO=4&transport=polling' "$UTOK"
	[[ $REQ_CODE == 403 ]] || bad+=("polling transport -> $REQ_CODE")
	verdict d-bus 3 "the message bus is refused to the user (websocket with the token in the header and in the query, and polling)" ${bad[@]+"${bad[@]}"}

	bad=()
	local unproven=()
	for p in /v2/app_management/web/appgrid /v2/app_management/compose /v2/app_management/stats /v2/app_management/logs/apps /v2/app_management/logs/events /v2/app_management/logs; do
		req GET "$p" "$UTOK"
		[[ $REQ_CODE == 200 ]] || {
			bad+=("GET $p -> $REQ_CODE")
			continue
		}
		grep -q "$HIDDEN" "$BODY" && bad+=("GET $p names $HIDDEN")
		# an answer that does not show the visible app either proves nothing for this endpoint
		((DRY)) || grep -q "$SHOWN" "$BODY" || unproven+=("$p")
	done
	if ((${#bad[@]} == 0 && ${#unproven[@]} > 0)); then
		record d-poll SKIP "no endpoint names $HIDDEN, but these do not show the visible $SHOWN either, so they prove nothing: ${unproven[*]}"
	else
		verdict d-poll 6 "no polling endpoint of the user names the hidden app, and each shows the visible one" ${bad[@]+"${bad[@]}"}
	fi
}

# ---- e: a deleted account's number ----------------------------------------------------------

check_e() {
	local bad=() id1 id2 tok1 ref1 tok2 p entries note
	make_user "${USER_NAME}a" "$USER_PASSWORD" || {
		record e FAIL "creating the account to delete answered $REQ_CODE"
		return
	}
	id1=$NEW_ID
	login "${USER_NAME}a" "$USER_PASSWORD" || {
		record e FAIL "sign-in of the account to delete answered $REQ_CODE"
		return
	}
	tok1=$L_ACCESS
	ref1=$L_REFRESH
	# tokens are dated to the second: the next account is made in a later second than this sign-in
	sleep_s 1.1
	req PUT "/v2/app_management/visibility/$HIDDEN" "$ATOK" "{\"hidden_for\":[${UID1//[^0-9]/},${id1//[^0-9]/}]}"
	req DELETE "/v1/users/$id1" "$ATOK"
	((DRY)) || [[ $REQ_CODE == 200 ]] || {
		record e FAIL "deleting the account answered $REQ_CODE"
		return
	}
	make_user "${USER_NAME}b" "$USER_PASSWORD" || {
		record e FAIL "creating the next account answered $REQ_CODE"
		return
	}
	id2=$NEW_ID
	echo "$id2" >>"$WORK/cleanup_users"
	login "${USER_NAME}b" "$USER_PASSWORD" || {
		record e FAIL "sign-in of the next account answered $REQ_CODE"
		return
	}
	tok2=$L_ACCESS
	sleep_s "$CACHE_WAIT"
	for p in /v1/users/current /v2/app_management/web/appgrid; do
		req GET "$p" "$tok1"
		[[ $REQ_CODE == 401 ]] || bad+=("old access token on $p -> $REQ_CODE")
	done
	req POST /v1/users/refresh '' "$(py jobj refresh_token "$ref1")"
	[[ $REQ_CODE == 401 ]] || bad+=("old refresh token -> $REQ_CODE")
	req GET "/v2/app_management/visibility/$HIDDEN" "$ATOK"
	entries=$(jget data.hidden_for)
	entries=" ${entries//[^0-9]/ } "
	[[ $entries == *" $id2 "* || $entries == *" $id1 "* ]] && bad+=("the entry of $HIDDEN still lists the deleted number:$entries")
	req GET /v2/app_management/web/appgrid "$tok2"
	if ! ((DRY)); then
		grep -q "$SHOWN" "$BODY" || bad+=("the new account does not see $SHOWN")
		grep -q "$HIDDEN" "$BODY" || bad+=("the new account does not see $HIDDEN (an inherited entry)")
	fi
	# an account number is never given twice: the other services know an account by it alone
	((DRY)) || [[ $id2 != "$id1" ]] || bad+=("the new account got the deleted account's number $id1")
	verdict e 6 "the new account has a number of its own; old access and refresh tokens answer 401; the visibility entry and the new account's view carry nothing of the old one" ${bad[@]+"${bad[@]}"}
}

# ---- f: UserService down, Gateway restarted -------------------------------------------------

run_hook() {
	if ((DRY)); then
		echo "DRY hook: $1"
		return 0
	fi
	bash -c "$1" >/dev/null 2>&1
}
wait_for() { # PATH: until the Gateway answers 200 on it
	local i
	for i in $(seq 1 45); do
		req GET "$1"
		[[ $REQ_CODE == 200 ]] && return 0
		sleep_s 2
	done
	((DRY))
}
check_f() {
	local stop=${ACCOUNTS_PROOF_STOP_USERSERVICE:-} start=${ACCOUNTS_PROOF_START_USERSERVICE:-} restart=${ACCOUNTS_PROOF_RESTART_GATEWAY:-}
	local us=${ACCOUNTS_PROOF_USERSERVICE_CONTAINER:-} gw=${ACCOUNTS_PROOF_GATEWAY_CONTAINER:-}
	if [[ -z $stop && -n $us ]]; then
		stop="docker stop $us"
		start="docker start $us"
	fi
	[[ -z $restart && -n $gw ]] && restart="docker restart $gw"
	if [[ -z $stop || -z $start || -z $restart ]]; then
		record f SKIP "set ACCOUNTS_PROOF_USERSERVICE_CONTAINER and ACCOUNTS_PROOF_GATEWAY_CONTAINER (or the three hook commands)"
		return
	fi
	local bad=() admin_code
	run_hook "$stop" || {
		record f FAIL "could not stop UserService"
		return
	}
	run_hook "$restart" || {
		run_hook "$start"
		record f FAIL "could not restart the Gateway"
		return
	}
	wait_for /ping || bad+=("the Gateway does not answer /ping after its restart")
	req GET /v2/app_management/web/appgrid "$UTOK"
	[[ $REQ_CODE == 503 ]] || bad+=("user token with UserService down -> $REQ_CODE, wanted 503")
	req GET /v2/app_management/web/appgrid "$ATOK"
	admin_code=$REQ_CODE
	run_hook "$start" || bad+=("could not start UserService again")
	wait_for /v1/users/status || bad+=("UserService does not answer through the Gateway after its restart")
	req GET /v1/users/current "$ATOK"
	[[ $REQ_CODE == 200 ]] || bad+=("admin token after UserService is back -> $REQ_CODE")
	verdict f 1 "user token 503 while UserService is down after a Gateway restart (the admin token got $admin_code then, informative); admin token 200 once it is back" ${bad[@]+"${bad[@]}"}
}

# ---- g: the UI's copy -----------------------------------------------------------------------

check_g() {
	local copy=$UI_REPO/src/views/testdata/ui-calls.txt
	if [[ ! -f $copy ]]; then
		record g SKIP "no $copy (set ACCOUNTS_PROOF_UI_REPO to the CasaOS-UI checkout)"
		return
	fi
	if py compare-ui "$copy" "$GW/testdata/ui-calls.txt"; then
		record g PASS "the UI's copy equals the Gateway's ui-calls.txt apart from its first line"
	else
		record g FAIL "the UI's src/views/testdata/ui-calls.txt differs from the Gateway's route/testdata/ui-calls.txt"
	fi
}

cleanup() {
	((DRY)) && return
	local id app
	for id in "$UID1" $(cat "$WORK/cleanup_users" 2>/dev/null); do
		[[ -n $id && -n $ATOK ]] && req DELETE "/v1/users/$id" "$ATOK"
	done
	for app in "$SHOWN" "$HIDDEN"; do
		[[ -n $ATOK ]] && req DELETE "/v2/app_management/compose/$app" "$ATOK"
		[[ -n $ATOK ]] && req DELETE "/v2/app_management/visibility/$app" "$ATOK"
	done
	return 0
}

check_g
if setup; then
	# a command of the stack's own, with both apps installed: a fake Engine writes no container logs,
	# so one can put a log of each app on disk here for d-poll's logs/apps to list
	[[ -n ${ACCOUNTS_PROOF_AFTER_SETUP:-} ]] && { run_hook "$ACCOUNTS_PROOF_AFTER_SETUP" || echo "ACCOUNTS_PROOF_AFTER_SETUP failed" >&2; }
	check_a
	check_b
	check_c
	check_d
	check_e
	check_f
	cleanup
else
	record setup FAIL "see the message above; checks a to f did not run"
fi
report
((FAILS == 0))
