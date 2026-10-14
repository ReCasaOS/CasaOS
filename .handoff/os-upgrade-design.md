# Mise à jour de l'OS depuis le tableau de bord ReCasaOS : conception finale

Date : 2026-10-09. Conception seulement, rien n'est construit. Les nombres marqués « (estimation) » sont les miens, aucune source ne les donne.

## 1) La réponse en un paragraphe

**Oui, il faut le prévoir, et Debian 11 → 12 est le saut urgent.** Debian 11 est sorti du support LTS le 2026-08-31 et les `.deb` de sécurité de bullseye ne sont déjà plus servis par `security.debian.org` (404 observé le 2026-10-09). Debian 12 → 13 peut attendre (bookworm est en LTS jusqu'au 2028-06-30). Ubuntu 22.04 a une échéance au 2027-06-01.

Je recommande de construire **par paliers**, pas en un bloc :
1. Un contrôle en lecture seule (badge de fin de support, rapport de faisabilité).
2. Un plan à blanc : le vrai résolveur apt contre bookworm, dans un bac à sable, sans toucher `/etc/apt`.
3. Le saut Debian 11 → 12 en bêta, avec un bouton par saut. Il couvre le Debian 11 du propriétaire, qui est le cas à traiter en premier.
4. Ubuntu 22.04 → 24.04 ensuite, Debian 12 → 13, puis Ubuntu 24.04 → 26.04 en dernier.

Le bouton pilote la procédure documentée par Debian et Canonical dans une unité systemd transitoire. Il est réversible jusqu'à un marqueur nommé et vérifié après le redémarrage. Le redémarrage est un état normal de l'automate, pas une panne.

**Ce que ça ne promet pas :**
- Aucun retour arrière de l'OS (Debian ne documente pas de rétrogradation, Canonical ne permet plus d'annuler après le téléchargement).
- Aucune preuve sur matériel réel (nom des cartes réseau, firmware, DKMS, RAID, LUKS, vrai GRUB, ARM). QEMU ne les prouve pas.
- Une machine qui ne redémarre pas ne peut pas afficher son propre tableau de bord. Il faut un snapshot ou une console avant de cliquer.
- Les sauts Ubuntu ne sont livrés que si un test préalable prouve que Docker survit à l'outil de Canonical.
- Aucune chaîne automatique 11 → 12 → 13.

## 2) Ce qui existe déjà et qu'on réutilise

**À réutiliser tel quel** (branches `feat/docker-update`, non fusionnées) :
- `systemPackageUpdater` : un mutex, des coutures (seams) testables, `untranslatedCommand` (LC_ALL=C).
- Le lancement : `systemd-run --no-block --collect`, la ligne 1 `QUEUED <nonce>` écrite par le cœur, les marqueurs à nonce de `pkg/dockerpkg/run.go`, la queue de log bornée à 128 Kio.
- `maintenanceBusy`, `dpkgLockHeld` (F_GETLK), les opérations AppManagement en échec fermé, `headerTokenOnly`, les 409 avec `data.error_code`.
- La table « conteneurs qui ne reviendront pas » (`GET /v1/sys/docker/containers`), `freeDiskBytes`, le Hub d'alertes et son fichier `*.notified`.
- Côté UI : modale sans dialogue Buefy pour les chaînes tierces, table code → texte en/fr, chaîne de `setTimeout` qui survit aux erreurs.
- Côté CI : le squelette QEMU/KVM de `install-check.yml` et `docker-update-proof.yml`, avec le rapport « rien n'est jugé dans l'invité » et `Authorization: Internal <secret>`.
- Le binaire du cœur est statique (`CGO_ENABLED=0`, netgo). Une sous-commande du cœur survit donc à une mise à jour de libc6 en cours de route.

**Ce qui ne convient pas tel quel :**
- **Le statut Docker.** `system_package_docker_run.go:397-410` déclare « échec / no_result » toute exécution sans marqueur terminal, avec une unité inactive. Copié tel quel, il rapporterait chaque saut réussi comme un échec, parce que l'unité transitoire disparaît au redémarrage.
- **`maintenanceBusy`.** Il ne voit que les unités et le verrou dpkg. Dans le trou du redémarrage, la mise à jour automatique nocturne ou une sauvegarde peuvent démarrer.
- **Le garde-fou Docker.** C'est une liste blanche, et une montée d'OS retire légitimement des paquets obsolètes. Il faut un contrôle par ensemble protégé.
- **Le plancher disque** de 1 Gio et **l'abandon du polling** à 10 minutes ne sont pas adaptés.
- **`isDebianFamily()`** (`system_package.go:189`) admet Ubuntu, Raspbian, Mint, Pop, DietPi, OMV et Proxmox par `ID_LIKE`. Il faut une barrière stricte `osUpgradeSupport()`.
- **Le bouton « Paquets système »** ne peut pas atteindre l'état « full-upgrade sur l'ancienne release » (`install --only-upgrade`, et `system_package_test.go:94` interdit `dist-upgrade`). L'étape « mettre à jour la version actuelle » est donc un nouveau code.
- **La sauvegarde AppManagement** ne contient pas l'état de l'OS (`/etc`, `passdb.tdb`, fstab, réseau). Elle protège les données, pas l'OS.

**Prérequis :** faire atterrir ces branches et lancer au moins une fois `docker-update-proof.yml` sur GitHub. Il n'a jamais tourné, et tout le reste en dépend.

## 3) Périmètre

### Sauts, un par ligne de la table `hops.go`

Chaque ligne a un état : `off`, `report`, `beta` ou `stable`. Elle ne passe à `beta` qu'après trois exécutions QEMU vertes consécutives.

