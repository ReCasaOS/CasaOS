# Suivre une sauvegarde en cours depuis l'interface

Date : 2026-11-09. Dépôts touchés : CasaOS-AppManagement (AM), CasaOS-UI.
Marqueurs : **(propriétaire)** = décidé par le propriétaire ; **(mon choix)** = décidé par l'assistant, à relire.

## Pourquoi

Le 2026-11-09, sur la box du propriétaire, la sauvegarde planifiée de seerr (`happy_rik`) copiait depuis
la veille 17:55 les 25 488 petits fichiers de son cache d'images (1,9 Go) vers Google Drive, à ~220 Ko/s.
Treize autres apps et la sauvegarde de la box (créneau de 03:00) attendaient derrière : le planificateur
fait une app à la fois sous un seul verrou. L'interface disait « Prochaine : dans la minute » et
« Réussie ». Il a fallu `curl` sur le socket de `rclone rcd` pour découvrir que rien n'était bloqué.

Le correctif d'affichage « après la sauvegarde en cours » (CasaOS-UI, branche
`fix/backup-next-while-running`, 55a635ab) dit qu'une sauvegarde tourne, pas où elle en est.

## Ce qu'on veut voir (propriétaire)

Qu'une sauvegarde est en cours, **de quoi**, **à quelle vitesse**, **combien sur combien**, **avec un %**.

Disposition retenue (maquette C, propriétaire) :

- **Carte « Sauvegardes » du tableau de bord** (`HealthStrip.vue`), courte : pastille « En cours »,
  « seerr → GDrive · 61 % », une barre, « 1,18 Go sur 1,93 Go · 220 Ko/s · ~56 min », un lien
  **« Voir le détail »**.
- **« Voir le détail »** ouvre la fenêtre « Sauvegardes et restauration » (`BackupDestinations.vue`)
  sur un **onglet « En cours »**, placé en premier.

Contenu de l'onglet « En cours » (maquette validée par « on part là-dessus », propriétaire) :

- Par sauvegarde qui tourne : app → destination, origine (planification de HH:MM, ou « à la demande »),
  heure de début, barre, % fait, données faites / total, fichiers faits / total, vitesse, temps restant,
  le dossier copié (« 1 sur 2 ») et le fichier qui part.
