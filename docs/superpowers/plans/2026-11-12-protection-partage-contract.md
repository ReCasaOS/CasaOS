# Shared-app protection — core ↔ Gateway contract

Spec: `docs/superpowers/specs/2026-11-12-protection-partage-design.md`. This file is the one place the three plans (`…-gateway.md`, `…-coeur.md`, `…-ui.md`) take names, JSON shapes and constants from. A plan that needs something not written here adds it here first.

## Constants

| Name | Value |
|---|---|
| Entry page path (on every shared host) | `/.recasaos/acces` |
| Session cookie name | `recasaos_access` |
| Session lifetime | 30 days from the last refresh; refreshed when older than 24 h |
| Code alphabet | `ABCDEFGHJKMNPQRSTUVWXYZ23456789` (31 symbols, no `0 O 1 I L`) |
| Code length | 12 symbols, shown as `XXXX-XXXX-XXXX` |
| Code normalisation | uppercase, drop every character outside the alphabet |
| Ban key | IPv4 address, or the IPv6 `/64` written `<prefix>::/64`; IPv4-mapped IPv6 is IPv4 |
| Ban durations (strike 1, 2, ≥3) | 1 h, 24 h, 7 days; strikes forgotten 30 days after the last ban ends |
| Journal | 7 days, at most 10 000 lines; a line repeating the previous line of the same key, app, reason, rule and action within 60 s increments its `count` |
| WAF request body inspected | first 64 KiB (`SecRequestBodyLimit 65536`, `SecRequestBodyLimitAction ProcessPartial`); `SecResponseBodyAccess Off` (1 MiB measured at ~5 s of CPU per form body: too costly) |
| WAF extra allowed methods | `PUT DELETE PATCH PROPFIND PROPPATCH MKCOL COPY MOVE LOCK UNLOCK REPORT` |
| CRS rules off for every app | 920420 (content-type allow list), 901340 (binary bodies parsed as forms); 200002 replaced by a rule that refuses only malformed bodies, not cut ones |
| Geo files | `/var/lib/casaos/geo/dbip-country-lite.mmdb`, `/var/lib/casaos/geo/dbip-asn-lite.mmdb` |
| Gateway persistent state | `<DataPath>/guard-state.json`, `DataPath` default `/var/lib/casaos/gateway`, mode 0600 |
| Gateway pushed config | `<RuntimePath>/guard.json` (tmpfs, next to `hosts.json`), mode 0600 |
| Core config | `/var/lib/casaos/fleet/share-guard.json`, mode 0600 |

Default policy values (core, `DefaultPolicy()`): `waf: "off"`, `rules_off: []`, `allow: []`, `block: []`, `countries: {mode: "none", list: []}`, `rate_per_min: 600`, `ban: {waf_hits: 10, waf_window_s: 600, scan_404: 100, scan_window_s: 60, rate_strikes: 3, rate_window_s: 600, code_fails: 5, code_window_s: 600}`.

Session token: `v1.<person id>.<gen>.<issued unix seconds>.<sig>`, `sig` = unpadded base64url of HMAC-SHA256(secret, everything before the last dot). Cookie attributes: `Domain=<host without its first label>` (for `jellyfin.mybox.box.recasaos.org`: `mybox.box.recasaos.org`), `Path=/`, `Secure`, `HttpOnly`, `SameSite=Lax`, `Max-Age=2592000`.

`code_hash` = lowercase hex of HMAC-SHA256(secret, normalised code). Test vector: secret = the 32 bytes `0x00…0x1f`, code `abcd-efgh-jkmn` → `ABCDEFGHJKMN` → `499b46abdc484007465c1451c284ae4485de2006d52fd01cc6fa0f0c6eed12c2`.

## Gateway internal API

All routes under the Gateway management server's `/v1/gateway`, same `Authorization: Internal <secret>` check as `/v1/gateway/hosts`. Errors use the existing `model.Result` envelope with `400` (bad body) or `500`.

### `GET /v1/gateway/guard` → 200

```json
{
  "version": 1,
  "configured": true,
  "geo": { "country": true, "asn": true },
  "people_last_used": { "p_7f3a": "2026-11-12T10:20:00Z" },
  "waves": [ { "app": "jellyfin", "bans": 23 } ]
}
```

