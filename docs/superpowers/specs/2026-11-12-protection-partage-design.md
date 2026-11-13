# Partage public : protéger les apps partagées sur la box (conception)

## Pourquoi

Le partage public (spec `2026-11-09-partage-public-design.md`) expose une app de la box à `<app>.<box>.box.recasaos.org`, à travers le relais. Le relais transmet le TLS **sans le déchiffrer** (passage SNI) : le certificat est sur la box, et seul le Gateway de la box voit les requêtes en clair. Un WAF placé devant le relais ne verrait donc rien des apps. Ce qui protège une app partagée doit vivre **dans le Gateway**.

Aujourd'hui, une app partagée est ouverte à n'importe qui sur internet, et sa page de connexion (Jellyfin, Nextcloud…) est la seule barrière. Le propriétaire (2026-11-12) veut :
- partager certaines apps avec des **proches**, derrière un **code par personne** ;
- partager d'autres apps **avec tout le monde**, en les protégeant contre les **attaques web courantes**, les **scans** et les **abus** ;
- **voir** ce qui a été bloqué, et pourquoi.

Ce document couvre **la box** (projet 1). Le relais (listes externes AbuseIPDB et CrowdSec, bans remontés par les box, liste communautaire) est un projet séparé, écrit ensuite (projet 2) ; ce document garde seulement la place pour s'y brancher.

