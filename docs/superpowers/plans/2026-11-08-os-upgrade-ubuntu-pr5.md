# Upgrade OS Ubuntu, PR 5 : le saut 24.04 → 26.04 — plan

**Goal:** le saut 24.04 → 26.04 entre dans les tables. Le plan et le runbook laissent passer le passage de coreutils à rust-coreutils, et rien d'autre.

**Spec:** sections 6 et Risques de `docs/superpowers/specs/2026-11-08-os-upgrade-ubuntu-design.md`.

## Ce qui entre

- `planHops` gagne la ligne `{HopUbuntu2404to2604, Ubuntu, noble → resolute, 24.04 → 26.04, Later: stonking, tenacious, Deb822, SimBroken: coreutils-from-uutils}`.
  - Les noms de code sont ceux de `distro-info-data` 2026.10.12, lus dans l'image `ubuntu:26.04`.
- `runbookHops` donne au saut les mêmes blocs que pour Debian. `debianBlocks` est renommé `aptBlocks`.
- `HopSpec.SimBroken` : la simulation d'apt bute sur ces paquets alors que le vrai saut passe (vu dans l'image `ubuntu:24.04`, PR 4).
  - **Côté Go, `withoutSimBroken`**, appliqué à `-s dist-upgrade` dans `Run`. Il n'agit qu'en code 100, quand chaque `E:` est le `Conf Broken` d'un de ces paquets. Il retire alors ces erreurs, la ligne `Conf X:arch broken` et les lignes de debug qui la suivent, et met le code à 0. Dans tous les autres cas, la sortie ne change pas.
  - **Côté shell, `sim_ok STATUS FILE`**, défini avec `shSim` pour ce saut seulement, appelé par `aptq -s dist-upgrade > F 2>&1 || sim_ok $? F || STOP`. Il porte sur `resim.txt` (bloc 3), `after-minimal.txt` (bloc 5) et `after-repair.txt` (l'échelle de réparation). La boucle de rattrapage du bloc 6 se termine déjà par `|| true`.
- Les golden `runbook-u24-26{,.auto,.refresh}.golden` sont faits du plan de la box mesurée, avec les sources de l'image 24.04 et `docker.sources` pour Ubuntu.

## Ce qui n'entre pas (PR 5b)

- Les contrôles de la readiness :
  - `ssh_dsa_hostkey` étendu à ce saut (son texte dit « Debian 13 ») ;
  - `boot_partition` ;
  - `ubuntu_pro`, que le plan refuse déjà par les sources (PR 2).
- `dbus-broker`, que le releaser force (`HopSpec.Install`), et `netplan*` dans les paquets protégés.
- Le texte des blocs 7 et 8 exécuté sous `ubuntu:26.04`, avec rust-coreutils.

## Critères

- Les six golden Debian sont identiques, et aucun texte Debian n'a `sim_ok`.
- `Run`, avec les vraies sorties d'apt de l'image, donne un plan : 90 mis à jour, 13 nouveaux, tailles connues.
- `sim_ok`, exécuté sous `sh`, ne laisse passer ni une autre erreur, ni un autre paquet, ni un autre code.
- Le bloc 8 trouve resolute dans `ubuntu.sources`.
