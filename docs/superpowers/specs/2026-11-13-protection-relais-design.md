# Partage public : protéger les apps partagées au niveau du relais (conception)

## Pourquoi

La protection côté box (spec `2026-11-12-protection-partage-design.md`, publiée dans v0.5.138) bloque les attaques **dans le Gateway de la box** : le trafic a déjà traversé le relais et la connexion du foyer. Le relais, lui, voit chaque visiteur avant le tunnel. Il peut couper les adresses connues comme malveillantes **avant** qu'elles n'atteignent une box : moins de charge pour les box, moins de bande passante pour les foyers, et une protection commune nourrie par toutes les box qui le veulent.

Le relais transmet le TLS sans le déchiffrer (passage SNI) : il ne connaît d'un visiteur que **son adresse IP et le nom demandé**. Tout ce document filtre sur ces deux informations.

Ce document est le « projet 2 » annoncé dans la spec côté box. Il touche le relais (`ReCasaOS-Relay`), le cœur (`CasaOS`) et le dashboard (`CasaOS-UI`).

## Décisions du propriétaire (2026-11-13)

- **Filtrage du relais par box, activé par défaut** pour une box à jour, avec un interrupteur pour le couper (propriétaire, sur ma proposition de distinguer recevoir et envoyer).
- **Partage des bans de la box vers le relais : sur accord actif du propriétaire de la box, jamais pré-coché**, mais **incitatif** (propriétaire).
- **Donnant-donnant** : la liste communautaire ReCasaOS ne protège que les box qui partagent elles-mêmes leurs bans ; les listes externes protègent toutes les box à jour (propriétaire).
- **Liste communautaire « plus stricte »** que ma première proposition (propriétaire) :
  - une adresse y entre quand **2 flottes différentes** l'ont bannie en **24 heures** ;
  - **récidive** : 24 h, puis 7 jours si elle revient dans les 30 jours, puis **ban définitif** à la 3ᵉ fois, jusqu'à ce que l'exploitant le lève ;
  - **plages** : bannir la plage quand plusieurs adresses d'une même plage sont bannies ; **ASN** et **pays** quand il le faut (propriétaire : « voir range/asn/pays si besoin »).
- **Listes externes** : AbuseIPDB et CrowdSec avec la clé de l'exploitant, **et toute autre liste** du genre de celles recensées dans `hslatman/awesome-threat-intelligence`, ajoutable depuis `/admin` (propriétaire).
- **Tout se gère depuis `/admin`** (propriétaire, voir plus bas).
- **Approche A** : tout dans le code du relais, l'état dans `enrolment`, le filtrage dans `share` (propriétaire, sur ma recommandation ; CrowdSec comme moteur et le pare-feu de l'hôte écartés, voir « Approches »).

## Ce qui existe déjà (relevé du code, 2026-11-13)

- **Relais** (`ReCasaOS-Relay`, `main` 631bada) :
  - `cmd/share` reçoit chaque connexion sur 80/443, lit le nom demandé (SNI), l'envoie à `front` (Caddy) ou dans le tunnel de la box. Il plafonne déjà les connexions par IP et par IP et box (`CONNS_PER_IP`, `CONNS_PER_IP_PER_BOX`, Relay#11) et ne compte pas les adresses privées.
  - `enrolment` (Go, SQLite) garde les flottes, les clés de box (`box_keys`: `node_id`, `fleet_id`, `box_key`), les noms partagés (`share_names`), les noms interdits (`share_banned`) et sert `/admin` (jeton `ADMIN_TOKEN`). `share` l'interroge par une API interne (`/internal/share/...`, réseau compose seulement).
  - Une box appelle les routes `/v1/share/*` en **signant avec sa propre clé** (`X-Box-Key` + signature, `boxAuth` dans `internal/server/share.go`) ; le relais connaît donc la **flotte** de chaque box.
  - `/admin` sait déjà interdire un nom (le libérer et le réserver : `POST /admin/share/banned`, `…/names/{name}/revoke`) et lever l'interdiction.
