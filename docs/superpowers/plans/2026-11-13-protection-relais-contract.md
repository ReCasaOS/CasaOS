# Relay-side protection — contract between relay, core and dashboard

Spec: `docs/superpowers/specs/2026-11-13-protection-relais-design.md`. This file is the one place the three plans (`…-relais.md`, `…-coeur.md`, `…-ui.md`) take names, JSON shapes and constants from. A plan that needs something not written here adds it here first.

## Precision on the spec (planning call)

The spec says operator bans cut a connection "dès l'acceptation". The relay host itself (fleet enrolment, Headscale, `/admin`) also arrives through `share`; a country or ASN ban checked at accept would cut the boxes of that country from their own relay. So **every filter is checked after the SNI peek, and only for a share name** (`<label>.<box>.<SHARE_DOMAIN>`). Connections to the relay host and port 80 are never filtered (the table of the spec says "tous les noms partagés": this keeps it).

## Constants

| Name | Value |
|---|---|
| Address key | IPv4 address as `a.b.c.d/32`, IPv6 as the masked `/64` (`netip.PrefixFrom(a, 64).Masked().String()`); IPv4-mapped IPv6 is IPv4. Enrolment parses every key it receives with `netip.ParsePrefix` and stores the canonical `String()` |
| Keys never kept | loopback, private, link-local, unspecified, multicast, `100.64.0.0/10` (CGNAT, also Tailscale) |
| Community entry | 2 distinct fleets reporting the same key within a rolling 24 h |
| Community tiers | strike 1: 24 h; 2: 7 days; 3 and more: permanent (until lifted in `/admin`) |
| Strike window | a new ban is a repeat when it starts less than 30 days after the end of the previous ban of the same key; otherwise strikes start again at 1 |
| Range ban | 5 distinct keys with an active ban inside one `/24` (IPv4) or `/48` (IPv6) → the range is banned, own strikes, same tiers. Only member bans opened after the range's previous ban ended count, and a key's reports count only if their `since` is after that key's previous ban ended (so a lifted or expired ban never reopens on old evidence) |
| ASN flag | 10 or more active range bans whose first address maps to the same ASN → flagged in `/admin`, never banned automatically |
| Report acceptance | (1) a connection from that key to that box (`node_id`) was accepted by `share` at or after `since − 24 h` (the relay keeps the newest time per pair); (2) the box's fleet `created_at` is at least 7 days old (`COMMUNITY_MIN_FLEET_AGE`, tests only) and the fleet holds at least one row in `share_names`; (3) the box's `share_bans` is on. Otherwise the report is dropped silently |
| Report fields | `key`, `reason` ∈ `waf scan rate code`, `app` (`^[a-z0-9][a-z0-9._-]{0,63}$`), `since` (not older than 24 h, not more than 2 min in the future) |
| Retention | accepted connections 24 h; reports 30 days (purged at once for a box that turns sharing off); refusal counters 7 days; strikes 30 days after the end of the last non-permanent ban; ended bans deleted; expired operator bans deleted; fleets that opened a ban kept 32 days (for `contributed_month`). Applied every minute even when sharing is off |
| Feed refresh | at most once an hour (`every_s` ≥ 3600); AbuseIPDB free preset 21 600 s (4 a day) |
| Feed download | 60 s timeout, body read up to 64 MiB, `User-Agent: recasaos-relay` |
| Feed caps | per feed `max_entries` (default 100 000); total of enabled feeds `FILTER_MAX_ENTRIES` (default 1 000 000). A feed over either cap is refused whole; the last good copy stays |
| Snapshot poll (`share`) | every 30 s with `If-None-Match`; first start with none: let everything through and log once `share: no filter yet, letting connections through`. Enrolment rebuilds the snapshot when something changed (and at the earliest ban end), at most 5 minutes apart otherwise |
| Events post (`share`) | every 15 s with the usage tick; an accepted (key, node) pair is sent at most once per 10 minutes |
| DB-IP on the relay | `<data>/geo/dbip-country-lite.mmdb`, `<data>/geo/dbip-asn-lite.mmdb`; from `https://download.db-ip.com/free/dbip-{country,asn}-lite-YYYY-MM.mmdb.gz`; checked daily, replaced when the month changes, written by rename; `<data>` = directory of `STORE_PATH` |
| Enrolment env | `FILTER_MAX_ENTRIES`, `PROTECT_RATE_PER_HOUR` (signed protection routes, per IP, default 600), `CONNS_PER_IP` / `CONNS_PER_IP_PER_BOX` (display only, same defaults as `share`), `COMMUNITY_MIN_FLEET_AGE` (tests only) |
| Feed copies | `<data>/feeds/<id>.txt`, one canonical prefix per line, written by rename |

## Feed presets

Checked on the official pages 2026-11-13. Every preset is added **disabled**. Only `https` URLs: a plain-http list could be rewritten in transit to ban everyone.

