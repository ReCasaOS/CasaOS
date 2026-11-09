# Suivre une sauvegarde en cours — plan d'implémentation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** l'interface montre chaque sauvegarde qui tourne (quoi, vitesse, combien sur combien, %), la file du passage en cours, et permet d'arrêter une sauvegarde.

**Architecture:** l'AM tient en mémoire un registre des sauvegardes vivantes, nourri par les compteurs de rclone (`core/stats` du job) et de restic (`backup --json`) via un rappel porté par le contexte. Il tient aussi l'état du passage du planificateur. Deux routes l'exposent. L'UI les lit : carte du tableau de bord courte, et onglet « En cours » dans la fenêtre Sauvegardes.

**Tech Stack:** Go 1.26 (echo, oapi-codegen v1.12.4, resty), Vue 3 + Buefy, vitest.

**Spec:** `docs/superpowers/specs/2026-11-09-backup-live-progress-design.md` (même dépôt, branche `docs/backup-live-progress`).

## Global Constraints

- Dossiers : AM = `D:\clients\casaos\_live-am` (branche `feat/backup-live-progress`, base inkly/main a28b4d5) ; UI = `D:\clients\casaos\_next-ui`, nouvelle branche `feat/backup-live-progress` créée depuis `fix/backup-next-while-running` (55a635ab).
- Commits : messages en anglais, style `feat(backup): …` ; **aucun trailer `Co-Authored-By`** ; jamais « Generated with Claude Code » dans une PR (la CI ReCasaOS échoue).
- Tests Go : le paquet `service` ne compile pas sous Windows. Toujours dans un conteneur :
  `MSYS_NO_PATHCONV=1 docker run --rm -v "D:/clients/casaos/_live-am:/src" -v casaos-gomod:/go/pkg/mod -w /src golang:1.26 go test <paquets> -run '<regex>'`
- Référence d'échecs Go préexistants (68 tests d'apps git : git ne lit pas le `.git` d'un worktree Windows dans le conteneur) : `C:\Users\garyd\AppData\Local\Temp\claude\9338d8bc1ec166f4\e6a369bc-6bee-4292-83b8-9ce10328f932\scratchpad\am-baseline-failures.txt`. Une suite complète est verte si ses `--- FAIL` sont un sous-ensemble de cette liste.
- Code généré : `go generate` lancé sous Windows dans `_live-am` (fonctionne) ; `codegen/` n'est pas versionné.
- Tests UI : `export PATH="/c/Users/garyd/AppData/Local/hermes/node:$PATH"; corepack pnpm exec vitest run <fichiers>` ; avant commit, `corepack pnpm exec eslint <fichiers touchés>` (titres `describe` en minuscule).
- Traductions : clés en anglais, ajoutées à `en_US.json` (clé = valeur) et `fr_FR.json` seulement.
- JSON des routes nouvelles : sans enveloppe ; horodatages RFC 3339 ; durées et ETA en secondes entières ; vitesse en octets/s ; champ inconnu **absent** (`omitempty`, pointeurs), jamais `null` ni 0 pour « inconnu ».
- Le suivi ne ralentit ni ne fait échouer une copie : toute erreur de `core/stats` ou de lecture JSON restic est avalée (journalisée au plus une fois par run).
- Pas d'appel réseau sous le verrou du registre.

## Review Focus

1. Arrêter une sauvegarde « tenue immobile » (conteneurs ou services arrêtés) : l'app ou les services repartent. → test dans Task 4.
2. Un `rclone rcd` qui répond mal à `core/stats` (erreur, JSON vide, `eta: null`) : la copie se termine normalement, sans stats. → tests dans Task 2.
3. Deux sauvegardes en même temps (une à la demande pendant un passage planifié) : deux lignes, deux `id` ; arrêter l'une n'arrête pas l'autre. → test dans Task 4.
4. Sortie restic avec lignes non JSON, très longues (> 64 Ko, `current_files` profonds) ou messages inconnus : ignorées, la sauvegarde finit. → test dans Task 3.
5. AM ancien (404 sur `/backup/running`) : la carte et la fenêtre se comportent comme aujourd'hui, sans onglet ni lien. → tests dans Task 8 et Task 9.

---

## Partie AM (`_live-am`)

### Task 1 : le paquet `pkg/backupprogress`

