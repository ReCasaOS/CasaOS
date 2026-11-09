# Stockage : voir les pools et jeux de données ZFS (conception)

## Pourquoi

Un utilisateur (se3, discussion ReCasaOS/CasaOS#66, commentaire du 2026-11-09) a un pool ZFS en miroir de deux NVMe. L'onglet Stockage lui montre les disques matériels (`nvme-WD_BLACK_SN770...`) mais **aucune consommation** : un jeu de données ZFS n'est pas un périphérique bloc, `lsblk` ne le voit pas, et le dashboard ne sait rien de ZFS. Il demande de voir les pools, les jeux de données et les snapshots, et en donne une maquette.

Aujourd'hui, ReCasaOS sait seulement **ne pas casser** un disque ZFS : `DiskInUse` (`service/storage_devices.go`) traite `zfs_member` comme « en usage », donc le disque n'est jamais proposé au formatage. Rien ne lit ZFS.

Cette première étape est **en lecture seule** : voir, rien de plus. Pas de création, de destruction ni d'action sur un pool.

## Décisions du propriétaire

- Première étape **en lecture seule** : pools, jeux de données, snapshots, état du dernier scrub, arbre des disques du pool, capacité (propriétaire, énoncé de la tâche).
- Ne **pas** s'appuyer sur la proposition de ppenguin (`/v2/storage`, brouillon ReCasaOS/CasaOS-LocalStorage#10, et l'onglet « Pool » de CasaOS-UI#69) et ne **pas** l'attendre : ce travail est indépendant d'elle (propriétaire).
- Trois questions restent au propriétaire, en fin de document : **où** ça s'affiche, **les E/S en direct** dans cette étape ou non, et **qui fournit un vrai ZFS** pour la preuve.

## Ce qui existe déjà (relevé du code, 2026-11-09, `inkly/main`)

- **Aucune lecture de ZFS.** `git grep -i -E 'zfs|zpool'` sur `inkly/main` ne trouve, côté LocalStorage, que `DiskInUse` (`zfs_member`), son test et une ligne de CHANGELOG ; côté CasaOS (cœur), le contrôle de mise à niveau `root_zfs` (`pkg/osreadiness`, refus si **le système** est sur ZFS) ; côté CasaOS-UI, le texte de ce refus (`osReadiness.js`). Gateway et Common : rien.
- **LocalStorage** (Go) est le service des disques. Routes `/v1/disks` : liste, USB, `raid` (lecture dans `/sys/block/md*/md`, **sans appeler mdadm** : `service/raid.go`), `snapraid` (état + actions). Le Gateway route déjà le **préfixe `/v1/disks`** (`main.go`) : une nouvelle route dessous **ne demande aucun changement du Gateway**. Les routes `/v1` sont du Echo à la main avec JWT (jeton interne exempté) et l'enveloppe `model.Result{Success, Message, Data}` ; `/v2` est généré depuis `api/local_storage/openapi.yaml`.
- **Appeler un outil externe**, le précédent est SnapRAID : `service/snapraid_run.go` lance `snapraid` avec `exec.CommandContext`, **sans shell**, derrière une variable (`snapraidExec`) que les tests remplacent ; `hasSnapRAID` est un `exec.LookPath` ; l'absence de l'outil donne `installed: false` et le panneau se tait. `pkg/utils/command` et `CasaOS-Common/utils/exec` refusent les arguments qui échoueraient à `shsprintf`.
- **Le service tourne en root** (l'unité systemd n'a pas de `User=`) : les commandes de lecture de ZFS passent.
- **Dashboard** : `StorageManagerPanel.vue` est une fenêtre à onglets (`Storage`, `Merged Storage`, `Drive`). Le panneau appelle `this.$api.disks.*` (`src/service/disks.js`) à l'ouverture, **en silence si la route n'existe pas** (un LocalStorage plus ancien : le cas de `raid()`), et `SnapRaidPanel.vue` se relit toutes les 3 s tant qu'une action tourne puis s'arrête (`clearTimeout` au démontage). Le watcher d'onglet se repère par **indice** (0 et 2).
- **Ce que l'utilisateur a collé** est une sortie **réelle** de ZFS (OpenZFS 2.x : colonnes `CKPOINT`, `EXPANDSZ`, date de scrub à l'anglaise) : `zpool list`, `zpool status` d'un miroir sain, `zfs list`, `zfs list -t snapshot`, `zfs get written`, `zpool iostat`. Elle sert de premier jeu d'essai (voir « Tests »), mais c'est la forme **lisible par un humain**, pas celle qu'on parse.
- **Plusieurs versions d'OpenZFS** sont à supporter (hypothèse, à vérifier sur chaque distribution) : 2.1.x (Debian 12, Ubuntu 22.04), 2.2.x (Ubuntu 24.04), 2.3.x (Debian 13). La sortie JSON (`zpool status -j`, `zfs list -j`) n'existe qu'à partir de la 2.3 : **on ne peut pas compter dessus**.

