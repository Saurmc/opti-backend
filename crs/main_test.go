package main

import "testing"

// BenchmarkUpdate mesure le coût d'un tick de simulation (updateSimulation).
// Lancer : go test -bench=Update -benchmem
//
//	go test -bench=Update -cpuprofile=cpu.out   (puis go tool pprof)
//
// Comparer avant/après avec benchstat sur plusieurs runs (-count=6).
func BenchmarkUpdate(b *testing.B) {
	initSimulation(defaultConfig())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		updateSimulation()
	}
}

// BenchmarkUpdateScale permet de vérifier la loi d'échelle O(n²) de la foule
// en faisant varier le nombre de manifestants.
func BenchmarkUpdateScale(b *testing.B) {
	for _, n := range []int{1000, 2000, 4000, 6000} {
		b.Run("prot="+itoa(n), func(b *testing.B) {
			c := defaultConfig()
			c.Protesters = n
			initSimulation(c)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				updateSimulation()
			}
		})
	}
}

// benchScan isole le coût de la PHASE PARALLÈLE seule (dispatchScan) sur un état
// FIGÉ : grille construite une fois, puis on répète le dispatch des scans (lecture
// seule, écrit scratch). Pas d'évolution de la sim => faible variance, on mesure
// uniquement le coût de la stratégie de parallélisation.
func benchScan(b *testing.B, mode string) {
	prev := parallelMode
	parallelMode = mode
	defer func() { parallelMode = prev }()
	initSimulation(defaultConfig())
	buildGrid()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dispatchScan()
	}
}

// Comparaison pour l'audit (échec constructif) :
//
//	go test -bench='Scan(Pool|PerItem|Seq)' -count=10
//
// BenchmarkScanPool    = worker pool borné (retenu).
// BenchmarkScanPerItem = une goroutine par manifestant (échec constructif attendu).
// BenchmarkScanSeq     = référence mono-cœur.
func BenchmarkScanPool(b *testing.B)    { benchScan(b, "pool") }
func BenchmarkScanPerItem(b *testing.B) { benchScan(b, "per-item") }
func BenchmarkScanSeq(b *testing.B)     { benchScan(b, "seq") }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [12]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
