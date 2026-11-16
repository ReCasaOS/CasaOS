# Comptes fermés par défaut — plan de corrections

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Note du 2026-11-16 :** ce plan est gardé comme il a été exécuté. Depuis, le numéro d'un compte n'est jamais redonné à un autre compte (voir `docs/comptes-et-visibilite.md`) : les étapes qui parlent d'un id réutilisé décrivent l'état d'avant.

**Goal:** Remplacer le filtre « lecture seule » par un accès fermé par défaut pour `user`, corriger les routes de compte et d'app cachée, puis ajouter les droits par module.

**Architecture:** Les services corrigent leurs routes de compte et leurs fuites d'app cachée (étape 1). Le Gateway n'ouvre à un `user` que les lignes d'un tableau testé (étape 2), l'interface suit. Une colonne `rights` ouvre ensuite la lecture de modules (étape 3).

**Tech Stack:** Go (echo, gorm/SQLite), Common (`external.RoleOf`), Vue 3 + Buefy + Vuex, tests Go sous Linux, tests UI Node 22 + pnpm 9.

**Spec:** `docs/superpowers/specs/2026-11-14-comptes-et-visibilite-design.md` (révisée le 2026-11-15, validée par Gary).

**Départ :** les branches `feat/accounts-visibility` existantes (Common `_work/acc-common`, UserService `_work/acc-users`, Gateway `_work/acc-gateway`, AppManagement `_work/acc-am`, UI `_work/acc-ui`). Les commits « TEMP replace Common » restent jusqu'au moment des PR.

## Global Constraints

- Un `user` n'atteint que son compte, les routes de base inscrites au tableau, et les modules dont il a le droit. Tout le reste : 403, lectures comprises. Websocket compris.
- L'`admin` est unique, ne se supprime pas, ne change pas de rôle. Une réponse « admin » positive vaut sans expiration dans Common.
- Un jeton présent que le Gateway ne peut pas vérifier, sur une route non publique : 503, jamais un passage.
- Caché = inconnu : même code, même corps, même ordre de contrôles qu'un nom qui n'existe pas.
- Aucun champ de chemin fourni par le client n'est lu, copié ni supprimé hors de `UserDataPath/<id du compte>/` (chemin nettoyé, puis `filepath.Rel` sans `..`).
- Une entrée invalide ne tue jamais un service : pas de `log.Fatal` dans une route.
- Texte d'interface : « caché », jamais « interdit ». Chaque clé de traduction existe en `en_US` et `fr_FR`.
- Rien qui reste à corriger dans un commit, un message de PR ou un commentaire d'issue. Les tests portent le nom du comportement (« refuse un chemin hors du dossier »), pas la façon d'y arriver.
- Aucune mention d'outil dans les commits et les PR. Rien n'est poussé, taggé ni fusionné sans « go » de Gary.
- Commandes de test : `go test ./...` pour UserService, Gateway et Common ; AppManagement après `go generate .`, sous Linux ; UI sous Node 22 avec corepack pnpm 9.

## Review Focus

1. Un `user` sans aucun droit ouvre l'interface complète sans une seule erreur visible : tout appel de l'interface en session `user` est dans le tableau (test qui parcourt `src/service/*` et les URL websocket).
2. Une route qui n'existe pas dans le tableau reste refusée sous toutes ses écritures (casse, échappements, séparateurs, méthode inattendue) : test sur un échantillon de chemins par service.
3. Un nom caché et un nom inconnu donnent la même réponse sur chaque route d'app, quels que soient les paramètres (`range`, `check`, destination, tampon) : test de comparaison par route.
4. Gateway redémarré pendant qu'UserService est arrêté : un `user` avec jeton reçoit 503, l'`admin` aussi (jamais un passage).
5. Compte supprimé puis id réutilisé : l'ancien jeton ne donne ni lecture, ni écriture, ni droit de module.

---

## Étape 1 — Services

