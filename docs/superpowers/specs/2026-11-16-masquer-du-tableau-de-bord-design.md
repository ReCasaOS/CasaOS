# Masquer une app du tableau de bord — conception

Issue : ReCasaOS/CasaOS#156 (détachée de #150, demande de njmike73).

## Intention

Certaines apps sont des services de fond sans rien à ouvrir (cloudflared-web). Leur icône
encombre le tableau de bord et invite à y toucher. L'admin doit pouvoir la masquer, pour
toute la box, lui compris (propriétaire).

## Buts, non-buts

- But : un réglage par app, « Masquer du tableau de bord », valable pour tous les comptes.
- But : l'admin retrouve l'app masquée sans quitter le tableau de bord.
- Non-but : protéger l'app. Elle reste dans l'API, les journaux, les sauvegardes, les mises à
  jour. Cacher une app à un compte reste le rôle de la visibilité par compte (#150).
- Non-but : les conteneurs hors compose et les apps v1, qui n'ont pas d'onglet Visibility.

## Approches

1. **Étendre la visibilité existante** (retenue, propriétaire) : même onglet, même service.
2. Réglage dans le navigateur ou par compte : ne vaut pas « pour tout le monde ».
3. Champ dans le compose de l'app : perdu à la réinstallation, mélange affichage et définition.

## Dessin

- AppManagement : liste des apps masquées dans `/var/lib/casaos/dashboard-hidden.json`,
  **fichier à part de `visibility.json`** (mon choix) : ce dernier décide qui voit quoi, sa
  forme est lue strictement, et un ancien service qui ne saurait pas la lire montrerait de
  nouveau les apps cachées à un compte. Un choix d'affichage n'a pas à y toucher.
- Routes `GET` et `PUT /v2/app_management/dashboard-hidden/{app}`, réservées au propriétaire.
- La grille (`/web/appgrid`) porte `hidden_on_dashboard` sur l'app ; elle n'est jamais filtrée
  côté serveur, d'autres écrans la lisent pour compter ou gérer les apps.
- Une désinstallation retire l'app de la liste (mon choix) : une app réinstallée qui
  n'apparaît pas serait un piège, et rien de privé n'en dépend.
- Interface : interrupteur en haut de l'onglet Visibility, enregistré aussitôt, indépendant
  des cases par compte. Le tableau de bord (classique et nouveau) retire la tuile, les
  favoris et les compteurs de filtres suivent. Sous la grille, pour l'admin seulement :
  « Apps masquées : 2 · Afficher », qui les dessine grisées avec leur menu habituel.
- L'app masquée garde sa place dans l'ordre enregistré ; après un glisser-déposer fait sans
  elles, les apps masquées passent en fin de liste (mon choix, le plus simple).