- Un bouton **Arrêter**.
- La file du passage en cours du planificateur : « Ensuite » (en attente, dans l'ordre) et « Déjà fait
  dans ce passage » (✓ durée · taille, ou ✗ échec, ou « arrêtée »).

## Objectifs

1. L'AM sait, à tout instant, quelles sauvegardes tournent et où chacune en est, d'après les compteurs du
   moteur de copie (rclone ou restic), pas d'après un pourcentage par dossier.
2. L'AM sait ce que le passage du planificateur a fait, fait et va faire.
3. L'UI l'affiche sur la carte et dans l'onglet, rafraîchi sans recharger.
4. Arrêter une sauvegarde depuis l'UI.

## Non-objectifs

- Les restaurations dans l'onglet (leurs événements actuels restent) **(mon choix)**.
- La fenêtre de flotte du maître (onglet Sauvegardes des autres box) **(mon choix)** : plus tard, ce même
  état pourra remonter par le maître.
- Reprendre une copie coupée par un redémarrage du service, rendre les copies rclone incrémentales,
  ou changer l'ordre / le parallélisme du planificateur.
- Garder un historique de vitesse ou un graphe.

## Anti-objectifs

- Un pourcentage qui reste figé des heures alors que la copie avance (le défaut actuel).
- Une interface qui dit « Réussie » ou « dans la minute » pendant qu'une sauvegarde tourne.
- Un « Arrêter » qui laisse la copie continuer en fond (rclone ou restic encore actifs), ou qui laisse une
  app arrêtée (sauvegarde « tenue immobile ») après l'arrêt.
- Casser une box plus ancienne : l'UI doit se comporter comme aujourd'hui face à un AM sans les routes
  nouvelles (404).

## Ce qui existe (relevé du code, inkly/main)

AM (`a28b4d5`) :
- `service/backup_run.go` `RunBackup` (app) et `service/backup_system.go` `RunSystemBackup` (box)
  passent tous deux par `copyPlan`, qui boucle sur `manifest.Operations` et appelle
  `copier.CopyDirectory` / `CopyFile` (interface `BackupCopier`).
- Le copieur est `restic.Router` (`pkg/restic/router.go`) : restic si la destination y est passée,
  sinon le client rclone (`pkg/rclone/copy.go`).
- rclone : `startJob` lance un job `_async` sur `rclone rcd`, `waitForJob` le sonde toutes les 2 s
  (`pollInterval`) et appelle `job/stop` si le contexte est annulé. Aucun appel à `core/stats`.
- restic : `execRunner` lance le binaire avec `--quiet`, stdout en tampon lu à la fin ; `backup` n'a
  pas `--json`. Annulation = `exec.CommandContext` (SIGKILL).
- Événements : `publishBackupProgress(done, total)` = index de dossier, d'où 0 % pendant 13 h.
- Planificateur `service/backup_scheduler.go` : `RunDueBackups` tient `backupTick`, la liste `due` est
  locale. `ForceSchedules` (`backup_force.go`) enchaîne ses runs dans une goroutine. Les sauvegardes à la
  demande (`POST /compose/{id}/backup`, `POST /backup/system`) tournent dans une goroutine sur
  `context.Background()`. **Aucune n'a d'annulation par run.**
- `GET /operations` donne seulement `{app, kind}`.

UI (`5fe668f8` + 55a635ab) :
- `BackupDestinations.vue` : `b-tabs v-model="tab"` à index numérique (0 Destinations, 1 Planifications,
  2 Historique), aucune prop ; ouverte par `HealthStrip.openBackups` et `TopBar.showBackupDestinations`.
- `HealthStrip.vue` suit les événements socket `backup:*` et relit les routes toutes les 60 s.

## Conception

### AM : un registre des sauvegardes vivantes (mon choix)

Nouveau fichier `service/backup_live.go`, en mémoire seulement (après un redémarrage, ce qui tournait
est mort de toute façon) :

- `LiveBackup` : `id` (chaîne unique), `app`, `destination`, `stamp`, `origin`
  (`scheduled` | `forced` | `on_demand`), `started_at`, `operation` (index, 1-based), `operations`
  (total), `source` (dossier ou fichier en cours), et `stats` : `bytes`, `total_bytes`, `files`,
  `total_files`, `speed` (octets/s), `eta` (secondes, absent si inconnu), `current` (fichier qui part),
  `updated_at`. Plus une fonction `cancel` non exposée.
- `RunBackup` et `RunSystemBackup` s'y inscrivent en entrant (avec un `context.WithCancel` à eux) et
  s'en retirent en sortant (defer). `copyPlan` met à jour `operation`/`source` et remet `stats` à zéro
  à chaque opération.
- L'origine vient de l'appelant (`runScheduled` → `scheduled`, `ForceSchedules` → `forced`, routes à la
  demande → `on_demand`), passée par le contexte ou les options.

### AM : les compteurs du moteur (mon choix)

Un petit paquet `pkg/backupprogress` : `type Stats struct{...}` et `With(ctx, func(Stats)) ctx` /
`Report(ctx, Stats)`. Le registre accroche son rappel au contexte ; les moteurs le lisent. Les interfaces
`BackupCopier` et `backupRunner` ne changent pas.

- **rclone** : dans `waitForJob`, à chaque sondage, si un rappel est présent, appeler `core/stats` avec
  `group=job/<id>` et rapporter `bytes`, `totalBytes`, `transfers`, `totalTransfers`, `speed`, `eta`,
  `transferring[0].name`. Une erreur de `core/stats` n'interrompt jamais la copie.
- **restic** : pour `backup` seulement, passer `--json` sans `--quiet`, lire stdout ligne à ligne pendant
  l'exécution, et rapporter les messages `status` (`bytes_done`, `total_bytes`, `files_done`,
  `total_files`, `seconds_remaining`, `current_files[0]`) ; vitesse = `bytes_done / seconds_elapsed`.
  Le message final `summary` et les erreurs restent traités comme aujourd'hui.

