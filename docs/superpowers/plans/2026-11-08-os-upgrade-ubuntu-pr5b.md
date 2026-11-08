# Upgrade OS Ubuntu, PR 5b : les contrôles du saut 24.04 → 26.04 — plan

**Ce qui a été lu :** `DistUpgradeQuirks.py` sur la branche `ubuntu/resolute-updates` d'`ubuntu-release-upgrader`. La spec est corrigée (section 4) : `boot_partition` et `dbus_broker` n'y sont pas.

## Fait

- `ssh_dsa_hostkey` porte aussi sur `HopUbuntu2404to2604`, puisque 26.04 a OpenSSH 10.2p1. Son texte ne nomme plus « Debian 13 » mais « OpenSSH 10: Debian 13, Ubuntu 26.04 ». Un scénario couvre une box Ubuntu 24.04 qui n'a qu'une clé DSA.
- Toute la suite `osplan` et `osrun` passe dans l'image `ubuntu:26.04`, avec rust-coreutils 0.10.0 et dash. Le Go 1.26 vient d'un volume rempli par l'image `golang:1.26`.

## Écarté

- **`netplan*` dans les paquets protégés :** la liste shell est commune, donc les golden Debian changeraient, et apt n'a pas de raison de retirer `netplan.io`.
- **MySQL et RabbitMQ :** déjà refusés par `host_services`.

## Reste à la page (PR 7)

Les phrases de `osReadiness.js` et des langues, qui disent « Debian 13 » pour `ssh_dsa_hostkey`.
