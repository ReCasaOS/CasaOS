# Accounts proof on a real stack

`run.sh` is the exit condition of the closed-by-default accounts (spec `docs/superpowers/specs/2026-11-14-comptes-et-visibilite-design.md`, « Condition de sortie »). It creates accounts, apps and a hidden app through the real API of a running stack, then checks what a `user` account with no right can and cannot reach. It prints one line per check (PASS, FAIL, SKIP) and exits non-zero if any check failed.

It sends every route of the services, writes included. Run it on a throwaway stack only. It has no default address: the Gateway's base URL is given every time, with `ACCOUNTS_PROOF_THROWAWAY=1` to say the stack is a throwaway one. It refuses a base URL that is not `localhost`, `127.0.0.1` or `[::1]` unless `ACCOUNTS_PROOF_ALLOW_REMOTE=1`.

For a release, set `ACCOUNTS_PROOF_RELEASE=1`: a SKIP then counts as a FAIL. c-direct, e and f must run, not skip, for a release.

## Run it

The stack is the real Gateway, MessageBus, UserService, core and AppManagement of `dev/stack.sh` (see `dev/README.md`), reached through the Gateway. On Windows the recipe is the Docker one: one Go container for `dev/stack.sh` with the Gateway forwarded to a host port. Install `curl` and `python3` (standard library only) on the machine that runs the script.

Checkouts the script reads (defaults are siblings of the CasaOS checkout; set these when you work from worktrees):

```bash
export ACCOUNTS_PROOF_GATEWAY_REPO=/path/to/CasaOS-Gateway   # route/testdata/{routes.txt,ui-calls.txt}, route/allowtable.go
export ACCOUNTS_PROOF_UI_REPO=/path/to/CasaOS-UI             # src/views/testdata/ui-calls.txt
```

See what it would send, without a stack:

```bash
bash dev/accounts-proof/run.sh --dry-run
```

Run it against the Gateway. The first account of a fresh stack is created through the register flow with these credentials; on a stack that already has accounts, give the admin's:

```bash
export ACCOUNTS_PROOF_ADMIN_USER=proofadmin
export ACCOUNTS_PROOF_ADMIN_PASSWORD='choose-a-password'
export ACCOUNTS_PROOF_THROWAWAY=1
bash dev/accounts-proof/run.sh http://localhost:18081
```

Check c, second half, calls AppManagement directly. Its address is in `app-management.url` under the stack's runtime folder (`.dev-runtime/run/`):

```bash
export ACCOUNTS_PROOF_AM_URL=$(cat .dev-runtime/run/app-management.url)
```

Check f stops UserService and restarts the Gateway. That needs them as separate containers, or three commands of your own:

```bash
export ACCOUNTS_PROOF_USERSERVICE_CONTAINER=casaos-user-service
export ACCOUNTS_PROOF_GATEWAY_CONTAINER=casaos-gateway
# or: ACCOUNTS_PROOF_STOP_USERSERVICE, ACCOUNTS_PROOF_START_USERSERVICE, ACCOUNTS_PROOF_RESTART_GATEWAY
```

`ACCOUNTS_PROOF_AFTER_SETUP` is a command run once both apps are installed. The dev stack's fake Engine writes no container logs, so `logs/apps` lists no app and d-poll SKIPs; on that stack, put one log file of each app on disk there (`/DATA/AppData/casaos/logs/<app>/web/<YYYY-MM-DD>.<12 hex>.log`, one line `<RFC3339> o text`).

