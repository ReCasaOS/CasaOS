# Comptes multiples, droits par module et visibilité des apps — conception

Date : 2026-11-14, révisée le 2026-11-15. Origine : [ReCasaOS/CasaOS#150](https://github.com/ReCasaOS/CasaOS/issues/150) (« Assign Apps and Modules by User »).
Décisions du propriétaire (Gary) : visibilité d'abord (13 nov.) ; solution A, liste dans AppManagement (14 nov.) ; comptes multiples inclus, pas d'urgence (14 nov.) ; lecture seule pour `user`, appliquée au Gateway (14 nov.) ; **accès fermé par défaut pour `user`, avec des droits par module (15 nov.)**.

## Intention

Un foyer partage une box. Le propriétaire veut donner un compte aux autres personnes sans leur donner la box : elles ouvrent les apps qu'il leur laisse voir, et rien d'autre tant qu'il ne l'a pas accordé.

Les services de la box tournent avec tous les droits de la machine. Un compte qui peut lire n'importe quoi vaut donc le compte du propriétaire. La règle de cette conception en découle : **un compte `user` n'atteint que ce qui lui est accordé ; tout le reste est refusé, lectures comprises.**

## Objectifs

1. Le propriétaire crée, liste et supprime des comptes de rôle `user`.
2. Un compte `user` n'atteint, par défaut, que son propre compte et les apps visibles pour lui. Toute autre route est refusée par le Gateway.
3. Le propriétaire accorde à un compte, module par module, le droit de lire ce module (étape 3).
4. Le propriétaire choisit, app par app, les comptes pour qui l'app est cachée ; le serveur applique la règle sur tout ce qu'un compte peut atteindre, événements compris.
5. Un compte `user` ne modifie rien d'autre que son propre compte.

## Non-objectifs

- Les apps écoutent sur leur propre port et ne passent pas par le Gateway : la box peut les cacher de ses listes, pas en interdire l'accès. Le texte de l'interface dit « caché », jamais « interdit ».
- Pas de droit d'écriture pour un `user` dans cette version (démarrer ou arrêter une app, par exemple). La grille de droits est faite pour en recevoir plus tard.
- Pas de second `admin`, pas de groupes, pas d'authentification externe (OIDC, LDAP).
- Pas de fichiers pour un `user` dans cette version : le Gateway dit oui ou non à une route, il ne peut pas limiter un chemin passé en paramètre. Un dossier par compte demande des racines par compte dans le cœur ; c'est un projet à part.
- Aucun changement pour l'`admin` : il voit et fait tout, comme aujourd'hui.

## Décisions

(propriétaire) = décision de Gary ; (mon choix) = à défaire s'il n'est pas d'accord ; (à trancher) = j'ai mis une valeur par défaut, la décision lui revient.

### Comptes et rôles (inchangé)

- Deux rôles, `admin` (le premier compte) et `user`. Un seul `admin`, créé à l'installation ; il ne peut être ni supprimé ni changé de rôle. (propriétaire, mon choix pour les noms)
- UserService : créer, lister, supprimer un compte, réservés à l'`admin`. Le rôle n'est jamais lu d'une requête.
- Supprimer un compte termine ses sessions, retire ses droits et le retire des listes de visibilité.
- Au démarrage, un compte au rôle vide devient `admin`.

### Le filtre du Gateway : fermé par défaut pour `user` (propriétaire)

- Pour un compte `user`, le Gateway ne laisse passer qu'une requête inscrite dans un **tableau d'autorisations** : méthode, chemin, et le droit qui l'ouvre. Toute autre requête répond 403, GET compris. Le sens est inversé par rapport à la première version, qui refusait une liste et laissait passer le reste.
- Le tableau a trois sortes de lignes :
  - **public** : ce qui passe sans jeton (connexion, renouvellement, état, fichiers statiques de l'interface) ;
  - **base** : ce que tout compte `user` atteint (son propre compte, les routes d'AppManagement qui servent le tableau de bord, Statistics et Logs des apps, filtrées par la visibilité) ;
  - **module** : ce qu'ouvre un droit accordé (étape 3).
- Le tableau est construit à partir des appels réels de l'interface en session `user`, pas de mémoire. Un test parcourt chaque appel que fait l'interface pour un `user` et vérifie qu'il est dans le tableau ; un autre vérifie que chaque ligne correspond à une route qui existe.
- Une ligne ne s'ajoute qu'après lecture du gestionnaire : une route qui agit, ouvre un canal interactif, sort sur le réseau ou rend un secret n'entre pas dans le tableau, même en GET.
- Websockets : seuls ceux inscrits au tableau passent. Aucun terminal, aucune console de VM pour un `user`.
- **Fermé en cas de doute** : un jeton présent que le Gateway ne peut pas vérifier, sur une requête qui n'est pas publique, reçoit 503, et ne passe pas. (mon choix)
- **Le propriétaire n'est pas enfermé dehors** : une réponse « admin » d'UserService reste valable dans le Gateway sans limite de durée, puisque l'`admin` ne peut être ni supprimé ni changé. Seul un Gateway qui redémarre alors qu'UserService est arrêté ne connaît plus personne ; dans ce cas la connexion elle-même est impossible. (mon choix)
- AppManagement garde ses propres refus (défense en profondeur), y compris sur les routes sensibles que le Gateway refuse déjà.

### Droits par module (propriétaire pour le principe, mon choix pour la liste)

- Un droit est le nom d'un module ; l'accorder ouvre **la lecture** de ce module. Liste de départ : `storage` (disques et santé), `vm` (liste et état des machines virtuelles, sans console), `backups` (état et historique des sauvegardes, sans les clés), `fleet` (liste des box de la flotte, sans relais), `system` (charge et utilisation de la box ; ni le matériel ni la version de l'OS, ni journaux bruts, ni terminal).
- `apps` n'est pas un droit : c'est la base, réglée app par app par la visibilité.
- `files` n'existe pas dans cette version (voir Non-objectifs).
- Stockage : une colonne `rights` sur le compte, dans UserService. La réponse interne qui donne le rôle donne aussi les droits ; Common la met en cache comme le rôle (5 s). Un échec de lecture vaut « aucun droit ». (mon choix)
- Écran : dans « Comptes », une ligne de cases à cocher par compte. Un nouveau compte n'a aucun droit.

### Le compte d'un `user` (corrigé par les services, pas par le Gateway)

- Ce qu'un `user` peut changer sur son compte est une liste fermée de champs : nom affiché, e-mail, description, mot de passe, 2FA, préférences, image d'avatar envoyée. Aucun champ ne porte un chemin de fichier fourni par le client.
- Toute route du compte qui lit, copie ou supprime un fichier ne travaille que dans le dossier du compte, chemin nettoyé puis vérifié.
- Une entrée invalide répond par une erreur ; elle n'arrête jamais le service.
- Un `user` ne lit que sa propre fiche. La liste des comptes est réservée à l'`admin`.

### Visibilité des apps

- Stockage inchangé : `/var/lib/casaos/visibility.json`, `{ "<app>": [<id de compte>, ...] }`, modèle « caché pour ».
- **Caché = inconnu, partout** : une route propre à une app cachée répond exactement comme pour un nom qui n'existe pas (même code, même message, quel que soit l'ordre des contrôles). Un test compare les deux réponses sur chaque route d'app.
- Les listes qui ne sont pas rangées par app (réseaux, compteurs, sauvegarde en cours) omettent ce qui appartient à une app cachée.
- **Événements** : un compte `user` n'ouvre pas le bus de messages ; l'interface rafraîchit ses listes à intervalle régulier (10 s). Le bus diffuse à tous ses abonnés sans savoir qui ils sont ; le filtrer par compte est un projet à part. (mon choix)
- **Désinstallation** (propriétaire, 15 nov.) : l'entrée de visibilité n'est plus retirée à la désinstallation, car les journaux, statistiques et sauvegardes de l'app lui survivent. Elle reste tant que l'`admin` ne la retire pas ; une réinstallation du même nom reste cachée. L'écran « Comptes » montre les entrées sans app installée et permet de les retirer.
- **Secrets des apps** (propriétaire, 15 nov.) : pour un `user`, AppManagement retire les valeurs d'environnement, le contenu du `.env`, les secrets de webhook et les réglages globaux de ses réponses. Les noms des variables restent.
- Un nom d'app enregistré dans la visibilité est validé comme un nom de projet compose (minuscules), sans exiger une app connue : le propriétaire peut cacher un nom avant d'installer l'app.
- Écran : onglet « Visibilité » dans les réglages de chaque app, pour l'`admin` seul.

### Interface

- Pour un `user`, l'interface n'affiche que ce que ses droits ouvrent : pas de fichiers, pas de terminal, et un module sans droit n'a ni carte ni entrée de menu.  Stockage, flotte et sauvegardes ne s'affichent sur le nouveau tableau de bord qu'avec `system` ; la carte Flotte reste réservée au propriétaire ; la page des VM montre liste, état et IP seulement.
- Aucun contrôle d'écriture n'est affiché à un `user`, hors de son compte. Une app arrêtée s'affiche comme arrêtée, avec un texte qui dit que seul le propriétaire peut la démarrer.
- Un refus du serveur (403) donne un seul message traduit, jamais une erreur brute ni une carte bloquée.
- Le rôle et les droits sont connus avant le premier affichage (repris de la session enregistrée, puis confirmés par le serveur) ; tant qu'ils sont inconnus, l'interface se comporte comme pour un `user` sans droit, sauf pour l'écran « Comptes » de l'`admin` qui attend la confirmation.

## Livraison par étapes

Chaque étape est publiable seule, mais **aucun second compte ne peut être créé par l'interface avant la fin de l'étape 2**.

1. **Services** : corrections du compte dans UserService ; « caché = inconnu » et refus propres dans AppManagement ; secrets retirés pour `user`.
2. **Gateway fermé par défaut**, avec le tableau public + base, le 503 en cas de doute, l'`admin` sans expiration. Interface : sans bus pour `user`, sans contrôles d'écriture, message de refus traduit, écran « Comptes ».
3. **Droits par module** : colonne `rights`, lecture par Common, lignes « module » du tableau, cases à cocher, cartes et menus selon les droits.

## Condition de sortie

Une preuve sur une vraie pile, avec deux comptes : un `user` sans droit parcourt l'interface sans erreur, n'atteint aucune route hors du tableau (essai sur chaque route connue des services), ne distingue pas une app cachée d'une app inconnue, et ne reçoit aucun événement d'une app cachée. Sans cette preuve, pas de version.

## À la charge du constructeur

Contenu exact du tableau d'autorisations ; forme de la colonne `rights` et de la réponse interne ; clés de traduction ; détail des tests ; ordre des corrections dans chaque service.