## Ce que ça fait

### Quand ZFS n'est pas là, rien ne se voit

- LocalStorage répond `available: false` (avec une `reason` : `tools_missing` si `zpool` ou `zfs` est introuvable, `module_missing` si l'outil est là mais le module noyau pas chargé, `failed` si la commande a échoué autrement). Le dashboard **n'affiche alors aucun onglet ni message** et ne relance rien : la quasi-totalité des boxes n'a pas ZFS et ne doit rien voir changer (mon choix).
- Outils présents, module chargé, **aucun pool importé** : l'onglet reste masqué (mon choix). Les pools **exportés** (disques `zfs_member` dont le pool n'est pas importé) ne sont pas listés : `zpool import` scanne les périphériques, c'est lourd et c'est déjà de la gestion.

### Ce que la box lit

Trois routes de LocalStorage (mon choix de noms, le constructeur peut les ajuster), toutes en `GET`, toutes sous `/v1/disks/zfs` :

1. **`GET /v1/disks/zfs`** : disponibilité, pools (taille, alloué, libre, fragmentation, capacité, santé, état détaillé, dernier scrub, arbre des disques) et jeux de données (nom, type, utilisé, disponible, référencé, utilisé par les snapshots, point de montage, monté, état de la clé de chiffrement). **Pas de snapshots dedans.**
2. **`GET /v1/disks/zfs/snapshots?dataset=<nom>`** : les snapshots **d'un seul** jeu de données, du plus récent au plus ancien, **200 au plus** (`truncated: true` s'il y en a plus). Nom, `used`, `referenced`, `written`, date de création. Chargés **à l'ouverture** de la liste dans le dashboard, pas avant (mon choix : un pool peut avoir des dizaines de milliers de snapshots, `zfs list -t snapshot` sur tout le pool est lent et ne doit pas partir à chaque rafraîchissement).
3. **`GET /v1/disks/zfs/io`** : les compteurs d'E/S **cumulés** de chaque pool (octets lus et écrits, opérations), lus dans `/proc/spl/kstat/zfs/<pool>/io`, **sans lancer de commande**. Le dashboard calcule les débits en faisant la différence entre deux lectures. Voir « E/S en direct ».

### Comment elles sont lues

Uniquement ces commandes, en tableau d'arguments (**jamais** de shell), avec `LC_ALL=C` dans l'environnement pour que le texte soit en anglais quelle que soit la langue de la box (mon choix) :

| Besoin | Commande | Forme |
|---|---|---|
| Pools | `zpool list -H -p -o name,size,allocated,free,fragmentation,capacity,health,altroot` | tabulations, entiers exacts, `-` quand une valeur n'existe pas |
| État d'un pool | `zpool status -p -P <pool>` | **texte** (pas d'option `-H` pour `status`), nombres exacts, chemins complets |
| Jeux de données | `zfs list -H -p -t filesystem,volume -o name,type,used,available,referenced,usedbysnapshots,mountpoint,mounted,quota,keystatus` | tabulations, octets exacts |
| Snapshots d'un jeu | `zfs list -H -p -t snapshot -d 1 -S creation -o name,used,referenced,written,creation <jeu>` | tabulations ; `creation` en secondes Unix |

