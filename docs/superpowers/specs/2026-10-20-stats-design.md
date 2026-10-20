# Statistiques des conteneurs, des machines virtuelles et de la machine (direct, 7 jours, alertes)

Date: 2026-10-20. Statut: conception à relire, rien n'est construit.

## Pourquoi

Le propriétaire veut voir, pour chaque conteneur (apps Docker), chaque machine virtuelle (module `virt`) et la machine elle-même, le processeur, la mémoire, le réseau et le disque, **en direct et sur 7 jours**, avec des graphiques **détaillés, dans le style de Netdata** (il a rejeté trois premières maquettes jugées trop simples), et **être prévenu dans le tableau de bord** quand quelque chose ne va pas. Le moment qu'il décrit : repérer lequel consomme trop, et savoir ce qui s'est passé hier.

## Ce qui existe déjà (relevé du code, 2026-10-20)

- La machine : `GET /v1/sys/utilization` du cœur, et un envoi toutes les 5 s par MessageBus (`casaos:system:utilization`) lu par le tableau de bord (widgets Cpu, Network, Disks). Aucun historique côté serveur ; le graphique réseau garde 60 points en mémoire du navigateur.
- Les conteneurs : `GET /v2/app_management/compose/{id}/containers/stats` dans `CasaOS-AppManagement` (processeur % d'un cœur, mémoire utilisée et limite, pour les conteneurs qui tournent), lu toutes les 5 s par l'onglet Conteneurs, affiché en texte. Il ouvre un flux Docker par conteneur à chaque appel (deux images, environ une seconde). Le client Docker (`github.com/docker/docker/client`) décode déjà réseau, disque, processus ; ils ne sont pas lus.
- Les VM : le module `virt` (go-libvirt) ne lit **aucune** statistique d'exécution aujourd'hui. libvirt sait donner le temps processeur, la mémoire, les compteurs disque et réseau par domaine (noms d'appels à vérifier dans go-libvirt, voir plus bas).
- Aucun magasin de séries temporelles dans aucun dépôt ; SQLite est disponible dans le cœur ; les services gardent leur état sous `/var/lib/casaos/`.
- Le tableau de bord a déjà apexcharts 4 (graphique réseau) et un patron « fenêtre chargée à la demande » (page des VM, lancée comme une application système).

## Ce que ça fait

### La page « Statistiques »

Une fenêtre du tableau de bord, ouverte depuis une icône d'application système (comme Fichiers et Machines virtuelles), chargée à la demande. Maquette validée par le propriétaire : `docs/superpowers/mockups/stats-netdata-style.html` (à copier dans le dépôt avec ce document ; fichier d'origine : `brainstorm-stats/.../content/netdata-style.html`).

- **Colonne de gauche** : trois groupes, Conteneurs, Machines virtuelles, Machine. Chaque ligne : nom, pastille d'état, processeur actuel. Un clic choisit l'élément.
- **En-tête** : nom, type, depuis quand il tourne, image ou description ; choix de la période : Direct, 15 min, 1 h, 24 h, 7 jours.
- **Six chiffres du moment** : processeur, mémoire, réseau reçu, réseau envoyé, disque, processus (pour une VM : processeurs virtuels à la place des processus).
- **Graphiques** (aires, détaillés) :
  - Processeur : empilé utilisateur / système / attente disque ; ligne « bridé » (throttled) pour un conteneur ; pour une VM le temps processeur total (libvirt ne sépare pas utilisateur et système du côté invité) et le nombre de processeurs virtuels.
  - Mémoire : applicatif (RSS), cache fichiers, swap ; ligne de la **limite** ; pour une VM : mémoire attribuée, et mémoire utilisée par l'invité **seulement si** le pilote « balloon » de l'invité la rapporte (sinon la courbe n'existe pas et une note le dit).
  - Réseau : reçu et envoyé en octets par seconde, paquets perdus ; une courbe par interface (conteneur : toutes ses interfaces ; VM : par carte réseau).
  - Disque : lecture et écriture en octets par seconde, opérations par seconde ; pour une VM par disque ; pour la machine aussi l'espace libre des systèmes de fichiers.
  - Processus et descripteurs ouverts (conteneurs).
  - Redémarrages et santé (conteneurs : compteur de redémarrages, état de santé s'il existe ; VM : changements d'état).
- **Interactions** (exigences, comme dans la maquette) : le survol met un trait au même instant sur **tous** les graphiques et une infobulle liste les valeurs de chaque série ; on sélectionne une plage sur un graphique pour zoomer ; on clique une série de la légende pour la masquer ; le choix Direct se rafraîchit toutes les 5 s tant que l'onglet est visible et se met en pause quand il est caché.
- **Alertes** : un bandeau en haut de la page liste les alertes en cours de l'élément choisi ; la cloche du tableau de bord reçoit les notifications (voir plus bas).
- **Accessibilité** : chaque graphique a une alternative texte (tableau des dernières valeurs et min / max / moyenne de la période), les couleurs ne portent pas seules l'information (séries nommées dans la légende), tout se fait au clavier.
- **Texte** : clés anglaises, valeurs françaises, comme le reste du tableau de bord ; toute chaîne qui vient d'un service (nom de conteneur, nom de VM) est du TEXTE, jamais du HTML.

### Les mesures

Chaque service mesure **ses** éléments et garde **sa** propre histoire ; la page interroge les trois et fusionne.

- **Conteneurs** : `CasaOS-AppManagement`. Un seul échantillonneur en arrière-plan (pas un flux Docker par appel) : un flux Docker ouvert par conteneur qui tourne, ou la liste puis les statistiques, au choix du constructeur, avec un coût mesuré. Il lit processeur, mémoire, réseau, disque, processus, redémarrages, santé.
- **VM** : le module `virt`. Un échantillonneur sur la connexion libvirt existante ; un seul appel pour toutes les VM (`ConnectGetAllDomainStats` ou équivalent, à vérifier dans go-libvirt) plutôt qu'un appel par VM. Le pourcentage de processeur vient de la différence de temps processeur entre deux échantillons.
- **Machine** : le cœur. Il envoie déjà processeur, mémoire et réseau toutes les 5 s ; il garde en plus l'histoire de ces trois et de l'espace disque (nouveau, car `utilization` ne contient pas le disque).
- **Cadence** : une mesure toutes les 5 s pour le direct, gardée en mémoire (15 minutes). Pour l'historique : une **moyenne par minute** (et le maximum de la minute pour le processeur et la mémoire, afin qu'un pic bref ne disparaisse pas), gardée **7 jours**.
- **Stockage** : sur le disque de la machine, sous `/var/lib/casaos/stats/<service>/`, **un fichier en anneau de taille fixe par élément** (10 080 minutes), écrit en continu, qui **survit à un redémarrage**. Ordre de grandeur : de l'ordre de 0,5 à 1 Mo par élément. L'espace total est borné par construction ; la suppression d'un conteneur ou d'une VM garde son historique 7 jours puis le supprime. Le format est au choix du constructeur (le propriétaire n'a pas de préférence), à condition d'être borné, atomique en cas d'arrêt brutal (on perd au pire la dernière minute) et lisible sans verrou.
- **Identité** : un conteneur est identifié par son application et son service (pas par l'identifiant Docker, qui change à chaque recréation), une VM par son UUID libvirt, la machine par `host`.
- **Budget** : l'échantillonnage au repos doit rester sous 1 % d'un cœur avec 30 conteneurs et 10 VM ; à mesurer et à rapporter.

### Les routes

Chaque service ajoute des routes sous son préfixe, derrière le jeton déjà exigé (aucune route publique) :

- Liste : `GET .../stats` → les éléments connus (id, nom, type, état, processeur actuel, mémoire actuelle) pour la colonne de gauche.
- Série : `GET .../stats/{id}?range=live|15m|1h|24h|7d` → pour chaque graphique, les séries avec leurs points (horodatage, valeur) et, pour les périodes de plus d'une heure, minimum / moyenne / maximum par point. Le contrat exact est écrit par le constructeur du premier service et suivi par les autres.
- Alertes : `GET .../stats/{id}/alerts` et `PUT .../stats/{id}/alerts` (lire et régler les seuils de cet élément).

Préfixes : `/v2/app_management/stats`, `/v2/virt_management/stats`, `/v1/sys/stats` (cœur).

### Les alertes

Le cœur a déjà un **hub d'alertes** (`pkg/alerts`, conception `2026-09-25-push-alerts-design.md`, livré) : canaux au format Shoutrrr réglés par le propriétaire, catégories, anti-répétition de 6 heures, message « résolu », et les événements des conteneurs (`app:container-died`, `app:container-unhealthy`, `app:container-restarting`, `app:container-healthy`). **Les alertes de ce projet s'appuient sur ce hub ; aucun deuxième circuit de notification n'est construit.**

- **Déjà couvert, rien à construire pour les conteneurs** : un conteneur qui meurt, redémarre en boucle ou devient malsain (événements ci-dessus, catégorie « apps »).
- **Nouveau** :
  - processeur ≥ 90 % de l'allocation pendant 10 minutes de suite ;
  - mémoire ≥ 90 % de la limite (ou de la mémoire attribuée à la VM) pendant 5 minutes de suite ;
  - une VM qui **plante** ou s'arrête en échec (événement de cycle de vie de libvirt : raison « crashed » ou « failed » ; un arrêt demandé, par l'interface ou en console, n'est pas une alerte) ;
  - l'espace libre d'un système de fichiers de la machine sous 10 % : **déjà couvert** par la catégorie « disks » du hub (`disk_threshold`) ; on ne l'ajoute pas.
  Ces alertes forment une nouvelle catégorie du hub, `resources` (« ressources »), activée par défaut comme les autres ; sans canal réglé, rien n'est envoyé hors du tableau de bord.
- **Dans le tableau de bord** (décision du propriétaire : « tableau de bord pour l'instant ») : la page Statistiques montre en bandeau les alertes en cours de l'élément choisi, et chaque alerte donne une notification dans la cloche du tableau de bord via MessageBus (comme le font déjà les autres notifications). Les canaux téléphone et courriel sont ceux que le propriétaire règle déjà dans le hub ; ils recevront aussi la catégorie `resources` quand il en aura réglé un.
- **Clé et anti-répétition** : `resource:<type>:<id>:<situation>` ; une seule alerte à l'arrivée, une seule « résolue » quand la situation cesse ; le hub applique son anti-répétition.
- **Seuils** : réglables **par élément** depuis la page (et désactivables) ; valeurs d'office ci-dessus ; fichier de réglage séparé de l'historique (voir « Décisions de conception »).
- **Qui évalue** : le service qui mesure l'élément, à chaque échantillon d'une minute, sur les moyennes par minute (une alerte ne réagit pas à un pic de quelques secondes). Il envoie l'alerte au hub du cœur par MessageBus, comme AppManagement le fait déjà pour les événements de conteneurs.

## Hors périmètre (non-objectifs)

- Aucune notification hors du tableau de bord (courriel, téléphone, webhook) : pour plus tard, à ne pas empêcher.
- Pas d'export des données, pas de point d'accès Prometheus, pas de plusieurs machines.
- Pas de détail par processus à l'intérieur d'un conteneur ou d'une VM.
- Pas de remplacement des widgets existants de l'écran d'accueil (Processeur, Réseau, Disques) : ils restent tels quels.
- Pas d'historique de plus de 7 jours.

## Ce qui ferait échouer le projet même s'il « marche »

- Un échantillonneur qui consomme visiblement du processeur ou qui ouvre une rafale de connexions à Docker ou libvirt.
- Un historique qui grossit sans borne ou qui se perd à chaque redémarrage.
- Un graphique qui ment : un pic qui disparaît dans la moyenne, une courbe de mémoire de VM affichée alors que la donnée n'existe pas (ballon absent), un pourcentage de processeur calculé sur le mauvais dénominateur.
- Des alertes qui se répètent en boucle ou qui crient pour rien au démarrage d'un conteneur.

## Contraintes

- Chaque service reste séparé : le tableau de bord ne lit jamais Docker ni libvirt, uniquement les routes des services, via la passerelle.
- Les routes sont derrière le jeton comme toutes les autres ; les sockets de Docker et de libvirt sont lus par les services qui y ont déjà accès.
- Le module `virt` reste facultatif : sans lui, la colonne « Machines virtuelles » n'apparaît pas et aucun appel n'est fait. La page se charge seulement quand on l'ouvre.
- Anglais pour les clés, français pour les valeurs ; aucun texte venant d'un service n'est interprété comme du HTML.
- Test et preuves comme le reste du projet (tests unitaires, tests de mutation des gardes, preuve sur machine virtuelle réelle pour le module `virt` et pour la tenue des 7 jours après redémarrage).

## Laissé au constructeur

- Le format exact du fichier en anneau et la manière d'écrire sans verrou ; le détail du contrat de série (noms des champs).
- Un flux Docker par conteneur ou liste + statistiques ; le nom exact de l'appel go-libvirt (`ConnectGetAllDomainStats`, `DomainGetInfo`, `DomainBlockStats`, `DomainInterfaceStats`, `DomainMemoryStats` sont des noms à **vérifier** dans le code de go-libvirt ; ils n'ont pas été lus) et si le pilote de test `test:///default` les gère (**non vérifié** ; sinon les tests de VM utilisent un faux libvirt).
- La disposition exacte et les couleurs, tant que la maquette est respectée dans l'esprit (dense, plusieurs graphiques détaillés synchronisés, survol commun).
- L'ordre de construction : le contrat et le service des conteneurs d'abord (le plus simple, déjà de la matière), la page, puis les VM, la machine, les alertes.

## Décisions de conception (ajoutées après relecture ; ce sont mes choix, à contester si besoin)

1. **Accès et fusion** : les trois services sont déjà derrière la passerelle (`/v2/app_management`, `/v2/virt_management`, et les routes `/v1/sys` du cœur) avec le jeton ; **aucun travail de passerelle**. La page fait les trois appels en parallèle dans le navigateur et fusionne (pas d'agrégateur). Un service qui ne répond pas ou qui est absent (`virt` non installé) donne un état d'erreur **pour sa colonne seulement**, pas pour toute la page.
2. **Contenu d'une minute d'historique** : **moyenne, minimum et maximum pour toutes les séries** (pas seulement processeur et mémoire). Pour 24 h et 7 jours, le service renvoie au plus environ 350 points : seaux de 5 minutes pour 24 h, de 30 minutes pour 7 jours (moyenne des moyennes, minimum des minimums, maximum des maximums).
3. **« Devait tourner » (arrêt à signaler)** : on n'enregistre pas d'intention. Conteneurs : le hub gère déjà la mort inattendue (événements Docker). VM : on lit l'événement de cycle de vie de libvirt et on n'alerte que pour une raison de plantage ou d'échec ; un arrêt demandé n'alerte pas. Les chemins d'arrêt et de démarrage des services ne sont pas touchés.
4. **Réglages et état des alertes** : fichier JSON **séparé** à côté du fichier en anneau (seuils, activé ou non, alertes en cours), écrit de façon atomique. Les compteurs « 10 minutes de suite » sont **reconstruits depuis l'historique gardé** au démarrage, pour qu'un redémarrage ne crée ni oubli ni fausse alerte.
5. **Périmètre de la machine** : systèmes de fichiers et interfaces réseau **comme les widgets existants** (cartes réseau physiques comme `utilization`, disques et stockages que le widget Disques liste, via LocalStorage) ; on exclut les systèmes pseudo, tmpfs, overlay de Docker, loop et squashfs. Le détail est laissé au constructeur dans ce cadre.
6. **Identité d'un conteneur** : application + service + numéro de réplique (étiquette `com.docker.compose.container-number`). **Seules les applications gérées par AppManagement (compose)** sont dans le périmètre ; les conteneurs hors compose ou système ne sont pas listés.
7. **Dénominateur du processeur** : les graphiques sont en **% d'un cœur** (jusqu'à N × 100 %, comme Docker et Netdata), avec une ligne « limite » quand une limite de processeur existe ; **l'alerte** se mesure en % de l'**allocation effective** : la limite du conteneur si elle existe, sinon le nombre de cœurs de la machine ; pour une VM, le nombre de processeurs virtuels. La page dit l'échelle sous chaque graphique.
8. **Maquette** : copiée dans le dépôt avec ce document, `docs/superpowers/mockups/stats-netdata-style.html` (les données y sont inventées).
9. Les chiffres de la colonne de gauche se rafraîchissent toutes les 5 s (pause quand l'onglet est caché) ; les réglages d'alertes n'ont pas d'autorisation en plus du jeton.
