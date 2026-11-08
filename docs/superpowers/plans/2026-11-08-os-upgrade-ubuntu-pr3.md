# Upgrade OS Ubuntu, PR 3 : Canonical offre le saut — plan d'implémentation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** qu'un plan Ubuntu ne se fasse que si Canonical offre le saut, en lisant `https://changelogs.ubuntu.com/meta-release-lts` comme `do-release-upgrade` le fait.

**Architecture:**
- Un nouveau code, `not_offered`, entre dans `ErrorCodes`.
- `Run` appelle `checkOffered` juste après `Preflight` et avant de toucher aux sources. Le bac à sable n'est donc pas créé pour un saut refusé.
- La lecture se fait en Go, par `net/http`, avec 30 s et 1 Mio au plus. `Options.MetaRelease` la remplace dans les tests.
- La dernière strophe dont `Dist` est la cible décide :
  - pas de strophe : refus, `reason: absent` ;
  - une strophe qui ne dit pas `Supported: 1` : refus, `reason: unsupported`.
- Un fichier qu'on ne peut pas obtenir, ou qui n'est pas fait de strophes, donne aussi `not_offered`, avec `unknown: true` : le plan échoue fermé.
- Un saut Debian ne demande rien à Canonical.

**Spec:** `docs/superpowers/specs/2026-11-08-os-upgrade-ubuntu-design.md`, section 3.

## Écart à la spec

La spec voulait, pour un fichier qui ne répond pas, un refus « de classe réseau ». C'est `not_offered` avec `unknown: true`, comme `busy` et `no_room` quand ils ne savent pas : un code de plus pour la page, et non deux.

## Limite connue

Le client de Go lit le proxy de l'environnement du cœur, et non celui d'apt. Une box qui ne sort que par le proxy d'apt est refusée avec `unknown`. Le code porte une ligne `shortcut:` qui le dit.

## Tâches

- [ ] `offered.go` : `MetaReleaseLTS`, `fetchMetaRelease`, `checkOffered` et `offered`.
- [ ] `failure.go` : `CodeNotOffered`. `run.go` : `Options.MetaRelease`, et l'appel après `Preflight`.
- [ ] `offered_test.go` :
  - Le fichier réel du 2026-11-08 (`testdata/real/ubuntu-meta-release-lts-2026-11-08.txt`) offre les deux sauts.
  - Les quatre refus : `absent`, `unsupported`, une strophe plus tardive qui décide, et un fichier qui n'est pas fait de strophes.
  - `Run` sur une box Ubuntu 24.04 : le saut offert va jusqu'au bac à sable ; le saut non offert et le fichier illisible sont refusés avant lui.
  - Un saut Debian ne demande rien.
- [ ] La PR, en brouillon, avec pour base `feat/os-upgrade-ubuntu-sources` (PR 2).
