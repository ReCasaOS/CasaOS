# Shared-app protection — dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the owner turn protection on, set it for the box and per app, manage people and their codes, and read the journal and bans; show ban waves and a missing country database in "Needs attention".

**Architecture:** A service module `src/service/shareGuard.js` (API + reactive store, same pattern as `publicShare.js`), pure helpers in `src/components/PublicShare/guardWords.js`, and four components under `src/components/PublicShare/`: `GuardPolicyForm.vue` (box policy or app override), `SharePeople.vue`, `GuardJournal.vue` (events + bans, optionally for one app), `ShareGuardSection.vue` (in the Remote access window, under `ShareSection`). `ShareAppModal.vue` gains the app's access mode, its own settings and its journal. `HealthStrip` gains two attention lines.

**Tech Stack:** Vue 3, Buefy-style `b-*` components already used in `PublicShare/`, vue-i18n (`src/assets/lang/en_US.json`, `fr_FR.json`), Vitest. `Intl.DisplayNames` for country names (no new dependency).

**Spec:** `docs/superpowers/specs/2026-11-12-protection-partage-design.md` (CasaOS repo). **Contract:** `docs/superpowers/plans/2026-11-12-protection-partage-contract.md`, section "Core public API" — every route and field below is defined there.

**Worktree:** `D:\clients\casaos\_work\ui-guard`, branch `feat/share-guard` from `inkly/main` (`git fetch inkly --no-tags`). Tests need Node 22 and pnpm 9 (corepack); Node 26 or pnpm 8 give false failures:

```bash
pnpm vitest run src/components/PublicShare src/service src/components/dashboard
```

## Global Constraints

- Nothing new shows when `GET /v1/share/guard` fails or answers `supported: false` with no shared app; with `supported: false` and shared apps, the protection section says "Update ReCasaOS to protect shared apps" / « Mettez ReCasaOS à jour pour protéger les apps partagées » and no control.
- A code is shown **only** in the answer of `POST /v1/share/guard/people` and `POST /v1/share/guard/people/:id/code`: kept in component state until the dialog closes, never in the store, never in `localStorage`.
- Switching an app to "Protected by code" first shows, and requires confirming: "Mobile and sync apps (Jellyfin, Nextcloud, Home Assistant…) will no longer connect: only a web browser gets in." / « Les applications mobiles et de synchronisation (Jellyfin, Nextcloud, Home Assistant…) ne pourront plus se connecter : seul un navigateur passe. »
- Turning protection on shows once: "A ban on the box comes after the relay and your Internet connection: it does not stop a flood (DDoS)." / « Un ban sur la box arrive après le relais et votre connexion : il n'arrête pas une inondation (DDoS). »
- Turning the OWASP rules on (box or app, from `off` to `observe` or `block`) says once: "The OWASP rules use about 50 MB of memory on the box." / « Les règles OWASP occupent environ 50 Mo de mémoire sur la box. » (measured on the Gateway branch; update the figure if the Gateway PR states another).
- Every view that shows a country or a network carries the link `<a href="https://db-ip.com">IP Geolocation by DB-IP</a>` (licence requirement).
- Every string in both `en_US.json` and `fr_FR.json`.

## Review Focus

1. A box with a hundred people or a journal at its 1 000-line cap must stay usable: lists paginate (25 per page) — test in Task 3 and Task 4.
2. The owner's own address in "Always allow" must not ban themselves after a typo elsewhere: "Always allow this address" adds the ban's IP to the box `allow` list **and** lifts the ban, in that order, and a failure of the second is shown — test in Task 3.
3. A write refused by the core (`bad_policy:allow[0]`) must point at the field, not show a raw code — test in Task 2.
4. An app unshared while its window is open must not leave a broken journal (the core drops it from `apps`): the app part hides — test in Task 5.
5. IPv6 ban keys (`2001:db8::/64`) must be escaped once in the unban URL — test in Task 1.

---

### Task 1: Service module and words

