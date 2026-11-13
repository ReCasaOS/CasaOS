# Relais : un journal de tout ce qui passe (conception)

## Pourquoi

L'exploitant du relais veut **suivre le trafic qui passe par le relais**, comme on lit un `access_log` et un `error_log` fusionnés : chaque visiteur d'une app partagée, chaque requête au relais lui-même, les événements de protection et les erreurs, dans un seul fil, en direct, et pouvoir remonter en arrière (propriétaire, 2026-11-13).

Aujourd'hui, rien de tout cela n'existe : `share` n'écrit aucune ligne par connexion (seulement les plafonds atteints, une fois par minute), Caddy (`front`) n'a pas de journal d'accès, et `/admin` ne montre que des totaux (débit des flottes par box, refus par heure).

## Décisions du propriétaire (2026-11-13)

- **Un fil unique, comme un access_log et un error_log fusionnés**, pour tout ce qui passe par le relais.
- **Visible en direct dans `/admin`.**
- **Gardé 7 jours**, avec **une rotation** (un fichier par jour).
- **Approche A** (ci-dessous) : chaque service écrit son propre journal dans un dossier commun ; `/admin` les fusionne.
- Pour les apps partagées, le **nom d'hôte** est montré, **pas le chemin** : le relais ne déchiffre pas (accepté par le propriétaire après explication).

## Ce que montre le fil

Une ligne par :
1. **connexion d'un visiteur à une app partagée** (`share`) : heure, adresse du visiteur, nom d'hôte demandé (SNI), décision (passée ; ou coupée avec la raison et la source, comme dans la protection du relais : `manual`, `suspended`, `external`, `community`, `cap`). À la fermeture d'une connexion passée : une seconde ligne avec la durée et les octets dans chaque sens (mon choix : deux lignes plutôt qu'une seule à la fin, pour voir une connexion longue dès qu'elle arrive) ;
2. **requête HTTP au relais lui-même** (`front`) : heure, adresse, hôte, méthode, chemin, code de réponse, durée, octets — l'API des box (`/v1/...`), `/register`, `/admin`, le serveur de coordination, le DERP (une ligne par connexion d'une box au relais, quand elle se ferme), le tunnel des box (`/v1/share/tunnel`) ;
3. **événement du relais** (`enrolment`) : ban communautaire ouvert ou levé, liste externe mise à jour ou en échec, base DB-IP mise à jour, signalements reçus d'une box (nombre gardé), réglages de protection d'une box changés, nom suspendu ou repris ;
4. **erreur** de `share` ou d'`enrolment` (ce qu'ils écrivent déjà dans leurs journaux, plus les refus d'API en 4xx/5xx qui sont déjà dans les lignes de `front`).

Hors du fil (mon choix) : le contenu du trafic chiffré des flottes (une ligne par connexion DERP suffit), les journaux de Headscale et de derper eux-mêmes (leurs requêtes passent par `front` et y sont vues), les lignes de santé internes (`/healthz`, l'instantané des filtres, les envois d'événements entre `share` et `enrolment`).

Exemple de ce qu'on voit :

```
11:42:07  share   203.0.113.7    jellyfin.mabox.box.recasaos.org   passée
11:42:07  share   198.51.100.20  nextcloud.autre.box.recasaos.org  coupée · liste FireHOL level 1
11:42:08  front   192.0.2.10     relay.recasaos.org  POST /v1/share/bans  204  12 ms
11:42:10  relais  ban communautaire ouvert · 203.0.113.50 · 2 flottes · 24 h
11:43:31  share   203.0.113.7    jellyfin.mabox.box.recasaos.org   fermée · 84 s · ↓ 12,4 Mo ↑ 210 Ko
11:44:02  erreur  enrolment · liste « AbuseIPDB » : téléchargement refusé (429)
```

## Approches

