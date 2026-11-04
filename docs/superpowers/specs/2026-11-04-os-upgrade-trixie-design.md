# Upgrade de l'OS : Debian 12 → 13 (trixie)

Date : 2026-11-04. Suite de l'upgrade Debian 11 → 12 (#20 à #33, beta derrière `/etc/casaos/os-upgrade.beta`).

## Pourquoi

Une box sur bookworm n'a plus de support de sécurité régulier depuis le 2026-07-11 : la LTS la couvre jusqu'au 2028-06-30, et seulement sur i386, amd64, armhf, arm64 et ppc64el. Trixie en est à la 13.7 (12 septembre 2026). Les boxes passées de 11 à 12 avec la beta sont maintenant sur bookworm, et n'ont plus d'étape suivante. Le propriétaire a demandé ce saut sans cadrage supplémentaire : les choix ci-dessous sont les miens, marqués « (mon choix) ».

## Buts

- Le même saut que 11 → 12 : même moteur, même unité transitoire, mêmes blocs, même interrupteur beta, mêmes appels de l'API et même page.
- Un plan possible sur les sources qu'ont réellement les boxes en bookworm, `.list` en une ligne **et** `.sources` en deb822 (les images cloud de Debian 12, et la doc d'installation actuelle de Docker, qui écrit `docker.sources`).
- Les pièges que les notes de version de trixie nomment, contrôlés avant le saut quand ils peuvent l'être.

## Non-buts

- Ubuntu, Debian 13 → 14.
- Le saut 11 → 13 d'un coup : Debian ne le prend pas en charge. Une box en 11 fait 11 → 12, puis 12 → 13.
- La réparation automatique : elle reste à zéro essai, comme pour 11 → 12.
- La conversion des sources en deb822 (`apt modernize-sources`) : les notes la proposent sans l'exiger. Le saut garde le format de chaque fichier.

## Anti-buts (échec même si ça marche)

- **Un seul octet changé dans ce que produit le saut 11 → 12.** Les golden `runbook-d11-12.*.golden` et les tests existants doivent passer sans retouche. Chaque PR le prouve.
- Un fichier de sources du propriétaire changé en place, ou non rendu octet pour octet en fin de run : c'est la règle actuelle, et elle s'étend aux `.sources`.
- Une ligne ou un champ de deb822 qu'on ne comprend pas, réécrit au jugé : on refuse avec le fichier et la strophe, comme pour les `.list`.

## Ce qui existe déjà (relevé sur `inkly/main`, 8343114)

- `osreadiness` connaît `HopDebian12to13` et a déjà ses contrôles : cgroup v1 (`cgroup1`), OpenSSH ≥ `1:9.2p1-2+deb12u7` (`openssh_old`), `/boot` de 768 Mo, keyring ≥ `2023.3+deb12u2`. Leurs phrases sont traduites dans l'interface.
- `ossupport` donne déjà trixie comme release suivante de bookworm.
- `osplan` ne connaît que 11 → 12 : une ligne dans `planHops` (`rewrite.go`) et une dans `runbookHops` (`runbook.go`). `RewriteSources` refuse tout `.sources` (`sources_format`, raison `deb822`).
- Les blocs 0 à 8 ne dépendent de la release que par `c.hop.From` et `c.hop.To`, sauf **une quarantaine de phrases** qui disent « Debian 11 » et « Debian 12 » en dur (`runbook.go`, `auto_refresh.go`, `auto_class.go`).
- Un garde-fou : `TestASecondHopNeedsTheBackupFoldersScopedByHop` casse dès qu'une deuxième ligne entre dans `runbookHops`, tant que `backupFoldersScopedByHop` est faux. `head()` et `otherPlans()` lisent tous les dossiers `/var/backups/casaos-os-upgrade-*`.
- Ce qui ne dépend que de bullseye l'est déjà explicitement : la réécriture vers `archive.debian.org` est gardée par `h.From == "bullseye"`.

## La forme du changement

### 1. Le nom des releases vient du saut, et non du texte

`HopSpec` gagne `FromName` et `ToName` (« Debian 12 », « Debian 13 »). Toutes les phrases des blocs, du runbook à la main, de la refresh et des classes d'arrêt les utilisent. `auto_class.go` classe les lignes STOP par leur préfixe : ses clés qui nomment une release sont produites pour chaque saut de la table, et non plus écrites à la main.

Critère : les golden 11 → 12 sont identiques octet pour octet. L'interface traduit les titres des blocs par leur texte anglais exact : les titres du saut 12 → 13 sont de nouvelles clés, ceux de 11 → 12 ne bougent pas.

