# Upgrade OS 12 → 13, PR 1 : textes tirés du saut, dossiers rangés par saut — plan d'implémentation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** que le moteur des blocs puisse porter un deuxième saut Debian sans rien changer de ce qu'il écrit pour 11 → 12.

**Architecture:** `HopSpec` gagne les numéros de version des deux releases. Toute phrase qui parle de la machine en face du texte est tirée du saut. Les phrases qui disent où un fait a été mesuré gardent leur release littérale et sont regroupées dans une fonction. Pour tout saut autre que 11 → 12, le dossier de sauvegarde porte un fichier `hop`, et les deux boucles qui lisent `/var/backups/casaos-os-upgrade-*` sautent les dossiers d'un autre saut. Le texte de 11 → 12 reste identique octet pour octet.

**Tech Stack:** Go 1.26, du shell exécuté sous dash dans les tests (`shellBox`).

**Spec:** `docs/superpowers/specs/2026-11-04-os-upgrade-trixie-design.md`, sections 1 et 2.

## Global Constraints

- Les trois golden `pkg/osplan/testdata/runbook-d11-12.golden`, `runbook-d11-12.auto.golden` et `runbook-d11-12.refresh.golden` ne changent pas. `-update` est interdit dans cette PR.
- Aucune ligne de 12 → 13 n'entre dans `planHops` ni dans `runbookHops` : les tests ajoutent un saut de test et le retirent (`t.Cleanup`).
- Les tests tournent sous Linux, avec dash (le texte des blocs est exécuté). Depuis Windows :
  `MSYS_NO_PATHCONV=1 docker run --rm -v "D:/clients/casaos/_work/os-trixie:/src:ro" -v casaos-gomod:/go/pkg/mod -v casaos-gocache:/root/.cache/go-build golang:1.26 sh -c 'mkdir /w && cd /src && tar --exclude=.git -cf - . | tar -C /w -xf - && cd /w && go test ./pkg/osplan/... ./pkg/osrun/... ./service/... ./route/...'`
- Les commits n'ont pas de trailer `Co-Authored-By` : le CI de ReCasaOS les refuse.
- Le registre des phrases est celui du fichier : de l'anglais simple, au présent, sans abréviation.

## Review Focus

