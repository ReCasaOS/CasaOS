# Synchro planifiée d'un dossier avec un compte cloud — plan d'implémentation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Des tâches de synchro (téléchargement, envoi, les deux sens) entre un dossier de la box et un dossier d'une destination de sauvegarde, sur planning, avec archive de 30 jours, depuis une entrée « Synchronisation » des réglages.

**Architecture:** L'AppManagement (AM) garde les tâches dans `sync.json`, les lance par un tick à lui (verrou distinct des sauvegardes) et pilote `rclone rcd` : `sync/copy` et `sync/sync` pour un sens, `sync/bisync` pour les deux. Une passe s'inscrit dans le registre vivant des sauvegardes, donc se suit et s'arrête par `/backup/running`. L'UI ajoute une fenêtre `CloudSync.vue` ouverte depuis le menu des réglages.

**Tech Stack:** Go (echo, oapi-codegen v1.12.4, resty), rclone v1.75.1 (`rcd`, socket unix), Vue 3 + Buefy, vitest + happy-dom.

**Spec:** `docs/superpowers/specs/2026-11-16-synchro-planifiee-design.md` (approuvée par Gary le 2026-11-16). Les essais cités (T1, B5, K3…) sont dans sa section « Essais ».

## Global Constraints

- Dépôts : `CasaOS-AppManagement` (tâches 1 à 6) et `CasaOS-UI` (tâches 7 à 10), chacun dans un worktree neuf tiré de `inkly/main` (`_sync-am`, `_sync-ui`), branche `feat/cloud-sync`. Les checkouts `CasaOS-AppManagement/` et `CasaOS-UI/` du disque sont en retard : ne pas partir d'eux.
- Commits : pas de trailer `Co-Authored-By`, pas de ligne « Generated with ». Pas de push ni de PR sans le feu vert de Gary.
- Chemins fixes : `/var/lib/casaos/backups/sync.json` (0600, écrit `.tmp` puis `rename`), état de bisync dans `/var/lib/casaos/backups/bisync/<id>`.
- Format de `<stamp>` : `2006-01-02T15-04-05Z` en UTC, celui des sauvegardes.
- Archive : `<parent de local_path>/.<nom du dossier>-archive/<stamp>` côté box, `<remote_path>-archive/<stamp>` côté cloud. Gardée `archive_days` jours, 30 par défaut, 0 = jamais purgée.
- `local_path` accepté seulement sous `/DATA`, `/media`, `/mnt` ; jamais ces racines, jamais `/DATA/AppData` ni dessous, jamais la racine d'un disque monté.
- bisync, toujours : `resilient=true`, `recover=true`, `maxLock=2m`, `maxDelete=50`, `conflictResolve=none`, `createEmptySrcDirs=true`, `_config={"SuffixKeepExtension":true}`, `checkAccess` absent.
- Les routes `/sync/*` sont réservées au propriétaire (`isOwner(ctx)`, sinon `forbidden(ctx)`), comme `route/v2/backup.go:200`.
- Textes de l'UI en anglais dans le code (`$t('…')`, clé = texte anglais), traduction ajoutée dans `src/assets/lang/fr_FR.json` et la clé dans `en_US.json`.
- Tests AM : ils ne tournent pas sous Windows. Compiler puis lancer dans l'image de rclone, ce que ce plan appelle **TEST-AM `<paquet>` `<motif>`** :

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/am.test ./<paquet> && MSYS_NO_PATHCONV=1 docker create --name amtest -e CASAOS_RCLONE_INTEGRATION=1 --entrypoint /am.test rclone/rclone:1.75.1 -test.run '<motif>' -test.v && MSYS_NO_PATHCONV=1 docker cp /tmp/am.test amtest:/am.test && docker start -a amtest; docker rm amtest
```

  (`go generate .` une fois avant, après tout changement d'`openapi.yaml`. Les montages de dossiers Windows bloquent Docker ici : `docker cp`, jamais `-v`.)
- Tests UI : Node 22 et `corepack pnpm` 9 ; `corepack pnpm vitest run <fichier>`.

## Review Focus

1. `local_path` est un lien symbolique vers `/etc` ou vers `/DATA/AppData` : refusé à l'enregistrement **et** au début de la passe (tâche 2, tâche 4).
2. La destination a été supprimée ou renommée hors de l'UI après la création de la tâche : la passe échoue avec un message qui nomme la destination, sans rien écrire ni supprimer, et sans passer en `needs_first_pass` (tâche 4).
3. `sync.json` illisible (JSON cassé) : erreur, aucune tâche ne tourne, et un `PUT` ne l'écrase pas par une liste vide (tâche 2).
4. `remote_path` saisi avec `/` en tête ou en fin, `..`, ou vide : normalisé (`/Photos/` devient `Photos`), `..` refusé, vide refusé pour `upload` et `both` (tâche 2).
5. L'AM redémarre pendant une passe `both` alors que rclone continue : la passe suivante est refusée pour verrou ; ce refus ne s'affiche pas comme une erreur et ne change pas `last_result` (tâche 4).

## File Structure

AM :
- `pkg/rclone/syncdir.go` (créé) : les appels rclone d'une passe, un sens et deux sens. Test : `syncdir_internal_test.go`, `syncdir_integration_test.go`.
- `service/sync_task.go` (créé) : le type `SyncTask`, le fichier, les contrôles d'enregistrement.
- `service/sync_run.go` (créé) : une passe, ses contrôles, ses états, la purge de l'archive.
- `service/sync_scheduler.go` (créé) : le tick, « lancer maintenant », la porte de mise à niveau.
- `route/v2/sync.go` (créé) : les routes. `api/app_management/openapi.yaml`, `main.go`, `service/backup_schedule.go`, `service/backup_live.go`, `route/v2/backup_running.go`, `route/v2/backup.go` (modifiés).

UI :
- `src/service/sync.js` (créé), `src/service/index.js` ou équivalent (modifié : là où `backup` est branché sur `$api`).
- `src/components/settings/CloudSync.vue` (créé) : la fenêtre et les cartes.
- `src/components/settings/CloudSyncTaskModal.vue` (créé) : le dialogue de création et de modification.
- `src/components/settings/cloudSyncText.js` (créé) : les phrases d'une carte, testables sans monter de composant.
- `src/components/TopBar.vue`, `src/components/settings/BackupRunning.vue`, les deux fichiers de langue (modifiés).

---

### Task 1: Les appels rclone d'une passe

**Files:**
- Create: `pkg/rclone/syncdir.go`
- Test: `pkg/rclone/syncdir_internal_test.go`, `pkg/rclone/syncdir_integration_test.go`
- Modify: `pkg/rclone/client.go:14` (le commentaire dit v1.61.1 ; écrire v1.75.1)

**Interfaces:**
- Consumes: `startJob`, `waitForJob`, `call` (`copy.go`), `serverFor` (`client_internal_test.go:19`), `DestinationPrefix`, `RawSuffix`.
- Produces:

```go
type SyncOptions struct {
	Mirror    bool   // sync/sync au lieu de sync/copy
	BackupDir string // fs complet ("/a/.b-archive/s" ou "casaos-backup-x:p-archive/s") ; "" = pas d'archive
	MaxDelete int64  // lu seulement si Mirror ; -1 = pas de plafond
}
func (c *Client) SyncDirectory(ctx context.Context, srcFs, dstFs string, o SyncOptions) error