Ce qui existe déjà côté relais (2026-11-12) : plafonds de connexions par IP et par IP et box (ReCasaOS-Relay#11), et HTTP/2 sur le listener de partage du Gateway (CasaOS-Gateway#12, dans v0.5.136).

## Décisions du propriétaire

- **Deux usages, au choix par app** : une app partagée est soit **protégée par code**, soit **ouverte à tous** (propriétaire).
- **Un code par personne** : chaque personne a son propre code, qu'on peut couper sans toucher aux autres ; un code ouvre **un choix d'apps** de la box ; **pas d'expiration** automatique (propriétaire).
- Un proche tape le code **une fois** sur une page ReCasaOS, puis ce navigateur reste reconnu **quelques semaines** (propriétaire, sur ma proposition).
- **Un WAF plus complet** que le seul code (propriétaire) :
  - les **règles OWASP** (Core Rule Set), avec le choix **coupé / observer / bloquer**, et la possibilité de **désactiver certaines règles** ;
  - **limitation par IP** ;
  - **listes d'adresses et de plages** (autoriser, bloquer) ;
  - **blocage par pays** ;
  - **ban automatique** sous conditions : scan, déclenchements répétés des règles, dépassement des limites, codes ratés.
- Les réglages se font **pour toute la box, et/ou app par app** (propriétaire).
- Le dashboard montre les **événements** (date et heure, IP, raison, pays, réseau/ASN, **app**) et les **bans** (propriétaire).
- Les **listes externes** (AbuseIPDB, CrowdSec) et la **liste communautaire** vont **sur le relais**, qui coupe l'adresse avant le tunnel ; le partage des bans d'une box vers le relais se fait **sur accord du propriétaire de la box** (propriétaire). Hors de ce document.
- Ordre : **la box d'abord** (propriétaire : « comme tu veux », sur ma recommandation).

## Ce qui existe déjà (relevé du code, 2026-11-12, `inkly/main`)

- **Cœur** (`CasaOS`, paquet `pkg/share`) : l'état du partage est dans `/var/lib/casaos/fleet/share.json` (`State{Name, Host, Domain, TOSAcceptedAt, Apps map[string]App{Port, Scheme}}`, `state.go`). L'API `/v1/share` (`route/v1/share.go`) : `GET` statut, `PUT /name` (`accept_tos`), `PUT`/`DELETE /apps/{name}`, `DELETE /apps`. Le cœur enregistre chaque app auprès du Gateway par `PUT /v1/gateway/hosts` (`{host, target}`) et `DELETE /v1/gateway/hosts/{host}` (`pkg/share/gateway.go`), avec l'en-tête `Authorization: Internal <secret>` lu dans le runtime.
- **Gateway** : la table des hôtes vit dans `hosts.json` **dans le runtime (tmpfs)** ; le cœur ré-enregistre ses hôtes à chaque démarrage. Le listener de partage (`share.go`) lit PROXY v2 puis TLS ; `RemoteAddr` est donc l'**adresse du visiteur**. La chaîne d'une requête : `serveShare` → `GetShareRoute()` → `serveHost()` → `management.GetHostProxy(host)` → `ReverseProxy` (`route/gateway_route.go`). Un hôte inconnu répond 404. Le proxy par hôte pose lui-même `X-Forwarded-For` (les en-têtes envoyés par le visiteur sont retirés). **C'est entre `GetShareRoute` et `serveHost` qu'un filtre se branche** : seul le listener de partage passe par là, pas le dashboard ni le réseau local.
- **Alertes** : le cœur a un `Hub` d'alertes (`pkg/alerts`) qui alimente « Needs attention » et les canaux de notification.
- **Dashboard** (`CasaOS-UI`) : `src/service/publicShare.js` (appels `/v1/share`), composants `src/components/PublicShare/` (`ShareNameForm`, `ShareSection`, `ShareAppModal`), badge « Public » et entrée de menu sur la carte d'app.

## Ce que ça fait

### Périmètre

- Tout ce qui suit s'applique **uniquement** aux requêtes qui arrivent par le **listener de partage** du Gateway, donc aux visiteurs venus du relais. Le dashboard, le réseau local et les appareils de la flotte (Tailscale) ne sont **jamais** filtrés (mon choix : ne jamais risquer de s'enfermer dehors).
- Une box qui ne partage rien ne voit **rien changer** : pas de téléchargement, pas de règles chargées, pas de nouvelle section dans le dashboard (mon choix).
- **Un interrupteur général « Protection »**, **coupé par défaut**, y compris pour les apps déjà partagées (mon choix, pour l'échec « une app cesse de marcher après une mise à jour ») : tant qu'il est coupé, **ni limites, ni bans automatiques, ni règles OWASP, ni pays** ne s'appliquent, et la box ne télécharge rien. Allumé, les valeurs de la box s'appliquent. **Le code d'accès en est indépendant** : il s'active app par app en passant l'app en « protégée », que l'interrupteur soit allumé ou non. « Dès que la protection est activée » désigne cet interrupteur.
- **Seule entrée filtrée** : le listener de partage écoute sur `127.0.0.1` uniquement, et seul le tunnel du cœur s'y connecte (chaque flux du relais, PROXY v2 puis TLS) ; l'adresse PROXY v2 est donc celle que le relais a vue, et on lui fait confiance pour les bans. Il n'existe pas d'autre entrée pour les noms partagés (les défis ACME passent par le DNS du relais).

### Mode d'accès d'une app

Chaque app partagée a un **mode d'accès** :
- **Ouverte** : comme aujourd'hui, tout le monde passe (sous réserve des autres filtres).
- **Protégée par code** : seules les personnes dont le code ouvre cette app passent.

Une app nouvellement partagée est **ouverte** (comportement actuel ; mon choix, pour ne rien casser chez ceux qui partagent déjà).

### Personnes et codes

- **Les apps protégées ferment en cas de doute** (mon choix) : tant que le Gateway n'a pas reçu sa configuration (redémarrage, tmpfs vide), une app protégée répond **503**, jamais ouverte. Le cœur **refuse** de passer une app en « protégée » si le Gateway ne sait pas l'appliquer (le Gateway annonce ce qu'il sait faire ; un Gateway plus ancien ne l'annonce pas), pour que le dashboard ne dise jamais « protégée » d'une app ouverte.
- La box tient une liste de **personnes** : un **nom** (« Maman »), un **code**, la **liste des apps** qu'il ouvre (parmi les apps partagées en mode protégé), la date de création, la date de dernière utilisation.
- Le **code** est **généré par la box** (mon choix), facile à dicter : 3 groupes de 4 caractères dans un alphabet sans caractères ambigus (pas de `0/O`, `1/I/L`), soit environ 60 bits. Il n'est **montré qu'à sa création** (et à sa régénération) ; la box n'en garde qu'une empreinte (mon choix). On peut **régénérer** le code d'une personne (l'ancien cesse de marcher).
- **Couper** une personne (la supprimer, ou régénérer son code) déconnecte ses navigateurs **à leur requête suivante**.
- **Pas d'expiration** (propriétaire).