| Key | Name | URL | Format | Every | Note shown in `/admin` |
|---|---|---|---|---|---|
| `firehol1` | FireHOL level 1 | `https://raw.githubusercontent.com/firehol/blocklist-ipsets/master/firehol_level1.netset` | text | 3600 | Aggregate (DShield, Feodo, full bogons, Spamhaus DROP); see each source's license |
| `spamhaus-drop` | Spamhaus DROP | `https://www.spamhaus.org/drop/drop.txt` | text | 86400 | Spamhaus asks for at most one download a day; free for non-commercial use; the text file is being replaced by JSON |
| `blocklistde` | blocklist.de (all) | `https://lists.blocklist.de/lists/all.txt` | text | 3600 | Regenerated every 30 minutes |
| `et-compromised` | Emerging Threats compromised | `https://rules.emergingthreats.net/blockrules/compromised-ips.txt` | text | 86400 | The builder checks it answers over https; if not, this preset is dropped |
| `tor-exits` | Tor exit nodes | `https://check.torproject.org/torbulkexitlist` | text | 3600 | Blocking cuts every Tor visitor, including people who only want privacy |
| `abuseipdb` | AbuseIPDB (subscription) | `https://api.abuseipdb.com/api/v2/blacklist?confidenceMinimum=90&limit=100000` | text, header `Key`, `Accept: text/plain` | 3600 | `confidenceMinimum` needs a paid plan; check your plan's daily download limit |
| `abuseipdb-free` | AbuseIPDB (free) | `https://api.abuseipdb.com/api/v2/blacklist` | same | 21600 | Free plan: 10 000 addresses at 100 % confidence, 5 downloads a day |
| `crowdsec` | CrowdSec blocklist | `https://admin.api.crowdsec.net/v1/integrations/<integration id>/content` (the operator replaces the id) | text, Basic auth `user:password` | 3600 | Raw IP list integration from the CrowdSec console; credentials shown once there |

Any feed (preset or not): an entry broader than `/8` (IPv4) or `/19` (IPv6) is skipped, so a broken or tampered list cannot ban the whole Internet.

## Refusal reasons and sources

| `reason` | `source` | When |
|---|---|---|
| `manual` | `address`, `range`, `asn`, `country` | operator ban |
| `suspended` | `""` | name suspended in `/admin` |
| `external` | the feed's name | box filter on, key in an enabled feed |
| `community` | `address`, `range` | box filter and sharing on, key in the community list |
| `cap` | `ip`, `ip-box` | existing connection caps (`ip` refusals have name `""`) |

## Box routes (enrolment, signed with the box key, `boxAuth`)

All under the existing `cfg.shareEnabled` wrapper. A relay without them answers 404 (mux): the core reads any 404 there as "relay too old".

### `PUT /v1/share/protection` → 200

Body (≤ 1 KiB): `{"relay_filter": true, "share_bans": false}`. Answer:

```json
{ "relay_filter": true, "share_bans": false, "share_bans_at": null,
  "community_size": 1234, "contributed_month": 0 }
```

- Stored per `node_id`. A box with no row: `relay_filter` false, `share_bans` false.
- `share_bans_at`: set when `share_bans` goes false → true, cleared when it goes back; RFC 3339 or `null`.
- `share_bans` true → false: delete every report of this `node_id`.
- `community_size`: active community bans (addresses + ranges).
- `contributed_month`: community bans opened since the first day of the current month (UTC) whose opening reports include this box's fleet.

### `POST /v1/share/bans` → 204

Body (≤ 32 KiB): `{"bans": [{"key": "203.0.113.7/32", "reason": "scan", "app": "jellyfin", "since": "2026-11-13T10:20:00Z"}]}`, at most 200 items. 400 `bad request` on bad JSON, more than 200 items, or an item failing the field rules. Items failing the acceptance rules are dropped; still 204. A repeat of (node, key, since) is ignored.

### `GET /v1/share/refusals` → 200

Refusals of the box's own name over 7 days, newest hour first, at most 2 000 rows; `[]` for a box without a name:

```json
[ { "hour": "2026-11-13T10:00:00Z", "key": "203.0.113.7/32", "reason": "external", "source": "FireHOL level 1", "count": 12 } ]
```

## Internal routes (enrolment ↔ share, `internalOnly`)

### `GET /internal/share/filter` → 200 or 304

`If-None-Match: "<version>"` → 304 when unchanged. 200 body, `ETag: "<version>"`:

```json
{
  "version": "<hex sha256 of the body without version>",
  "allow": ["192.0.2.0/24"],
  "manual": { "address": ["…/32"], "range": ["…/24"], "asn": ["…"], "country": ["…"] },
  "external": { "FireHOL level 1": ["…"], "AbuseIPDB": ["…"] },
  "community": { "address": ["…/32", "…/64"], "range": ["…/24"] },
  "names": { "mybox": { "filter": true, "community": true } },
  "suspended": ["badname"]
}
```

- `names` lists only names whose box has a protection row; `community` is `filter && share_bans`.
- `asn` and `country` are already expanded to prefixes by enrolment.

### `POST /internal/share/events` → 204

Body (≤ 1 MiB):

```json
{ "seen": [ { "key": "203.0.113.7/32", "node_id": "7" } ],
  "refusals": [ { "hour": "2026-11-13T10:00:00Z", "name": "mybox", "key": "…", "reason": "external", "source": "FireHOL level 1", "count": 3 } ] }
```

Refusal counts add up on (hour, name, key, reason, source).

## Core ↔ dashboard

### `GET /v1/share/guard` gains `relay`

```json
"relay": { "supported": true, "filter": true, "share_bans": false, "share_bans_at": null,
           "community_size": 1234, "contributed_month": 0 }
```

`supported` false (relay 404 or sharing off): the other fields keep their zero values and the dashboard hides the relay block. The numbers are the last answer of `PUT /v1/share/protection` (pushed at start, at every resync and on every change).

### `PUT /v1/share/guard/relay` → 200 (header token)

Body: `{"filter": true}` or `{"share_bans": true, "consent": true}` (either field or both). `share_bans: true` without `consent: true` → 400 `consent_required`. Relay too old → 400 `relay_unsupported`. Answer: the new `GuardView`. Push failure: saved locally, 500 `the change is saved…` like the other guard writes.

### `GET /v1/share/guard/relay-refusals` → 200

The relay's `GET /v1/share/refusals` passed through; relay too old → `[]`.

### Core settings in `share-guard.json`

`"relay_filter": true|false` (absent = true), `"share_bans": false`, `"share_bans_at": "<RFC 3339>"` (omitted when zero).