- **On parse les formes `-H -p`, jamais la forme lisible** (`1.81T`, `53%`) : elles sont arrondies et dépendent de la langue. Les colonnes sont choisies avec `-o` pour que leur **ordre ne dépende pas de la version**. `zfs get written` n'est pas lancée à part : `written` est une colonne de `zfs list`, et le jeu d'essai de l'utilisateur la montre justement pour les snapshots.
- **`zpool status` est le seul cas en texte.** On le lit par ses ancres (`pool:`, `state:`, `status:`, `action:`, `scan:`, `config:`, `errors:`), pas par position, et **toute ligne qu'on ne comprend pas est gardée telle quelle ou ignorée, jamais une raison de tout refuser**. L'arbre de `config:` se lit par l'indentation (2 espaces par niveau) : un nom, puis `STATE READ WRITE CKSUM`, puis éventuellement une remarque (`(resilvering)`, `was /dev/sdb1`). Les groupes sans état (`logs`, `cache`, `spares`, `special`, `dedup`) sont des en-têtes. Les types de vdev (`mirror-0`, `raidz1-0`, un disque) se déduisent du nom.
- **`status:` et `action:`** (ex. « One or more devices has been removed... ») sont gardés **en anglais, tels quels**, et montrés dans un détail repliable : ZFS ne les traduit pas et on ne le fera pas à sa place.
- **Le dernier scan** (`scan:`) est lu en une forme structurée **et** gardé brut : `none`, `scrub` ou `resilver`, en cours ou terminé ou annulé ou en pause, octets réparés, erreurs, durée, date de fin (ou pourcentage et temps restant s'il est en cours). La date n'a pas de fuseau : elle est lue dans le fuseau de la box (mon choix). **ZFS ne garde que le dernier scan** : un resilver récent efface le souvenir du dernier scrub, le dashboard dit donc « dernier scan », pas « dernier scrub ».
- **Les disques d'un pool** : la feuille de l'arbre est un nom de périphérique (`/dev/disk/by-id/nvme-WD_BLACK_SN770_..-part1`). On la résout en `EvalSymlinks` puis en disque parent par `sysfs` (le `diskOf` de `service/raid.go`), pour que le dashboard puisse dire « sur nvme0n1 ». Si ça ne se résout pas (disque absent, `UNAVAIL`), on garde le nom brut : **jamais une erreur**.

### Ne jamais montrer « tout va bien » quand on ne sait pas

- Si `zpool status` d'un pool échoue, dépasse son délai ou n'est pas compris, le pool est rendu avec `known: false` : la carte s'affiche **grise, « état inconnu »**, avec la capacité de `zpool list` mais **sans étiquette verte** (mon choix).
- Seul `ONLINE` est vert ; `DEGRADED` est orange ; `FAULTED`, `UNAVAIL`, `OFFLINE`, `REMOVED`, `SUSPENDED` et **tout état inconnu** sont rouges. Une forme (coche, point d'attention, croix) accompagne la couleur, pas la couleur seule.
- Une barre de capacité passe à l'orange à **80 %** et au rouge à **90 %** (mon choix : au-delà de 80 %, ZFS ralentit nettement, c'est un conseil d'exploitant).

### Ce que le dashboard montre

Un nouvel onglet **« ZFS »** dans le gestionnaire de stockage (voir la question 1), avec, comme la maquette :

- **Une carte par pool** : nom, étiquette de santé, disposition (`mirror-0`, `raidz1-0`...), disques membres (nom lisible et disque résolu), résultat du dernier scan (« Dernier scan : scrub, 0 erreur, 16 min 47 s, le 11 oct. à 00:40 »), barre de capacité avec alloué / libre / total, fragmentation, et la ligne d'erreurs de données (`No known data errors`, ou le nombre d'erreurs). Un détail repliable donne l'arbre complet des vdevs avec leurs compteurs `READ / WRITE / CKSUM`, et les lignes `status:` / `action:` brutes.
- **Un arbre de jeux de données** par pool : barre d'utilisation, utilisé, disponible, référencé, point de montage (ou « non monté », « legacy », « verrouillé » pour un jeu chiffré dont la clé n'est pas chargée), espace pris par les snapshots. Les zvols (`volume`) sont listés avec une icône à part et sans point de montage.
- **La barre d'un jeu** = `used / (used + available)` : c'est ce que ZFS lui-même annonce, il tient compte des quotas et réservations (mon choix). Elle n'est **pas** relative au pool.
- **Les snapshots**, sous chaque jeu, dans une liste repliable dont le contenu est chargé au premier dépliage : nom (la partie après `@`), taille propre, écrit depuis le précédent, date.
- **Les E/S en direct**, par pool, en une ligne (« Lecture 10,6 Mo/s (1,32 k op/s) · Écriture 41,1 Mo/s (1,74 k op/s) ») tant que l'onglet est ouvert et visible.
- **Aucun bouton d'action.** La maquette a « Pool », « Snapshots », « Manage » et « View/Restore » : rien de tout ça en première étape.

