package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
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
	BlastRadius      float64 = 220.0
	BlastPush        float64 = 60.0
	BlastCooldown    int16   = 120
	PanicDuration    int16   = 90

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
	Stamina       int16
	PanicTimer    int16
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
}

type Gas struct {
	X, Y float64
	R    float64
	MaxR float64
	Age  int
}

// Points sérialisés vers le front (clés courtes : grosse foule).
type PPoint struct {
	X int16 `json:"x"`
	Y int16 `json:"y"`
	L bool  `json:"l"` // leader
	P bool  `json:"p"` // panique
	E bool  `json:"e"` // épuisé
}

type CPoint struct {
	X int16 `json:"x"`
	Y int16 `json:"y"`
}

type GPoint struct {
	X int16 `json:"x"`
	Y int16 `json:"y"`
	R int16 `json:"r"`
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
	Active     int     `json:"active"`
	Passed     int     `json:"passed"`
	Arrested   int     `json:"arrested"`
	Panicked   int     `json:"panicked"`
	Exhausted  int     `json:"exhausted"`
	Leaders    int     `json:"leaders"`
	Civils     int     `json:"civils"`
	Gases      int     `json:"gases"`
	PassRate   float64 `json:"pass_rate"`
	IsFinished bool    `json:"is_finished"`
}

type FramePayload struct {
	TPS        int        `json:"tps"`
	WorldSize  float64    `json:"world_size"`
	GridCols   int        `json:"grid_cols"`
	GridRows   int        `json:"grid_rows"`
	Stats      Stats      `json:"stats"`
	Obstacles  []Obstacle `json:"obstacles"`
	Objective  Rect       `json:"objective"`
	Gases      []GPoint   `json:"gases"`
	Protesters []PPoint   `json:"protesters"`
	Cops       []CPoint   `json:"cops"`
	Heatmap    []int      `json:"heatmap"`
}

var (
	simRng     *rand.Rand
	protesters []Protester
	cops       []Cop
	obstacles  []Obstacle
	gases      []Gas
	passed     int
	arrested   int
)

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

func initSimulation(seed int64) {
	simRng = rand.New(rand.NewSource(seed))
	passed = 0
	arrested = 0
	gases = nil

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
	protesters = make([]Protester, TotalProtesters)
	for i := 0; i < TotalProtesters; i++ {
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
			Stamina:       MaxStamina - int16(simRng.Intn(20)),
			X:             x,
			Y:             y,
			Speed:         2.0 + simRng.Float64()*0.7,
			Determination: det,
			WanderAng:     simRng.Float64() * 2 * math.Pi,
		}
	}

	// CRS : cordon initial en travers de l'avenue.
	cops = make([]Cop, TotalCops)
	for i := 0; i < TotalCops; i++ {
		x := 400.0 + (WorldSize-800.0)*float64(i)/float64(TotalCops-1)
		y := CordonY + (simRng.Float64()-0.5)*120.0
		x, y = findFreeSpot(x, y)
		cops[i] = Cop{
			ID:        int64(i),
			X:         x,
			Y:         y,
			Speed:     2.5 + simRng.Float64()*0.4,
			WanderAng: simRng.Float64() * 2 * math.Pi,
		}
	}
}

