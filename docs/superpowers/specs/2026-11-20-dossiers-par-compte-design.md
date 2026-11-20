# Dossiers par compte dans Fichiers (CasaOS#168, lot A)

Date : 2026-11-20. Lot B (droits Samba par compte et par partage) viendra après, sur ce socle.

## Ce qui existe

- Un compte est `admin` (propriétaire) ou `user`. Un `user` ne modifie rien sur la box ; il lit les modules cochés pour lui (`storage vm backups fleet system`). La Gateway refuse à un `user` toute route absente de sa table (`route/allowtable.go`), dont tout `/v1/file`, `/v1/folder`, `/v1/batch`. L'UI le renvoie de `/files`.
- Le core confine déjà l'API fichiers à des racines (`pkg/fileroots`, `[file] ManagementRoots`) : chaque chemin est résolu avec openat2 sous sa racine, `..` et les liens ne sortent pas ; un dossier au-dessus d'une racine ne liste que le chemin qui y mène. Ces racines sont celles de la box, sans notion de compte. Le core tourne en root.

## Ce qui est construit

1. **Un compte reçoit des dossiers** (propriétaire, Comptes) : une liste de chemins, chacun en Lecture ou Lecture-écriture. Pas de dossier : pas de Fichiers (rien n'est visible). Au plus 32 dossiers par compte (mon choix).
2. **UserService** garde la liste dans `o_users.Folders` (JSON `[{path, write}]`), l'écrit par `PUT /v1/users/:id/folders` (propriétaire seul), et la donne avec le rôle (`GET /v1/users/role/:id`, champ `folders`) et dans le compte courant (pour l'UI). Un chemin : absolu, écrit une seule fois (pas de `..`, `.`, `//`), pas `/`, aucun dans un autre du même compte (un dossier, un mode). Qu'il soit un vrai dossier de la box, le core en juge quand il est atteint (un dossier absent ou écrit à travers un lien est refusé). Common rejette aussi une liste imbriquée.
3. **Common** lit ces dossiers avec le rôle, dans la même réponse en cache : `FoldersOf(runtimePath, id)`, fermé par défaut comme `RightsOf` (rien si la réponse a plus de cinq secondes, si user-service ne répond pas, pour l'admin). `RightsOf` donne en plus le droit dérivé `files` à un compte qui a au moins un dossier.
4. **Gateway** ouvre à un compte qui a `files` les routes de Fichiers : liste, lecture, téléchargement (seul ou en archive), miniatures, taille et nombre, création, renommage, édition, envoi (v1 et `/v2/casaos/file/upload`), copie/déplacement, suppression (y compris vers la corbeille), racines. Restent fermés : la corbeille (voir, restaurer, vider), le transfert entre appareils (`/v1/file/ws`, `peers`), la recherche, les lecteurs cloud, Samba, ZeroTier.
5. **Le core** confine chaque chemin d'une requête d'un `user` aux dossiers de ce compte, avec `fileroots` (mêmes garanties openat2), en plus des racines de la box. Écrire (créer, éditer, envoyer, renommer, supprimer, destination d'une copie, source et destination d'un déplacement) exige un dossier en Lecture-écriture ; lire, un dossier quelconque. Au-dessus de ses dossiers, un compte ne voit que le chemin qui y mène. `GET /v1/file/roots` répond ses dossiers, avec ceux qu'il peut écrire. Le core demande le rôle et les dossiers à user-service (Common), il ne fait plus confiance à la seule Gateway pour Fichiers.
6. **UI** : dans Comptes, chaque compte a une section Dossiers (ajouter avec le sélecteur de dossier, Lecture / Lecture-écriture, retirer). Un compte qui a des dossiers voit Fichiers, avec ses dossiers pour racines ; les actions d'écriture sont absentes dans un dossier en lecture ; la corbeille, le partage Samba et le transfert entre appareils n'y sont pas.

## Choix et limites

- Les fichiers créés par un `user` appartiennent à root, comme ceux du propriétaire aujourd'hui (le core est root). Le lot B (Samba) posera la question des propriétaires.
- Supprimer pour un `user` déplace vers la corbeille quand l'UI le demande ; seul le propriétaire voit et vide la corbeille.
- Un dossier retiré ou passé en lecture s'applique au plus cinq secondes après (cache du rôle), y compris à une copie ou un déplacement déjà en file : la tâche relit les dossiers du compte quand elle s'exécute.
- Une copie ou un déplacement d'un compte s'exécute plus tard, dans la file : il passe par `os.Root` ouvert sur ses dossiers, si bien qu'un dossier échangé contre un lien pendant l'attente ou la copie ne mène nulle part ailleurs.
