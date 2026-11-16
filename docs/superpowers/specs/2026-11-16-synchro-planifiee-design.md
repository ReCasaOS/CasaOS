# Synchroniser un dossier de la box avec un compte cloud, sur planning

Date : 2026-11-16. Dépôts touchés : CasaOS-AppManagement (AM), CasaOS-UI. Demande : CasaOS#153.
Marqueurs : **(propriétaire)** = décidé par le propriétaire ; **(mon choix)** = décidé par l'assistant, à relire.

## Pourquoi

Dans CasaOS#153, NoBackUp42 demande ce que font le Cloud Sync de Synology et celui de TrueNAS : une
**copie locale tenue à jour sur planning**, dans un sens ou dans les deux, sans perte quand un fichier
est supprimé ou modifié des deux côtés. Aujourd'hui la box sait :

- **monter** un compte cloud dans Fichiers (un lien, rien n'est copié) ;
- **sauvegarder** une app ou la box vers une destination (box vers cloud, par app, datée, restaurable).

Aucun des deux ne garde un dossier quelconque de la box identique à un dossier du cloud. C'est une
fonction nouvelle **(propriétaire)**. S3 doit en faire partie **(propriétaire)**.

Ce qui a été annoncé publiquement dans l'issue, et que cette spec doit couvrir ou reporter en le disant :
un sens par tâche (les deux sens, téléchargement seul, envoi seul), un choix de dossiers, un planning,
un interrupteur « ne pas supprimer de l'autre côté », des conflits gardés en copies renommées, une
archive des fichiers supprimés ou écrasés, S3 comme Dropbox, Google Drive et OneDrive, pas de date.

## Objectifs

1. Une **tâche de synchro** relie un dossier de la box à un dossier d'un compte cloud, avec un sens et
   un planning.
2. Rien n'est perdu par défaut : aucune suppression propagée, et tout fichier écrasé ou supprimé par
   une synchro part dans une archive.
3. Tout backend déjà accepté comme destination de sauvegarde marche, S3 compris, sans code par backend.
4. Une synchro se voit, se suit et s'arrête comme une sauvegarde (onglet « En cours »).
5. Une longue synchro ne retarde pas les sauvegardes, et l'inverse.

## Non-objectifs

- La détection en temps réel (inotify, webhooks du fournisseur) : planning seulement **(mon choix)**.
- Les filtres par tâche (motifs à exclure), la limite de débit, plusieurs couples de dossiers par tâche
  (on crée plusieurs tâches) **(mon choix)**.
- La flotte : créer ou voir les tâches d'une autre box depuis le maître.
- restic : une synchro passe toujours par rclone, y compris vers une destination dont les sauvegardes
  sont tenues par restic.
- Un historique des passages : seul le dernier résultat de chaque tâche est gardé **(mon choix)**.

## Anti-objectifs

- **Une source vide ou injoignable qui vide l'autre côté** : disque non monté, jeton expiré, bucket
  renommé. C'est l'accident type de ce genre d'outil.
- Une tâche qui écrit ou supprime dans les sauvegardes de la même destination (`<app>/<stamp>`,
  `casaos-system`, `casaos-restic`).
- Une tâche qui écrit dans `/`, dans le système ou dans les données des apps.
- Une synchro de 13 heures qui bloque les sauvegardes de 03:00 (le cas seerr du 2026-11-09).
- Une resynchronisation lancée seule qui fait réapparaître des fichiers supprimés. (La reprise après
  une interruption le fait quand même, voir plus bas : accepté avec un message.)
- Casser une box plus ancienne : face à un AM sans les routes nouvelles (404), l'UI ne montre pas
  la carte « Synchronisation » et se comporte comme aujourd'hui.

## Ce qui existe (relevé du code, inkly/main)

AM (`7114ede`) :
- `pkg/rclone` parle au démon `rclone rcd` par `/var/run/rclone/rclone.sock`. Les copies sont des jobs
  `_async` (`copy.go` `startJob`, `waitForJob`), sondés toutes les 2 s, avec `core/stats` par
  `group=job/<id>` (`copy.go:128`) et `job/stop` quand le contexte est annulé.
- `CopyDirectory` appelle `sync/copy` (ne supprime jamais). `RestoreDirectory` (`restore.go`) appelle
  `sync/sync` de la destination vers la box : le sens cloud vers box existe donc déjà dans le client.
- `verify.go` passe des options par job avec `_config` (noms de champs `ConfigInfo`, pas de drapeaux)
  et `_filter`.
- rclone livré : **v1.75.1** (`get` `release/components.env:17`). Le commentaire d'en-tête de
  `client.go:14` dit encore v1.61.1 : périmé, à corriger en passant.
- Destinations : des remotes `casaos-backup-<nom>` dans la config de rclone, pas de second magasin.
  Avec un `root` (dossier, ou bucket pour S3) la destination est un remote `alias` sur
  `casaos-backup-<nom>-raw:<root>` (`client.go:168-209`) ; chiffrée, un remote `crypt` sur ce même
  remote brut (`client.go:326`).
- Connexion par navigateur pour Google Drive, Dropbox, OneDrive : `POST /backup/signin` et
  `/backup/signin/finish`.
- Planificateur `service/backup_scheduler.go` : `RunDueBackups` est appelé chaque minute par le
  crontab de `main.go`, tient `backupTick` (`TryLock`, un passage à la fois pour toute la box), et
  s'arrête tant qu'une mise à niveau du système est ouverte (`osUpgradeOpen`). Les plannings sont dans
  `/var/lib/casaos/backups/schedules.json` (écriture `.tmp` puis `rename`, 0600), `every` vaut `daily`
  ou `weekly`, et `mostRecentSlot` rattrape un créneau manqué.
- `service/backup_live.go` : registre en mémoire des sauvegardes qui tournent (`beginLive`,
  `LiveBackups`, `StopLiveBackup`), servi par `GET /backup/running` et
  `POST /backup/running/{backup_id}/stop`.
- L'unité sauvegardée est toujours une app, ou `casaos-system`. Aucun mode « dossier quelconque ».

UI (`81297e8b`) : fenêtre « Sauvegardes et restauration » (`BackupDestinations.vue`), onglets nommés
(`running` si quelque chose tourne, `destinations`, `schedules`, `exclusions`, `history`), ouverte sur
un onglet par la prop `initialTab` ; `backupBackends.js` décrit les backends (S3 avec
son champ « Bucket ») ; `service/backup.js` porte les appels.

## Approches

1. **Sens unique seulement** (`sync/copy` et `sync/sync`), jamais de double sens. Le plus simple et le
   plus sûr, tout le client existe. Mais le double sens est annoncé dans l'issue.
2. **Les trois sens d'un coup**, le double sens par `sync/bisync`. Couvre tout, mais bisync apporte un
   état sur disque par tâche, une première passe spéciale (`resync`), un verrou, et des limites par
   backend qu'on n'a jamais essayées sur cette box.
3. **Deux lots, un seul modèle** : un premier lot livre téléchargement seul et envoi seul ; un second
   ajoute « les deux » sans changer le fichier des tâches ni les routes.

Retenue : **2 (propriétaire, 2026-11-16 : « tout d'un coup »)**. L'assistant proposait la 3. Le
double sens a donc été essayé sur le vrai rclone v1.75.1 avant le plan : voir « Essais ». La
conception ci-dessous tient compte de ce qu'ils ont montré.

## Conception

### La tâche (mon choix)

Fichier `/var/lib/casaos/backups/sync.json`, écrit comme `schedules.json` (liste entière, `.tmp` puis
`rename`, 0600). Une tâche :

| Champ | Sens |
|---|---|
| `id` | chaîne unique, donnée par l'AM à la création |
| `name` | nom affiché, unique |
| `destination` | nom d'une destination de sauvegarde existante |
| `remote_path` | dossier dans cette destination |
| `local_path` | dossier absolu sur la box |
| `direction` | `download` (cloud vers box), `upload` (box vers cloud), `both` (les deux) |
| `mirror` | `false` par défaut : rien n'est supprimé en face. `true` : ce qui disparaît de la source disparaît en face. Toujours `true` pour `both` |
| `archive` | `true` par défaut : ce qu'une synchro écrase ou supprime est déplacé dans l'archive |
| `archive_days` | `30` par défaut **(propriétaire, 2026-11-16)** : ce qui est dans l'archive depuis plus longtemps est effacé. `0` : rien n'est jamais effacé **(mon choix)** |
| `every`, `at`, `weekday` | comme un planning de sauvegarde, plus `hourly` (`at` = la minute) |
| `enabled` | interrupteur |
| `last_run`, `last_result`, `last_error`, `last_files`, `last_bytes` | dernier passage, écrits par l'AM |
| `first_pass_done` | `both` seulement, écrit par l'AM : la première passe a réussi |
| `needs_first_pass` | `both` seulement, écrit par l'AM : l'état de bisync est perdu, la tâche attend une confirmation |
| `needs_delete_confirm` | écrit par l'AM : trop de suppressions d'un coup (plafond d'un sens unique `mirror`, ou plus de la moitié d'un côté en `both`), la tâche attend une confirmation |

Les comptes cloud **sont les destinations de sauvegarde** **(propriétaire, 2026-11-16)** : même connexion, même
dialogue. Deux conséquences à connaître :

- Une destination **chiffrée** (`crypt`) ne montre que des noms chiffrés : un téléchargement n'y verrait
  aucun fichier ordinaire du compte, un envoi y poserait des fichiers illisibles depuis le site du
  fournisseur. Le téléchargement depuis une destination chiffrée est **refusé** ; l'envoi est accepté
  et le dialogue dit que les fichiers ne seront lisibles que par cette box **(mon choix)**. « Les
  deux » y est refusé aussi : un fichier posé depuis le site du fournisseur n'y serait pas lu.
- Une destination à `root` ne voit que l'intérieur de ce dossier, en général celui des sauvegardes.
  Pour synchroniser un autre dossier du même compte, il faut une seconde destination sans `root` :
  même compte, seconde connexion. Le dialogue le dit quand la liste de dossiers est vide.

### Ce que l'AM refuse à l'enregistrement

- `local_path` hors de `/DATA`, `/media` et `/mnt`, ou égal à l'une de ces racines, ou dans
  `/DATA/AppData` (données des apps), ou **racine d'un disque monté** (il faut un sous-dossier)
  **(mon choix)**. Le chemin est nettoyé et ses liens symboliques résolus avant le contrôle.
- `direction: download` ou `both` avec une destination chiffrée.
- Deux tâches dont les `local_path` se contiennent, ou deux tâches sur la même destination dont les
  `remote_path` se contiennent.
- `remote_path` vide pour `upload` (et `both`) : une tâche n'écrit jamais à la racine d'une
  destination, là où vivent les sauvegardes. Pour tous les sens, un `remote_path` qui commence par
  `casaos-restic`, `casaos-system`, ou par le nom d'une app sauvegardée sur cette destination.
- Supprimer une destination qu'une tâche utilise : refusé, avec le nom de la tâche.

Le dossier `local_path` d'un téléchargement est créé **à l'enregistrement**, jamais pendant un
passage. Tous ces contrôles sont refaits au début de chaque passage : un lien symbolique peut avoir
changé, une app installée depuis peut porter le nom du `remote_path`.

Qui peut créer une tâche : les administrateurs seulement **(mon choix)** ; à brancher sur le contrôle
que les routes `/backup/*` font aujourd'hui (à relever avant le code).

### Un passage à sens unique

Nouveau fichier `pkg/rclone/syncdir.go`, une méthode `SyncDirectory(ctx, src, dst, options)` :

- `mirror: false` : `sync/copy`. `mirror: true` : `sync/sync`.
- `archive: true` : `_config.BackupDir` = l'archive du côté **écrit**, dossier voisin daté :
  - téléchargement : `<parent de local_path>/.<nom du dossier>-archive/<stamp>` ;
  - envoi : `<remote_path>-archive/<stamp>` sur le même remote que `dstFs`.
  rclone exige le même remote et aucun chevauchement : un dossier voisin remplit les deux.
- `mirror: true` ajoute `_config.MaxDelete` = la moitié du nombre de fichiers du côté écrit
  (compté par `operations/size`, au moins 10) **(mon choix)**. C'est un **plafond**, pas un tout ou
  rien (essai T4) : rclone supprime jusqu'au plafond, puis arrête le passage en erreur
  (« failed to delete N files »). Retenter au créneau suivant supprimerait une autre moitié, puis
  une autre : après cette erreur la tâche **ne tourne plus**, l'AM écrit `needs_delete_confirm`, et
  l'UI demande « Confirmer les suppressions » (une passe sans plafond). Avec l'archive, ce que la
  passe plafonnée a supprimé y est ; **archive éteinte, jusqu'à la moitié du côté écrit peut être
  perdue avant l'arrêt**, et le dialogue le dit quand on éteint l'archive.
- Les liens symboliques locaux ne sont pas suivis (comme `verificationLocal`).

Avant chaque passage, dans `service/sync_run.go` **(mon choix)** :

1. La source doit répondre : `operations/list` sur le dossier source. Une erreur arrête le passage.
2. **Source vide, côté écrit non vide : le passage est refusé**, que `mirror` soit actif ou non, avec
   le message « la source est vide ». C'est la parade au disque non monté et au jeton expiré.
3. `local_path` doit exister et être un dossier, dans les deux sens. Absent, le passage échoue avec
   « le dossier de la box est introuvable » : c'est le disque non monté côté écrit, et sans cette
   règle tout le compte cloud serait copié sur le disque système. Comme la racine d'un disque est
   refusée, le dossier et son archive voisine sont toujours sur le même disque.

Le passage s'inscrit dans le registre vivant (`beginLive`) avec un champ nouveau `kind: "sync"` et le
nom de la tâche à la place de l'app. Il apparaît donc dans `GET /backup/running`, avec sa progression
par `core/stats`, et s'arrête par la route existante. À la fin, l'AM écrit `last_*` dans `sync.json`. Pas d'événement
sur le bus de messages : aucun écran ne le lirait, la fenêtre relit ses tâches **(mon choix)**.

### Un passage dans les deux sens (mon choix)

Méthode `BisyncDirectory` dans le même fichier, job `_async` sur `sync/bisync` :

- `path1` = `local_path`, `path2` = le dossier du cloud.
- `workdir` = `/var/lib/casaos/backups/bisync/<id>` : l'état de bisync (ses listes des deux côtés)
  vit avec les autres fichiers de sauvegarde, pas dans le cache du compte qui fait tourner rclone.
- **Première passe** (`first_pass_done` faux) : `resync: true`, **suivie aussitôt d'une passe
  normale**. La première réunit les deux côtés et ne supprime rien ; si un fichier diffère, la
  version de la box est gardée **même si elle est la plus ancienne**, et celle du cloud part dans
  l'archive du cloud (essai B1), ce que le dialogue dit. On garde ce réglage par défaut de bisync
  **(mon choix)** ; `resyncMode` permettrait « la plus récente gagne », pas essayé. La seconde passe
  est nécessaire : tant qu'aucune passe normale n'a réussi, une coupure oblige à tout refaire (essais
  K1, K3 et R1). L'AM écrit `first_pass_done` après les deux.
- **Passes suivantes** : les ajouts, modifications **et suppressions** de chaque côté sont portés en
  face. bisync n'a pas d'interrupteur « ne pas supprimer en face » : pour `both`, `mirror` vaut
  toujours `true`, et c'est l'archive qui protège : accepté **(propriétaire)**. C'est moins que ce
  que Synology propose ; le dialogue le dit.
- `archive: true` : `backupDir1` et `backupDir2`, les deux dossiers voisins décrits plus haut, un par
  côté, avec le `<stamp>` de la passe (bisync ne date rien lui-même).
- `maxDelete: 50` (un pourcentage, le défaut de bisync) : au-delà de la moitié d'un côté à supprimer,
  bisync s'arrête sans rien changer, avec l'erreur « too many deletes », **et recommence à chaque
  passe** tant que personne ne tranche (essai B5). L'AM écrit alors `needs_delete_confirm` ; l'UI
  demande « Confirmer les suppressions », ce qui lance une passe avec `force: true` (essai F1 : les
  fichiers partent dans l'archive).
- Conflits (un fichier modifié des deux côtés entre deux passes) : `conflictResolve: none` et
  `_config.SuffixKeepExtension: true`. Les deux versions sont gardées des deux côtés, renommées
  `photo.conflict1.jpg` (celle de la box) et `photo.conflict2.jpg` (celle du cloud). Sans cette
  option le nom devient `photo.jpg.conflict1`, qui ne s'ouvre plus d'un clic (essais B3 et C1).
- `resilient: true` et `recover: true` : après un arrêt depuis l'interface ou une coupure, la passe
  suivante reprend seule et finit la copie (essais K3, K5 et R1). **Mais la reprise fait réapparaître
  les fichiers supprimés depuis la dernière passe réussie** (essais R2 et R2b : un fichier supprimé
  sur la box avant la passe interrompue est de retour sur la box après la reprise). C'est l'un des
  anti-objectifs, et bisync n'offre rien pour l'éviter. Limite acceptée **(propriétaire)** : rien n'est
  perdu, et le résultat de la passe de reprise dit « reprise après une interruption : des fichiers
  supprimés depuis ont pu réapparaître ». L'UI le dit aussi avant un « Arrêter » sur une tâche
  `both`.
- `maxLock: "2m"` : une coupure de courant laisse un fichier verrou dans le `workdir`, et sans cette
  option la passe suivante est refusée (essai K4 ; d'après la doc de rclone ce verrou n'expire
  jamais). Avec, le verrou expire : refusé juste après la coupure, accepté 150 s plus tard (essai
  K5). Une passe qui dure plus de deux minutes garde son verrou : une seconde passe demandée à 150 s
  est refusée (essai R3), donc deux passes ne tournent jamais sur les mêmes dossiers, même si l'AM
  redémarre pendant que rclone continue. Une passe refusée pour verrou (« prior lock file found »)
  n'est pas une erreur à montrer : elle est retentée au créneau suivant.
- `createEmptySrcDirs: true` : les dossiers vides sont portés en face (essai E1). S3 n'a pas de
  dossiers vides.
- `checkAccess` éteint : il demande des fichiers témoins `RCLONE_TEST` posés à la main.
- Google Drive : les Google Docs n'ont ni taille ni empreinte ; ils sont ignorés
  (`skip_gdocs` sur le remote, quand le backend de la destination est `drive`). Pas essayé.

Les contrôles d'avant passage s'appliquent aux deux côtés : un côté vide alors que l'autre ne l'est
pas arrête le passage, sauf à la première passe.

**État perdu ou refusé.** L'API ne distingue pas une erreur passagère d'un état perdu : dans les deux
cas elle répond seulement « bisync aborted » (essais B4 et B5b ; le détail n'est que dans le journal
de rclone). L'AM regarde donc le `workdir` après un échec : s'il n'y reste ni `*.path1.lst` ni
`*.path1.lst-old`, l'état est perdu. (Sans `.lst` mais avec un `.lst-old`, c'est une passe coupée, que
la suivante reprend : essai K3.) L'AM **ne relance pas** de première
passe : elle écrit `needs_first_pass`, la tâche ne tourne plus, et l'UI demande « Refaire la première
passe », en disant qu'elle fait réapparaître les fichiers supprimés d'un seul côté depuis la dernière
passe réussie (essai B9 : c'est bien ce qui arrive). Changer `local_path`, `remote_path` ou
`destination` d'une tâche `both` efface son `workdir` et remet `first_pass_done` à faux ; supprimer
la tâche efface son `workdir`.

### La purge de l'archive

L'archive est gardée **30 jours (propriétaire, 2026-11-16)**. Après chaque passe **réussie**, l'AM
efface les dossiers `<stamp>` de l'archive dont la date (lue dans le nom, pas dans les dates des
fichiers) a plus de `archive_days` jours : `os.RemoveAll` côté box, `Purge` (`operations/purge`, déjà
dans le client) côté cloud, après une liste des dossiers par `operations/list`. Jamais après une passe
ratée, arrêtée ou en attente de confirmation : c'est là qu'on a besoin de l'archive. Un nom qui n'est
pas un `<stamp>` n'est jamais effacé. Une erreur de purge est journalisée et ne fait pas échouer la
passe **(mon choix)**.

### Le planificateur (mon choix)

`RunDueSyncs`, appelé chaque minute par une entrée de crontab à lui, avec **son propre verrou**
`syncTick` : les synchros passent une à la fois entre elles, mais ne tiennent pas `backupTick`. Une
synchro et une sauvegarde peuvent donc tourner ensemble ; `rclone rcd` sait mener plusieurs jobs.
Comme les sauvegardes : `last_run` est écrit que le passage ait réussi ou non, et un créneau manqué
est rattrapé une fois. `mostRecentSlot` apprend `hourly`.

Mise à niveau du système : même ordre que `RunDueBackups`. La synchro **s'inscrit d'abord** comme
opération en cours (ce que le core lit avant d'ouvrir une mise à niveau), **puis** regarde
`osUpgradeOpen` ; ouverte, elle se retire et son créneau reste dû. Sans cet ordre, une mise à niveau
pourrait démarrer au milieu d'une synchro. « Lancer maintenant » suit la même règle et répond 409
pendant une mise à niveau.

Écritures concurrentes : `sync.json` a son verrou. L'écriture des `last_*` relit le fichier sous ce
verrou et met à jour la tâche par `id` ; si elle a été supprimée entre-temps, rien n'est écrit. Un
`PUT` pendant un passage n'est donc jamais défait.

### Routes (AM, openapi)

- `GET /sync/tasks` : la liste, avec les `last_*`.
- `PUT /sync/tasks` : remplace la liste (comme `/backup/schedules`) ; l'AM garde les `last_*` des
  tâches dont l'`id` existe déjà et donne un `id` aux nouvelles.
- `POST /sync/tasks/{id}/run` : lance le passage maintenant, répond 202 ; 409 si cette tâche tourne
  déjà ou si une mise à niveau du système est ouverte.
- `GET /backup/destinations/{name}/folders?path=` : les sous-dossiers d'un dossier de la destination
  (`operations/list`, `dirsOnly`), pour choisir `remote_path` sans le taper.
- `POST /sync/tasks/{id}/first-pass` : confirme une nouvelle première passe d'une tâche `both` en
  `needs_first_pass`, répond 202.
- `POST /sync/tasks/{id}/confirm-deletes` : lance une passe qui accepte les suppressions d'une tâche
  en `needs_delete_confirm` (`force` en `both`, sans plafond en sens unique), répond 202.

Suivi et arrêt : routes `/backup/running` existantes.

### UI

Une **entrée à part dans les réglages** **(propriétaire, 2026-11-16)** : une carte « Synchronisation »
dans le menu des réglages de `TopBar.vue`, à côté de celle des sauvegardes (bouton
`showBackupDestinations`, `TopBar.vue:320`), qui ouvre une fenêtre à elle, nouveau composant
`settings/CloudSync.vue` **(mon choix pour le nom)**. La fenêtre des sauvegardes ne change pas. Une carte par tâche, dans le langage des cartes de destinations :

```
┌───────────────────────────────────────────────────────────┐
│ Photos                                    [ Activée  ●━ ]  │
│ Dropbox /Photos  →  /DATA/Gallery/Photos                   │
│ Tous les jours à 02:00 · rien n'est supprimé · archive     │
│ Dernier passage : hier 02:00, réussi, 212 fichiers, 1,4 Go │
│ [ Lancer maintenant ]  [ Modifier ]  [ Supprimer ]         │
└───────────────────────────────────────────────────────────┘
```

Le dialogue de création demande : nom, compte (liste des destinations, lien « Ajouter un compte » qui ouvre
la fenêtre des sauvegardes sur l'onglet `destinations`), dossier du cloud (liste de dossiers), dossier de la box (le sélecteur de
dossiers de Fichiers), sens, planning, puis deux interrupteurs :

- « Supprimer en face ce qui est supprimé à la source », éteint par défaut, avec la phrase
  « Éteint : aucun fichier n'est jamais supprimé par cette tâche. »
- « Garder une copie de ce qui est remplacé ou supprimé », allumé par défaut, avec le chemin de
  l'archive et « pendant 30 jours ».

Avec le sens « Les deux », le premier interrupteur est allumé et verrouillé, avec la phrase « Dans
les deux sens, une suppression d'un côté est toujours portée de l'autre ; la copie gardée ci-dessous
est ce qui protège. » Une tâche en `needs_first_pass` porte un bandeau et le bouton « Refaire la
première passe » ; en `needs_delete_confirm`, un bandeau « Plus de la moitié des fichiers d'un côté
ont disparu » et le bouton « Confirmer les suppressions ». Une tâche en cours se voit dans l'onglet « En
cours » avec les sauvegardes. Si `GET /sync/tasks` répond 404, la carte du menu n'est pas montrée.

Textes en anglais dans le code, traduits comme le reste (`$t`).

## Tests

- Unitaires AM : contrôles d'enregistrement (chemins refusés, chevauchements, chiffré refusé),
  créneaux `hourly`, refus sur source vide, `last_*` gardés par `PUT`, verrou distinct de `backupTick`.
- Avec le vrai rclone v1.75.1 (comme `signin_integration_test.go`), remotes locaux :
  copie simple ; fichier modifié à la source, l'ancien part dans l'archive ; `mirror` éteint, un
  fichier supprimé à la source reste en face ; `mirror` allumé, il part dans l'archive ; `MaxDelete`
  dépassé, le passage échoue et la tâche attend ; purge de l'archive : un dossier de 31 jours
  effacé, un de 29 gardé, rien d'effacé après un échec ; source vide, rien ne bouge ; arrêt en cours de
  passage.
- Deux sens, vrai rclone : première passe qui réunit ; ajout, modification et suppression portés
  dans chaque sens ; suppression gardée dans l'archive du bon côté ; conflit, deux copies renommées ;
  `workdir` effacé, la tâche passe en `needs_first_pass` sans rien toucher ; passage tué au milieu,
  la passe suivante reprend ; plus de la moitié d'un côté supprimée, rien ne bouge.
- UI : specs du dialogue et de la carte, entrée du menu absente sur 404.

## Essais sur rclone v1.75.1 (2026-11-16)

Image officielle `rclone/rclone:1.75.1`, `rclone rcd` sur un socket unix, appels par l'API comme le
fait l'AM. Les scripts sont dans `2026-11-16-synchro-planifiee-essais/` (cinq tours ; les parties S3
des tours 2 et 3 n'ont pas tourné, le serveur de test n'était pas joignable : S3 vient du tour 4). Dossiers locaux, un remote `alias`, un remote `crypt`, et un S3 servi par
`rclone serve s3`. **Pas essayé : un vrai compte Dropbox, Google Drive, OneDrive ou AWS S3.**

Sens unique :

| Essai | Résultat |
|---|---|
| T1 `sync/copy` + `_config.BackupDir` | la version écrasée est dans l'archive |
| T2 `sync/copy`, fichier supprimé à la source | il reste en face |
| T3 `sync/sync` + `BackupDir` | le fichier supprimé part dans l'archive |
| T4 `MaxDelete: 3`, 8 à supprimer | 3 partis dans l'archive, puis erreur « failed to delete 5 files » |
| T4b source vide, `MaxDelete: 0` | rien supprimé, erreur. Sans plafond tout serait supprimé : le contrôle d'avant passage est nécessaire |
| T5 `BackupDir` sur `alias` et sur `crypt` | marche, dossier voisin accepté ; noms chiffrés sur le disque |
| T5c `BackupDir` dans la destination | refusé : « destination and parameter to --backup-dir mustn't overlap » |
| S3 `BackupDir` sur le même bucket | marche ; un fichier dont seule la date change n'est pas renvoyé |

Deux sens :

| Essai | Résultat |
|---|---|
| B1 première passe, `_async` | job suivi par `job/status`, compteurs par `core/stats` `group=job/<id>` ; rien supprimé ; fichier différent : la box gagne, l'autre version dans l'archive du cloud |
| B2 ajout, modification, suppressions des deux côtés | tout porté en face, supprimés et écrasés dans l'archive du côté écrit |
| B3, C1 conflit | `photo.jpg.conflict1/2` par défaut, `photo.conflict1.jpg` avec `SuffixKeepExtension` |
| K1 arrêt pendant la passe qui suit la première passe | passe suivante refusée, « Must run --resync to recover » |
| K3 arrêt après une passe normale réussie | la passe suivante reprend et finit la copie |
| K4 `rclone rcd` tué (coupure de courant), sans `maxLock` | verrou laissé, passe suivante refusée |
| K5 même coupure avec `maxLock: 2m` | refusé aussitôt, accepté après 150 s, copie finie |
| R1 première passe, passe normale sans rien à faire, arrêt, reprise | la reprise marche |
| R2 suppressions faites avant une passe arrêtée | le fichier supprimé sur la box revient sur la box à la reprise ; celui supprimé côté cloud était déjà parti dans l'archive de la box avant l'arrêt |
| R2b même chose, coupure de courant | le fichier supprimé revient aussi ; un fichier `.partial` est resté sur la box |
| R3 passe de plus de 2 min avec `maxLock: 2m` | une seconde passe demandée à 150 s est refusée, le verrou est tenu |
| B5, F1 plus de la moitié supprimée | « too many deletes », rien ne bouge, répété à chaque passe ; avec `force` les suppressions passent par l'archive |
| B5b un côté vide (disque absent) | « bisync aborted », rien ne bouge ; le dossier revenu, la passe suivante marche |
| B4 `workdir` effacé | « bisync aborted », rien ne bouge, plus aucune passe |
| B9 première passe refaite | un fichier supprimé d'un seul côté réapparaît |
| E1 dossiers vides | portés avec `createEmptySrcDirs`, pas sans |
| Essais ratés | le premier essai d'arrêt (tour 1, B6) a arrêté une copie déjà finie ; l'essai « listes perdues d'un seul côté » (tour 2, L1) a tourné sur une tâche déjà bloquée par « too many deletes » : aucun des deux ne prouve rien |
| S3 deux sens | ajout, suppression, conflit et archive sur le bucket : comme en local ; une passe sans changement ne transfère rien |

## Reste à vérifier avant d'écrire le code

1. Un vrai compte Dropbox, Google Drive, OneDrive et un vrai S3, dans les deux sens (les essais
   n'ont vu que des dossiers locaux et le S3 de rclone). Google Drive : les Google Docs.
2. Comment reconnaître la racine d'un disque monté (LocalStorage le sait ; sinon comparer le
   périphérique du dossier et de son parent).
3. Sous quel compte tourne `rclone rcd` et s'il peut écrire dans `/DATA`, `/media` et `/mnt`.
4. Comment les routes `/backup/*` limitent aujourd'hui l'accès selon le compte.
5. L'UI a-t-elle un sélecteur de dossiers réutilisable hors de Fichiers ?
6. Le fichier `.partial` resté sur la box après une coupure (R2b) : qui le nettoie.

## Écarts à l'exécution (2026-11-16)

Décidés par l'assistant pendant l'exécution du plan, à relire. Le code fait foi.

- **Première passe d'une tâche à deux sens** : un côté vide est accepté tant que la première passe
  n'a pas réussi (sinon une tâche neuve dont un côté est vide ne démarrerait jamais). Elle ne
  supprime rien.
- **Une passe tourne sur le chemin réel du dossier** (liens symboliques résolus), archive comprise.
- **La purge de l'archive** ne touche que le côté où la tâche archive : la box pour un
  téléchargement, le cloud pour un envoi, les deux pour « les deux ».
- **Modifier les dossiers ou le sens d'une tâche** efface ses attentes de confirmation et son état de
  double sens : elle repart de zéro.
- **Résultat impossible à écrire** (disque plein) : la tâche est tenue à l'arrêt jusqu'au redémarrage
  du service, au lieu de retenter chaque minute avec un plafond neuf.
- **Dossiers de sauvegarde refusés** sans tenir compte de la casse, et aussi tout premier dossier qui
  contient des dossiers datés. À chaque passe d'une tâche qui écrit dans le cloud, un dossier qui
  contient `casaos-system` ou `casaos-restic` est refusé.
- **Autres destinations de la box** : un dossier qui est la racine d'une autre destination du même
  compte, qui la contient ou qui se trouve dedans est refusé, à n'importe quelle profondeur, à
  l'enregistrement et à chaque passe. Quand la configuration ne distingue pas deux comptes d'un même
  service (Drive, Dropbox, OneDrive), ils comptent pour un seul.
- **Le dossier d'un téléchargement** n'est créé qu'à la création de la tâche ou quand son dossier
  change, pas à chaque enregistrement.
- **Le nom d'une tâche** n'est montré qu'au propriétaire (opérations en cours, refus de supprimer une
  destination).
- **Box plus ancienne** : elle répond 400 « no matching operation was found », pas 404 ; l'UI cache
  l'entrée dans les deux cas.
- **Destination chiffrée** : l'UI ne peut pas le savoir ; elle montre un rappel sur tout envoi, et le
  refus du téléchargement ou du double sens vient de la box.
- **Pas d'événement sur le bus**, et **pas de pile complète** pour la vérification : un essai de bout
  en bout sur le vrai rclone, et la fenêtre pilotée dans un navigateur sur le faux serveur de
  développement.

## Décisions du propriétaire (2026-11-16)

Plus aucune question ouverte.

1. Les trois sens sont livrés d'un coup.
2. Les comptes cloud de la synchro sont les destinations de sauvegarde.
3. La synchro a son entrée à part dans les réglages.
4. L'archive est gardée 30 jours.
5. Dans les deux sens, une suppression est toujours portée en face ; l'archive suffit comme
   protection.
6. Dans les deux sens, les fichiers supprimés qui reviennent après un arrêt ou une coupure sont
   acceptés, avec un message.
