package main

import "testing"

// BenchmarkUpdate mesure le coût d'un tick de simulation (updateSimulation).
// Lancer : go test -bench=Update -benchmem
//          go test -bench=Update -cpuprofile=cpu.out   (puis go tool pprof)
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
