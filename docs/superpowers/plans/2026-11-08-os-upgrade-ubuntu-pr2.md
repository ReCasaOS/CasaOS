# Upgrade OS Ubuntu, PR 2 : les sources d'Ubuntu — plan d'implémentation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** que la réécriture des sources lise une machine Ubuntu (22.04 et 24.04), avec les vrais fichiers des images, et refuse ce qu'elle ne sait pas faire : Ubuntu Pro et l'archive d'une autre distribution.

**Architecture:** la réécriture est déjà générique : les suites `-updates` et `-security` passent à la cible et `-backports` est retiré, en `.list` comme en deb822. Cette PR ajoute quatre choses :
- un refus `ubuntu_pro` pour `esm.ubuntu.com`, lu avant le Decider ;
- un refus `other_distro` pour une ligne de l'archive d'une autre distribution que celle du saut ;
- la reconnaissance des hôtes de l'archive d'Ubuntu par leur nom, dans `archiveHost`, comme ceux de Debian ;
- les fixtures des images.

Comme dans la PR 1, aucun saut Ubuntu n'entre dans les tables.

**Tech Stack:** Go 1.26.

**Spec:** `docs/superpowers/specs/2026-11-08-os-upgrade-ubuntu-design.md`, section 2.

## Global Constraints

- Les six golden Debian ne changent pas, et `-update` est interdit.
- Les fixtures sont les fichiers des images `ubuntu:22.04` (`sha256:076e7bf8…`) et `ubuntu:24.04` (`sha256:fe815609…`), copiés tels quels dans `pkg/osplan/testdata/real/`. Ceux des images cloud viendront de la jambe QEMU (PR 4).
- Pas de trailer `Co-Authored-By` ni de ligne « Generated with ».

## Review Focus

1. **ESM avant le Decider** : le Release d'`esm.ubuntu.com` dit `Origin: UbuntuESMApps`, que `distroOrigin` ne compte pas comme celui de la distribution. Sans le refus, la ligne serait mise de côté comme une source tierce, puis remise au bloc 8 avec la suite de l'ancienne release.
2. **`archiveHost`** : seuls `archive.ubuntu.com`, ses sous-domaines (miroirs de pays et de cloud) et `security.ubuntu.com` sont reconnus par leur nom. `esm`, `ports`, `old-releases` et les autres continuent de passer par le fichier Release.
3. **`other_distro`** : le refus porte sur le nom de l'hôte, pour les deux sens. Une ligne de Debian lue sur une machine Debian n'est pas concernée.

### Task 1 : le Decider reconnaît l'archive d'Ubuntu

- [ ] `archiveHost` gagne `(?:[a-z0-9-]+\.)*archive\.ubuntu\.com|security\.ubuntu\.com`.
- [ ] Le test du Decider gagne trois cas qui répondent sans fichier Release : `archive.ubuntu.com`, `fr.archive.ubuntu.com` et `security.ubuntu.com`. Il gagne aussi deux cas qui ne sont pas reconnus par leur nom : `esm.ubuntu.com` et `ppa.launchpadcontent.net`.

### Task 2 : les deux refus

- [ ] `reasonText` gagne `ubuntu_pro` et `other_distro`.
- [ ] Le refus `ubuntu_pro` est lu en `.list` avant `decider.Suite`, et en deb822 dans la boucle des adresses de `readDeb822File`.
- [ ] Le refus `other_distro` s'applique aux lignes de la distribution : en `.list` avant `distroProblem`, et en deb822 dans `rewriteDeb822`, après `deb822Problem`.

### Task 3 : les tests des images

Fichier `pkg/osplan/rewrite_ubuntu_test.go`, avec `jammyToNoble` et `nobleToResolute`.

- [ ] **22.04 vers 24.04** : les neuf lignes de l'archive passent à noble, la ligne backports est retirée, et les `deb-src` commentés ne bougent pas.
- [ ] **24.04 vers 26.04** : `Suites: noble noble-updates noble-backports` devient `resolute resolute-updates` (`suite,dropped`), et `noble-security` devient `resolute-security`.
- [ ] **Docker pour Ubuntu**, en `docker.list` et en `docker.sources` : le fichier est mis de côté, avec `Docker: true`.
- [ ] **Ubuntu Pro**, en `.list` (22.04) et en `.sources` (24.04) : refus `ubuntu_pro`.
- [ ] **Autre distribution** : `deb.debian.org` dans un saut Ubuntu est refusé, en `.list` et en deb822 ; `archive.ubuntu.com` dans un saut Debian aussi.
- [ ] **Refresh** : les sources de 22.04 restent telles quelles.

### Task 4 : PR

- [ ] Pousser `feat/os-upgrade-ubuntu-sources` et ouvrir la PR en brouillon, avec pour base `feat/os-upgrade-ubuntu` (PR 1).
