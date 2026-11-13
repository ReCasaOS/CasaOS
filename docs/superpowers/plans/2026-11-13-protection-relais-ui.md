# Relay-side protection — dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** In the Protection section, the owner sees and switches the relay filtering, is invited to join the community protection with an explicit agreement, sees what their reports achieved, and finds the relay's refusals in the journal.

**Architecture:** Two calls join `shareGuardApi`; `ShareGuardSection.vue` gains a relay block (a switch, the join card or the sharing switch); a small `RelayJoin.vue` holds the card and the agreement modal; `GuardJournal.vue` gains a third tab. Strings are English keys in `en_US.json` and French in `fr_FR.json`, checked by `shareKeys.spec.js`.

**Tech Stack:** Vue 3, Buefy, Vitest + `@vue/test-utils` (happy-dom).

**Spec:** `docs/superpowers/specs/2026-11-13-protection-relais-design.md`. **Contract:** `docs/superpowers/plans/2026-11-13-protection-relais-contract.md` ("Core ↔ dashboard").

**Worktree:** `D:\clients\casaos\_work\ui-relay-guard`, branch `feat/relay-protection` from `inkly/main`. Node 22 at `/c/Users/garyd/AppData/Local/hermes/node` with corepack pnpm 9:

```bash
pnpm vitest run src/components/PublicShare src/service/shareGuard.spec.js
```

## Global Constraints

- The relay block shows only when `view.relay.supported` is true; otherwise nothing about the relay is drawn.
- The agreement checkbox is never pre-ticked; "I agree" stays disabled until it is ticked.
- Exact sentences (English key → French):
  - `Filtering by the relay` → `Filtrage par le relais`
  - `The relay cuts addresses known as malicious before they reach your box.` → `Le relais coupe les adresses connues comme malveillantes avant qu'elles n'atteignent votre box.`
  - `Join the community protection` → `Rejoindre la protection communautaire`
  - `Your box sends the relay the addresses it bans: the address, the reason and the app, nothing else. In return, addresses banned by several networks are cut before they reach you.` → `Votre box envoie au relais les adresses qu'elle bannit : l'adresse, la raison et l'app, rien d'autre. En échange, les adresses bannies par plusieurs réseaux sont coupées avant de vous atteindre.`
  - `{n} addresses already blocked by the community` → `{n} adresses déjà bloquées par la communauté`
  - `These addresses could have been cut at the relay before reaching your box.` → `Ces adresses auraient pu être coupées au relais avant d'atteindre votre box.`
  - `Share my bans` → `Partager mes bans`
  - `Your reports added {n} addresses to the community list this month` → `Vos signalements ont fait entrer {n} adresses dans la liste communautaire ce mois-ci`
  - `I agree that this box sends the addresses it bans to the relay` → `J'accepte que cette box envoie au relais les adresses qu'elle bannit`
  - `I agree` → `J'accepte`
  - `Cut at the relay` → `Coupé au relais`
- Agreement modal text (key → French), in this order:
  1. `What is sent: for each address your box bans, the address, the reason (OWASP rules, scan, request limit, access code) and the app.` → `Ce qui est envoyé : pour chaque adresse que votre box bannit, l'adresse, la raison (règles OWASP, scan, limite de requêtes, code d'accès) et l'app.`
  2. `What is never sent: pages visited, headers, codes, the addresses you block or allow yourself.` → `Ce qui n'est jamais envoyé : les pages visitées, les en-têtes, les codes, les adresses que vous bloquez ou autorisez vous-même.`
  3. `An address is cut for everyone only when boxes of two different owners banned it within 24 hours. The relay keeps reports 30 days.` → `Une adresse n'est coupée pour tous que si des box de deux propriétaires différents l'ont bannie en 24 heures. Le relais garde les signalements 30 jours.`
  4. `You can stop at any time; your reports are then deleted from the relay.` → `Vous pouvez arrêter à tout moment ; vos signalements sont alors supprimés du relais.`
- Relay refusal reason labels (`reason` → key → French): `manual` → `Blocked by the relay operator` → `Bloqué par l'exploitant du relais`; `suspended` → `Shared name suspended` → `Nom partagé suspendu`; `external` → `On the list {source}` → `Sur la liste {source}`; `community` → `Community list` → `Liste communautaire`; `cap` → `Too many connections` → `Trop de connexions`.

## Review Focus