### Rafraîchissement

- `GET /v1/disks/zfs` : à l'ouverture de l'onglet, puis **toutes les 15 s** tant qu'il est l'onglet actif et la page visible, et un bouton « Actualiser » (mon choix). Un scrub qui avance se voit ainsi sans que la box soit sollicitée toutes les secondes.
- `GET /v1/disks/zfs/io` : **toutes les 3 s**, aux mêmes conditions (même rythme que le panneau SnapRAID). Dès que l'onglet n'est plus actif, **plus aucune requête**.
- Côté serveur, le résultat de la liste est **gardé 10 s** et partagé entre les clients (mon choix) : deux navigateurs ouverts ne doublent pas les commandes.

### E/S en direct : relevé, pas flux

L'utilisateur a suggéré `zpool iostat -n 1`. Un `zpool iostat` continu n'est **pas** le bon outil dans un service : un processus long par client, à arrêter proprement, qui reste coincé si le pool se fige. Les deux autres façons :

- **Compteurs `kstat` (mon choix)** : `/proc/spl/kstat/zfs/<pool>/io` donne des totaux (octets lus et écrits, opérations) ; la route les rend tels quels, avec l'heure de lecture du serveur, et le dashboard fait la différence entre deux relevés. Aucun processus, pas d'état côté serveur, rien à nettoyer ; le débit est **faux** seulement au premier relevé, que le dashboard n'affiche donc pas. Si le fichier n'existe pas (version ancienne), la ligne d'E/S **n'apparaît pas** : jamais un « 0 B/s » qui ne veut rien dire.
- **Repli** : un relevé unique `zpool iostat -H -p -y <pool> 1 1` par pool (une seconde de latence et un processus par lecture). À n'utiliser que si les compteurs `kstat` ne concordent pas avec `zpool iostat` sur un vrai pool : **le constructeur doit les comparer** (je ne sais pas si le noyau les compte exactement comme `zpool iostat`).

## Sécurité et garde-fous (le plus important)

- **Seulement des commandes de lecture**, et seulement celles du tableau ci-dessus. Aucune de ces routes ne peut monter, démonter, réparer, importer, supprimer. Aucun appel à `zpool import`, `zfs mount`, `zfs load-key`, `zpool scrub`...
- **Le seul paramètre qui vient du client** est le nom d'un jeu de données (snapshots). Il est accepté **s'il fait partie de la liste que la box vient de lire**, sinon 404 ; jamais passé tel quel, jamais commençant par `-`. Pas de shell, les arguments sont un tableau.
- **Délais** (mon choix) : 10 s par `zpool list` / `zfs list` / `zpool status`, 30 s pour toute la requête, 20 s pour les snapshots ; `WaitDelay` de 2 s après l'annulation pour ne pas rester bloqué sur les tuyaux.
- **Un pool « suspendu » ou un disque qui ne répond plus peut bloquer `zpool status` ou `zfs list` sans qu'on puisse tuer le processus** (attente disque non interruptible). Donc : **une seule exécution à la fois par commande** (les autres demandes attendent ou reçoivent le dernier résultat), et si une exécution précédente est encore bloquée, on **ne lance pas la suivante** : la réponse reprend le dernier résultat connu marqué `stale: true`, ou `busy: true` s'il n'y en a pas. Sans ça, le rafraîchissement toutes les 15 s empile des processus coincés.
- **Plafonds de lecture** : 4 Mio de sortie par commande (au-delà, on coupe et on le dit), 200 snapshots par réponse, pour que la route ne puisse pas saturer la mémoire d'une petite box.
- **Rien à écrire** sur le disque : pas de fichier d'état, pas de base.
- Les noms de jeux de données peuvent révéler le contenu (`immich-db`, `bitcoin-data`) : la route est derrière le même JWT que `/v1/disks`, au même niveau d'information que la liste des disques.

