# Comptes multiples et visibilité des apps — plan d'implémentation

> **Pour les exécutants :** sous-compétence requise : superpowers:subagent-driven-development (recommandé) ou superpowers:executing-plans. Les étapes sont des cases à cocher (`- [ ]`).

**Objectif :** le propriétaire crée des comptes `user`, cache des apps pour certains d'entre eux, et un `user` est en lecture seule partout.

**Architecture :** UserService porte comptes et rôles. Le rôle est lu par Gateway et AppManagement par un appel interne mis en cache (une seule implémentation, dans Common). Le Gateway refuse les écritures d'un `user` ; AppManagement refuse les siennes aussi et filtre ses listes d'après `/var/lib/casaos/visibility.json`.

**Pile :** Go (echo, gorm/SQLite, oapi-codegen pour AppManagement), Vue 3 + Buefy pour l'interface.

**Conception :** `CasaOS/docs/superpowers/specs/2026-11-14-comptes-et-visibilite-design.md` (à lire avant chaque tâche ; ce plan l'applique).

## Contraintes globales

- Rôles : `admin` (le premier compte) et `user`. Un seul `admin`, jamais créé par l'API. Le rôle ne vient jamais d'une requête et ne change pas après la création.
- Rôle inconnu, compte supprimé, ou valeur introuvable = `user`. Jamais `admin` par défaut.
- Cache du rôle : 5 s ; si UserService ne répond pas, dernière valeur connue, 5 min au plus.
- Une requête interne (`external.IsInternalRequest`) et un `admin` voient et peuvent tout.
- Visibilité : fichier `/var/lib/casaos/visibility.json`, forme `{ "<projet compose>": [<id de compte>, ...] }`, écrit en fichier temporaire + renommage, 0600, verrou (comme `service/backup_scheduler.go`). Modèle « caché pour » : app nouvelle visible de tous.
- Une app cachée répond 404 à un `user` ; les éléments hôte, machines virtuelles et conteneurs hors app restent visibles dans Statistics et Logs.
- Textes de l'interface : « caché », jamais « interdit ». Aucune mention d'outil dans les commits et les PR ; un texte public décrit des comportements.
- Les tests Go d'AppManagement tournent sous Linux ; ceux de l'interface sous Node 22 + pnpm 9.

## Points de relecture (cas que la conception ne dit pas et qui peuvent casser)

1. Un compte supprimé puis un autre créé reçoit le même id SQLite : il ne doit hériter ni des apps cachées de l'ancien (tâche 2 et 5), ni de ses sessions (tâche 2).
2. `PUT /v1/users/current` avec `{"role":"admin"}` envoyé par un `user` ne change rien (tâche 2).
3. Un `user` qui passe le jeton en paramètre `?token=` (websocket) vers une route d'écriture ou de terminal est refusé comme avec l'en-tête (tâche 4).
4. Une mise à jour ou une réinstallation d'une app cachée garde la liste ; seule la vraie désinstallation l'efface (tâche 5).
5. Un identifiant de conteneur ou de Statistics (`app:<app>:<service>:<n>`) d'une app cachée n'est pas lisible par son id direct (tâches 5 et 6).

---

## Tâche 1 : lecture du rôle dans Common

**Fichiers :** créer `CasaOS-Common/external/role.go`, `CasaOS-Common/external/role_test.go`.

**Interfaces :**
- Produit : `func RoleOf(runtimePath string, userID int) string` — renvoie `"admin"` ou `"user"`. Appelle `GET <user-service>/v1/users/role/<id>` avec `InternalAuthorization(runtimePath)` (adresse lue comme `fetchPublicKey`, fichier `user-service.url`).
- Produit : `var roleNow = time.Now` (variable de test) pour piloter l'horloge.

- [ ] **Étape 1 : tests.** `TestRoleIsCachedFiveSeconds` (deux appels en moins de 5 s = une seule requête), `TestRoleKeepsLastKnownValueUpToFiveMinutesWhenUserServiceIsDown`, `TestRoleIsUserWhenNothingIsKnown`, `TestDeletedAccountIsUser` (le serveur répond 404 : la valeur mise en cache est `user`), `TestOnlyTheWordAdminIsAdmin` (réponse `"Admin"`, `""`, `"root"` = `user`). Le serveur de test est un `httptest.Server` dont l'adresse est écrite dans un `user-service.url` temporaire.
- [ ] **Étape 2 : voir échouer** — `go test ./external -run Role` : FAIL, `RoleOf` indéfini.
- [ ] **Étape 3 : implémenter `RoleOf`** dans `external/role.go` : table `map[int]entry{role, at}` sous `sync.Mutex`, mêmes motifs que `GetPublicKey`.
- [ ] **Étape 4 : voir passer** — `go test ./external` : PASS.
- [ ] **Étape 5 : commit** puis PR, merge, tag `v0.x.y` de Common. Les tâches 3, 4 et 5 attendent ce tag (`go get github.com/ReCasaOS/CasaOS-Common@<tag>` dans chaque module).

---

## Tâche 2 : UserService — comptes, rôles, suppression

**Fichiers :** modifier `CasaOS-UserService/route/v1.go`, `route/v1/user.go`, `service/user.go`, `main.go` ; créer `route/v1/accounts.go`, `route/accounts_test.go` ; modifier `api/user-service/openapi.yaml` si le dépôt y documente les routes v1.

**Interfaces :**
- Produit (HTTP, réservé à `admin`, sinon 403) :
  - `POST /v1/users` corps `{"username","password"}` → crée un compte de rôle `user`. Mêmes règles que `PostUserRegister` (nom non vide et inconnu `USER_EXIST`, mot de passe 6 caractères au moins `PWD_IS_TOO_SIMPLE`, hash `encryption.HashPassword`, dossier `UserDataPath/<id>`). Refuse tout champ `role`.
  - `GET /v1/users` → liste `[{id, username, role, totp_enabled}]`.
  - `DELETE /v1/users/:id` → supprime ; refuse (409) de supprimer l'`admin` ou son propre compte. `DELETE /v1/users` (tout supprimer) devient réservé à une requête interne.
- Produit (HTTP, requête interne seulement, sinon 403) : `GET /v1/users/role/:id` → `Data: "admin"|"user"`, 404 si le compte n'existe pas.
- Produit : `func requireAdmin(next echo.HandlerFunc) echo.HandlerFunc` dans `route/v1/accounts.go` (rôle lu en base d'après l'en-tête `user_id`).
- Produit : `func (u *userService) NormaliseRoles()` dans `service/user.go`, appelée au démarrage dans `main.go` : tout rôle vide devient `admin`.
- Consomme (tâche 6) : `DELETE /v2/app_management/visibility/accounts/{id}` (interne, côté AppManagement).

- [ ] **Étape 1 : tests** (`route/accounts_test.go`, motifs de `v1_sessions_test.go`) : `TestUserCannotCreateAccounts` (403), `TestAdminCreatesUserAccount` (role `user`, mot de passe haché, peut se connecter), `TestRoleFieldIsRefusedOnCreation`, `TestPutCurrentIgnoresRole` (un `user` envoie `{"role":"admin"}`, la base garde `user`), `TestAdminCannotDeleteSelfOrAdmin` (409), `TestDeletedAccountSessionsAreRefused` (un refresh token du compte supprimé répond 401), `TestRecreatedAccountDoesNotInheritSessions` (même id réutilisé, ancien refresh token refusé), `TestRoleRouteIsInternalOnly`, `TestEmptyRoleBecomesAdminAtStart`, `TestDeleteAccountTellsAppManagementFirst` (AppManagement indisponible : 502 et le compte reste).
- [ ] **Étape 2 : voir échouer** — `go test ./route -run 'Account|Role'` : FAIL.
- [ ] **Étape 3 : implémenter** les routes dans `route/v1/accounts.go` et les brancher dans `route/v1.go` (`requireAdmin` sur la création, la liste et les deux suppressions). Dans `PutUserInfo` (`route/v1/user.go:372`), remplacer le rôle reçu par `user.Role` toujours. Suppression : appeler d'abord AppManagement (client interne, `external.InternalAuthorization`), puis `DeleteUserById`. Choix du plan : si AppManagement ne répond pas, la suppression échoue (502) plutôt que de laisser un id réutilisable avec une liste cachée.
- [ ] **Étape 4 : voir passer** — `go test ./...` : PASS (les anciens tests de `Role: "admin"` restent verts).
- [ ] **Étape 5 : commit** `feat: accounts with roles, admin-only management`.

---

## Tâche 3 : interface — écran « Comptes »

**Fichiers :** dans `CasaOS-UI` (worktree depuis `inkly/main`) : créer le composant de l'écran dans le dossier des réglages (voir comment `src/components/settings/` range les écrans en cartes), un module d'API `src/api/accounts.js`, les clés de traduction dans `src/locales/*`, un test `*.spec.js` à côté du composant.

**Interfaces :**
- Consomme : les routes de la tâche 2. Produit : `listAccounts()`, `createAccount(username, password)`, `deleteAccount(id)` dans `src/api/accounts.js` (même client HTTP que `src/api/users.js`).
- Produit : un getter `isAdmin` (rôle de `GET /v1/users/current`) dans le magasin utilisé par la tâche 7.

- [ ] **Étape 1 : tests** : `lists accounts and shows a delete button except for the admin`, `creating an account calls createAccount and refreshes the list`, `the screen is not reachable when the role is user`.
- [ ] **Étape 2 : voir échouer** — `corepack pnpm exec vitest run <chemin du test>` : FAIL.
- [ ] **Étape 3 : implémenter** l'écran (créer, lister, supprimer avec confirmation) dans le style des cartes existantes ; entrée de menu visible de l'`admin` seul.
- [ ] **Étape 4 : voir passer** — vitest, puis `corepack pnpm exec eslint <fichiers>` : propre.
- [ ] **Étape 5 : commit**.

---

## Tâche 4 : Gateway — lecture seule pour `user`

**Fichiers :** créer `CasaOS-Gateway/route/readonly.go`, `route/readonly_test.go` ; modifier `route/gateway_route.go` (autour de `proxy.ServeHTTP(w, r)`, à la fin de `GetRoute`).

**Interfaces :**
- Consomme : `external.RoleOf` (tâche 1), `external.GetPublicKey`, `jwt.Validate`, `external.IsInternalRequest`.
- Produit : `func userMayCall(method, path string) bool` — table des appels permis à un `user`.
- Produit : `func (g *GatewayRoute) readOnlyForUsers(next http.Handler) http.Handler` — enveloppe le proxy : sans jeton valide, ou requête interne, ou rôle `admin` : passe ; rôle `user` : passe si `userMayCall`, sinon 403 au format `model.Result`.

- [ ] **Étape 1 : tests** (`route/readonly_test.go`, table `[]struct{method, path string; role string; want int}`) : GET/HEAD/OPTIONS permis ; POST/PUT/DELETE/PATCH refusés pour un `user` sur `/v2/app_management/...`, `/v1/...` ; permis pour `user` : `POST /v1/users/login`, `/v1/users/refresh`, `/v1/users/2fa/*`, `PUT /v1/users/current`, `PUT /v1/users/current/password`, `POST|PUT|DELETE /v1/users/current/custom/*` et `/current/image/*`, `PUT /v1/users/avatar`, `PUT /v1/users/login-style` ; refusés même en GET : les routes du terminal du système et du terminal de conteneur (le constructeur les relève avec `git grep -n -i "terminal\|websocket\|/ws" CasaOS/route CasaOS-AppManagement/route` et les inscrit dans la table). Cas : `TestTokenInQueryStringIsJudgedLikeTheHeader`, `TestInternalRequestPasses`, `TestAdminPasses`, `TestUnknownRoleIsUser`, `TestNoTokenPassesToTheServiceWhichRefuses`, `TestGetThatTriggersAnActionIsListed` (le constructeur dresse la liste des GET à effet de bord, par exemple lancer une vérification ou un export, et les ajoute en refus).
- [ ] **Étape 2 : voir échouer** — `go test ./route -run ReadOnly` : FAIL.
- [ ] **Étape 3 : implémenter** : le jeton se lit comme dans `management_route.go` (en-tête `Authorization`, sinon paramètre `token`) ; `RoleOf(runtimePath, claims.ID)`. Brancher `readOnlyForUsers` autour du proxy de chemin ; ne pas toucher à `serveHost` ni au port de partage.
- [ ] **Étape 4 : voir passer** — `go test ./...` : PASS.
- [ ] **Étape 5 : commit** `feat: accounts of role user are read-only at the gateway`.

---

## Tâche 5 : AppManagement — identité, rôle, refus des écritures, stockage de la visibilité

**Fichiers :** créer `CasaOS-AppManagement/service/visibility.go`, `service/visibility_test.go`, `route/role.go`, `route/role_test.go` ; modifier `route/v1.go:49` et `route/v2.go:96` (après la pose de `user_id`), le crochet de désinstallation du service des apps, `api/app_management/openapi.yaml` (route interne) puis `go generate .`.

**Interfaces :**
- Consomme : `external.RoleOf`.
- Produit : `func rejectWritesFromUsers() echo.MiddlewareFunc` (`route/role.go`) — pour un `user`, toute méthode autre que GET/HEAD/OPTIONS répond 403 ; les requêtes internes et `admin` passent. Posé sur les groupes v1 et v2.
- Produit : `type Visibility struct{...}` dans `service/visibility.go` avec `func (v *Visibility) HiddenFor(app string, account int) bool`, `Hide(app string, accounts []int) error`, `Get(app string) []int`, `RemoveApp(app string) error`, `RemoveAccount(id int) error` ; chemin dans la variable `VisibilityPath = "/var/lib/casaos/visibility.json"` (surchargée comme `BackupSchedulePath` dans `service/datapath.go`).
- Produit (HTTP, `admin` seulement) : `GET|PUT /v2/app_management/visibility/{app}` corps `{"hidden_for":[<id>...]}` ; (interne seulement) `DELETE /v2/app_management/visibility/accounts/{id}`.
- Produit : `func visibleTo(ctx echo.Context, app string) bool` — vrai pour interne, `admin`, ou si l'app n'est pas cachée pour l'id de `user_id`.

- [ ] **Étape 1 : tests.** `visibility_test.go` : `TestHideAndReadBack`, `TestWriteIsAtomicAnd0600`, `TestRemoveAccountClearsEveryEntry`, `TestCorruptFileMeansNothingHiddenButIsNotOverwrittenSilently` (fichier illisible : rien de caché, l'erreur est journalisée, le fichier reste en place). `role_test.go` : `TestUserWriteIs403OnV1AndV2`, `TestUserGetIsAllowed`, `TestInternalAndAdminWritesPass`, `TestVisibilityRoutesAreAdminOnly`, `TestAccountRemovalRouteIsInternalOnly`. Désinstallation : `TestUninstallRemovesTheEntry`, `TestUpdateAndReinstallKeepTheEntry`.
- [ ] **Étape 2 : voir échouer** — `go generate .`, puis compiler les tests et les lancer sous Linux : FAIL.
- [ ] **Étape 3 : implémenter** `Visibility` (écriture comme `service/git_app_state.go:277`, verrou `sync.RWMutex`), le middleware, les trois routes et l'appel de `RemoveApp` dans le crochet de désinstallation seulement (pas dans la mise à jour ni la réinstallation : le constructeur sépare les deux chemins).
- [ ] **Étape 4 : voir passer** — mêmes commandes : PASS ; les anciens tests restent verts.
- [ ] **Étape 5 : commit** `feat: visibility store and read-only role at AppManagement`.

---

## Tâche 6 : AppManagement — filtrage des listes et des routes d'app

**Fichiers :** modifier `route/v2/internal_web.go` (`/web/appgrid`), `route/v2/compose_app.go`, `route/v2/container_detail.go`, `route/v2/container_stats.go`, `route/v2/stats.go`, `route/v2/logs.go`, `route/v2/app_operations.go` (`/operations`) ; tests à côté (`*_test.go`).

**Interfaces :**
- Consomme : `visibleTo` (tâche 5).
- Produit : `func appOfStatsID(id string) (app string, ok bool)` (`route/v2/stats.go`) — extrait `<app>` de `app:<app>:<service>:<n>` ; `ok=false` pour un identifiant hôte, VM ou hors app (qui reste visible).

- [ ] **Étape 1 : tests**, deux comptes (un `admin`, un `user`) et une app cachée pour le second : `TestAppGridOmitsHiddenApps`, `TestComposeListOmitsHiddenApps`, `TestComposeRoutesOfAHiddenAppAre404` (`/compose/{id}`, `containers`, `containers/stats`, `logs`, `env`, `healthcheck`), `TestContainerDetailOfAHiddenAppIs404` (identifiant de conteneur résolu vers son projet compose), `TestStatsOmitsAndRefusesHiddenAppsButKeepsHostAndVMs` (`/stats`, `/stats/{id}`, `/stats/{id}/alerts`), `TestLogsAppsLogsStreamEventsExportOmitHiddenApps`, `TestOperationsOmitHiddenApps`, `TestAdminAndInternalSeeEverything`, `TestAppOfStatsID` (cas `app:immich:db:1`, `host`, `vm:x`, chaîne vide).
- [ ] **Étape 2 : voir échouer** — Linux : FAIL.
- [ ] **Étape 3 : implémenter** le filtre dans chaque handler listé ; une route propre à une app renvoie 404 avec le même corps que pour une app inconnue. Le constructeur confirme sur `inkly/main` qu'aucun autre handler n'énumère des apps (le relevé n'en a pas trouvé) et ajoute ce qu'il trouve avec son test.
- [ ] **Étape 4 : voir passer** — PASS.
- [ ] **Étape 5 : commit**, PR AppManagement (tâches 5 et 6 ensemble ou à la suite).

---

## Tâche 7 : interface — section « Visibilité » et contrôles cachés

**Fichiers :** dans `CasaOS-UI` : la modale de réglages d'une app (là où se règlent les autres propriétés de l'app, voir `ComposeConfig.vue`), le magasin des rôles (tâche 3), les clés de traduction, tests `*.spec.js`.

**Interfaces :**
- Consomme : `GET|PUT /v2/app_management/visibility/{app}`, `listAccounts()` (tâche 3), `isAdmin`.
- Produit : getter `canManage` (vrai pour `admin`) ; les contrôles d'installation, désinstallation, mise à jour, démarrage/arrêt, sauvegardes, magasins et réglages globaux sont masqués quand il est faux.

- [ ] **Étape 1 : tests** : `visibility section lists user accounts as checkboxes for the admin only`, `saving calls PUT with the checked ids`, `user does not see install, uninstall, update, start/stop buttons`, `the wording says hidden, never forbidden` (cherche « interdit » / « forbidden » dans les clés ajoutées).
- [ ] **Étape 2 : voir échouer** — vitest : FAIL.
- [ ] **Étape 3 : implémenter** la section (cases à cocher, une par compte `user`) et masquer les contrôles d'après `canManage`. Le serveur refuse de toute façon (tâches 4 et 5).
- [ ] **Étape 4 : voir passer** — vitest + eslint : propres.
- [ ] **Étape 5 : commit**, PR.

---

## Tâche 8 : preuve de bout en bout et documentation

**Fichiers :** créer `CasaOS/docs/comptes-et-visibilite.md` (en anglais, comme la documentation publique voisine ; dire la limite connue : un `user` lit encore les autres modules, y compris les fichiers ; et que « caché » n'est pas « verrouillé »).

- [ ] **Étape 1 :** monter la pile réelle dans Docker, créer un compte `user`, cacher une app, puis vérifier avec deux sessions : la liste et les routes propres de l'app cachée (404), une écriture refusée (403) en HTTP et en websocket, la même app visible de l'`admin`, la suppression du compte (ses sessions mortes, son id retiré de `visibility.json`).
- [ ] **Étape 2 :** vérifier ce que font les téléphones de la flotte côté identité (un téléphone agit-il comme `admin` ?) et l'écrire dans la documentation sans le filtrer ici.
- [ ] **Étape 3 :** commiter conception, plan et documentation dans `CasaOS` ; mettre à jour le projet todo (élément #150) ; préparer la version selon la chaîne habituelle, sur le « go » du propriétaire.

---

## Ordre et publication

Common (1) → UserService + écran Comptes (2, 3) → Gateway (4) → AppManagement (5, 6) → interface (7) → preuve (8). Chaque étape est publiable seule ; l'étape 4 ne doit pas partir avant la 2 (sans compte `user`, elle ne change rien, mais sans la route de rôle elle traiterait tout le monde en `user`).

## Auto-relecture

- Couverture : les quatre étapes de livraison de la conception sont les tâches 2–3, 4–5, 5–6, 7. Les objectifs 1 à 4 et les non-objectifs sont dans les contraintes et la tâche 8.
- Dépend de la conception : `GET /v1/users/role/:id` et la route interne de retrait d'un compte sont des choix du plan (la conception laissait « format exact des routes » au constructeur).
- Choix du plan à valider : suppression d'un compte refusée (502) si AppManagement ne répond pas ; `DELETE /v1/users` (tout supprimer) réservé à l'interne.
