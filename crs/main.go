package main

import (
	"container/heap"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	_ "net/http/pprof" // expose /debug/pprof/* sur le serveur (profiling live, méthode du cours)
	"os"
	"runtime"
	"runtime/pprof"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

const (
	MasterSeed      int64   = 42
	WorldSize       float64 = 5000.0
	GridCols        int     = 100
	GridRows        int     = 100
	TotalProtesters int     = 6000
	LeaderRatio     float64 = 0.10
	TotalCops       int     = 160
	TargetFPS       int     = 30

	// Trame urbaine : grille d'immeubles réguliers séparés par des rues,
	// avec une avenue centrale ouverte menant à l'objectif.
	BlockSize     float64 = 520.0
	StreetWidth   float64 = 160.0
	AvenueCenterX float64 = 2500.0
	AvenueHalfW   float64 = 300.0

	// Nasse de rue : un segment tenu aux deux bouts par des CRS piège la foule.
	KettleSealRadius   float64 = 170.0
	KettleArrestChance float64 = 0.03

	// Équipement manifestants.
	GogglesRatio         float64 = 0.25 // lunettes : immunité lacrymo
	UmbrellaRatio        float64 = 0.20 // parapluie : anti canon à eau + LBD (Phase 4)
	CounterGasChance     float64 = 0.20 // chance de renvoyer une lacrymo vers les CRS
	CounterGasCd         int16   = 120
	TripwireDeployChance float64 = 0.004 // par meneur/tick quand des CRS sont proches
	TripwireRange        float64 = 420.0 // distance CRS déclenchant la pose
	TripwireRadius       float64 = 65.0
	TripwireMaxActive    int     = 80
	TripwireUses         int16   = 3
	CopFallTicks         int16   = 70 // CRS à terre (immobile)
	CopSlowTicks         int16   = 80 // puis ralenti en se relevant

	// Interactions de foule (type boids) — implémentation naïve O(n²) : chaque
	// manifestant est comparé à TOUS les autres. Hot path vedette à optimiser
	// ensuite (grille spatiale -> O(n)).
	SeparationRadius float64 = 40.0  // répulsion des voisins très proches
	CohesionRadius   float64 = 130.0 // attraction vers le centre local
	SeparationWeight float64 = 0.7
	CohesionWeight   float64 = 0.25

	// Navigation (flow field vers l'objectif, contournement des rues gazées).
	NavN       int = 100 // résolution de la grille de navigation
	NavRecalc  int = 15 // recalcul du champ tous les N ticks
	NavGasCost int = 12 // pénalité d'une cellule gazée

	// Armes CRS.
	WaterCannonPer      int     = 40 // 1 canon pour N CRS
	WaterCannonMin      int     = 2
	WaterCannonRange    float64 = 750.0
	WaterCannonSpread   float64 = 0.5 // demi-angle du cône (rad)
	WaterCannonPush     float64 = 42.0
	WaterWetTicks       int16   = 60 // ralenti après aspersion
	WaterWetSlow        float64 = 0.5
	UmbrellaWaterFactor float64 = 0.35 // le parapluie amortit le jet

	LBDCopRatio        float64 = 0.15
	LBDCooldown        int16   = 150
	LBDRange           float64 = 900.0
	LBDRadius          float64 = 170.0 // rayon de séparation à l'impact
	LBDPush            float64 = 48.0
	LBDKillChance      float64 = 0.05
	UmbrellaKillFactor float64 = 0.2 // le parapluie réduit la létalité

	// Pavés (arme des manifestants).
	PaveRange   float64 = 330.0
	PavePush    float64 = 36.0 // recul infligé au CRS touché
	PaveStagger int16   = 45   // CRS étourdi (ralenti)
	PaveKnock   float64 = 0.18 // chance de faire tomber le CRS
	PaveKill    float64 = 0.05 // chance qu'un pavé mette un CRS hors de combat
	ShotLife    int     = 6    // durée d'affichage d'un tir (ticks)

	// Géographie de la manif : objectif en haut, départ de la foule en bas, cordon CRS au milieu.
	ObjectiveY float64 = 450.0  // un manifestant qui atteint Y <= ObjectiveY a "passé"
	SpawnMinY  float64 = 3300.0 // bande de départ de la foule
	SpawnMaxY  float64 = 4800.0
	CordonY    float64 = 1750.0 // ligne initiale du cordon CRS

	// CRS
	CopVisionRadius float64 = 1400.0 // portée de perception de la foule
	ArrestRadius    float64 = 22.0   // contact nécessaire pour une interpellation
	EncircleRadius  float64 = 140.0  // rayon dans lequel on compte les CRS autour d'un manifestant
	EncircleCops    int     = 3      // nombre de CRS autour => nasse => interpellable
	ArrestChance    float64 = 0.04   // proba d'interpellation par tick si conditions réunies
	SubmergeRadius  float64 = 80.0   // manifestants collés au CRS => risque de submersion
	BlastThreshold  int     = 12     // au-delà, le CRS lance une grenade de désencerclement

	// Répulsion ressentie par les manifestants
	CopFearRadius float64 = 260.0

	// Lacrymo (gaz persistant)
	GasThrowSense     float64 = 320.0 // rayon dans lequel le CRS "sent" la densité devant lui
	GasThrowThreshold int     = 14    // densité déclenchant un tir de lacrymo
	GasCooldownTicks  int16   = 180
	MaxGases          int     = 40
	GasMaxRadius      float64 = 420.0
	GasGrowTicks      int     = 40
	GasLifeTicks      int     = 300
	GasSlowFactor     float64 = 0.45 // ralentissement dans le nuage

	// Grenade de désencerclement (impulsion)
	BlastRadius   float64 = 220.0
	BlastPush     float64 = 60.0
	BlastCooldown int16   = 120
	PanicDuration int16   = 90

	// Endurance
	MaxStamina     int16 = 100
	StaminaDrain   int16 = 2
	StaminaRecover int16 = 1
	StaminaResume  int16 = 60
)

type Obstacle struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

type Protester struct {
	Arrested      bool
	Passed        bool
	ID            int64
	IsLeader      bool
	IsPanicked    bool
	IsExhausted   bool
	InGas         bool
	Goggles       bool // immunité lacrymo
	Umbrella      bool // anti canon à eau + LBD
	IsThrower     bool // lanceur de pavés
	Stamina       int16
	PanicTimer    int16
	CounterCd     int16 // cooldown contre-lacrymo
	PaveCd        int16 // cooldown lancer de pavé
	WetTimer      int16 // aspergé par le canon à eau (ralenti)
	X, Y          float64
	Speed         float64
	Determination float64 // 0..1 : volonté d'avancer vers l'objectif
	WanderAng     float64
}

type Cop struct {
	ID        int64
	X, Y      float64
	Speed     float64
	WanderAng float64
	GasCd     int16
	BlastCd   int16
	FallTimer int16 // à terre (fil tendu)
	SlowTimer int16 // ralenti après s'être relevé
	HasLBD     bool  // équipé d'un lanceur LBD
	LBDCd      int16 // cooldown LBD
	Eliminated bool  // mis hors de combat (pavé)
}

// Shot : tir à visualiser quelques ticks (pavé ou LBD).
type Shot struct {
	X0, Y0, X1, Y1 float64
	Kind           int // 0 = pavé, 1 = LBD
	Age            int
}

// WaterCannon : camion à eau fixe projetant un jet en cône.
type WaterCannon struct {
	X, Y float64
	Ang  float64 // direction du jet
}

// Tripwire : fil tendu par les manifestants, fait tomber les CRS.
type Tripwire struct {
	X, Y float64
	Uses int16
}

type Gas struct {
	X, Y   float64
	R      float64
	MaxR   float64
	Age    int
	Thrown bool // lacrymo renvoyée par les manifestants (bleue, fait reculer les CRS)
}

// Segment : un corridor de rue et ses deux intersections (bouts).
type Segment struct {
	X0, Y0, X1, Y1 float64 // boîte du corridor
	AX, AY         float64 // intersection A
	BX, BY         float64 // intersection B
}

// Points sérialisés vers le front (clés courtes : grosse foule).
type PPoint struct {
	X int16 `json:"x"`
	Y int16 `json:"y"`
	L bool  `json:"l"` // leader
	P bool  `json:"p"` // panique
	E bool  `json:"e"` // épuisé
	G bool  `json:"g"` // lunettes
	U bool  `json:"u"` // parapluie
}

type CPoint struct {
	X int16 `json:"x"`
	Y int16 `json:"y"`
}

type GPoint struct {
	X int16 `json:"x"`
	Y int16 `json:"y"`
	R int16 `json:"r"`
	B bool  `json:"b"` // renvoyée (bleue)
}

type CannonPoint struct {
	X int16   `json:"x"`
	Y int16   `json:"y"`
	A float64 `json:"a"` // angle du jet
}

type ShotPoint struct {
	X0 int16 `json:"x0"`
	Y0 int16 `json:"y0"`
	X1 int16 `json:"x1"`
	Y1 int16 `json:"y1"`
	K  int8  `json:"k"` // 0 = pavé, 1 = LBD
	A  int8  `json:"a"` // âge (fade)
}

type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

type Stats struct {
	Seed       int64   `json:"seed"`
	Cops       int     `json:"cops"`
	CopsGone   int     `json:"cops_gone"`
	Active     int     `json:"active"`
	Passed     int     `json:"passed"`
	Arrested   int     `json:"arrested"`
	Killed     int     `json:"killed"`
	Paves      int     `json:"paves"`
	Panicked   int     `json:"panicked"`
	Exhausted  int     `json:"exhausted"`
	Leaders    int     `json:"leaders"`
	Civils     int     `json:"civils"`
	Gases      int     `json:"gases"`
	Goggles    int     `json:"goggles"`
	Umbrellas  int     `json:"umbrellas"`
	Tripwires  int     `json:"tripwires"`
	CopsDown   int     `json:"cops_down"`
	PassRate   float64 `json:"pass_rate"`
	IsFinished bool    `json:"is_finished"`
}

type ConfigEcho struct {
	Seed          int64   `json:"seed"`
	Protesters    int     `json:"protesters"`
	Cops          int     `json:"cops"`
	Aggr          float64 `json:"aggr"`
	LBDRatio      float64 `json:"lbd_ratio"`
	LBDKill       float64 `json:"lbd_kill"`
	LBDRate       float64 `json:"lbd_rate"`
	GasRate       float64 `json:"gas_rate"`
	CounterChance float64 `json:"counter_chance"`
	PaveRatio     float64 `json:"pave_ratio"`
	PaveRate      float64 `json:"pave_rate"`
	PaveHit       float64 `json:"pave_hit"`
	TripwireRate  float64 `json:"tripwire_rate"`
	Paused        bool    `json:"paused"`
	Stopped       bool    `json:"stopped"`
}

type FramePayload struct {
	TPS        int           `json:"tps"`
	Config     ConfigEcho    `json:"config"`
	WorldSize  float64       `json:"world_size"`
	GridCols   int           `json:"grid_cols"`
	GridRows   int           `json:"grid_rows"`
	Stats      Stats         `json:"stats"`
	Obstacles  []Obstacle    `json:"obstacles"`
	Objective  Rect          `json:"objective"`
	Kettles    []Rect        `json:"kettles"`
	Tripwires  []CPoint      `json:"tripwires"`
	Cannons    []CannonPoint `json:"cannons"`
	Shots      []ShotPoint   `json:"shots"`
	Gases      []GPoint      `json:"gases"`
	Protesters []PPoint      `json:"protesters"`
	Cops       []CPoint      `json:"cops"`
	Heatmap    []int         `json:"heatmap"`
}

var (
	simRng       *rand.Rand
	protesters   []Protester
	cops         []Cop
	obstacles    []Obstacle
	gases          []Gas
	tripwires      []Tripwire
	waterCannons   []WaterCannon
	shots          []Shot
	segments       []Segment
	sealedRects    []Rect
	passed         int
	arrested       int
	killed         int
	pavesThrown    int
	copsEliminated int

	// Navigation.
	navBlocked []bool
	navDirX    []float64
	navDirY    []float64
	navTick    int

	// Grille spatiale (spatial hashing) pour les interactions de foule — buffers
	// réutilisés entre ticks (zero-alloc). Listes chaînées : gridHeads[cell] = premier
	// manifestant de la case, gridNext[i] = suivant.
	gridCellSize float64
	gridCols     int
	gridRows     int
	gridHeads    []int
	gridNext     []int
)

// buildGrid range les manifestants actifs dans la grille (cases = rayon d'interaction).
func buildGrid() {
	gridCellSize = CohesionRadius
	gridCols = int(WorldSize/gridCellSize) + 1
	gridRows = gridCols
	nCells := gridCols * gridRows
	if len(gridHeads) != nCells {
		gridHeads = make([]int, nCells)
	}
	for i := range gridHeads {
		gridHeads[i] = -1
	}
	if len(gridNext) != len(protesters) {
		gridNext = make([]int, len(protesters))
	}
	for i := range protesters {
		p := &protesters[i]
		if p.Arrested || p.Passed {
			gridNext[i] = -1
			continue
		}
		cx := int(p.X / gridCellSize)
		cy := int(p.Y / gridCellSize)
		if cx < 0 {
			cx = 0
		} else if cx >= gridCols {
			cx = gridCols - 1
		}
		if cy < 0 {
			cy = 0
		} else if cy >= gridRows {
			cy = gridRows - 1
		}
		c := cy*gridCols + cx
		gridNext[i] = gridHeads[c]
		gridHeads[c] = i
	}
}

var (
	// Contrôle de simulation.
	simMu   sync.Mutex
	cfg     SimConfig
	paused  bool
	stopped bool
)

// Tas pour Dijkstra du flow field.
type navNode struct {
	cell int
	dist int
}
type navHeap []navNode

func (h navHeap) Len() int            { return len(h) }
func (h navHeap) Less(i, j int) bool  { return h[i].dist < h[j].dist }
func (h navHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *navHeap) Push(x interface{}) { *h = append(*h, x.(navNode)) }
func (h *navHeap) Pop() interface{} {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
}

// initNav marque les cellules bloquées (murs/immeubles) une fois la carte générée.
func initNav() {
	navBlocked = make([]bool, NavN*NavN)
	navDirX = make([]float64, NavN*NavN)
	navDirY = make([]float64, NavN*NavN)
	cell := WorldSize / float64(NavN)
	for i := range navBlocked {
		gx := i % NavN
		gy := i / NavN
		x := (float64(gx) + 0.5) * cell
		y := (float64(gy) + 0.5) * cell
		navBlocked[i] = collides(x, y)
	}
	navTick = 0
}

// computeNavField : Dijkstra multi-source depuis l'objectif, pénalisant le gaz.
func computeNavField() {
	n := NavN * NavN
	cell := WorldSize / float64(NavN)
	dist := make([]int, n)
	const INF = int(1) << 30
	for i := range dist {
		dist[i] = INF
	}

	// Coût d'entrée par cellule (1 + pénalité gaz).
	enter := make([]int, n)
	for i := 0; i < n; i++ {
		enter[i] = 1
		if navBlocked[i] {
			continue
		}
		gx := i % NavN
		gy := i / NavN
		x := (float64(gx) + 0.5) * cell
		y := (float64(gy) + 0.5) * cell
		for gi := range gases {
			if gases[gi].Thrown {
				continue // seule la lacrymo CRS (jaune) est contournée
			}
			dx := x - gases[gi].X
			dy := y - gases[gi].Y
			rr := gases[gi].R + 40.0
			if dx*dx+dy*dy < rr*rr {
				enter[i] = 1 + NavGasCost
				break
			}
		}
	}

	h := &navHeap{}
	heap.Init(h)
	for i := 0; i < n; i++ {
		if navBlocked[i] {
			continue
		}
		gy := i / NavN
		y := (float64(gy) + 0.5) * cell
		if y <= ObjectiveY {
			dist[i] = 0
			heap.Push(h, navNode{i, 0})
		}
	}

	for h.Len() > 0 {
		cur := heap.Pop(h).(navNode)
		if cur.dist > dist[cur.cell] {
			continue
		}
		gx := cur.cell % NavN
		gy := cur.cell / NavN
		nbs := [4]int{}
		cnt := 0
		if gx > 0 {
			nbs[cnt] = cur.cell - 1
			cnt++
		}
		if gx < NavN-1 {
			nbs[cnt] = cur.cell + 1
			cnt++
		}
		if gy > 0 {
			nbs[cnt] = cur.cell - NavN
			cnt++
		}
		if gy < NavN-1 {
			nbs[cnt] = cur.cell + NavN
			cnt++
		}
		for k := 0; k < cnt; k++ {
			nb := nbs[k]
			if navBlocked[nb] {
				continue
			}
			nd := cur.dist + enter[nb]
			if nd < dist[nb] {
				dist[nb] = nd
				heap.Push(h, navNode{nb, nd})
			}
		}
	}

	// Direction = vers le voisin de plus faible distance.
	for i := 0; i < n; i++ {
		navDirX[i] = 0
		navDirY[i] = 0
		if navBlocked[i] || dist[i] == INF {
			continue
		}
		gx := i % NavN
		gy := i / NavN
		best := dist[i]
		bx, by := 0, 0
		if gx > 0 && !navBlocked[i-1] && dist[i-1] < best {
			best = dist[i-1]
			bx, by = -1, 0
		}
		if gx < NavN-1 && !navBlocked[i+1] && dist[i+1] < best {
			best = dist[i+1]
			bx, by = 1, 0
		}
		if gy > 0 && !navBlocked[i-NavN] && dist[i-NavN] < best {
			best = dist[i-NavN]
			bx, by = 0, -1
		}
		if gy < NavN-1 && !navBlocked[i+NavN] && dist[i+NavN] < best {
			best = dist[i+NavN]
			bx, by = 0, 1
		}
		navDirX[i] = float64(bx)
		navDirY[i] = float64(by)
	}
}

// navDirAt renvoie la direction du flow field à une position monde.
func navDirAt(x, y float64) (float64, float64) {
	cell := WorldSize / float64(NavN)
	gx := int(x / cell)
	gy := int(y / cell)
	if gx < 0 || gx >= NavN || gy < 0 || gy >= NavN {
		return 0, -1
	}
	i := gy*NavN + gx
	dx, dy := navDirX[i], navDirY[i]
	if dx == 0 && dy == 0 {
		// Cellule sans direction (bord d'immeuble) : récupérer celle d'un voisin.
		if gx > 0 && (navDirX[i-1] != 0 || navDirY[i-1] != 0) {
			return navDirX[i-1], navDirY[i-1]
		}
		if gx < NavN-1 && (navDirX[i+1] != 0 || navDirY[i+1] != 0) {
			return navDirX[i+1], navDirY[i+1]
		}
		if gy > 0 && (navDirX[i-NavN] != 0 || navDirY[i-NavN] != 0) {
			return navDirX[i-NavN], navDirY[i-NavN]
		}
		if gy < NavN-1 && (navDirX[i+NavN] != 0 || navDirY[i+NavN] != 0) {
			return navDirX[i+NavN], navDirY[i+NavN]
		}
		return 0, -1 // repli : vers le haut
	}
	return dx, dy
}

// SimConfig : paramètres pilotables appliqués au (re)démarrage.
type SimConfig struct {
	Seed          int64
	Protesters    int
	Cops          int
	Aggr          float64 // agressivité CRS (0.5..3.0)
	LBDRatio      float64 // fraction de CRS équipés d'un LBD
	LBDKill       float64 // probabilité de tuer au LBD
	LBDRate       float64 // tirs LBD/s par CRS équipé
	GasRate       float64 // lacrymos/s par CRS
	CounterChance float64 // probabilité de renvoi de lacrymo
	PaveRatio     float64 // fraction de manifestants lanceurs de pavés
	PaveRate      float64 // pavés/s par lanceur
	PaveHit       float64 // probabilité de toucher un CRS
	TripwireRate  float64 // fils/s par meneur
}

func defaultConfig() SimConfig {
	return SimConfig{
		Seed:          MasterSeed,
		Protesters:    TotalProtesters,
		Cops:          TotalCops,
		Aggr:          1.0,
		LBDRatio:      LBDCopRatio,
		LBDKill:       LBDKillChance,
		LBDRate:       float64(TargetFPS) / float64(LBDCooldown),
		GasRate:       float64(TargetFPS) / float64(GasCooldownTicks),
		CounterChance: CounterGasChance,
		PaveRatio:     0.35,
		PaveRate:      0.6,
		PaveHit:       0.5,
		TripwireRate:  TripwireDeployChance * float64(TargetFPS),
	}
}

// rateToCd convertit un taux (par seconde) en cooldown (ticks). 0 => jamais.
func rateToCd(ratePerSec float64) int16 {
	if ratePerSec <= 0 {
		return 32000
	}
	cd := float64(TargetFPS) / ratePerSec
	if cd < 1 {
		cd = 1
	}
	if cd > 32000 {
		cd = 32000
	}
	return int16(cd)
}

func collides(x, y float64) bool {
	if x <= 20 || x >= WorldSize-20 || y <= 20 || y >= WorldSize-20 {
		return true
	}
	for _, obs := range obstacles {
		if x >= obs.X && x <= obs.X+obs.W && y >= obs.Y && y <= obs.Y+obs.H {
			return true
		}
	}
	return false
}

func capVec(x, y, max float64) (float64, float64) {
	d := math.Hypot(x, y)
	if d > max && d > 1e-9 {
		return x / d * max, y / d * max
	}
	return x, y
}

// findFreeSpot cherche la case libre la plus proche (spawn/cordon dans la trame).
func findFreeSpot(x, y float64) (float64, float64) {
	if !collides(x, y) {
		return x, y
	}
	for r := 20.0; r < 900.0; r += 20.0 {
		for a := 0; a < 12; a++ {
			ang := float64(a) / 12.0 * 2 * math.Pi
			nx := x + math.Cos(ang)*r
			ny := y + math.Sin(ang)*r
			if !collides(nx, ny) {
				return nx, ny
			}
		}
	}
	return x, y
}

// stepAround tente d'avancer dans la direction (dirx,diry) et, si un mur bloque,
// essaie des angles de plus en plus ouverts pour contourner (wall-following naïf).
func stepAround(x, y, dirx, diry, speed float64) (float64, float64, bool) {
	d := math.Hypot(dirx, diry)
	if d < 1e-9 {
		return x, y, false
	}
	base := math.Atan2(diry, dirx)
	for _, off := range []float64{0, 0.4, -0.4, 0.9, -0.9, 1.5, -1.5} {
		a := base + off
		nx := x + math.Cos(a)*speed
		ny := y + math.Sin(a)*speed
		if !collides(nx, ny) {
			return nx, ny, true
		}
	}
	return x, y, false
}

func initSimulation(c SimConfig) {
	if c.Protesters < 1 {
		c.Protesters = 1
	}
	if c.Cops < 2 {
		c.Cops = 2
	}
	if c.Aggr <= 0 {
		c.Aggr = 1.0
	}
	c.LBDRatio = math.Max(0, math.Min(1, c.LBDRatio))
	c.LBDKill = math.Max(0, math.Min(1, c.LBDKill))
	c.CounterChance = math.Max(0, math.Min(1, c.CounterChance))
	c.PaveRatio = math.Max(0, math.Min(1, c.PaveRatio))
	c.PaveHit = math.Max(0, math.Min(1, c.PaveHit))
	c.LBDRate = math.Max(0, c.LBDRate)
	c.GasRate = math.Max(0, c.GasRate)
	c.PaveRate = math.Max(0, c.PaveRate)
	c.TripwireRate = math.Max(0, c.TripwireRate)
	cfg = c
	paused = false
	stopped = false
	simRng = rand.New(rand.NewSource(c.Seed))
	passed = 0
	arrested = 0
	killed = 0
	pavesThrown = 0
	copsEliminated = 0
	gases = nil
	tripwires = nil
	waterCannons = nil
	shots = nil

	// Trame de rues : immeubles réguliers séparés par des rues. L'objectif (haut)
	// et l'avenue centrale restent dégagés. Les angles d'immeubles bordent les rues,
	// ce qui rend les nasses de rue possibles.
	obstacles = nil
	period := BlockSize + StreetWidth
	avMinX := AvenueCenterX - AvenueHalfW
	avMaxX := AvenueCenterX + AvenueHalfW
	for bx := StreetWidth; bx+BlockSize < WorldSize-StreetWidth; bx += period {
		for by := StreetWidth; by+BlockSize < WorldSize-StreetWidth; by += period {
			if by < ObjectiveY+StreetWidth { // dégager l'objectif en haut
				continue
			}
			if bx+BlockSize > avMinX && bx < avMaxX { // ne pas obstruer l'avenue
				continue
			}
			obstacles = append(obstacles, Obstacle{X: bx, Y: by, W: BlockSize, H: BlockSize})
		}
	}

	// Manifestants : foule dense en bas.
	protesters = make([]Protester, c.Protesters)
	for i := 0; i < c.Protesters; i++ {
		var x, y float64
		for {
			x = 350.0 + simRng.Float64()*(WorldSize-700.0)
			y = SpawnMinY + simRng.Float64()*(SpawnMaxY-SpawnMinY)
			if !collides(x, y) {
				break
			}
		}
		leader := simRng.Float64() < LeaderRatio
		det := 0.25 + simRng.Float64()*0.30 // civils : avancée modérée
		if leader {
			det = 0.75 + simRng.Float64()*0.25 // meneurs : forte détermination
		}
		protesters[i] = Protester{
			ID:            int64(i),
			IsLeader:      leader,
			Goggles:       simRng.Float64() < GogglesRatio,
			Umbrella:      simRng.Float64() < UmbrellaRatio,
			IsThrower:     simRng.Float64() < c.PaveRatio,
			Stamina:       MaxStamina - int16(simRng.Intn(20)),
			X:             x,
			Y:             y,
			Speed:         2.0 + simRng.Float64()*0.7,
			Determination: det,
			WanderAng:     simRng.Float64() * 2 * math.Pi,
		}
	}

	// CRS : cordon initial en travers de l'avenue.
	cops = make([]Cop, c.Cops)
	for i := 0; i < c.Cops; i++ {
		x := 400.0 + (WorldSize-800.0)*float64(i)/float64(c.Cops-1)
		y := CordonY + (simRng.Float64()-0.5)*120.0
		x, y = findFreeSpot(x, y)
		cops[i] = Cop{
			ID:        int64(i),
			X:         x,
			Y:         y,
			Speed:     2.5 + simRng.Float64()*0.4,
			WanderAng: simRng.Float64() * 2 * math.Pi,
			HasLBD:    simRng.Float64() < c.LBDRatio,
		}
	}

	// Canons à eau : camions fixes répartis sur la ligne du cordon.
	nCannons := c.Cops / WaterCannonPer
	if nCannons < WaterCannonMin {
		nCannons = WaterCannonMin
	}
	waterCannons = make([]WaterCannon, nCannons)
	for i := 0; i < nCannons; i++ {
		cx := 500.0 + (WorldSize-1000.0)*float64(i)/(float64(nCannons-1)+1e-9)
		cy := CordonY - 120.0
		cx, cy = findFreeSpot(cx, cy)
		waterCannons[i] = WaterCannon{X: cx, Y: cy, Ang: math.Pi / 2}
	}

	// Pré-calcul des segments de rue (corridors + intersections) pour la nasse.
	segments = nil
	sealedRects = nil
	K := int(WorldSize / period)
	vsX := func(k int) float64 { return float64(k)*period + StreetWidth/2 }
	hsY := func(h int) float64 { return float64(h)*period + StreetWidth/2 }
	for k := 0; k <= K; k++ {
		x := vsX(k)
		if x > WorldSize {
			break
		}
		for h := 0; h < K; h++ {
			ay, by := hsY(h), hsY(h+1)
			if by > WorldSize {
				break
			}
			if by < ObjectiveY {
				continue
			}
			segments = append(segments, Segment{
				X0: x - StreetWidth/2, Y0: ay,
				X1: x + StreetWidth/2, Y1: by,
				AX: x, AY: ay, BX: x, BY: by,
			})
		}
	}
	for h := 0; h <= K; h++ {
		y := hsY(h)
		if y > WorldSize {
			break
		}
		if y < ObjectiveY {
			continue
		}
		for k := 0; k < K; k++ {
			ax, bx := vsX(k), vsX(k+1)
			if bx > WorldSize {
				break
			}
			segments = append(segments, Segment{
				X0: ax, Y0: y - StreetWidth/2,
				X1: bx, Y1: y + StreetWidth/2,
				AX: ax, AY: y, BX: bx, BY: y,
			})
		}
	}

	initNav()
}

// protScratch : résultats des scans coûteux (lecture seule) d'un manifestant,
// calculés en parallèle puis consommés par la phase séquentielle.
type protScratch struct {
	skip                     bool
	inGasRaw                 bool
	gasFx, gasFy             float64
	copFx, copFy             float64
	nearestCopD2             float64
	nearestCopX, nearestCopY float64
	nearestCopIdx            int
	encircle                 int
	sepX, sepY               float64
	cohX, cohY               float64
	cohCount                 int
	leaderX, leaderY         float64
	leaderD2                 float64
	hasLeader                bool
}

var scratch []protScratch

// scanProtester calcule les forces de voisinage (gaz, CRS, foule) d'un manifestant
// SANS rien muter d'autre que son propre scratch[i]. Parallélisable sans course :
// les positions sont gelées pendant cette phase.
func scanProtester(i int) {
	p := &protesters[i]
	s := &scratch[i]
	*s = protScratch{nearestCopD2: math.MaxFloat64, nearestCopIdx: -1}
	if p.Arrested || p.Passed || p.Y <= ObjectiveY {
		s.skip = true
		return
	}

	// Gaz (hors lacrymo renvoyée) : distances au carré, racine seulement dans le nuage.
	for gi := range gases {
		g := &gases[gi]
		if g.Thrown {
			continue
		}
		dx := p.X - g.X
		dy := p.Y - g.Y
		rr := g.R + 40.0
		d2 := dx*dx + dy*dy
		if d2 < rr*rr {
			s.inGasRaw = true
			if d2 > 1e-12 {
				d := math.Sqrt(d2)
				s.gasFx += dx / d
				s.gasFy += dy / d
			}
		}
	}

	// CRS : répulsion + plus proche + encerclement (carré partout).
	encR2 := EncircleRadius * EncircleRadius
	fearR2 := CopFearRadius * CopFearRadius
	for ci := range cops {
		c := &cops[ci]
		if c.Eliminated {
			continue
		}
		dx := p.X - c.X
		dy := p.Y - c.Y
		d2 := dx*dx + dy*dy
		if d2 < s.nearestCopD2 {
			s.nearestCopD2 = d2
			s.nearestCopX, s.nearestCopY = c.X, c.Y
			s.nearestCopIdx = ci
		}
		if d2 < encR2 {
			s.encircle++
		}
		if d2 < fearR2 && d2 > 1e-12 {
			d := math.Sqrt(d2)
			w := (CopFearRadius - d) / CopFearRadius
			s.copFx += dx / d * w
			s.copFy += dy / d * w
		}
	}

	// Foule (grille spatiale) : séparation + cohésion + meneur le plus proche.
	sepR2 := SeparationRadius * SeparationRadius
	cohR2 := CohesionRadius * CohesionRadius
	s.leaderD2 = math.MaxFloat64
	pcx := int(p.X / gridCellSize)
	pcy := int(p.Y / gridCellSize)
	for gy := pcy - 1; gy <= pcy+1; gy++ {
		if gy < 0 || gy >= gridRows {
			continue
		}
		for gx := pcx - 1; gx <= pcx+1; gx++ {
			if gx < 0 || gx >= gridCols {
				continue
			}
			for j := gridHeads[gy*gridCols+gx]; j != -1; j = gridNext[j] {
				if j == i {
					continue
				}
				o := &protesters[j]
				dx := p.X - o.X
				dy := p.Y - o.Y
				d2 := dx*dx + dy*dy
				if d2 < sepR2 && d2 > 1e-12 {
					d := math.Sqrt(d2)
					s.sepX += dx / d
					s.sepY += dy / d
				}
				if d2 < cohR2 {
					s.cohX += o.X
					s.cohY += o.Y
					s.cohCount++
				}
				if o.IsLeader && d2 < s.leaderD2 {
					s.leaderD2 = d2
					s.leaderX, s.leaderY = o.X, o.Y
					s.hasLeader = true
				}
			}
		}
	}
}

func updateSimulation() {
	aggr := cfg.Aggr
	if aggr <= 0 {
		aggr = 1.0
	}
	arrestChance := ArrestChance * aggr
	kettleChance := KettleArrestChance * aggr
	gasThresh := int(float64(GasThrowThreshold) / aggr)
	if gasThresh < 1 {
		gasThresh = 1
	}

	// Recalcul périodique du flow field (le gaz bouge => itinéraires de contournement).
	if navTick%NavRecalc == 0 {
		computeNavField()
	}
	navTick++

	// Pré-passe : centre de gravité de la foule active.
	var sumX, sumY, activeCount float64
	for i := range protesters {
		p := &protesters[i]
		if p.Arrested || p.Passed {
			continue
		}
		sumX += p.X
		sumY += p.Y
		activeCount++
	}
	crowdX, crowdY := WorldSize/2, WorldSize/2
	if activeCount > 0 {
		crowdX = sumX / activeCount
		crowdY = sumY / activeCount
	}

	// Range les manifestants dans la grille spatiale (interactions de foule en ~O(n)).
	buildGrid()

	// 1a. Phase PARALLÈLE (lecture seule) : scans de voisinage (gaz/CRS/foule) répartis
	// sur les cœurs via un worker pool borné. Positions gelées => aucune course.
	if len(scratch) != len(protesters) {
		scratch = make([]protScratch, len(protesters))
	}
	nWorkers := runtime.GOMAXPROCS(0)
	if nWorkers < 1 {
		nWorkers = 1
	}
	if nWorkers > len(protesters) {
		nWorkers = len(protesters)
	}
	var wg sync.WaitGroup
	chunk := (len(protesters) + nWorkers - 1) / nWorkers
	for w := 0; w < nWorkers; w++ {
		start := w * chunk
		if start >= len(protesters) {
			break
		}
		end := start + chunk
		if end > len(protesters) {
			end = len(protesters)
		}
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			for i := start; i < end; i++ {
				scanProtester(i)
			}
		}(start, end)
	}
	wg.Wait()

	// 1b. Phase SÉQUENTIELLE : décisions (RNG) + déplacement, dans l'ordre => déterministe
	// et tous les écrits partagés (compteurs, append, mutations CRS) restent mono-thread.
	for i := range protesters {
		p := &protesters[i]
		if p.Arrested || p.Passed {
			continue
		}
		if p.Y <= ObjectiveY {
			p.Passed = true
			passed++
			continue
		}
		s := &scratch[i]

		if p.PanicTimer > 0 {
			p.PanicTimer--
		}
		if p.CounterCd > 0 {
			p.CounterCd--
		}
		if p.PaveCd > 0 {
			p.PaveCd--
		}
		if p.WetTimer > 0 {
			p.WetTimer--
		}

		// Lunettes : immunité (ni panique ni ralentissement).
		inGas := s.inGasRaw && !p.Goggles
		if inGas {
			p.PanicTimer = PanicDuration
		}
		p.InGas = inGas
		p.IsPanicked = p.PanicTimer > 0

		// Contre-lacrymo : un porteur de lunettes dans un nuage peut le renvoyer.
		counterWanted := s.inGasRaw && p.Goggles && p.CounterCd == 0 && simRng.Float64() < cfg.CounterChance

		// Alias des scans précalculés (phase parallèle) : le code en aval est inchangé.
		copFx, copFy := s.copFx, s.copFy
		gasFx, gasFy := s.gasFx, s.gasFy
		nearestCopD2 := s.nearestCopD2
		nearestCopX, nearestCopY := s.nearestCopX, s.nearestCopY
		nearestCopIdx := s.nearestCopIdx
		encircle := s.encircle

		// Condition d'interpellation : contact + (nasse OU gazé OU épuisé OU à bout de souffle).
		if nearestCopD2 < ArrestRadius*ArrestRadius {
			if encircle >= EncircleCops || inGas || p.IsExhausted || p.Stamina < 25 {
				if simRng.Float64() < arrestChance {
					p.Arrested = true
					arrested++
					continue
				}
			}
		}

		// Contre-lacrymo : renvoie un nuage près du CRS le plus proche.
		if counterWanted && nearestCopD2 < 1200.0*1200.0 && len(gases) < MaxGases {
			dx := nearestCopX - p.X
			dy := nearestCopY - p.Y
			dl := math.Hypot(dx, dy)
			if dl < 1e-6 {
				dl = 1
			}
			gases = append(gases, Gas{
				X:      nearestCopX - dx/dl*60.0,
				Y:      nearestCopY - dy/dl*60.0,
				R:      30.0,
				MaxR:   GasMaxRadius * 0.7,
				Thrown: true,
			})
			p.CounterCd = CounterGasCd
		}

		// Fil tendu : un meneur en pose un quand des CRS approchent.
		if p.IsLeader && nearestCopD2 < TripwireRange*TripwireRange && len(tripwires) < TripwireMaxActive &&
			simRng.Float64() < cfg.TripwireRate/float64(TargetFPS) {
			tripwires = append(tripwires, Tripwire{X: p.X, Y: p.Y, Uses: TripwireUses})
		}

		// Pavé : un lanceur vise le CRS le plus proche (recul + étourdissement).
		if p.IsThrower && cfg.PaveRate > 0 && p.PaveCd == 0 && !p.IsExhausted &&
			nearestCopIdx >= 0 && nearestCopD2 < PaveRange*PaveRange {
			p.PaveCd = rateToCd(cfg.PaveRate)
			pavesThrown++
			c := &cops[nearestCopIdx]
			shots = append(shots, Shot{X0: p.X, Y0: p.Y, X1: c.X, Y1: c.Y, Kind: 0})
			if simRng.Float64() < cfg.PaveHit {
				dx := c.X - p.X
				dy := c.Y - p.Y
				d := math.Hypot(dx, dy)
				if d < 1e-6 {
					d = 1
				}
				if !collides(c.X+dx/d*PavePush, c.Y) {
					c.X += dx / d * PavePush
				}
				if !collides(c.X, c.Y+dy/d*PavePush) {
					c.Y += dy / d * PavePush
				}
				if c.SlowTimer < PaveStagger {
					c.SlowTimer = PaveStagger
				}
				if simRng.Float64() < PaveKill {
					c.Eliminated = true
					copsEliminated++
				} else if simRng.Float64() < PaveKnock {
					c.FallTimer = CopFallTicks / 2
				}
			}
		}

		// Épuisé : immobilisé le temps de récupérer (reste interpellable).
		if p.IsExhausted {
			p.Stamina += StaminaRecover
			if p.Stamina >= StaminaResume {
				p.IsExhausted = false
			}
			continue
		}

		// Somme des forces.
		var fx, fy float64
		goalW := p.Determination
		if p.IsPanicked {
			goalW *= 0.1
		}
		// Objectif via flow field (routage par les rues, contournement du gaz).
		ndx, ndy := navDirAt(p.X, p.Y)
		fx += ndx * goalW
		fy += ndy * goalW

		copW := 1.4 - p.Determination // les meneurs résistent mieux au cordon
		if p.IsPanicked {
			copW *= 1.8
		}
		fx += copFx * copW
		fy += copFy * copW

		if inGas {
			gl := math.Hypot(gasFx, gasFy)
			if gl > 1e-6 {
				fx += gasFx / gl * 1.8
				fy += gasFy / gl * 1.8
			}
		}

		// Interactions de foule (séparation/cohésion/meneur) : précalculées en phase
		// parallèle (scanProtester), on lit juste le scratch.
		sepX, sepY := s.sepX, s.sepY
		cohX, cohY, cohCount := s.cohX, s.cohY, s.cohCount
		leaderBd := s.leaderD2
		leaderX, leaderY := s.leaderX, s.leaderY
		hasLeader := s.hasLeader
		// Les civils calmes suivent le meneur proche repéré ci-dessus.
		if hasLeader && !p.IsLeader && !p.IsPanicked {
			d := math.Sqrt(leaderBd)
			if d > 1e-6 {
				fx += (leaderX - p.X) / d * 0.4
				fy += (leaderY - p.Y) / d * 0.4
			}
		}
		fx += sepX * SeparationWeight
		fy += sepY * SeparationWeight
		if cohCount > 0 {
			cx := cohX/float64(cohCount) - p.X
			cy := cohY/float64(cohCount) - p.Y
			cl := math.Hypot(cx, cy)
			if cl > 1e-6 {
				fx += cx / cl * CohesionWeight
				fy += cy / cl * CohesionWeight
			}
		}

		fx += (simRng.Float64() - 0.5) * 0.3
		fy += (simRng.Float64() - 0.5) * 0.3

		// Endurance : fuir (panique/gaz) épuise, marcher récupère.
		sp := p.Speed
		fleeing := p.IsPanicked || inGas
		if fleeing {
			p.Stamina -= StaminaDrain
			if p.Stamina <= 0 {
				p.Stamina = 0
				p.IsExhausted = true
				continue
			}
			sp *= 1.2
		} else {
			if p.Stamina < MaxStamina {
				p.Stamina++
			}
			sp *= 0.9
		}
		if inGas {
			sp *= GasSlowFactor
		}
		if p.WetTimer > 0 {
			sp *= WaterWetSlow
		}

		dx, dy := capVec(fx, fy, sp)
		nx := p.X + dx
		ny := p.Y + dy
		if !collides(nx, p.Y) {
			p.X = nx
		}
		if !collides(p.X, ny) {
			p.Y = ny
		}
	}

	// 2. CRS : interception, lacrymo, grenade de désencerclement.
	for i := range cops {
		c := &cops[i]
		if c.Eliminated {
			continue
		}
		if c.GasCd > 0 {
			c.GasCd--
		}
		if c.BlastCd > 0 {
			c.BlastCd--
		}

		// À terre (fil tendu) : immobile, puis passe en ralenti.
		if c.FallTimer > 0 {
			c.FallTimer--
			if c.FallTimer == 0 {
				c.SlowTimer = CopSlowTicks
			}
			continue
		}
		if c.SlowTimer > 0 {
			c.SlowTimer--
		} else {
			// Déclenchement d'un fil tendu (uniquement si pas déjà en train de récupérer).
			for ti := range tripwires {
				t := &tripwires[ti]
				if t.Uses <= 0 {
					continue
				}
				dx := c.X - t.X
				dy := c.Y - t.Y
				if dx*dx+dy*dy < TripwireRadius*TripwireRadius {
					c.FallTimer = CopFallTicks
					t.Uses--
					break
				}
			}
			if c.FallTimer > 0 {
				continue
			}
		}
		copSpeed := c.Speed
		if c.SlowTimer > 0 {
			copSpeed *= 0.5
		}

		var targetFront *Protester
		frontD2 := math.MaxFloat64
		var targetAny *Protester
		anyD2 := math.MaxFloat64
		senseCount := 0
		submerged := 0

		for j := range protesters {
			p := &protesters[j]
			if p.Arrested || p.Passed {
				continue
			}
			dx := p.X - c.X
			dy := p.Y - c.Y
			d2 := dx*dx + dy*dy
			if d2 >= CopVisionRadius*CopVisionRadius {
				continue
			}
			if d2 < anyD2 {
				anyD2 = d2
				targetAny = p
			}
			if p.Y < c.Y && d2 < frontD2 { // devant le CRS, côté objectif
				frontD2 = d2
				targetFront = p
			}
			if d2 < GasThrowSense*GasThrowSense {
				senseCount++
			}
			if d2 < SubmergeRadius*SubmergeRadius {
				submerged++
			}
		}

		// Lacrymo : densité trop forte devant => tir vers la foule.
		if c.GasCd == 0 && cfg.GasRate > 0 && senseCount >= gasThresh && len(gases) < MaxGases {
			var dirx, diry float64
			if targetFront != nil {
				dirx, diry = targetFront.X-c.X, targetFront.Y-c.Y
			} else {
				dirx, diry = crowdX-c.X, crowdY-c.Y
			}
			dl := math.Hypot(dirx, diry)
			if dl < 1e-6 {
				dl = 1
			}
			gases = append(gases, Gas{
				X:    c.X + dirx/dl*200.0,
				Y:    c.Y + diry/dl*200.0,
				R:    30.0,
				MaxR: GasMaxRadius,
			})
			c.GasCd = rateToCd(cfg.GasRate)
		}

		// Grenade de désencerclement : CRS submergé => repousse tout autour.
		if c.BlastCd == 0 && submerged >= BlastThreshold {
			for j := range protesters {
				p := &protesters[j]
				if p.Arrested || p.Passed {
					continue
				}
				dx := p.X - c.X
				dy := p.Y - c.Y
				d := math.Hypot(dx, dy)
				if d < BlastRadius && d > 1e-6 {
					push := (BlastRadius - d) / BlastRadius * BlastPush
					pnx := p.X + dx/d*push
					pny := p.Y + dy/d*push
					if !collides(pnx, p.Y) {
						p.X = pnx
					}
					if !collides(p.X, pny) {
						p.Y = pny
					}
					p.PanicTimer = PanicDuration
				}
			}
			c.BlastCd = BlastCooldown
		}

		// LBD : sépare la foule autour de l'impact et peut blesser mortellement.
		if c.HasLBD {
			if c.LBDCd > 0 {
				c.LBDCd--
			}
			if c.LBDCd == 0 && cfg.LBDRate > 0 && targetFront != nil && frontD2 < LBDRange*LBDRange {
				impactX, impactY := targetFront.X, targetFront.Y
				shots = append(shots, Shot{X0: c.X, Y0: c.Y, X1: impactX, Y1: impactY, Kind: 1})
				kc := cfg.LBDKill
				if targetFront.Umbrella {
					kc *= UmbrellaKillFactor
				}
				if simRng.Float64() < kc {
					targetFront.Arrested = true
					killed++
				}
				for j := range protesters {
					p := &protesters[j]
					if p.Arrested || p.Passed {
						continue
					}
					dx := p.X - impactX
					dy := p.Y - impactY
					d := math.Hypot(dx, dy)
					if d < LBDRadius && d > 1e-6 {
						push := (LBDRadius - d) / LBDRadius * LBDPush
						if p.Umbrella {
							push *= UmbrellaWaterFactor
						}
						pnx := p.X + dx/d*push
						pny := p.Y + dy/d*push
						if !collides(pnx, p.Y) {
							p.X = pnx
						}
						if !collides(p.X, pny) {
							p.Y = pny
						}
						p.PanicTimer = PanicDuration
					}
				}
				c.LBDCd = rateToCd(cfg.LBDRate)
			}
		}

		// Déplacement : intercepter en priorité le manifestant le plus avancé proche.
		target := targetFront
		speedFactor := 1.0
		if target == nil {
			target = targetAny
			speedFactor = 0.8
		}
		var dx, dy float64
		if target != nil {
			tx := target.X - c.X
			ty := target.Y - c.Y
			d := math.Hypot(tx, ty)
			if d < 1e-6 {
				d = 1
			}
			dx = tx / d * copSpeed * speedFactor
			dy = ty / d * copSpeed * speedFactor
		} else {
			tx := crowdX - c.X
			ty := crowdY - c.Y
			d := math.Hypot(tx, ty)
			if d > 1e-6 {
				dx = tx / d * copSpeed * 0.7
				dy = ty / d * copSpeed * 0.7
			}
		}
		dx += (simRng.Float64() - 0.5) * 0.5
		dy += (simRng.Float64() - 0.5) * 0.5

		// Lacrymo renvoyée (bleue) : repousse les CRS, les force à reculer.
		for gi := range gases {
			g := &gases[gi]
			if !g.Thrown {
				continue
			}
			gdx := c.X - g.X
			gdy := c.Y - g.Y
			gd := math.Hypot(gdx, gdy)
			if gd < g.R+60.0 && gd > 1e-6 {
				dx += gdx / gd * copSpeed * 2.5
				dy += gdy / gd * copSpeed * 2.5
			}
		}

		// Contournement des immeubles (débloque les CRS coincés sur les angles).
		if nx, ny, ok := stepAround(c.X, c.Y, dx, dy, copSpeed); ok {
			c.X, c.Y = nx, ny
		}
	}

	// 2b. Nasse de rue : un segment tenu aux deux bouts par des CRS piège la foule.
	sealedRects = sealedRects[:0]
	r2 := KettleSealRadius * KettleSealRadius
	for si := range segments {
		seg := &segments[si]
		copA, copB := false, false
		for ci := range cops {
			c := &cops[ci]
			if c.Eliminated {
				continue
			}
			if !copA {
				dx, dy := c.X-seg.AX, c.Y-seg.AY
				if dx*dx+dy*dy <= r2 {
					copA = true
				}
			}
			if !copB {
				dx, dy := c.X-seg.BX, c.Y-seg.BY
				if dx*dx+dy*dy <= r2 {
					copB = true
				}
			}
			if copA && copB {
				break
			}
		}
		if !(copA && copB) {
			continue
		}
		sealedRects = append(sealedRects, Rect{X: seg.X0, Y: seg.Y0, W: seg.X1 - seg.X0, H: seg.Y1 - seg.Y0})
		for pi := range protesters {
			p := &protesters[pi]
			if p.Arrested || p.Passed {
				continue
			}
			if p.X >= seg.X0 && p.X <= seg.X1 && p.Y >= seg.Y0 && p.Y <= seg.Y1 {
				if simRng.Float64() < kettleChance {
					p.Arrested = true
					arrested++
				}
			}
		}
	}

	// 2c. Canons à eau : jet en cône, fort recul + ralentissement (amorti par le parapluie).
	for wi := range waterCannons {
		wc := &waterCannons[wi]
		wc.Ang = math.Atan2(crowdY-wc.Y, crowdX-wc.X)
		for pi := range protesters {
			p := &protesters[pi]
			if p.Arrested || p.Passed {
				continue
			}
			dx := p.X - wc.X
			dy := p.Y - wc.Y
			d := math.Hypot(dx, dy)
			if d > WaterCannonRange || d < 1e-6 {
				continue
			}
			ang := math.Atan2(dy, dx)
			da := math.Abs(math.Atan2(math.Sin(ang-wc.Ang), math.Cos(ang-wc.Ang)))
			if da > WaterCannonSpread {
				continue
			}
			push := (1 - d/WaterCannonRange) * WaterCannonPush
			wet := WaterWetTicks
			if p.Umbrella {
				push *= UmbrellaWaterFactor
				wet = WaterWetTicks / 2
			}
			pnx := p.X + dx/d*push
			pny := p.Y + dy/d*push
			if !collides(pnx, p.Y) {
				p.X = pnx
			}
			if !collides(p.X, pny) {
				p.Y = pny
			}
			p.WetTimer = wet
			p.PanicTimer = PanicDuration
		}
	}

	// 3. Vieillissement des nuages de gaz.
	if len(gases) > 0 {
		kept := gases[:0]
		for gi := range gases {
			g := gases[gi]
			g.Age++
			if g.Age < GasGrowTicks {
				g.R = g.MaxR * float64(g.Age) / float64(GasGrowTicks)
			} else {
				g.R = g.MaxR
			}
			if g.Age <= GasLifeTicks {
				kept = append(kept, g)
			}
		}
		gases = kept
	}

	// 3c. Vieillissement des traceurs de tir.
	if len(shots) > 0 {
		kept := shots[:0]
		for si := range shots {
			shots[si].Age++
			if shots[si].Age <= ShotLife {
				kept = append(kept, shots[si])
			}
		}
		shots = kept
	}

	// 3b. Retrait des fils tendus épuisés.
	if len(tripwires) > 0 {
		kept := tripwires[:0]
		for ti := range tripwires {
			if tripwires[ti].Uses > 0 {
				kept = append(kept, tripwires[ti])
			}
		}
		tripwires = kept
	}
}

