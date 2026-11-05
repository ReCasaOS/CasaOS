# Flotte : plusieurs boxes ReCasaOS vues et pilotées depuis l'une d'elles (conception)

Date : 2026-11-02. Statut : approuvé par le propriétaire le 2026-11-02 ("Oui, au plan") ; rien n'est construit. **(propriétaire)** = décidé par Gary ; **(mon choix)** = décision du concepteur, à contester.

## Pourquoi

Le propriétaire gère plusieurs boxes ReCasaOS sur des sites différents (chez des clients, derrière leur propre accès Internet). Aujourd'hui, savoir si tout va bien, voir ce qui tourne ou passer une mise à jour veut dire se rappeler des adresses et ouvrir chaque dashboard. Il veut **une page unique** qui dit "tout est vert ?", liste **tous les conteneurs et toutes les VMs** de toutes les boxes, montre l'OS de chacune, et permet d'**agir en groupe** : démarrer, arrêter, redémarrer, mettre à jour les apps, mettre à jour ReCasaOS et les paquets système sur plusieurs boxes d'un coup.

## Décisions du propriétaire

- Les boxes sont **dispersées** ; Tailscale "ou du genre" est envisageable mais pas acquis.
- Le master est **une fonction de ReCasaOS activée sur l'une des boxes**, pas un service à part.
- Pas seulement une vue : **des actions groupées**, et la liste de **tous les conteneurs**.
- Dans la première version : les **VMs** (même traitement que les conteneurs), **l'OS de chaque box**, et **la mise à jour des paquets système partout en même temps**.
- Le dashboard complet d'une box s'ouvre par sa propre adresse (Tailscale ou locale), pas à travers le master.

## Ce qui existe déjà (relevé du code, 2026-11-02)