| Saut | Mécanisme | Quand |
|---|---|---|
| Debian 11 bullseye → 12 bookworm | apt (procédure des notes de version) | en premier |
| Ubuntu 22.04 → 24.04 | `do-release-upgrade` | après le test S0 (§8) |
| Debian 12 bookworm → 13 trixie | apt | ensuite |
| Ubuntu 24.04 → 26.04 | `do-release-upgrade` | en dernier, pas avant 2026-11-28 (60 jours après l'ouverture du 2026-09-29) |

### Hôtes acceptés

- `ID` exactement `debian` ou `ubuntu` (jamais `ID_LIKE`).
- Ubuntu LTS seulement.
- systemd en PID 1, `apt-get`, cœur en root.
- **amd64 seulement** en v1.
- Machine physique ou VM (une VM reçoit le message « faites un snapshot hyperviseur »).

### Refusé, avec la raison

| Refusé | Raison |
|---|---|
| arm64 (UEFI inclus), armhf, armel, i386 | Rien ne peut être prouvé : pas de KVM sur les runners hébergés. Sur 32 bits, la transition `time_t` 64 bits casse les ABI. i386 n'est pas montable vers trixie. |
| Raspberry Pi OS et toute carte Pi, Armbian, DietPi, OpenMediaVault, Proxmox, TrueNAS | Le fournisseur dit de réinstaller (Raspberry Pi) ou a son propre outil. Les marqueurs de détection sont inférés et non vérifiés : en cas de doute, refus. |
| Dérivés (Mint, Pop, Zorin…), testing/sid, Ubuntu non-LTS | Hors table. |
| Ubuntu 20.04 | L'installeur ne l'a jamais supporté et il n'y a pas de test CI. Le dépôt apt Docker de focal est figé depuis 2025-04-18. On affiche seulement la fin de support. |
| Conteneurs / LXC / WSL, invités Xen | Le noyau et le réseau appartiennent à l'hôte. Xen renomme ses interfaces (`enX#`). |
| Racine ZFS ou LUKS, md dégradé, `/bin` `/sbin` `/lib` non liens symboliques, chargeur ≠ GRUB | Causes connues de machine qui ne démarre plus. |
| `/var/log/casaos` sur tmpfs/zram | Le journal d'état disparaîtrait au redémarrage. |
| Services d'hôte : postgresql, mariadb/mysql, slapd, dovecot, rabbitmq, libvirt, strongswan, bacula, request-tracker | Leurs migrations propres à chaque release ne sont pas gérées. |
| Swarm, Docker snap ou d'origine inconnue | Hors modèle. |
| Paquets en `hold` (hors famille Docker), alias de suite (`stable`, `oldstable`…) vers une autre release, pins, proposed-updates, architectures étrangères avec paquets installés | Le solveur apt échoue ou dérive. |

Debian 13 et Ubuntu 26.04 sont « dernière version » : c'est un état neutre, pas un refus.

**Hors périmètre du saut :** les paquets du moteur Docker (la source apt Docker est mise en pause pendant le saut), les conteneurs et volumes, les disques de données et pools mergerfs (vérifiés seulement), les binaires ReCasaOS (pas de relance de l'installeur sauf bouton Réparer).

## 4) Le parcours dans le tableau de bord

Une modale « Système d'exploitation », ouverte depuis un badge dans la barre du haut et une ligne dans « Paquets système ». Les textes sont des codes traduits en/fr, jamais comparés en anglais.

1. **Aperçu.** Exemple : « Debian 11 ne reçoit plus de correctifs de sécurité Debian depuis le 31 août 2026. » Dates affichées au mois près quand les sources divergent (§12). Saut proposé : Debian 11 → 12.
2. **Faisabilité.** Les contrôles par gravité : bloquants, avertissements, ok. Chaque bloquant a une phrase et, quand c'est possible, une action (« Redémarrer maintenant », « Mettre à jour d'abord »). Bouton « Copier le rapport » sans secrets. Rien n'est modifié.
3. **Mettre à jour Debian 11.** Bouton séparé, avec sa propre confirmation. Il amène la release actuelle à son dernier état (11.11 plus le dernier instantané de sécurité), ce que les notes de Debian exigent avant le saut. Si le noyau a changé ou si un redémarrage est demandé : « Redémarrez maintenant. Cela prouve aussi que la machine redémarre sur la release actuelle. » Puis on refait le contrôle.
4. **Plan.** Calculé dans un bac à sable, `/etc/apt` n'est pas touché. Il affiche :
   - le diff des sources, les sources tierces mises en pause, la liste des paquets retirés (en évidence ceux installés à la main) ;
   - « À télécharger » et « Espace supplémentaire » d'apt, qualifiés d'**estimation** parce que le calcul définitif n'est possible qu'après la mise à niveau minimale ;
   - les conteneurs qui ne reviendront pas après redémarrage (politique `no`), et la table des fichiers de configuration conservés.
5. **Confirmation.** La **carte de reprise s'affiche avant le formulaire** : accès console, série ou hyperviseur (les tunnels Tailscale ou Cloudflare ne comptent pas, ils tombent avec la carte réseau), mode secours de l'installeur, aucun retour arrière, Samba et SSH vont redémarrer, les applications s'arrêtent au redémarrage. Puis :
   - le filet de sécurité déclaré : snapshot / console + sauvegarde / « j'accepte de réinstaller » ;
   - les acquittements : pas de coupure, sources en pause, retraits relus ;
   - l'âge de la dernière sauvegarde AppManagement ;
   - le nom d'hôte à retaper.
   Le bouton reste grisé tant que ce n'est pas complet, et le serveur revérifie tout.
6. **Progression.** Étapes, durée écoulée, « dernière activité il y a N min ». Le polling ne s'abandonne jamais tant qu'un saut est ouvert. Texte : « Ne coupez pas l'alimentation. Un paquet qui reste silencieux plusieurs minutes est normal. »
7. **Prêt à redémarrer.** Bannière persistante dans la barre du haut même modale fermée, avec rappels. Bouton « Redémarrer maintenant ». Pas de redémarrage automatique en v1.
8. **Redémarrage.** Surcouche « connexion perdue, c'est attendu », sans délai d'abandon. Après 15 minutes, état `reboot_stalled` et conseil de regarder l'écran. Si le jeton est refusé après le redémarrage, reconnexion puis reprise de la vue.
9. **Résultat.** Réussi, réussi avec constats (chaque constat a son action), ou échec (par code, réversible ou non, carte de reprise, journal, options de reprise). Le saut suivant n'est proposé qu'ici, avec un conseil de quelques jours de recul.

Un badge dans la barre du haut (« redémarrage requis / en cours / à examiner ») sert d'entrée si le tableau de bord est rouvert après le redémarrage. Une alerte push par état important (dédoublonnée dans `os-upgrade.notified`).

## 5) Architecture

### Principe

Le cœur calcule, valide et décide. Une **sous-commande racine du binaire du cœur** (`casaos os-upgrade-run`, dispatchée avant `flag.Parse`) exécute les phases. Je préfère cela à un gros script sh (environ 450 lignes) : la logique (réécriture des sources, analyse du plan, garde-fous, boot_id) se teste nativement sous Windows, et la liste de paquets protégés n'existe qu'une fois, en Go. Le cœur refuse de lancer un exécutable dont le chemin finit par ` (deleted)`.

### Endpoints (`/v1/sys`, groupe JWT, POST derrière `headerTokenOnly`, refus en 409 + `data.error_code`)

| Endpoint | Rôle |
|---|---|
| `GET /os` | identité, statut de fin de support, saut proposé. Pas d'apt, pas de réseau. |
| `GET /os/upgrade/readiness` | contrôles locaux (< 2 s, échec fermé : un contrôle qui ne répond pas est un refus) |
| `POST /os/upgrade/refresh` | phase `refresh` : amener la release actuelle à jour |
| `POST /os/upgrade/plan` | phase `plan` : bac à sable en lecture seule, produit `plan_id` |
| `POST /os/upgrade/start` | `{plan_id, safety_net, hostname, accept_removals[], disable_sources[]}` : revérifie tout, écrit `CONFIRMED`, lance `run` |
| `GET /os/upgrade/status` | jamais de mutex, jamais d'apt : dérivé de `run.json`, du journal, de `systemctl show` et de `/proc/sys/kernel/random/boot_id` |
| `POST /os/upgrade/reboot` | seulement en `awaiting_reboot` ; `sync` puis le `SystemReboot` existant, émis par le cœur après la fin de l'unité |
| `POST /os/upgrade/resume {continue\|immediate_configure_off}` | relance `recover` après clic du propriétaire |
| `POST /os/upgrade/ack {discard\|acknowledge}` | abandonner un plan, ou acquitter un résultat |
| `POST /os/upgrade/repair` | relance l'installeur existant seulement si un constat dit qu'une unité ou un fichier ReCasaOS manque |
| `GET /os/upgrade/runbook` | texte généré par saut (§9, PR 3) |
| `POST /os/upgrade/beta` | crée ou retire le fichier `/etc/casaos/os-upgrade.beta` |

Le bundle de sauvegarde de `/etc` (il contient shadow et les clés d'hôte) n'est jamais téléchargeable.

### Unité systemd

```
systemd-run --quiet --no-block --collect --unit=casaos-os-upgrade \
  --property=Type=exec --setenv=CASAOS_OSU_PHASE=<refresh|plan|run|verify|recover> ... \
  -- systemd-inhibit --what=shutdown:sleep --mode=block <core-exe> os-upgrade-run
```

- Les données (nonce 32 hex, journal, hop, plan_id, etc.) passent par `--setenv` et sont revalidées par la sous-commande. Aucun argument libre.
- Environnement du processus fils : `PATH` propre, `LC_ALL=C`, `DEBIAN_FRONTEND=noninteractive`, `APT_LISTCHANGES_FRONTEND=none`, `NEEDRESTART_SUSPEND=1`, pas de `LD_LIBRARY_PATH`.
- Options apt : `apt-get -y -q -o Dpkg::Use-Pty=0 -o DPkg::Lock::Timeout=300 -o Acquire::Retries=3 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold`.
- Jamais : `--no-install-recommends`, `--allow-*`, `--force-yes`, `--force-confnew`, `APT::Force-LoopBreak`, `dpkg --force-depends`, `DEBIAN_PRIORITY=critical`.
- Jamais d'`autoremove` ni de `purge ?obsolete` pendant le saut.
- L'unité ne redémarre **jamais** la machine.
- Pas de `TimeoutStopSec=infinity` ni de `SendSIGKILL=no`. Le verrou d'inhibition réduit le risque d'un `reboot` accidentel mais root peut le contourner : je ne le compte pas comme garde-fou.
- Avant `MINIMAL_START`, SIGTERM veut dire « annuler » (restaurer et sortir). Après, il est ignoré tant qu'un `apt`/`dpkg` fils tourne.

### Séquence de la phase `run` (Debian 11 → 12)

1. `STARTED`. Arrêt des timers apt-daily et d'unattended-upgrades (état mémorisé, restauré à toute sortie). Refus si le verrou dpkg est tenu.
2. Instantané dans `/var/lib/casaos/os-upgrade/<run>/pre/` : sélections dpkg, `apt-mark`, copie de `/var/lib/dpkg` et `extended_states`, tar de `/etc`, `passdb.tdb`, ports d'écoute, `sshd -T`, noms de cartes, alternative iptables, conteneurs avec politique de redémarrage, liste des fichiers `*.dpkg-*`.
3. Contrôles rapides revérifiés, par le même code Go que le contrôle de faisabilité.
4. `SRC_OPEN` : copie de `/etc/apt` vers `/var/backups/casaos-os-upgrade-<run>/apt-orig`, écriture de l'arbre de sources généré, mise en pause (renommage en `.casaos-paused`) des sources non-Debian, Docker compris, puis `apt-get update --error-on=any`.
5. Re-simulation. Les décomptes et tailles peuvent dériver. Un nouveau nom de paquet retiré, une nouvelle source en pause ou un paquet protégé touché est interdit : on restaure et on termine par `CHANGED` (réversible).
6. `apt-get --download-only upgrade` pendant que les applications tournent.
7. **`MINIMAL_START`** : point de non-retour. Avant, tout est restaurable. Ensuite on ne va qu'en avant. `apt-get upgrade` (équivalent de `upgrade --without-new-pkgs`), puis re-simulation de `dist-upgrade` avec le garde-fou.
8. Une violation à ce stade arrête l'automate au **point d'arrêt documenté par Debian** (état `stopped_at_waypoint`, § 4.1.3 des notes). Rien n'est annulé, une machine à moitié revertie serait pire.
9. Métapaquet noyau si seule une image versionnée Debian tourne, `--download-only dist-upgrade`, puis `dist-upgrade`.
10. Convergence : `apt-get -s dist-upgrade` jusqu'à « 0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded » (2 passes de rattrapage au plus), `dpkg --audit` vide, `apt-get check`.
11. **Barrière avant redémarrage** (voir §6). Un échec ici ne propose pas le redémarrage.
12. Dépôt apt Docker : inchangé pendant la pause. `REBOOT_PENDING <boot_id>`, drapeau `reboot-required`, timers restaurés, retraits listés. Sortie 0.

### Phase `verify` (lancée par le cœur après le redémarrage)

Elle attend `systemctl is-system-running --wait` (10 minutes maximum) et `docker.service`. Elle vérifie ensuite :
- `VERSION_ID` et le noyau, `dpkg --audit`, `apt-get check`, `apt-get update` propre.
- Les ports d'écoute (sur-ensemble de l'instantané), route par défaut, DNS, NTP, `systemctl --failed` pas pire qu'avant.
- Les sept unités casaos, la passerelle, Samba (`testparm`, ligne `include`, nombre d'utilisateurs `pdbedit` égal à l'instantané), pools mergerfs montés.
- Les paquets de dépendance de l'installeur toujours présents.
- Docker : démon répond, version inchangée, conteneurs revenus selon leur politique (le reste en `NOTRETURNED`, jamais démarrés d'office).

Puis, seulement après un HEAD 200 sur `https://download.docker.com/linux/<debian|ubuntu>/dists/<nouveau>/InRelease`, elle réécrit la source Docker pour la nouvelle release. Ensuite `apt-get update`. Le bouton existant « Mettre à jour Docker » prend le relais, comme action confirmée à part. Elle liste (sans exécuter) `autoremove`, les obsolètes et les `config-files`.

Résultat : `SUCCESS` (aucun constat d'erreur), `SUCCESS_WITH_FINDINGS` (avertissements) ou `FAILED verify`. Le succès se juge sur les post-conditions, jamais sur un code de sortie ni sur `/etc/os-release` (qui change tôt, `base-files` étant déballé en premier).

### Phase `recover`

Idempotente et bornée. Attendre le verrou, puis :
1. `dpkg --configure -a --force-confdef --force-confold`
2. `apt-get -f install`
3. `apt-get update --error-on=any`
4. `apt-get dist-upgrade`
5. Convergence et barrière avant redémarrage.

L'identité du saut vient de la ligne `QUEUED`, jamais de `os-release`. Sur « Could not perform immediate configuration on X » : `apt-get install X` d'abord. `-o APT::Immediate-Configure=0` seulement sur clic explicite du propriétaire.

### Où vit l'état

| Donnée | Emplacement | Écrivain |
|---|---|---|
| Identité du run (`run_id`, hop, version du cœur) | `/var/lib/casaos/os-upgrade/run.json` (0600, écrit une fois) | cœur |
| Faits du run | journal `/var/log/casaos/os-upgrade-<run_id>.log` : ligne 1 `QUEUED <nonce>`, ensuite marqueurs. Le nonce n'est lu qu'à la ligne 1, les champs sont revalidés, le texte libre d'apt/dpkg ne commence jamais une ligne-marqueur | cœur + unité |
| Instantané, plan, constats | `/var/lib/casaos/os-upgrade/<run_id>/` (0700, jamais exécuté) | unité |
| Sauvegarde des sources | `/var/backups/casaos-os-upgrade-<run_id>/apt-orig/` | unité |
| Drapeau « occupé » (lu par AppManagement) | `/var/lib/casaos/os-upgrade/busy` | cœur seul |
| Déduplication des alertes | `os-upgrade.notified` | cœur |

On garde les 3 derniers runs. Le nettoyage ne supprime que les répertoires dont le nom est un UUID et qui contiennent le fichier sentinelle, sans suivre les liens, sans glob.

### États et reprise après redémarrage

Les états : `idle`, `refreshing`, `reboot_first`, `planning`, `planned`, `plan_expired`, `running`, `stopped_at_waypoint`, `awaiting_reboot`, `rebooting`, `reboot_stalled`, `resuming`, `verifying`, `succeeded`, `succeeded_with_findings`, `failed(stage, reason, reversible)`, `interrupted`, `recovering`.

Règles du réducteur :
- `REBOOT_PENDING` est un marqueur d'**attente**, pas un terminal. `REBOOT` n'ajoute qu'un délai : 15 minutes sans changement de `boot_id` donne `reboot_stalled`.
- Un `boot_id` différent de celui enregistré, sans `RESUMED`, donne `resuming`. Même un redémarrage par un autre moyen compte.
- Unité inactive, aucun terminal de phase, journal plus ancien que 30 s et `boot_id` différent du `STARTED` donne `interrupted`. Avec le même `boot_id`, c'est `failed(no_result)`.

`settle()`, au démarrage du cœur puis toutes les 30 s tant qu'un run est ouvert :
1. Retirer les fichiers temporaires orphelins (drop-ins apt et needrestart marqués).
2. `SRC_OPEN` sans `SRC_CLOSED` pendant `refresh` ou `plan` : restaurer `apt-orig`, `dpkg --configure -a`, `apt-get -f install`.
3. `interrupted` après `MINIMAL_START` : lancer `recover`, **2 tentatives au maximum**, puis `failed(half)`.
4. `boot_id` changé : attendre le système, lancer `verify`.
5. `awaiting_reboot` : rappels à +1 h, +24 h, puis chaque jour.
6. Recalculer le drapeau « occupé ».

`busy` vaut vrai de `refreshing` à `verifying` (y compris `reboot_stalled`, `interrupted`, `recovering`, `failed(half)`). Il alimente `maintenanceBusy`, la mise à jour automatique, le bouton Paquets système, le garde Docker, les boutons redémarrer/éteindre, et un contrôle d'~100 lignes dans `RunDueBackups` d'AppManagement. Pas d'unité `oneshot` de reprise au démarrage : le cœur est `Restart=always` et sert de repreneur. La CI vérifie que le cœur, la passerelle et AppManagement reviennent seuls à chaque saut. Si une exécution montre le contraire, on ajoute un `oneshot` (précédent : `casaos-local-storage-first.service`).

### Ubuntu (même cadre, fonction de saut différente)

Mécanisme : `do-release-upgrade -f DistUpgradeViewNonInteractive`, lancé depuis l'unité. De là il n'y a ni `screen` ni `sshd` sur le port 1022, puisque l'ancêtre est systemd.

Étapes propres à Ubuntu :
- Mise en pause des sources non-Ubuntu, `apt-mark manual` sur les dépendances de l'installeur, installation d'`update-manager-core` si absent.
- `LC_ALL=C do-release-upgrade -c` doit sortir 0 **et** afficher la version attendue. Sinon : `not_offered` (neutre : Canonical n'a pas ouvert ou a suspendu le saut) ou `target_mismatch` (une `Prompt=normal` sur 26.04 proposerait 26.10 dès le 2026-10-15). `Prompt=never` est signalé, jamais changé sans confirmation explicite.
- Jamais `-d`, `-p`, `--allow-third-party`.
- Classement par post-conditions : codename atteint + `dpkg --audit` vide + `apt-get check` OK donne « mis à niveau ». Codename inchangé et jeu de paquets inchangé donne « jamais lancé » (on restaure `/etc/apt` depuis **notre** instantané). Sinon, « à moitié » : on va en avant.
- Réparation de Docker : toutes les variantes de source Docker sont supprimées (`docker.list`, `.sources`, `.disabled`, copies `.distUpgrade`), puis `docker.sources` est réécrit pour le nouveau codename seulement après HEAD 200. Si Docker a disparu des paquets : erreur explicite avec réinstallation guidée.

Il n'y a **pas de plan avant confirmation** sur Ubuntu : l'outil de Canonical calcule le sien après le téléchargement. L'écran le dit, et n'affiche que ce que ReCasaOS sait.

## 6) Garde-fous

Légende : **REFUSE** = pas de bouton / 409 avec code ; **WARN** = affiché et acquitté, ne bloque pas ; **AUTO** = corrigé par le saut et journalisé ; **NEUTRE** = pas une erreur. Les planchers disque sont **les miens**, à recalibrer d'après la CI. Le seul chiffre officiel est celui de `/boot` pour trixie.

### Avant (contrôle local, revérifié au démarrage du saut et dans l'unité)

| Code | Type | Condition |
|---|---|---|
| `unsupported_os`, `vendor_os`, `unproven_arch`, `container`, `virt_xen` | REFUSE | voir §3 |
| `latest`, `not_offered`, `hold_until` | NEUTRE | dernière version ; Ubuntu non ouvert ; période de recul sur 24.04 → 26.04 |
| `busy`, `apps` | REFUSE, échec fermé | `maintenanceBusy`, verrou dpkg, unattended-upgrades/apt-daily/PackageKit en cours, opération ou sauvegarde AppManagement en cours **ou AppManagement injoignable** |
| `reboot_first` | REFUSE + action | noyau installé plus récent que `uname -r`, `reboot-required` présent, ou longue durée de fonctionnement avec noyau périmé. Le redémarrage répète le démarrage actuel avant le saut. Plus de 180 jours d'uptime : avertissement. |
| `broken_dpkg` | REFUSE | `dpkg --audit` non vide, fichier tout-chiffres dans `/var/lib/dpkg/updates`, `apt-get check` en échec |
| `held` | REFUSE | tout `hold` **sauf** `docker-ce`, `docker-ce-cli`, `containerd.io`, `docker-*-plugin` (inoffensifs tant que leur source est en pause). Un hold est un acte délibéré : on ne le lève jamais pour l'utilisateur. |
| `sources_*` | REFUSE | `cdrom:`/`file:`, alias de suite vers une autre release, pin ou `Default-Release` nommant une release, `proposed-updates`, format non reconnu, URI Debian ou Docker dont `dists/<cible>/InRelease` ne répond pas 200 |
| `sources_*` | AUTO | alias vers le codename courant réécrit ; lignes backports retirées (`bullseye-backports` renvoie déjà 404 sur `deb.debian.org`) ; `non-free-firmware` ajouté là où `non-free` ou un paquet `firmware-*` / `*-microcode` est présent |
| `sources_third_party` | WARN + acquittement | liste des fichiers mis en pause pour le saut ; ils **restent** en pause après |
| `sources_archive_gap` | AUTO | HEAD sur un vrai `.deb` de l'index de sécurité courant. Observé le 2026-10-09 : l'index de bullseye-security liste des paquets dont les fichiers sont en 404 sur `security.debian.org` et `deb.debian.org`, mais en 200 sur `archive.debian.org`. On teste le `.deb`, jamais l'index. |
| `keyring_old`, `openssh_old` | AUTO puis REFUSE | `debian-archive-keyring` ≥ 2021.1.1+deb11u1 (11→12) ou ≥ 2023.3+deb12u2 (12→13) ; `openssh-server` ≥ 1:9.2p1-2+deb12u7 avant 12→13. L'étape « mettre à jour la version actuelle » les amène ; si insuffisant après, refus. |
| `foreign_arch` | REFUSE | architecture étrangère avec paquets installés (bug Debian 1108193, décalage de libc6) |
| `merged_usr`, `cgroup1` | REFUSE | `/bin` `/sbin` `/lib` doivent être des liens ; `stat -fc %T /sys/fs/cgroup` doit répondre `cgroup2fs` pour 12→13 et 24.04→26.04. Le correctif `usrmerge` n'est pas automatisé. |
| `bootloader`, `root_storage` | REFUSE | pas GRUB ; BIOS : `grub-pc/install_devices` vide ou introuvable (la postinst sort en erreur en mode non interactif) ; UEFI : ESP non montée ou < 16 Mio libres ; racine ZFS ou LUKS ; md dégradé ; SMART FAILED sur le disque racine |
| `fstab` | REFUSE / WARN | source non-`nofail` qui ne se résout pas : REFUSE (la machine tomberait en mode secours au premier redémarrage depuis des mois) ; lignes NFS/CIFS sans `_netdev`/`nofail` et constats de `findmnt --verify` : WARN |
| `network_uplink` | REFUSE | route par défaut sur Wi-Fi sans lien filaire avec porteuse |
| `dkms` | REFUSE / WARN | DKMS sur la carte réseau ou le contrôleur du disque racine, ou tout DKMS avec Secure Boot sans clé MOK : REFUSE. Autres DKMS (ZFS de données, NVIDIA) : WARN. Non prouvable en QEMU. |
| `host_services` | REFUSE | voir §3 |
| `disk_coarse` | REFUSE | ≥ 1,5 Gio libres sur le système de fichiers de `/var/cache/apt/archives`, ≥ 2 Gio sur `/` (ou `/usr`), ≥ 300 Mio sur `/boot`. 12→13 : `/boot` ≥ 768 Mo avec ~300 Mo libres (**seul chiffre officiel**, notes trixie 5.1.5). |
| `ram_power` | REFUSE / WARN | RAM+swap < 1 Gio REFUSE, < 2 Gio WARN (26.04 serveur : 1,5 Go documentés) ; batterie en décharge ou < 50 % REFUSE |
| `docker_state` | REFUSE / WARN | Swarm actif ou Docker d'origine inconnue : REFUSE. Démon arrêté ou mise à jour Docker en attente : WARN. |
| `containers_policy` | WARN | conteneurs en politique `no` ; conteneurs publiant 53/80/443 ou en réseau hôte (la machine est peut-être le DNS ou le web) |

### Dans le plan (bac à sable) et dans l'unité

- **Sandbox** : `apt-get -o Dir::Etc::sourcelist=… -o Dir::Etc::sourceparts=… -o Dir::State::lists=… -o Dir::Cache=… -o Debug::NoLocking=true update --error-on=any`, puis `-s upgrade` et `-s dist-upgrade`. Un `NO_PUBKEY` ou une suite manquante apparaît ici, avant tout changement. Le dossier d'échantillon demande ~100 à 200 Mio de travail sur `/var` : vérifié avant, supprimé après.
- **Garde-fou de retrait** : un seul slice Go de noms protégés, qui rend à la fois le contrôle Go et tout motif shell (un test garantit l'égalité). Contenu : `docker-ce`, `docker-ce-cli`, `containerd.io`, `docker-*-plugin`, `docker.io`, `samba*`, `cifs-utils`, `mergerfs`, `ntfs-3g`, `udevil`, `smartmontools`, `parted`, `net-tools`, `wget`, `curl`, `unzip`, `git`, `openssh-*`, `sudo`, `systemd*`, `dbus`, réseau (`ifupdown`, `network-manager`, `netplan.io`, `dhcpcd`), `ca-certificates`, métapaquets `linux-image`, `grub*`, `shim*`, `initramfs-tools`, `apt`, `dpkg`, `libc6`, tout paquet Essential ou Priority required. Aucun `Remv`/`Purg` de ces noms. Tout `Remv` d'un paquet installé à la main doit avoir été accepté par son nom. Une erreur du solveur est un refus.
- **Plancher disque exact** (estimation, issu de l'ensemble `apt -o APT::Get::Trivial-Only=true`) : archives ≥ max(1,25 × « À télécharger » + 256 Mio, 1,1 × « À télécharger » + 512 Mio) ; `/usr` ≥ 1,5 × « Espace supplémentaire » + 1 Gio ; `/` ≥ 512 Mio si `/usr` est séparé. Un plancher qui refuse une machine ayant de la place est un bug : les planchers vivent dans la table et se retarent d'après les chiffres de la CI.
- **`plan_id`** : somme de contrôle de {hop, hash des sources actuelles, hash de l'arbre généré, sources en pause triées, holds triés, retraits manuels acceptés, verdict du garde-fou, état Docker}. Pas les décomptes `Inst` (ils dérivent pendant la lecture). Un `plan_id` différent au lancement donne `changed` (409). Le plan expire à 24 h ou au changement de version du cœur.

### Confirmation (côté serveur)

`safety_net ∈ {snapshot, console_and_backup, reinstall_ok}`. Avec `console_and_backup`, une sauvegarde AppManagement réussie est exigée pour les applications dont les données sont sur le disque système (sinon `no_backup`). Les acquittements doivent égaler ceux exigés par le plan, plus le nom d'hôte tapé.

### Barrière avant redémarrage (échec : le redémarrage n'est pas proposé)

- Noyau et initrd neufs présents, initrd > 5 Mio et `lsinitramfs` réussit (un seul `update-initramfs -u -k` de rattrapage), `grub.cfg` les nomme (un seul `update-grub`), noyau précédent conservé.
- `sshd -t` et les cinq clés de `sshd -T` (port, listenaddress, permitrootlogin, passwordauthentication, pubkeyacceptedalgorithms) identiques à l'instantané.
- Alternative iptables inchangée ; `systemd-resolved` installé s'il servait de résolveur (11→12, sinon le DNS tombe au redémarrage).
- Source fstab résolue ; démon d'heure présent ; `findmnt --verify` en avertissement.
- **12→13 uniquement** : pour chaque carte physique, comparer son nom courant à `ID_NET_NAME` de `udevadm test-builtin net_setup_link` ; si différent, écrire un `.link` à correspondance MAC puis `update-initramfs -u` (AUTO) ; une carte qui renommerait sans pouvoir être épinglée donne un échec de barrière. Aussi : `linux-sysctl-defaults` installé, `systemd-cryptsetup` si `/etc/crypttab` n'est pas vide, copie des lignes de `/etc/sysctl.conf` dans `/etc/sysctl.d/99-recasaos-migrated.conf`.
- Les nouveaux fichiers `*.dpkg-dist/*.dpkg-old/*.ucf-*` par rapport au point de départ sont listés comme constats.

## 7) Quand ça échoue

Il n'y a **pas de retour arrière de l'OS**, et l'interface le dit avant la confirmation. Ce qui existe : une fenêtre réversible, un chemin vers l'avant, un rapport honnête et une console que le propriétaire doit avoir *avant* de cliquer.

| Moment | Ce qui se passe | Ce que l'utilisateur peut faire |
|---|---|---|
| Refus avant démarrage | Rien n'a changé. | Lire la phrase du code et suivre l'action proposée. |
| Échec de `refresh` ou `plan` (mirroir, coupure, redémarrage du cœur) | La release reste l'ancienne ; seuls des correctifs ordinaires ont été appliqués. `settle()` restaure `apt-orig`, `dpkg --configure -a`, `apt-get -f install`. | Relancer. |
| Échec après confirmation, avant `MINIMAL_START` (sources, plan changé, disque, téléchargement, SIGTERM) | `/etc/apt` restauré, `apt-get update` sur l'ancienne release, `CHANGED`/`FAILED` réversible. | Corriger la cause, relancer. |
| Après `MINIMAL_START` : violation du garde-fou, ou échec du `dist-upgrade` | Arrêt au point d'arrêt documenté (§ 4.1.3 des notes). Machine à moitié mise à niveau : à ne pas laisser longtemps. | Corriger la cause (libérer de l'espace, lever un hold), puis `resume`. Option `immediate_configure_off` : un clic journalisé. |
| Coupure de courant ou reset en pleine mise à niveau | État `interrupted` ; le cœur lance `recover` (2 tentatives), puis `failed(half)` avec le journal. | Relancer `resume` ; si la machine ne démarre pas, voir la ligne « ne démarre plus ». |
| Barrière avant redémarrage rouge (initrd, grub.cfg, sshd, fstab, nom de carte) | `failed(gate)`. La machine tourne sur l'ancien noyau avec la nouvelle userland : normal pendant quelques heures. | Ne pas redémarrer sans console ; le contrôle fautif est nommé. |
| Rappels ignorés pendant des jours | `busy` reste vrai : pas de mise à jour auto, ni sauvegarde, ni mise à jour Docker en état bancal. Les correctifs de sécurité d'unattended-upgrades peuvent revenir. | Redémarrer. |
| La machine ne démarre plus (carte réseau renommée, firmware, DKMS, GRUB, initramfs) | Le tableau de bord n'existe plus. | Console / série / hyperviseur ; mode secours de l'installeur ou live + chroot (notes § 4.1.4) ; `dpkg --configure -a`, `apt-get -f install` ; ancien noyau toujours dans GRUB (on ne purge jamais). Si un snapshot existe : le restaurer, c'est le seul vrai retour arrière. Sinon : réinstaller, relancer l'installeur ReCasaOS, restaurer les sauvegardes d'applications (elles ne contiennent ni `/etc`, ni utilisateurs Samba, ni réseau). |
| Démarre, mais le cœur ne revient pas | Rien ne vérifie ni ne rapporte. | SSH si sshd a survécu. La CI vérifie que ça n'arrive pas ; sinon on ajoute le `oneshot` de reprise. |
| Démarre, `verify` trouve des problèmes | `succeeded_with_findings` ou `failed(verify)` ; chaque constat a son action : unité redémarrée une fois (automatique), « Réparer ReCasaOS » (un clic), paquet de dépendance manquant installé (automatique), conteneurs non revenus listés. | Appliquer l'action ; rien n'est annulé. |

Pools mergerfs manquants après redémarrage : les conteneurs `restart=always` peuvent écrire dans un point de montage vide. `verify` lève une **erreur** (pas un avertissement) et nomme les applications.

**Jamais fait automatiquement :** retour arrière, rétrogradation, `--force-*` sans clic, `autoremove`, démarrage de conteneurs arrêtés, ré-activation d'une source tierce.

## 8) Preuve en CI

**Principe :** rien n'est jugé dans l'invité. Un script de rapport hors invité décide (le concept « INVALID RUN » du travail Docker). Le secret interne est relu après **chaque** redémarrage.

### Mesure préalable (PR 0, aucun code produit)

`os-upgrade-measure.yml`, déclenché à la main, fait la procédure manuelle sur des invités Debian 11 → 12 → 13 et Ubuntu 22.04 → 24.04 → 26.04, et enregistre :
- Durées, « À télécharger » / « Espace supplémentaire », paquets retirés.
- Les rebonds de services pendant `dpkg` (glibc relance ssh et samba en mode non interactif, à vérifier pour 2.31 et 2.36).
- La coupure des conteneurs (conteneur témoin avec compteur).
- Si le bac à sable fonctionne avec apt 2.2 contre l'InRelease de bookworm. Si non, repli : confirmation en une seule phase, les garde-fous restant dans l'unité.
- Si un docker-ce construit pour bullseye survit à la simulation bookworm sans `Remv`.
- Si l'outil `apt-get update --allow-releaseinfo-change-*` se comporte comme documenté sur apt 2.2.4 et 2.6.1.
- Si le `reboot` de root est refusé sous un verrou `systemd-inhibit`.
- Si le cœur, la passerelle et AppManagement reviennent seuls.
- Si la session navigateur survit au redémarrage (le commentaire d'`UpdateModal.vue` et `user_key.go` se contredisent).
- **Test S0 (Ubuntu)** : l'outil non interactif tourne-t-il depuis `systemd-run` sans terminal ? Retire-t-il `docker-ce` comme obsolète une fois sa source en pause (avec ou sans hold, avec ou sans `RemoveObsoletes=no`) ? Les fichiers `release-upgrades.d/*.cfg` sont-ils pris en compte ? Quel est l'état final de `docker.list` ? Quel est le comportement avec Ubuntu Pro ? Le journal garde aussi la version de l'outil de mise à niveau et la date de modification du tarball (il bouge : `noble.tar.gz` 2026-09-07, `resolute.tar.gz` 2026-09-24).

**Critères d'arrêt** : si le cœur ne revient pas après un saut, si les conteneurs sont corrompus sans remède, ou si Docker ne peut pas être protégé sur Ubuntu, alors le bouton n'est pas livrable pour cette famille. Le repli est le rapport de faisabilité plus un runbook généré.

### Jambes de preuve (`os-upgrade-proof.yml`, déclenché à la main, jamais par push)

- **Invariance (lecture seule prouvée).** Hash de `/etc`, `/var/lib/dpkg`, `/var/lib/apt/extended_states` avant et après faisabilité, plan, statut et verify : seuls `/var/lib/casaos/os-upgrade` et `/var/log/casaos` peuvent bouger.
- **Matrice de refus** sur invités salis : hold sur docker-ce et sur libc6, source tierce sans la suite cible, ligne backports, alias `stable`, `Default-Release`, dpkg interrompu, `/boot` minuscule, paquet i386, redémarrage en attente, périphérique fstab absent, mauvais périphérique GRUB (BIOS), verrou dpkg tenu, opération AppManagement en cours. Après chaque refus, le hash de `/etc/apt` et de `dpkg-query` est inchangé.
- **D1 : Debian 11 (bascule d'archive de l'installeur) → 12** avec Docker, ReCasaOS, conteneurs de chaque politique, base de données avec fichier témoin, port publié, partage Samba + utilisateur + connexion SMB réelle, pool mergerfs sur deux fichiers boucle. `plan_id` erroné donne 409 `changed`. On suit les états `rebooting → resuming → verifying → succeeded` à travers le trou du redémarrage.
- **D2 :** même saut avec la ligne de sécurité encore sur `security.debian.org`.
- **D3 : 12 → 13**, à partir d'un overlay qcow2 de la fin de D1, avec renommage de carte forcé et `sysctl.conf`. Plus une jambe indépendante Debian 12 pour les exécutions rapides.
- **Pannes injectées, en dernier, sur leurs propres invités :** `kill -9` de l'unité en plein dpkg, `system_reset` QEMU en plein dpkg, coupure réseau pendant le téléchargement, `/boot` plein, dépôt tiers sans la suite, plan périmé, sshd sur un port non standard.
- **Jambes Ubuntu :** 22.04 → 24.04 et 24.04 → 26.04, chacune gardée par `do-release-upgrade -c`. Si Canonical a suspendu le saut, la jambe rapporte `NOT_OFFERED` (neutre et visible, ni rouge ni faux vert).
- **Rejeu du jeu de paquets du propriétaire** (avec son accord, noms de paquets installés à la main + sources, sans secrets) dans un invité bullseye, avant de toucher sa machine.
- **Hebdomadaire :** D1 contre les miroirs réels. Rouge = alarme (par exemple bullseye retiré de `deb.debian.org`), pas un désactivateur automatique.
- **Assertions hors invité** : version cible, noyau, `dpkg --audit`, `apt-get -s dist-upgrade` à 0/0/0/0, conteneurs selon leur politique, témoin intact, `NOTRETURNED` exactement les conteneurs en politique `no`, Samba/SMB, `fuse.mergerfs`, source Docker réécrite, plan Docker analysable sous apt 3.0, fichiers conservés par `--force-confold` (checksums de `sshd_config` et `smb.conf` inchangés).
- **Mesures :** « À télécharger » et « Espace supplémentaire » par saut, durées par phase, secondes de coupure Docker.

**Délais :** aucune mesure primaire n'existe. Estimation : 25 à 50 minutes par saut sur un invité KVM à 4 vCPU, 45 à 90 minutes par jambe avec installation et vérification. `timeout-minutes` de 120 par saut et 240 pour la chaîne, à fixer à 2 × le p95 mesuré après les premières exécutions. Les runners standard ont 14 Go de SSD : afficher `df` au début et placer le qcow2 sur le plus grand disque (ou utiliser un runner auto-hébergé).

### Ce que la CI ne peut pas prouver

Noms de cartes réseau réels, firmware, Wi-Fi, DKMS/NVIDIA/ZFS, RAID/LVM/LUKS/racine ZFS (images cloud à un seul disque), variantes réelles de BIOS/UEFI et NVRAM, écritures tronquées d'une vraie coupure (un `system_reset` n'est qu'une approximation), disques lents, arm64 et Raspberry Pi, Ubuntu Pro/ESM, dépôts tiers réels, miroirs de pays ou apt-cacher, des années d'historique de paquets sur une vraie machine. Une CI verte ne prouve pas que le saut est sûr sur le matériel du propriétaire.

## 9) Livraison par étapes

Chaque ligne = une PR relisable seule (ou deux). Les tailles sont des estimations en lignes, tests compris (ratio usuel du dépôt : environ 2,5 lignes de test par ligne de code). L'étalon est la mise à jour Docker : environ 12,1 k lignes mesurées (`git diff`), donc « 1 D ».

| PR | Contenu | Taille | Ce que ça apporte | Critère pour passer à la suivante |
|---|---|---|---|---|
| **-1** (prérequis, pas du travail neuf) | Faire atterrir les branches Docker-update (cœur, UI, get-digest) et **lancer `docker-update-proof.yml` sur GitHub** | reste de ce travail | La preuve QEMU est enfin exécutée une fois | Une exécution verte (disque du runner, relecture du secret interne) |
| **0** | `os-upgrade-measure.yml` (CI seule, voir §8) | ~1,5 k | Aucun utilisateur. Réponses chiffrées aux inconnues, planchers, estimations, verdict S0 | Tableau go/no-go écrit (bac à sable OK ou repli ; Docker figé OK ; cœur revient ; S0 Ubuntu) |
| **1** | Identité stricte + table de fin de support datée + `GET /v1/sys/os` + badge + une alerte push par changement de statut | ~2,7 k (1-2 PR) | **Premier palier utile pour le Debian 11 du propriétaire** : « plus de correctifs de sécurité depuis le 2026-08-31 », sans rien modifier | Badge correct sur Debian 11, 12, 13 et sur un Ubuntu ; table périmée = « inconnu », pas faux |
| **2** | Moteur de faisabilité (≈45 contrôles pilotés par table, échec fermé) + vue + « copier le rapport » + jambe d'invariance | ~3,6 k (2 PR) | Le propriétaire lit pourquoi **sa** machine peut ou non sauter (usr fusionné, non-free, dépôts tiers, holds, backports, `/boot`, GRUB, uptime). Aucune écriture | Le rapport sur sa machine est lu ; l'invariance est verte |
| **3** | Réécriture des sources (lignes simples), arbre généré, plan en bac à sable, garde-fou, `plan_id`, vue Plan, **runbook généré par saut** (blocs `set -e` avec lignes d'arrêt, carte de reprise) + jambe QEMU « plan seul » | ~4,3 k (2-3 PR) | Le plan exact de **sa** machine (retraits, sources en pause, chiffres disque), toujours en lecture seule. Pont : il peut faire le saut à la main avec le texte généré, s'il a un snapshot ou une console | Chiffres du plan cohérents avec PR 0 ; sinon passage au repli une-phase |
| **4** | Moteur : sous-commande `os-upgrade-run` (phases refresh, run, verify, recover), marqueurs et réducteur avec `boot_id`, `run.json`, `settle()`, routes, busy persistant, mute d'alertes. Drapeau **off**. PR séparée de ~100 lignes dans AppManagement (`RunDueBackups` saute pendant le saut) | ~4,6 k | Rien de visible (c'est la partie qui doit être juste) | Réducteur testé sur journaux adversariaux ; garde-fous « cassés puis vus échouer » |
| **5** | UI du parcours : mise à jour de la release actuelle, confirmation (carte de reprise, filet, nom d'hôte, retraits), progression sans abandon, bannière de redémarrage, résultat, reprise 401 | ~2,5 k | Le bouton complet, derrière le drapeau | Specs : HTML dans les noms rendu en texte, polling qui survit aux erreurs |
| **6** | `os-upgrade-proof.yml` : jambes D1, D2, refus, pannes, rapport hors invité, rejeu des paquets du propriétaire, hebdomadaire | ~3,3 k (1-2 PR) | Preuve que le flux marche sur images cloud | **Trois exécutions vertes consécutives** de D1+D2 sur miroirs réels, cœur revenu seul, toutes les pannes injectées vertes |
| **7** | Passage de `report` à `beta` pour Debian 11 → 12 + README | ~0,1 k | Le bouton pour les machines qui activent la bascule bêta. **Machine du propriétaire en premier**, avec snapshot ou console | Applications revenues selon leur politique, `systemctl --failed` = référence, puis « Mettre à jour Docker » |
| **8** | `beta` → `stable` | ~0 | Bouton pour tous | 14 jours de recul sur la machine canari **et** quelques machines volontaires (jamais déclaré stable sur une seule machine) |
| **9** | Ubuntu 22.04 → 24.04 (même cadre, outil de Canonical, réparation de Docker) | ~3,8 k, incertitude ×1,5 | Couvre l'échéance 2027-06-01 | **Seulement si S0 prouve que docker-ce, containerd.io, samba, mergerfs, udevil survivent.** Sinon, repli : runbook interactif avec carte de réponses tirée des transcriptions CI, ou abandon d'Ubuntu |
| **10** | Debian 12 → 13 (deb822, nom de cartes + `.link`, `sysctl.conf`, `/boot` 768 Mo, clés apt 3, cgroup2fs, DSA, findings wsdd/Samba, texte périmé de `shares_samba.go:98`) | ~2,3 k | Dernier saut Debian | Jambe de chaîne verte à partir de l'overlay de fin de D1 |
| **11** | Ubuntu 24.04 → 26.04 | ~0,7 k | Dernier saut | Pas avant 2026-11-28, tarball de l'outil inchangé depuis 14 jours, pas de bug de suspension ouvert, jambe verte 3 fois, contrôles Samba 4.23 et mergerfs prouvés |
| **12** (conditionnel) | arm64 UEFI | — | — | Seulement avec un runner arm64 KVM auto-hébergé ou un canari matériel réel |

**Total :** environ 29 k lignes (2,0 à 3,0 D, donc 2,4 en moyenne). Debian seul (PR 0 à 8) : environ 22,6 k, soit 1,9 D. Je ne donne pas de dates : l'étalon n'a lui-même aucune mesure de temps en CI, et le calendrier sera dominé par les itérations de CI (45 à 90 minutes par jambe).

**Pour la machine du propriétaire :** PR 1 le prévient tout de suite, PR 2 lui dit s'il est éligible, PR 3 lui donne le plan exact de sa machine et le runbook. Le bouton arrive à la PR 7, environ 22 k lignes plus loin. Entre-temps, le faire à la main avec le runbook (s'il a un snapshot ou une console) est un choix légitime, sinon il reste sur un OS sans correctifs de sécurité.

## 10) Risques qui restent

1. **Pas de retour arrière de l'OS.** Après `MINIMAL_START`, une panne laisse une machine à moitié mise à niveau, que seul le chemin vers l'avant ou une console répare. Une machine qui ne démarre plus se termine par une réinstallation.
2. **Matériel invisible à la CI :** carte réseau renommée (le saut 12→13 peut isoler une machine sans écran), firmware, DKMS, ordre fstab avec disques USB, RAID. Les barrières sont des heuristiques sur des causes documentées, pas des preuves.
3. **Le premier redémarrage après des mois d'uptime** peut échouer pour des raisons sans rapport avec le saut. `reboot_first` réduit le risque, ne l'élimine pas.
4. **Le plan est une estimation** calculée par l'ancien apt/dpkg ; le plan réel n'existe qu'après la mise à niveau minimale, déjà hors de la fenêtre réversible.
5. **Le cœur est le seul repreneur.** S'il ne démarre pas après le saut, personne ne vérifie ni ne rapporte.
6. **Rebonds de services pendant dpkg :** libc6 relance ssh, samba, cron ; dockerd peut redémarrer. Les bases de données reçoivent le délai d'arrêt par défaut.
7. **Samba et mergerfs :** les changements de sémantique (4.13 → 4.17 → 4.22, mergerfs 2.31 → 2.33 → 2.40) ne sont contrôlés que sur la syntaxe, le montage et le nombre d'utilisateurs. `wsdd` disparaît de Debian 13 (découverte Windows silencieusement perdue : rapporté, pas corrigé). OpenSSH 8.4 → 9.2 → 10 peut exclure d'anciens clients et les clés DSA.
8. **Ubuntu est plus faible par construction :** pas de plan avant confirmation, outil mobile et non reproductible en CI, vue non interactive qui répond oui à tout, survie de Docker non prouvée, Pro/ESM non vérifié. 24.04 → 26.04 n'a que 10 jours de retour d'expérience. Les sauts Ubuntu peuvent ne pas être livrés.
9. **Bullseye peut quitter `deb.debian.org`** à une date inconnue. La jambe hebdomadaire est le seul avertissement précoce ; un refus clair est alors possible à la place d'un saut.
10. **`--force-confold` garde les anciennes versions** des fichiers édités. Ils sont listés, mais personne n'est forcé de les relire.
11. **Un bouton invite les moins prudents à l'opération la plus risquée.** Le nom d'hôte tapé et les acquittements sont de la friction, pas de la protection ; le serveur ne peut pas vérifier qu'un snapshot existe.
12. **Les marqueurs de variantes** (Pi, Armbian, OMV…) sont inférés ; un faux « ok » sur un système de fournisseur est pire qu'un faux refus. Défaut : refuser.
13. **Pas d'interrupteur à distance** : un mauvais saut ne s'arrête que par une nouvelle version, qui est elle-même bloquée tant qu'un run est ouvert.
14. **Friction vis-à-vis de « sans CLI » :** un `hold` ou un usr non fusionné se corrigent en ligne de commande ; les sources tierces restent en pause jusqu'à un futur bouton de réactivation.
15. **Maintenance :** la table de fin de support et la table des sauts se mettent à jour à chaque release ; elle devient « inconnu » 18 mois après sa date plutôt que fausse. Les comptes de machines par OS sont dans PostHog EU (inaccessibles ici), donc l'ordre des priorités est un pari fondé sur les échéances.

## 11) Les décisions à prendre

1. **Jamais de chaîne 11 → 12 → 13 : deux exécutions confirmées séparément.** Recommandation : oui (les notes trixie exigent que bookworm soit terminé avant). Le saut suivant n'est proposé qu'après vérification réussie, avec un conseil de quelques jours de recul.
2. **Redémarrage : manuel seulement en v1.** Recommandation : oui. Une option de redémarrage automatique pourra venir plus tard, jamais dans les premiers paliers (une machine sans écran à qui on retire la main).
3. **Filet de sécurité :** déclaration obligatoire (snapshot / console + sauvegarde / « je réinstallerai ») plus nom d'hôte tapé, avec sauvegarde AppManagement exigée seulement pour les applications dont les données sont sur le disque système. Recommandation : oui.
4. **Holds :** refuser tous sauf la famille Docker, lever un hold en ligne de commande en v1. Recommandation : oui, avec bouton « lever le hold » en v1.1.
5. **Sources tierces :** toutes en pause pendant le saut, seul Docker est réécrit après HEAD 200, les autres restent en pause. Recommandation : oui, bouton de réactivation par source en v1.1.
6. **Docker :** geler le moteur pendant le saut, puis bouton « Mettre à jour Docker » existant comme action confirmée à part. Recommandation : oui. Cela évite deux opérations risquées qui se chevauchent. (Un Docker bumpé d'abord resterait possible tant que la suite bullseye gelée résout encore, mais je ne le recommande pas.)
7. **La machine Debian 11 du propriétaire :** attendre le bouton (PR 7), ou faire le saut à la main avec le runbook après PR 3 ? Recommandation : lire d'abord le rapport PR 2 ; ne rien faire sans snapshot ou console ; faire le saut à la main dès PR 3 si elle en a, sinon réinstaller directement en Debian 13 et restaurer les sauvegardes d'applications.
8. **Ubuntu :** ordre 22.04 → 24.04 d'abord (échéance 2027-06-01) après le verdict S0 ; 24.04 → 26.04 pas avant 2026-11-28 ; Ubuntu abandonné si S0 montre que Docker ne peut pas être protégé ; 20.04 hors périmètre. Recommandation : oui à tout.
9. **Architectures :** amd64 seulement, Raspberry Pi OS / Armbian / OMV / Proxmox refusés avec guide de réinstallation fondé sur la sauvegarde existante. Recommandation : oui.
10. **Télémétrie :** ajouter un événement `os_upgrade_result` (saut, code d'erreur, durée), respectant l'opt-out, à la PR 7. Aujourd'hui un saut réussi est visible (le champ `os` change pour le même `distinct_id`) et un échec est invisible. Recommandation : oui.
11. **Interrupteur à distance :** non (nouvel appel sortant). Un mauvais saut se corrige par une nouvelle version. Recommandation : non.
12. **CI :** runners hébergés (gratuits en dépôt public, 14 Go de SSD) avec contrôle du disque, ou runner auto-hébergé ? Recommandation : essayer l'hébergé, passer à l'auto-hébergé si le disque ne suffit pas ou si arm64 devient un objectif.
13. **Critère de `stable` :** 14 jours de recul sur le canari **et** quelques machines volontaires. Recommandation : oui.
14. **Dates affichées :** au mois près quand les sources divergent (Debian 12, Ubuntu 22.04). Recommandation : oui. Alerte push de fin de support activée tout de suite pour Debian 11 (hors LTS depuis le 2026-08-31).

## 12) Les faits datés sur lesquels repose le plan

### Dates de support

| Fait | Source |
|---|---|
| **Debian 11 bullseye** : support régulier terminé 2024-08-14 ; **LTS terminé 2026-08-31** ; ELTS payant (Freexian) 2026-09-01 → 2031-06-30. 11.11 = 2024-08-31. | https://www.debian.org/News/2026/20260831 ; https://wiki.debian.org/LTS/Extended ; https://wiki.debian.org/DebianReleases |
| **Debian 12 bookworm** : 12.15 publié 2026-07-11 ; support régulier terminé 2026-07-12 ; LTS jusqu'au 2028-06-30 (i386, amd64, armhf, arm64, ppc64el). **Divergence :** le wiki LTS dit « LTS dès 2026-06-11 », la page des releases et l'annonce disent juillet. Sans effet opérationnel. | https://www.debian.org/News/2026/20260712 ; https://www.debian.org/releases/ ; https://wiki.debian.org/LTS |
| **Debian 13 trixie** : 13.7 publié 2026-09-12 ; fin 2028-08-09 ; LTS 2030-06-30. | https://www.debian.org/releases/ |
| **Ubuntu 20.04** : standard terminé 2025-05-29 ; ESM jusqu'au 2030-04-23. | https://git.launchpad.net/ubuntu/+source/distro-info-data/plain/ubuntu.csv?h=ubuntu/devel ; https://ubuntu.com/about/release-cycle |
| **Ubuntu 22.04** : standard jusqu'au **2027-06-01** (distro-info ; 235 jours depuis aujourd'hui), « mai 2027 » sur la page release-cycle ; ESM 2032-04-21. | mêmes sources |
| **Ubuntu 24.04** : standard 2029-05-31 ; ESM 2034-04-25. **Ubuntu 26.04** : standard 2031-05-29 ; ESM 2036-04-23. 26.10 prévu 2026-10-15 (non LTS). | mêmes sources |
| **Règle Ubuntu :** passage LTS → LTS suivant seulement, proposé après le premier point release (.1). **26.04.1** ISO le 2026-08-27 ; sauts 24.04 → 26.04 ouverts le **2026-09-29 à 12:29 UTC** (+33 jours). L'état « offert » se lit sur la machine (`do-release-upgrade -c`), jamais codé en dur. Précédent : les sauts vers 24.04.1 ont été suspendus vers 2024-09-04 (bug 2078720) puis rétablis vers 2024-09-09. | https://ubuntu.com/server/docs/how-to/software/upgrade-your-release/ ; https://lists.ubuntu.com/archives/ubuntu-announce/2026-September/000328.html ; https://changelogs.ubuntu.com/meta-release-lts ; https://bugs.launchpad.net/ubuntu/+source/ubuntu-release-upgrader/+bug/2078720 |

### Dépôts, observés le 2026-10-09 (par les chercheurs, sur les miroirs)

- `deb.debian.org/debian/dists/bullseye` et `bullseye-updates` sont encore servis (Release daté 2025-08-09, alias `oldoldstable`). `bullseye-backports` et `bullseye-proposed-updates` ne le sont plus (seulement sur `archive.debian.org`).
- Les `.deb` de `bullseye-security` sont en **404** sur `security.debian.org` et `deb.debian.org/debian-security` (vérifié pour ntfs-3g, openssh-server, libssl1.1) ; **200** sur `archive.debian.org/debian-security`. Aucune suite `bookworm` ni `trixie` sur `archive.debian.org`.
- Dépôt Docker pour Debian : suites `bullseye` (InRelease daté 2026-09-03, **figé à docker-ce 5:29.8.0**), `bookworm` et `trixie` (2026-10-08, 5:29.9.0). Pour Ubuntu : `focal` figé depuis 2025-04-18 ; `jammy`, `noble`, `resolute` à jour. https://download.docker.com/linux/debian/dists/ ; https://download.docker.com/linux/ubuntu/dists/
- Noms de paquets de dépendance de l'installeur : tous présents en bookworm et trixie, sauf `wsdd` qui n'est pas dans trixie (`wsdd2`, autre programme, y est). Source secondaire/à re-vérifier : https://tracker.debian.org/pkg/wsdd

### Notes de version (primaires)

- https://www.debian.org/releases/bookworm/amd64/release-notes/ch-upgrading.en.html et `ch-information.en.html`
- https://www.debian.org/releases/trixie/release-notes/upgrading.en.html, `issues.en.html`, `whats-new.en.html`
- Points repris : `/boot` ≥ 768 Mo avec ~300 Mo libres (trixie 5.1.5) ; noms de cartes (5.1.17) ; saut de release non supporté (5.1.25) ; `/etc/sysctl.conf` non lu (5.1.15) ; `/tmp` en tmpfs (5.1.6) ; `non-free-firmware` (bookworm 5.1.1) ; usr fusionné requis (bookworm 5.1.14) ; `systemd-resolved` non installé automatiquement (bookworm 5.2.3).
- Ubuntu : https://documentation.ubuntu.com/release-notes/24.04/ et `/26.04/`, https://manpages.ubuntu.com/manpages/noble/man8/do-release-upgrade.8.html, et le code de `ubuntu-release-upgrader` sur git.launchpad.net (branches `ubuntu/noble` et `ubuntu/resolute`).

### Sources secondaires ou anciennes (à ne pas citer sans vérification)

- Retard de 26.04.1 dû à des correctifs rust-coreutils : https://linuxiac.com/ubuntu-26-04-1-lts-released-24-04-lts-upgrades-coming-in-a-few-weeks/ (secondaire).
- Date d'ouverture 24.04 → 26.04 vue aussi sur https://www.omgubuntu.co.uk/2026/09/ubuntu-2604-upgrades-live-2404 (secondaire, mais recoupée par l'annonce primaire).
- Retrait de `bullseye` du miroir principal : patch Isar du 2026-09-06 (https://groups.google.com/g/isar-users/c/FCVRYvvPLrw), secondaire ; aucune date officielle.
- Durées d'une montée Ubuntu (35 à 55 minutes) : https://www.debugpoint.com/upgrade-ubuntu-24-04-from-22-04/ et https://computingforgeeks.com/migrate-ubuntu-2204-to-2604/ (secondaires).
- Marqueurs de variantes (Pi, Armbian, OMV, Proxmox, DietPi) : inférés, non vérifiés sur images réelles.
- Politique SHA-1 de apt 3.0 (Sequoia, coupures 2026-02-01) : tirée de `NEWS` et `default-sequoia.config` sur sources.debian.org (primaire) mais non observée sur un dépôt réel.
- Fin de support d'Ubuntu 22.04 : la date « avril 2027 » vue ailleurs n'a pas de source primaire, ne pas l'utiliser.

### Reste à vérifier sur une vraie machine (PR 0 et machine du propriétaire)

- Sur **sa** machine : usr fusionné, composant `non-free`, dépôts tiers, ligne backports, hold sur docker-ce, taille de `/boot`, BIOS ou UEFI, uptime, version de docker-ce réellement installée (la valeur 28.0.4 vue dans les dossiers vient des fixtures de test, pas de la machine).
- Le bac à sable `Dir::Etc` / `Dir::State::lists` d'apt 2.2 contre l'InRelease de bookworm ; les options `--allow-releaseinfo-change-{suite,version,label}` et `--error-on=any` sur apt 2.2.4 et 2.6.1.
- Un docker-ce construit pour bullseye survit-il à la simulation bookworm sans `Remv` ?
- `systemd-inhibit` refuse-t-il vraiment le `reboot` de root ? L'unité transitoire survit-elle aux mises à jour de systemd/dbus/libc6 ? Les services redémarrés par libc6 pour 2.31 et 2.36 ?
- Le cœur, la passerelle et AppManagement reviennent-ils seuls ? La session navigateur survit-elle ?
- Samba 4.17/4.22 avec le `smb.conf` généré par ReCasaOS, mergerfs 2.31 → 2.40 sur un vrai `/DATA` multi-disques, comportement de `nftables.service` avec les règles Docker au démarrage.
- Taille de `/tmp` (tmpfs) face à l'installeur sur une machine à 1 Go après 12→13.
- Ubuntu (test S0) : Docker est-il retiré comme obsolète ? Les `release-upgrades.d/*.cfg` et `RemoveObsoletes=no` sont-ils respectés ? L'état final de `docker.list` ? Ubuntu Pro/ESM ? Les invites réelles ?
- Formats exacts de sortie d'apt 3.0 et 3.1 face à l'analyseur de plan.