- A Gateway without this route answers 404: the core reads that as "cannot protect" (`gateway_unsupported`).
- `configured`: a config has been received since `guard.json` was last empty.
- `geo.*`: the file is loaded and readable.
- `waves`: apps with more than 20 bans started in the last 10 minutes.

### `PUT /v1/gateway/guard` → 204

Replaces the whole config atomically, writes `guard.json`. Body (`GuardConfig`):

```json
{
  "secret": "<base64 std, 32 bytes>",
  "protection": true,
  "apps": {
    "jellyfin.mybox.box.recasaos.org": {
      "app": "jellyfin",
      "access": "code",
      "policy": {
        "waf": "observe",
        "rules_off": [942100],
        "allow": ["192.0.2.0/24"],
        "block": ["198.51.100.7"],
        "countries": { "mode": "block", "list": ["XX"] },
        "rate_per_min": 600,
        "ban": { "waf_hits": 10, "waf_window_s": 600, "scan_404": 100, "scan_window_s": 60,
                 "rate_strikes": 3, "rate_window_s": 600, "code_fails": 5, "code_window_s": 600 }
      }
    }
  },
  "people": [ { "id": "p_7f3a", "gen": 2, "code_hash": "<64 hex>", "apps": ["jellyfin"] } ]
}
```