- **La passerelle** relaie par préfixe de chemin avec le `ReverseProxy` de la bibliothèque standard, WebSocket compris (`/v1/sys/wsssh`, `/v1/file/ws` passent déjà). Elle sert HTTPS sur 443 d'office avec un certificat auto-signé (`/var/lib/casaos/tls/gateway.crt`, CN "CasaOS", noms `localhost` + nom de machine, adresses 127.0.0.1 et ::1), ou le certificat réglé dans `gateway.ini`.
- **L'identité "service interne"** : une requête venant de la boucle locale avec `Authorization: Internal <secret>` (`/var/run/casaos/internal.secret`, écrit par la passerelle à chaque démarrage, root 0600) passe le contrôle du jeton dans **tous** les services (cœur, AppManagement, LocalStorage, UserService, MessageBus, passerelle, et le module VirtManagement : même `Skipper` dans son `route/v2.go`, vérifié sur GitHub le 2026-11-02) ; `headerTokenOnly` du cœur, qui protège les routes qui arrêtent tout (mise à jour, modules, Docker), la laisse passer aussi. C'est ainsi que le cœur interroge déjà AppManagement avant une mise à jour automatique (`GET /v2/app_management/operations`). CasaOS-Common fournit `external.InternalAuthorization(runtimePath)` et l'ajout automatique de l'en-tête vers la boucle locale.
- **Les jetons** : un seul compte admin, JWT ES256 de 3 h, refresh de 7 jours, **aucune clé d'API ni jeton longue durée**. Stocker des identifiants de chaque box sur le master n'est donc ni possible proprement ni souhaitable.
- **Tout ce que la page doit montrer ou faire a déjà sa route** (toutes derrière le jeton, sauf la première) :
  - version et mise à jour : `GET /v1/sys/version/check` (`current_version`, `need_update`, `version`), `POST /v1/sys/update` (unité `casaos-update`, le cœur redémarre), `GET /v1/sys/autoupdate` ;
  - OS : `GET /v1/sys/os` (`pkg/ossupport` : distribution, version, `support` ∈ supported | ending_soon | security_ended | unknown) ;
  - paquets : `GET /v1/sys/packages` (fait `apt-get update`, jusqu'à 5 min, Docker à part), `POST /v1/sys/packages/update`, `GET /v1/sys/packages/update/status` (idle | running | finalizing | succeeded | failed) ;
  - machine : `GET /v1/sys/utilization` (processeur, mémoire, réseau, disques), `GET /v2/casaos/health/services`, `GET /v1/sys/modules` ;
  - apps : `GET /v2/app_management/compose` (état et `update_available` par app), `GET /v2/app_management/web/appgrid` (ce que la grille affiche : apps v2, v1, conteneurs hors compose avec leur état), `PUT /v2/app_management/compose/{id}/status` ("start" | "stop" | "restart", asynchrone, événements `app:*`), `PATCH /v2/app_management/compose/{id}` (mise à jour d'une app), `POST /v2/app_management/compose/updates` + `GET .../updates/run` (mise à jour de plusieurs apps d'une box avec suivi par app), `POST /v2/app_management/image-updates` (vérification des images, lent), `GET /v2/app_management/operations` ;
  - VMs (module `virt`, dépôt CasaOS-VirtManagement) : `GET /v2/virt_management/vms` (`id`, `title`, `state` ∈ running | shut-off | paused | crashed | …, `vcpus`, `memory_mib`, `os`), `POST /v2/virt_management/vms/{id}/{action}` (start | shutdown | reboot | force-off | reset | pause | resume) ; la console est un WebSocket VNC.
- **Les patrons du cœur** : un paquet par fonction avec un fichier d'état JSON sous `/var/lib/casaos` écrit de façon atomique (`pkg/telemetry`, `pkg/autoupdate`, `pkg/alerts`), un `Run(ctx)` lancé depuis `main.go`, des routes `/v1/...` écrites à la main dans `route/v1.go`, l'enveloppe `{success, message, data}`. `gorilla/websocket` est déjà dans `go.mod` (client du bus). Le hub d'alertes (`pkg/alerts`) a des catégories fixes et une anti-répétition de 6 h.
- **Le dashboard** (CasaOS-UI, branche `main` de ReCasaOS : Vue 3, Buefy, Vuex, vitest) a le patron "application système chargée à la demande" : une carte dans la grille (`builtInApplications`, `app_type: 'system'`), un `entry.js` qui ouvre la page en modale ou dans un onglet (`#/stats`), comme Statistiques et Machines virtuelles. Les réglages sont des lignes du menu de la barre du haut qui ouvrent une modale, visibles seulement si le cœur répond à la route (`alertsAvailable`). Les actions longues se suivent par les événements du bus (socket.io) ou par un sondage. Les textes : clé anglaise, valeurs en `en_US.json` et `fr_FR.json`, un test de parité des clés.
- La conception des statistiques (`2026-10-20-stats-design.md`) a mis "plusieurs machines" hors périmètre : c'est ce projet.

## Ce que ça fait

### Les rôles et l'enrôlement

- **Master** : sur une box, le propriétaire active le rôle et donne **l'adresse à laquelle les autres boxes le joignent** (`https://…`, nom ou IP, Tailscale ou publique). Il ajoute ensuite chaque box par son nom ; le master génère **un code d'enrôlement** (une seule chaîne à copier, montrée une seule fois) qui contient son adresse, l'identifiant de la box, une clé secrète propre à cette box et l'empreinte de son certificat.
- **Membre** : sur chaque autre box, dans les réglages, "Rejoindre une flotte" : coller le code. La box se connecte au master et le reste. "Quitter" efface tout de son côté.
- Le master **révoque** une box en la retirant de la liste (la connexion est coupée, la clé ne vaut plus rien) et peut **régénérer** son code (l'ancienne clé est remplacée).
- Le master **s'enrôle lui-même** par un bouton "Ajouter cette box" : il figure dans sa flotte comme n'importe quelle autre, par le même tunnel (vers sa propre passerelle). Aucun cas particulier dans la page.
- Une box a **un seul master** ; un master n'est pas lui-même membre d'un autre.

### Le tunnel

- Le **membre** ouvre une connexion WebSocket sortante vers `wss://<master>/v1/fleet/tunnel`, s'authentifie avec son identifiant et sa clé, et la garde ouverte (ping toutes les 30 s ; reconnexion automatique avec un délai qui double de 5 s à 5 min). Rien n'écoute côté membre, aucun port à ouvrir, Tailscale ou pas.
- Le membre **vérifie le certificat** du master comme suit (mon choix) : la chaîne est vérifiée normalement (un vrai certificat devant le master marche sans rien) ; si elle n'est pas valide, la connexion est acceptée **seulement** si l'empreinte SHA-256 du certificat présenté est celle du code d'enrôlement (le cas du certificat auto-signé d'office). Sinon, refus et erreur lisible dans les réglages.
- Le tunnel est **asymétrique** : seul le master y envoie des requêtes ; un membre n'y envoie que des réponses et un "bonjour" (nom de machine, version). Un code volé ne permet donc jamais d'appeler l'API du master.
- Une seule connexion par box : une nouvelle remplace l'ancienne. Un membre révoqué (401 du master) ne réessaie plus que toutes les 10 min et le dit dans ses réglages.
- Un membre sans master joignable continue de fonctionner exactement comme avant : le master n'est jamais nécessaire à la box.

### Le relais, et la liste des routes autorisées

- Sur le master, `/v1/fleet/boxes/{id}/relay/<chemin>` envoie la requête (méthode, chemin, paramètres, `Content-Type`, `Accept`, corps) dans le tunnel de la box `{id}`. Le membre l'exécute **contre sa propre passerelle locale** (`http://127.0.0.1:<port>`) avec son identité de service interne, et renvoie statut, `Content-Type` et corps tels quels. La page Flotte appelle donc **les mêmes routes que le dashboard d'une box**, préfixées par la box.
- **Seules les routes de la table ci-dessous passent**, vérifiées **des deux côtés** (le membre ne fait pas confiance au master) ; tout le reste répond 403. `{id}` est un segment sans `/`. Le paramètre `token` est toujours retiré.

| Lecture | Action |
|---|---|
| `GET /v1/sys/version/check`, `GET /v1/sys/os`, `GET /v1/sys/utilization`, `GET /v1/sys/modules`, `GET /v1/sys/autoupdate` | `POST /v1/sys/update` |
| `GET /v1/sys/packages` (lent), `GET /v1/sys/packages/update/status`, `GET /v1/sys/docker/update/status` | `POST /v1/sys/packages/update` |
| `GET /v2/casaos/health/services` | |
| `GET /v2/app_management/compose`, `GET /v2/app_management/web/appgrid`, `GET /v2/app_management/operations`, `GET /v2/app_management/compose/updates` (lent avec `check=true`), `GET /v2/app_management/compose/updates/run`, `GET /v2/app_management/logs/apps`, `GET /v2/app_management/logs`, `GET /v2/app_management/logs/events`, `GET /v2/app_management/logs/export` (lent) | `PUT /v2/app_management/compose/{id}/status`, `PATCH /v2/app_management/compose/{id}`, `POST /v2/app_management/compose/updates`, `POST /v2/app_management/image-updates` (lent) |
| `GET /v2/virt_management/vms` | `POST /v2/virt_management/vms/{id}/{action}`, `action` ∈ start, shutdown, reboot, force-off |

- Jamais par le relais, quoi qu'il arrive : les fichiers, les partages, les comptes, les disques, les sauvegardes, les alertes, le bus, les modules (installation), la console et les journaux système. Les journaux des apps passent, en lecture seule, depuis la spec Journaux du 2026-11-05 (`logs/settings` et `logs/stream` restent refusés). Étendre la liste = une ligne dans la table et son test.
- Délais : 60 s par requête, 5 min pour celles marquées *lent* ; 16 requêtes en vol par box, au-delà 503. Corps limité à 4 Mio dans chaque sens. Box hors ligne : 503 `box offline` ; délai dépassé : 504.
- Sur le master, une action (tout sauf `GET`) exige le jeton d'accès dans l'en-tête, comme `POST /v1/sys/update` sur une box (`headerTokenOnly`).

### La page Flotte

Une application système du dashboard du master, carte "Flotte" dans la grille, chargée à la demande comme Statistiques (modale ou onglet `#/fleet`). La carte n'apparaît que si la box est master. Quatre onglets ; chaque onglet interroge toutes les boxes **en parallèle depuis le navigateur** et fusionne ; une box qui ne répond pas ou qui n'a pas le module (404) donne un état d'erreur ou une absence **pour sa ligne seulement**.

1. **Vue d'ensemble** : une ligne par box : nom, pastille (en ligne / hors ligne depuis…), version ReCasaOS et badge "vX dispo", OS (distribution, version, badge de support : à jour / bientôt fini / fini), apps (en marche / total, en panne), VMs (en marche / total), processeur, mémoire, disque `/`, lien "Ouvrir" vers l'adresse de la box réglée sur le master. Par ligne : ouvrir, renommer, régénérer le code, retirer (confirmation). Un bouton "Ajouter une box" (nom, adresse du dashboard facultative) montre le code à copier.
2. **Conteneurs** : tous les éléments de la grille de chaque box (apps v2, apps v1, conteneurs hors compose) : box, nom, état, mise à jour disponible ; filtres par box et par état ; sélection multiple ; actions **Démarrer, Arrêter, Redémarrer, Mettre à jour** sur la sélection. Les actions ne sont offertes que pour les apps v2 (les seules que l'API sait piloter en v2) ; un conteneur hors compose ou une app v1 est listé et dit pourquoi il n'est pas actionnable. Rafraîchi toutes les 10 s tant que l'onglet est visible.
3. **VMs** : toutes les VMs de chaque box qui a le module : box, nom, état, processeurs, mémoire, OS ; sélection multiple ; **Démarrer, Arrêter, Redémarrer, Forcer l'arrêt** (confirmation pour ce dernier).
4. **Mises à jour** : par box : ReCasaOS (installée / disponible, état des MAJ automatiques), paquets système (nombre en attente, chargé à la demande par "Vérifier" car la route fait `apt-get update`), apps avec mise à jour disponible. Sélection de boxes et trois actions : **Mettre à jour ReCasaOS** (chaque box passe hors ligne quelques minutes puis revient avec sa nouvelle version ; la ligne le suit), **Mettre à jour les paquets** (Docker exclu, comme sur une box ; état suivi par box), **Mettre à jour les apps** (sur chaque box, `POST …/compose/updates` avec ses apps à jour, suivi par `GET …/run`).

Chaque action groupée donne **un résultat par ligne** (fait, refusé 409 avec la raison de la box, hors ligne, erreur), jamais un seul message pour tout le monde. Les dialogues de confirmation reprennent les mots des modales existantes (paquets, mise à jour).

### Les réglages

Une ligne "Flotte" dans le menu des réglages (groupe Système), visible seulement si le cœur répond à `GET /v1/fleet`. Sa modale : le rôle (aucun / master / membre). Master : l'adresse, le bouton vers la page Flotte, "Désactiver" (confirmation : la liste des boxes est effacée). Membre : coller le code, l'état (connecté depuis… / dernier essai et erreur / refusé par le master), "Quitter".

### Les alertes

Le master lève, dans le hub existant, une alerte **`fleet:<box>:offline`** quand une box n'a plus de tunnel depuis 10 min ("La box Maison n'a pas joint le master depuis 10 minutes"), résolue quand elle revient ; rien pendant les 10 premières minutes après le démarrage du master. Nouvelle catégorie `fleet` du hub, activée d'office comme les autres, visible dans la modale Alertes. Aucune autre alerte en v1.

### Les routes du cœur

Toutes sous `/v1/fleet`, derrière le jeton du groupe sauf le tunnel ; enveloppe `{success, message, data}`.

- `GET /v1/fleet` → `{role, master: {address, boxes: [{id, name, url, online, last_seen, hostname, version}]}, member: {master_url, connected, since, last_error}}` ; `role` ∈ none | master | member.
- `PUT /v1/fleet/master` `{enabled, address}` (jeton d'accès en en-tête). Désactiver coupe les tunnels et efface la liste.
- `POST /v1/fleet/boxes` `{name, url?, self?}` → `{box, code}` ; `self: true` enrôle le master lui-même et écrit aussi son `member.json`. `PATCH /v1/fleet/boxes/{id}` `{name?, url?}`. `DELETE /v1/fleet/boxes/{id}`. `POST /v1/fleet/boxes/{id}/code` → nouveau code. (jeton d'accès en en-tête pour tout sauf `PATCH`)
- `ANY /v1/fleet/boxes/{id}/relay/*` : le relais.
- `GET /v1/fleet/tunnel` : WebSocket, hors du groupe JWT, authentifié par `Authorization: Fleet <box_id>:<clé>`.
- `PUT /v1/fleet/member` `{code}` rejoint (remplace) ; `DELETE /v1/fleet/member` quitte. (jeton d'accès en en-tête)

### Les fichiers d'état

- `/var/lib/casaos/fleet/master.json` (root, 0600) : `{"address": "https://…", "boxes": [{"id", "name", "url", "key_sha256", "created_at", "last_seen", "hostname", "version"}]}`. La clé n'est stockée que hachée.
- `/var/lib/casaos/fleet/member.json` (root, 0600) : `{"master_url", "box_id", "key", "master_cert_sha256"}`.
- Fichier absent = rôle inactif ; fichier illisible = rôle inactif et une ligne de journal ; écriture atomique comme `telemetry.json`. Ni clé ni code dans aucun journal.

## Hors périmètre (non-objectifs)

- La console des VMs, les journaux, le terminal et le dashboard complet d'une box à travers le master (il faudrait des flux dans le tunnel ; la liste des routes et le tunnel ne doivent pas l'empêcher).
- Les vues agrégées des sauvegardes, des disques, des alertes, des fichiers, des comptes ; la ligne d'ensemble ne les résume pas non plus en v1.
- Les actions sur les apps v1 et sur les conteneurs hors compose (l'API v2 d'AppManagement n'en a pas).
- Un état des boxes connu du master quand la page est fermée (donc pas d'alerte "mise à jour disponible sur la box X") ; une box dans deux flottes ; plusieurs utilisateurs ou des droits sur le master ; l'installation des modules à distance.

## Ce qui ferait échouer le projet même s'il « marche »

- Un membre qui répond à autre chose qu'à la table, ou à une requête qui n'est pas venue par son tunnel ; un code d'enrôlement qui donne à une box étrangère un accès à l'API du master.
- Une clé, un code ou le secret interne dans un journal, une réponse ou une URL.
- Une box hors ligne montrée comme saine, ou une action groupée sans résultat par box.
- Une mise à jour lancée deux fois sur la même box, ou lancée pendant une sauvegarde (c'est la box qui refuse, par ses garde-fous existants ; la page montre son refus).
- Un membre gêné par un master en panne ou injoignable (reconnexions qui saturent le journal ou le processeur).
- Le master compromis = toutes les boxes pilotables : la 2FA y est à recommander dans la documentation.

## Contraintes

- Tout dans le cœur et le dashboard : pas de nouveau composant, pas de nouvelle dépendance Go ni npm, rien dans l'installateur.
- Le rôle membre n'ouvre aucun port et ne change rien tant qu'aucun code n'est collé ; le rôle master ne change rien aux boxes qui ne l'ont pas rejoint.
- Chaque box garde ses garde-fous : une mise à jour par le relais est exactement la mise à jour du bouton de la box (mêmes routes, mêmes refus 409, même exclusion de Docker).
- Le dashboard ne lit jamais Docker ni libvirt ; il passe par le relais, qui passe par la passerelle de la box.
- Textes : clés anglaises, valeurs en anglais et en français ; tout ce qui vient d'une box (noms, messages) est du texte, jamais du HTML.
- Tests et preuves comme le reste du projet.

## Comment (mon choix)

1. **Un paquet `pkg/fleet` dans le cœur**, avec les deux rôles : chaque box a le cœur, le master n'est qu'une box de plus. Pas de module optionnel (il faudrait l'installer des deux côtés) ni de dépôt à part.
2. **WebSocket + JSON, pas de multiplexeur** : un message par requête et par réponse (`{t: "req", id, method, path, query, headers, body}` / `{t: "res", id, status, headers, body}`, corps en base64), plusieurs en vol, appariés par `id`. `gorilla/websocket` est déjà là. Le jour où il faut un flux (console), ce sera un nouveau type de message.
3. **Relais d'API, pas d'agrégateur** : le master n'a aucune logique métier ni aucun cache ; la page fait les appels box par box dans le navigateur et fusionne, comme la page Statistiques fusionne trois services. Pour quelques dizaines de boxes c'est quelques dizaines de requêtes par onglet, en parallèle.
4. **Le membre exécute avec son identité de service interne** : c'est exactement ce que fait déjà le cœur pour interroger AppManagement, et cela évite tout identifiant sur le master. La table des routes est ce qui borne cette identité ; elle est vérifiée par le membre lui-même.
5. **Épinglage du certificat par empreinte dans le code**, après la vérification normale : le certificat auto-signé d'office marche sans rien configurer ; un vrai certificat (Let's Encrypt devant le master) marche aussi et se renouvelle sans casser l'épingle.
6. **Le master s'enrôle lui-même** plutôt qu'un cas "local" dans le relais : un seul chemin de code, et la page de test d'installation (un seul runner) peut tout exercer de bout en bout.
7. **Fichiers JSON sous `/var/lib/casaos/fleet/`**, comme `telemetry.json`, `autoupdate.json`, `alerts.json` ; pas de table SQLite.
8. **Alerte "hors ligne" seulement**, dans le hub, nouvelle catégorie `fleet`, 10 min de tolérance : c'est la seule chose que le master sait sans la page ouverte.
9. **Page système comme Statistiques** (`src/components/Fleet/entry.js`, `FleetPage.vue`, carte masquée hors master), onglets Buefy, tables avec sélection multiple, un résultat par ligne. Rien de nouveau dans le store Vuex.
10. **Mise à jour ReCasaOS en groupe** = `POST /v1/sys/update` sur chaque box sélectionnée, l'une après l'autre sans attendre ; la page montre "mise à jour…" tant que la box est hors ligne ou répond l'ancienne version, puis la nouvelle version quand son tunnel revient. Pas de mise à jour du master depuis sa propre page (son cœur redémarre : on le fait depuis ses réglages, comme aujourd'hui).
11. **Nommage** : "Flotte" / "Fleet" dans le dashboard, `pkg/fleet`, `/v1/fleet`, catégorie d'alerte `fleet`.

## À la charge du constructeur

- Le format exact des messages du tunnel et des erreurs, les valeurs de délai et de reprise, la lecture du port de la passerelle locale (le cœur la connaît déjà : `external.NewManagementService(...).GetPort()` ou `gateway.ini`), le calcul de l'empreinte (certificat de `gateway.ini` s'il est réglé, sinon `/var/lib/casaos/tls/gateway.crt`).
- Le branchement du tunnel hors du groupe JWT (comme `GET /v1/sys/version/check` dans `route/v1.go`), l'ajout de `/v1/fleet` à la liste des préfixes que `main.go` enregistre auprès de la passerelle, et le joker Echo du relais.
- Deux cas de passerelle à traiter proprement : avec un certificat réglé dans `gateway.ini`, le port "clair" sert lui-même du HTTPS et 443 n'est pas servi (l'auto-enrôlement vise alors `https://127.0.0.1:<ce port>`, avec `ServerName` = hôte de l'adresse du master pour qu'un vrai certificat se renouvelle sans casser l'épingle) ; avec `gateway.Address` réglée sur une adresse qui n'est pas locale, le relais vers `127.0.0.1` ne peut pas marcher et le membre doit le dire dans ses réglages plutôt que d'échouer en silence.
- `GET /v1/fleet` marque la box du master (`self: true`) pour que la page n'offre pas "Mettre à jour ReCasaOS" sur sa propre ligne.
- La disposition des tables, l'icône de la carte, la pagination au-delà de quelques centaines de lignes, les textes exacts en anglais et en français.
- L'ordre de construction : tunnel + relais + table des routes (avec leurs tests), puis les réglages (master, membre, code), la vue d'ensemble, les conteneurs, les VMs, les mises à jour, l'alerte, le test d'installation.

## Tests et preuve

- **Cœur (Go)** : le code d'enrôlement (aller-retour, champ manquant refusé) ; l'authentification du tunnel (clé inconnue, clé révoquée, comparaison à temps constant, une seconde connexion remplace la première) ; la table des routes (chaque ligne passe, `/v1/file/…`, `/v1/users/…`, `GET /v1/sys/update`, un `{action}` hors des quatre, un `?token=` → 403) ; le relais de bout en bout avec `httptest` (master et membre dans le même processus : statut, `Content-Type` et corps rendus tels quels, deux requêtes en vol, corps trop gros, délai → 504, box hors ligne → 503) ; la décision TLS (chaîne valide ; chaîne invalide + empreinte juste ; chaîne invalide + empreinte fausse ; nom d'hôte ignoré quand l'empreinte décide) ; les fichiers d'état (absent, illisible, 0600, atomique) ; l'alerte hors ligne (après 10 min, résolue au retour, rien dans les 10 premières minutes) ; désactiver le master coupe les tunnels, retirer une box coupe le sien.
- **Dashboard (vitest)** : la carte n'existe que si le cœur dit master ; la ligne de réglages absente contre un cœur plus ancien ; chaque onglet fusionne des réponses par box et porte l'erreur sur la ligne seule ; une sélection → une requête par box et par élément, un résultat par ligne ; les dialogues de confirmation ; rejoindre et quitter ; parité des clés en/fr.
- **Test d'installation (un runner)** : une étape "Fleet" de `.github/workflows/install-check.yml` dans CasaOS-Install (le dépôt `get` sur ce disque ; les étapes "Alerts" et "Automatic update" y sont le modèle) : activer le master avec l'adresse `https://127.0.0.1:443`, "Ajouter cette box", voir la box en ligne en moins de 30 s dans `GET /v1/fleet` ; `GET …/relay/v1/sys/version/check` égal à l'appel direct ; arrêter une app de test par le relais et la voir arrêtée ; `…/relay/v1/file/…` → 403 ; retirer la box → le membre se dit refusé.
- **Preuve sur de vraies boxes** avant publication : deux boxes du propriétaire sur deux sites, une mise à jour ReCasaOS lancée depuis la page sur la box distante, et le retour de son tunnel avec la nouvelle version.

## Composants et livraison

- **CasaOS (cœur)** : `pkg/fleet`, les routes `/v1/fleet`, la catégorie `fleet` du hub, les tests.
- **CasaOS-UI** (branche `main` de ReCasaOS, qui a déjà les pages VMs et Statistiques) : la modale de réglages, la page Flotte, les textes, les tests.
- **CasaOS-Install** : rien, sinon la ligne du README et le test d'installation. Livrés ensemble dans une release de la distribution.