func generateHeatmap() []int {
	grid := make([]int, GridCols*GridRows)
	cellW := WorldSize / float64(GridCols)
	cellH := WorldSize / float64(GridRows)
	for i := range protesters {
		p := &protesters[i]
		if p.Arrested || p.Passed {
			continue
		}
		gx := int(p.X / cellW)
		gy := int(p.Y / cellH)
		if gx >= 0 && gx < GridCols && gy >= 0 && gy < GridRows {
			grid[gy*GridCols+gx]++
		}
	}
	return grid
}

func getCounts() (active, panicked, exhausted, leaders, civils, goggles, umbrellas int) {
	for i := range protesters {
		p := &protesters[i]
		if p.Arrested || p.Passed {
			continue
		}
		active++
		if p.IsLeader {
			leaders++
		} else {
			civils++
		}
		if p.IsPanicked {
			panicked++
		}
		if p.IsExhausted {
			exhausted++
		}
		if p.Goggles {
			goggles++
		}
		if p.Umbrella {
			umbrellas++
		}
	}
	return
}

type controlMsg struct {
	Cmd           string  `json:"cmd"`
	Seed          int64   `json:"seed"`
	Protesters    int     `json:"protesters"`
	Cops          int     `json:"cops"`
	Aggr          float64 `json:"aggr"`
	LBDRatio      float64 `json:"lbd_ratio"`
	LBDKill       float64 `json:"lbd_kill"`
	LBDRate       float64 `json:"lbd_rate"`
	GasRate       float64 `json:"gas_rate"`
	CounterChance float64 `json:"counter_chance"`
	PaveRatio     float64 `json:"pave_ratio"`
	PaveRate      float64 `json:"pave_rate"`
	PaveHit       float64 `json:"pave_hit"`
	TripwireRate  float64 `json:"tripwire_rate"`
}

