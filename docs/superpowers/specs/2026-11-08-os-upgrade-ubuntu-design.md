# Upgrade de l'OS : Ubuntu 24.04 → 26.04 et 22.04 → 24.04

Date : 2026-11-08. Suite des sauts Debian 11 → 12 et 12 → 13 (#103, #104, CasaOS-UI#144). Le propriétaire a demandé « les plans de mises à jour Ubuntu comme on a fait sur Debian », sans autre cadrage : les choix ci-dessous sont les miens, marqués « (mon choix) ».

## Pourquoi

- La télémétrie du 2026-11-08 compte environ 43 boxes en Ubuntu 24.04 et 15 en 22.04 (propriétaire, capture PostHog). La readiness leur montre déjà leur saut, mais `osplan` répond `hop_not_planned` (« Ubuntu is a later rung », `rewrite.go:50`).
- 22.04 sort du support standard en mai 2027 (ubuntu.com/about/release-cycle). Le saut 24.04 → 26.04 est ouvert par Canonical depuis le 2026-09-29 (annonce Discourse « Upgrades from Ubuntu 24.04 LTS to Ubuntu 26.04.1 LTS now enabled »). `hold_until` le retient chez nous jusqu'au 2026-11-28.

## Buts

- Les deux sauts LTS qu'`ossupport` connaît déjà : 24.04 (noble) → 26.04 (resolute), et 22.04 (jammy) → 24.04 (noble). Ils utilisent le même moteur, la même unité transitoire, les mêmes blocs, le même interrupteur beta et la même page que Debian.
- Un plan possible sur les sources qu'ont réellement les boxes Ubuntu : `sources.list` en une ligne (22.04), `ubuntu.sources` en deb822 (24.04, et les images cloud récentes), et `docker.list` ou `docker.sources`.
- Les contrôles que le releaser de Canonical (`ubuntu-release-upgrader`, `DistUpgradeQuirks.py`) fait pour ces deux sauts sont repris un par un. Chaque contrôle est soit porté, soit couvert par un contrôle existant, soit écarté avec sa raison écrite.
- Le saut n'est proposé que si Canonical l'offre (le fichier `meta-release-lts`), lu au moment du plan.

## Non-buts

- Les releases non LTS (la télémétrie montre des 26.10), les sauts 20.04 → 22.04 et 26.04 → 28.04.
- Le saut de deux LTS d'un coup : Canonical ne le prend pas en charge.
- Une box attachée à Ubuntu Pro, avec des sources ESM actives : elle est refusée dans cette version (voir 4).
- La conversion de `sources.list` en `ubuntu.sources` que fait le releaser de Canonical vers 24.04 : le saut garde le format de chaque fichier, comme pour Debian.
- Les architectures autres qu'amd64 : la porte `unproven_arch` ne change pas.

## Anti-buts (échec même si ça marche)

- **Un seul octet changé dans ce que produisent les sauts Debian.** Les six golden `runbook-d11-12.*` et `runbook-d12-13.*` passent sans retouche, dans chaque PR.
- Une ligne de l'archive Ubuntu réécrite par un saut Debian, ou l'inverse. Chaque saut ne réécrit que l'archive de sa distribution.
- Un fichier de sources du propriétaire changé en place, ou non rendu octet pour octet en fin de run. Cette règle s'étend sans exception à Ubuntu.

## La décision de fond : le moteur apt, pas `do-release-upgrade` (mon choix)

Canonical recommande `do-release-upgrade` pour Server (« We recommend upgrading the system using the do-release-upgrade command », Ubuntu Server docs). Je garde pourtant le moteur des sauts Debian, et je reprends à la main les contrôles du releaser :

- **Le plan disparaîtrait.** Le moteur montre avant le saut ce qu'apt retirerait (simulation dans un bac à sable), garde les paquets protégés, fait une répétition qui se défait et pose un point de non-retour. `do-release-upgrade` fait tout d'un bloc, et on ne voit son résultat qu'après.
- **Le mode non interactif n'est pas documenté.** `-f DistUpgradeViewNonInteractive` n'est pas dans le manuel. Des bugs le montrent encore demander une réponse (LP #1796193, #1797636). Il refuse aussi les fichiers de configuration modifiés sans dire lesquels (il répond « n »).
- **Les sources ne seraient pas rendues.** Le releaser désactive ou déplace les sources tierces (`.disabled`, `Enabled: no`) et réécrit `sources.list` en `ubuntu.sources`. La règle « octet pour octet » et le retour de la source Docker du bloc 8 ne tiendraient plus.
- **Les suspensions de Canonical restent respectées** : on lit le même `meta-release-lts` que lui (voir 3).

Ce que ce choix coûte : ces sauts ne passent pas par le chemin que Canonical teste. La réponse est la liste des quirks reprise un par un (section 5) et une jambe de preuve par saut avant toute promotion (section 8). Si le propriétaire préfère `do-release-upgrade`, seules les sections 2, 3 et 5 survivent. Il faut le dire avant la PR 2.

## Ce qui existe déjà (relevé sur `inkly/main`, 729c402)

- `osreadiness` connaît `HopUbuntu2204to2404` et `HopUbuntu2404to2604`, ainsi que `ubuntuHops`. Les contrôles des sauts Ubuntu existent déjà : `hold_until` (24.04 → 26.04, jusqu'au 2026-11-28), `not_offered` (Info : « The plan step asks for it »), `cgroup1` (24.04 → 26.04). `ram` a une phrase pour Ubuntu 26.04, et `sources_pin` connaît les pins d'Ubuntu Pro.
- `officialHost` et `distroOrigin` reconnaissent déjà `*.ubuntu.com` et `Origin: Ubuntu`. `sources_third_party` dit déjà « Debian's or Ubuntu's ».
- `ossupport` a les lignes 22.04 → 24.04 et 24.04 → 26.04 (`StateReport`).
- `osplan` ne parle que de Debian, à un petit nombre d'endroits :
  - `FromName()` et `ToName()` écrivent « Debian » + le numéro (`rewrite.go:47-48`). Le texte des blocs passe déjà par `@from@` et `@to@` (`runbook.go:864`).
  - Les phrases qui disent « Debian » en dur : `runbook.go:282` (« Sources that are not Debian's »), `:1135`, et la clé STOP `"this machine is not Debian"` (`auto_class.go:103`).
  - `provenance()` (`runbook.go:301-312`) dit que « les notes de version de @to@ prescrivent les mêmes commandes ». C'est vrai de Debian et faux d'Ubuntu.
  - L'adresse que le bloc 8 demande pour le retour de la source Docker est en dur : `https://download.docker.com/linux/debian/dists/<to>/InRelease` (`runbook.go:1458`). Le texte de l'unité a la même chose (`auto.go:327-337`).
  - `rewriteDistro` connaît les suffixes de suite de Debian (`-updates`, `-security`, `-backports`, `-backports-sloppy`, `/updates`, `-proposed-updates`), `archive.debian.org` et l'archive de sécurité de bullseye (gardée par `h.From == "bullseye"`). Il ajoute `non-free-firmware` derrière `h.Firmware`.
- Les dossiers de sauvegarde sont déjà rangés par saut (fichier `hop`, PR 1 de trixie) : un dossier Debian n'arrête pas un saut Ubuntu, et l'inverse non plus.

## La forme du changement

### 1. La distribution vient du saut

`HopSpec` gagne `Distro string` (« Debian », « Ubuntu »). `FromName()` donne « Debian 12 » ou « Ubuntu 24.04 ». Les trois phrases en dur, la clé STOP (une clé par distribution, même classe `ClassIdentity`) et l'adresse Docker (`linux/debian` ou `linux/ubuntu`, par `strings.ToLower(h.Distro)`) viennent du saut. Pour Ubuntu, `provenance()` dit que Canonical ne prescrit pas ces commandes et que son outil est `do-release-upgrade`, en une ligne.

Critère : les six golden Debian sont identiques octet pour octet.

### 2. Les sources d'Ubuntu

`rewriteDistro` et la version deb822 lisent les suites d'Ubuntu quand `Distro == "Ubuntu"` :

- Suites : `jammy`, `jammy-updates` et `jammy-security` passent à la cible. `jammy-backports` est retiré (commenté, ou `Enabled: no` pour une strophe qui n'a que lui), comme les backports de Debian. `-proposed` est déjà refusé par la readiness (`sources_proposed`), et le plan le refuse aussi.
- Hôtes : seuls `*.ubuntu.com` et les miroirs dont le `Release` dit `Origin: Ubuntu` comptent pour l'archive. Une ligne de Debian dans un saut Ubuntu est refusée (`sources_format`, raison `other_distro`), et l'inverse aussi. Les composants (`main restricted universe multiverse`) ne changent pas. Il n'y a ni firmware ni hôte d'archive à déplacer.
- `esm.ubuntu.com` (Ubuntu Pro) : refus `ubuntu_pro` (voir 4), et non pas une source tierce mise de côté. Ses fichiers sont réécrits par le client `pro` au moment du saut, ce que le moteur ne sait pas faire.
- Les fichiers réels servent de fixtures : le `sources.list` de l'image cloud 22.04, `ubuntu.sources` et le `sources.list` commenté de l'image 24.04, ainsi que `docker.list` et `docker.sources` selon la doc Docker pour Ubuntu.

`Deb822` est vrai pour les deux sauts (mon choix : une 22.04 peut avoir `docker.sources`, et le lecteur deb822 est maintenant en service).

### 3. Canonical offre le saut

Au moment du plan, le cœur lit `https://changelogs.ubuntu.com/meta-release-lts` (l'URI `URI_LTS` de `/etc/update-manager/meta-release`). Le saut est offert si la strophe de la cible existe et dit `Supported: 1`. Sinon, c'est un refus `not_offered` avec la date de la lecture. Si le fichier ne répond pas, c'est un refus de classe réseau, comme un `apt-get update` qui ne passe pas. Le contrôle `not_offered` de la readiness reste Info (elle ne demande rien au réseau), mais sa phrase dit maintenant que le plan le vérifie.

C'est par ce fichier que Canonical a suspendu le saut 22.04 → 24.04 entre le 2 et le 9 septembre 2024 (bug LP #2078895). Le lire nous fait suivre ses suspensions sans rien coder de plus. `hold_until` garde ses 60 jours en plus.

### 4. Les nouveaux contrôles de la readiness

**Corrigé le 2026-11-08** après lecture de `DistUpgradeQuirks.py`, sur la branche `ubuntu/resolute-updates` d'`ubuntu-release-upgrader` : c'est l'outil qui fait le saut vers 26.04. La première version de cette section citait `_test_and_fail_on_unsupported_boot_partition` et `_force_dbus_broker_install`, deux fonctions que ce fichier n'a pas.

Ce que le releaser fait vers resolute, et ce qu'on en fait :

| Quirk | Ici |
|---|---|
| `_test_and_fail_without_cgroupv2` | `cgroup1` (existant) |
| `_test_and_fail_on_tpm_fde` | `root_luks` (existant) |
| `_test_and_fail_mysql_server_using_mysql_native_password`, `_test_and_fail_rabbitmq_server_installed_and_not_upgradable` | `host_services` refuse déjà `mysql-server` et `rabbitmq-server` installés sur l'hôte, pour tous les sauts |
| `_fix_lintian_resolver_deadlock` (depuis noble, quand lintian est installé) | apt garde lintian en arrière, et le plan bloque sur un paquet gardé : l'échec est fermé |
| `_maybe_remove_gpg_wks_server` (gpg-wks-server sans MTA tire postfix) | le plan montre postfix parmi les paquets nouveaux |
| `_install_linux_metapackage` | la garde du noyau des blocs 7 et 8 (`linux-image-[0-9]*`) |
| `_force_dracut_install`, Pi, s390x, riscv | hors amd64 |
| `_install_linux_sysctl_defaults` | ne vaut que depuis oracular |
| snaps (`_checkStoreConnectivity`, `_replaceDebsAndSnaps`) | apt ne touche pas aux snaps |

Contrôles ajoutés :
- `ubuntu_pro` : refusé par le plan, au moment de lire les sources (PR 2).
- `ssh_dsa_hostkey` est étendu au saut 24.04 → 26.04. L'OpenSSH de 26.04 est la 10.2p1 (lu dans l'image), qui ne charge plus les clés DSA.
- Le saut 22.04 → 24.04 aura ses propres contrôles quand on lira les quirks de noble : `apt_key_weak`, `ssh_socket`.

### 5. Ce qui tourne après le saut : rust-coreutils, sudo-rs, apt 3.1

En 26.04, les outils de base sont ceux de `rust-coreutils` (« not fully compatible yet » d'après les notes ; `cp`, `mv` et `rm` restent ceux de GNU). Les blocs 7 et 8 et la refresh tournent après le saut, et leur texte utilise `sort`, `cut`, `stat`, `sha256sum`, `date` et `awk`. Un test exécute le texte de ces blocs sous dash dans l'image `ubuntu:26.04`, avec ses vrais coreutils, comme `shellBox` le fait aujourd'hui avec des commandes de remplacement (mon choix : c'est la seule preuve qu'on peut avoir sans machine). sudo-rs ne nous touche pas, puisque les blocs tournent en root et sans sudo. La sortie d'apt 3.1 est relue comme celle d'apt 3.0 l'a été (#78).

### 6. Les deux lignes des tables

- `planHops` :
  - `{ID: HopUbuntu2404to2604, Distro: "Ubuntu", From: "noble", To: "resolute", FromVersion: "24.04", ToVersion: "26.04", Later: []string{"<nom de code de 26.10>"}, Deb822: true, Install: []string{"dbus-broker"}}` ;
  - `{ID: HopUbuntu2204to2404, Distro: "Ubuntu", From: "jammy", To: "noble", FromVersion: "22.04", ToVersion: "24.04", Later: []string{"resolute"}, Deb822: true}`.
  - `Later` doit nommer la release qui suit : 26.10 a un nom de code à relever dans distro-info-data (je ne le connais pas avec certitude).
- `runbookHops` : `debianBlocks`, renommé `aptBlocks`. Les commandes apt ne changent pas (`update`, `upgrade --without-new-pkgs`, `full-upgrade`). Le releaser de Canonical fait la même chose avec python-apt.
- Les golden `runbook-u24-26.*` et `runbook-u22-24.*`.

### 7. Interface

La page ne connaît pas les releases, sauf dans les phrases. Elle a besoin des nouvelles clés en anglais et en français (titres des blocs, lignes STOP, `not_offered` du plan, les contrôles de la section 4) et d'une fixture de réponses du cœur pour un saut Ubuntu dans les tests d'`OsUpgradePlan`. La phrase « why no plan » de CasaOS-UI#144 ne doit plus dire qu'Ubuntu n'a pas de plan.

### 8. Preuve et doc

- **Fait le 2026-11-08 :** toute la suite `osplan` et `osrun` passe dans l'image `ubuntu:26.04`, avec ses rust-coreutils 0.10.0 et dash. Cela couvre les blocs, le driver et le script de restauration, exécutés en shell.
- Une jambe `os-plan-proof` par saut, sur l'image cloud Ubuntu (22.04 et 24.04, avec `SHA256SUMS`). Elle donne la vraie sortie d'`apt-get -s` d'Ubuntu (origines `Ubuntu:24.04/noble-updates`) que `parse.go` doit lire. **Elle vient avant la PR des tables** : sans elle, le parseur n'a jamais lu une ligne d'Ubuntu.
- Une jambe `os-upgrade-proof` par saut, qui garde les sources de l'image telles quelles (`ubuntu.sources` en 24.04).
- `docs/os-upgrade-beta.md` gagne une section Ubuntu, avec tout en [N] tant que les jambes n'ont pas tourné.

## Découpage en PR

1. La distribution vient du saut (section 1). **Aucun changement de comportement** : les golden Debian sont identiques.
2. Les sources d'Ubuntu, derrière un saut de test (section 2), avec les fixtures des images cloud.
3. Canonical offre le saut : la lecture de `meta-release-lts` au moment du plan (section 3).
4. Fixtures du parseur : la vraie sortie d'apt dans les images `ubuntu:22.04` et `ubuntu:24.04`, sources réécrites (fait le 2026-11-08). Une jambe `os-plan-proof` passe par le plan du cœur, qui répond `hop_not_planned` tant que la PR 5 n'a pas ajouté la ligne du saut : les jambes QEMU viennent donc après la PR 5, sur une pré-release.
5. Le saut 24.04 → 26.04 : sa ligne dans les tables, ses contrôles, `Install`, le test sous `ubuntu:26.04` et ses golden. Il passe en premier parce qu'il concerne environ trois fois plus de boxes.
6. Le saut 22.04 → 24.04 : sa ligne, ses contrôles et ses golden.
7. Interface : les clés et la fixture.
8. Jambes `os-upgrade-proof`, puis doc beta.

Chaque PR passe `go test ./...` sous Linux. L'interface tourne avec Node 22 et pnpm 9.

## Laissé au constructeur

- Le texte exact des phrases, dans le registre des phrases actuelles.
- Le corps des quirks marqués « à lire » : on lit leur code avant la PR de leur saut, et la décision est écrite dans la PR.

## Risques

- **Trouvé le 2026-11-08, dans l'image `ubuntu:24.04` :** de 24.04 à 26.04, `apt-get -s dist-upgrade` finit en code 100 sur `E: Conf Broken coreutils-from-uutils:amd64`. C'est le passage de coreutils à rust-coreutils, et il se produit avant comme après le saut minimal. Le vrai `dist-upgrade` passe pourtant : code 0, la machine en 26.04.1, `dpkg --audit` propre. Le plan (`sandbox_solver_failed`) et les simulations des blocs 3, 5 et 6 du runbook (STOP) refuseraient donc chaque box. La PR 5 doit tolérer cette erreur, et elle seule, pour ce saut : dans `ParseSolver` comme dans le texte shell. Les golden Debian ne changent pas.

- Ce n'est pas le chemin que Canonical teste. Une transition qu'apt seul résout mal (comme t64 l'a été pour Noble) ne se voit que dans la jambe de preuve, ou chez un propriétaire. C'est pour cela que la promotion est conditionnée aux jambes.
- La lecture du réseau au moment du plan ajoute une panne possible. Elle échoue fermée.
- Les noms des cartes réseau, netplan et systemd-resolved restent non contrôlés, comme pour Debian.
