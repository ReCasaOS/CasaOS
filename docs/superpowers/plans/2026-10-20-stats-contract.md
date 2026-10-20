# Statistiques: contrat commun aux cinq plans

Spec: `docs/superpowers/specs/2026-10-20-stats-design.md`. Ce document fixe les **noms et les formes** que les cinq plans (Common, AppManagement, virt, cœur, tableau de bord) utilisent tous. Un plan ne change aucun nom d'ici ; s'il manque quelque chose, il l'ajoute dans sa propre section « Extensions » et le dit.

Ordre de construction: **Common d'abord** (paquet `pkg/stats`, étiqueté **v0.4.28**, branche créée depuis `inkly/main` de CasaOS-Common), puis AppManagement, virt et cœur (qui montent leur `go.mod` sur ce tag, indépendants entre eux; tant que le tag n'existe pas, un `replace` local **non commité** est toléré, jamais commité), puis le tableau de bord (qui lit les routes des trois).

## 1. Paquet partagé `github.com/ReCasaOS/CasaOS-Common/pkg/stats`

Un seul code pour le stockage, les moyennes, la lecture par période et les règles d'alerte; les trois services ne font que **mesurer** et **appeler** ce paquet.

```go
package stats

// Range est une période demandée par la page.
type Range string
const (RangeLive Range = "live"; Range15m Range = "15m"; Range1h Range = "1h"; Range24h Range = "24h"; Range7d Range = "7d")
func ParseRange(s string) (Range, bool)

// Sample est une mesure à un instant: clé "graphique/série" -> valeur. Une clé absente = pas de donnée (jamais 0).
type Sample map[string]float64

// Point: t en secondes Unix; Avg/Min/Max nil = trou (la courbe s'interrompt).
type Point struct { T int64; Avg, Min, Max *float64 }   // JSON: [t, avg, min, max] avec null

// Les étiquettes JSON sont dans Common (les services embarquent ces types tels quels).
type SeriesData struct {
	Chart  string  `json:"chart"`
	Name   string  `json:"name"`
	Unit   string  `json:"unit"`
	Points []Point `json:"points"`
}
type Result struct {
	Range       Range        `json:"range"`
	StepSeconds int          `json:"step_seconds"`
	From        int64        `json:"from"`
	To          int64        `json:"to"`
	Series      []SeriesData `json:"series"` // jamais nil: un élément vide donne "series":[]
}

type Clock interface{ Now() time.Time }

// Store garde, par élément, les 15 dernières minutes en mémoire (pas de 5 s) et 7 jours de minutes sur disque (fichier en anneau).
type Store struct{ /* ... */ }
func Open(dir string, clock Clock) (*Store, error)               // dir = /var/lib/casaos/stats/<service>
func (s *Store) Record(item string, at time.Time, smp Sample) error  // appelé toutes les 5 s; écrit un enregistrement quand une minute est complète
func (s *Store) Query(item string, r Range) (Result, error)
func (s *Store) Items() []string
func (s *Store) Forget(item string, at time.Time) error          // l'élément n'existe plus: son historique est gardé 7 jours puis supprimé
func (s *Store) Sweep(now time.Time) error                       // supprime les historiques oubliés depuis plus de 7 jours; ne supprime, dans le dossier, que ses propres `*.ring.tmp` et `*.ring.bad`
func (s *Store) SetOnMinute(fn func(item string, minute time.Time))  // appelé une fois par élément et par minute écrite (voir « Câblage »)
func (s *Store) Close() error
```