1. **Une box qui a fini 11 → 12 avec la beta** (son dossier a `done-after` et `begun-upgrade`, mais pas de fichier `hop`) : aucun bloc de 12 → 13 ne doit s'arrêter sur ce dossier. Couvert par le test de la tâche 2.
2. **Un dossier de 11 → 12 à moitié fait** (avec `apt-orig`, sans `done-after`) sur une box en bookworm : le texte de 12 → 13 l'ignore, et c'est le refus du cœur au démarrage (`osUpgradeManualFolder`, `service/system_os_upgrade_start.go:176`, qui n'est pas filtré par saut) qui garde la box. Ce refus ne change pas : un test le fixe dans la tâche 2.
3. **Un fichier `hop` illisible ou vide** dans un dossier : pour 12 → 13, le dossier compte comme celui d'un autre saut. Testé dans la tâche 2.
4. **Une phrase « Debian 11 » oubliée dans le texte de 12 → 13**, par exemple dans un `stop(...)` d'un bloc rarement atteint. Le test de la tâche 1 lit le texte entier des trois constructeurs (`BuildRunbook`, `BuildAuto`, `BuildRefresh`).
5. **Une ligne STOP de 12 → 13 sans classe** (« this machine is not Debian 12 » vient maintenant du bloc 0 du nouveau saut) : le test de la tâche 1 classe tout le texte auto du saut de test.

---

### Task 1 : les noms des releases viennent du saut

**Files:**
- Modify: `pkg/osplan/rewrite.go:21-46` (`HopSpec`, `planHops`)
- Modify: `pkg/osplan/runbook.go` (les 50 occurrences de « Debian 11 » et « Debian 12 », `debian11to12Blocks` renommé `debianBlocks`)
- Modify: `pkg/osplan/auto_refresh.go:44,117,123,159`
- Modify: `pkg/osplan/auto_class.go:103-104`
- Test: `pkg/osplan/hop_text_test.go` (nouveau)

**Interfaces:**
- Produces: `HopSpec.FromVersion`, `HopSpec.ToVersion string` (« 11 », « 12 ») ; `func (h HopSpec) FromName() string` et `ToName() string` (« Debian 11 ») ; `func provenance(c *rbCtx) string`, les lignes d'en-tête du runbook à la main qui disent ce qui a été mesuré et où ; `func withTestHop(t *testing.T) HopSpec` (helper de test, dans `hop_text_test.go`), qui ajoute aux deux tables `{ID: osreadiness.HopDebian12to13, From: "bookworm", To: "trixie", FromVersion: "12", ToVersion: "13", Later: []string{"forky", "duke"}, Firmware: true}` avec `debianBlocks`, et les retire au `Cleanup` ; `func planFor(t *testing.T, hop HopSpec) Plan`, le `realPlan(t)` avec `Hop` mis à `string(hop.ID)`.

- [ ] **Step 1 : écrire le test qui échoue**

`TestTheTextOfAHopNamesItsOwnReleases` dans `hop_text_test.go` :
- `hop := withTestHop(t)`, `p := planFor(t, hop)`.
- Pour le texte de `BuildRunbook(p)` privé de `provenance(...)`, de `BuildAuto(p, <les AutoParams de realAuto>)` et de `BuildRefresh(<les RefreshInputs du test de la refresh existant, avec ce saut>)` : `strings.Contains(text, "Debian 11")` est faux, `strings.Contains(text, "bullseye")` est faux.
- Le runbook à la main contient `"write the sources of Debian 13, and read the plan again"` et `"# Upgrade of the operating system by hand: Debian 12 (bookworm) to Debian 13 (trixie)."`.
- `classify(auto.Text)` ne rend pas d'erreur.

- [ ] **Step 2 : le lancer et le voir échouer**

Run : la commande des Global Constraints, avec `go test ./pkg/osplan -run TheTextOfAHopNamesItsOwnReleases -v`.
Expected : FAIL à la compilation (`FromVersion` inconnu).

- [ ] **Step 3 : ajouter les champs et tirer les phrases du saut**

Remplir `FromVersion: "11", ToVersion: "12"` dans la ligne 11 → 12. Chaque occurrence passe par `c.hop.FromName()` ou `c.hop.ToName()`. « Debian 11 and 12 are mixed » devient `c.hop.FromName() + " and " + c.hop.ToVersion + " are mixed"`, ce qui donne le même texte. Les chaînes construites hors de tout `rbCtx` (`otherPlans()`, `forwardWords` et les constantes du même genre) prennent le saut en paramètre.

La règle de tri, à appliquer phrase par phrase : une phrase sur **la machine en face du texte** utilise le saut. Une phrase sur **l'origine d'un fait** (le guest mesuré, un chroot de Debian 11, apt 2.2.4, les notes de version de Debian 12 dont vient l'échelle de réparation) garde sa release littérale et passe dans `provenance(c)`. Aujourd'hui, cela concerne les lignes 289 à 299 du texte écrit par `runbook.go`. Les commentaires Go ne changent pas.

Dans `auto_class.go`, les deux clés `"this machine is not Debian 11"` et `"this machine is not Debian 12"` deviennent une seule clé `"this machine is not Debian"`, de même classe (`osrun.ClassIdentity`). C'est un préfixe, et aucune autre clé ne commence ainsi.

- [ ] **Step 4 : tout lancer**

Run : la commande des Global Constraints.
Expected : PASS, y compris `TestTheRunbookOfTheRealBoxIsTheGolden` et les deux autres golden, sans `-update`.

- [ ] **Step 5 : commit**

```bash
git add pkg/osplan
git commit -m "refactor(osplan): the texts of the blocks name the releases of their hop"
```

### Task 2 : les dossiers de sauvegarde sont rangés par saut

**Files:**
- Modify: `pkg/osplan/runbook.go:122-131` (`backupFoldersScopedByHop`), `:430` (`bkdir`), `:561-583` (`otherPlans`), `:611-635` (`head`), `done()`
- Test: `pkg/osplan/hop_folders_test.go` (nouveau) ; `service/system_os_upgrade_start_test.go` (un cas)

**Interfaces:**
- Consumes: `withTestHop`, `planFor`, `debianBlocks` (tâche 1).
- Produces: `func (c *rbCtx) mkBK() string`, qui remplace la constante `bkdir` partout où un bloc crée `$BK`. Pour le saut 11 → 12, elle rend exactement `bkdir`. Pour tout autre saut, elle rend `bkdir` suivi de `printf '%s\n' '<hop id>' > "$BK/hop"\n`. `func (c *rbCtx) sameHop(v string) string` rend `""` pour 11 → 12, et pour un autre saut `  [ "$(cat "$` + v + `/hop" 2>/dev/null)" = '<hop id>' ] || continue\n`. Elle se place en première ligne du corps des deux boucles `for … in /var/backups/casaos-os-upgrade-*`.

- [ ] **Step 1 : écrire les tests qui échouent**

`TestABlockOfAnotherHopIgnoresTheFoldersOfTheHopBefore` dans `hop_folders_test.go`. Sur le modèle de `TestNoBlockRunsOnAMachineThatAnotherPlanTookToItsEnd` (`runbook_states_test.go:155`), avec `withTestHop(t)`, `buildFor(t, planFor(t, hop))`, `os-release` mis à `VERSION_CODENAME=bookworm` après `setupVerify` :
- `others/casaos-os-upgrade-p1-0123456789ab/` contient `begun-upgrade`, `done-minimal`, `done-after`, sans fichier `hop` (une box finie en 11 → 12) : le bloc `verify` sort en 0, sans « the hop is over » ni « another plan of this machine ».
- Le même dossier avec `hop` contenant `debian-12-13` et `done-after` : le bloc `timers` s'arrête avec `"STOP: the hop is over for this machine: the plan whose folder is ./others/casaos-os-upgrade-p1-0123456789ab took it to the end"`.
- Le même dossier avec `hop` vide et `begun-upgrade` : le bloc `verify` sort en 0.
- Après le bloc `verify`, `bk/hop` vaut `"debian-12-13\n"`.

`TestTheBlocksOf11To12WriteNoHopFile` : avec `realRunbook(t)`, après le bloc `verify`, `bk/hop` n'existe pas.

Dans `service/system_os_upgrade_start_test.go`, `TestAHalfDoneFolderOfAnyHopStillStopsTheStart` : un dossier `casaos-os-upgrade-x` avec `apt-orig`, sans `done-after` ni `hop`. `osUpgradeManualFolder(nil)` rend `"casaos-os-upgrade-x"`. Ce test fixe un comportement déjà en place.

- [ ] **Step 2 : les lancer et les voir échouer**

Run : `go test ./pkg/osplan -run 'OfAnotherHop|WriteNoHopFile' -v` et `go test ./service -run HalfDoneFolderOfAnyHop -v`, avec la commande des Global Constraints.
Expected : FAIL pour le premier (« the hop is over » sur le dossier de 11 → 12), PASS pour les deux autres.

- [ ] **Step 3 : implémenter `mkBK` et `sameHop`, puis basculer le garde-fou**

`done(id, n)` devient une méthode de `rbCtx` (ou reçoit `c`), pour que le bloc 0 écrive le fichier `hop`. `head()` utilise `c.mkBK()`. Les boucles de `head()` et `otherPlans()` reçoivent `c.sameHop("fin")` et `c.sameHop("d")`. Dans le commentaire de `head()`, la phrase « Only one hop has a runbook… » dit maintenant la règle du fichier `hop`, et `backupFoldersScopedByHop = true` reçoit un commentaire qui dit la même chose.

- [ ] **Step 4 : tout lancer**

Run : la commande des Global Constraints.
Expected : PASS, golden 11 → 12 inchangés, et `TestASecondHopNeedsTheBackupFoldersScopedByHop` passe.

- [ ] **Step 5 : commit**

```bash
git add pkg/osplan service
git commit -m "feat(osplan): a hop other than 11 to 12 marks its backup folder and looks only at its own"
```

### Task 3 : la PR

- [ ] **Step 1 :** `git diff inkly/main --stat -- pkg/osplan/testdata` ne liste rien.
- [ ] **Step 2 :** pousser `feat/os-upgrade-trixie` sur `inkly` et ouvrir la PR « refactor(osplan): a second Debian hop can use the blocks », qui décrit les deux tâches et la preuve des golden, sans la ligne « Generated with Claude Code » (le CI la refuse aussi).
