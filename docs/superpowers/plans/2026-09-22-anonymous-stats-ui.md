# Anonymous Statistics — Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The dashboard (CasaOS-UI) tells the owner once that anonymous statistics are on, lets them turn them off from that notice or from Settings, and shows the exact properties the core would send.

**Architecture:** Two methods on the existing `sys` API client reach the core's `GET`/`PUT /v1/sys/telemetry`, and the dashboard talks to nothing else. `CoreService.vue` shows the one-time notice after login, `TopBar.vue` carries the Settings switch, and both open `TelemetryPreviewModal.vue`, which asks the core for the preview when it opens. When a PUT from the notice goes through, CoreService passes the core's `enabled` to TopBar over the existing `$EventBus`, so the switch never has to ask the core again.

**Tech Stack:** Vue 3.5 (Options API), Buefy 3.1.0, Bulma 1, vue-i18n 9 (legacy mode, `fallbackLocale: 'en_us'`), axios 0.34, mitt (the `$EventBus`), vitest 4 + happy-dom + @vue/test-utils 2.

**Spec:** D:/clients/casaos/CasaOS/docs/superpowers/specs/2026-09-22-anonymous-stats-design.md

## Global Constraints

- Repository: `D:/clients/casaos/CasaOS-UI`. Branch `feat/telemetry` from `inkly/main` (tag `v0.4.69`, commit `4dd9720`). Run every command from the repository root.
- Read: `GET /v1/sys/telemetry` answers the core's `model.Result` envelope `{success, message, data}`, with `data = {"enabled": bool, "notice_seen": bool, "preview": {"event": "heartbeat", "properties": {...the exact properties the sender would send...}}}`.
- Change: `PUT /v1/sys/telemetry` with body `{"enabled"?: bool, "notice_seen"?: bool}`. Both fields are optional, and the core ignores unknown fields. It answers `data` = the same object as GET, after the change.
- Authentication: the core requires an admin JWT. The shared axios instance in `src/service/service.js` already adds it, so the client adds nothing.
- Client paths: `api.get('/sys/telemetry')` and `api.put('/sys/telemetry', data)`. `testVisionNum()` in `service.js` prefixes `/v1`.
- The dashboard sends nothing to PostHog. It only talks to the core. The spec rules out anything browser-side.
- Notice text: "ReCasaOS sends anonymous statistics (versions, hardware, country)". Its two actions are *See what is sent* and *Turn off*.
- When the notice shows: only when `enabled` is true and `notice_seen` is false. Closing the notice or either action sends `notice_seen: true`. *Turn off* also sends `enabled: false`, in the same request.
- Settings switch: labelled "Anonymous usage statistics". It sends `{enabled, notice_seen: true}`: using the switch shows the owner knows, so an owner who opts in there is not shown the notice at the next login.
- Switch and notice stay in step through `$EventBus` event `events.TELEMETRY_CHANGED` (`'telemetryChanged'`, payload: the core's `enabled` after a PUT from the notice). TopBar asks the core once, from `mounted()`; opening the Settings dropdown sends nothing.
- Preview modal: renders `data.preview.properties` as JSON, plus one sentence that names PostHog (EU) and links `https://github.com/ReCasaOS/CasaOS-Install#anonymous-statistics`.
- Strings: added to `src/assets/lang/en_US.json` and `src/assets/lang/fr_FR.json` only. The key is the English text, and new keys go at the end of the file. The other 29 languages fall back to `en_us` (`src/plugins/i18n.js`), so they are not touched.
- Older core: the core ships first, but the dashboard still has to behave against a core without the route. A failed GET means no notice and no Settings row.
- pnpm: always `npx -y pnpm@9.0.6`. Tests: `npx -y pnpm@9.0.6 exec vitest run <file>`. Lint gate: `npx eslint . --quiet` (0 errors). Never run `--fix`.
- Spec files that touch the DOM start with `// @vitest-environment happy-dom`. Under happy-dom, `import.meta.url` is not a file URL, so a spec reads a source file by its project-root path (e.g. `'src/components/TopBar.vue'`).
- Language tables: `@/assets/lang` uses webpack's `require.context`. Any spec that imports a component importing it mocks it: `vi.mock('@/assets/lang', () => ({ default: { en_us: { lang_name: 'English' } } }))`.
- Import order (ESLint `import/order`, an error): `node:` builtins, then packages, then relative `./` imports, then `@/` aliases.
- Commits: `git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "..."`. Never add a `Co-Authored-By` trailer or any AI attribution.
- Forbidden: `git merge`, `git pull`, `git push`, `git tag`, `git rebase`, `rm -rf`, `git checkout -- <path>`, `git reset --hard`, `git clean`, `eslint --fix`.
- Windows working tree: files are CRLF (`core.autocrlf=true`), and `LF will be replaced by CRLF` warnings are noise. Keep the tab indentation of every file you touch. The JSON files use 2 spaces.
- Line numbers in this plan are those of the file at `inkly/main`. Every anchor is also quoted: once an earlier insertion has shifted a file, find the anchor by its text.
- Baseline at `v0.4.69`: `npx -y pnpm@9.0.6 exec vitest run` passes 55 files and 516 tests, and `npx eslint . --quiet` exits 0.

---

## File structure

| File | Change | Responsibility |
|---|---|---|
| `src/service/sys.js` | Modify | `getTelemetry()` / `setTelemetry(data)`: the two calls to the core. |
| `src/service/sys.spec.js` | Create | Checks the method, URL and body each call sends, through a fake axios adapter. |
| `src/components/settings/TelemetryPreviewModal.vue` | Create | The "What is sent" modal. It fetches the preview when it opens, shows the properties as JSON, and has one sentence that names PostHog (EU) and links the README. |
| `src/components/settings/TelemetryPreviewModal.spec.js` | Create | Checks that the modal renders the API's properties, names PostHog (EU), links the README, and shows an error when the call fails. |
| `src/events/events.js` | Modify | `TELEMETRY_CHANGED`: the bus event that carries the core's `enabled` from the notice to the switch. |
| `src/components/CoreService.vue` | Modify | `announceTelemetry()` (the one-time notice, called from `mounted()`, emitting `TELEMETRY_CHANGED` after its PUT) and `showTelemetryPreview()`. |
| `src/components/CoreService.spec.js` | Modify | Notice behaviour: that `mounted()` asks, when the notice is shown, what each way out sends, and the event after *Turn off*. |
| `src/components/TopBar.vue` | Modify | The Settings row (switch and *See what is sent* link), its four methods, and the `TELEMETRY_CHANGED` listener added in `mounted()` and removed in `beforeUnmount()`. |
| `src/components/TopBar.spec.js` | Modify | Switch wiring, `mounted()` asking the core and following the notice, no row after a failed GET, what the switch sends, reverting when the core refuses, and the link opening the modal. |
| `src/assets/lang/en_US.json` | Modify | The 8 new English strings. |
| `src/assets/lang/fr_FR.json` | Modify | The same 8 keys in French. |

Strings added (the key and the en_US value are identical):

| Key | fr_FR | Task |
|---|---|---|
| `What is sent` | `Ce qui est envoyé` | 2 |
| `When statistics are on, these properties go to PostHog (EU) once a day and after each install or update.` | `Quand les statistiques sont activées, ces propriétés partent chez PostHog (UE) une fois par jour et après chaque installation ou mise à jour.` | 2 |
| `The preview could not be loaded.` | `L'aperçu n'a pas pu être chargé.` | 2 |
| `ReCasaOS sends anonymous statistics (versions, hardware, country).` | `ReCasaOS envoie des statistiques anonymes (versions, matériel, pays).` | 3 |
| `See what is sent` | `Voir ce qui est envoyé` | 3 |
| `Turn off` | `Désactiver` | 3 |
| `Anonymous usage statistics` | `Statistiques d'utilisation anonymes` | 4 |
| `The setting could not be saved.` | `Le réglage n'a pas pu être enregistré.` | 4 |

(`Close` and `Learn more` already exist in both files.)

---

### Task 1: API client for the statistics state

**Files:**
- Modify: `src/service/sys.js` (insert after line 125, the closing `},` of `checkSshLogin`)
- Test: `src/service/sys.spec.js` (create)

**Interfaces:**
- Consumes: the core routes `GET /v1/sys/telemetry` and `PUT /v1/sys/telemetry` (contract above); `api` from `src/service/service.js`.
- Produces:
  - `sys.getTelemetry(): Promise<AxiosResponse<{ success: number, message: string, data: TelemetryState }>>`
  - `sys.setTelemetry(data: { enabled?: boolean, notice_seen?: boolean }): Promise<AxiosResponse<{ success: number, message: string, data: TelemetryState }>>`
  - `TelemetryState = { enabled: boolean, notice_seen: boolean, preview: { event: 'heartbeat', properties: Record<string, string | number | boolean> } }`
  - Components reach both as `this.$api.sys.getTelemetry()` / `this.$api.sys.setTelemetry(data)` (`$api` is set in `src/main.js`).

- [ ] **Step 1: Create the branch**

```bash
cd D:/clients/casaos/CasaOS-UI
git status --short
git fetch inkly
git switch -c feat/telemetry inkly/main
git log --oneline -1
```

Expected: `git status --short` prints nothing. The last line reads `4dd9720 refactor(events): remove the websocket hub nothing opened`.

- [ ] **Step 2: Write the failing test** — create `src/service/sys.spec.js`:

```js
// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import sys from './sys.js'
import { instance } from './service.js'

// service.js wires its 401 interceptor to the router and the store; neither
// is exercised here.
vi.mock('@/router', () => ({ default: { replace() {} } }))
vi.mock('@/store', () => ({ default: { commit() {} } }))

// A fake adapter sees the request as XHR would, after transformRequest.
const adapter = vi.fn(config => Promise.resolve({ data: { success: 200, data: {} }, status: 200, statusText: 'OK', headers: {}, config }))
instance.defaults.adapter = adapter

const sent = () => adapter.mock.calls[0][0]

describe('anonymous statistics client', () => {
	beforeEach(() => adapter.mockClear())

	it('reads the state from the core\'s v1 sys group', async () => {
		await sys.getTelemetry()
		expect(sent().method).toBe('get')
		expect(sent().url).toBe('/v1/sys/telemetry')
	})

	it('sends only the fields it is given', async () => {
		await sys.setTelemetry({ notice_seen: true })
		expect(sent().method).toBe('put')
		expect(sent().url).toBe('/v1/sys/telemetry')
		expect(JSON.parse(sent().data)).toEqual({ notice_seen: true })
	})
})
```

- [ ] **Step 3: Run it and watch it fail**

```bash
npx -y pnpm@9.0.6 exec vitest run src/service/sys.spec.js
```

Expected: `Tests  2 failed (2)`, each with `TypeError: sys.getTelemetry is not a function` / `TypeError: sys.setTelemetry is not a function`.

- [ ] **Step 4: Implement** — in `src/service/sys.js`, insert after line 125 (the `},` that closes `checkSshLogin`, before the `// power -- data:shutdown` comment):

```js

	// Anonymous statistics: { enabled, notice_seen, preview: { event, properties } },
	// the preview built by the core's sender at the moment of the call.
	getTelemetry() {
		return api.get(`${PREFIX}/telemetry`)
	},

	// data: { enabled?, notice_seen? }; answers the same object as getTelemetry.
	setTelemetry(data) {
		return api.put(`${PREFIX}/telemetry`, data)
	},
```

- [ ] **Step 5: Run it and watch it pass**

```bash
npx -y pnpm@9.0.6 exec vitest run src/service/sys.spec.js
npx eslint src/service/sys.js src/service/sys.spec.js --quiet
```

Expected: `Tests  2 passed (2)`. ESLint prints nothing and exits 0.

- [ ] **Step 6: Commit**

```bash
git add src/service/sys.js src/service/sys.spec.js
git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(sys): client for the core's anonymous statistics state"
```

---

### Task 2: The "What is sent" preview modal

**Files:**
- Create: `src/components/settings/TelemetryPreviewModal.vue`
- Modify: `src/assets/lang/en_US.json` (lines 787–788, the last entry and `}`), `src/assets/lang/fr_FR.json` (lines 787–788)
- Test: `src/components/settings/TelemetryPreviewModal.spec.js` (create)

**Interfaces:**
- Consumes: `this.$api.sys.getTelemetry()` (Task 1).
- Produces: `TelemetryPreviewModal` (default export, `name: 'TelemetryPreviewModal'`). It takes no props and emits `close`. Tasks 3 and 4 open it with:
  `this.$buefy.modal.open({ component: TelemetryPreviewModal, hasModalCard: true, trapFocus: true, canCancel: ['escape', 'outside'], scroll: 'keep', animation: 'zoom-in' })`.

- [ ] **Step 1: Write the failing test** — create `src/components/settings/TelemetryPreviewModal.spec.js`:

```js
// @vitest-environment happy-dom
import { h } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import TelemetryPreviewModal from './TelemetryPreviewModal.vue'

// The core builds this with the function its sender uses; the modal shows it as is.
const PROPERTIES = {
	distribution: 'v0.5.0',
	core: 'v0.4.59',
	arch: 'arm64',
	os: 'debian 12',
	kernel: '6.8',
	virtualization: 'none',
	model: 'Raspberry Pi 5 Model B Rev 1.0',
	docker: '28.3.1',
	cpu_cores: 4,
	ram_gb: 8,
	disks: 2,
	storage_tb: '4-8',
	raid: false,
}

function open(getTelemetry) {
	return mount(TelemetryPreviewModal, {
		global: {
			mocks: { $t: key => key, $api: { sys: { getTelemetry } } },
			// The warning's words are what is checked, so its stub keeps them.
			stubs: { 'b-button': true, 'b-message': { render() { return h('div', this.$slots.default()) } } },
		},
	})
}

describe('what is sent', () => {
	it('shows the properties the core answers, as JSON', async () => {
		const getTelemetry = vi.fn(() => Promise.resolve({
			data: { success: 200, data: { enabled: true, notice_seen: true, preview: { event: 'heartbeat', properties: PROPERTIES } } },
		}))
		const wrapper = open(getTelemetry)
		await flushPromises()

		expect(getTelemetry).toHaveBeenCalledTimes(1)
		expect(JSON.parse(wrapper.find('pre').text())).toEqual(PROPERTIES)
	})

	it('names PostHog (EU) and links the README section', async () => {
		const wrapper = open(vi.fn(() => new Promise(() => {})))

		expect(wrapper.text()).toContain('PostHog (EU)')
		expect(wrapper.find('a').attributes('href')).toBe('https://github.com/ReCasaOS/CasaOS-Install#anonymous-statistics')
	})

	it('says so when the preview cannot be loaded', async () => {
		const wrapper = open(vi.fn(() => Promise.reject(new Error('offline'))))
		await flushPromises()

		expect(wrapper.find('pre').exists()).toBe(false)
		expect(wrapper.text()).toContain('The preview could not be loaded.')
	})
})
```

- [ ] **Step 2: Run it and watch it fail**

```bash
npx -y pnpm@9.0.6 exec vitest run src/components/settings/TelemetryPreviewModal.spec.js
```

Expected: the suite fails to load with `Failed to resolve import "./TelemetryPreviewModal.vue" from "src/components/settings/TelemetryPreviewModal.spec.js". Does the file exist?`

- [ ] **Step 3: Implement** — create `src/components/settings/TelemetryPreviewModal.vue`:

```vue
<template>
	<div class="modal-card">
		<header class="modal-card-head">
			<h3 class="title is-header">
				{{ $t('What is sent') }}
			</h3>
		</header>

		<section class="modal-card-body">
			<p class="is-size-7 mb-3">
				{{ $t('When statistics are on, these properties go to PostHog (EU) once a day and after each install or update.') }}
				<a href="https://github.com/ReCasaOS/CasaOS-Install#anonymous-statistics" rel="noopener noreferrer" target="_blank">{{ $t('Learn more') }}</a>
			</p>

			<b-message v-if="loadError" size="is-small" type="is-warning">
				{{ loadError }}
			</b-message>
			<pre v-else-if="properties">{{ json }}</pre>
		</section>

		<footer class="modal-card-foot is-flex is-justify-content-flex-end">
			<b-button :label="$t('Close')" rounded @click="$emit('close')" />
		</footer>
	</div>
</template>

<script>
export default {
	name: 'TelemetryPreviewModal',
	emits: ['close'],
	data() {
		return {
			properties: null,
			loadError: '',
		}
	},
	computed: {
		json() {
			return JSON.stringify(this.properties, null, 2)
		},
	},
	// Asked for when it opens: the core builds the preview with the function its
	// sender uses, so this is what would go out now.
	async mounted() {
		try {
			const res = await this.$api.sys.getTelemetry()
			this.properties = res.data.data.preview.properties
		} catch {
			this.loadError = this.$t('The preview could not be loaded.')
		}
	},
}
</script>
```

In `src/assets/lang/en_US.json`, replace the last two lines:

```json
  "Cloning the repository…": "Cloning the repository…"
}
```

with:

```json
  "Cloning the repository…": "Cloning the repository…",
  "What is sent": "What is sent",
  "When statistics are on, these properties go to PostHog (EU) once a day and after each install or update.": "When statistics are on, these properties go to PostHog (EU) once a day and after each install or update.",
  "The preview could not be loaded.": "The preview could not be loaded."
}
```

In `src/assets/lang/fr_FR.json`, replace the last two lines:

```json
  "Cloning the repository…": "Clonage du dépôt…"
}
```

with:

```json
  "Cloning the repository…": "Clonage du dépôt…",
  "What is sent": "Ce qui est envoyé",
  "When statistics are on, these properties go to PostHog (EU) once a day and after each install or update.": "Quand les statistiques sont activées, ces propriétés partent chez PostHog (UE) une fois par jour et après chaque installation ou mise à jour.",
  "The preview could not be loaded.": "L'aperçu n'a pas pu être chargé."
}
```

- [ ] **Step 4: Run it and watch it pass; check the language files still parse**

```bash
npx -y pnpm@9.0.6 exec vitest run src/components/settings/TelemetryPreviewModal.spec.js
npx eslint src/components/settings/TelemetryPreviewModal.vue src/components/settings/TelemetryPreviewModal.spec.js --quiet
node -e "const en=require('./src/assets/lang/en_US.json'),fr=require('./src/assets/lang/fr_FR.json');const keys=['What is sent','When statistics are on, these properties go to PostHog (EU) once a day and after each install or update.','The preview could not be loaded.'];const miss=keys.filter(k=>!en[k]||!fr[k]);if(miss.length){console.error(miss);process.exit(1)}console.log('lang ok')"
```

Expected: `Tests  3 passed (3)`. ESLint prints nothing. The node check prints `lang ok`. A JSON syntax error would throw here instead: vitest mocks the language table, and ESLint ignores `src/assets/lang/*.json`, so neither of them would catch it.

- [ ] **Step 5: Commit**

```bash
git add src/components/settings/TelemetryPreviewModal.vue src/components/settings/TelemetryPreviewModal.spec.js src/assets/lang/en_US.json src/assets/lang/fr_FR.json
git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(settings): show what the anonymous statistics send"
```

---

### Task 3: The one-time notice after login

**Files:**
- Modify: `src/events/events.js` (lines 32–33, `CLOSE_APP_IFRAME: 'closeAppIframe',` and `}`)
- Modify: `src/components/CoreService.vue`. It gets imports at lines 23 and 33, `mounted()` at lines 108–110, and two new methods after line 148, the `},` closing `announceBackupFailures`. `events` is already imported (line 31).
- Modify: `src/assets/lang/en_US.json`, `src/assets/lang/fr_FR.json` (the last entry, added in Task 2, and `}`)
- Test: `src/components/CoreService.spec.js` (lines 2–3 imports; a new `describe` appended after line 100)

**Interfaces:**
- Consumes: `this.$api.sys.getTelemetry()`, `this.$api.sys.setTelemetry(data)` (Task 1), `TelemetryPreviewModal` (Task 2), `this.$EventBus.$emit(type, payload)` (`src/events/eventBus.js`), and Buefy's `this.$buefy.notification.open(params)`. That call accepts `message: string | VNode | VNode[]` and `onClose: () => void`, returns an instance with `close()`, and calls `onClose` whenever the notice closes, whether from its cross or from `close()`.
- Produces:
  - `events.TELEMETRY_CHANGED = 'telemetryChanged'` in `src/events/events.js`. CoreService emits it on `this.$EventBus` after every PUT from the notice that succeeds, with one payload: `enabled: boolean`, taken from the core's answer. A PUT that fails emits nothing.
  - `CoreService` methods `announceTelemetry(): Promise<void>` (called from `mounted()`) and `showTelemetryPreview(): void`.

- [ ] **Step 1: Write the failing test** — in `src/components/CoreService.spec.js`, replace lines 2–3:

```js
import { describe, expect, it, vi } from 'vitest'
import CoreService from '@/components/CoreService.vue'
```

with:

```js
import { flushPromises } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import CoreService from '@/components/CoreService.vue'
import TelemetryPreviewModal from '@/components/settings/TelemetryPreviewModal.vue'
import events from '@/events/events'
```

and append at the end of the file:

```js

// Statistics are on by default, and this notice is how the owner of a box learns
// it: once, after login, with the two ways out right in it.
describe('the anonymous statistics notice', () => {
	function box(state) {
		let params
		const vm = {
			$t: key => key,
			$api: {
				sys: {
					getTelemetry: vi.fn(() => Promise.resolve({ data: { success: 200, data: state } })),
					// The core answers the state after the change.
					setTelemetry: vi.fn(change => Promise.resolve({ data: { success: 200, data: { enabled: true, ...change } } })),
				},
			},
			$EventBus: { $emit: vi.fn() },
			$buefy: {
				modal: { open: vi.fn() },
				// Buefy calls onClose whenever the notice closes: its cross, or close().
				notification: {
					open: vi.fn((p) => {
						params = p
						return { close: () => p.onClose() }
					}),
				},
			},
		}
		vm.showTelemetryPreview = CoreService.methods.showTelemetryPreview.bind(vm)
		const announce = CoreService.methods.announceTelemetry.bind(vm)
		// The message is [sentence, row of buttons]; a button is found by its words.
		const click = label => params.message[1].children.find(b => b.children === label).props.onClick()
		const close = () => params.onClose()

		return { vm, announce, click, close }
	}

	it('is asked for when the dashboard opens', () => {
		const vm = { announceBackupFailures: vi.fn(), announceTelemetry: vi.fn() }

		CoreService.mounted.call(vm)

		expect(vm.announceTelemetry).toHaveBeenCalledTimes(1)
	})

	it('is shown when statistics are on and it was never seen', async () => {
		const { vm, announce } = box({ enabled: true, notice_seen: false })

		await announce()

		expect(vm.$buefy.notification.open).toHaveBeenCalledTimes(1)
		const [sentence] = vm.$buefy.notification.open.mock.calls[0][0].message
		expect(sentence.children).toBe('ReCasaOS sends anonymous statistics (versions, hardware, country).')
	})

	it.each([
		['statistics are off', { enabled: false, notice_seen: false }],
		['it was seen', { enabled: true, notice_seen: true }],
	])('is not shown when %s', async (_, state) => {
		const { vm, announce } = box(state)

		await announce()

		expect(vm.$buefy.notification.open).not.toHaveBeenCalled()
	})

	it('is not shown when the core cannot be asked', async () => {
		const { vm, announce } = box()
		vm.$api.sys.getTelemetry.mockRejectedValue(new Error('offline'))

		await announce()

		expect(vm.$buefy.notification.open).not.toHaveBeenCalled()
	})

	it('closed, is marked seen and changes nothing else', async () => {
		const { vm, announce, close } = box({ enabled: true, notice_seen: false })

		await announce()
		close()

		expect(vm.$api.sys.setTelemetry).toHaveBeenCalledTimes(1)
		expect(vm.$api.sys.setTelemetry).toHaveBeenCalledWith({ notice_seen: true })
	})

	it('opens the preview from See what is sent, and is marked seen', async () => {
		const { vm, announce, click } = box({ enabled: true, notice_seen: false })

		await announce()
		click('See what is sent')

		expect(vm.$buefy.modal.open).toHaveBeenCalledWith(expect.objectContaining({ component: TelemetryPreviewModal }))
		expect(vm.$api.sys.setTelemetry).toHaveBeenCalledTimes(1)
		expect(vm.$api.sys.setTelemetry).toHaveBeenCalledWith({ notice_seen: true })
	})

	it('turns statistics off from Turn off, in the request that marks it seen, and moves the Settings switch', async () => {
		const { vm, announce, click } = box({ enabled: true, notice_seen: false })

		await announce()
		click('Turn off')
		await flushPromises()

		expect(vm.$api.sys.setTelemetry).toHaveBeenCalledTimes(1)
		expect(vm.$api.sys.setTelemetry).toHaveBeenCalledWith({ notice_seen: true, enabled: false })
		expect(vm.$EventBus.$emit).toHaveBeenCalledWith(events.TELEMETRY_CHANGED, false)
	})
})
```

- [ ] **Step 2: Run it and watch it fail**

```bash
npx -y pnpm@9.0.6 exec vitest run src/components/CoreService.spec.js
```

Expected: `Tests  8 failed | 6 passed (14)`. The 6 existing tests pass. `is asked for when the dashboard opens` fails its `toHaveBeenCalledTimes(1)` assertion (called 0 times), because `mounted()` only calls `announceBackupFailures()`. The other 7 new ones fail with `TypeError: Cannot read properties of undefined (reading 'bind')`, because `CoreService.methods.showTelemetryPreview` does not exist yet.

- [ ] **Step 3: Implement**

In `src/events/events.js`, replace lines 32–33:

```js
	CLOSE_APP_IFRAME: 'closeAppIframe',
}
```

with:

```js
	CLOSE_APP_IFRAME: 'closeAppIframe',
	// CoreService -> TopBar: the core's `enabled` after the notice's PUT
	TELEMETRY_CHANGED: 'telemetryChanged',
}
```

In `src/components/CoreService.vue`:

(a) Replace line 23:

```js
import { Swiper, SwiperSlide } from 'swiper/vue'
```

with:

```js
import { h } from 'vue'
import { Swiper, SwiperSlide } from 'swiper/vue'
```

(b) Replace line 33:

```js
import DiskLearnMore from '@/components/Storage/DiskLearnMore.vue'
```

with:

```js
import DiskLearnMore from '@/components/Storage/DiskLearnMore.vue'
import TelemetryPreviewModal from '@/components/settings/TelemetryPreviewModal.vue'
```

(c) Replace `mounted()` (lines 108–110):

```js
	mounted() {
		this.announceBackupFailures()
	},
```

with:

```js
	mounted() {
		this.announceBackupFailures()
		this.announceTelemetry()
	},
```

(d) After line 148 (the `},` that closes `announceBackupFailures`, just after `rememberBackupFailures(seen)` and before `_isValidDiskEvent(evt) {`), insert:

```js

		// Statistics are on unless the owner turned them off, so the owner is told,
		// once per box, here where the dashboard opens. Closing the notice and both
		// of its actions mark it seen, in one request that also carries Turn off.
		// A request that fails leaves the notice unseen: it comes back next time.
		async announceTelemetry() {
			let state
			try {
				const res = await this.$api.sys.getTelemetry()
				state = res.data.data
			} catch {
				return
			}
			if (!state?.enabled || state.notice_seen)
				return

			const change = { notice_seen: true }
			const notice = this.$buefy.notification.open({
				position: 'is-bottom-right',
				indefinite: true,
				queue: false,
				ariaCloseLabel: this.$t('Close'),
				message: [
					h('p', this.$t('ReCasaOS sends anonymous statistics (versions, hardware, country).')),
					h('div', { class: 'buttons mt-3' }, [
						h('button', {
							type: 'button',
							class: 'button is-small is-rounded is-light',
							onClick: () => {
								this.showTelemetryPreview()
								notice.close()
							},
						}, this.$t('See what is sent')),
						h('button', {
							type: 'button',
							class: 'button is-small is-rounded is-light is-outlined',
							onClick: () => {
								change.enabled = false
								notice.close()
							},
						}, this.$t('Turn off')),
					]),
				],
				// The Settings switch in TopBar then shows what the core now has.
				onClose: () => {
					this.$api.sys.setTelemetry(change)
						.then(res => this.$EventBus.$emit(events.TELEMETRY_CHANGED, res.data.data.enabled))
						.catch(() => {})
				},
			})
		},

		showTelemetryPreview() {
			this.$buefy.modal.open({
				component: TelemetryPreviewModal,
				hasModalCard: true,
				trapFocus: true,
				canCancel: ['escape', 'outside'],
				scroll: 'keep',
				animation: 'zoom-in',
			})
		},
```

In `src/assets/lang/en_US.json`, replace the last two lines:

```json
  "The preview could not be loaded.": "The preview could not be loaded."
}
```

with:

```json
  "The preview could not be loaded.": "The preview could not be loaded.",
  "ReCasaOS sends anonymous statistics (versions, hardware, country).": "ReCasaOS sends anonymous statistics (versions, hardware, country).",
  "See what is sent": "See what is sent",
  "Turn off": "Turn off"
}
```

In `src/assets/lang/fr_FR.json`, replace the last two lines:

```json
  "The preview could not be loaded.": "L'aperçu n'a pas pu être chargé."
}
```

with:

```json
  "The preview could not be loaded.": "L'aperçu n'a pas pu être chargé.",
  "ReCasaOS sends anonymous statistics (versions, hardware, country).": "ReCasaOS envoie des statistiques anonymes (versions, matériel, pays).",
  "See what is sent": "Voir ce qui est envoyé",
  "Turn off": "Désactiver"
}
```

- [ ] **Step 4: Run it and watch it pass; the smoke mount still boots CoreService without a warning**

```bash
npx -y pnpm@9.0.6 exec vitest run src/components/CoreService.spec.js src/__tests__/mount.spec.js
npx eslint src/events/events.js src/components/CoreService.vue src/components/CoreService.spec.js --quiet
node -e "const en=require('./src/assets/lang/en_US.json'),fr=require('./src/assets/lang/fr_FR.json');const keys=['ReCasaOS sends anonymous statistics (versions, hardware, country).','See what is sent','Turn off'];const miss=keys.filter(k=>!en[k]||!fr[k]);if(miss.length){console.error(miss);process.exit(1)}console.log('lang ok')"
```

Expected: `Test Files  2 passed (2)`, including `14 passed` in CoreService.spec.js and `mounts CoreService` in mount.spec.js. ESLint prints nothing. The node check prints `lang ok`.

- [ ] **Step 5: Commit**

```bash
git add src/events/events.js src/components/CoreService.vue src/components/CoreService.spec.js src/assets/lang/en_US.json src/assets/lang/fr_FR.json
git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(home): say once, after login, that anonymous statistics are on"
```

---

### Task 4: The Settings switch, and the whole-suite gates

**Files:**
- Modify: `src/components/TopBar.vue`. It gets a template row after line 259, an import after line 401, a data field after line 446, `mounted()` at lines 535–540 plus a new `beforeUnmount()` right after it, and methods after line 696, the `},` closing `usbAutoMount`. `events` is already imported (line 406). `onOpen()` is not touched.
- Modify: `src/assets/lang/en_US.json`, `src/assets/lang/fr_FR.json` (the last entry, added in Task 3, and `}`)
- Test: `src/components/TopBar.spec.js` (whole file replaced; it is 12 lines today)

**Interfaces:**
- Consumes: `this.$api.sys.getTelemetry()`, `this.$api.sys.setTelemetry(data)` (Task 1), `TelemetryPreviewModal` (Task 2), `events.TELEMETRY_CHANGED` and its `enabled: boolean` payload (Task 3), `this.$EventBus.$on/$off(type, handler)` (`src/events/eventBus.js`), and `this.$refs.settingsDrop` (the existing Settings `b-dropdown`).
- Produces: `TopBar` data `telemetryEnabled: boolean | null` (null until the core answers; the row stays hidden while it is null); the methods `getTelemetry(): void`, `onTelemetryChanged(enabled: boolean): void`, `setTelemetry(enabled: boolean): Promise<void>` and `showTelemetryPreview(): void`; `mounted()` calls `getTelemetry()` and subscribes `onTelemetryChanged` to `events.TELEMETRY_CHANGED`, and `beforeUnmount()` unsubscribes that same reference.

- [ ] **Step 1: Write the failing test** — replace the whole of `src/components/TopBar.spec.js` with:

```js
// @vitest-environment happy-dom
import { readFileSync } from 'node:fs'
import { flushPromises } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import TopBar from './TopBar.vue'
import TelemetryPreviewModal from './settings/TelemetryPreviewModal.vue'
import createEventBus from '@/events/eventBus'
import events from '@/events/events'

// The locale table is assembled with webpack's `require.context`, which Vite has
// no equivalent for; TopBar only reads the language names from it.
vi.mock('@/assets/lang', () => ({ default: { en_us: { lang_name: 'English' } } }))

// From the project root: import.meta.url is not a file URL under happy-dom.
const source = readFileSync('src/components/TopBar.vue', 'utf8')

// The settings menu is a Buefy dropdown whose slot a shallow mount never
// renders, and a full mount of TopBar needs more scaffolding than the row is
// worth (see __tests__/mount.spec.js), so the template is checked as text.
describe('top bar settings', () => {
	it('has no news feed row', () => {
		expect(source).not.toMatch(/news feed|rss/i)
	})
})

// The same reason keeps the methods and hooks off a mounted TopBar: they run
// against the few fields they touch.
describe('the anonymous statistics switch', () => {
	// sys: the slice of $api.sys the test drives; telemetryEnabled: where the
	// switch starts (null until the core has answered).
	function bar(sys, telemetryEnabled = true) {
		const vm = {
			telemetryEnabled,
			$t: key => key,
			$api: { sys },
			$EventBus: createEventBus(),
			$buefy: { toast: { open: vi.fn() }, modal: { open: vi.fn() } },
			$refs: { settingsDrop: { toggle: vi.fn() } },
			// The rest of mounted(), which these tests do not look at.
			checkVersion: vi.fn(),
			getUserInfo: vi.fn(),
			getUsbStatus: vi.fn(),
			getHardwareInfo: vi.fn(),
		}
		for (const name of ['getTelemetry', 'onTelemetryChanged', 'setTelemetry', 'showTelemetryPreview'])
			vm[name] = TopBar.methods[name].bind(vm)

		return vm
	}

	it('is wired to setTelemetry', () => {
		expect(source).toMatch(/:model-value="telemetryEnabled"[^>]*@update:model-value="setTelemetry"/)
	})

	it('asks the core when the bar mounts, and follows the notice\'s Turn off until it unmounts', async () => {
		const getTelemetry = vi.fn(() => Promise.resolve({
			data: { success: 200, data: { enabled: true, notice_seen: false, preview: { event: 'heartbeat', properties: {} } } },
		}))
		const vm = bar({ getTelemetry }, null)

		TopBar.mounted.call(vm)
		await flushPromises()

		expect(getTelemetry).toHaveBeenCalledTimes(1)
		expect(vm.telemetryEnabled).toBe(true)

		vm.$EventBus.$emit(events.TELEMETRY_CHANGED, false)
		expect(vm.telemetryEnabled).toBe(false)

		TopBar.beforeUnmount.call(vm)
		vm.$EventBus.$emit(events.TELEMETRY_CHANGED, true)
		expect(vm.telemetryEnabled).toBe(false)
	})

	it('shows no row when the core cannot be asked', async () => {
		const vm = bar({ getTelemetry: vi.fn(() => Promise.reject(new Error('offline'))) }, null)

		vm.getTelemetry()
		await flushPromises()

		expect(vm.telemetryEnabled).toBe(null)
		expect(source).toMatch(/v-if="telemetryEnabled !== null"/)
	})

	it('sends enabled, marks the notice seen, and shows what the core answered', async () => {
		const setTelemetry = vi.fn(() => Promise.resolve({
			data: { success: 200, data: { enabled: false, notice_seen: true, preview: { event: 'heartbeat', properties: {} } } },
		}))
		const vm = bar({ setTelemetry })

		await vm.setTelemetry(false)

		expect(setTelemetry).toHaveBeenCalledWith({ enabled: false, notice_seen: true })
		expect(vm.telemetryEnabled).toBe(false)
	})

	it('goes back where it was when the core refuses', async () => {
		const vm = bar({ setTelemetry: vi.fn(() => Promise.reject(new Error('offline'))) })

		await vm.setTelemetry(false)

		expect(vm.telemetryEnabled).toBe(true)
		expect(vm.$buefy.toast.open).toHaveBeenCalledTimes(1)
	})

	it('opens the preview from See what is sent', () => {
		const vm = bar({})

		vm.showTelemetryPreview()

		expect(vm.$buefy.modal.open).toHaveBeenCalledWith(expect.objectContaining({ component: TelemetryPreviewModal }))
	})
})
```

- [ ] **Step 2: Run it and watch it fail**

```bash
npx -y pnpm@9.0.6 exec vitest run src/components/TopBar.spec.js
```

Expected: `Tests  6 failed | 1 passed (7)`. `has no news feed row` passes. `is wired to setTelemetry` fails on `toMatch`. The other five fail with `TypeError: Cannot read properties of undefined (reading 'bind')`, because `TopBar.methods.getTelemetry` does not exist yet.

- [ ] **Step 3: Implement** — in `src/components/TopBar.vue`:

(a) After line 259 (the blank line after `<!-- Automount USB Drive End  -->`, just before `<!-- Update Start -->`), insert:

```html
					<!-- Anonymous statistics Start -->
					<div v-if="telemetryEnabled !== null" class="_is-large hover-effect _is-radius pr-2 mr-4 ml-4">
						<div class="is-flex is-align-items-center">
							<div class="is-flex is-align-items-center is-flex-grow-1 _is-normal">
								<b-icon class="mr-1 ml-2" custom-size="mdi-20px" icon="chart-box-outline" />
								{{ $t("Anonymous usage statistics") }}
							</div>
							<div>
								<b-field>
									<b-switch :model-value="telemetryEnabled"
										class="is-flex-direction-row-reverse mr-0 _small"
										type="is-dark"
										@update:model-value="setTelemetry" />
								</b-field>
							</div>
						</div>
						<div class="pl-55 ml-1 is-size-7">
							<a href="#" @click.prevent="showTelemetryPreview">{{ $t("See what is sent") }}</a>
						</div>
					</div>
					<!-- Anonymous statistics End -->

```

(b) After line 401 (`import AppLaunchModal from './settings/AppLaunchModal.vue'`), insert:

```js
import TelemetryPreviewModal from './settings/TelemetryPreviewModal.vue'
```

(c) After line 446 (`autoUsbMount: false,` in `data()`), insert:

```js
			// null until the core answers GET /v1/sys/telemetry: a core without the
			// route shows no switch that could not work
			telemetryEnabled: null,
```

(d) Replace `mounted()` (lines 535–540):

```js
	mounted() {
		this.checkVersion()
		this.getUserInfo()
		this.getUsbStatus()
		this.getHardwareInfo()
	},
```

with:

```js
	mounted() {
		this.checkVersion()
		this.getUserInfo()
		this.getUsbStatus()
		this.getTelemetry()
		this.getHardwareInfo()
		// the notice's Turn off (CoreService) moves the switch too
		this.$EventBus.$on(events.TELEMETRY_CHANGED, this.onTelemetryChanged)
	},
	beforeUnmount() {
		this.$EventBus.$off(events.TELEMETRY_CHANGED, this.onTelemetryChanged)
	},
```

(e) After line 696 (the `},` that closes `usbAutoMount()`, just before the `getHardwareInfo` doc comment), insert:

```js

		/*************************************************
		 * PART 1-4b  Dashboard Setting - Anonymous statistics
		 **************************************************/
		// Asked once, from mounted(). The row stays hidden until the core answers,
		// so the switch cannot be flipped while this is in flight.
		getTelemetry() {
			this.$api.sys.getTelemetry().then((res) => {
				this.telemetryEnabled = res.data.data.enabled
			}).catch(() => {})
		},

		// The core's `enabled` after the notice's PUT (CoreService).
		onTelemetryChanged(enabled) {
			this.telemetryEnabled = enabled
		},

		// Using the switch shows the owner knows, so it also marks the notice seen:
		// an owner who opts in here is not told about it at the next login.
		// The switch ends where the core says it is: a refused PUT puts it back.
		async setTelemetry(enabled) {
			this.telemetryEnabled = enabled
			try {
				const res = await this.$api.sys.setTelemetry({ enabled, notice_seen: true })
				this.telemetryEnabled = res.data.data.enabled
			} catch {
				this.telemetryEnabled = !enabled
				this.$buefy.toast.open({
					message: this.$t('The setting could not be saved.'),
					type: 'is-danger',
				})
			}
		},

		showTelemetryPreview() {
			this.$refs.settingsDrop.toggle()
			this.$buefy.modal.open({
				component: TelemetryPreviewModal,
				hasModalCard: true,
				trapFocus: true,
				canCancel: ['escape', 'outside'],
				scroll: 'keep',
				animation: 'zoom-in',
			})
		},
```

In `src/assets/lang/en_US.json`, replace the last two lines:

```json
  "Turn off": "Turn off"
}
```

with:

```json
  "Turn off": "Turn off",
  "Anonymous usage statistics": "Anonymous usage statistics",
  "The setting could not be saved.": "The setting could not be saved."
}
```

In `src/assets/lang/fr_FR.json`, replace the last two lines:

```json
  "Turn off": "Désactiver"
}
```

with:

```json
  "Turn off": "Désactiver",
  "Anonymous usage statistics": "Statistiques d'utilisation anonymes",
  "The setting could not be saved.": "Le réglage n'a pas pu être enregistré."
}
```

- [ ] **Step 4: Run it and watch it pass; the smoke mount still boots TopBar without a warning**

```bash
npx -y pnpm@9.0.6 exec vitest run src/components/TopBar.spec.js src/__tests__/mount.spec.js
npx eslint src/components/TopBar.vue src/components/TopBar.spec.js --quiet
```

Expected: `Test Files  2 passed (2)`, including `7 passed` in TopBar.spec.js and `mounts TopBar` in mount.spec.js (its `$EventBus` is a real `createEventBus()`, so the subscribe in `mounted()` and the unsubscribe on unmount both run). ESLint prints nothing; TopBar.vue's existing warnings are hidden by `--quiet`.

- [ ] **Step 5: Whole-suite gates**

```bash
node -e "const en=require('./src/assets/lang/en_US.json'),fr=require('./src/assets/lang/fr_FR.json');const keys=['What is sent','When statistics are on, these properties go to PostHog (EU) once a day and after each install or update.','The preview could not be loaded.','ReCasaOS sends anonymous statistics (versions, hardware, country).','See what is sent','Turn off','Anonymous usage statistics','The setting could not be saved.'];const miss=keys.filter(k=>!en[k]||!fr[k]);if(miss.length){console.error(miss);process.exit(1)}console.log('lang ok')"
npx -y pnpm@9.0.6 exec vitest run > "$TEMP/ui-vitest.log" 2>&1; echo "vitest exit=$?"; tail -6 "$TEMP/ui-vitest.log"
npx eslint . --quiet; echo "eslint exit=$?"
git diff --check inkly/main
```

Expected: `lang ok`, then `vitest exit=0` with `Test Files  57 passed (57)` and `Tests  535 passed (535)`. That is the baseline of 55/516 plus 2 new files and 19 new tests (2 in `sys.spec.js`, 3 in `TelemetryPreviewModal.spec.js`, 8 in `CoreService.spec.js`, 6 in `TopBar.spec.js`). Then `eslint exit=0` with no other output, and `git diff --check` printing nothing. If vitest fails, read `$TEMP/ui-vitest.log` rather than running it again. CI (`.github/workflows/ci.yml`) also runs `pnpm build`. Nothing here needs a new dependency or a new asset, so the build is left to CI.

- [ ] **Step 6: Commit**

```bash
git add src/components/TopBar.vue src/components/TopBar.spec.js src/assets/lang/en_US.json src/assets/lang/fr_FR.json
git -c user.name=Gary -c user.email=contact@ixelia.fr commit -m "feat(settings): a switch for anonymous usage statistics, and a look at what is sent"
git log --oneline inkly/main..HEAD
```

Expected: 4 commits on `feat/telemetry`, none carrying a `Co-Authored-By` trailer. Do not push, merge or tag: the controller releases the dashboard after review, in the ship order of the contract.

---

## Spec coverage

| Spec requirement (CasaOS-UI) | Where |
|---|---|
| API client for `GET`/`PUT /v1/sys/telemetry` | Task 1 (`sys.getTelemetry`, `sys.setTelemetry`, `sys.spec.js`) |
| Notice once per box, after login, when statistics are on and `notice_seen` is false | Task 3 (`announceTelemetry` from `CoreService.mounted()`); tests "is asked for when the dashboard opens", "is shown…" and "is not shown when…" |
| Notice text "ReCasaOS sends anonymous statistics (versions, hardware, country)" | Task 3; test "is shown…" checks the sentence |
| *See what is sent* in the notice opens the preview | Task 3 (`showTelemetryPreview`); test "opens the preview from See what is sent" |
| *Turn off* sends `enabled: false` | Task 3; test "turns statistics off from Turn off…" |
| Closing the notice, or either action, sets `notice_seen` | Task 3 (`onClose` is the only place that sends); tests "closed…", "opens the preview…", "turns statistics off…" |
| Upgraded boxes see the notice too | Covered by the condition: the core reports `notice_seen: false` on a box that has not seen it, and the dashboard makes no distinction |
| Settings switch "Anonymous usage statistics" sends `enabled` | Task 4 (`setTelemetry`, which also sends `notice_seen: true`); tests "is wired to setTelemetry", "sends enabled, marks the notice seen…" |
| The switch shows what the core has, after the notice's *Turn off* too | Task 3 (emits `events.TELEMETRY_CHANGED`) and Task 4 (`onTelemetryChanged`, subscribed in `mounted()`, removed in `beforeUnmount()`); tests "turns statistics off from Turn off… moves the Settings switch" and "asks the core when the bar mounts, and follows the notice's Turn off…" |
| Settings *See what is sent* link opens the modal | Task 4 (`showTelemetryPreview`); test "opens the preview from See what is sent" |
| Modal shows the exact JSON properties, built at that moment by the sender's function | Task 2 (the modal fetches on open); test "shows the properties the core answers, as JSON" |
| Modal has one sentence naming PostHog (EU) and linking the README section | Task 2; test "names PostHog (EU) and links the README section" |
| All strings in the language files, English and French at least | Tasks 2–4 (8 keys in `en_US.json` and `fr_FR.json`, checked by the `node -e` gates; the other 29 languages fall back to `en_us`) |
| The dashboard sends nothing to PostHog | By construction: the only new requests go to `/v1/sys/telemetry` (Task 1 test) |
| Older core: a failed GET means no notice and no Settings row | Task 3 test "is not shown when the core cannot be asked"; Task 4 test "shows no row when the core cannot be asked" |
| Vitest: notice only when enabled and not seen; each action sends `notice_seen`; *Turn off* also sends `enabled: false` | Task 3 spec |
| Vitest: switch sends `enabled`; preview modal renders the API's properties | Task 4 spec, Task 2 spec |
