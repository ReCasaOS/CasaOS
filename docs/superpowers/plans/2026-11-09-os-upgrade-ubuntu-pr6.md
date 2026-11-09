# Upgrade OS Ubuntu, PR 6 : le saut 22.04 → 24.04 — plan

**Ce qui a été lu :** `DistUpgradeQuirks.py` sur la branche `ubuntu/noble-updates` d'`ubuntu-release-upgrader`, c'est l'outil qui fait le saut vers 24.04 (lu le 2026-11-09).

## Fait

- La ligne `HopUbuntu2204to2404` dans `planHops` et `runbookHops` (`aptBlocks`).
  - `Later` : oracular, plucky, questing, resolute.
  - `OneStep` : la mise à niveau entière au bloc 5, comme le saut vers 26.04 l'a appris de la preuve QEMU (#117).
  - Pas de `SimBroken` : dans l'image `ubuntu:22.04`, la simulation complète passe (`testdata/real/docker-u22-24-*`).
  - Les mises à jour progressives et le filtre de sshd viennent de la distribution, sans rien de plus.
- Les golden `runbook-u22-24.*` et un plan fait avec les vraies sorties d'apt de l'image : 90 mises à jour, 14 nouveaux paquets, 9 retirés (renommages t64 et usrmerge), que dpkg connaît et dont aucun n'arrête le plan.
- Les tests qui prenaient 22.04 pour un saut sans plan prennent `debian-13-14`. Celui du refus `hop_not_planned` par l'identité est retiré : plus aucune release reconnue n'est sans plan.

## Les quirks de noble, et ce qu'on en fait

| Quirk | Ici |
|---|---|
| `_test_and_fail_on_unmerged_usr` | `merged_usr` (existant, tous les sauts) |
| `_test_and_fail_rabbitmq_server_installed` (jammy seulement) | `host_services` |
| `_test_and_fail_on_tpm_fde`, i386, POWER8, armhf | `root_luks`, hors amd64 |
| `_install_t64_replacement_packages` | apt le résout seul dans la simulation de l'image : les 9 retraits sont ces renommages |
| `_install_linux_metapackage` | la garde du noyau des blocs 7 et 8 |
| `_disable_stub_resolver` (jammy) | le saut en une étape n'a plus besoin du réseau après son point de non-retour (F3 de la preuve du saut vers 26.04) |
| `_set_grub_cloud_style_installation_on_cloud_installs` | à voir dans la preuve QEMU sur l'image cloud 22.04 |
| `_test_and_fail_on_aufs` (répertoires aufs de Docker) | écarté : Docker 24 et suivants n'ont plus aufs |
| `_handle_ufw_breaks`, `_maybe_remove_gpg_wks_server`, `_disable_cloud_init`, snaps | le plan montre ce qu'apt retire ou ajoute ; apt ne touche pas aux snaps |

`apt_key_weak` et `ssh_socket`, que la spec prévoyait, ne sont pas parmi les quirks de noble : pas de contrôle ajouté.

## Reste

- La jambe `os-upgrade-proof` du saut, sur l'image cloud 22.04, après la pré-release.