### Page d'entrée et reconnaissance du navigateur

- Une requête vers une app **protégée**, sans session valide pour cette app, reçoit une **page ReCasaOS** servie par le Gateway : le nom de l'app, un champ pour le code, un bouton. La page vit sur un chemin réservé du même hôte (mon choix : `/.recasaos/acces`, qu'aucune app n'utilise) ; après un code juste, retour à l'adresse demandée au départ.
- Les requêtes qui ne sont **pas** une page (API, WebSocket, fichiers) reçoivent un **401** au lieu de la page (mon choix, sur l'en-tête `Accept` et la méthode) ; les applications mobiles et de synchronisation ne passent donc pas (propriétaire, voir la fin du document).
- Un code juste pose un **cookie de session** valable **30 jours**, prolongés à chaque usage (mon choix sur « quelques semaines »), sur le domaine de la box (`.<box>.box.recasaos.org`), donc reconnu sur **toutes les apps** de cette personne sans retaper le code (mon choix, pour les proches qui utilisent plusieurs apps). Le cookie est `Secure`, `HttpOnly`, `SameSite=Lax`.
- Le cookie est **signé** (HMAC, clé de la box) et porte l'identifiant de la personne et sa **génération** ; couper ou régénérer une personne incrémente sa génération, ce qui invalide ses cookies sans table de sessions (mon choix, le constructeur peut faire autrement si le résultat est le même).
- **Le Gateway retire son propre cookie** de la requête avant de la transmettre à l'app (mon choix, **obligatoire**) : une app partagée ne doit jamais voir le jeton d'une personne, sinon elle pourrait s'en servir pour ouvrir les autres apps.
- Une app partagée pourrait écraser ce cookie sur le domaine de la box : ça ne donne aucun accès, au pire la personne retape son code. Accepté (mon choix).
- Un code qui n'ouvre pas l'app demandée affiche « Ce code n'ouvre pas cette app ».
- Les codes **ratés** comptent pour le ban automatique (voir plus bas).

### Règles OWASP (Core Rule Set)

- Moteur : **Coraza** (`github.com/corazawaf/coraza/v3`, Apache-2.0, v3.8.1 au 2026-10-02) en middleware Go (`http.WrapHandler`), avec le **CRS** embarqué par `github.com/corazawaf/coraza-coreruleset` (Apache-2.0, CRS v4.25) (mon choix : écrit en Go comme le Gateway, pas de processus de plus).
- **Trois modes** pour la box et pour chaque app (propriétaire) : **coupé**, **observer** (`DetectionOnly` : on note ce qui aurait été bloqué), **bloquer**.
- Par défaut : **coupé** (mon choix ; voir « Risques » sur la mémoire d'un Raspberry Pi). Le dashboard propose **observer** comme première étape quand on l'active.
- **Corps des requêtes** (mon choix) : le WAF inspecte au plus le **premier mégaoctet** d'un corps de requête et laisse passer la suite sans l'inspecter (`ProcessPartial`), pour que les gros envois (Nextcloud, Immich) marchent ; les règles sur le **corps des réponses** sont coupées (mémoire, et rarement utiles ici). Les méthodes **WebDAV** (`PROPFIND`, `PROPPATCH`, `MKCOL`, `COPY`, `MOVE`, `LOCK`, `UNLOCK`, `REPORT`) sont ajoutées aux méthodes permises du CRS (Nextcloud, synchronisation). Pas de préréglages par app dans cette étape : le mode observer puis la désactivation de règles depuis le journal suffisent.
- **Niveau de paranoïa 1** (le défaut du CRS, le moins sujet aux faux positifs) ; les niveaux plus stricts ne sont pas proposés dans cette étape (mon choix).
- **Désactiver une règle** pour une app (propriétaire), par son identifiant, en particulier **depuis le journal** : un bouton « Ne plus appliquer cette règle à <app> » sur un événement. Une règle peut aussi être désactivée pour toute la box.
- Fait à respecter : `SecRuleRemoveById` agit **à la lecture de la configuration**, pour toute l'instance du WAF. Pour éviter une instance par app (le CRS pèse lourd en mémoire), la configuration par app se fait de préférence **dans une seule instance**, par des règles de phase 1 qui testent l'hôte et utilisent `ctl:ruleRemoveById` et `ctl:ruleEngine` (mon choix ; le constructeur vérifie que Coraza les applique bien par requête et mesure l'effet).

### Listes d'adresses, pays, limites

- **Listes** (propriétaire) : adresses et plages (CIDR, IPv4 et IPv6) **toujours autorisées** et **toujours bloquées**. Une adresse autorisée passe **tous** les filtres de ce document sauf le code d'accès (mon choix : la liste blanche sert à ne pas se faire bannir soi-même, pas à ouvrir une app protégée).
- **Pays** (propriétaire) : une liste de pays **bloqués**, ou une liste de pays **seuls autorisés** (mon choix d'offrir les deux sens).
  - Base : **DB-IP Lite**, « IP to Country Lite » et « IP to ASN Lite », format MMDB, licence **CC BY 4.0**, mise à jour **mensuelle**, téléchargeable sans compte (`https://download.db-ip.com/free/dbip-<country|asn>-lite-YYYY-MM.mmdb.gz`, environ 8 et 9 Mo compressés en novembre 2026). Lecture par `github.com/oschwald/maxminddb-golang/v2` (ISC) (mon choix).
  - La licence impose un **lien vers DB-IP** sur les pages qui affichent ses résultats : le journal du dashboard porte « IP Geolocation by DB-IP » avec le lien (obligatoire).
  - La box télécharge les deux bases **seulement si un réglage s'en sert** (pays) ou pour afficher pays et réseau dans le journal dès que la protection est activée (mon choix), puis une fois par mois. Si le téléchargement échoue, la box garde l'ancienne base ; **sans aucune base, le filtre pays laisse passer** et « Needs attention » le signale (mon choix : un filtre de pays cassé ne doit pas fermer les apps).
- **Limites** (propriétaire, valeurs par défaut de mon choix, modifiables) : nombre de requêtes par minute et par adresse, par app (par exemple 600/min) ; au-delà, **429**.

### Ban automatique

Une adresse est **bannie** (toutes les requêtes refusées, **403**, sans rien transmettre à l'app) quand, sur une fenêtre glissante (valeurs par défaut de mon choix, modifiables) :
- **règles OWASP** : elle déclenche des blocages répétés (par exemple 10 en 10 minutes), en mode bloquer seulement ;
- **scan** : elle reçoit beaucoup de réponses 404 de l'app (par exemple 100 en 1 minute ; une page Jellyfin ou Immich peut produire des dizaines de 404 d'images manquantes, d'où un seuil haut) ;
- **limite** : elle dépasse la limite de requêtes plusieurs fois (par exemple 3 fois en 10 minutes) ;
- **code d'accès** : elle rate le code plusieurs fois (par exemple 5 fois en 10 minutes).

- Durée : **croissante** en cas de récidive (par exemple 1 h, puis 24 h, puis 7 jours) (mon choix).
- Portée : un ban vaut pour **toute la box** (mon choix : une adresse qui scanne Jellyfin n'a rien à faire sur Nextcloud). Il note **l'app où l'adresse a été prise**.
- Clé : l'adresse IPv4, ou le **/64** en IPv6, comme les plafonds du relais (mon choix).
- Les adresses **toujours autorisées** ne sont jamais bannies.
- Les bans **survivent à un redémarrage** du Gateway et de la box (mon choix).
- Réserve à dire dans le dashboard : un ban sur la box arrive **après** le relais et la connexion du foyer ; il ne protège pas d'un DDoS (c'est le rôle du relais).

### Réglages de la box et réglages par app

- Chaque réglage (mode des règles OWASP, règles désactivées, listes, pays, limites, seuils de ban) a une **valeur pour la box** ; une app peut **la remplacer** pour elle-même (propriétaire). Une app qui ne remplace rien suit la box, y compris quand la box change.
- Pour les **listes** (mon choix) : les adresses toujours autorisées, toujours bloquées et les règles désactivées d'une app **s'ajoutent** à celles de la box, elles ne les remplacent pas (la liste blanche de la box protège le propriétaire de son propre ban). Les **pays**, eux, se **remplacent** en bloc : une app peut avoir sa propre politique de pays, y compris « aucun filtre ».
- Les **seuils** d'un ban sont ceux de l'app où les événements ont été comptés ; le ban qui en résulte vaut pour toute la box (mon choix).
- Le **mode d'accès** (ouverte / protégée) et la liste des apps d'une personne sont **par app** par nature.

### Ce que le propriétaire voit (dashboard)

Dans la partie Partage du dashboard (mon choix d'emplacement ; le constructeur suit les écrans existants de `PublicShare/`) :

- **Protection** (réglages de la box) : mode des règles OWASP, listes, pays, limites, seuils et durées de ban.
- **Personnes** : la liste, avec pour chacune son nom, ses apps, sa dernière utilisation, et les actions « Régénérer le code » et « Supprimer ». Le code s'affiche **une seule fois**, avec un bouton pour le copier.
- Dans la **fenêtre de partage d'une app** : son mode d'accès, les personnes qui l'ouvrent, ses réglages propres (ou « comme la box »), et **ses** événements et bans.
- **Événements** : une ligne par requête bloquée, observée ou bannie. Colonnes (propriétaire) : date et heure, IP, pays (avec drapeau), réseau (nom de l'opérateur et ASN), **app**, chemin demandé, **raison en clair** (« règle OWASP 942100 : injection SQL », « pays bloqué : XX », « scan : 40 pages inexistantes en 1 min », « code d'accès raté 5 fois », « limite de requêtes dépassée »), action (bloqué, banni, observé seulement). Filtres par app et par raison. Lien « IP Geolocation by DB-IP ».
- **Bans en cours** : IP, pays, réseau, **app** où elle a été prise, raison, depuis, jusqu'à ; boutons **Débannir** et **Toujours autoriser cette adresse**.
- **Conservation** : le journal garde **7 jours** et au plus un nombre fixe de lignes (par exemple 10 000), les plus anciennes partent d'abord (mon choix : carte SD d'un Raspberry Pi, et le moins d'adresses IP possible, données personnelles). Les mêmes événements répétés à la suite sont **regroupés** (une ligne avec un compte) pour qu'un scan ne remplisse pas tout.
- **Alertes** : « Needs attention » et les notifications signalent une **vague** (par exemple plus de 20 bans en 10 minutes sur une app) et une base pays absente, **pas** chaque ban (mon choix).

### Ordre des filtres sur une requête

Mon choix, à respecter par le constructeur car il change ce qui est compté :
1. Adresse **bannie** → 403 (rien d'autre n'est évalué).
2. Adresse **toujours bloquée** → 403.
3. Adresse **toujours autorisée** → saute les étapes 4 à 6.
4. **Pays** → 403.
5. **Limite de requêtes** → 429.
6. **Règles OWASP** (selon le mode).
7. **Code d'accès** (si l'app est protégée) → page d'entrée ou 401.
8. Transmission à l'app ; une réponse **404** de l'app compte pour le scan.

La page d'entrée elle-même passe par les étapes 1 à 6.

## Où vivent les données

- **Le cœur garde la configuration** (persistante) : personnes (nom, empreinte du code, génération, apps, dates), mode d'accès des apps, réglages de la box et des apps. Un fichier à côté de `share.json` dans `/var/lib/casaos/fleet/` (mon choix ; nom au constructeur). La **clé HMAC** des cookies y est générée une fois.
- **Le cœur pousse la configuration au Gateway** par l'API interne, comme il pousse les hôtes (même authentification `Internal`), à chaque changement et au démarrage (le Gateway la garde en tmpfs comme `hosts.json`) (mon choix, sur le modèle existant).
- **Le Gateway applique** les filtres, tient les **compteurs**, les **bans** et le **journal**. Les bans et le journal doivent **survivre au redémarrage** : le constructeur choisit où le Gateway les écrit (hors tmpfs), avec des droits restreints (0600).
- **Le dashboard lit** événements et bans **par le cœur** (une route `/v1/share/...` qui relaie le Gateway), pour garder une seule API publique et son authentification (mon choix).
- **Le cœur télécharge** les bases DB-IP (il a déjà la logique de téléchargement et de planification) et les dépose là où le Gateway les lit (mon choix ; le constructeur peut inverser si c'est plus simple).

## Ce qui est prévu pour la suite (projet 2, relais)

Sans le construire maintenant, la box garde de quoi s'y brancher :
- Chaque ban a une **raison typée** (owasp, scan, limite, code, manuel) et une **app**, pour pouvoir être remonté au relais.
- Un réglage « **Partager mes bans avec le relais** » existera plus tard, **désactivé** par défaut, avec un texte sur les données personnelles. Il n'apparaît pas dans cette étape.

## Hors de ce document

- Les listes externes (AbuseIPDB, CrowdSec), la remontée des bans au relais et la liste communautaire (projet 2, relais).
- Une gestion de comptes : les apps gardent leurs propres identifiants ; le code n'est qu'une porte devant.
- Des défis anti-robots (CAPTCHA, preuve de travail).
- La protection du dashboard lui-même, du réseau local et de la flotte.
- Les niveaux de paranoïa 2 à 4 du CRS.

## Ce qui serait un échec même si tout marche (mon interprétation)

- Une app partagée qui marchait **cesse de marcher** après une mise à jour, sans que le propriétaire ait rien activé.
- Le propriétaire **ne comprend pas pourquoi** quelqu'un est bloqué : chaque blocage doit avoir sa raison en clair dans le journal.
- Un Raspberry Pi qui **manque de mémoire** à cause des règles OWASP.
- Une app qui voit le **cookie d'accès** d'une personne.

## Risques

- **Mémoire du CRS** : aucune mesure publiée sur ARM ou Raspberry Pi ; des utilisateurs de Traefik + Coraza rapportent une forte consommation. Le constructeur **mesure** la mémoire résidente du Gateway, avec et sans le CRS, sur une box ARM 64 bits (et 32 bits si le Gateway y tourne), et l'écrit dans la PR. Si c'est trop lourd, le dashboard affiche le coût avant l'activation.
- **Faux positifs** : un envoi de fichier, une recherche, une API JSON peuvent déclencher des règles ; d'où le mode observer et la désactivation par règle.
- **Une seule clé HMAC** : sa fuite permettrait de forger des sessions ; elle reste dans `/var/lib/casaos` (0600), jamais dans une réponse d'API.
- **Données personnelles** : le journal contient des adresses IP ; conservation courte (7 jours).

## Tests et preuve

- Gateway : tests de bout en bout sur le listener de partage (PROXY v2 + TLS, comme `TestShareListenerServesAVisitorThroughPROXYAndTLS`) pour chaque filtre et pour l'**ordre** ci-dessus ; un test qui vérifie que **le cookie d'accès n'atteint jamais l'app** ; un test de WebSocket à travers une app protégée ; un test de redémarrage (bans conservés).
- Cœur : configuration (personnes, génération, régénération, suppression), poussée au Gateway, routes `/v1/share/...` ; la configuration n'expose jamais un code en clair après sa création.
- Dashboard : tests des écrans (personnes, réglages, journal, bans), vus en navigateur contre des réponses simulées.
- Preuve réelle à faire : une app protégée ouverte depuis un téléphone en 4G à travers le relais ; une règle OWASP qui bloque `?id=1' OR '1'='1` ; un ban de scan ; la mémoire mesurée sur une box ARM.

## Laissé au constructeur

Les noms de routes, de fichiers et de champs ; le texte exact de la page d'entrée (dans les langues du dashboard) ; la forme des écrans dans le style existant ; l'ordre de livraison en plusieurs PR (par exemple code d'accès d'abord, puis listes/pays/limites/bans, puis OWASP) ; les valeurs par défaut exactes des seuils, dans l'esprit de celles proposées.

## Réponse du propriétaire après relecture

- **Navigateur seulement** (propriétaire, 2026-11-12) : une app protégée par code ne marche que dans un navigateur. Les applications mobiles, de télévision et de synchronisation (Jellyfin, Nextcloud, Home Assistant, Immich, WebDAV) reçoivent un **401** ; il n'y a pas de second moyen d'entrer dans cette étape. Le dashboard le dit **au moment de passer une app en « protégée »** (« Les applications mobiles et de synchronisation ne pourront plus s'y connecter : seul un navigateur passe »). Une app qui doit servir ces applications reste **ouverte**, protégée par les autres filtres.