Les chiffres affichés sont ceux de **l'opération en cours** (avec « dossier i sur n »), pas un total
d'app : rclone ne connaît le total d'un dossier qu'en le parcourant **(mon choix)**.

Les événements `backup:progress` existants portent désormais un vrai % :
`(done + bytes/total_bytes) / (n + 1)`, où `done` est le nombre d'opérations finies (0-based, celui que
`copyPlan` passe déjà) et le `+ 1` l'écriture du manifeste, comme aujourd'hui ; sans `total_bytes`, la
fraction de l'opération compte pour 0. Envoyés au plus toutes les 5 s **(mon choix)**, pour que les
cartes d'installation et les toasts qui les écoutent avancent eux aussi. Les appels actuels restent :
seul le pourcentage entre deux opérations est nouveau.

restic : au début d'un `backup --json`, les `status` arrivent sans total (parcours en cours) ;
`total_bytes`/`total_files` restent alors absents et l'UI montre une barre indéterminée avec
« inventaire en cours » au lieu de 0 % **(mon choix)**.

### AM : le passage du planificateur (mon choix)

`RunDueBackups` publie son passage dans une variable de paquet protégée : début, et pour chaque couple
app/destination dû : `waiting` → `running` → `done` | `failed` | `stopped`, avec `finished_at`, `bytes`,
durée. Le passage reste lisible après sa fin jusqu'au suivant. `ForceSchedules` publie sa chaîne de la
même façon, comme un passage d'origine `forced`.

### AM : deux routes (mon choix)

- `GET /backup/running` → `{ "backups": [LiveBackup…], "passes": [Pass…] }`, sans enveloppe, comme
  `/operations`. Vide quand rien ne tourne.
- `POST /backup/running/{id}/stop` → annule le contexte de ce run. 202 si trouvé, 404 sinon. Modèle :
  `/backup/verifications/{id}/cancel`.

Déclarées dans `api/app_management/openapi.yaml`, code régénéré (`go generate`, ligne 1 de `main.go`).

### Contrat JSON de `GET /backup/running` (mon choix)

Horodatages en RFC 3339 ; durées et ETA en secondes entières ; vitesse en octets par seconde ; un champ
inconnu est **absent** (jamais `null`, jamais 0 qui voudrait dire « inconnu »).

```json
{
  "backups": [{
    "id": "b7",                         // opaque, unique dans la vie du processus
    "app": "happy_rik", "destination": "GDrive", "stamp": "2026-11-08T16-55-18Z",
    "origin": "scheduled",              // scheduled | forced | on_demand
    "started_at": "2026-11-08T17:55:18+01:00",
    "operation": 1, "operations": 2,    // 1-based : « dossier 1 sur 2 »
    "source": "/DATA/AppData/overseerr/config",
    "stats": {                          // absent tant que le moteur n'a rien dit
      "bytes": 1182844747, "total_bytes": 1926565931,  // total_bytes absent = inventaire en cours
      "files": 9812, "total_files": 25488,
      "speed": 219371, "eta": 3390,
      "current": "cache/images/tmdb/…jpg",
      "updated_at": "2026-11-09T06:40:12+01:00"
    }
  }],
  "passes": [{
    "origin": "scheduled",              // scheduled | forced
    "started_at": "2026-11-08T17:55:18+01:00",
    "finished_at": "…",                 // absent tant que le passage tourne
    "items": [{
      "app": "big-bear-dozzle", "destination": "GDrive",
      "state": "done",                  // waiting | running | done | failed | stopped | held
      "started_at": "…", "finished_at": "…", "bytes": 1820, "error": "…"
    }]
  }]
}
```

- `passes` contient **au plus deux** passages : le dernier du planificateur et la dernière chaîne forcée,
  chacun gardé après sa fin jusqu'au suivant de même origine. L'onglet montre « Ensuite / Déjà fait »
  pour chaque passage encore en cours ; un passage fini ne s'affiche plus.
- `held` : couple laissé en attente parce qu'une mise à niveau de l'OS est ouverte (comportement actuel
  du planificateur).
