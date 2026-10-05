# CRS Kettling • Simulateur de Manifestation & Encerclement

Moteur de simulation spatiale temps réel : une foule de manifestants tente d'atteindre un
objectif à travers un réseau de rues, pendant que les CRS cherchent à l'encercler (nasse) et
à interpeller un maximum d'individus, à l'aide de gadgets et d'armes.

Même base technique que `../ZombieHorde` : serveur de calcul **Go**, télémétrie web **Canvas 2D**
via **WebSocket** (bidirectionnel), boucle déterministe par seed. Implémentation **volontairement
non optimisée** (boucles `O(n·m)` / `O(n²)`, allocations par tick, JSON par frame, Dijkstra
périodique) : c'est la baseline destinée à l'audit de performance backend.

---

## 1. Règles métier

### Carte — réseau de rues
- Espace de 5 000 × 5 000 unités, murs infranchissables.
- **Trame Manhattan** : immeubles réguliers (`BlockSize`) séparés par des rues (`StreetWidth`).
- **Avenue centrale** ouverte menant à l'objectif.
- **Zone objectif** : bande haute (`Y ≤ ObjectiveY`). Un manifestant qui l'atteint a **« passé »**.
- Départ : foule dense en bas, répartie dans les rues.

### Navigation — flow field
- Les manifestants suivent un **flow field** calculé par **Dijkstra multi-source** depuis
  l'objectif, sur une grille de navigation (`NavN²`).
- Les cellules **gazées** sont pénalisées ; le champ est **recalculé périodiquement**
  (`NavRecalc`) → la foule **route par les rues**, contourne une rue bloquée/gazée et peut
  rebrousser chemin.
- Les CRS contournent les immeubles via `stepAround` (wall-following).

### Manifestants
- **Meneurs** (~`LeaderRatio`) : forte détermination, poussent vers l'objectif, résistent mieux
  au cordon. **Civils** : détermination modérée, suivent le meneur le plus proche quand ils sont calmes.
- **Panique** temporaire (gaz, grenade, LBD, canon) : effondre la poussée vers l'objectif, la
  fuite domine, puis décroît (la foule se regroupe).
- **Endurance** : fuir épuise la jauge ; à zéro l'individu tombe, immobilisé, et devient très
  vulnérable à l'interpellation.
- **Équipement** :
  - **Lunettes** (`GogglesRatio`) : immunité lacrymo (ni panique ni ralentissement).
  - **Contre-lacrymo** : un porteur de lunettes dans un nuage peut le **renvoyer** vers le CRS
    le plus proche (`CounterChance` ~20 %, cooldown). Le nuage renvoyé est **bleu** et **fait
    reculer les CRS**.
  - **Parapluie** (`UmbrellaRatio`) : amortit le **canon à eau** et la **létalité du LBD**.
  - **Fil tendu** : posé par les meneurs quand des CRS approchent ; fait **tomber** puis
    **ralentit** les CRS (usages limités).
  - **Pavé** : un lanceur (`PaveRatio`) vise le CRS le plus proche (`PaveHit`) → **recul** +
    **étourdissement** du CRS, et une chance de le faire tomber.

### CRS
- Interceptent en priorité le manifestant le plus **avancé** proche (côté objectif) → le front
  enveloppe les percées (encerclement émergent) ; sinon convergent vers la foule.
- **Gadgets & armes** :
  - **Lacrymogène** : nuage persistant qui grossit puis s'estompe, **repousse / affole /
    ralentit** (×`WaterWetSlow`… pour l'eau ; `GasSlowFactor` pour le gaz).
  - **Grenade de désencerclement** : impulsion instantanée quand un CRS est submergé.
  - **Canon à eau** : camions fixes projetant un **jet en cône**, fort recul + ralentissement.
  - **LBD** (fraction `LBDRatio` des CRS) : tir à cooldown qui **sépare** la foule autour de
    l'impact et **tue** la cible avec une probabilité `LBDKill` (victimes comptées à part).

### Interpellation & nasse
- **Interpellation** : au contact d'un CRS (`< ArrestRadius`) **ET** (encerclé par ≥ `EncircleCops`
  CRS **OU** gazé **OU** épuisé), avec une probabilité par tick modulée par l'agressivité.
- **Nasse de rue** : un segment de rue tenu **aux deux bouts** par des CRS est scellé → les
  manifestants pris dans le corridor sont arrêtés progressivement.
- Tout manifestant arrêté (ou tué) **disparaît**.

### Déterminisme
- Seed fixe (`MasterSeed` par défaut) → trajectoires et statistiques reproductibles.

---

## 2. Pilotage

Panneau de contrôle dans l'UI (WebSocket bidirectionnel) :
- **Play / Pause / Relancer / Stop**.
- Paramètres appliqués au **Relancer** : nb manifestants, nb CRS, **agressivité CRS**, seed,
  **% de CRS au LBD**, **létalité LBD**, **% de renvoi lacrymo**, et toutes les **cadences en
  tirs/seconde** : LBD/s, lacrymos/s, fils/s, pavés/s, plus **% de lanceurs de pavés** et
  **précision des pavés**. Les cadences sont converties en cooldown interne (`rateToCd`),
  découplées du framerate ; un taux à 0 désactive l'arme.
- **Zoom** molette centré sur le curseur, **glisser** pour déplacer, **double-clic** pour réinitialiser.

---

## 3. Télémétrie

Taux de passage, passés / arrêtés, **victimes LBD**, meneurs / civils en lutte, foule en panique /
à terre, effectif CRS, lacrymos actifs, **lunettes / parapluies**, **fils tendus / CRS à terre**,
TPS serveur et FPS rendu.

---

## 4. Structure

```text
.
├── index.html   # Interface web (Canvas 2D, zoom/pan, dashboard, contrôles)
├── go.mod / go.sum
├── main.go      # Moteur, flow field, boucle de simulation, serveur WebSocket
└── README.md
```

## 5. Lancement

```bash
go run main.go
# puis http://localhost:8080
```

## 6. Paramètres (constantes en haut de `main.go`)

Carte (`BlockSize`, `StreetWidth`, `AvenueCenterX/HalfW`), navigation (`NavN`, `NavRecalc`,
`NavGasCost`), effectifs & agressivité (`SimConfig`), interpellation / nasse
(`ArrestChance`, `EncircleCops/Radius`, `KettleSealRadius/ArrestChance`), gadgets
(`Gas*`, `Blast*`), armes (`WaterCannon*`, `LBD*`), équipement (`GogglesRatio`, `UmbrellaRatio`,
`CounterGasChance`, `Tripwire*`), endurance (`Stamina*`).