### 2. Les dossiers de sauvegarde sont rangés par saut

Chaque dossier de plan reçoit un fichier `hop` qui contient l'identifiant du saut (`debian-12-13`). Un dossier sans ce fichier est de `debian-11-12` : ce sont ceux que la beta a déjà laissés sur les boxes. `head()` et `otherPlans()` ne regardent que les dossiers de leur saut, et `backupFoldersScopedByHop` passe à vrai (mon choix : un fichier plutôt qu'un nouveau nom de dossier, parce que le nom actuel est lu par le pilote, le reducer, la reprise et la doc).

Le cas à couvrir par un test : une box qui a fini 11 → 12 garde son dossier, et ce dossier ne doit pas arrêter le bloc 0 du saut 12 → 13 au motif d'« un autre plan de cette machine ».

### 3. Le plan lit les `.sources` (deb822), pour 12 → 13 seulement

`HopSpec` gagne `Deb822 bool`, vrai pour 12 → 13 seulement (mon choix : le saut 11 → 12 est mesuré, et l'ouvrir aux `.sources` changerait ce qu'il fait sur une box qui en a). Avec `Deb822`, le plan et la refresh lisent les `.sources` avec le lecteur que la readiness a déjà (`parseDeb822` dans `osreadiness/sources.go`), sans en écrire un deuxième.

Les règles, par strophe, reprennent celles des lignes en une ligne :

- **Toutes les `URIs` de la strophe sont celles de Debian** (même décision que pour un `.list`) : chaque mot de `Suites` passe à la release cible (`bookworm` → `trixie`, `-updates` et `-security` suivent). Un mot `-backports` ou `-proposed-updates` est retiré, et une strophe qui n'avait que celui-là prend `Enabled: no`. `non-free-firmware` est ajouté à `Components` à côté de `non-free` quand il n'y est pas.
- **Aucune `URIs` n'est de Debian** : la strophe est mise de côté comme une ligne tierce, et un fichier dont toutes les strophes sont tierces est mis de côté entier, comme un `.list` (c'est le cas de `docker.sources`).
- **Une strophe qui mêle Debian et un autre dépôt**, ou un fichier qui mêle des strophes de Debian et des strophes tierces : refus `sources_mixed_file`, comme pour un `.list`.
- **Un champ ou une valeur que le lecteur ne sait pas lire**, ou `Enabled: no` déjà présent : refus `sources_format`, avec le fichier et la strophe (`Enabled: no` d'origine est refusé parce que le rendre « octet pour octet » se lit mal quand on ajoute soi-même ce champ ; mon choix, à relâcher si une vraie box l'a).
- `Signed-By`, `Architectures`, les commentaires et l'ordre des champs sont gardés tels quels. Seuls `Suites`, `Components` et `Enabled` changent.

La restauration en fin de run rend chaque `.sources` octet pour octet et le vérifie par hash, comme les `.list` aujourd'hui (`restore-sources.sh` sait déjà effacer `*.sources`).

### 4. La ligne du saut 12 → 13

- `planHops` : `{ID: HopDebian12to13, From: "bookworm", To: "trixie", Later: ["forky", "duke"], Firmware: true, Deb822: true}`. `Firmware` reste vrai : une box venue de 11 avec la beta peut avoir `non-free` sans `non-free-firmware`.
- `runbookHops` : les mêmes blocs, renommés `debianBlocks`.
- Les commandes apt ne changent pas. Les notes de trixie prescrivent la même suite (`apt update`, `apt upgrade --without-new-pkgs`, `apt full-upgrade`) que le saut mesuré, qui utilise `apt-get` aux mêmes effets. Le plan tourne avec l'apt de la box (2.6.1, déjà lu par `allow.go`). Les passes de rattrapage après le bloc 5 tournent avec apt 3.0, dont la sortie est déjà acceptée (#78).

### 5. Les nouveaux contrôles de la readiness, pour 12 → 13

Ils lisent des fichiers et passent par la liste blanche des commandes, comme les autres :