## Hors périmètre (non-objectifs)

- **Créer, détruire, importer, exporter** un pool ; créer, détruire, renommer, monter, démonter un jeu de données ; régler une propriété ; charger une clé de chiffrement.
- **Snapshots** : en créer, en supprimer, **revenir en arrière** (`rollback`), cloner, **restaurer** (le « View/Restore » de la maquette).
- **`zfs send` / `receive`** et toute réplication.
- **Lancer, arrêter ou suspendre un scrub ou un trim**, remplacer, attacher, détacher, mettre en ligne ou hors ligne un disque, `zpool upgrade` (changer les fonctions d'un pool peut le rendre illisible par GRUB).
- Installer ZFS, ou proposer à un utilisateur d'y passer.
- Les pools **exportés** (voir plus haut), la déduplication, le cache ARC, les statistiques par vdev ou par dataset, l'historique (`zpool history`), les graphiques d'E/S au-delà d'une ligne en direct.
- Utiliser un jeu de données ZFS comme **emplacement d'installation des apps** ou comme source d'un stockage fusionné : c'est un autre projet.
- La proposition `/v2/storage` de ppenguin : si elle est un jour fusionnée, ces lectures pourront devenir un de ses fournisseurs ; **rien ici ne la suppose**.

Suites possibles, non faites : alerte (notification) quand un pool passe `DEGRADED`, lancement d'un scrub, étiquette « membre du pool X » sur l'onglet Drive, `-j` quand la 2.3 sera la règle.

## Ce qui ferait échouer le projet même s'il « marche »

- Un pool **montré vert alors qu'on n'en sait rien** (statut illisible, délai dépassé, état que le code ne connaît pas).
- **Des processus `zpool` / `zfs` coincés qui s'empilent**, un par rafraîchissement.
- Un onglet ou un message ZFS qui **apparaît sur une box sans ZFS**, ou une erreur de console, ou une route qui rend 500 au lieu de `available: false`.
- Une **liste de snapshots géante** chargée d'un coup qui gèle le dashboard ou la box.
- **Un nom de jeu de données non vérifié** passé à `zfs` (argument en forme d'option).
- Une valeur **fausse** parce qu'on a lu la sortie arrondie, mal compté les unités (`-p` donne des octets), ou pris `used` d'un jeu pour « les données » alors qu'il inclut snapshots et enfants.
- Un **parseur qui plante ou répond vide** sur une ligne nouvelle d'une autre version d'OpenZFS au lieu de la sauter.
- Une ligne d'E/S **à 0** quand on n'a pas pu mesurer.
- Casser l'onglet Stockage des boxes sans ZFS, ou les modifications du panneau (qui est partagé avec les brouillons de ppenguin) rendues difficiles à fusionner.

## Contraintes

- **Aucune dépendance Go ni JS nouvelle.**
- **Aucun changement du Gateway ni de CasaOS (cœur)** : la route est sous un préfixe déjà enregistré.
- Un **dashboard plus récent qu'un LocalStorage** (route absente, 404) reste silencieux ; un **LocalStorage plus récent qu'un dashboard** n'a aucun effet visible.
- Les textes du dashboard : clé anglaise, `en_US.json` et `fr_FR.json`, avec le test de parité des clés des autres modules.
- Les changements du panneau principal (`StorageManagerPanel.vue`) restent **minces** : un onglet et un appel ; le reste dans des fichiers nouveaux (`ZfsPanel.vue`, `zfs.js`), et l'onglet est **ajouté en dernière position** pour ne pas déplacer les indices 0 et 2 du watcher d'onglet (mon choix : ça évite aussi un conflit avec l'onglet « Pool » du brouillon UI#69).
- Pas de `Co-Authored-By` dans les commits ni de ligne « Generated with Claude Code » dans les PR (le contrôle `no-ai-attribution` échoue).

## Comment (mon choix)

- **LocalStorage** porte la fonction (c'est lui qui gère les disques et qui a déjà RAID et SnapRAID). Routes sous `/v1/disks/zfs` plutôt que `/v1/storage` (déjà pris par les volumes montés) ou `/v2/storage` (le brouillon de ppenguin).
- Un fichier `service/zfs.go` (exécution, parseurs, regroupement), `route/v1/zfs.go` (3 poignées), sur le modèle de `raid.go` / `snapraid.go` : `var zfsExec = func(ctx, args...)` remplaçable par les tests, `hasZFS` par `exec.LookPath`.
- **Parseurs purs** sur du texte, sans exécution : `parseZpoolList`, `parseZpoolStatus`, `parseZfsList`, `parseSnapshots`, `parseKstatIO`. C'est ce qu'on teste le plus.
- Réponse **en octets et en secondes Unix** ; la mise en forme (Go, Mo/s, « il y a ») est du ressort du dashboard.
- Dashboard : `src/components/Storage/ZfsPanel.vue`, `zfs.js` (arbre des jeux de données, étiquettes et couleurs, débits à partir de deux relevés, tous testés sans DOM), `src/service/disks.js` (`zfs()`, `zfsSnapshots(dataset)`, `zfsIo()`), dans la même veine que `SnapRaidPanel.vue` / `snapraid.js`.
- **Une PR par dépôt**, ouvertes prêtes, jamais fusionnées par moi : LocalStorage d'abord, le dashboard ensuite ; le dashboard fonctionne sans LocalStorage plus récent (il se tait).

## À la charge du constructeur

- Les noms et le JSON exacts des routes et des champs (ce document donne les champs, pas leur orthographe).
- Les **colonnes qui existent réellement** dans les versions 2.1, 2.2 et 2.3 d'OpenZFS (`-o` refuse une propriété inconnue : si l'une manque sur une version, la retirer de la liste ou la lire autrement) et la forme exacte de `-p` pour `fragmentation`, `capacity`, `quota` et `compressratio`.
- Ce que rendent `zpool list -H` et `zfs list -H` **sans aucun pool** ou sans aucun jeu de données (sortie vide, ligne « no pools available », code de retour) : les deux doivent donner une liste vide, pas une erreur.
- Les formes de la ligne `scan:` (scrub / resilver, en cours / terminé / annulé / en pause) et les remarques de fin de ligne de l'arbre.
- Le choix exact entre `kstat` et `zpool iostat` pour les E/S après comparaison sur un vrai pool.
- Les messages de la phrase de synthèse ; la disposition de la carte et de l'arbre.

## Tests et preuve

- **Go** (dans `golang:1.26`, `go generate ./...` d'abord), sur des **fichiers de sortie réels** dans `service/testdata/zfs/` :
  - un miroir **sain** (la sortie réelle de se3 : `pool1`, `mirror-0`, deux NVMe, scrub propre) ;
  - un pool **dégradé** (un disque `UNAVAIL` ou `FAULTED`, ou `OFFLINE`, avec `status:` et `action:`, et des erreurs de lecture) ;
  - un scrub **en cours**, un resilver, un scrub **annulé** ;
  - un pool avec `logs`, `cache`, `spares` ;
  - un pool **sans jeu de données enfant** (seulement la racine) et une sortie de `zfs list` **vide** ;
  - **aucun pool**, **module absent**, **outil absent** ;
  - une ligne inconnue au milieu d'un `status`, une sortie tronquée, un pool dont `status` **dépasse son délai** (faux exécuteur) ;
  - la liste de snapshots tronquée à 200, un nom de jeu **inconnu** ou **commençant par `-`** (404, rien n'est exécuté) ;
  - les compteurs `kstat` (valides, fichier absent, fichier mal formé) ;
  - la garde « une exécution à la fois » : un exécuteur qui ne rend jamais la main ne fait pas partir un deuxième processus.
- **Où prendre de vrais fichiers de sortie** (mon choix) : (1) ce que se3 a déjà collé (sain, forme lisible), à compléter en lui demandant les **mêmes commandes avec `-H -p`** ; (2) pour le dégradé, le scrub et le reste, une **machine Ubuntu jetable** (le noyau Ubuntu embarque en principe le module `zfs`, à vérifier) avec `apt install zfsutils-linux` et des **pools sur fichiers** : `truncate -s 1G /tmp/a /tmp/b && zpool create t mirror /tmp/a /tmp/b`, puis `zpool offline t /tmp/b`, `zpool scrub t`, `zpool status -p -P t`. Le Docker de Windows (noyau WSL2) n'a pas le module ZFS : **je ne peux pas capturer de sortie ici**. Tant qu'elles ne sont pas capturées, les fichiers de test sont écrits d'après la documentation d'OpenZFS et **marqués comme tels**.
- **Dashboard** (vitest, Node 22, `corepack pnpm`) : arbre des jeux de données (racine, enfants, zvol, non monté, verrouillé), couleur et forme par état, seuils de capacité, calcul des débits (le premier relevé n'affiche rien), le panneau avec une API simulée (`available: false` → aucun onglet ; dégradé → étiquette rouge ; statut inconnu → grise ; **le rafraîchissement s'arrête quand on quitte l'onglet**), parité `en_US` / `fr_FR`. ESLint vert.
- **Preuve sur une vraie box** : un pool réel (celui de se3 ou le propriétaire s'il en a un), ouvrir l'onglet, comparer chaque chiffre à `zpool list` / `zfs list`, lancer une charge pour voir les E/S bouger, ouvrir une liste de snapshots, **et** une box **sans ZFS** pour constater qu'aucun onglet ne paraît. Je n'ai pas de box ZFS ici : tant que ce n'est pas fait, c'est écrit « non vérifié sur le matériel ».

## Composants et livraison

1. **CasaOS-LocalStorage** : `service/zfs.go`, `route/v1/zfs.go`, enregistrement des routes, tests, CHANGELOG. Une PR.
2. **CasaOS-UI** : `ZfsPanel.vue`, `zfs.js`, `disks.js`, l'onglet dans `StorageManagerPanel.vue`, textes fr/en, specs. Une PR.
3. **CasaOS-Install** : épingler les deux versions dans une release, comme d'habitude (à la main du propriétaire, pas dans cette étape).

Ordre : LocalStorage, puis le dashboard, puis la release.

## Questions au propriétaire — réponses (2026-11-09 : « reco pour les 3 »)

Tranchées en suivant mes recommandations : (1) **onglet « ZFS » séparé** ; (2) **E/S en direct dans cette étape**, par les compteurs `kstat` ; (3) **fixtures d'après la documentation d'OpenZFS tant qu'on n'a pas de vraie sortie**, marquées comme telles ; la demande à se3 et la réponse publique restent au propriétaire (rien n'est posté). La construction suit.


1. **Où ça s'affiche ?** (propriétaire) Un **onglet « ZFS »** séparé dans le gestionnaire de stockage (ma recommandation : un jeu de données n'est pas un périphérique, l'onglet Stockage est bâti sur `lsblk`, et l'onglet apparaît seulement avec ZFS) ; ou une **section « ZFS »** en haut de l'onglet Stockage (plus visible, mais elle mélange deux modèles).
2. **Les E/S en direct sont-elles dans cette première étape ?** (propriétaire) Oui par les compteurs `kstat`, un petit endpoint de plus, c'est ce que l'utilisateur a demandé (ma recommandation) ; ou **non**, on livre pools / jeux / snapshots d'abord et on ajoute les E/S ensuite.
3. **Qui fournit un vrai ZFS pour la preuve ?** (propriétaire) Je propose de demander à se3, dans la discussion #66, la sortie de `zpool list -H -p`, `zpool status -p -P` et `zfs list -H -p` (et de les tester ensuite sur sa box), et de prendre le reste sur une machine Ubuntu jetable ; **y a-t-il un ZFS chez toi** à utiliser aussi ? Et la **réponse publique** à se3 est à toi (je ne poste rien).