- `apps` keys are hosts exactly as registered with `PUT /v1/gateway/hosts`. `policy` is the **effective** policy (box merged with the app's override by the core); the Gateway never merges.
- `access`: `"open"` or `"code"`. `waf`: `"off"`, `"observe"`, `"block"`. `countries.mode`: `"none"`, `"block"`, `"allow"`; `list` holds ISO 3166-1 alpha-2 codes, uppercase.
- `allow`/`block`: IPs or CIDRs, IPv4 or IPv6.
- With `protection: false` the Gateway applies only the access gate (`access: "code"`); every other filter is skipped and nothing is counted.
- Validation failures → 400 naming the field; the previous config stays.

### `GET /v1/gateway/guard/events?app=<app>&reason=<reason>&limit=<n>` → 200

Newest first, `limit` default 200, max 1000. `[]` when empty.

```json
[ { "time": "2026-11-12T10:21:03Z", "ip": "203.0.113.9", "key": "203.0.113.9",
    "country": "FR", "asn": 3215, "as_org": "Orange",
    "app": "jellyfin", "path": "/x", "reason": "waf", "rule": 942100,
    "detail": "SQL Injection Attack Detected via libinjection",
    "action": "blocked", "count": 1 } ]
```

`reason`: `banned`, `blocklist`, `country`, `rate`, `waf`, `scan`, `code`. `action`: `blocked`, `banned` (this event started a ban), `observed` (WAF in observe mode). `rule`/`detail` only for `waf`. `country`/`asn`/`as_org` empty or 0 when unknown. `path` is the request path without its query, cut to 200 bytes.

### `GET /v1/gateway/guard/bans` → 200

```json
[ { "key": "203.0.113.9", "ip": "203.0.113.9", "country": "FR", "asn": 3215, "as_org": "Orange",
    "app": "jellyfin", "reason": "scan", "since": "2026-11-12T10:21:03Z",
    "until": "2026-11-12T11:21:03Z", "strikes": 1 } ]
```

Active bans only, newest first. `reason`: `waf`, `scan`, `rate`, `code`.

### `DELETE /v1/gateway/guard/bans/{key}` → 204

Lifts the ban now (idempotent). `{key}` is path-escaped (`2001:db8::/64` → `2001:db8::%2F64`). Strikes are kept.

## Order of filters on a shared host (Gateway)

As in the spec, for every request on the share listener whose host is in the host table:
1. `allow` match → skip 2–6 and the scan count (an allowed address is never refused, counted or banned, even with an older ban on its key);
2. banned key → 403, event `banned`/`blocked` (grouping applies);
3. `block` match → 403, `blocklist`;
4. country filter → 403, `country` (no country database: pass);
5. rate limit → 429, `rate`, counts a rate strike;
6. WAF (`observe`: event `observed`, request passes; `block`: 403, `waf`, counts a WAF hit);
7. access gate if `access: "code"` → entry page / 401 / 503;
8. proxy to the app; a 404 answer counts for `scan`.

Steps 1–6 and the scan count run only with `protection: true`. A host absent from `apps` is served as `access: "open"` with no filter. Fail-closed comes from the push order below and from `guard.json` living next to `hosts.json` (both survive a Gateway restart, both vanish at reboot): a protected host is never registered before its config. One more case is fail-closed in the Gateway itself: a host whose config says `access: "code"` while the config holds no `secret` (cannot verify) answers 503.

## Core order of pushes

At start and on every resync: `PUT /v1/gateway/guard` **before** any `PUT /v1/gateway/hosts`. On every change of the guard config: `PUT /v1/gateway/guard` at once.

## Core public API (dashboard ↔ core)

Under the existing `/v1/share` group; reads take what the group takes, every write wants the access token in the header (`headerTokenOnly`), like the other share writes. Answers use the core's `model.Result` envelope (`data`). Errors: `400` with `message` = one of the codes below, or the core's usual `500`.

`PolicyOverride` (an app's own settings): every field optional; `null`/absent = follow the box. `rules_off`, `allow`, `block` **add** to the box's lists; `waf`, `countries`, `rate_per_min`, `ban` **replace** the box's value.

```json
{ "waf": "block", "rules_off": [942100], "allow": [], "block": [], "countries": null, "rate_per_min": null, "ban": null }
```

### `GET /v1/share/guard`

```json
{
  "supported": true,
  "reason": "",
  "protection": false,
  "box": { "...": "Policy, as in the Gateway config" },
  "apps": {
    "jellyfin": { "access": "open", "override": null, "effective": { "...": "Policy" } }
  },
  "people": [ { "id": "p_7f3a", "name": "Maman", "apps": ["jellyfin"],
                "created_at": "2026-11-12T10:00:00Z", "last_used_at": "2026-11-12T10:20:00Z" } ],
  "geo": { "country": true, "asn": true, "updated_at": "2026-11-01T03:00:00Z", "error": "" },
  "waves": [ { "app": "jellyfin", "bans": 23 } ]
}
```

- `supported` false with `reason: "gateway_unsupported"` when the Gateway has no `/v1/gateway/guard`; every write then answers 400 `gateway_unsupported`.
- `apps` lists the **shared** apps only, by compose name.
- `last_used_at` absent when never used.

### Writes

| Route | Body | Answer `data` | Error codes |
|---|---|---|---|
| `PUT /v1/share/guard/protection` | `{"on": true}` | the `GET` body | `gateway_unsupported` |
| `PUT /v1/share/guard/box` | `Policy` | the `GET` body | `bad_policy:<field>` |
| `PUT /v1/share/guard/apps/:name` | `{"access":"code","override":PolicyOverride\|null}` | the `GET` body | `not_shared`, `bad_policy:<field>`, `gateway_unsupported` |
| `POST /v1/share/guard/people` | `{"name":"Maman","apps":["jellyfin"]}` | `{"person":{…},"code":"ABCD-EFGH-JKMN"}` | `bad_name`, `unknown_app:<name>` |
| `PUT /v1/share/guard/people/:id` | `{"name":"…","apps":[…]}` | the person | `not_found`, `bad_name`, `unknown_app:<name>` |
| `POST /v1/share/guard/people/:id/code` | — | `{"code":"…"}` (gen + 1) | `not_found` |
| `DELETE /v1/share/guard/people/:id` | — | — (204) | — |
| `GET /v1/share/guard/events?app=&reason=&limit=` | — | the Gateway's list, `app` filtered by compose name | `gateway_unsupported` |
| `GET /v1/share/guard/bans` | — | the Gateway's list | `gateway_unsupported` |
| `DELETE /v1/share/guard/bans/:key` | — | — (204) | `gateway_unsupported` |

A write whose push to the Gateway fails (other than 404) keeps the saved change, wakes the resync and answers 500 — except `POST …/people` and `POST …/people/:id/code`, which answer 200 with the code (shown only once; the resync pushes it). A bad body answers 400 `bad_request`.

`bad_name`: empty, over 40 characters, or a control character. A person's `apps` may name only shared apps; unsharing an app removes it from every person and from `apps`.