type BisyncOptions struct {
	Workdir, BackupDir1, BackupDir2 string // BackupDir vides = pas d'archive
	Resync, Force                   bool
}
func (c *Client) BisyncDirectory(ctx context.Context, path1, path2 string, o BisyncOptions) error

var ErrTooManyDeletes = errors.New("too many files would be deleted")
var ErrSyncLocked     = errors.New("another pass holds the lock")
var ErrBisyncAborted  = errors.New("bisync aborted")

func (c *Client) CountFiles(ctx context.Context, fs string) (int64, error)   // operations/size, champ count
func (c *Client) IsEmpty(fs string) (bool, error)                            // operations/list sans récursion, zéro entrée
func (c *Client) Folders(fs, remote string) ([]string, error)                // operations/list, opt dirsOnly, noms triés
func (c *Client) DestinationKind(name string) (backend string, encrypted bool, err error)
```

`DestinationKind` lit `config/get` de `casaos-backup-<name>` ; si son `type` est `alias` ou `crypt`, le backend est le `type` de `casaos-backup-<name>-raw`, et `encrypted` vaut `type == "crypt"`.

- [ ] **Step 1: Tests du faux démon** (`syncdir_internal_test.go`, avec `serverFor`) :

```go
func TestOneWayWithoutMirrorNeverCallsSync(t *testing.T)   // endpoint == "/sync/copy", pas de MaxDelete dans _config
func TestMirrorPassesItsCapAndItsArchive(t *testing.T)     // "/sync/sync", _config == {"BackupDir":"/t/.b-archive/s","MaxDelete":7}
func TestBisyncAlwaysCarriesItsSafetyOptions(t *testing.T) // form: resilient=true recover=true maxLock=2m maxDelete=50 conflictResolve=none createEmptySrcDirs=true, _config == {"SuffixKeepExtension":true}; resync et force absents quand faux
func TestBisyncErrorsAreTold(t *testing.T)
// job error "too many deletes" -> errors.Is(err, ErrTooManyDeletes)
// "…prior lock file found: …" (avec codes couleur ANSI autour) -> ErrSyncLocked
// "bisync aborted" -> ErrBisyncAborted
func TestACappedMirrorSaysTooManyDeletes(t *testing.T)     // Mirror, job error "failed to delete 5 files" -> ErrTooManyDeletes
func TestAnEncryptedDestinationIsTold(t *testing.T)        // config/get: crypt puis raw s3 -> ("s3", true)
```

- [ ] **Step 2:** TEST-AM `pkg/rclone` `OneWay|Mirror|Bisync|Capped|Encrypted` → FAIL (symboles absents).
- [ ] **Step 3: Écrire `syncdir.go`.** `_config` part en chaîne JSON dans le formulaire, comme `verificationConfig` (`verify.go:47`) ; les booléens en `"true"`. Les erreurs sont reconnues par `strings.Contains` sur le texte du job après retrait des séquences `\x1b[…m`. « failed to delete » n'est traduit en `ErrTooManyDeletes` que si `Mirror` et `MaxDelete >= 0` (rclone dit la même phrase pour un refus de droits : la passe de confirmation le montrera alors comme une erreur ordinaire).
- [ ] **Step 4:** TEST-AM `pkg/rclone` même motif → PASS.
- [ ] **Step 5: Test sur le vrai rclone** (`syncdir_integration_test.go`, garde `CASAOS_RCLONE_INTEGRATION` et lancement du démon copiés de `verify_integration_test.go:20-60`). Une fonction `TestSyncWithRealRclone` avec ces sous-tests, dossiers sous `t.TempDir()` :

```go
// "copy keeps the overwritten version": dst/f1="old", src/f1="newer-content" -> dst/f1 nouveau, archive/f1 == "old"
// "mirror archives what it deletes":    fichier absent de src -> absent de dst, présent dans l'archive
// "the cap stops a mirror":             10 fichiers en dst, 2 en src, MaxDelete 3 -> ErrTooManyDeletes, au moins 7 fichiers restent
// "both: first pass unites":            p1/x, p2/y, Resync -> x et y des deux côtés
// "both: a deletion crosses and is archived": supprimer p1/x, passe -> p2/x absent, BackupDir2/x présent
// "both: a conflict keeps both, extension last": photo.jpg modifié des deux côtés -> photo.conflict1.jpg et photo.conflict2.jpg des deux côtés
// "both: more than half deleted is refused": -> ErrTooManyDeletes, p2 inchangé ; avec Force -> passe
```

- [ ] **Step 6:** TEST-AM `pkg/rclone` `RealRclone` → PASS (les sept sous-tests, pas SKIP).
- [ ] **Step 7:** Commit `feat(rclone): one-way and two-way folder sync calls`.

### Task 2: Le fichier des tâches et ses contrôles

**Files:**
- Create: `service/sync_task.go`
- Test: `service/sync_task_internal_test.go`

**Interfaces:**
- Consumes: `ScheduleEvery`, `ScheduleDaily`, `ScheduleWeekly`, `parseAt` (`backup_schedule.go`), le modèle d'écriture de `writeSchedules` (`backup_scheduler.go:59`), `BackupSchedules()`.
- Produces:

```go
type SyncDirection string
const ( SyncDownload SyncDirection = "download"; SyncUpload SyncDirection = "upload"; SyncBoth SyncDirection = "both" )
const ScheduleHourly ScheduleEvery = "hourly"

type SyncTask struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Destination string        `json:"destination"`
	RemotePath  string        `json:"remote_path"`
	LocalPath   string        `json:"local_path"`
	Direction   SyncDirection `json:"direction"`
	Mirror      bool          `json:"mirror"`
	Archive     bool          `json:"archive"`
	ArchiveDays int           `json:"archive_days"`
	Every       ScheduleEvery `json:"every"`
	At          string        `json:"at"`
	Weekday     time.Weekday  `json:"weekday"`
	Enabled     bool          `json:"enabled"`
	// écrits par l'AM seulement
	LastRun            time.Time `json:"last_run"`
	LastResult         string    `json:"last_result"` // "", "ok", "recovered", "failed", "stopped"
	LastError          string    `json:"last_error"`
	LastFiles          int64     `json:"last_files"`
	LastBytes          int64     `json:"last_bytes"`
	FirstPassDone      bool      `json:"first_pass_done"`
	NeedsFirstPass     bool      `json:"needs_first_pass"`
	NeedsDeleteConfirm bool      `json:"needs_delete_confirm"`
}