**Files:**
- Create: `pkg/backupprogress/progress.go`
- Test: `pkg/backupprogress/progress_test.go`

**Interfaces:**
- Produces :
  ```go
  type Stats struct {
      Bytes      int64   `json:"bytes"`
      TotalBytes int64   `json:"total_bytes,omitempty"` // 0 = inventaire en cours
      Files      int64   `json:"files"`
      TotalFiles int64   `json:"total_files,omitempty"`
      Speed      int64   `json:"speed,omitempty"`        // octets/s
      ETA        *int64  `json:"eta,omitempty"`          // secondes ; nil = inconnu
      Current    string  `json:"current,omitempty"`
  }
  func With(ctx context.Context, report func(Stats)) context.Context
  func Report(ctx context.Context, stats Stats) // sans rappel : ne fait rien
  func Wanted(ctx context.Context) bool
  ```

- [ ] **Step 1 : tests** `TestReportReachesTheCallbackOfTheContext` (With puis Report → le rappel reçoit les mêmes Stats), `TestReportWithoutACallbackDoesNothing` (Report sur `context.Background()` ne panique pas ; `Wanted` = false), `TestAZeroTotalIsLeftOutOfTheJSON` (`json.Marshal(Stats{Bytes: 5})` = `{"bytes":5,"files":0}`).
- [ ] **Step 2 :** lancer `go test ./pkg/backupprogress/` (conteneur) → FAIL (paquet absent).
- [ ] **Step 3 :** implémenter (clé de contexte non exportée).
- [ ] **Step 4 :** relancer → PASS.
- [ ] **Step 5 :** commit `feat(backup): a progress callback carried by the context`.

### Task 2 : rclone rapporte les compteurs du job

**Files:**
- Modify: `pkg/rclone/copy.go` (`waitForJob`, + `jobStats`)
- Test: `pkg/rclone/copy_internal_test.go`

**Interfaces:**
- Consumes : `backupprogress.Wanted/Report/Stats` (Task 1).
- Produces : `func (c *Client) jobStats(ctx context.Context, id int64) (backupprogress.Stats, error)` — POST `/core/stats` avec `group=job/<id>` ; mappe `bytes`→Bytes, `totalBytes`→TotalBytes, `transfers`→Files, `totalTransfers`→TotalFiles, `speed` (float)→Speed arrondi, `eta` (nombre ou null)→ETA, `transferring[0].name`→Current.

- [ ] **Step 1 : tests** (serveur `serverFor` existant, `fastPolling`) :
  - `TestAJobWithAReporterReportsItsStatsWhileItRuns` : `/core/stats` exige `group=job/7` et répond `{"bytes":1182844747,"totalBytes":1926565931,"transfers":9812,"totalTransfers":25488,"speed":219371.14,"eta":3390,"transferring":[{"name":"cache/a.jpg"}]}` ; le rappel reçoit `Stats{Bytes:1182844747, TotalBytes:1926565931, Files:9812, TotalFiles:25488, Speed:219371, ETA:&3390, Current:"cache/a.jpg"}` au moins une fois.
  - `TestStatsThatFailDoNotStopTheCopy` : `/core/stats` répond 500 puis `{}` puis `{"eta":null}` ; `CopyDirectory` rend `nil`.
  - `TestNoStatsAreAskedWithoutAReporter` : sans `backupprogress.With`, aucun appel à `/core/stats`.
- [ ] **Step 2 :** `go test ./pkg/rclone/ -run 'Stats|Reporter'` → FAIL.
- [ ] **Step 3 :** dans `waitForJob`, après un statut non fini et si `backupprogress.Wanted(ctx)`, appeler `jobStats` et `Report` ; erreur ignorée.
- [ ] **Step 4 :** `go test ./pkg/rclone/` → PASS (tout le paquet).
- [ ] **Step 5 :** commit `feat(backup): rclone copies report their job's stats`.

### Task 3 : restic rapporte ses `status`

**Files:**
- Modify: `pkg/restic/engine.go` (champ `stream`, `execStreamer`, `backup`)
- Create: `pkg/restic/progress.go` (`parseStatus`)
- Test: `pkg/restic/engine_internal_test.go`, `pkg/restic/progress_internal_test.go`