- Une sauvegarde `on_demand` n'a pas de passage : l'onglet montre sa ligne « en cours », sans file.

### Arrêter (mon choix, le propriétaire n'a pas tranché)

- Arrête **cette app seulement** ; le passage continue avec la suivante. Les couples encore en attente
  n'ont pas de bouton (pas de retrait de la file en v1).
- rclone : l'annulation fait déjà `job/stop` (`waitForJob`). restic : le processus est tué par le
  contexte. restic tient pour périmé le verrou d'un processus mort sur la même machine ; le
  constructeur le vérifie par un test (backup tué puis backup relancé sur le même dépôt). Si le verrou
  bloque, envoyer SIGINT avant de tuer (`exec.Cmd.Cancel` + `WaitDelay`), pas de `restic unlock`.
- Les `defer` existants remettent en route une app ou des services tenus immobiles.
- Le run est écrit dans `runs.json` avec l'erreur « arrêtée depuis l'interface » ; le planificateur
  compte le créneau comme passé (comportement actuel pour toute erreur), donc pas de relance avant le
  créneau suivant.
- La copie rclone déjà partie reste à la destination, incomplète, et n'est pas supprimée. **Vérifié :**
  `rclone.Client.Runs` liste tout dossier daté, manifeste ou non ; une copie arrêtée est donc listée,
  exactement comme aujourd'hui une copie en échec ou coupée par un redémarrage (seerr, 2026-11-08
  13:55 → 17:54). Arrêter ne crée pas de cas nouveau ; corriger le listage (exiger le manifeste) est
  une suite à part, hors de ce chantier. restic n'écrit son instantané qu'à la fin : rien n'est listé.

### UI (mon choix sauf la disposition)

- `src/service/backup.js` : `getRunning()`, `stopRunning(id)`.
- `BackupDestinations.vue` : onglets nommés (`value` sur chaque `b-tab-item`) au lieu d'index, une prop
  `initialTab`, un onglet « En cours » en premier, visible seulement si quelque chose tourne.
- Nouveau `BackupRunning.vue` : lit `GET /backup/running` toutes les 3 s tant qu'il est affiché
  (`setInterval` + nettoyage au démontage), affiche ce que la maquette montre, « Arrêter » avec
  confirmation. Une app est nommée par son titre (grille d'apps, comme `BackupSchedules.vue`),
  `casaos-system` par « Cette machine ».
- `HealthStrip.vue` : la carte lit `GET /backup/running` ; toutes les 5 s tant que quelque chose tourne,
  sinon au rythme actuel de 60 s, et tout de suite sur `backup:begin`. « Voir le détail » ouvre la
  fenêtre avec `initialTab: 'running'`.
- Quand une sauvegarde disparaît de `/backup/running` (finie, ou trop courte pour avoir été vue), la
  carte relit `GET /backup/runs` comme aujourd'hui (`refreshSlow`) : le résultat final vient toujours de
  `runs.json`, jamais du passage.
- AM ancien (404 sur `/backup/running`) : la carte et la fenêtre restent comme aujourd'hui, sans onglet.
- Clés de traduction en `en_US.json` et `fr_FR.json`.

## Contraintes

- Ne jamais ralentir ni faire échouer une copie à cause du suivi (erreurs de `core/stats` ou de
  lecture JSON avalées, journalisées au plus une fois).
- Le verrou du registre est court ; pas d'appel réseau sous verrou.
- CI de ReCasaOS : pas de trailer d'attribution dans les commits ni de ligne « Generated with » dans
  les PR.
- Tests UI : Node 22 + `corepack pnpm` ; eslint sur les fichiers touchés.
- L'AM doit être publié avant l'UI (l'UI tolère l'absence des routes, mais la fonction n'apparaît
  qu'avec l'AM).

## Laissé au constructeur

- Le nom exact des types et fichiers, la forme de l'`id` d'un run.
- Le découpage en commits et en PR (une PR AM, une PR UI, construite sur `fix/backup-next-while-running`).
- La présentation fine de l'onglet, dans l'esprit de la maquette
  (`.superpowers/brainstorm/38859-1794202708/content/onglet-en-cours.html`).