func wsHandler(ws *websocket.Conn) {
	defer ws.Close()

	simMu.Lock()
	initSimulation(defaultConfig())
	simMu.Unlock()

	// Goroutine de contrôle : lit les commandes du client (play/pause/restart/stop).
	go func() {
		for {
			var raw string
			if err := websocket.Message.Receive(ws, &raw); err != nil {
				return
			}
			var m controlMsg
			if err := json.Unmarshal([]byte(raw), &m); err != nil {
				continue
			}
			simMu.Lock()
			switch m.Cmd {
			case "pause":
				paused = true
			case "play":
				paused = false
				stopped = false
			case "stop":
				stopped = true
			case "restart":
				initSimulation(SimConfig{
					Seed:          m.Seed,
					Protesters:    m.Protesters,
					Cops:          m.Cops,
					Aggr:          m.Aggr,
					LBDRatio:      m.LBDRatio,
					LBDKill:       m.LBDKill,
					LBDRate:       m.LBDRate,
					GasRate:       m.GasRate,
					CounterChance: m.CounterChance,
					PaveRatio:     m.PaveRatio,
					PaveRate:      m.PaveRate,
					PaveHit:       m.PaveHit,
					TripwireRate:  m.TripwireRate,
				})
			}
			simMu.Unlock()
		}
	}()

	ticker := time.NewTicker(time.Second / time.Duration(TargetFPS))
	defer ticker.Stop()

	frames := 0
	lastCheck := time.Now()
	currentTPS := 0

	for range ticker.C {
		simMu.Lock()

		if !paused && !stopped {
			updateSimulation()
			frames++
		}
		if time.Since(lastCheck) >= time.Second {
			currentTPS = frames
			frames = 0
			lastCheck = time.Now()
		}

		active, panicked, exhausted, leaders, civils, goggles, umbrellas := getCounts()
		isFinished := (active == 0)

		pPoints := make([]PPoint, 0, active)
		for i := range protesters {
			p := &protesters[i]
			if p.Arrested || p.Passed {
				continue
			}
			pPoints = append(pPoints, PPoint{
				X: int16(p.X),
				Y: int16(p.Y),
				L: p.IsLeader,
				P: p.IsPanicked,
				E: p.IsExhausted,
				G: p.Goggles,
				U: p.Umbrella,
			})
		}

		copsDown := 0
		cPoints := make([]CPoint, 0, len(cops))
		for i := range cops {
			if cops[i].Eliminated {
				continue
			}
			cPoints = append(cPoints, CPoint{X: int16(cops[i].X), Y: int16(cops[i].Y)})
			if cops[i].FallTimer > 0 {
				copsDown++
			}
		}
		activeCops := len(cPoints)

		shotPoints := make([]ShotPoint, len(shots))
		for i := range shots {
			shotPoints[i] = ShotPoint{
				X0: int16(shots[i].X0), Y0: int16(shots[i].Y0),
				X1: int16(shots[i].X1), Y1: int16(shots[i].Y1),
				K: int8(shots[i].Kind), A: int8(shots[i].Age),
			}
		}

		twPoints := make([]CPoint, len(tripwires))
		for i := range tripwires {
			twPoints[i] = CPoint{X: int16(tripwires[i].X), Y: int16(tripwires[i].Y)}
		}

		caPoints := make([]CannonPoint, len(waterCannons))
		for i := range waterCannons {
			caPoints[i] = CannonPoint{X: int16(waterCannons[i].X), Y: int16(waterCannons[i].Y), A: waterCannons[i].Ang}
		}

		gPoints := make([]GPoint, len(gases))
		for i := range gases {
			gPoints[i] = GPoint{X: int16(gases[i].X), Y: int16(gases[i].Y), R: int16(gases[i].R), B: gases[i].Thrown}
		}

		resolved := passed + arrested + killed
		rate := 0.0
		if resolved > 0 {
			rate = (float64(passed) / float64(resolved)) * 100.0
		}

		payload := FramePayload{
			TPS: currentTPS,
			Config: ConfigEcho{
				Seed:          cfg.Seed,
				Protesters:    cfg.Protesters,
				Cops:          cfg.Cops,
				Aggr:          cfg.Aggr,
				LBDRatio:      cfg.LBDRatio,
				LBDKill:       cfg.LBDKill,
				LBDRate:       cfg.LBDRate,
				GasRate:       cfg.GasRate,
				CounterChance: cfg.CounterChance,
				PaveRatio:     cfg.PaveRatio,
				PaveRate:      cfg.PaveRate,
				PaveHit:       cfg.PaveHit,
				TripwireRate:  cfg.TripwireRate,
				Paused:        paused,
				Stopped:       stopped,
			},
			WorldSize: WorldSize,
			GridCols:  GridCols,
			GridRows:  GridRows,
			Stats: Stats{
				Seed:       cfg.Seed,
				Cops:       activeCops,
				CopsGone:   copsEliminated,
				Active:     active,
				Passed:     passed,
				Arrested:   arrested,
				Killed:     killed,
				Paves:      pavesThrown,
				Panicked:   panicked,
				Exhausted:  exhausted,
				Leaders:    leaders,
				Civils:     civils,
				Gases:      len(gases),
				Goggles:    goggles,
				Umbrellas:  umbrellas,
				Tripwires:  len(tripwires),
				CopsDown:   copsDown,
				PassRate:   math.Round(rate*10) / 10,
				IsFinished: isFinished,
			},
			Obstacles:  obstacles,
			Objective:  Rect{X: 0, Y: 0, W: WorldSize, H: ObjectiveY},
			Kettles:    sealedRects,
			Tripwires:  twPoints,
			Cannons:    caPoints,
			Shots:      shotPoints,
			Gases:      gPoints,
			Protesters: pPoints,
			Cops:       cPoints,
			Heatmap:    generateHeatmap(),
		}

		data, err := json.Marshal(payload)
		simMu.Unlock()

		if err != nil {
			break
		}
		if err := websocket.Message.Send(ws, string(data)); err != nil {
			break
		}
	}
}

