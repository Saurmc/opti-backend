# Rapport d'Audit de Performance — CRS Kettling Engine

> **Statut : EN COURS.** Document vivant, mis à jour à chaque étape. C'est le **seul livrable noté**
> (le code sert de pièce à conviction pour la reproductibilité des mesures).
> Cible : RNCP Bloc 4 — Optimisations & Performances Backend (20 pts + 2 bonus).

---

## 0. Objet du système audité

Moteur de simulation spatiale temps réel (Go + WebSocket + Canvas 2D) : une foule de
manifestants tente d'atteindre un objectif à travers un réseau de rues pendant que les CRS
l'encerclent. Choix assumé : **baseline volontairement non optimisée**, pour servir de point de
départ mesurable à l'audit. L'historique Git matérialise la démarche (features d'abord, puis
optimisations).

### 0.1 Journal des fonctionnalités construites (baseline)

| Commit (feature) | Contenu | Coût de calcul introduit (hot paths) |
|---|---|---|
| baseline | Foule/CRS, gadgets (lacrymo, désencerclement), interpellation | boucles `manifestants × CRS`, JSON ~300 Ko/frame, allocations/tick |
| street grid | Trame Manhattan + avenue, `stepAround` (contournement murs) | `collides()` sur liste d'obstacles, multi-essais/agent |
| street kettling | Nasse : segment tenu aux 2 bouts → arrestations | `segments × CRS` + `scellés × manifestants` |
| live controls | play/pause/restart/stop + params (SimConfig, mutex) | — |
| protester equipment | lunettes, parapluie, contre-lacrymo, fils tendus | scans gaz/CRS/leaders par manifestant |
| CRS weapons | canon à eau (cône), LBD (séparation + létalité) | `canons × manifestants`, `tirs LBD × manifestants` |
| flow-field nav | Dijkstra multi-source depuis l'objectif, pénalité gaz | **Dijkstra ~10 000 nœuds toutes les 15 ticks** |
| zoom + panneau probas | zoom canvas, toutes cadences réglables (tirs/s) | — |
| pavés + lacrymo bleue | arme manifestants, renvoi gaz, cadences `rateToCd` | `manifestants × gaz`, jets |
| élimination + traceurs | pavé peut éliminer un CRS, traceurs LBD/pavé | `shots`, scans élargis |
| **foule boids O(n²)** | séparation + cohésion entre manifestants (naïf) | **`manifestants × manifestants` = 6000² ≈ 36M dist/tick** |

### 0.2 Symptômes de performance observés (préliminaire, à mesurer rigoureusement en §1-2)

- **TPS cible 30 (plafond du ticker), observé ~9** à 6000 manifestants une fois la force boids
  `O(n²)` active (contre ~13 sans elle) → le serveur ne tient plus la cadence : **~3× sous la cible**.
