# Upgrade OS Ubuntu, PR 1 : la distribution vient du saut — plan d'implémentation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** que le texte des blocs puisse nommer Ubuntu sans rien changer de ce qu'il écrit pour les deux sauts Debian.

**Architecture:** `HopSpec` gagne `Distro`. `FromName()`, `ToName()`, les trois phrases qui disent « Debian » en dur, la clé STOP d'identité, l'adresse Docker du retour de la source et une ligne de `provenance()` passent par lui. Aucun saut Ubuntu n'entre dans les tables : les tests en ajoutent un et le retirent.

**Tech Stack:** Go 1.26, du shell exécuté sous dash dans les tests (`shellBox`).

**Spec:** `docs/superpowers/specs/2026-11-08-os-upgrade-ubuntu-design.md`, section 1.

## Global Constraints

- Les six golden `pkg/osplan/testdata/runbook-d11-12{,.auto,.refresh}.golden` et `runbook-d12-13{,.auto,.refresh}.golden` ne changent pas. `-update` est interdit dans cette PR.
- Aucune ligne Ubuntu n'entre dans `planHops` ni dans `runbookHops`. Le saut de test est ajouté puis retiré au `t.Cleanup`.
- Tests sous Linux, avec dash. Depuis Windows :
  `MSYS_NO_PATHCONV=1 docker run --rm -v "D:/clients/casaos/_work/os-ubuntu:/src:ro" -v casaos-gomod:/go/pkg/mod -v casaos-gocache:/root/.cache/go-build golang:1.26 sh -c 'mkdir /w && cd /src && tar --exclude=.git -cf - . | tar -C /w -xf - && cd /w && go test ./pkg/osplan/... ./pkg/osrun/... ./service/... ./route/...'`
- Les commits n'ont ni trailer `Co-Authored-By` ni ligne « Generated with » : le CI de ReCasaOS les refuse.
- Le registre des phrases est celui du fichier : anglais simple, présent, sans abréviation.

## Review Focus

1. **Un octet de Debian qui bouge** : les golden le disent. Une phrase qui ne change que pour Ubuntu passe par `c.hop.Distro`, jamais par une réécriture des chaînes de Debian.
2. **La clé STOP** : `auto_class.go` classe par préfixe. `"this machine is not Ubuntu"` doit être classée `ClassIdentity` comme celle de Debian, et le test du saut de test passe tout le texte auto à `classify`.
3. **L'adresse Docker** dans le texte à la main (`runbook.go:1458`) **et** dans celui de l'unité (`auto.go:327-337`) : les deux, ou le bloc 8 de l'unité demanderait `linux/debian/dists/resolute`.
4. **`provenance()`** : pour Ubuntu, elle ne doit pas dire que les notes de version prescrivent ces commandes.

---

### Task 1 : `Distro` dans `HopSpec`

**Files:**
- Modify: `pkg/osplan/rewrite.go:22-56` (`HopSpec`, `FromName`, `ToName`, les deux lignes de `planHops`)
- Modify: `pkg/osplan/rewrite_test.go:19`, `rewrite_deb822_test.go:13` (les `HopSpec` littéraux des tests gagnent `Distro: "Debian"`)
- Test: `pkg/osplan/hop_text_test.go`

**Interfaces:**
- Produces: `HopSpec.Distro string` ; `FromName()` = `h.Distro + " " + h.FromVersion` ; `func withUbuntuTestHop(t *testing.T) HopSpec` (dans `hop_text_test.go`), qui ajoute aux deux tables `{ID: osreadiness.HopUbuntu2404to2604, Distro: "Ubuntu", From: "noble", To: "resolute", FromVersion: "24.04", ToVersion: "26.04", Deb822: true}` avec `debianBlocks`, et les retire au `Cleanup`.

- [ ] **Step 1 : le test qui échoue.** `TestTheTextOfAnUbuntuHopNamesUbuntu` : `hop := withUbuntuTestHop(t)` et `p := planFor(t, hop)`. Dans `BuildRunbook(p)`, `BuildAuto(...)` et `BuildRefresh(...)` (mêmes entrées que `TestTheTextOfAHopNamesItsOwnReleases`), `"Debian"` n'apparaît pas hors de `provenance(...)`. Le runbook contient `"# Upgrade of the operating system by hand: Ubuntu 24.04 (noble) to Ubuntu 26.04 (resolute)."`, et `classify(auto.Text)` ne rend pas d'erreur.
- [ ] **Step 2 : le lancer.** Il échoue à la compilation (`Distro` inconnu).
- [ ] **Step 3 : le champ.** Ajouter `Distro: "Debian"` aux deux lignes de `planHops` et aux littéraux des tests, puis changer `FromName` et `ToName`.
- [ ] **Step 4 : tout lancer.** Le nouveau test échoue encore sur les phrases de la tâche 2. Les golden passent.

### Task 2 : les phrases, la clé STOP, Docker, la provenance

**Files:**
- Modify: `pkg/osplan/runbook.go:282` (« Sources that are not Debian's »), `:1135`, `:301-312` (`provenance`), `:1458` (adresse Docker)
- Modify: `pkg/osplan/auto.go:327-337` (adresse Docker du texte de l'unité, si elle y est écrite ; sinon, la fonction qui la construit)
- Modify: `pkg/osplan/auto_class.go:103`

- [ ] **Step 1** : `"not Debian's"` devient `"not " + c.hop.Distro + "'s"`, et de même à `:1135`.
- [ ] **Step 2** : l'adresse Docker devient `"https://download.docker.com/linux/" + strings.ToLower(c.hop.Distro) + "/dists/" + c.hop.To + "/InRelease"`, aux deux endroits.
- [ ] **Step 3** : `auto_class.go` ajoute `{"this machine is not Ubuntu", osrun.ClassIdentity}` à côté de la clé de Debian. Vérifier qu'aucune autre clé ne commence ainsi.
- [ ] **Step 4** : dans `provenance()`, la ligne « The release notes of %s prescribe the same commands in the same order » n'est écrite que pour Debian. Pour Ubuntu, la ligne dit que Canonical fait ce saut avec son propre outil, `do-release-upgrade`, et non avec ces commandes, et que ce texte n'a jamais été mesuré sur Ubuntu. Les lignes qui parlent de la mesure (Debian 11, son guest, son chroot) gardent leur release littérale : c'est une provenance.
- [ ] **Step 5 : tout lancer.** Tout passe, y compris les six golden, sans `-update`.
- [ ] **Step 6 : commit.**

```bash
git add pkg/osplan
git commit -m "refactor(osplan): the texts of the blocks name the distribution of their hop"
```

### Task 3 : les autres paquets ne disent pas « Debian » à la place du saut

**Files:**
- Read: `pkg/osrun`, `service/system_os_upgrade_*.go`, `route/v1/system_os_upgrade*.go`

- [ ] **Step 1** : `git grep -n '"[^"]*Debian' pkg/osrun service route`, en ignorant les commentaires et les tests. Chaque phrase envoyée à l'interface ou au journal qui nomme Debian pour la machine en face passe par le saut, ou par `ossupport` (le `PrettyName` de la release).
- [ ] **Step 2** : s'il y en a, un test par phrase, avec le saut de test Ubuntu. S'il n'y en a pas, la PR le dit dans sa description.
- [ ] **Step 3 : commit**, s'il y a lieu.

### Task 4 : PR

- [ ] Pousser `feat/os-upgrade-ubuntu` sur `inkly` et ouvrir la PR en brouillon. Le corps cite la spec et dit « golden Debian inchangés ».