- **Enregistrement d'une minute**: pour **chaque série**, moyenne, minimum et maximum des mesures de la minute. Fichier en anneau de taille fixe (10 080 minutes), écriture atomique (au pire la dernière minute est perdue), lisible sans verrou. Le format exact est au choix du plan Common; il est borné (de l'ordre de 3 Mo par élément, ≈ 115 MiB pour 41 éléments, au plus ≈ 6 Mo par élément) et versionné (en-tête avec un numéro de version). Bornes de nombre: **256 éléments vivants** (les éléments oubliés ne comptent pas) et au plus **256 éléments oubliés** gardés (le plus ancien oublié part d'abord); au-delà, `Record` d'un nouvel élément échoue avec `ErrTooManyItems`. `Open` et `Sweep` ne suppriment dans le dossier que `*.ring.tmp` et `*.ring.bad`, **jamais** un autre fichier (`alerts.json`, `alerts.json.tmp`... appartiennent à l'évaluateur).
- **Périodes**: `live` = 5 s sur 15 min (mémoire); `15m` = minutes sur 15 min; `1h` = minutes sur 60; `24h` = seaux de 5 min (≈ 288 points); `7d` = seaux de 30 min (≈ 336 points); pour les seaux: moyenne des moyennes, minimum des minimums, maximum des maximums.
- **Identité d'un élément** (`item`): chaîne choisie par le service, stable: conteneur d'application `app:<application>:<service>:<n>`; autre conteneur `container:<nom>`; VM `vm:<uuid>`; machine `host`. Le paquet traite `item` comme opaque (il en fait un nom de fichier sûr par hachage).
- **Clés de série** (`Sample`), identiques partout; seules celles qui existent pour l'élément sont présentes:
  - `cpu/user`, `cpu/system`, `cpu/iowait`, `cpu/throttled` (conteneurs, % d'**un cœur**); `cpu/total` (VM et machine, % d'un cœur); `cpu/vcpus` (VM, nombre, pas un pourcentage).
  - `memory/rss`, `memory/cache`, `memory/swap`, `memory/limit` (octets); VM: `memory/assigned`, `memory/used` (seulement si le pilote balloon le rapporte).
  - `network/rx:<interface>`, `network/tx:<interface>` (octets par seconde), `network/dropped:<interface>` (paquets par seconde).
  - `disk/read`, `disk/write` (octets par seconde, ou `disk/read:<disque>` pour une VM), `disk/iops_read`, `disk/iops_write`; machine: `fs/free_percent:<point de montage>`.
  - `pids/count`, `pids/fds` (conteneurs).
  - `restarts/count` (compteur cumulé, conteneurs), `health/ok` (1 ou 0: conteneur qui a une sonde de santé; **VM**: 1 en marche, 0 sinon, y compris arrêtée tant que son historique existe).
  - Séries d'alerte, non dessinées: `alert/cpu_high`, `alert/mem_high` (% de l'**allocation effective**, voir spec décision 7).
- **Clés de série permanentes**: un anneau ne libère jamais une série (48 au plus par élément); les suffixes (`:<interface>`, `:<disque>`, `:<point de montage>`) doivent donc être **stables** (adresse MAC, index, nom de périphérique du système invité), jamais un nom qui change à chaque démarrage (libvirt `vnetN`, interfaces veth de l'hôte).
- **Erreurs partielles**: une erreur de `Record` qui enveloppe `ErrSeriesCap` ou `ErrBadKey` est **partielle** (les autres clés ont été enregistrées): l'appelant ne réessaie pas et ne jette pas l'échantillon; `IsPartial(err)` le dit si le paquet l'exporte.
- Les unités et les noms affichés sont dans une table du paquet (`Unit(chart, name)`), partagée par les services. Vocabulaire des unités: `percent`, `bytes`, `bytes_per_second`, `packets_per_second`, `ops_per_second`, `count`, `flag` (`flag` = booléen 0/1, alias fixe côté tableau de bord).

### Règles d'alerte

```go
type Rule struct { Key string `json:"key"`; Enabled bool `json:"enabled"`; Threshold float64 `json:"threshold"`; Minutes int `json:"minutes"` }   // Key: "cpu_high" | "mem_high"
func DefaultRules() []Rule   // cpu_high 90 % 10 min; mem_high 90 % 5 min; tous actifs

type Event struct { Item, Key string; State string /* "firing"|"resolved" */; Value float64; Since time.Time }

type Evaluator struct{ /* ... */ }
func NewEvaluator(store *Store, dir string, emit func(Event)) (*Evaluator, error)   // dir: dossier où l'évaluateur écrit alerts.json (réglages et état), séparé des anneaux, écriture atomique
func (e *Evaluator) Rules(item string) []Rule
func (e *Evaluator) SetRules(item string, rules []Rule) error                       // validation: seuil 1..100, minutes 1..120; erreur typée *RuleError{Index int; Field, Message string}
func (e *Evaluator) Active(item string) []Event
func (e *Evaluator) OnMinute(item string, minute time.Time)                         // appelé par le Store (SetOnMinute), jamais par un service; lit les séries alert/* de la minute dans le Store, compte les minutes de suite, émet firing/resolved
func (e *Evaluator) Rebuild(item string)                                            // au démarrage: reconstruit les compteurs depuis l'historique gardé
func (e *Evaluator) Resolve(item string)                                            // l'élément n'est plus mesuré (conteneur ou VM arrêté, supprimé, oublié): un « resolved » par alerte active, une seule fois
func (e *Evaluator) Prune()                                                         // oublie les réglages et l'état des éléments qui ne sont plus dans le Store
```

Un seul événement « firing » à l'arrivée, un seul « resolved » à la fin; l'état « en cours » survit au redémarrage (fichier de réglages). Le hub du cœur applique en plus sa propre anti-répétition.

### Câblage de l'évaluateur (une seule façon, pour les trois services)

1. Au démarrage: `ev, _ := NewEvaluator(store, dir, emit)`, puis **une fois** `store.SetOnMinute(ev.OnMinute)`, puis `ev.Rebuild(item)` pour chaque élément du disque.
2. Les services n'appellent **jamais** `OnMinute` et ne suivent pas les changements de minute (pas de rattrapage). Le Store appelle le crochet une fois par élément et par minute **écrite** (quand un `Record` de la minute suivante la valide), hors verrou, et pas si l'écriture a échoué.
3. Un élément sans `Record` pendant un tick (appel Stats ou lecture en échec, élément absent de la réponse) est un trou: rien n'est écrit pour lui, `OnMinute` n'est pas appelé. Une minute sans aucun `Record` remet la course à zéro mais **ne termine pas** une alerte en cours; une minute écrite sans la série `alert/*` la termine.
4. Un élément qui cesse d'être mesuré (conteneur qui n'est plus `running`, VM qui n'est plus `running` ni `paused`, élément oublié ou supprimé): le service appelle `ev.Resolve(item)` une fois, au tick qui le constate.

## 2. Routes (même forme dans les trois services)

Derrière le jeton, sous le préfixe du service: `/v2/app_management/stats`, `/v2/virt_management/stats`, `/v1/sys/stats` (cœur, élément `host`).

```
GET  {prefix}                    -> { "items": [Item], "notes": [ "<code>" ] }
GET  {prefix}/{id}?range=live|15m|1h|24h|7d   -> Series
GET  {prefix}/{id}/alerts        -> { "rules": [Rule], "active": [Alert] }
PUT  {prefix}/{id}/alerts        <- { "rules": [Rule] }        -> idem GET
```

- **Enveloppe, par préfixe**: `/v2/app_management/stats` et `/v2/virt_management/stats` répondent des corps **nus** (pas de `data`): `{ "items", "notes" }`, l'objet Series, `{ "rules", "active" }`; une erreur est `{ "message": "<code>" }`. `/v1/sys/stats` (cœur) répond dans l'enveloppe habituelle du cœur `{ "success": n, "message": "...", "data": <corps> }` (`model.Result`): `data` est exactement le corps ci-dessus; une erreur est le statut HTTP avec `message` = le code.
- `id`: l'`item` encodé pour une URL (un seul segment; `:` et `/` encodés).
- `Item`: `{ "id", "name", "kind": "app-container|container|vm|host", "group": "Conteneurs|Autres conteneurs|Machines virtuelles|Machine", "app"?: "<nom de l'application>", "state": "running|stopped|paused|crashed|unknown", "started_at": "<RFC 3339>"|null, "detail": "<texte>"|null, "cpu_percent": n|null, "memory_used": n|null, "memory_limit": n|null, "alerts": n }`. `started_at`: démarrage du conteneur, de la VM ou de la machine (boot), `null` si inconnu. `detail`: image du conteneur, description de la VM (ou indice d'OS), `null` pour la machine; texte, au plus 200 caractères, jamais du HTML. Le `name` de la machine (`host`) est le nom d'hôte de la machine (repli `Machine`), le même partout. États d'une VM (libvirt vers contrat, table complète dans le plan virt, tâche 4): running, blocked, shutdown -> `running`; paused, pmsuspended -> `paused`; shutoff -> `stopped`; crashed -> `crashed`; nostate ou inconnu -> `unknown`. `notes` de la liste: `stats_capacity` quand une borne de nombre d'éléments est atteinte (des éléments ne sont pas mesurés).
- `Series`: `{ "id", "range", "step_seconds", "from", "to", "scale": { "cpu": "percent_of_one_core", "cpu_allocation": n|null, "memory_limit": n|null }, "series": [ { "chart": "cpu", "name": "user", "unit": "percent", "points": [[t, avg, min, max], ...] } ], "notes": [ "<code>" ] }`. Les trous sont `null`. `scale.cpu_allocation` est un nombre de **cœurs**: la limite du conteneur si elle existe, sinon le nombre de cœurs de la machine avec la note `no_cpu_limit`; pour une VM le nombre de processeurs virtuels; pour la machine son nombre de cœurs. `scale.memory_limit`: limite déclarée en octets, ou `null` avec la note `no_memory_limit` (l'alerte `mem_high` d'un conteneur sans limite se mesure alors sur la mémoire totale de la machine). `notes` porte des codes (`vm_memory_used_unavailable`, `no_cpu_limit`, `no_memory_limit`, `container_gone`, ...) que la page traduit.
- `Alert`: `{ "key", "since", "value" }`.
- `PUT .../alerts`: `rules` is a list of `{key, enabled, threshold, minutes}`; it UPDATES the keys sent (merge per key); a malformed body gives `400 bad_rules: rules`, a missing or mistyped `enabled` gives `400 bad_rules: rules[i].enabled`; `404 unknown_item` when the service has no history for the item (a 200 always means the rules are kept); `503 stats_unavailable` also means 'applied in memory but not durable' when the state file could not be written.
- Erreurs: `404 unknown_item`, `400 bad_range`, `400 bad_rules`, `503 stats_unavailable` (échantillonneur arrêté ou disque illisible); jamais de contenu venant d'un conteneur dans un message d'erreur. **Forme unique de `bad_rules`**: HTTP 400 et `message` = `bad_rules: rules[0].threshold` (le code, deux-points, espace, le champ fautif `rules[<index>].<key|threshold|minutes>`), sans autre propriété; les trois services le déduisent du `*RuleError` de Common.

## 3. Événements MessageBus (alertes de ressources)

Les services publient, le cœur (hub) les lit. Deux événements, enregistrés comme l'ont été `app:container-*`:

- `resource:alert-firing`, `resource:alert-resolved`; propriétés: `item` (id), `name`, `kind`, `situation` (`cpu_high`, `mem_high`, `vm_crashed`), `value` (chaîne: entier ou décimal, `93` ou `93.4`, que le consommateur affiche tel quel; les trois services envoient un entier; absent pour `vm_crashed`), `minutes` (chaîne: fenêtre de la règle en minutes, entier 1..120; absent pour `vm_crashed`), `since` (RFC 3339), `source` (`app_management` | `virt_management` | `core`).
- Le cœur (`pkg/alerts`): catégorie `resources` (activée d'office), clé `resource:<kind>:<id>:<situation>`, message en anglais comme toutes les phrases du hub, « <name>: CPU at 93 % for 10 min. » (une phrase, rien de sensible; la durée est omise si `minutes` manque), `resolved` seulement si le `firing` a été envoyé.
- Le module `virt` publie en plus `vm_crashed` (firing) quand libvirt signale un arrêt en échec ou un plantage, et `resolved` quand la VM redémarre ou est supprimée.
- Le tableau de bord écoute les deux événements pour la notification en fenêtre, la pastille de l'icône Statistiques et le bandeau de la page (il n'a pas de centre de notifications).

## 4. Tests communs

- Aucun test n'utilise l'horloge réelle (`Clock` injectée).
- Chaque plan: tests d'abord, tests de mutation des gardes, `gofmt`/`go vet`, jeu de tests complet une fois; messages de commit sans `Co-Authored-By` ni « Generated with ».
- Budget: chaque service garde son échantillonnage sous 0,5 % d'un cœur avec 30 conteneurs / 10 VM (budgets séparés, pas additionnés), mesuré dans son plan.
- Preuve sur machine virtuelle (QEMU) pour le module `virt` (l'historique survit à un redémarrage; les VM de test `test:///default`).