Other variables: `ACCOUNTS_PROOF_USER_PASSWORD` (default: random), `ACCOUNTS_PROOF_TIMEOUT` (seconds per request, 10), `ACCOUNTS_PROOF_CACHE_WAIT` (seconds to let Common's 5 s role cache expire, 6), `ACCOUNTS_PROOF_COMPOSE_FILE` (a compose file with `@NAME@` where the app name goes, if the stack refuses `compose.template.yaml`).

The pure parts (route list, table matching, comparison of the two `ui-calls.txt` copies, comparison of two answers) have tests:

```bash
python -I dev/accounts-proof/test_routes.py
```

## Files

- `run.sh`: the proof.
- `routes.py`: the pure parts, called by `run.sh` as `python -I routes.py <command>`; `regen` rebuilds the Gateway's `routes.txt` by running the Gateway's own `gen-routes.py` (it needs `ACCOUNTS_PROOF_{GATEWAY,CORE,USERS,AM}_REPO`, optional `..._REF`, PyYAML and git). Nothing of `gen-routes.py` is copied.
- `extras.txt`: routes of services that `routes.txt` does not list (message bus, local storage, virtual machines, the Gateway's own).
- `admin-refusals.txt`: the GET routes a service itself refuses the admin when sent with no parameters, with the words of that refusal; a-admin accepts a 401 or 403 there only.
- `compose.template.yaml`: the app installed twice, as `proofshown` and `proofhidden`.
- `test_routes.py`: tests of `routes.py`.

## What each check proves

| Check | Proves |
|---|---|
| setup | the first account (register flow), a `user` account (`POST /v1/users`), two installed apps, the second hidden from the user through `PUT /v2/app_management/visibility/{app}`. The owner can still read the hidden app, so check c compares something that exists. |
| a-user | every route of `routes.txt` and `extras.txt` that no row of the allow table opens (rows of `route/allowtable.go` and of `ui-calls.txt`, plus the Gateway's own `/ping`) answers 403 to the user, whatever the method. Path parameters are written `x`. |
| a-admin | the GET routes of that same list are not 401 or 403 for the admin. Writes are not sent as admin (a few stop or reboot the box). |
| b | every `public` and `base` call of `ui-calls.txt`, with the fixture app in place of `immich`, reaches its service for the user with no right (never 401, 403, 5xx or silence); every `module:<right>` call answers 403 to that user. Public rows also with no token; every other row answers 401 with no token. Writes carry an empty JSON body, so a 400 counts as reached. |
| b-rights | the user is given each right alone in turn (`PUT /v1/users/{id}/rights`, then the 5 s of Common's cache): the `module:` calls of that right reach their service, those of the other rights answer 403. The rights are taken back at the end. |
| c-gw | for every per-app route of AppManagement (`compose`, `container`, `stats`, `git` with an app name), with and without `range` and `check`: the user gets the same status and the same body (the app's name apart) for the hidden app and for a name that does not exist, through the Gateway. A GET is first sent as the owner: one that answers the owner alike for both is skipped and counted, since it says nothing of existence. Writes are never sent as the owner. Routes the Gateway refuses answer 403 for both, which proves only the refusal. SKIP when no route told the two apart for the owner. |
| c-direct | the same on AppManagement's own address, so the service's refusals are compared on the routes the Gateway already closes. |
| d-bus | the user cannot open the message bus: websocket with the token in the header and in the query, and polling. |
| d-poll | none of the user's polling endpoints (app grid, app list, statistics, logs: apps, events, history) names the hidden app. Each endpoint must show the visible app: one that does not proves nothing and makes the check SKIP, naming it. |
| e | after an account is deleted and another created (a second later, since tokens are dated to the second): the new account has a number of its own (a number is never given twice), the old access token and refresh token answer 401, the visibility entry does not list the deleted number, and the new account sees both apps. |
| f | with UserService stopped and the Gateway restarted, a user token gets 503 (never 200); once UserService is back, the admin token gets 200. The admin's answer during the outage is printed, not judged. |
| g | the UI's `src/views/testdata/ui-calls.txt`, without its first `# COPY of` line, equals the Gateway's `route/testdata/ui-calls.txt`. |

## What it does not prove

- No browser: nothing here shows that the dashboard opens without a visible error for a `user`. It proves the calls the dashboard makes are answered, not that the screens render.
- Apps listen on their own ports. Hiding keeps them out of the box's answers; it does not close their ports.
- The message bus is refused to a user; the proof does not show what the bus carries to other subscribers.
- Write routes are not sent as admin, and write routes in check b carry an empty body: a handler's success path is not exercised.
- Routes that exist in a service but in neither `routes.txt` nor `extras.txt` are not tried. Regenerate `routes.txt` with `routes.py regen` when a service changes.
- `docker-compose` of the installed apps runs against the stack's record-keeping fake Engine, so no container serves the apps.

## Assumptions made without a running stack

- Response shapes: `data.token.access_token`, `data.token.refresh_token`, `data.user.id` for sign-in, `data.initialized` and `data.key` for the status, `data` as a list of `{id, username}` for `GET /v1/users`, `data.hidden_for` for the visibility. Read from the services' code.
- AppManagement installs `POST /v2/app_management/compose` with a YAML body and lists the app in `GET /v2/app_management/compose` under its name, within 60 s. `compose.template.yaml` is a minimal compose with `x-casaos`; the stack may want more.
- AppManagement, reached directly, accepts the user's access token in `Authorization`, as behind the Gateway.
- The Gateway takes the token bare or as `Bearer` and, for a websocket, as the `token` query parameter (its tests say so).
- Visibility entries survive a failed run. Re-running creates new account names, and the script deletes the accounts and apps it made at the end of a run that got that far.

## Runs

2026-11-15, dev stack in one `golang:1.26` container, services built from the `feat/accounts-visibility` worktrees: release mode, no FAIL. What a run needs from the stack, beyond `dev/stack.sh`:

- A fresh `.dev-runtime` each run: the fake Engine fails the apps' uninstall (its image list is an object, Compose wants a list), so a second run installs over a broken record.
- `/DATA/AppData` writable (AppManagement's logs store; without it every `logs` call is 503) and an rclone daemon on `/var/run/rclone/rclone.sock` (backup destinations; without it 500).
- Something registered on `/v2/virt_management`: with no service on the prefix the dashboard's files answer 404, not the Gateway's 403.
- UserService gives an address 5 sign-ins, then one a minute: the script waits on a 429; signing in by hand before a run eats into it.