var SyncTasksPath = "/var/lib/casaos/backups/sync.json"
var BisyncStateDir = "/var/lib/casaos/backups/bisync"

func SyncTasks() ([]SyncTask, error)
func SaveSyncTasks(wanted []SyncTask) ([]SyncTask, error) // contrôle, donne les id, garde les champs de l'AM, écrit
func updateSyncTask(id string, change func(*SyncTask)) error // relit sous verrou ; tâche disparue = rien, nil
func checkSyncTask(task SyncTask, others []SyncTask) error   // tous les refus ; rappelé au début de chaque passe
func (t SyncTask) workdir() string                           // BisyncStateDir/<id>
func (t SyncTask) localArchive(stamp string) string
func (t SyncTask) remoteArchive(stamp string) string         // chemin dans la destination, sans le nom du remote
type SyncRefusal string                                      // erreur due à ce qui est demandé (400), comme BadSignIn

// remplaçables dans les tests
var syncDestinationKind = func(name string) (backend string, encrypted bool, err error) // rclone.NewClient().DestinationKind
var syncDestinations    = func() ([]string, error)                                       // rclone.NewClient().Destinations
var syncAppNames        = func() []string // apps installées + apps nommées dans BackupSchedules()
var isMountRoot         = func(path string) bool // périphérique du dossier != celui de son parent (syscall.Stat_t.Dev)
```

Règles de `SaveSyncTasks` : `id` vide → `"s" + 8 octets aléatoires en hexa` ; `id` connu → les champs de l'AM sont repris de la tâche stockée, ceux reçus sont ignorés ; pour une tâche `both` dont `destination`, `remote_path` ou `local_path` change, `FirstPassDone`, `NeedsFirstPass` et `NeedsDeleteConfirm` reviennent à faux et `workdir()` est effacé ; une tâche retirée de la liste voit son `workdir()` effacé ; `Direction == both` force `Mirror = true` ; pour `download`, `local_path` est créé (`MkdirAll` 0755) à l'enregistrement. `hourly` : `at` s'écrit `00:MM`, l'heure est ignorée.

- [ ] **Step 1: Tests** (`t.TempDir()` pour `SyncTasksPath` et pour une racine de test qui remplace `/DATA` par une variable `syncLocalRoots = []string{"/DATA","/media","/mnt"}` et `syncAppData = "/DATA/AppData"`) :

```go
func TestALocalFolderMustBeAUsersFolder(t *testing.T)
// refusés : "/", "/etc", "/DATA", "/DATA/AppData/x", un lien symbolique /DATA/l -> /etc, un dossier où isMountRoot dit vrai
// accepté : "/DATA/Gallery/Photos"
func TestTwoTasksNeverShareAFolder(t *testing.T)            // local_path contenu dans un autre ; même destination et remote_path contenu
func TestTheRemoteFolderIsCleanedAndKeptAwayFromBackups(t *testing.T)
// "/Photos/" -> "Photos" ; ".." refusé ; "" refusé pour upload et both, accepté pour download
// "casaos-restic/x", "casaos-system", "<nom d'une app>/x" refusés
func TestAnEncryptedDestinationOnlyTakesUploads(t *testing.T) // download et both refusés, upload accepté
func TestAnUnknownDestinationIsRefused(t *testing.T)
func TestSavingKeepsWhatTheBoxWrote(t *testing.T)           // last_run et first_pass_done d'une tâche connue survivent à un PUT qui les met à zéro
func TestMovingATwoWayTaskStartsItOver(t *testing.T)        // remote_path changé -> first_pass_done faux, workdir() absent
func TestBothAlwaysMirrors(t *testing.T)
func TestABrokenFileIsAnErrorNotAnEmptyList(t *testing.T)   // SyncTasks() rend une erreur ; SaveSyncTasks aussi, le fichier n'est pas réécrit
func TestUpdateOfAVanishedTaskWritesNothing(t *testing.T)
func TestScheduleFieldsAreChecked(t *testing.T)             // every inconnu, at "25:00", archive_days négatif refusés
```

- [ ] **Step 2:** TEST-AM `service` `LocalFolder|TwoTasks|RemoteFolder|Encrypted|UnknownDestination|SavingKeeps|MovingATwoWay|BothAlways|BrokenFile|Vanished|ScheduleFields` → FAIL.
- [ ] **Step 3: Écrire `sync_task.go`.** Chemins locaux : `filepath.Clean` puis `filepath.EvalSymlinks` (sur le plus long préfixe existant quand le dossier n'existe pas encore), puis les contrôles sur le résultat. Un verrou `syncTasksLock sync.Mutex` pour toute lecture-écriture.
- [ ] **Step 4:** TEST-AM `service` même motif → PASS.
- [ ] **Step 5:** Commit `feat(sync): the tasks file and what it refuses`.

### Task 3: Le créneau horaire

**Files:**
- Modify: `service/backup_schedule.go:106-140` (`mostRecentSlot`), et la validation des plannings de sauvegarde (là où `every` est contrôlé pour `PUT /backup/schedules`)
- Test: `service/backup_schedule_internal_test.go` (fichier de tests existant de `mostRecentSlot`)

**Interfaces:**
- Consumes: `ScheduleHourly` (tâche 2).
- Produces: `func syncDue(task SyncTask, now time.Time) (bool, error)` dans `service/sync_task.go` : vrai si `task.Enabled` et `task.LastRun` avant le dernier créneau. Il passe par `mostRecentSlot(BackupSchedule{Every: task.Every, At: task.At, Weekday: task.Weekday}, now)`.

- [ ] **Step 1: Tests**

```go
func TestAnHourlySlotIsTheLastPassedMinute(t *testing.T)
// at "00:20", now 14:35 -> 14:20 ; now 14:05 -> 13:20 ; now 00:10 -> 23:20 la veille
func TestBackupsDoNotGoHourly(t *testing.T)          // un planning de sauvegarde "hourly" est refusé comme un every inconnu
func TestADisabledTaskIsNeverDue(t *testing.T)
func TestAMissedSlotIsCaughtUpOnce(t *testing.T)     // last_run avant-hier, daily : dû ; last_run = now : plus dû
```

- [ ] **Step 2:** TEST-AM `service` `Hourly|BackupsDoNotGoHourly|DisabledTask|MissedSlot` → FAIL.
- [ ] **Step 3:** Ajouter le cas `ScheduleHourly` à `mostRecentSlot` (créneau construit avec `time.Date(..., now.Hour(), minute, ...)`, reculé d'une heure s'il est après `now`), le refus côté sauvegardes, et `syncDue`.
- [ ] **Step 4:** TEST-AM `service` même motif → PASS, puis TEST-AM `service` `Slot|Due|Schedule` → PASS (rien de cassé côté sauvegardes).
- [ ] **Step 5:** Commit `feat(sync): hourly slots, for sync tasks only`.

### Task 4: Une passe

**Files:**
- Create: `service/sync_run.go`
- Modify: `service/backup_live.go` (champ `Kind`)
- Test: `service/sync_run_internal_test.go`

**Interfaces:**
- Consumes: tâche 1 (`SyncDirectory`, `BisyncDirectory`, `CountFiles`, `IsEmpty`, `Folders`, les trois erreurs), tâche 2 (`checkSyncTask`, `updateSyncTask`, `workdir`, `localArchive`, `remoteArchive`), `beginLive`, `backupClock`, `rclone.Destination`, `Purge` (`copy.go`), `ErrStoppedFromInterface`.
- Produces:

```go
type syncEngine interface { // *rclone.Client le remplit ; les tests passent un faux
	SyncDirectory(ctx context.Context, srcFs, dstFs string, o rclone.SyncOptions) error
	BisyncDirectory(ctx context.Context, path1, path2 string, o rclone.BisyncOptions) error
	CountFiles(ctx context.Context, fs string) (int64, error)
	IsEmpty(fs string) (bool, error)
	Folders(fs, remote string) ([]string, error)
	Purge(ctx context.Context, destinationName, remotePath string) error
}
type syncMode int
const ( syncNormal syncMode = iota; syncFirstPass; syncConfirmDeletes )