// runBench exécute N ticks de simulation le plus vite possible (sans ticker ni
// réseau ni JSON) et affiche le débit réel. C'est la mesure de référence de l'audit.
func runBench(c SimConfig, ticks int) {
	initSimulation(c)
	start := time.Now()
	for i := 0; i < ticks; i++ {
		updateSimulation()
	}
	elapsed := time.Since(start)
	msPerTick := float64(elapsed.Microseconds()) / float64(ticks) / 1000.0
	ups := float64(ticks) / elapsed.Seconds()
	fmt.Printf("bench | %d ticks en %v | %.3f ms/tick | %.1f updates/s | seed=%d manifestants=%d CRS=%d\n",
		ticks, elapsed, msPerTick, ups, c.Seed, c.Protesters, c.Cops)
}

func main() {
	bench := flag.Bool("bench", false, "mode headless : exécute -ticks ticks sans ticker ni réseau, puis quitte")
	ticks := flag.Int("ticks", 1000, "nombre de ticks à simuler en mode bench")
	nProt := flag.Int("protesters", TotalProtesters, "nombre de manifestants")
	nCops := flag.Int("cops", TotalCops, "nombre de CRS")
	seed := flag.Int64("seed", MasterSeed, "graine déterministe")
	cpuprofile := flag.String("cpuprofile", "", "écrit un profil CPU dans ce fichier")
	memprofile := flag.String("memprofile", "", "écrit un profil mémoire dans ce fichier")
	flag.Parse()

	if *bench {
		if *cpuprofile != "" {
			f, err := os.Create(*cpuprofile)
			if err != nil {
				log.Fatal(err)
			}
			pprof.StartCPUProfile(f)
			defer pprof.StopCPUProfile()
		}

		c := defaultConfig()
		c.Seed = *seed
		c.Protesters = *nProt
		c.Cops = *nCops
		runBench(c, *ticks)

		if *memprofile != "" {
			f, err := os.Create(*memprofile)
			if err != nil {
				log.Fatal(err)
			}
			pprof.WriteHeapProfile(f)
			f.Close()
		}
		return
	}

	http.Handle("/", http.FileServer(http.Dir(".")))
	http.Handle("/ws", websocket.Handler(wsHandler))

	port := 8080
	fmt.Printf("Simulateur manif / encerclement CRS sur http://localhost:%d\n", port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
}
