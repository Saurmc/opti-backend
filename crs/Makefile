# Makefile — raccourcis de mesure pour l'audit de performance.
# (le barème accepte Makefile OU script ; voir aussi run_benchmarks.sh)

.PHONY: all build run bench scale modes profile race hyperfine sqldemo benchmarks clean

all: build

build:          ## compile le binaire
	go build -o crs.exe .

run: build      ## lance le serveur web (http://localhost:8080)
	./crs.exe

bench:          ## micro-benchmark du tick (ns/op, B/op, allocs/op)
	go test -bench='^BenchmarkUpdate$$' -benchmem -count=10 -run='^$$'

scale:          ## loi d'échelle O(n^2)->O(n)
	go test -bench='^BenchmarkUpdateScale$$' -count=3 -run='^$$'

modes:          ## échec constructif : pool vs per-item vs seq
	go test -bench='^BenchmarkScan(Pool|PerItem|Seq)$$' -count=10 -run='^$$'

profile:        ## profil CPU + top des fonctions chaudes
	go test -bench='^BenchmarkUpdate$$' -run='^$$' -cpuprofile=cpu.out -benchtime=3s
	go tool pprof -top -nodecount=12 cpu.out

race:           ## détecteur de courses (nécessite CGO + gcc)
	CGO_ENABLED=1 go test -race -bench='^BenchmarkUpdate$$' -benchtime=5x -run='^$$'

hyperfine: build ## débit réel du binaire (40 ticks)
	hyperfine --warmup 1 --runs 5 './crs.exe -bench -ticks=40'

sqldemo: build  ## démo persistance SQL (batch insert + index B-Tree)
	./crs.exe -sqldemo -db runs.db -runs 40 -ticks 1000 -protesters 150 -cops 20

benchmarks:     ## rejoue TOUTE la chaîne de mesure (une commande)
	bash run_benchmarks.sh full

clean:          ## nettoie les artefacts
	rm -f crs.exe cpu.out mem.out runs.db
	rm -rf bench-results