### Task 1: UserService — routes du compte confinées

**Files:**
- Modify: `CasaOS-UserService/_work/acc-users/route/v1/user.go` (PutUserInfo ~372, PutUserAvatar ~284, GetUserAvatar ~337, DeleteUserImage ~795, image copy ~669, GetUserAllUsername/`/:username`)
- Modify: `route/v1.go`, `route/v1/accounts.go` (DeleteUser ~155-170), `main.go` si la route interne `DELETE /v1/users` y est branchée
- Test: `route/accounts_test.go` (+ nouveau `route/own_account_test.go`)

**Interfaces:**
- Produces: `func ownDir(id int) string` (= `UserDataPath/<id>`) et `func within(dir, p string) bool` (chemin nettoyé, `filepath.Rel` sans `..`) dans `route/v1/user.go`. Tous les accès fichier du compte passent par `within`.
- Consumes: rien des autres tâches.

- [ ] **Step 1: Tests rouges** (un par comportement, avec les valeurs exactes) :
  - `TestPutCurrentIgnoresAvatarFromClient` : un enregistrement du profil qui nomme un chemin d'avatar répond 200 et laisse l'avatar du compte inchangé ; de même pour les champs que le profil ne possède pas (rôle, id, mot de passe, 2FA).
  - `TestAvatarServedOnlyFromOwnFolder` : si la colonne `Avatar` porte un chemin hors de `ownDir`, `GET /v1/users/avatar` sert l'avatar par défaut.
  - `TestDeleteImageRefusesOutsideOwnFolder` : la suppression d'une image hors du dossier du compte est refusée (403) et le fichier reste, y compris dans le dossier d'un autre compte dont le numéro commence comme le sien ; un fichier du dossier du compte est supprimé (200).
  - `TestPutAvatarWithBrokenImageIs400` : `PUT /v1/users/avatar {"file":"AAAA"}` → 400, le processus reste en vie.
  - `TestUserSeesOnlyOwnRow` : `GET /v1/users/name` et `GET /v1/users/<autre nom>` par un `user` → 403 ; par l'`admin` → 200.
  - `TestImageCopyStaysInOwnFolder` : `PUT /v1/users/current/image/:key` avec un fichier source hors des dossiers de données refusé.
  - `TestDeleteAccountRechecksTargetUnderLock` : une suppression en double après réutilisation de l'id ne supprime pas le nouveau compte.
  - `TestDeleteAccountReturnsDBError` : si `DeleteUserById` échoue, la route répond une erreur, le dossier reste, AppManagement n'a pas été appelé pour retirer les droits.