| Code | Rouge ou avertissement | Ce qu'il lit | Pourquoi |
|---|---|---|---|
| `ssh_dsa_hostkey` | rouge | `HostKey` de `sshd_config` et `sshd_config.d` | OpenSSH 10 ne charge plus de clé DSA : sshd ne démarre pas, et la box est perdue sans console |
| `arch_i386` | rouge | `dpkg --print-architecture` | Debian : « Users running i386 systems should not upgrade to trixie » |
| `cryptsetup_missing` | rouge | `/etc/crypttab` non vide sans `systemd-cryptsetup` | sans ce paquet, les volumes chiffrés ne s'ouvrent pas au redémarrage |
| `sysctl_conf` | avertissement | réglages actifs dans `/etc/sysctl.conf` | systemd-sysctl ne le lit plus : les réglages disparaissent sans bruit |
| `tmp_tmpfs` | avertissement | `/tmp` non monté à part dans `/etc/fstab` | après le redémarrage, `/tmp` est en mémoire (jusqu'à 50 %) et vidé à chaque démarrage ; `systemctl mask tmp.mount` pour le garder |

Pour `armel`, la dernière release est trixie : ce n'est pas un refus, et je n'ajoute rien. Les noms des cartes réseau restent non contrôlés, comme pour 11 → 12 : c'est écrit dans la doc et le propriétaire est à la console. Chaque contrôle a sa phrase en anglais et en français dans l'interface.

### 6. Après le saut

`osverify` est générique : il compare les faits d'avant et d'après sans nommer de release. Le bloc 8 vérifie que les sources nomment `c.hop.To` : c'est déjà paramétré. Rien d'autre ne change.

### 7. Interface

La page ne connaît pas les releases, sauf les phrases. Il faut les nouvelles clés en anglais et en français (titres des blocs du saut 12 → 13, lignes STOP, nouveaux contrôles), et une fixture de réponses du cœur pour 12 → 13 dans les tests de `OsUpgradePlan`.

### 8. Preuve et doc

- La branche `ci/os-upgrade-proof` (5 commits, jamais fusionnée, elle ne touche que des fichiers nouveaux) est rebasée et gagne une jambe Debian 12 depuis l'image cloud de bookworm. Cette jambe **garde** le `debian.sources` de l'image, au lieu de le remplacer par un `sources.list` comme le fait la jambe 11 : c'est la seule façon de faire tourner la lecture deb822 pour de vrai (mon choix).
- `docs/os-upgrade-beta.md` gagne une section 12 → 13 avec les marques [M], [R] et [N]. Tout y est [N] tant que la jambe 12 n'a pas tourné. Les lignes 29, 35 et 54, qui disent encore qu'il n'y a pas de page, sont corrigées au passage.

## Découpage en PR

1. Phrases tirées du saut, dossiers rangés par saut. **Aucun changement de comportement** : les golden 11 → 12 sont identiques.
2. Lecture et réécriture deb822, derrière `Deb822`. Tests sur des `.sources` réels (image cloud bookworm, `docker.sources` de la doc Docker).
3. La ligne 12 → 13 dans les deux tables, les cinq contrôles, les golden `runbook-d12-13.*`.
4. Interface : clés, fixture.
5. Jambe de preuve Debian 12, puis doc beta.

Chaque PR passe `go test ./...` sous Linux (le cœur a des tests `_linux_test.go`) et, pour l'interface, avec Node 22 et pnpm 9.

## À reprendre dans la PR 3 (relevé par la relecture de la PR 1)

- `hop_folders_test.go` ne tourne que sur le runbook à la main : l'ajouter à `execSuiteFiles` (`auto_exec_test.go`) pour qu'il tourne aussi sur le texte de l'unité, et ajouter un cas R0 de la refresh à côté d'un dossier 11 → 12 fini.
- `osUpgradeManualFolder` (`service/system_os_upgrade_start.go`) n'est pas rangé par saut côté Go. C'est voulu, puisqu'un dossier à moitié fait d'un autre saut doit refuser le démarrage, mais il faut un test qui le dise pour 12 → 13.
- `/var/backups/casaos-os-upgrade-timers.state` est commun aux deux sauts. Il faut vérifier qu'un fichier laissé par un 11 → 12 interrompu ne trompe pas le bloc 1 de 12 → 13.

## Laissé au constructeur

- Le texte exact des nouvelles phrases, dans le registre des phrases actuelles.
- La forme interne de la réécriture deb822, tant qu'elle passe par le lecteur existant et rend les fichiers octet pour octet.
- Les seuils de `tmp_tmpfs`. Ma proposition : avertir sans mesurer `/tmp`.

## Risques

- Une image cloud de bookworm met souvent `debian.sources` **et** un `sources.list` vide ou commenté. Les deux doivent passer.
- L'apt 3.0 qui tourne après le bloc 5 n'a jamais été exécuté par le moteur sur une vraie machine. Seule sa sortie a été corrigée (#78).
- La jambe de preuve 12 demande un runner KVM, comme la jambe 11 : elle peut ne pas tourner tout de suite.