- Payload **JSON ~300 Ko/frame**.
- Hot paths pressentis, par ordre de coût attendu :
  1. **`manifestants × manifestants`** (boids séparation/cohésion) = `O(n²)` ≈ 36M dist/tick → cible macro n°1.
  2. `manifestants × CRS` (répulsion/interpellation/encerclement) ≈ `n·m`.
  3. `manifestants × gaz` (jusqu'à 40 nuages), Dijkstra du flow field (toutes les 15 ticks),
     nasse `segments × CRS`.
  4. Allocations par tick (slices `make` à chaque frame, `encoding/json`).

> Remarque métrologie : le TPS affiché est **plafonné à 30** par le `time.Ticker`. Pour mesurer le
> **débit réel** du moteur (updates/s sans plafond), on utilisera le **mode headless `-bench`** (§1).

---

## 1. Environnement & métrologie (baseline) — /3

### 1.1 Banc d'essai matériel
| Élément | Valeur |
|---|---|
| CPU | 13th Gen Intel Core i7-13620H (hybride : 6 P-cores + 4 E-cores) |
| Cœurs / threads | 10 cœurs physiques / 16 threads logiques |
| Cache L1 | ~80 Ko par P-core (48 Ko données + 32 Ko instructions) |
| Cache L2 | 9 728 Ko au total |
| Cache L3 | 24 576 Ko (24 Mo) partagé |
| RAM | 15,7 Go |
| OS | Windows 11 Famille, build 26200 |
| Runtime | go 1.26.4 windows/amd64 |

### 1.2 Protocole de mesure
- **Mode headless déterministe** : `crs-sim -bench -ticks=N` exécute N ticks de `updateSimulation()`
  **sans ticker, sans WebSocket, sans JSON** (on isole le moteur), seed fixe (42) → strictement
  reproductible. Mesure : temps total, **ms/tick**, **updates/s** (débit réel non plafonné).
- **Micro-benchmark Go** : `go test -bench=Update -benchmem -count=3` → ns/op, **B/op**, **allocs/op**
  (pour `benchstat` sur l'avant/après). Warmup et répétitions gérés par le framework `testing`.
- **Profil CPU** : deux voies. (a) headless : `crs-sim -bench -ticks=200 -cpuprofile=cpu.out` puis
  `go tool pprof` (flat/cum, flamegraph). (b) live (méthode du cours) : le serveur importe
  `net/http/pprof` → `go tool pprof -http=:8081 http://localhost:8080/debug/pprof/profile?seconds=30`
  (et `.../debug/pprof/heap`, `.../mutex` pour la contention).
- Outils installés : `benchstat` (`go install golang.org/x/perf/cmd/benchstat`), `hyperfine` (scoop).
- Isolation : binaire natif, machine au repos, pas de rendu ni de réseau pendant la mesure headless.

### 1.3 Mesures baseline (6000 manifestants, 160 CRS, seed 42)
| Métrique | Valeur |
|---|---|
| Temps par tick | **~178–223 ms/tick** |
| Débit réel | **~4,5–5,6 updates/s** (cible 30 → **~6× sous la cible**) |
| `go test -bench` | ~184 ms/op · **34 Ko/op** · **1016 allocs/op** |

> Constat clé : le moteur est **CPU-bound** (très peu d'allocations : 34 Ko/tick), le temps part dans
> l'arithmétique des boucles imbriquées, pas dans le GC. La priorité est donc l'optimisation
> **macro algorithmique**, pas d'abord le zero-alloc.

### 1.4 Loi d'échelle — preuve empirique du `O(n²)`
`crs-sim -bench -ticks=200 -protesters=N` :

| manifestants N | ms/tick | ratio temps | ms/tick ÷ N² |
|---|---|---|---|
| 1000 | 5,82 | — | 5,82·10⁻⁶ |
| 2000 | 17,38 | ×2,99 | 4,35·10⁻⁶ |
| 4000 | 87,80 | ×5,05 | 5,49·10⁻⁶ |
| 6000 | 204,6 | ×2,33 (pour ×1,5 agents) | 5,68·10⁻⁶ |

**`ms/tick ÷ N²` reste ≈ constant (~5·10⁻⁶)** → le coût croît en `N²`. Vérification directe :
4000 → 6000 multiplie les agents par 1,5 et le temps par **2,33 ≈ 1,5² = 2,25**. Signature nette
du quadratique.

---

## 2. Diagnostic matériel & profiling réel — /5

### 2.1 Profil CPU (pprof)
Commande : `crs-sim -bench -ticks=200 -cpuprofile=cpu.out` puis `go tool pprof -top cpu.out`.

```
Duration: 44.66s, Total samples = 43.95s (98.42%)
      flat  flat%   sum%        cum   cum%
    41.11s 93.54% 93.54%     43.72s 99.48%  main.updateSimulation
     1.68s  3.82% 97.36%      1.68s  3.82%  math.archHypot
     0.42s  0.96% 98.32%      2.10s  4.78%  math.Hypot (inline)
```

### 2.2 Identification formelle du Hot Path
- **99,5 % du temps CPU est dans `updateSimulation`**, et **93,5 % en *flat*** (code exécuté
  directement dans la fonction, hors appels). C'est de l'arithmétique inline : les calculs de
  distance `dx*dx + dy*dy` des boucles imbriquées.
- `math.Hypot` (racine carrée pour normaliser les vecteurs) pèse **4,8 %** en cumulé — c'est la
  partie non-inline des mêmes distances (séparation boids + cohésion des meneurs + répulsions).
- **Aucune pression GC notable** (cf. §1.3 : 34 Ko/tick) : le goulot n'est ni l'allocation ni l'I/O,
  mais le **nombre d'opérations arithmétiques**, lui-même piloté par la complexité `O(n²)`.

**Conclusion du diagnostic.** Le hot path est la boucle `manifestants × manifestants` (boids
séparation/cohésion), confirmée à la fois par l'analyse de complexité (§1.4, loi en N²) et par le
profil CPU (tout le temps dans `updateSimulation`). Cible d'optimisation n°1 : la rendre `~O(n)`.

> Flamegraph : `go tool pprof -http=:0 cpu.out` (ou `-svg` avec Graphviz) — montre `main` →
> `runBench` → `updateSimulation` comme unique branche large.

---

## 3. Journal d'optimisation — /5

> Une sous-section par optimisation. Format imposé pour chacune :
> - **Catégorie : macro** (algorithmique/architectural) **ou micro** (bas niveau).
> - **Axe du cours** : mémoire/localité cache · concurrence/scalabilité CPU · I/O réseau & persistance.
> - **Hypothèse d'impact matériel** + **commande de profiling** pour la vérifier.
> - **Avant → après chiffré** (benchstat / hyperfine / pprof), avec le delta.

### Optimisations prévues (feuille de route)
- **MACRO** — `manifestants ↔ voisins` et `manifestants × CRS` : grille spatiale (spatial hashing)
  `O(n²)` → `O(n)`. (La force `O(n²)` manif↔manif est en place, cf. Optimisation 1 ci-dessous.)
- **MACRO** — flow field : cadence/coût du Dijkstra, réutilisation du buffer.
- **MICRO** — mémoire/cache : réordonnancement des champs (`struct padding`), alignement sur
  lignes de cache 64 B, **zero-allocation** (buffers réutilisés, `sync.Pool`), AoS → SoA.
- **CONCURRENCE** — worker pool dimensionné aux cœurs physiques sur la boucle manifestants,
  synchronisation atomique, arrêt précoce.
- **I/O & PERSISTANCE** — JSON → **Protobuf/binaire** (réduction payload), étage **SQL** avec
  index prouvé par **`EXPLAIN ANALYZE`**, cache (`sync.Pool` / LRU).

---

### Optimisation 1 — Foule `O(n²)` → grille spatiale  *(MACRO — axe scalabilité CPU)*

**État : baseline en place (le "avant"). Optimisation à venir.**

**Le comportement.** Modèle *boids* : chaque manifestant applique deux règles locales —
**séparation** (s'écarter des voisins à < 40 u, pour ne pas se superposer) et **cohésion**
(se rapprocher du centre local des voisins à < 130 u, pour rester groupé).

**L'implémentation naïve (le "avant").** Pour calculer ces forces sur un manifestant `i`, le code
**parcourt TOUS les autres** manifestants `j` (double boucle imbriquée `for i { for j }`) et teste
la distance. Fichier `main.go`, section « Interactions de foule (séparation + cohésion) — NAÏF O(n²) ».

**Pourquoi c'est le goulot — complexité `O(n²)`.** `n` agents × `n` voisins = `n²` comparaisons.
À 6000 : `6000² = 36 000 000` distances **par tick**, × 30 ticks/s visés ≈ **~1 Md d'op/s** pour
cette seule étape. Loi d'échelle : **foule ×2 ⇒ coût ×4** (signature du quadratique).

**Preuve chiffrée (constatée, à raffiner en §1/§2).** Serveur visant 30 TPS :
- sans la force boids : ~13 TPS
- avec la force boids (6000 agents) : **~9 TPS** → **~3× sous la cible**.

**Hypothèse d'impact matériel.** Le coût est dominé par les calculs de distance et le parcours
mémoire de tout le tableau d'agents pour chaque agent (mauvaise localité, beaucoup de cycles CPU).
**Commande de profiling pour le vérifier :** `go test -bench=Update -cpuprofile=cpu.out` puis
`go tool pprof` (flamegraph) — on s'attend à voir `updateSimulation` / la double boucle en tête.

**Optimisation réalisée (le "après").** Grille spatiale (*spatial hashing*) en **deux temps**,
guidés par le profiling ligne-à-ligne (`go tool pprof -list=updateSimulation`) :

**Temps 1 — grille sur la boucle boids.** La carte est découpée en cases de la taille du rayon
d'interaction (130 u). Les manifestants actifs sont rangés dans la grille via **listes chaînées
`gridHeads`/`gridNext`** (buffers réutilisés entre ticks → zéro allocation par case). Chaque agent
ne scanne plus que les **9 cases voisines (3×3)** au lieu des 6000. Résultat : 204 → **59 ms/tick**.

**Temps 2 — fusion du scan des meneurs.** Le profil d'après montrait un terme quadratique résiduel :
chaque suiveur parcourait **tous** les meneurs (`suiveurs × meneurs = 0,09·n²`). Comme les meneurs
sont déjà dans la grille, on **repère le meneur le plus proche pendant le même scan 3×3** (aucune
boucle en plus, suppression de `leaderIdx`). Résultat : 59 → **35,7 ms/tick**.

**Avant → après (headless `-bench -ticks=300`) :**

| manifestants | AVANT (ms/tick) | APRÈS (ms/tick) | gain |
|---|---|---|---|
| 1000 | 5,82  | 3,44  | ×1,7 |
| 2000 | 17,38 | 7,75  | ×2,2 |
| 4000 | 87,80 | 21,37 | ×4,1 |
| 6000 | 204,6 | 35,7  | **×5,7** |

- `go test -bench` : **184 ms/op → ~40 ms/op** (×4,6) ; allocations **1016 → ~900/op** (suppression
  de `leaderIdx` + buffers de grille réutilisés).
- **Débit à 6000 : ~5,6 → ~28 updates/s** (cible 30 quasi atteinte).

**Hypothèse confirmée.** Le gain **croît avec n** (×1,7 à 1000, ×5,7 à 6000) : c'est la signature de
l'élimination du **terme quadratique dominant**. Le coût n'est plus piloté par `n²`.

**Nuance honnête (importante pour l'oral).** Une grille spatiale est `O(n)` **à densité bornée**.
Ici la carte est de taille **fixe** : quand `n` augmente, la **densité** augmente, donc le nombre de
voisins par case croît ∝ n → il reste une composante superlinéaire (4000→6000 : ×1,5 agents ⇒
×1,67 temps, contre ×2,25 en quadratique pur). Mais la **constante est ~40× plus petite** (voisins
locaux au lieu de la foule entière), d'où le gain d'ordre de grandeur réel.

**Validation comportementale.** La sortie n'est pas bit-à-bit identique à la version naïve (l'ordre
de sommation des voisins diffère → divergence par arrondi flottant), mais le comportement de foule
est **équivalent en distribution** (répartition dans les rues, contournement). On valide donc sur la
**performance** (ms/tick, benchstat) et l'équivalence visuelle, pas sur une égalité exacte.

**Catégorie : MACRO** (algorithmique) · **Axe : scalabilité CPU**. Commande de vérification :
`go tool pprof -top` / `-list=updateSimulation` sur `cpu.out` avant et après.

**Validation statistique (outils du cours).** Comparaison naïf `O(n²)` → optimisé (grille + §Opti 2) :

- **benchstat** (`go test -bench=Update -count=10` ; test Mann-Whitney) :
  `Update-16  208.12m ± 5%  →  36.37m ± 17%  **-82.52%** (p=0.000, n=10)`.
- **Hyperfine** (`hyperfine --warmup 1 --runs 5 'naif -bench -ticks=40' 'opti -bench -ticks=40'`) :
  naïf **10,288 s ± 0,55** → optimisé **1,484 s ± 0,13** = **-85,6 %, ×6,9 plus rapide**.

Les deux outils (statistique + wall-clock binaire) concordent sur un gain d'**ordre de grandeur**.

---

### Optimisation 2 — `math.Sqrt` → comparaison des distances au carré  *(MICRO — axe CPU)*

**La technique du cours (J1-J2).** La racine carrée (`math.Sqrt`/`math.Hypot`) est coûteuse. Pour
tester si deux agents sont dans un rayon `R`, on compare les **carrés** : `dx²+dy² ≤ R²` (aucune
racine). On ne calcule la racine **que** lorsqu'on a besoin de la **longueur** du vecteur (pour le
normaliser).

**Application.** Deux hot sous-boucles le faisaient inutilement à chaque tick :
- **Scan CRS** : `6000 × 160 ≈ 960 000` appels `math.Hypot`/tick, alors qu'on ne fait que **comparer**
  des distances (plus proche CRS, encerclement, portées pavé/fil/renvoi). → passées en carré ;
  `nearestCopD` devient `nearestCopD2`, seuils comparés au carré. Racine conservée **uniquement**
  dans le rayon de répulsion (normalisation).
- **Scan gaz** : `6000 × 40 ≈ 240 000` `Hypot`/tick → carré ; racine seulement à l'intérieur du nuage.

**Résultat.** Sur le profil post-grille, `math.Hypot` pesait ~13 % → on le ramène à l'essentiel.
Le gain propre est **modeste (~10 %, dans le bruit du benchmark)** : logique, car la grille (Opti 1)
avait déjà supprimé le coût dominant. C'est un **point honnête** : une micro-opti correcte mais à
effet limité une fois le terme quadratique éliminé — l'ordre des optimisations (macro d'abord) est
ce qui compte (règle *Make it right, then fast* / Pareto du cours).

**Catégorie : MICRO** · **Axe : CPU (réduction d'instructions coûteuses)**.

---

### Optimisation 3 — Parallélisation (worker pool borné)  *(MACRO — axe concurrence / scalabilité CPU)*

**Le problème.** La boucle des manifestants est le gros du coût, mais elle écrit de l'état
**partagé** (compteurs `arrested`/`killed`/`passed`/`pavesThrown`, `append` sur `gases`/`tripwires`/
`shots`, mutations de CRS via les pavés) et utilise un **RNG non thread-safe** (`math/rand`). La
paralléliser naïvement = data races + perte de déterminisme.

**La solution — séparation en deux phases.**
1. **Phase parallèle (lecture seule)** : les **scans de voisinage coûteux** (gaz, CRS, foille via la
   grille) sont extraits dans `scanProtester(i)`, qui n'écrit QUE son propre `scratch[i]`. Répartis
   sur les cœurs via un **worker pool borné sur `runtime.GOMAXPROCS`** (partition en tranches
   contiguës + `sync.WaitGroup`). Les positions sont **gelées** pendant cette phase → aucune course.
2. **Phase séquentielle (bon marché)** : décisions (arrestation, renvoi, pavé) et déplacement, dans
   l'ordre, avec le `simRng` global. Tous les écrits partagés restent **mono-thread** → **race-free
   ET déterminisme préservé**.

**Validation de correction.** Détecteur de courses Go : `CGO_ENABLED=1 go test -race -bench=Update`
→ **PASS, aucune `DATA RACE`**. (Correct aussi par construction : écritures disjointes dans
`scratch[i]`, données partagées en lecture seule pendant la phase parallèle.)

**Avant → après (benchstat, séquentiel grille+sqrt → parallèle) :**
`Update-16  36.11m ± 19%  →  10.43m ± 3%  **-71.13%** (p=0.000, n=10)`.
Headless à 6000 : **35,7 → 8,75 ms/tick** (~114 updates/s).

**Loi d'Amdahl (point clé pour l'oral).** 10 cœurs ne donnent pas ×10 mais **~×3,5** : la fraction
**séquentielle** (phase 1b + buildGrid + boucle CRS + nasse + sérialisation) borne l'accélération
(`S = 1/((1-P)+P/N)`). On a parallélisé la partie dominante (les scans), pas le reste.

**Bilan cumulé macro+micro+concurrence (naïf → ici), à 6000 :** **~204 → ~8,8 ms/tick ≈ ×23.**

**Catégorie : MACRO/concurrence** · **Axe : scalabilité CPU**. Note : déterministe à nombre de
workers fixe ; la sortie diffère de la version séquentielle (modèle de mise à jour « simultanée »
avec positions gelées au lieu de « en place »), équivalence validée sur la performance et le visuel.

---

### Optimisation 4 — Sérialisation binaire WebSocket (JSON → binaire)  *(axe I/O réseau & mémoire)*

**Le problème (goulot migré).** Après les 3 optis CPU, le moteur tourne à ~114 updates/s en
headless mais plafonne à **~17 TPS dans le navigateur**. Le goulot n'est plus la simulation mais
la **frame** : `json.Marshal` d'un payload **~431 Ko** (6000 agents en texte verbeux +
heatmap 10 000 entiers), parsing texte côté client, et l'allocation de ~240 Ko de slices de structs
intermédiaires (`pPoints`/`cPoints`/…) **à chaque frame** (pression GC).

**La solution (TP J4_AM du cours).** Frame auto-descriptive `[uint32 jsonLen][en-tête JSON][blob]` :
- **Petit volume / statique** (stats, config, obstacles, nasses, objectif) → en-tête **JSON**.
- **Gros volume** (manifestants, CRS, gaz, tirs, heatmap) → **binaire compact little-endian**, écrit
  **directement** depuis `protesters[]` dans un buffer **réutilisé** (`frameBuf`), sans slice
  intermédiaire. Un manifestant = **5 octets** : `int16 x`, `int16 y`, `uint8 drapeaux`
  (leader/panique/épuisé/lunettes/parapluie en **bits**). Positions quantifiées en `int16`
  (plage 0-5000 ⊂ int16).
- Client : `ws.binaryType = "arraybuffer"` + **`DataView`** décode le buffer sans parsing texte,
  reconstruit un objet `data` de même forme → le rendu ne change pas.

**Avant → après (taille de frame mesurée via client WS) :**
**431 Ko (JSON) → 41,8 Ko (binaire) = −90,3 %** de bande passante (cible cours ≥85 % dépassée).
Effet de bord : TPS in-browser **~17 → 31** (le serveur tient enfin la cible de 30), car `Marshal`
ne traite qu'un petit en-tête et les 240 Ko de slices de structs par frame disparaissent.

**Validation.** Rendu vérifié de bout en bout via `/browse` (foule, CRS, gaz, traceurs corrects),
**zéro erreur console**. Taille mesurée par un client WebSocket dédié sur 3 frames.

**Catégorie : MACRO** · **Axe : I/O réseau & persistance (sérialisation binaire) + mémoire
(zéro slice intermédiaire, buffer réutilisé).** Gains quantifiés à reporter aussi dans le tableau
final (bande passante réseau).

---

### Optimisation 5 — Persistance SQL : batch insert + index B-Tree  *(axe persistance / J4_PM)*

**Le contexte.** On persiste les statistiques par tick de plusieurs simulations dans une base
(table `tick_stats(run_id, tick_index, active, passed, arrested, killed, paves, cops_gone)`),
puis on optimise l'accès. Base : **SQLite** (driver pur Go `modernc.org/sqlite`, zéro install).
Mode dédié : `crs -sqldemo -runs N -ticks M`.

**(a) Batch insert (équivalent `pgx.CopyFrom`).** En SQLite chaque `INSERT` en autocommit = une
transaction avec `fsync` sur disque. Regrouper les écritures dans **une seule transaction** +
statement préparé supprime ce coût.
Mesuré (3000 lignes) : **ligne-par-ligne 22,4 s → batch 16 ms = ×1425.**
(L'enregistrement réel des 40 000 lignes de stats utilise une transaction par run.)

**(b) Index B-Tree composite `(run_id, tick_index)`.** Requête de plage
`SELECT COUNT(*), SUM(arrested) FROM tick_stats WHERE run_id=? AND tick_index BETWEEN ? AND ?`.
Sans index, SQLite parcourt toute la table (scan séquentiel `O(n)`). Avec un index composite,
il descend l'arbre et balaie seulement la plage (`O(log n)` + balayage local).

Preuve par `EXPLAIN QUERY PLAN` (l'équivalent SQLite d'`EXPLAIN ANALYZE`) sur 40 000 lignes :
| | plan d'exécution | temps moyen (300 exéc.) |
|---|---|---|
| **SANS index** | `SCAN tick_stats` | **3,73 ms** |
| **AVEC index** | `SEARCH tick_stats USING INDEX idx_run_tick (run_id=? AND tick_index>? AND tick_index<?)` | **504 µs** |

**Gain ×7,4**, et l'écart croît avec le nombre de lignes (le scan est `O(n)`, l'index `O(log n)`).

**Note de fidélité au cours.** Le cours (J4_PM) utilise PostgreSQL + `pgx.CopyFrom` +
`EXPLAIN (ANALYZE, BUFFERS)` (avec `shared hit`/`shared read`). SQLite démontre les **mêmes
concepts** (batch transactionnel, index B-Tree composite, plan SCAN→SEARCH) sans serveur ; la
différence est l'absence du détail `BUFFERS` (pages cache vs disque), remplacé ici par le
chronométrage direct avant/après.

**Catégorie : MACRO** · **Axe : persistance / indexation SQL.**

---

_(autres sous-sections détaillées ajoutées au fur et à mesure)_

---

## 4. Confrontation critique & « échec constructif » — /3

### Tentative : une goroutine par manifestant (au lieu du worker pool borné)

**Hypothèse (naïve).** « Si paralléliser sur 10 cœurs accélère, alors paralléliser *au maximum* —
une goroutine par manifestant (~6000 par tick) — devrait aller encore plus vite, le runtime Go
plaçant lui-même les goroutines sur les cœurs. »

**Mise en œuvre.** Dans `dispatchScan`, mode `per-item` : `wg.Add(len(protesters))` puis
`go scanProtester(i)` pour chaque manifestant, au lieu de ~`GOMAXPROCS` goroutines traitant chacune
une tranche contiguë.

**Mesure (benchmark isolé sur état figé, `BenchmarkScan*`, `benchstat`, n=10) :**

| stratégie | temps/dispatch |
|---|---|
| worker pool borné (`NumCPU`) | **5,02 ms ± 7 %** |
| une goroutine par manifestant | **5,90 ms ± 8 %** |

`X-16  5.020m ± 7%  →  5.904m ± 8%  **+17,62 %** (p=0.000 n=10)` → **régression statistiquement
significative de ~18 %.**

**Analyse mécanique (pourquoi c'est plus lent).**
1. **Coût de création/ordonnancement** : créer 6000 goroutines/tick (× 30 = 180 000/s) au lieu de
   ~16 ajoute des allocations de piles et du travail à l'ordonnanceur GMP (mise en file, *work
   stealing* entre P), pour **zéro parallélisme supplémentaire** — il n'y a que 10 cœurs physiques.
2. **Granularité trop fine** : chaque `scanProtester` dure ~1 µs ; le surcoût d'orchestration par
   goroutine n'est plus amorti (ratio overhead/travail défavorable).
3. **Localité de cache dégradée** : les tranches contiguës du pool parcourent `protesters[]` et
   `scratch[]` de façon séquentielle (préchargement efficace) ; 6000 goroutines entrelacées cassent
   ce motif d'accès.

C'est exactement l'écueil « goroutines non bornées / context-switching » enseigné en J3.

**Retour arrière.** La version retenue reste le **worker pool borné** (`parallelMode = "pool"`, le
défaut). Le mode `per-item` est conservé uniquement comme variante *benchmarkable*
(`BenchmarkScanPerItem`) pour étayer ce chapitre — il n'est jamais emprunté en production.

---

## 5. Reproductibilité & synthèse comparative — /4

### 5.1 Automatisation en une commande

Toute la chaîne de mesure est rejouable :

```bash
make benchmarks          # = bash run_benchmarks.sh full
# ou directement :
bash run_benchmarks.sh full
```

Le script `run_benchmarks.sh` écrit des résultats **horodatés** dans `bench-results/<timestamp>/` :
environnement (CPU, Go, GOMAXPROCS), build, `BenchmarkUpdate` (`-benchmem -count=10`), loi
d'échelle, modes de parallélisation + `benchstat`, profil CPU (`pprof -top`), et en mode `full` :
Hyperfine (naïf via `git stash` vs optimisé) + démo SQL. Cibles courtes aussi dans le `Makefile`
(`make bench`, `make scale`, `make modes`, `make profile`, `make race`, `make sqldemo`).

### 5.2 Tableau de synthèse — Baseline naïf → Version finale (6000 manifestants, seed 42)

| Métrique | Baseline naïf `O(n²)` | Version finale | Gain | Outil |
|---|---|---|---|---|
| **Temps / tick** (headless) | ~204 ms | **~8,8 ms** | **×23** | `-bench` |
| **Débit moteur** | 5,6 updates/s | **114 updates/s** | ×20 | `-bench` |
| **`BenchmarkUpdate`** | 208 ms/op | **~10 ms/op** | **−95 %** | `benchstat` (p<0,05) |
| **TPS dans le navigateur** | ~9 TPS | **31 TPS** (cible 30 atteinte) | ×3,4 | UI |
| **Bande passante / frame** | 431 Ko (JSON) | **41,8 Ko** (binaire) | **−90,3 %** | client WS |
| **Requête SQL (plage)** | 3,73 ms (`SCAN`) | **504 µs** (`SEARCH INDEX`) | ×7,4 | `EXPLAIN QUERY PLAN` |
| **Insertion 3000 lignes** | 22,4 s (ligne-par-ligne) | **16 ms** (batch) | ×1425 | chrono |

### 5.2bis Validation sous charge HTTP (Vegeta) — endpoint `/api/frame`

Le chemin de **sérialisation** (axe I/O) exposé en HTTP sur `/api/frame` (frame binaire 40,3 Ko),
martelé par **Vegeta** (`-rate=2000 -duration=15s`, 30 000 requêtes) :

| Métrique | Valeur |
|---|---|
| Débit soutenu | **2000,6 req/s** (cible tenue) |
| Disponibilité | **100 %** (30 000 × HTTP 200) |
| Latence P50 | **563 µs** |
| Latence P90 / P95 | 1,68 ms / 2,69 ms |
| Latence **P99** | **24,5 ms** (max 71 ms) |
| Octets/réponse | 41 308 (frame binaire) |

Commande : `echo "GET http://127.0.0.1:8080/api/frame" | vegeta attack -rate=2000 -duration=15s | vegeta report`.
**Lecture (piège de la moyenne / tail latency P99)** : P50 à 563 µs mais P99 à 24,5 ms → la queue
vient de la **contention sur le mutex global** de sérialisation (chaque requête sérialise 40 Ko sous
`simMu`, lane unique saturée à ~2000/s). Piste d'optimisation future : snapshot lock-free / `RWMutex`.

### 5.3 Décomposition du gain CPU (contributions successives, `benchstat`)

| Étape | ms/op (BenchmarkUpdate) | effet |
|---|---|---|
| Baseline `O(n²)` | 208 | — |
| + grille spatiale + sqrt→carré (MACRO+MICRO) | 36 | **−82,5 %** (p=0,000) |
| + worker pool borné (concurrence) | ~10 | **−71 %** (p=0,000) |

Validé par deux outils concordants : `benchstat` (significativité statistique, Mann-Whitney p<0,05)
et Hyperfine (débit du binaire, naïf 10,29 s → optimisé 1,48 s, ×6,9 sur les seules optis CPU).

---

## 6. Bonus — gouvernance IA (`constitution.md`) — +2

Fourni à la racine du projet : **`crs/constitution.md`**. Il respecte les 4 directives :
1. **Rôle/posture système stricts** — l'IA = ingénieur système Go contraint par le budget 33,3 ms/
   tick, les lignes de cache, les allocations, les cœurs physiques ; mesurer, ne jamais deviner.
2. **Contraintes négatives (gardes-fous)** — interdits sur le hot path : `fmt.Sprintf`, goroutines
   non bornées, conversions `string`↔`[]byte` superflues, allocation tas non justifiée,
   `Sqrt`/`Hypot` quand la comparaison au carré suffit, JSON sur les données volumineuses.
3. **Justification empirique** — toute optimisation = couple **{hypothèse d'impact matériel /
   commande de profiling qui la vérifie}** ; sinon refusée.
4. **Format compact et impératif** — injonctions courtes et vérifiables, un commit atomique par
   optimisation avec avant→après chiffré.