**Interfaces:**
- Consumes : Task 1.
- Produces :
  ```go
  type streamer func(ctx context.Context, env []string, line func([]byte), args ...string) error
  func execStreamer(ctx context.Context, env []string, line func([]byte), args ...string) error
  func parseStatus(line []byte) (backupprogress.Stats, bool)
  ```
  `Engine` gagne `stream streamer` ; `New()` le règle à `execStreamer` ; le `fake` des tests (`engineWith`) en fournit un.

- [ ] **Step 1 : tests**
  - `TestAStatusLineBecomesStats` : `{"message_type":"status","seconds_elapsed":10,"seconds_remaining":56,"percent_done":0.61,"total_files":25488,"files_done":9812,"total_bytes":1926565931,"bytes_done":1182844747,"current_files":["/DATA/x.jpg"]}` → `Stats{Bytes:1182844747, TotalBytes:1926565931, Files:9812, TotalFiles:25488, Speed:118284474, ETA:&56, Current:"/DATA/x.jpg"}`, true.
  - `TestTotalsStayUnknownWhileResticScans` : même ligne **sans** `seconds_remaining` → `TotalBytes`/`TotalFiles` = 0, `ETA` nil.
  - `TestOtherLinesAreNotStatus` : `{"message_type":"summary",…}`, `not json`, ligne vide → false.
  - `TestABackupWithAReporterStreamsJSON` : avec `backupprogress.With`, le fake `stream` voit `--json` et pas `--quiet`, émet deux lignes status et une de 100 Ko de texte ; le rappel reçoit deux Stats ; `CopyDirectory` rend nil.
  - `TestABackupWithoutAReporterIsUnchanged` : sans rappel, l'appel passe par `run` avec `--quiet` comme aujourd'hui.
  - `TestAKilledBackupDoesNotLockTheNext` : `t.Skip` si `!Available()` ; dépôt local temporaire, backup d'un gros dossier annulé après 1 s, puis backup du même dossier → réussit en moins de 30 s.
- [ ] **Step 2 :** `go test ./pkg/restic/` → FAIL.
- [ ] **Step 3 :** `execStreamer` : `StdoutPipe` + `bufio.Scanner` avec tampon max 1 Mo (ligne plus longue : ignorée, la lecture continue) ; stderr gardé comme `execRunner` (6 dernières lignes → `ExitError`). `backup` : si `Wanted(ctx)`, appel par `stream` avec les mêmes env/`--retry-lock 5m`, `--json` au lieu de `--quiet`, chaque ligne passée à `parseStatus` puis `Report`. Vitesse = `bytes_done / seconds_elapsed` (0 si elapsed = 0). Totaux seulement si `seconds_remaining` est présent.
- [ ] **Step 4 :** `go test ./pkg/restic/` → PASS ; puis une fois dans le conteneur avec restic : `… golang:1.26 sh -c 'apt-get update -qq && apt-get install -y -qq restic >/dev/null && go test ./pkg/restic/ -run Killed -v'` → PASS (pas SKIP). Si le verrou bloque : `cmd.Cancel` envoie SIGINT et `cmd.WaitDelay = 10 * time.Second`, relancer.
- [ ] **Step 5 :** commit `feat(backup): restic backups report their status`.

### Task 4 : le registre des sauvegardes vivantes, et Arrêter

**Files:**
- Create: `service/backup_live.go`
- Modify: `service/backup_run.go` (`RunBackup`, `copyPlan`), `service/backup_system.go` (`RunSystemBackup`), `service/backup_events.go` (`publishBackupProgress` reçoit une fraction), `service/backup_scheduler.go` (`runScheduled` : origine), `service/backup_force.go` (origine), `route/v2/backup.go` (rien : défaut `on_demand`)
- Test: `service/backup_live_internal_test.go`

**Interfaces:**
- Consumes : Task 1.
- Produces :
  ```go
  type BackupOrigin string // "scheduled" | "forced" | "on_demand"
  const OriginScheduled, OriginForced, OriginOnDemand BackupOrigin
  func WithBackupOrigin(ctx context.Context, origin BackupOrigin) context.Context // absent = on_demand
  type LiveBackup struct {
      ID, App, Destination, Stamp string; Origin BackupOrigin; StartedAt time.Time
      Operation, Operations int; Source string
      Stats *backupprogress.Stats; StatsAt time.Time
  }
  func LiveBackups() []LiveBackup               // copies, triées par StartedAt puis ID
  func StopLiveBackup(id string) error          // ErrLiveBackupNotFound si absent
  var ErrLiveBackupNotFound, ErrStoppedFromInterface error // "stopped from the interface"
  func beginLive(ctx context.Context, app, destination, stamp string) (context.Context, func())
  func liveOperation(ctx context.Context, operation, operations int, source string)
  ```