- **Box** (v0.5.138) : le Gateway tient des bans typés (`waf`, `scan`, `rate`, `code`) avec l'app et l'adresse (`GET /v1/gateway/guard/bans`) ; le cœur pousse la configuration et sert `/v1/share/guard` ; le dashboard a la section Protection, le journal et « Needs attention ». L'adresse d'un visiteur vient de l'en-tête PROXY v2 **posé par le relais** : une box ne peut signaler qu'une adresse qui l'a réellement contactée par le relais.

## Approches

- **A. Tout dans le code du relais** (retenue). `enrolment` garde l'état (listes, signalements, liste communautaire, bans manuels, liste blanche, réglages par nom) et sert `/admin` ; `share` reprend un instantané compilé et filtre en mémoire. Un seul langage, le modèle déjà en place pour les noms interdits et les plafonds, rien de plus à installer, et le donnant-donnant par box s'exprime simplement. Coût : écrire nous-mêmes seuils, récidive et expirations.
- **B. CrowdSec comme moteur** (écartée) : un service de plus sur chaque relais auto-hébergé ; CrowdSec décide par adresse, pas par box, donc le donnant-donnant s'y exprime mal ; `/admin` devrait passer par son API.
- **C. Pare-feu de l'hôte (nftables)** (écartée) : coupe avant la connexion, mais sans connaître le nom demandé, donc sans réglage par box ; il faudrait A en plus, et chaque hébergeur gère son pare-feu à sa façon.

## Ce que ça fait

### Les sources d'adresses à couper

