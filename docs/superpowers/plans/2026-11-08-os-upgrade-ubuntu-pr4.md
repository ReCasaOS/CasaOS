# Upgrade OS Ubuntu, PR 4 : la vraie sortie d'apt sur Ubuntu — plan

**Goal:** que le parseur ait lu une vraie sortie d'apt d'Ubuntu avant que la PR 5 ne mette un saut Ubuntu dans les tables.

**Écart à la spec :** la spec mettait ici une jambe `os-plan-proof`. Elle passe par le plan du cœur, qui répond `hop_not_planned` tant que la ligne du saut n'existe pas. Les fixtures viennent donc des images Docker, et les jambes QEMU passent après la PR 5, sur une pré-release.

## Ce qui a été fait (2026-11-08)

Dans `ubuntu:22.04` (apt 2.4.14) et `ubuntu:24.04` (apt 2.8.3), les sources ont été réécrites comme le plan les écrit, backports retiré. Puis, sous `LC_ALL=C` :
- `apt-get update --error-on=any` ;
- `-s upgrade` ;
- `-s dist-upgrade` ;
- `-o APT::Get::Trivial-Only=true dist-upgrade`.

Chaque sortie est gardée telle quelle, avec son code de sortie : `testdata/real/docker-u22-24-*` et `docker-u24-26-*`.

- **22.04 → 24.04 :** tout se lit. Le saut retire 9 paquets : les renommages t64 et usrmerge.
- **24.04 → 26.04 :** `-s dist-upgrade` finit en code 100 sur `E: Conf Broken coreutils-from-uutils:amd64` ; le reste se lit.
  - Le vrai saut passe pourtant, dans la même image : `upgrade --without-new-pkgs` puis `dist-upgrade` finissent en code 0, en 26.04.1, avec `dpkg --audit` propre.
  - La simulation refaite après le saut minimal bute aussi.
  - Un test fige ce refus, pour que la PR 5 le voie changer.

## Tâches

- [x] Les fixtures.
- [x] `parse_ubuntu_test.go`.
- [ ] La PR, en brouillon, avec pour base `feat/os-upgrade-ubuntu-offer` (PR 3).