**Files:**
- Create: `src/service/shareGuard.js`, `src/service/shareGuard.spec.js`, `src/components/PublicShare/guardWords.js`, `src/components/PublicShare/guardWords.spec.js`

**Interfaces:**
- Produces:
  - `shareGuardApi` with `view()`, `setProtection(on)`, `setBox(policy)`, `setApp(name, access, override)`, `addPerson(name, apps)`, `updatePerson(id, name, apps)`, `newCode(id)`, `deletePerson(id)`, `events({ app, reason, limit })`, `bans()`, `unban(key)` — routes per the contract (`encodeURIComponent` on `:name`, `:id`, `:key`).
  - `shareGuard` reactive `{ view: null, ok: false }`, `refreshGuard({ force })`, `applyGuard(promise)` — same rules as `refresh`/`apply` in `publicShare.js` (60 s, write counter).
  - In `guardWords.js`: `reasonText(event, t)` (the spec's plain sentences: `waf` → "OWASP rule {rule}: {detail}", `country` → "Country blocked: {country}", `scan` → "Scan: many missing pages", `rate` → "Request limit exceeded", `code` → "Access code wrong too often", `blocklist` → "Address blocked", `banned` → "Banned address"), `actionText(action, t)`, `flag(cc)` (regional-indicator emoji, `''` for empty), `countryName(cc, locale)` (`Intl.DisplayNames`), `guardErrorText(err, t)` (maps `bad_policy:<field>`, `bad_name`, `unknown_app:<x>`, `not_shared`, `not_found`, `gateway_unsupported` to sentences; anything else → the generic error sentence of `shareWords.js`).

- [ ] **Step 1: Write the failing tests**
  - `unban('2001:db8::/64')` calls `DELETE /v1/share/guard/bans/2001%3Adb8%3A%3A%2F64`.
  - `applyGuard` stores `data`; a read started before a write does not overwrite it (same test shape as `publicShare.spec.js`).
  - `reasonText({reason:'waf',rule:942100,detail:'SQL Injection'}, t)` → "OWASP rule 942100: SQL Injection"; each reason maps; `flag('FR')` = `🇫🇷`; `flag('')` = `''`.
  - `guardErrorText({response:{data:{message:'bad_policy:allow[0]'}}}, t)` names the allow list.

- [ ] **Step 2: Run and see them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run and see them pass.**
- [ ] **Step 5: Commit** — `feat(share): protection API client and words`

### Task 2: Policy form (box policy or app override)

**Files:**
- Create: `src/components/PublicShare/GuardPolicyForm.vue`, `GuardPolicyForm.spec.js`

**Interfaces:**
- Consumes: Task 1.
- Produces: `<guard-policy-form :policy="Policy" :box="Policy|null" :override="PolicyOverride|null" :busy="bool" @save="payload" />`. With `box` null it edits a box `Policy` and emits one; with `box` set it edits an override: each block (OWASP rules, lists, countries, limits, bans) has a "Same as the box" / « Comme la box » switch; emits a `PolicyOverride` (`null` for blocks left on the box value; lists hold only the app's additions and show the box's entries greyed above them).
- Fields: OWASP mode (Off / Observe / Block, with "Observe first" / « Observer d'abord » as the hint), disabled rule ids (numbers, removable chips), allow and block lists (one IP or CIDR per line, validated client-side with the same rule as the core: IPv4/IPv6 address or CIDR), countries (None / Block these / Only these, multi-select with flag and `countryName`), requests per minute, the four ban rules (count and window in minutes).

- [ ] **Step 1: Write the failing tests**
  - Box mode: change OWASP to Block, add `192.0.2.0/24` to allow → `save` emits a full `Policy` with those values and the rest unchanged.
  - Override mode: only "Countries" switched off "Same as the box", set to Block `XX` → emits `{countries:{mode:'block',list:['XX']}}` and nothing else non-null.
  - An invalid line `300.1.1.1` in allow → inline error under the field, no `save`.
  - A core refusal `bad_policy:ban.scan_404` passed back through a `error` prop highlights the scan field.

- [ ] **Step 2–4:** fail, implement, pass.
- [ ] **Step 5: Commit** — `feat(share): protection policy form for the box and for an app`

### Task 3: Journal and bans

**Files:**
- Create: `src/components/PublicShare/GuardJournal.vue`, `GuardJournal.spec.js`

**Interfaces:**
- Consumes: Task 1.
- Produces: `<guard-journal :app="string|null" :box-policy="Policy" @changed />` — two tabs, "Events" / « Événements » and "Bans" / « Bans en cours ».
  - Events: columns date and time (locale), IP, country (flag + name), network (`as_org` + `AS<asn>`), app (hidden when `app` is set), path, reason (`reasonText`), action (`actionText`), count when > 1. Filters by app (when `app` null) and by reason. 25 per page. On a `waf` event: button "Stop applying this rule to {app}" / « Ne plus appliquer cette règle à {app} » → `setApp(app, currentAccess, override with rules_off + rule)`.
  - Bans: IP, country, network, app, reason, since, until; buttons "Unban" / « Débannir » and "Always allow this address" / « Toujours autoriser cette adresse » (box `allow` + ban's `ip`, then `unban(key)`).
  - DB-IP attribution link under both tabs.
  - Reads on mount and when the tab is shown; a "Refresh" button; no polling.

- [ ] **Step 1: Write the failing tests** (mocked `shareGuardApi`)
  - Events render the reason sentence and the flag; `count: 50` shows "×50".
  - `app="jellyfin"` → `events` called with `{app:'jellyfin'}` and no app column.
  - "Stop applying this rule" on a 942100 event → `setApp('jellyfin', 'open', {rules_off:[942100]})` when the app had no override; appends to an existing override's `rules_off`.
  - "Always allow" → `setBox` with the IP added, then `unban`; a failed `unban` shows an error and keeps the row.
  - 60 events → 3 pages.
  - The DB-IP link is present.

- [ ] **Step 2–4:** fail, implement, pass.
- [ ] **Step 5: Commit** — `feat(share): journal and bans of the protection`

### Task 4: People and the protection section

**Files:**
- Create: `src/components/PublicShare/SharePeople.vue`, `SharePeople.spec.js`, `src/components/PublicShare/ShareGuardSection.vue`, `ShareGuardSection.spec.js`
- Modify: `src/components/settings/RemoteAccessModal.vue` (render `<share-guard-section />` right after `<share-section />`)

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces:
  - `<share-people />`: list (name, apps, "Last used {date}" / « Utilisé le {date} » or "Never used" / « Jamais utilisé »), "Add a person" / « Ajouter une personne » (name + checkboxes of the apps whose `access` is `code`; when none, a sentence saying to protect an app first), edit (name, apps), "New code" / « Nouveau code » (confirm: the old code stops working), "Delete" / « Supprimer » (confirm). After add or new code, a dialog shows the code large, a copy button, and "This code will not be shown again." / « Ce code ne sera plus affiché. ». 25 per page.
  - `<share-guard-section />`: visible only when `publicShare` has at least one shared app. Title "Protection of shared apps" / « Protection des apps partagées ». Protection switch (with the DDoS sentence on first turn-on), then, when on: `<guard-policy-form>` for the box, `<share-people>`, `<guard-journal :app="null">`. `<share-people>` shows even when protection is off (codes do not depend on it, per the spec).

- [ ] **Step 1: Write the failing tests**
  - `AddPerson` flow: `addPerson` resolves `{person, code:'ABCD-EFGH-JKMN'}` → the dialog shows the code; after closing, the code is nowhere in the DOM or the store.
  - New code → confirm → `newCode(id)` → dialog with the new code.
  - No app in `code` access → the add form says to protect an app first and has no checkbox.
  - Section hidden without shared apps; `supported:false` with shared apps → only the update sentence.
  - Protection switch → `setProtection(true)` after the DDoS notice is acknowledged; off → no notice.
  - `RemoteAccessModal` renders `share-guard-section` after `share-section`.

- [ ] **Step 2–4:** fail, implement, pass.
- [ ] **Step 5: Commit** — `feat(share): people with access codes and the box's protection settings`

### Task 5: The app's window — access mode, own settings, own journal

**Files:**
- Modify: `src/components/PublicShare/ShareAppModal.vue`, `ShareAppModal.spec.js`

**Interfaces:**
- Consumes: Tasks 1–4.
- Produces: in `ShareAppModal`, under the shared app's address, when `shareGuard.view.apps[appName]` exists:
  - "Access" / « Accès »: "Open to anyone" / « Ouverte à tous » or "Protected by code" / « Protégée par code » (the browser-only confirmation of the Global Constraints before switching to code); when protected, the people whose `apps` include it, and a link to manage people (opens the Remote access window as the existing `openRemoteAccess` does).
  - "Protection" / « Protection »: shown when the box's protection is on: "Same as the box" / « Comme la box » or "Own settings" / « Réglages propres » (`<guard-policy-form :box="view.box" :override>`), then `<guard-journal :app="appName">`.
  - Also replace the sentence "Only its own login page protects it." with one that depends on the access mode: open → unchanged; protected → "Only people with an access code get in." / « Seules les personnes ayant un code d'accès entrent. ».
  - The access mode is also shown in the existing sharing sentence before the share switch is turned on: nothing changes there (an app is shared open).

- [ ] **Step 1: Write the failing tests**
  - Switch to "Protected by code" → confirmation shown → confirm → `setApp('jellyfin','code', null)`; cancel → no call, still open.
  - `gateway_unsupported` refusal → the sentence from `guardErrorText`, mode stays open.
  - Protected app lists "Maman" from `view.people`.
  - Box protection off → no "Protection" part; on → part present with "Same as the box" selected when `override` is null.
  - `view.apps` loses `jellyfin` after a refresh → the access and protection parts disappear without error.

- [ ] **Step 2–4:** fail, implement, pass.
- [ ] **Step 5: Commit** — `feat(share): access mode, own protection and journal in the app's sharing window`

### Task 6: Needs attention

**Files:**
- Modify: `src/components/dashboard/healthTiles.js` (`attentionItems` takes `guard`), `src/components/dashboard/HealthStrip.vue` (reads `shareGuard` through `refreshGuard()` with the strip's other sources), `src/components/dashboard/HealthStrip.spec.js`, `src/components/dashboard/healthTiles.spec.js`

**Interfaces:**
- Consumes: Task 1.
- Produces: attention item `share-reset` (level `danger`, when `view.reset` is true: "Protection settings of shared apps were reset: every shared app asks for a code, people must be added again" / « Les réglages de protection des apps partagées ont été réinitialisés : chaque app partagée demande un code, il faut rajouter les personnes »), attention items `share-wave-<app>` (level `danger`: "{app}: {n} addresses banned in 10 minutes" / « {app} : {n} adresses bannies en 10 minutes ») and `share-geo` (level `warning`, only when `protection` is on, some app's `effective.countries.mode` is not `none` and `geo.country` is false: "Country filtering of shared apps is not working" / « Le filtrage par pays des apps partagées ne fonctionne pas »). Clicking either opens the Remote access window.

- [ ] **Step 1: Write the failing tests**
  - `attentionItems({ guard: { waves:[{app:'jellyfin',bans:23}] } })` contains the wave item; no `guard` → no item (older core).
  - Geo item only under the stated condition (three cases).

- [ ] **Step 2–4:** fail, implement, pass; then `pnpm lint` (0 errors) and `pnpm build`.
- [ ] **Step 5: Commit** — `feat(dashboard): ban waves and missing country data in Needs attention`
- [ ] **Step 6: See it in the browser** against a stub that answers the contract's examples (extend `mock/stats-stub/grid.js`, which already answers `/v1/share` and `/v1/fleet`): the Remote access window with protection on, a person added (code shown once), the journal with a WAF event and its "Stop applying" button, a ban with "Always allow", an app window switched to protected with the browser-only confirmation. Screenshots in the PR.