func runSync(ctx context.Context, task SyncTask, mode syncMode, engine syncEngine) // écrit le résultat par updateSyncTask
func liveKind(ctx context.Context, kind string)                                   // pose LiveBackup.Kind
// LiveBackup gagne : Kind string  ("" = sauvegarde, "sync")
```

Déroulé de `runSync`, dans cet ordre :
1. `checkSyncTask` ; un refus → `failed` avec son texte.
2. `local_path` doit exister et être un dossier → sinon `failed`, « the folder on this box is missing ».
3. Côté lu vide alors que le côté écrit ne l'est pas (`IsEmpty`) → `failed`, « the source is empty » ; pour `both`, l'un des deux côtés vide et pas l'autre, sauf en `syncFirstPass`. Une erreur d'`IsEmpty` côté cloud → `failed` avec le nom de la destination.
4. `beginLive(ctx, task.Name, task.Destination, stamp)` puis `liveKind(ctx, "sync")`.
5. Un sens : `SyncDirectory` ; `Mirror` → `MaxDelete = max(10, CountFiles(côté écrit)/2)`, ou `-1` en `syncConfirmDeletes` ; `Archive` → `BackupDir`.
6. Deux sens : en `syncFirstPass` ou si `!FirstPassDone`, `Resync: true` **puis** une seconde passe normale, et `FirstPassDone = true` après les deux ; sinon une passe ; `Force: true` en `syncConfirmDeletes`. Pour le backend `drive`, `path2` est `casaos-backup-<nom>,skip_gdocs=true:<chemin>`.
7. Résultat :

| Fin de la passe | Écrit |
|---|---|
| réussie | `ok`, ou `recovered` si la tâche est `both` et que `LastResult` valait `failed` ou `stopped` ; `NeedsDeleteConfirm=false`, `NeedsFirstPass=false`, `last_error=""`, `last_files`, `last_bytes` |
| `ErrTooManyDeletes` | `failed`, `NeedsDeleteConfirm=true` |
| `ErrSyncLocked` | **rien** : ni `last_run`, ni `last_result` |
| `ErrBisyncAborted` | `failed` ; `NeedsFirstPass=true` selon la règle sous ce tableau |
| contexte annulé par Stop | `stopped` |
| autre erreur | `failed` avec le texte de rclone |

   Règle de `NeedsFirstPass` : après un `ErrBisyncAborted`, s'il n'y a dans `workdir()` ni `*.path1.lst` ni `*.path1.lst-old`, l'état est perdu → `NeedsFirstPass=true` tout de suite. S'il reste un `.lst-old` sans `.lst`, c'est une passe coupée que la suivante reprend (essai K3) : `failed` seulement.
8. Après une passe réussie seulement : purge de l'archive (`purgeSyncArchive(ctx, task, now, engine)`), dossiers dont le nom se lit comme un `<stamp>` plus vieux que `ArchiveDays` jours ; `os.RemoveAll` côté box, `Purge` côté cloud ; `ArchiveDays == 0` → rien ; une erreur est journalisée, la passe reste réussie.

`last_run` est écrit dans tous les cas sauf `ErrSyncLocked`.

- [ ] **Step 1: Tests** (faux `syncEngine` qui enregistre les appels) :

```go
func TestAMissingLocalFolderFailsThePass(t *testing.T)        // aucun appel au moteur
func TestAnEmptySourceNeverReachesRclone(t *testing.T)        // download, cloud vide, box non vide ; upload symétrique ; both hors première passe
func TestAGoneDestinationIsNamedAndChangesNothing(t *testing.T) // IsEmpty rend une erreur -> failed, texte contient le nom, NeedsFirstPass faux
func TestASymlinkSwappedAfterSavingIsCaughtAtRun(t *testing.T)
func TestOneWayOptions(t *testing.T)
// mirror faux -> SyncOptions{Mirror:false}, BackupDir = localArchive(stamp) en download, Destination(dest, remoteArchive(stamp)) en upload
// mirror vrai, 40 fichiers côté écrit -> MaxDelete 20 ; 6 fichiers -> 10 ; archive faux -> BackupDir ""
func TestTheFirstTwoWayPassIsTwoPasses(t *testing.T)          // appels : Resync true, puis Resync false ; FirstPassDone vrai ; une erreur à la 2e le laisse faux
func TestDriveSkipsGoogleDocs(t *testing.T)
func TestTooManyDeletesParksTheTask(t *testing.T)             // un sens mirror et both -> NeedsDeleteConfirm
func TestConfirmingLiftsTheCapOnce(t *testing.T)              // syncConfirmDeletes : MaxDelete -1 / Force true ; réussie -> NeedsDeleteConfirm faux
func TestALockedPassLeavesNoTrace(t *testing.T)               // LastRun, LastResult, LastError inchangés
func TestLostStateAsksForAFirstPass(t *testing.T)             // ErrBisyncAborted, workdir vide -> NeedsFirstPass ; avec un .path1.lst-old -> failed seulement
func TestAPassAfterAnInterruptionSaysRecovered(t *testing.T)  // both, LastResult "stopped" puis succès -> "recovered" ; même chose en download -> "ok"
func TestOldArchivesGoAfterAGoodPassOnly(t *testing.T)
// dossiers 31 j et 29 j : seul le premier est effacé ; "notes" (pas un stamp) gardé ; passe ratée : rien d'effacé ; ArchiveDays 0 : rien
func TestARunningSyncIsListedAsSync(t *testing.T)             // pendant la passe, LiveBackups() contient Kind "sync", App == task.Name
```

- [ ] **Step 2:** TEST-AM `service` `MissingLocal|EmptySource|GoneDestination|SymlinkSwapped|OneWayOptions|FirstTwoWay|DriveSkips|TooManyDeletes|Confirming|LockedPass|LostState|SaysRecovered|OldArchives|ListedAsSync` → FAIL.
- [ ] **Step 3: Écrire `sync_run.go`** et le champ `Kind`.
- [ ] **Step 4:** même commande → PASS. Puis TEST-AM `service` `Live|Running` → PASS.
- [ ] **Step 5:** Commit `feat(sync): one pass, its guards and its archive`.

### Task 5: Le planificateur et les lancements à la demande

**Files:**
- Create: `service/sync_scheduler.go`
- Modify: `main.go` (après le bloc `RunDueVerifications`, ~l.186)
- Test: `service/sync_scheduler_internal_test.go`

**Interfaces:**
- Consumes: `syncDue` (tâche 3), `runSync` et `syncMode` (tâche 4), `markInProgress` (`app_operations.go:71`), `osUpgradeOpen` (`backup_upgrade_hold.go:49`), `holdNoticeDue`, `WithBackupOrigin`.
- Produces:

```go
func RunDueSyncs(ctx context.Context, now time.Time, engine syncEngine)
func StartSync(id string, mode syncMode, engine syncEngine) error // rend la main dès que la passe est lancée
var ErrSyncNotFound   = errors.New("no sync task with this id")
var ErrSyncRunning    = errors.New("this sync task is already running")
var ErrSyncUpgrade    = errors.New("an operating system upgrade is open")
var ErrSyncWrongState = errors.New("this sync task does not wait for that")
```

Règles : `syncTick sync.Mutex` avec `TryLock`, **pas** `backupTick` ; les tâches dues passent une à la fois, triées par nom ; une tâche en `NeedsFirstPass` ou `NeedsDeleteConfirm` n'est jamais due. Un ensemble `syncRunning map[string]struct{}` sous verrou sert au tick et à `StartSync`. Avant chaque passe, dans cet ordre : `markInProgress("sync:"+task.Name, "sync")`, **puis** `osUpgradeOpen()` ; ouverte → retirer la marque, ne rien écrire (le créneau reste dû), un message de journal au plus par heure. `StartSync` : `syncFirstPass` exige `NeedsFirstPass` ou `!FirstPassDone` sur une tâche `both`, `syncConfirmDeletes` exige `NeedsDeleteConfirm`, sinon `ErrSyncWrongState` ; la passe tourne dans une goroutine sur `context.Background()` avec l'origine `OriginOnDemand`.

- [ ] **Step 1: Tests** (`runSync` remplacé par une variable `runSyncPass` pour compter et bloquer) :

```go
func TestALongBackupDoesNotHoldSyncs(t *testing.T)      // backupTick pris : RunDueSyncs lance quand même
func TestSyncsGoOneAtATime(t *testing.T)                // deuxième RunDueSyncs pendant le premier : rien
func TestAParkedTaskIsNotDue(t *testing.T)
func TestNothingRunsWhileAnUpgradeIsOpen(t *testing.T)  // aucune passe, LastRun inchangé
func TestTheMarkComesBeforeTheUpgradeFlagIsRead(t *testing.T) // osUpgradeBusyStat remplacé : au moment où il est appelé, RunningOperations() liste "sync:<name>"
func TestRunNowRefusesWhatItShould(t *testing.T)
// id inconnu -> ErrSyncNotFound ; déjà en cours -> ErrSyncRunning ; mise à niveau -> ErrSyncUpgrade
// confirm-deletes sur une tâche qui n'attend pas -> ErrSyncWrongState
```

- [ ] **Step 2:** TEST-AM `service` `LongBackup|OneAtATime|ParkedTask|UpgradeIsOpen|MarkComesBefore|RunNowRefuses` → FAIL.
- [ ] **Step 3:** Écrire `sync_scheduler.go` ; dans `main.go`, `crontab.AddFunc("@every 1m", func() { service.RunDueSyncs(ctx, service.BoxNow(), rclone.NewClient()) })`.
- [ ] **Step 4:** même commande → PASS.
- [ ] **Step 5:** Vérifier que le core tient compte d'une opération de genre `sync` avant une mise à niveau : `git grep -n "operations" inkly/main -- '*.go'` dans `CasaOS/` (le core), lire comment il filtre la réponse de `/operations`. S'il ne retient que certains genres, l'écrire dans le compte rendu de la tâche : c'est un changement du core, hors de ce plan.
- [ ] **Step 6:** Commit `feat(sync): the tick, run now, and the upgrade gate`.

### Task 6: Les routes

**Files:**
- Modify: `api/app_management/openapi.yaml` (après `/backup/restore`, ~l.1441 ; schéma `LiveBackup` : champ `kind`), `route/v2/backup_running.go`, `route/v2/backup.go` (suppression d'une destination)
- Create: `route/v2/sync.go`
- Test: `route/v2/sync_internal_test.go`, `route/v2/backup_running_internal_test.go`

**Interfaces:**
- Consumes: `SyncTasks`, `SaveSyncTasks`, `SyncRefusal`, `StartSync` et ses erreurs, `Folders`, `isOwner`, `forbidden`, `backupBadRequest`, `ownerCaller`/`visibilityCaller` (aides de test de `hidden_internal_test.go`).
- Produces (réponses dans l'enveloppe `{ "data": … }` des routes `/backup/*` voisines) :

| Route | Corps | Réponses |
|---|---|---|
| `GET /sync/tasks` | | 200 liste de `SyncTask` |
| `PUT /sync/tasks` | `{ "tasks": [SyncTask] }`, lu strictement (champ inconnu = 400, comme `SetBackupExclusions`) | 200 la liste enregistrée ; 400 avec le texte du refus |
| `POST /sync/tasks/{id}/run` | | 202 ; 404 ; 409 (`ErrSyncRunning`, `ErrSyncUpgrade`) |
| `POST /sync/tasks/{id}/first-pass` | | 202 ; 404 ; 409 (les mêmes, et `ErrSyncWrongState`) |
| `POST /sync/tasks/{id}/confirm-deletes` | | 202 ; 404 ; 409 |
| `GET /backup/destinations/{name}/folders?path=` | | 200 `{ "folders": ["a","b"] }` ; 400 si la destination ne répond pas |

Toutes : 403 si l'appelant n'est pas le propriétaire. `GET /backup/running` : chaque élément porte `kind` (`"backup"` ou `"sync"`) ; un élément `sync` n'est rendu qu'au propriétaire. `DELETE /backup/destinations/{name}` : 400 « used by the sync task <nom> » si une tâche l'utilise.

- [ ] **Step 1: Tests**

```go
func TestSyncRoutesAreTheOwnersOnly(t *testing.T)            // les six routes, visibilityCaller -> 403
func TestSavingTasksReadsTheBodyStrictly(t *testing.T)       // champ inconnu -> 400 ; refus de contrôle -> 400 et son texte
func TestRunNowAnswers(t *testing.T)                         // 202, 404, 409 selon l'erreur de StartSync (variable remplaçable)
func TestFoldersOfADestination(t *testing.T)
func TestADestinationInUseIsNotForgotten(t *testing.T)
func TestARunningSyncIsShownToTheOwnerOnly(t *testing.T)     // runningOut : kind "sync" absent pour un compte user, présent pour le propriétaire
func TestABackupStillSaysBackup(t *testing.T)                // kind "backup" quand LiveBackup.Kind == ""
```

- [ ] **Step 2:** Écrire l'openapi, puis `go generate .` ; le paquet ne compile plus tant que `sync.go` manque : TEST-AM `route/v2` `Sync` → FAIL de compilation.
- [ ] **Step 3:** Écrire `route/v2/sync.go` et les deux modifications.
- [ ] **Step 4:** TEST-AM `route/v2` `Sync|Folders|InUse|RunningSync|StillSaysBackup|Running` → PASS. Puis les suites entières `service`, `route/v2`, `pkg/rclone` : seuls les quatre tests connus qui demandent un démon Docker peuvent être rouges (`TestPullAndApplyRestoresDotEnvOnFailure`, `TestNonExistingContainer`, `TestCurrentArchitecture`, `TestTheGitRoutesAreServed`).
- [ ] **Step 5:** Commit `feat(sync): routes for the sync tasks`.

### Task 7: UI, le service et les phrases d'une carte

**Files:**
- Create: `src/service/sync.js`, `src/service/sync.spec.js`, `src/components/settings/cloudSyncText.js`, `src/components/settings/cloudSyncText.spec.js`
- Modify: le fichier qui branche `backup` sur `$api` (`git grep -n "backup" inkly/main -- src/service/index.js src/service/api.js`), `src/service/backup.js` (`getFolders`)

**Interfaces:**
- Produces:

```js
// src/service/sync.js — PREFIX '/v2/app_management', client `api` de './service.js', comme backup.js
sync.getTasks()            // GET  /sync/tasks
sync.saveTasks(tasks)      // PUT  /sync/tasks  { tasks }
sync.run(id)               // POST /sync/tasks/<id>/run
sync.firstPass(id)         // POST /sync/tasks/<id>/first-pass
sync.confirmDeletes(id)    // POST /sync/tasks/<id>/confirm-deletes
backup.getFolders(name, path) // GET /backup/destinations/<name>/folders?path=<path>

// cloudSyncText.js — fonctions pures ; t est $t
routeOf(task)              // 'Dropbox /Photos → /DATA/Gallery/Photos' (download), '←' inversé en upload, '↔' en both
ruleOf(task, t)            // 'Every day at 02:00 · nothing is ever deleted · copies kept 30 days'
lastOf(task, t, now)       // 'Last pass: yesterday 02:00, done, 212 files, 1.4 GB' ; 'never run' ; l'erreur si failed
noticeOf(task, t)          // null | { kind: 'first-pass' | 'deletes' | 'recovered', text }
```

Textes fixés par la spec (clés anglaises) : `noticeOf` rend pour `needs_delete_confirm` « More than half of the files on one side are gone », pour `needs_first_pass` « This task lost track of the two sides », pour `last_result === 'recovered'` « Resumed after an interruption: files deleted since may have come back ».

- [ ] **Step 1: Tests** : `sync.spec.js` sur le modèle de `backup.spec.js` (adaptateur axios simulé) : méthode et URL de chaque appel, `id` et `name` encodés (`encodeURIComponent`), corps `{ tasks }`. `cloudSyncText.spec.js` : les trois flèches ; `mirror` vrai → « deletions are carried over » ; `archive` faux → pas de « copies kept » ; `archive_days` 0 → « copies kept for ever » ; hourly → « Every hour at :20 » ; les trois `noticeOf` et `null`.
- [ ] **Step 2:** `corepack pnpm vitest run src/service/sync.spec.js src/components/settings/cloudSyncText.spec.js` → FAIL.
- [ ] **Step 3:** Écrire les deux fichiers et brancher `sync` sur `$api`.
- [ ] **Step 4:** même commande → PASS.
- [ ] **Step 5:** Commit `feat(sync): service calls and card sentences`.

### Task 8: UI, la fenêtre et son entrée dans les réglages

**Files:**
- Create: `src/components/settings/CloudSync.vue`, `src/components/settings/CloudSync.spec.js`
- Modify: `src/components/TopBar.vue` (carte après `<!-- Backup destinations End -->`, l.325 ; méthode à côté de `showBackupDestinations`, ~l.1087)

**Interfaces:**
- Consumes: tâche 7 ; `CloudSyncTaskModal` (tâche 9, importé ici, ouvert par `$buefy.modal.open` avec `props: { task, tasks }` et l'événement `saved`).
- Produces: composant `cloud-sync`, `emits: ['close']`, classe racine `modal-card cloud-sync touch-targets`, ouvert avec les mêmes options que `showBackupDestinations` (`hasModalCard`, `customClass: 'backup-modal'`, `trapFocus`, `canCancel: ['escape']`, `scroll: 'keep'`, `animation: 'zoom-in'`).

Comportement : au montage `getTasks()` ; relu toutes les 15 s tant que la fenêtre est ouverte (intervalle effacé dans `beforeUnmount`). Une carte par tâche (`<section class="settings-card">`, comme les cartes de destination) : nom, interrupteur `enabled` (un `saveTasks` de la liste entière), `routeOf`, `ruleOf`, `lastOf`, le bandeau de `noticeOf` avec son bouton (« Run the first pass again » → `firstPass`, « Confirm the deletions » → `confirmDeletes`, chacun derrière un `$buefy.dialog.confirm` qui dit la conséquence), puis « Run now », « Edit », « Delete » (confirmation). Liste vide : « No sync task yet ». Échec de lecture : « Sync tasks could not be loaded », jamais la phrase de liste vide. Dans `TopBar.vue` : la carte « Folder sync » avec le bouton « Configure » n'est rendue que si `getTasks()` a répondu autrement que 404 (essayé une fois à l'ouverture du menu des réglages, résultat gardé).

- [ ] **Step 1: Tests** (`open()` copié de `backupDialogs.spec.js:19-35`, mocks `$api.sync`) :

```js
it('shows one card per task with its three sentences')
it('does not call a failed load an empty list')
it('saves the whole list when a task is switched off')            // saveTasks reçoit toutes les tâches, enabled false sur la bonne
it('offers the first pass again only on a task that lost its state')
it('asks before confirming deletions, and calls confirmDeletes on yes')
it('shows what the box answered when run now is refused')         // 409 -> message de l'AM dans un toast
it('says a pass resumed after an interruption')
```

Et dans un `TopBar` monté à part ou une fonction extraite `syncAvailable(error)` : `it('hides the settings entry when the box answers 404')`.

- [ ] **Step 2:** `corepack pnpm vitest run src/components/settings/CloudSync.spec.js` → FAIL.
- [ ] **Step 3:** Écrire `CloudSync.vue` et la carte de `TopBar.vue`.
- [ ] **Step 4:** même commande → PASS.
- [ ] **Step 5:** Commit `feat(sync): the folder sync window and its settings entry`.

### Task 9: UI, le dialogue d'une tâche

**Files:**
- Create: `src/components/settings/CloudSyncTaskModal.vue`, `src/components/settings/CloudSyncTaskModal.spec.js`

**Interfaces:**
- Consumes: `backup.getDestinations()`, `backup.getFolders(name, path)`, `sync.saveTasks`, `FilePanel` (`src/components/fileList/FilePanel.vue`, props `initPath`, `rootPath`, `showFile: false`, événement `updatePath`, ouvert en modal comme `forms/IconInput.vue:55-76`), `BackupDestinations` avec `props: { initialTab: 'destinations' }`.
- Produces: props `task` (objet ou `null` pour une création), `tasks` (la liste entière) ; événements `saved` (la liste rendue par l'AM) et `close`.

Champs : nom ; compte (liste des destinations ; lien « Add an account » qui ouvre la fenêtre des sauvegardes) ; dossier du cloud (liste de `getFolders`, on descend d'un clic, on valide le dossier courant ; liste vide sur une destination : « This account shows no folder here. An account limited to one folder only sees inside it. ») ; dossier de la box (`FilePanel`, `rootPath: '/DATA'`) ; sens (trois choix) ; planning (`hourly` avec une minute, `daily`, `weekly` avec un jour) ; deux interrupteurs. Valeurs d'une création : `direction: 'download'`, `mirror: false`, `archive: true`, `archive_days: 30`, `every: 'daily'`, `at: '02:00'`, `enabled: true`.

Phrases fixées par la spec :
- interrupteur 1 éteint : « Off: this task never deletes a file. »
- interrupteur 2 : « Keep a copy of what is replaced or deleted », avec le chemin de l'archive et « for 30 days » ; éteint avec l'interrupteur 1 allumé : « Without copies, up to half of the files can be lost before the task stops. »
- sens « Both » : interrupteur 1 allumé et verrouillé, « Both ways, a deletion on one side is always carried to the other; the copy kept below is what protects you. », et « At the first pass, when a file differs the box's version is kept; the cloud's goes to the copies. »
- destination chiffrée choisie avec « Upload » : « These files will only be readable from this box. »

Un refus de l'AM (400) s'affiche tel quel au-dessus des boutons, le dialogue reste ouvert avec ce qui a été saisi.

- [ ] **Step 1: Tests**

```js
it('starts a new task with nothing deleted and copies kept 30 days')   // corps envoyé à saveTasks
it('locks deletions on when both ways is chosen, and says why')
it('warns when copies are off and deletions are on')
it('sends the edited task in place, with the others untouched')
it('keeps the form and shows the box\'s words when saving is refused')
it('walks into a cloud folder and picks it')                           // getFolders('nas','') puis ('nas','Photos'), remote_path 'Photos'
it('says so when the account shows no folder')
```

- [ ] **Step 2:** `corepack pnpm vitest run src/components/settings/CloudSyncTaskModal.spec.js` → FAIL.
- [ ] **Step 3:** Écrire le dialogue.
- [ ] **Step 4:** même commande → PASS.
- [ ] **Step 5:** Commit `feat(sync): the task dialog`.

### Task 10: UI, les synchros dans « En cours », les traductions, la vérification

**Files:**
- Modify: `src/components/settings/BackupRunning.vue` (l.6 titre, ~l.136 confirmation d'arrêt), `src/components/settings/BackupRunning.spec.js`, `src/assets/lang/en_US.json`, `src/assets/lang/fr_FR.json`

**Interfaces:**
- Consumes: le champ `kind` de `GET /backup/running` (tâche 6).

Comportement : un élément `kind === 'sync'` s'affiche « Sync: <nom> → <destination> » sans passer par `titleOf` ; sa confirmation d'arrêt ajoute « Files deleted since the last pass may come back when it resumes. » si la tâche est dans les deux sens (le composant lit `getTasks()` une fois, au mieux : en cas d'échec la phrase est ajoutée pour toute synchro).

- [ ] **Step 1: Tests** dans `BackupRunning.spec.js` : `it('names a running sync by its task, not by an app')`, `it('warns before stopping a sync')`, `it('still shows a backup as before when kind is missing')` (un AM plus ancien n'envoie pas `kind`).
- [ ] **Step 2:** `corepack pnpm vitest run src/components/settings/BackupRunning.spec.js` → FAIL.
- [ ] **Step 3:** Modifier `BackupRunning.vue`. Ajouter chaque clé `$t` nouvelle des tâches 7 à 10 aux deux fichiers de langue ; les lister par `git diff inkly/main -- src | grep -o "\$t(['\"][^'\"]*" | sort -u` et vérifier qu'aucune ne manque dans `fr_FR.json`.
- [ ] **Step 4:** même commande → PASS ; puis `corepack pnpm vitest run` (suite entière) et `corepack pnpm lint` → verts.
- [ ] **Step 5: Vérification dans le navigateur.** Pile réelle selon la note « Full stack in Docker » (AM de la branche, rclone v1.75.1), une destination `local` vers un dossier du conteneur. Créer une tâche de chaque sens depuis l'entrée « Folder sync », « Run now », voir la passe dans « En cours », la carte passer à « done » ; supprimer plus de la moitié d'un côté d'une tâche « Both », voir le bandeau et « Confirm the deletions » ; arrêter une passe et voir « Resumed after an interruption » à la suivante. Une capture de la fenêtre et une du dialogue.
- [ ] **Step 6:** Commit `feat(sync): running syncs, French, and the last checks`.

---

## Hors de ce plan

- Les tags, la release de l'installeur et la réponse sur CasaOS#153 : après fusion, sur décision de Gary.
- Un essai sur un vrai compte Dropbox, Google Drive, OneDrive et un vrai S3 : à faire sur une box réelle avant d'annoncer la fonction (la spec le liste).
- Les droits du compte qui fait tourner `rclone rcd` sur `/DATA`, `/media`, `/mnt` : constatés à l'étape 5 de la tâche 10 dans la pile Docker, à confirmer sur une box installée.
- Les événements `sync:done` et `sync:failed` du bus de messages : aucun écran ne les lirait (la fenêtre relit ses tâches), ils ne sont pas faits. La spec est corrigée en ce sens.
- Le fichier `.partial` laissé sur la box après une coupure (essai R2b).