func updateSimulation() {
	// Pré-passe : centre de gravité de la foule active + indices des meneurs.
	var sumX, sumY, activeCount float64
	leaderIdx := make([]int, 0, int(LeaderRatio*float64(TotalProtesters))+1)
	for i := range protesters {
		p := &protesters[i]
		if p.Arrested || p.Passed {
			continue
		}
		sumX += p.X
		sumY += p.Y
		activeCount++
		if p.IsLeader {
			leaderIdx = append(leaderIdx, i)
		}
	}
	crowdX, crowdY := WorldSize/2, WorldSize/2
	if activeCount > 0 {
		crowdX = sumX / activeCount
		crowdY = sumY / activeCount
	}

	// 1. Manifestants : perception CRS/gaz, interpellation, puis déplacement.
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

		if p.PanicTimer > 0 {
			p.PanicTimer--
		}

		// Nuages de gaz : répulsion + panique + ralentissement.
		inGas := false
		var gasFx, gasFy float64
		for gi := range gases {
			g := &gases[gi]
			dx := p.X - g.X
			dy := p.Y - g.Y
			d := math.Hypot(dx, dy)
			if d < g.R+40.0 {
				inGas = true
				p.PanicTimer = PanicDuration
				if d > 1e-6 {
					gasFx += dx / d
					gasFy += dy / d
				}
			}
		}
		p.InGas = inGas
		p.IsPanicked = p.PanicTimer > 0

		// Scan CRS : répulsion ressentie + données d'interpellation.
		var copFx, copFy float64
		nearestCopD := math.MaxFloat64
		encircle := 0
		for ci := range cops {
			c := &cops[ci]
			dx := p.X - c.X
			dy := p.Y - c.Y
			d := math.Hypot(dx, dy)
			if d < nearestCopD {
				nearestCopD = d
			}
			if d < EncircleRadius {
				encircle++
			}
			if d < CopFearRadius && d > 1e-6 {
				w := (CopFearRadius - d) / CopFearRadius
				copFx += dx / d * w
				copFy += dy / d * w
			}
		}

		// Condition d'interpellation : contact + (nasse OU gazé OU épuisé OU à bout de souffle).
		if nearestCopD < ArrestRadius {
			if encircle >= EncircleCops || inGas || p.IsExhausted || p.Stamina < 25 {
				if simRng.Float64() < ArrestChance {
					p.Arrested = true
					arrested++
					continue
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
		fy += -1.0 * goalW // objectif = vers le haut (Y décroissant)

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

		// Cohésion : les civils calmes suivent le meneur le plus proche.
		if !p.IsLeader && !p.IsPanicked {
			bd := math.MaxFloat64
			var lx, ly float64
			found := false
			for _, li := range leaderIdx {
				l := &protesters[li]
				dx := l.X - p.X
				dy := l.Y - p.Y
				d2 := dx*dx + dy*dy
				if d2 < bd {
					bd = d2
					lx, ly = l.X, l.Y
					found = true
				}
			}
			if found && bd < 1800.0*1800.0 {
				d := math.Sqrt(bd)
				if d > 1e-6 {
					fx += (lx - p.X) / d * 0.4
					fy += (ly - p.Y) / d * 0.4
				}
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
		if c.GasCd > 0 {
			c.GasCd--
		}
		if c.BlastCd > 0 {
			c.BlastCd--
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
		if c.GasCd == 0 && senseCount >= GasThrowThreshold && len(gases) < MaxGases {
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
			c.GasCd = GasCooldownTicks
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
			dx = tx / d * c.Speed * speedFactor
			dy = ty / d * c.Speed * speedFactor
		} else {
			tx := crowdX - c.X
			ty := crowdY - c.Y
			d := math.Hypot(tx, ty)
			if d > 1e-6 {
				dx = tx / d * c.Speed * 0.7
				dy = ty / d * c.Speed * 0.7
			}
		}
		dx += (simRng.Float64() - 0.5) * 0.5
		dy += (simRng.Float64() - 0.5) * 0.5

		// Contournement des immeubles (débloque les CRS coincés sur les angles).
		if nx, ny, ok := stepAround(c.X, c.Y, dx, dy, c.Speed); ok {
			c.X, c.Y = nx, ny
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

func getCounts() (active, panicked, exhausted, leaders, civils int) {
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
	}
	return
}

func wsHandler(ws *websocket.Conn) {
	defer ws.Close()

	initSimulation(MasterSeed)

	ticker := time.NewTicker(time.Second / time.Duration(TargetFPS))
	defer ticker.Stop()

	frames := 0
	lastCheck := time.Now()
	currentTPS := 0

	for range ticker.C {
		updateSimulation()

		frames++
		if time.Since(lastCheck) >= time.Second {
			currentTPS = frames
			frames = 0
			lastCheck = time.Now()
		}

		active, panicked, exhausted, leaders, civils := getCounts()
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
			})
		}

		cPoints := make([]CPoint, len(cops))
		for i := range cops {
			cPoints[i] = CPoint{X: int16(cops[i].X), Y: int16(cops[i].Y)}
		}

		gPoints := make([]GPoint, len(gases))
		for i := range gases {
			gPoints[i] = GPoint{X: int16(gases[i].X), Y: int16(gases[i].Y), R: int16(gases[i].R)}
		}

		resolved := passed + arrested
		rate := 0.0
		if resolved > 0 {
			rate = (float64(passed) / float64(resolved)) * 100.0
		}

		payload := FramePayload{
			TPS:       currentTPS,
			WorldSize: WorldSize,
			GridCols:  GridCols,
			GridRows:  GridRows,
			Stats: Stats{
				Seed:       MasterSeed,
				Cops:       len(cops),
				Active:     active,
				Passed:     passed,
				Arrested:   arrested,
				Panicked:   panicked,
				Exhausted:  exhausted,
				Leaders:    leaders,
				Civils:     civils,
				Gases:      len(gases),
				PassRate:   math.Round(rate*10) / 10,
				IsFinished: isFinished,
			},
			Obstacles:  obstacles,
			Objective:  Rect{X: 0, Y: 0, W: WorldSize, H: ObjectiveY},
			Gases:      gPoints,
			Protesters: pPoints,
			Cops:       cPoints,
			Heatmap:    generateHeatmap(),
		}

		data, err := json.Marshal(payload)
		if err != nil {
			break
		}
		if err := websocket.Message.Send(ws, string(data)); err != nil {
			break
		}
	}
}

func main() {
	http.Handle("/", http.FileServer(http.Dir(".")))
	http.Handle("/ws", websocket.Handler(wsHandler))

	port := 8080
	fmt.Printf("Simulateur manif / encerclement CRS sur http://localhost:%d\n", port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
}