1. A core without `relay` in the view (v0.5.138) must not break the section — test in Task 1 (`TestNoRelayKeyHidesBlock`).
2. Closing the agreement modal with Escape must not turn sharing on — test in Task 1 (`TestEscapeDoesNotJoin`).
3. A `consent_required` or `relay_unsupported` answer must show a sentence, not "Something went wrong" — test in Task 1.
4. The relay tab with an empty answer must show an empty state, not a broken table — test in Task 2.
5. Turning sharing off must not ask for confirmation and must take effect on one click — test in Task 1.

---

### Task 1: Relay block, join card and agreement

**Files:**
- Create: `src/components/PublicShare/RelayJoin.vue`, `src/components/PublicShare/RelayJoin.spec.js`
- Modify: `src/service/shareGuard.js` (`shareGuardApi.setRelay(body)` → `PUT /v1/share/guard/relay`, result through `applyGuard`; `shareGuardApi.relayRefusals()` → `GET /v1/share/guard/relay-refusals`), `src/components/PublicShare/ShareGuardSection.vue` (relay block under the protection switch, shown when `view.relay?.supported`, whether protection is on or not), `src/components/PublicShare/guardWords.js` (`guardErrorText` handles `consent_required` → `Tick the box to agree first.` and `relay_unsupported` → `The relay does not offer this yet.`), `src/assets/lang/en_US.json`, `src/assets/lang/fr_FR.json`, `src/components/PublicShare/ShareGuardSection.spec.js`, `src/service/shareGuard.spec.js`

**Interfaces:**
- Consumes: `view.relay` (contract), `shareGuardApi`, `applyGuard`.
- Produces: `RelayJoin` props `{ relay: Object, waves: Array, busy: Boolean }`, emits `join` (after the agreement) — the section calls `setRelay({ share_bans: true, consent: true })`.
- Layout: `b-switch` "Filtering by the relay" with its sentence; then, when `share_bans` is false, `RelayJoin` (card title, the "what is sent" sentence, the community count, the wave sentence when `waves.length > 0`, a "Join the community protection" button opening a `b-modal` with the four paragraphs, an unticked checkbox and "I agree"); when true, the `b-switch` "Share my bans" and the contribution line.
- [ ] **Step 1: Write the failing tests**:
  - `TestNoRelayKeyHidesBlock`: view without `relay` → no `.relay-block`.
  - `TestFilterSwitchCallsSetRelay`: flipping it → `setRelay({ filter: false })`.
  - `TestJoinNeedsTick`: open the modal → "I agree" disabled; tick → enabled; click → `setRelay({ share_bans: true, consent: true })`.
  - `TestEscapeDoesNotJoin`: open, press Escape → no call.
  - `TestWaveSentenceShown`: `waves: [{app:'jellyfin', bans: 23}]` → the wave sentence is in the card.
  - `TestShareOffOneClick`: `share_bans` true, flip off → `setRelay({ share_bans: false })`, no dialog.
  - `TestContributionLine`: `contributed_month: 3` → the line with 3.
  - `TestRelayErrors`: `consent_required` and `relay_unsupported` answers → their sentences.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement**, strings added to both locale files.
- [ ] **Step 4: Run `pnpm vitest run src/components/PublicShare src/service/shareGuard.spec.js` (includes `shareKeys.spec.js`) and see it pass.**
- [ ] **Step 5: Commit** — `feat(share): relay filtering and community protection in the Protection section`

### Task 2: "Cut at the relay" journal tab

**Files:**
- Modify: `src/components/PublicShare/GuardJournal.vue` (third `b-tab-item` "Cut at the relay", shown only when `shareGuard.view?.relay?.supported` and the `app` prop is null; columns Hour, IP address, Reason, Count; loads `shareGuardApi.relayRefusals()` when the tab opens and on Refresh; paginated by the existing `PER`; empty state `Nothing was cut at the relay in the last 7 days.` → `Rien n'a été coupé au relais ces 7 derniers jours.`), `src/components/PublicShare/guardWords.js` (`relayReasonText(r, t)` with the contract labels), locale files, `src/components/PublicShare/GuardJournal.spec.js`, `src/components/PublicShare/guardWords.spec.js`

- [ ] **Step 1: Write the failing tests**: `TestRelayTabHiddenWhenUnsupported`, `TestRelayTabHiddenPerApp` (`app` prop set), `TestRelayTabRows` (one `external` row with source `FireHOL level 1` → `On the list FireHOL level 1`, count 12), `TestRelayTabEmpty`, `relayReasonText` for each reason.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run the PublicShare specs and see them pass.**
- [ ] **Step 5: Commit** — `feat(share): relay refusals in the protection journal`