1. **Listes externes**, téléchargées par `enrolment` :
   - **Un mécanisme générique de liste** : nom, adresse de téléchargement, en-tête d'authentification facultatif (pour les listes à clé), format, fréquence (au plus une fois par heure), nombre maximal d'adresses, interrupteur, note libre (licence).
   - **Formats** : texte, une adresse ou une plage par ligne, commentaires `#` et `;` ignorés (la grande majorité des listes : Spamhaus DROP/EDROP, FireHOL, blocklist.de, Emerging Threats, CINS, nœuds de sortie Tor…) ; CSV avec le numéro de la colonne qui porte l'adresse. Le JSON et les formats exotiques plus tard si le besoin vient.
   - **AbuseIPDB** et **CrowdSec** sont des préréglages de ce mécanisme, avec un champ pour la clé (mon choix : ce sont des téléchargements de listes avec un en-tête d'authentification, pas besoin de connecteurs à part) :
     - AbuseIPDB : `GET https://api.abuseipdb.com/api/v2/blacklist` avec `Key: <clé>` et `Accept: text/plain`, `confidenceMinimum=90` quand l'abonnement le permet ; l'offre gratuite n'accepte pas ce paramètre et renvoie les adresses à 100 % (au plus 10 000, 5 téléchargements par jour) : le préréglage gratuit télécharge 4 fois par jour au plus ;
     - CrowdSec : la liste de blocage d'une « intégration » de la console CrowdSec (URL et identifiants fournis par la console) ; le constructeur vérifie le format exact et l'authentification.
   - **Préréglages proposés en un clic, tous coupés par défaut** : FireHOL niveau 1, Spamhaus DROP, blocklist.de (toutes catégories), Emerging Threats « compromised », nœuds de sortie Tor (bloquer coupe aussi les visiteurs anonymes : à l'exploitant de décider).
   - **Échec de téléchargement** : la dernière copie bonne reste en service, `/admin` le signale. **Liste au-delà de son maximum** : refusée entière, jamais tronquée.
   - **Licences** : l'exploitant choisit les listes et en respecte les conditions ; le relais les utilise pour son propre filtrage et ne les redistribue pas aux box.
2. **Liste communautaire ReCasaOS**, calculée par `enrolment` à partir des bans que les box partagent (voir plus bas).
3. **Bans manuels de l'exploitant**, depuis `/admin` : adresse, plage, ASN, pays.
4. **Liste blanche du relais** : adresses et plages, chacune avec une note. Elle l'emporte toujours.

### Pays et réseaux sur le relais

Pour les bans par pays et pour repérer les ASN, `enrolment` télécharge lui aussi **DB-IP Lite** (pays et ASN, MMDB, CC BY 4.0, une fois par mois, remplacement par renommage), comme le cœur de la box (mon choix : même source, même licence). Le lien « IP Geolocation by DB-IP » figure sur les pages de `/admin` qui affichent un pays ou un réseau.

`share` n'a pas DB-IP : `enrolment` traduit les bans de pays et d'ASN en plages dans l'instantané et les retraduit à chaque mise à jour de DB-IP (mon choix).

### À qui s'applique quoi

| Source | S'applique à |
|---|---|
| Liste blanche du relais | tout le monde (jamais coupé) |
| Plafonds de connexions (existant) | tout le monde |
| Bans manuels de l'exploitant (adresse, plage, ASN, pays) | **tous les noms partagés**, y compris les box pas à jour (mon choix : c'est une décision de l'exploitant du relais, comme l'interdiction d'un nom), **quel que soit l'interrupteur de filtrage** de la box |
| Listes externes | les box dont le **filtrage par le relais** est activé |
| Liste communautaire | les box dont le filtrage est activé **et** qui **partagent leurs bans** |

- **Une box pas encore à jour** (cœur sans ces réglages) n'est filtrée que par la liste blanche, les plafonds et les bans manuels : elle n'a pas d'interrupteur pour couper le reste (propriétaire : seules les box à jour sont filtrées par défaut).
- **Un nom suspendu** par l'exploitant : toutes ses connexions sont refusées, sans libérer le nom (différent de l'interdiction existante, qui libère et réserve le nom) (mon choix : suspension réversible).

### Ce que fait `share` d'une connexion

Ordre (mon choix) :
1. adresse dans la **liste blanche** → passe (seuls les plafonds s'appliquent) ;
2. **plafonds de connexions** (existant) ;
3. adresse, plage, ASN ou pays **banni à la main** → fermée **dès l'acceptation**, avant de lire quoi que ce soit ;
4. lecture du nom demandé (SNI, existant) ; nom **suspendu** → fermée ;
5. si le filtrage de cette box est activé : adresse dans une **liste externe** → fermée ;
6. si cette box partage aussi ses bans : adresse ou plage dans la **liste communautaire** → fermée ;
7. sinon, la connexion entre dans le tunnel (existant).

- Une connexion refusée est **fermée sans réponse** : le relais ne peut pas afficher de page, le certificat est sur la box (contrainte).
- Chaque refus est **compté** par nom, raison et source (quelle liste), par adresse et par heure, gardé 7 jours (mon choix : de quoi alimenter `/admin` et le journal de la box, pas plus).
- `share` reprend un **instantané compilé** de toutes les sources auprès d'`enrolment` toutes les 30 secondes (seulement s'il a changé) et le garde en mémoire ; la recherche d'une adresse est en temps logarithmique (plages triées par famille). Sans instantané (démarrage, `enrolment` injoignable), `share` garde le dernier reçu ; au tout premier démarrage sans instantané, il laisse passer et le journalise (mon choix : un relais qui démarre ne doit pas couper toutes les apps partagées).

### Réglages d'une box au relais

- Deux réglages par box (par `node_id`), poussés par le cœur de la box, signés par sa clé : **filtrage par le relais** (oui/non) et **partage des bans** (oui/non, avec la date de l'accord).
- **Absence de réglage** (box pas à jour) : filtrage non, partage non.
- Le cœur à jour pousse ces réglages au démarrage, à chaque changement et à chaque resync ; sa valeur par défaut est **filtrage oui, partage non**.

### Partage des bans et liste communautaire

- **Ce que la box envoie**, seulement si le partage est activé : pour chaque ban **démarré** par son Gateway, l'adresse (clé : IPv4 ou /64 IPv6), la raison typée (`waf`, `scan`, `rate`, `code`), l'app, l'heure. Rien d'autre : ni chemin, ni en-tête, ni pays (mon choix).
- **Comment** : le cœur de chaque box (pas seulement le master) lit les bans de son Gateway une fois par minute et envoie les nouveaux par lots, signés par sa clé de box ; le relais en tire la flotte.
- **Bans manuels de la box** et bans pour une adresse que la box a mise en liste blanche : jamais envoyés (mon choix).
- **Entrée dans la liste communautaire** (propriétaire) : une adresse signalée par **2 flottes différentes en 24 heures**.
- **Durée et récidive** (propriétaire) : 1ʳᵉ fois 24 h ; 2ᵉ fois dans les 30 jours 7 jours ; 3ᵉ fois **définitif**, jusqu'à ce que l'exploitant le lève dans `/admin`. Un compteur de récidive par adresse est gardé 30 jours après la fin du dernier ban, sauf pour les bans définitifs.
- **Plages, automatique** (propriétaire, seuils de mon choix) : quand **5 adresses** d'une même plage (/24 en IPv4, /48 en IPv6) sont dans la liste communautaire en même temps, la plage entière est bannie 24 h, avec les mêmes paliers de récidive.
- **ASN, proposé seulement** (propriétaire « si besoin », règle de mon choix) : quand au moins 10 plages d'un même ASN sont bannies en même temps, `/admin` le signale avec un bouton « Bannir ce réseau » ; jamais automatiquement.
- **Pays : manuel uniquement**, dans `/admin`.
- **Garde-fous** : la liste blanche du relais l'emporte toujours (ni l'adresse ni sa plage) ; une adresse privée ou réservée n'est jamais retenue ; les signalements d'une flotte ne comptent qu'une fois par adresse et par 24 h.
- **Contre l'abus** (propriétaire, 2026-11-13 : n'importe qui peut créer une flotte sur le relais, une seule personne avec deux flottes pourrait donc faire bannir n'importe qui) — un signalement ne compte que si **les deux** conditions sont vraies :
  1. l'adresse a réellement contacté **cette box par le relais** dans les 24 h avant le début du ban : `share` note chaque connexion acceptée (clé d'adresse, box) et `enrolment` la garde **24 h**, puis la supprime ;
  2. la flotte de la box a **au moins 7 jours** et **au moins un nom partagé**.
  Un signalement qui ne remplit pas ces conditions est ignoré sans erreur (la box n'a pas à le savoir).
- **Données personnelles** : connexions acceptées (adresse, box) gardées **24 h** ; signalements bruts gardés **30 jours** puis supprimés ; bans communautaires supprimés à leur fin ; bans définitifs gardés jusqu'à leur levée, avec date et raisons, visibles dans une liste à part dans `/admin` (adresses de particuliers qui changent de main : à revoir).

#### Règles précises (mon choix, après relecture)

- **Clé** : l'adresse IPv4 exacte ; en IPv6, le /64, partout dans la liste communautaire (signalement, comptage, filtrage). Plage automatique : 5 clés distinctes dans un même /24 (IPv4) ou /48 (IPv6).
- **Une « fois »** = un nouveau ban communautaire, ouvert quand le seuil (2 flottes sur 24 h glissantes) est atteint alors qu'aucun ban n'est en cours pour cette clé. Un signalement pendant un ban en cours ne le prolonge ni ne l'aggrave. **Récidive** : le nouveau ban commence moins de 30 jours après la **fin** du précédent ; sinon on repart à 24 h.
- **Plages** : un ban de plage a son propre compteur de récidive et les mêmes paliers (3ᵉ fois définitif) ; il dure tout son palier même si les adresses qui l'ont déclenché expirent ; les bans de ces adresses continuent leur propre vie.
- **Retrait de l'accord** : la box cesse d'envoyer ; le relais supprime ses signalements (ils ne comptent plus pour de nouveaux bans) ; les bans communautaires déjà ouverts vont à leur terme. Les signalements sont donc rangés par box.
- **Doublons** : le cœur retient ce qu'il a déjà envoyé (clé, raison, début du ban) ; le relais ignore un signalement identique de la même box.
- **Contribution** d'une flotte : les bans communautaires ouverts ce mois-ci dont un des signalements déclencheurs vient d'elle.

### Incitation à partager (propriétaire : « incitatif »)

Sans jamais pré-cocher ni retirer de fonction à ceux qui refusent :
- **Donnant-donnant** : seules les box qui partagent reçoivent la liste communautaire.
- **Une carte « Rejoindre la protection communautaire »** dans la section Protection tant que le partage est coupé : ce qui est envoyé en une phrase (l'adresse, la raison, l'app, rien d'autre), ce qu'on y gagne avec un chiffre (« N adresses déjà bloquées par la communauté »), un bouton qui ouvre l'accord (texte complet, case à cocher non pré-cochée, bouton « J'accepte »).
- **Un rappel après une vague de bans** sur une app de la box (l'alerte existante) : « Ces adresses auraient pu être coupées au relais avant d'atteindre votre box. » avec le même bouton.
- **Pour ceux qui partagent** : « Vos signalements ont fait entrer N adresses dans la liste communautaire ce mois-ci » (mon choix de mesure : nombre de bans communautaires auxquels la flotte a contribué ; plus simple et honnête qu'un nombre de box protégées).

### `/admin` (propriétaire : tout s'y gère)

Dans le style des pages actuelles du relais :
- **Vue d'ensemble** : connexions refusées aujourd'hui par raison (liste externe, liste communautaire, ban manuel, nom suspendu, plafond de connexions), graphique sur 7 jours.
- **Listes externes** : chaque liste (nom, source, dernière mise à jour réussie, nombre d'adresses, erreur éventuelle, interrupteur, « Mettre à jour maintenant », modifier, supprimer) ; « Ajouter une liste » (formulaire du mécanisme générique) et les préréglages en un clic ; clés masquées.
- **Liste communautaire** : bans en cours (adresse ou plage, raisons, nombre de flottes, depuis, jusqu'à, palier) avec « Lever » et « Mettre en liste blanche » ; **bans définitifs** à part ; **ASN signalés** avec « Bannir ce réseau » ; nombre de box qui partagent.
- **Bans manuels** : adresses, plages, ASN, pays (avec durée ou définitif, et une note).
- **Par nom partagé** : refus pour ce nom (raisons, sources), ses réglages (filtrage, partage), « Suspendre » / « Reprendre », et l'interdiction existante.
- **Liste blanche du relais** : adresses et plages avec note.
- **Plafonds de connexions** : refus par adresse et par nom, valeurs en vigueur.

### Côté box

- **Cœur** : les deux réglages dans l'état de protection (`share-guard.json`) avec la date de l'accord ; poussés au relais ; envoi des bans démarrés ; lecture au relais, pour le dashboard, du nombre d'adresses de la liste communautaire, de la contribution de la flotte et des refus du relais pour ce nom (7 jours).
- **Dashboard** :
  - dans la section Protection, l'interrupteur « Filtrage par le relais » (activé) et la carte « Rejoindre la protection communautaire » ou, une fois accepté, l'interrupteur « Partager mes bans » et la ligne de contribution ;
  - le journal montre aussi les **refus du relais** pour les noms de cette box, quelle que soit la source, y compris ban manuel et nom suspendu (raison « Coupé au relais » et la source) ; une box ne voit jamais les refus d'un autre nom (mon choix : le propriétaire doit pouvoir voir ce que l'exploitant coupe chez lui) ;
  - le rappel après une vague de bans.
- Une box dont le **relais est trop ancien** (404 sur les nouvelles routes) : le dashboard cache ces réglages, comme pour un cœur trop ancien. Une erreur réseau ou un 5xx n'est pas un relais trop ancien : le cœur réessaie à la resync suivante et garde le dernier état connu ; tant qu'il n'a rien poussé, le relais applique l'absence de réglage (filtrage non, partage non) (mon choix).

## Hors de ce document

- Déchiffrer au relais, CAPTCHA, défis.
- Des listes propres à une box sur le relais (une box a déjà ses listes, dans son Gateway).
- Le bannissement automatique d'un ASN ou d'un pays.
- La redistribution des listes externes aux box.
- Les formats JSON et exotiques de listes externes (plus tard).

## Ce qui serait un échec même si tout marche

- Les visiteurs légitimes d'une box coupés sans que son propriétaire puisse le voir ou l'empêcher.
- Un seul propriétaire capable de faire bannir quelqu'un pour toutes les box.
- Des adresses IP de visiteurs gardées sans limite de temps, hors bans définitifs revus dans `/admin`.
- Un relais auto-hébergé qui, sans configuration, se met à couper du trafic (sans clé ni liste activée, seules la liste blanche, les plafonds et les bans manuels s'appliquent).
- Un relais dont le démarrage coupe toutes les apps partagées.

## Risques

- **Faux positifs des listes externes** (IP d'opérateurs mobiles partagées) : seuils prudents, listes coupables une à une, liste blanche, journal des refus visible par la box.
- **Bans définitifs d'adresses dynamiques** : liste à part dans `/admin`, avec date et raisons.
- **Mémoire de `share`** : plusieurs listes de centaines de milliers d'entrées ; le constructeur mesure la mémoire avec 500 000 entrées et fixe un plafond total. Une liste externe qui le ferait dépasser est refusée entière, comme pour son propre maximum, et `/admin` le dit ; la liste communautaire et les bans manuels ne sont jamais écartés pour ce plafond (mon choix).
- **Charge d'`enrolment`** : téléchargements et calcul de l'instantané hors des requêtes.

## Tests et preuve

- Relais : analyse des formats (texte, CSV, commentaires, adresses invalides, plages, IPv4 et IPv6, dépassement du maximum), échec de téléchargement (dernière copie gardée), seuils et récidive de la liste communautaire (horloge simulée), plages et signalement d'ASN, ordre des filtres dans `share` pour chaque combinaison de réglages, suspension d'un nom, instantané absent ou périmé, signature des nouvelles routes, purge à 30 jours. Les tests de bout en bout existants (`e2e/`) gardent leur forme ; un test de bout en bout ajoute deux box de deux flottes qui signalent la même adresse et vérifie qu'une troisième box qui partage la voit coupée, et qu'une qui ne partage pas la laisse passer.
- Cœur : réglages, valeurs par défaut, poussée au relais, envoi des bans (seulement si partage activé, jamais les bans manuels ni les adresses en liste blanche), relais trop ancien.
- Dashboard : interrupteur, carte et accord (non pré-coché), rappel après une vague, refus du relais dans le journal, relais trop ancien.
- Preuve réelle : le relais déployé avec AbuseIPDB et une liste FireHOL, une adresse de test mise en liste par deux flottes de test, vue coupée pour une box qui partage.

## Laissé au constructeur

Les noms de routes, tables et champs ; le format exact de l'instantané entre `enrolment` et `share` ; la mise en page de `/admin` dans le style existant ; le découpage en PR (par exemple relais d'abord, puis cœur, puis dashboard) ; les textes exacts du dashboard (anglais et français) ; le format exact de la liste CrowdSec.