- **A. Chaque service écrit son propre journal dans un volume commun** (retenue). `share`, `front` (le journal d'accès JSON de Caddy) et `enrolment` écrivent des lignes JSON dans `logs/`, un fichier par service et par jour ; `/admin` lit et fusionne par heure, suit les fichiers en direct, filtre. Fichiers standards, lisibles aussi par `tail -f` et `grep` sur le serveur ; pas de trafic interne en plus ; une attaque sur `share` ne charge pas `enrolment`. Coût : un volume partagé entre trois conteneurs et une fusion à la lecture.
- **B. Tout passe par `enrolment`, qui écrit un seul fichier** (écartée) : chaque connexion envoyée à `enrolment`, beaucoup de trafic interne pendant une attaque, lignes perdues ou à garder quand `enrolment` redémarre.
- **C. Les journaux Docker lus depuis `/admin`** (écartée) : il faudrait le socket Docker dans `enrolment`, c'est-à-dire le contrôle de la machine.
- Une pile externe (Loki, Grafana) est écartée : trop lourde pour un relais auto-hébergé.

## Comment

### Fichiers

- Un volume `relay-logs` monté en écriture dans `share`, `front` et `enrolment` (à `/logs`), en lecture seule ailleurs s'il le faut.
- Un fichier par service et par jour UTC : `share-2026-11-13.jsonl`, `front-2026-11-13.jsonl`, `enrolment-2026-11-13.jsonl` (mon choix des noms ; Caddy peut imposer son propre nommage de rotation : le constructeur l'adapte et le lecteur suit).
- **Rotation** : un nouveau fichier chaque jour ; les fichiers de plus de **7 jours** sont supprimés par le service qui les écrit (pour Caddy, sa propre rotation, `roll_keep_for 168h` ou équivalent).
- Une ligne = un objet JSON avec au moins `time` (RFC 3339 avec millisecondes, UTC), `src` (`share`, `front`, `relay`, `error`) et les champs de son type (le constructeur fixe la liste exacte dans le plan ; `/admin` et un `jq` sur le serveur s'en servent).
- **Ne jamais écrire** : la clé d'une liste externe, le jeton `ADMIN_TOKEN`, l'en-tête `Authorization`, les cookies, les en-têtes signés des box (`X-Box-Signature`). Caddy masque déjà `Authorization` et `Cookie` dans ses journaux par défaut : le constructeur le vérifie et le garde.
- **Écriture qui ne bloque jamais le trafic** : `share` écrit à travers un tampon ; si le disque ne suit pas (attaque), il **jette des lignes plutôt que de ralentir les connexions**, et écrit une ligne « N lignes non écrites » dès qu'il peut (mon choix : le trafic passe avant le journal).
- **Disque** : en plus des 7 jours, un plafond de taille total (`LOG_MAX_SIZE`, par défaut 2 Go, mon choix) : au-delà, les fichiers les plus anciens sont supprimés avant 7 jours, et le fil le dit. Sans plafond, une journée d'attaque remplirait le disque.

### `/admin/journal`

- Une nouvelle page, liée depuis l'en-tête de `/admin` et de `/admin/protection`, sous la même protection (jeton, Basic, CSRF pour d'éventuelles actions).
- **En haut**, des filtres : source (visiteurs des apps / requêtes au relais / événements / erreurs), recherche libre (adresse, nom d'hôte, chemin, raison), « erreurs et coupures seulement » ; une plage de temps (dernière heure par défaut, jusqu'à 7 jours).
- **Le fil** : les lignes les plus récentes en haut, mises en forme comme l'exemple, une couleur par type (passée, coupée, erreur) ; cliquer une adresse filtre sur elle, cliquer un nom d'hôte filtre sur lui.
- **En direct** : tant que la page est ouverte et sur « maintenant », les nouvelles lignes arrivent toutes seules (flux poussé par le serveur, pas de rechargement de page) ; un bouton « Pause » fige le fil pour lire.
- **Remonter** : en changeant la plage, la page lit les fichiers du passé, par pages de quelques centaines de lignes (« Plus ancien »).
- **Limites** : un débit maximal de lignes poussées au navigateur (ex. 200 par seconde, mon choix) ; au-delà, la page affiche « N lignes non montrées, mettez en pause ou filtrez ».
- Pas de téléchargement en masse dans cette version (le serveur a les fichiers).

### Données personnelles

Ce journal garde l'adresse IP de chaque visiteur des apps partagées et de chaque client du relais **7 jours**. C'est plus long que les 24 h promises jusqu'ici pour les connexions acceptées de la protection. La section « Protection of shared apps » et une nouvelle section « Logs » de `docs/self-hosting.md` le disent clairement : ce qui est écrit, combien de temps, où, comment réduire la durée (`LOG_KEEP_DAYS`, 1 à 7, défaut 7 — mon choix d'en faire un réglage) ou couper le journal (`LOG_DIR` vide). Le texte public ne détaille aucune faiblesse.

## Hors de ce document

- Le chemin et le code HTTP des requêtes vers les apps partagées (il faudrait déchiffrer).
- Un journal par paquet du trafic des flottes.
- Les alertes (mail, notification) sur un motif du journal.
- L'export, l'envoi vers un service externe (syslog, Loki).

## Ce qui serait un échec même si tout marche

- Un relais ralenti ou coupé parce que son journal n'arrive plus à écrire.
- Un disque plein à cause du journal.
- Une clé, un jeton ou un cookie dans un fichier de journal.
- Des adresses gardées au-delà de 7 jours, ou un relais qui écrit ce journal sans que la documentation le dise.
- La page `/admin/journal` qui fait tomber le navigateur ou `enrolment` pendant une attaque.

## Tests et preuve

- `share` : une ligne par connexion passée, coupée (chaque raison), fermée (durée, octets) ; aucune ligne pour le nom du relais ; tampon plein → lignes jetées et ligne de comptage, connexions non ralenties (test de charge court).
- Rotation et rétention : changement de jour (horloge simulée), suppression après 7 jours et au plafond de taille, `LOG_KEEP_DAYS`.
- `front` : la configuration Caddy écrit du JSON dans `/logs` avec rotation ; `Authorization` et `Cookie` absents des lignes.
- `enrolment` : chaque événement listé produit sa ligne ; aucune clé de liste dans les lignes.
- `/admin/journal` : fusion de trois fichiers par heure, filtres, pagination vers le passé, flux en direct, limite de débit.
- e2e : une connexion d'un visiteur et une requête à l'API apparaissent dans `/admin/journal`.

## Laissé au constructeur

Les champs exacts des lignes JSON, la façon de suivre les fichiers (lecture périodique ou notifications du système), le mécanisme du flux en direct (Server-Sent Events conseillé, comme pour un `tail -f`), la mise en page dans le style des pages `/admin` existantes, le nommage des fichiers de Caddy.