- [ ] **Step 2: Lancer et constater le rouge** : `go test ./route/ -run "TestPutCurrent|TestAvatar|TestDeleteImage|TestUserSees|TestImageCopy|TestDeleteAccount" -v`, attendu FAIL.
- [ ] **Step 3: Implémenter** : `PutUserInfo` ne reprend que `nickname`, `description`, `email`, `username` (validé comme à l'inscription) ; l'avatar ne se règle que par `PutUserAvatar`. `GetUserAvatar` ne sert que `within(ownDir, user.Avatar)`. `DeleteUserImage` : `within(ownDir, path)`. `PutUserAvatar` : erreur 400 si l'image est invalide, plus aucun `log.Fatal` dans le paquet `route`. `GET /v1/users/name` et `GET /v1/users/:username` : `RequireAdmin`. Copie d'image : la source doit être sous `UserDataPath` ou le dossier d'images partagé existant. `DeleteUser` : relire la cible sous `UserRegisterMu`, propager l'erreur de suppression en base avant d'effacer le dossier. Retirer la route interne `DELETE /v1/users` (sans appelant).
- [ ] **Step 4: Vert** : `go test ./...` dans `acc-users` (tous les tests déjà verts le restent, les trois tests qui sautent par `t.Skipf` quand l'id n'est pas réutilisé deviennent des échecs : forcer la réutilisation en créant/supprimant dans le test).
- [ ] **Step 5: Commit** : `fix: account routes only touch the account's own folder` (sans détail).

### Task 2: AppManagement — caché = inconnu, secrets retirés pour `user`

**Files:**
- Modify: `CasaOS-AppManagement/_work/acc-am/route/v2/stats.go` (131-142), `route/v2/backup.go` (~706), `route/v1/docker.go` (~203 networks), `route/v2/compose_updates.go` (26-83), `route/v2/global.go`, `route/v2/compose_app.go` (env, compose), `route/v2/container_detail.go`, `route/v2/git.go`, `route/v2/backup.go` (restic password ~199), `route/v2/visibility.go` (~151), `service/visibility.go`, `service/compose_app.go` (~1233 forgetUninstalledApp), `service/git_apps.go` (~474), `route/role.go`, `service/backup_running.go`, `pkg/dockerstats/identity.go`
- Test: `route/v2_visibility_reads_test.go` (refaire 523-542), `route/role_test.go` (retirer l'entérinement de `/v1/container/networks`), nouveau `route/v2_hidden_equals_unknown_test.go`

**Interfaces:**
- Produces: `func isUser(ctx echo.Context) bool` dans `route/role.go` (vrai pour tout compte non `admin` et non interne) ; `func redactEnv(ctx echo.Context, env map[string]*string) map[string]*string` (valeurs remplacées par `""` pour un `user`, noms conservés) ; route admin `GET /v2/app_management/visibility/orphans` (entrées sans app installée) et `DELETE /v2/app_management/visibility/{app}` (retire l'entrée).
- Consumes: Task 1 : rien ; `external.RoleOf` de Common.

- [ ] **Step 1: Tests rouges** :
  - `TestHiddenEqualsUnknownOnEveryAppRoute` : pour chaque route d'app d'AppManagement (liste fournie par la table OpenAPI), `user` reçoit les mêmes code et corps pour `immich` caché et `inconnu`, avec et sans `range`, `check`, destination, tampon.
  - `TestStatsRangeCheckedBeforeHidden`, `TestBackupBrowseSameForHiddenAndUnknown`.
  - `TestNetworksOmitHiddenProjects` (réseaux dont l'étiquette `com.docker.compose.project` est cachée).
  - `TestUpdatesCheckOnlyForOwner` : `GET /compose/updates?check=true` par un `user` ne lance rien et ne touche pas `image_updates.json`.
  - `TestUserGetsNoSecrets` : `/compose/{id}`, `/compose`, `/compose/{id}/env`, `/container/{id}/detail`, `/v1/container/:id`, `/v1/container/:id/compose`, `/global`, `/global/{key}`, `/git/{app}` (secret du webhook), `restic/password` : valeurs vides ou 403 pour `user`, pleines pour `admin`.
  - `TestUninstallKeepsVisibilityEntry` : l'entrée survit à `RemoveApp` ; `GET /visibility/orphans` la liste ; `DELETE /visibility/{app}` la retire.
  - `TestVisibilityNameIsValidated` : `Immich` et noms vides ou de plus de 64 caractères refusés (400).
  - `TestEmptyListsDoNotRevealHiddenWork` : `/compose/updates/run`, `/backup/running` (y compris `Current`), conteneurs `docker compose run` sous `container:<nom>`.
- [ ] **Step 2: Rouge** : `go generate .` puis le test sous Linux (voir Global Constraints), attendu FAIL.
- [ ] **Step 3: Implémenter** : déplacer le contrôle caché après la validation des paramètres, ou faire valider d'abord les paramètres avec le nom de substitution (comme `AskAboutContainer`). `isUser` appliqué aux lectures de secrets (`redactEnv`, webhook, global, restic password : 403 pour la dernière, `""` pour les autres). `forgetUninstalledApp` ne retire plus l'entrée. Nom validé par l'expression `^[a-z0-9][a-z0-9_-]{0,63}$`. Les listes sans rangement par app filtrent par étiquette compose. Mettre `isUser` à jour des routes de lecture qui agissent (`check=true`).
- [ ] **Step 4: Vert** : `go test ./route/... ./service/... ./pkg/...` sous Linux ; seuls échecs admis : les quatre tests qui exigent un démon Docker.
- [ ] **Step 5: Commit** : `fix: hidden apps answer as unknown, secrets stay with the owner`.

### Task 3: Common — l'admin sans expiration, 503 sur doute

**Files:**
- Modify: `CasaOS-Common/_work/acc-common/external/role.go`
- Test: `external/role_test.go`

**Interfaces:**
- Produces: `RoleOf(runtimePath, id string) string` inchangé de forme ; une réponse `"admin"` reste valable sans expiration (cache par id, jamais évincé tant que le processus vit). Nouvelle fonction `RoleKnown(runtimePath, id string) (role string, ok bool)` : `ok` faux quand aucune réponse positive n'a jamais été reçue pour cet id (le Gateway s'en sert pour répondre 503).
- Consumes: rien.

- [ ] **Step 1: Tests rouges** : `TestAdminAnswerNeverExpires` (horloge avancée de 24 h, UserService en panne : `admin` reste `admin`) ; `TestUserAnswerStillExpires` ; `TestRoleKnownFalseBeforeAnyAnswer` ; id `0`, négatif, non numérique, énorme : `("", false)`.
- [ ] **Step 2: Rouge** : `go test ./external/ -run "TestAdminAnswer|TestUserAnswer|TestRoleKnown" -v`.
- [ ] **Step 3: Implémenter** dans `role.go` : le cache des réponses `admin` n'a pas de limite de durée ; les autres réponses gardent 5 s et 5 min de repli. `RoleKnown` lit la même table.
- [ ] **Step 4: Vert** : `go test ./external/...`.
- [ ] **Step 5: Commit** : `feat: keep a positive admin answer, report unknown roles`.

## Étape 2 — Gateway fermé par défaut, interface

### Task 4: Gateway — tableau d'autorisations

**Files:**
- Create: `CasaOS-Gateway/_work/acc-gateway/route/allowtable.go` (le tableau : `type rule struct{ Method, Pattern string; Kind kind }`, `kind` ∈ `public`, `base`, `module(name)`)
- Create: `route/allowtable_test.go`, `route/ui_calls_test.go`, `route/testdata/ui-calls.txt` (un appel de l'interface par ligne : `METHOD /chemin`)
- Modify: `route/readonly.go` (remplace `refusedForUser` et `ownAccount` par le tableau ; fonction `userMayCall(method, p string, rights map[string]bool) bool`), `route/gateway_route.go`
- Test: `route/readonly_test.go` (mise à jour)

**Interfaces:**
- Consumes: Task 3 `external.RoleKnown`, `external.RoleOf`, `external.AccountGone`.
- Produces: `var allowTable []rule`, `func userMayCall(method, p string, rights map[string]bool) bool`. Le Gateway répond : 403 (`INSUFFICIENT_PERMISSIONS`) pour une route absente du tableau ; 503 si un jeton est présent, la route n'est pas `public`, et que la clé ne se lit pas ou que `RoleKnown` est faux.

- [ ] **Step 1: Inventaire** (étape de travail, pas de code de production) : lire `CasaOS-UI/_work/acc-ui/src/service/*.js`, les URL websocket (`socket.io`, `/v2/message_bus/*`, `/v2/app_management/logs/stream`), les URL à `?token=` et toutes les routes de base de la conception. Écrire `testdata/ui-calls.txt` ; chaque ligne est classée `public`, `base` ou `module:<nom>` en lisant le gestionnaire (une route qui agit, ouvre un canal interactif, sort sur le réseau ou rend un secret n'entre pas, même en GET). Les écritures admises : le compte propre (connexion, jeton de renouvellement, mot de passe, 2FA, préférences, image d'avatar) et rien d'autre.
- [ ] **Step 2: Tests rouges** : `TestEveryUICallIsInTheTable` (lit `ui-calls.txt`, chaque ligne `base` ou `public` doit être acceptée pour un `user` sans droit) ; `TestUnknownRouteRefusedForUser` sur un échantillon par service (`/v1/file/content`, `/v1/zt/x`, `/v1/other/search`, `/v1/sys/proxy`, `/v2/virt_management/vms/1/console`, `/v2/message_bus/event/app-management`, `/v2/app_management/global`, `/v1/container/1/terminal`) ; `TestPathTricksNeverOpenAnything` (casse, échappements, séparateurs, méthodes inattendues) ; `TestUnverifiableTokenIs503` (clé publique illisible ou `RoleKnown` faux, route non publique) ; `TestAdminPassesEverythingWithKnownRole` ; `TestNoTokenOnPublicRoutesPasses` (connexion, état, renouvellement, fichiers statiques) ; `TestTableRulesMatchRealRoutes` (chaque règle correspond à une route d'un `routes.txt` généré par les services, test sauté si le fichier est absent).
- [ ] **Step 3: Rouge** : `go test ./route/ -run "TestEveryUICall|TestUnknownRoute|TestPathTricks|TestUnverifiable|TestAdminPasses|TestNoToken|TestTableRules" -v`.
- [ ] **Step 4: Implémenter** : le tableau et `userMayCall(method, p, rights)` (chemin nettoyé ; le chemin brut non nettoyé n'est jamais accepté pour une écriture, et pour une lecture seulement dans la forme déjà décidée : barre oblique finale tolérée). `readOnlyForUsers` devient `closedForUsers` : jeton valide + `RoleOf == admin` passe ; jeton valide + non admin passe selon le tableau ; compte supprimé : 401 ; jeton présent mais non vérifiable : 503 ; aucune requête de jeton sur les routes `public`. Les droits viennent de la réponse interne de rôle (Task 8) ; ici `rights` est vide.
- [ ] **Step 5: Vert** : `go test ./route/` (hors `pkg/guard` et `common`, connus).
- [ ] **Step 6: Commit** : `feat: closed by default for user accounts`.

### Task 5: Interface — compte `user` sans bus, sans écriture, sans erreur brute

**Files:**
- Modify: `CasaOS-UI/_work/acc-ui/src/service/service.js` (79-140), `src/events/index.js` (21), `src/store/state.js`, `src/store/getters.js`, `src/components/Apps/AppCard.vue` (3, 157, 513-543, 977), `src/components/Apps/AppVisibility.vue`, `src/components/settings/AccountsModal.vue`, `src/components/Apps/AppSection.vue`, `src/components/filebrowser/*` (le bloc fichiers, masqué pour `user`), `src/views/LogsPage.vue` (84-85), `src/components/TopBar.vue` (252, 649, 1484), `Login.vue` (233), `src/locales/en_us.json`, `src/locales/fr_fr.json`
- Test: `src/components/canManage.spec.js` et nouveaux `src/service/errors.spec.js`, `src/components/Apps/AppCard.user.spec.js`

**Interfaces:**
- Produces: getter `canWrite` (vrai pour `admin`) ; `canManage` devient synonyme de `isAdmin` hors de la période où le rôle est inconnu ; `canReadModule(name)` (faux sans droit accordé) ; plugin `service.js` qui traduit tout 403 `INSUFFICIENT_PERMISSIONS` en un seul toast `common.noRight`, dédupliqué sur 3 s.
- Consumes: Task 4 (le Gateway refuse déjà).

- [ ] **Step 1: Tests rouges** :
  - `AppCard.user.spec.js` : cliquer sur une app arrêtée en tant que `user` ne lance aucun appel et affiche le texte « arrêtée, seul le propriétaire peut la démarrer » ; `isStarting` reste faux.
  - `errors.spec.js` : un 403 donne un seul toast traduit (en et fr), deux 403 en moins de 3 s n'en donnent qu'un ; une erreur sans `data.data` n'affiche pas de toast vide.
  - Rôle inconnu : au rechargement, l'état part du rôle enregistré à la connexion (localStorage) ; tant qu'aucun rôle n'est connu aucun contrôle d'écriture n'est rendu ; l'écran Comptes de l'admin attend la confirmation.
  - `$messageBus()` n'envoie rien pour un `user` ; les listes se rafraîchissent toutes les 10 s pour un `user` (test avec horloge simulée).
  - `AccountsModal` : double envoi bloqué par `if (saving) return`, minimum de 6 caractères vérifié côté client, messages serveur passés par `$t`, un nom d'utilisateur tronqué des espaces, bouton « Supprimer <nom> ».
  - `AppVisibility` : texte « visible de tous » seulement si `loaded && !hidden.length` ; avertissement de modifications non enregistrées via `editorState.isDirty` ; coches modifiées pendant un enregistrement non écrasées ; bouton « Réessayer » en cas d'échec de lecture.
  - Pour un `user` : plus de bloc Fichiers, plus de Nouveau, Renommer, Supprimer, notice avec croix, Tips modifiable, Redémarrer/Arrêter de Logs, Sauvegarder, Libérer, VM, Alertes, Supprimer le conteneur, mise à niveau de l'OS, Syncthing, terminal plein écran.
- [ ] **Step 2: Rouge** : `pnpm test` ciblé sur les nouveaux fichiers avec Node 22 (voir Global Constraints), attendu FAIL.
- [ ] **Step 3: Implémenter** : getters et plugin d'erreur ; `openApp` sort tôt si `!canWrite` pour une app arrêtée ; `$messageBus` sans effet pour `user` et boucle de rafraîchissement ; `v-if="canWrite"` sur chaque contrôle listé ; clés `common.noRight`, `app.stoppedOwnerOnly`, `accounts.*` en `en_us` et `fr_fr` (« Cachée pour {name} » au féminin). Rendre les noms de clés conformes aux fichiers de langue existants.
- [ ] **Step 4: Vert** : `pnpm test` (suite complète) et `pnpm lint` sur les fichiers modifiés.
- [ ] **Step 5: Commit** : `feat: user accounts see only what they can use`.

### Task 6: Preuve sur une vraie pile (condition de sortie)

**Files:**
- Create: `CasaOS/dev/accounts-proof/run.sh` et `routes.sh` (liste de chaque route des services, lue des tableaux OpenAPI et des fichiers de routes)
- Create: `CasaOS/dev/accounts-proof/README.md` (comment la lancer avec la pile Docker de `casaos-full-stack-in-docker`)

**Interfaces:**
- Consumes: Task 4 (tableau), Task 2 (cachée = inconnue).
- Produces: un script qui crée deux comptes, installe une app, la cache, puis vérifie : (a) un `user` sans droit reçoit 403 sur chaque route hors tableau, (b) chaque appel de `ui-calls.txt` répond 200/204, (c) le nom caché et un nom inconnu ont les mêmes réponses, (d) la liste des événements reçus par un `user` est vide, (e) la suppression d'un compte puis la réutilisation de son id n'ouvre rien à l'ancien jeton, (f) UserService arrêté puis Gateway redémarré : 503.

- [ ] **Step 1: Écrire le script** et sa liste de routes (`curl`, `jq`-free via python, comme `windows-json-tooling`).
- [ ] **Step 2: Lancer** sur la pile Docker quand le moteur répond ; sinon noter « non exécuté » et laisser la tâche ouverte.
- [ ] **Step 3: Commit** : `test: accounts proof script on the full stack`. Cette tâche reste la condition de sortie de la version, même si elle ne s'exécute pas ici.

## Étape 3 — Droits par module

### Task 7: UserService + Common — colonne `rights`

**Files:**
- Modify: `CasaOS-UserService/_work/acc-users/model/…` (modèle du compte), `service/user.go`, `route/v1/accounts.go` (liste, création, `PUT /v1/users/:id/rights`), `route/v1/user.go` (`GET /v1/users/role/:id` renvoie `{role, rights}`), `CasaOS-Common/_work/acc-common/external/role.go` (`RightsOf(runtimePath, id string) map[string]bool`, mise en cache comme le rôle)
- Test: `route/accounts_test.go`, `external/role_test.go`

**Interfaces:**
- Produces: noms de droits valides : `storage`, `vm`, `backups`, `fleet`, `system` ; `PUT /v1/users/:id/rights` (admin seul) corps `{"rights":["storage","vm"]}`, un nom inconnu → 400 ; `GET /v1/users/role/:id` → `{"role":"user","rights":["storage"]}` ; `external.RightsOf` ne rend jamais un droit sans réponse positive (échec = carte vide).
- Consumes: Task 3 (cache).

- [ ] **Step 1: Tests rouges** : un droit accordé apparaît dans la réponse interne ; un compte neuf n'en a aucun ; un `user` ne peut pas s'en accorder (403) ; `RightsOf` rend une carte vide si UserService ne répond pas ; supprimer le compte retire ses droits ; id réutilisé = aucun droit hérité.
- [ ] **Step 2: Rouge**, **Step 3: Implémenter** (colonne `rights` en texte JSON, migration à `AutoMigrate`, valeur vide), **Step 4: Vert**, **Step 5: Commit** : `feat: per-module rights on accounts`.

### Task 8: Gateway + interface — lignes « module » et cases à cocher

**Files:**
- Modify: `CasaOS-Gateway/_work/acc-gateway/route/allowtable.go` (lignes `module(storage|vm|backups|fleet|system)`, lues de `ui-calls.txt` classées `module:<nom>`, sans console de VM, sans mot de passe restic, sans relais de flotte, sans journaux bruts, sans terminal), `route/readonly.go` (appelle `external.RightsOf`)
- Modify: `CasaOS-UI/_work/acc-ui/src/components/settings/AccountsModal.vue` (cases à cocher), `src/store/getters.js` (`canReadModule`), cartes et entrées de menu des modules
- Test: `route/allowtable_test.go`, `route/readonly_test.go`, `src/components/settings/AccountsModal.spec.js`

**Interfaces:**
- Consumes: Task 7 `RightsOf`, `PUT /v1/users/:id/rights` ; Task 4 `userMayCall(method, p, rights)`.

- [ ] **Step 1: Tests rouges** : un `user` avec `storage` lit `/v1/disks` et pas `/v2/virt_management/vms` ; avec `vm` lit les VM et jamais `/console` ; `RightsOf` en échec = comportement « aucun droit » ; l'interface masque une carte sans droit et la montre avec ; la case cochée appelle `PUT …/rights` et se recoche après rechargement.
- [ ] **Step 2: Rouge**, **Step 3: Implémenter**, **Step 4: Vert**, **Step 5: Commit** : `feat: module rights in the gateway and the accounts window`.

### Task 9: Documentation d'utilisation

**Files:**
- Modify: `CasaOS/docs/comptes-et-visibilite.md`, `CasaOS/docs/superpowers/specs/2026-11-14-comptes-et-visibilite-design.md` (seulement si une valeur change)

- [ ] **Step 1:** Réécrire la doc pour le modèle fermé par défaut : ce qu'un compte voit, les droits par module, ce que « caché » veut dire (la box cache de ses listes, pas de l'adresse de l'app), le bus de messages fermé, l'entrée de visibilité qui survit à la désinstallation, comment la retirer. Seulement des comportements.
- [ ] **Step 2: Commit** : `docs: closed-by-default accounts and module rights`.
