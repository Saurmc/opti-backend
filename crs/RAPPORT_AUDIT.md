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

> À remplir : banc d'essai matériel (CPU, cœurs/threads, caches L1/L2/L3, RAM, OS, version Go
> exacte) + protocole de mesure (Hyperfine : warmup, itérations, isolation du bruit, moyenne /
> médiane / écart-type / variance). Prérequis : **mode headless déterministe** (`-bench -ticks=N`,
> sans ticker ni réseau) et `BenchmarkUpdate` pour `benchstat`.

_(section en attente de la mise en place du mode headless)_

---

## 2. Diagnostic matériel & profiling réel — /5

> À remplir : captures `pprof` / flamegraphs annotées (CPU + allocations) générées sur la machine,
> identification formelle du **Hot Path** initial (analyse textuelle précise : instructions
> bloquantes, contention mémoire, pression GC).

_(en attente)_

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

**Optimisation prévue (le "après").** Remplacer « chacun compare à tous » par une **grille spatiale
(spatial hashing)** : la carte est découpée en cases de la taille du rayon d'interaction ; un agent
ne compare qu'aux agents des **cases adjacentes** (3×3). Complexité attendue : `O(n²)` → ~`O(n)`.
Mesure **avant → après** à fournir (benchstat sur `BenchmarkUpdate`, flamegraph, TPS/updates-s).

_(résultats chiffrés à compléter après implémentation de la grille)_

---

_(autres sous-sections détaillées ajoutées au fur et à mesure)_

---

## 4. Confrontation critique & « échec constructif » — /3

> À remplir : au moins une tentative d'optimisation **contre-productive**, avec analyse mécanique
> et **chiffrée** de la régression avant retour arrière (ex. parallélisation prématurée détruite
> par le context-switching, cache augmentant la pression GC, verrouillage trop fin).

_(en attente)_

---

## 5. Reproductibilité & synthèse comparative — /4

> À remplir : script/`Makefile` lançant toutes les mesures **en une commande**, + **tableau de
> synthèse** chiffrant les gains d'ordres de grandeur (baseline vs version finale via
> `benchstat` / `hyperfine`).

_(en attente)_

---

## 6. Bonus — gouvernance IA (`constitution.md`) — +2

> À remplir : fichier `constitution.md` à la racine (rôle système strict d'ingénieur contraint par
> le matériel, contraintes négatives — bannir `fmt.Sprintf` sur le hot path, interdire les
> goroutines non bornées, proscrire les conversions `string`/`[]byte` superflues —, principe de
> justification empirique « hypothèse d'impact matériel / commande de profiling », format impératif).

_(en attente)_