- `beginLive` : `context.WithCancelCause` ; `StopLiveBackup` annule avec `ErrStoppedFromInterface` ; installe `backupprogress.With` qui met à jour `Stats`/`StatsAt` (horloge `backupClock`) et publie `backup:progress` au plus toutes les 5 s avec `(operation-1 + bytes/total_bytes) / (operations+1)` (fraction de l'opération = 0 sans total). `liveOperation` remet `Stats` à nil. `RunBackup`/`RunSystemBackup` appellent `beginLive` juste après `markInProgress` (avant toute tenue immobile) et, si `context.Cause(ctx) == ErrStoppedFromInterface`, rendent une erreur qui l'enveloppe (`errors.Is` vrai) — c'est elle que `runs.json` enregistre.
- `publishBackupProgress(ctx, done, total int, current string)` garde sa signature ; une nouvelle `publishBackupFraction(ctx, fraction float64, current string)` sert au rappel. `copyPlan` appelle `liveOperation(ctx, i+1, len(ops), operation.Source)`.

- [ ] **Step 1 : tests** (fakes de copieur et de docker existants dans `backup_run_internal_test.go` / `backup_scheduler_internal_test.go`) :
  - `TestARunningBackupIsListedWithItsOperationAndStats` : un copieur qui appelle `backupprogress.Report` puis attend un canal ; pendant l'attente `LiveBackups()` a une entrée `{App, Destination, Stamp, Origin:"on_demand", Operation:1, Operations:n, Source}` avec les Stats rapportées ; après, la liste est vide.
  - `TestStoppingABackupEndsItAndSaysWhy` : `StopLiveBackup(id)` pendant la copie (le copieur rend `ctx.Err()` à l'annulation) → `RunBackup` rend une erreur `errors.Is(err, ErrStoppedFromInterface)` ; `BackupOnDemand` écrit un record dont `Error` contient « stopped from the interface ».
  - `TestStoppingAHeldBackupBringsTheAppBack` (Review Focus 1) : `HoldStill: true`, arrêt pendant la copie → les conteneurs arrêtés sont redémarrés (assertion sur le fake docker, comme les tests de tenue existants).
  - `TestTwoBackupsAreListedApartAndStopApart` (Review Focus 3) : deux `RunBackup` concurrents, ID distincts ; arrêter le premier, le second finit sans erreur.
  - `TestStoppingAnUnknownBackupIsNotFound` : `errors.Is(StopLiveBackup("nope"), ErrLiveBackupNotFound)`.
  - `TestProgressEventsCarryTheFractionAtMostEveryFiveSeconds` : `backupClock` piloté ; trois Report à 0 s, 1 s, 6 s, opération 1 sur 2, 50 % des octets → deux événements, `app:progress` = `16` (`(0+0.5)/3`).
  - `TestTheSchedulerAndAForceSayWhereABackupComesFrom` : origine `scheduled` via `runScheduled`, `forced` via `ForceSchedules`.
- [ ] **Step 2 :** `go test ./service/ -run 'Live|Stopping|TwoBackups|ProgressEvents|ComesFrom'` → FAIL.
- [ ] **Step 3 :** implémenter selon Interfaces.
- [ ] **Step 4 :** `go test ./service/` → seuls des échecs de la liste de référence.
- [ ] **Step 5 :** commit `feat(backup): a register of the backups that run, and a way to stop one`.

### Task 5 : le passage du planificateur et des forçages

**Files:**
- Create: `service/backup_pass.go`
- Modify: `service/backup_scheduler.go` (`RunDueBackups`, `runScheduled` rend son `BackupRunRecord`), `service/backup_force.go` (goroutine de `ForceSchedules`)
- Test: `service/backup_pass_internal_test.go`

**Interfaces:**
- Produces :
  ```go
  type PassItemState string // "waiting" | "running" | "done" | "failed" | "stopped" | "held"
  type BackupPassItem struct {
      App, Destination string; State PassItemState
      StartedAt, FinishedAt *time.Time; Bytes int64; Error string
  }
  type BackupPass struct { Origin BackupOrigin; StartedAt time.Time; FinishedAt *time.Time; Items []BackupPassItem }
  func BackupPasses() []BackupPass // au plus deux : planifié puis forcé ; copies
  func startPass(origin BackupOrigin, couples []BackupSchedule, now time.Time) *backupPass
  func (p *backupPass) running(i int, at time.Time)
  func (p *backupPass) finished(i int, record BackupRunRecord) // done | failed | stopped (errors.Is via le texte de ErrStoppedFromInterface)
  func (p *backupPass) hold(from int)                         // held pour i >= from
  func (p *backupPass) drop(i int)                            // couple qu'un forçage exécute : retiré du passage planifié
  func (p *backupPass) end(at time.Time)
  ```
  Un nouveau passage d'une origine remplace l'ancien de la même origine.

- [ ] **Step 1 : tests** (runner factice existant `slowRunner`) :
  - `TestAPassListsWhatWaitsRunsAndIsDone` : trois couples dus, le runner bloque sur le 2e → `BackupPasses()[0].Items` = done (avec `Bytes`, `FinishedAt`), running, waiting ; à la fin `FinishedAt` posé, tous done.
  - `TestAFailedAndAStoppedItemAreToldApart` : le 1er rend une erreur ordinaire, le 2e `ErrStoppedFromInterface` → failed, stopped.
  - `TestAnUpgradeHoldsTheRestOfThePass` : la tenue de mise à niveau s'ouvre avant le 2e → items 2 et 3 `held`.
  - `TestAForcedChainIsAPassOfItsOwn` : `ForceSchedules` de deux couples pendant qu'un passage planifié existe → deux passages, origines `scheduled` et `forced`.
- [ ] **Step 2 :** `go test ./service/ -run 'Pass|ToldApart|HoldsTheRest|ForcedChain'` → FAIL.
- [ ] **Step 3 :** implémenter (verrou propre au fichier).
- [ ] **Step 4 :** `go test ./service/` → seuls des échecs de la liste de référence.
- [ ] **Step 5 :** commit `feat(backup): the scheduler's pass, what waits and what is done`.

### Task 6 : les routes

**Files:**
- Modify: `api/app_management/openapi.yaml` (deux chemins, schémas `LiveBackup`, `LiveBackupStats`, `BackupPass`, `BackupPassItem`, réponse `BackupRunningOK`)
- Create: `route/v2/backup_running.go`
- Test: `route/v2/backup_running_internal_test.go`

**Interfaces:**
- Consumes : `service.LiveBackups`, `service.BackupPasses`, `service.StopLiveBackup`, `service.ErrLiveBackupNotFound`.
- Produces : `GET /backup/running` (operationId `backupRunning`, tag « Backup methods ») → 200 `{"backups":[…],"passes":[…]}` au contrat JSON de la spec (`LiveBackup.stats` absent si nil ; `operation` 1-based) ; `POST /backup/running/{backup_id}/stop` (operationId `stopRunningBackup`, `backup_id` 1–64 caractères) → 202 `BaseResponse{message:"Stop requested"}`, 404 `ResponseNotFound`. Modèle : `/backup/verifications/{verification_id}/cancel`.

- [ ] **Step 1 :** écrire le YAML, `go generate` (Windows, `_live-am`), vérifier que `codegen/app_management_api.go` déclare `BackupRunning` et `StopRunningBackup`.
- [ ] **Step 2 : tests** : `TestTheRunningRouteSaysNothingRunsWithEmptyLists` (`{"backups":[],"passes":[]}`), `TestTheRunningRouteListsABackupAndItsPass` (un run vivant injecté via un copieur bloquant → JSON contient `"operation":1`, `"total_bytes"` absent tant que non rapporté, horodatage RFC 3339), `TestStoppingThroughTheRouteStopsTheBackup` (202 puis le run finit avec `ErrStoppedFromInterface`), `TestStoppingAnUnknownBackupIs404`.
- [ ] **Step 3 :** `go test ./route/v2/ -run Running` → FAIL ; implémenter les handlers ; relancer → PASS.
- [ ] **Step 4 :** `go vet ./...` avec `GOOS=linux` (Windows) → propre ; `go test ./service/ ./route/... ./pkg/...` (conteneur) → seuls des échecs de la liste de référence.
- [ ] **Step 5 :** commit `feat(backup): GET /backup/running and a stop route`.

---

## Partie UI (`_next-ui`, `git switch -c feat/backup-live-progress` depuis `fix/backup-next-while-running`)

### Task 7 : le service et les chiffres partagés

**Files:**
- Modify: `src/service/backup.js` ; Test: `src/service/backup.spec.js`
- Create: `src/components/backup/liveBackup.js` ; Test: `src/components/backup/liveBackup.spec.js`

**Interfaces:**
- Produces :
  - `backup.getRunning()` → GET `${PREFIX}/backup/running` ; `backup.stopRunning(id)` → POST `${PREFIX}/backup/running/${encodeURIComponent(id)}/stop`.
  - `liveFigures(backup)` → `{ percent: number|null, bytes, totalBytes, files, totalFiles, speed, eta }` où `percent = floor(100 * bytes / total_bytes)` ou `null` sans total ni stats.
  - `liveTitle(app, titleOf, t)` → `t('This box')` pour `casaos-system`, sinon `titleOf(app)`.

- [ ] **Step 1 : tests** : chemins et verbes des deux appels ; `liveFigures` sur l'exemple de la spec → `percent: 61` ; sans `total_bytes` → `percent: null` ; sans `stats` → `percent: null`, compteurs 0.
- [ ] **Step 2 :** vitest → FAIL. **Step 3 :** implémenter. **Step 4 :** vitest → PASS.
- [ ] **Step 5 :** commit `feat(backup): read the backups that run`.

### Task 8 : l'onglet « En cours »

**Files:**
- Modify: `src/components/settings/BackupDestinations.vue` (onglets nommés, prop `initialTab`, sondage)
- Create: `src/components/settings/BackupRunning.vue`
- Modify: `src/assets/lang/en_US.json`, `src/assets/lang/fr_FR.json`
- Test: `src/components/settings/BackupRunning.spec.js`, `src/components/settings/backupDialogs.spec.js`

**Interfaces:**
- Consumes : Task 7.
- `BackupDestinations` : `props: { initialTab: { type: String, default: 'destinations' } }` ; valeurs `'running' | 'destinations' | 'schedules' | 'history'` sur chaque `b-tab-item` (`value`), `v-model="tab"` en chaîne ; les `v-if="tab === 1/2"` deviennent `'schedules'/'history'`. Lit `getRunning()` au montage puis toutes les **3 s** (`setInterval`, nettoyé dans `beforeUnmount`). Onglet « Running » (clé existante, fr « En cours ») rendu en premier dès qu'une réponse a des `backups` ou un passage sans `finished_at`, puis **gardé jusqu'à la fermeture** pour ne pas faire sauter l'onglet sous les yeux (mon choix). Un 404 ou une erreur : arrêt du sondage, pas d'onglet ; `initialTab: 'running'` retombe alors sur `'destinations'`.
- `BackupRunning` : `props: { running: { backups: Array, passes: Array } }`, `emits: ['refresh']`. Charge les titres d'apps comme `BackupSchedules.load` (`$openAPI.appGrid.getAppGrid`). Par sauvegarde : `{title} → {destination}`, origine, `started {when}` (`whenOf`), barre (`<progress>` sans `value` si `percent === null`), stats, `folder {operation} of {operations}`, `source`, `stats.current` ; bouton « Stop » → `$buefy.dialog.confirm` puis `stopRunning(id)`, toast « Stop requested », `$emit('refresh')`. Par passage non fini : « Next in this pass » (waiting, held) et « Done in this pass » (done ✓ durée · taille, failed, stopped).
- Clés (en → fr) : `See details` → `Voir le détail` ; `{done} of {total}` → `{done} sur {total}` ; `{count} files of {total}` → `{count} fichiers sur {total}` ; `about {duration} left` → `environ {duration} restant` ; `Taking stock` → `Inventaire en cours` ; `folder {n} of {total}` → `dossier {n} sur {total}` ; `scheduled at {time}` → `planification de {time}` ; `run now` → `lancée maintenant` ; `on demand` → `à la demande` ; `started {when}` → `commencée {when}` ; `Stop the backup of {title}? What is already copied stays at the destination, incomplete.` → `Arrêter la sauvegarde de {title} ? Ce qui est déjà copié reste à la destination, incomplet.` ; `Stop requested` → `Arrêt demandé` ; `Next in this pass` → `Ensuite dans ce passage` ; `Done in this pass` → `Déjà fait dans ce passage` ; `held by a system upgrade` → `retenue par une mise à niveau du système` ; `stopped` → `arrêtée` ; `Nothing is running any more.` → `Plus rien ne tourne.`. Réutilisées : `Running`, `Stop`, `Failed`, `Waiting`, `This box`.

- [ ] **Step 1 : tests** `BackupRunning.spec.js` (Buefy + i18n comme `backupDialogs.spec.js`) : avec l'exemple de la spec → texte contient `61`, `1.10 GB` et `1.79 GB` (format de `renderSize`), `9812`, `25488`, `folder 1 of 2` ; sans `total_bytes` → `Taking stock` et pas de `%` ; clic Stop + confirmation → `stopRunning('b7')` appelé, `refresh` émis ; passage avec items waiting/held/done/stopped → les deux sections et leurs libellés.
  `backupDialogs.spec.js` : `initialTab: 'running'` avec `getRunning` qui répond un run → onglet « Running » actif ; `getRunning` rejeté (404) → pas d'onglet, onglet Destinations actif (Review Focus 5) ; sondage arrêté au démontage (`vi.useFakeTimers`, `advanceTimersByTime(3000)` après `unmount` → pas de nouvel appel).
- [ ] **Step 2 :** vitest → FAIL. **Step 3 :** implémenter. **Step 4 :** vitest des deux fichiers + `backupLayout.spec.js` → PASS.
- [ ] **Step 5 :** eslint des fichiers touchés ; commit `feat(backup): a Running tab that follows each backup`.

### Task 9 : la carte du tableau de bord

**Files:**
- Modify: `src/components/dashboard/HealthStrip.vue` ; Test: `src/components/dashboard/HealthStrip.spec.js`

**Interfaces:**
- Consumes : Task 7, prop `initialTab` (Task 8).
- `data.live: { backups: [], passes: [] }` lu par `refreshSlow` (`getRunning`, 404 → vide). Un second minuteur **5 s** tourne tant que `live.backups.length > 0` (posé et retiré à chaque lecture, nettoyé dans `beforeUnmount`) ; `backup:begin` déclenche une lecture immédiate. Quand une sauvegarde disparaît de `live`, `refreshSlow` relit `getRuns` (résultat final depuis `runs.json`).
- Carte : si `live.backups[0]` → pastille « Running », `{title} → {destination} · {percent} %` (ou `Taking stock`), barre, `{done} of {total} · {speed}/s · about {eta} left`, et un lien « See details » qui appelle `openBackups('running')` ; sinon le rendu actuel (y compris le suivi socket). `openBackups(tab)` passe `props: { initialTab: tab }` quand `tab` est donné. Le titre vient de `backupSubject` quand le socket l'a donné, sinon `app`.

- [ ] **Step 1 : tests** : `getRunning` répond l'exemple de la spec → `.backups` contient `61 %`, `See details` ; clic → `$buefy.modal.open` reçu avec `props.initialTab === 'running'` ; minuteur 5 s : `advanceTimersByTime(5000)` → `getRunning` rappelé, puis réponse vide → plus d'appel à 5 s et `getRuns` relu ; `getRunning` absent ou rejeté (404) → rendu identique à aujourd'hui (tests existants inchangés, Review Focus 5).
- [ ] **Step 2 :** vitest `HealthStrip.spec.js` → FAIL. **Step 3 :** implémenter. **Step 4 :** vitest → PASS.
- [ ] **Step 5 :** eslint ; commit `feat(backup): the dashboard card follows the backup that runs`.

### Task 10 : vérification de bout en bout

- [ ] **Step 1 :** UI : suite complète `corepack pnpm exec vitest run` → 0 échec ; eslint des fichiers touchés.
- [ ] **Step 2 :** AM : `go test ./service/ ./route/... ./pkg/...` (conteneur) → seuls des échecs de la liste de référence ; `GOOS=linux go vet ./...` propre.
- [ ] **Step 3 :** essai réel sur le serveur mock de l'UI (`corepack pnpm exec vue-cli-service serve --mode mock`) avec une réponse `/backup/running` simulée ; capture de la carte et de l'onglet.
- [ ] **Step 4 :** rien n'est poussé : la suite (PR draft AM puis UI, publication AM avant UI) attend l'accord du propriétaire.
