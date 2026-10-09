#!/usr/bin/env bash
# run_benchmarks.sh — rejoue TOUTE la chaîne de mesure de l'audit en une commande.
# Résultats horodatés écrits dans bench-results/<timestamp>/.
#
# Usage : ./run_benchmarks.sh            (profil rapide, défaut)
#         ./run_benchmarks.sh full       (profil complet : + Hyperfine, + SQL)
#
# Prérequis optionnels : benchstat, hyperfine (sinon les étapes concernées sont sautées).
set -uo pipefail
cd "$(dirname "$0")"

MODE="${1:-quick}"
STAMP="$(date +%Y%m%d-%H%M%S)"
OUT="bench-results/$STAMP"
mkdir -p "$OUT"
echo "== Résultats dans $OUT (mode: $MODE) =="

have() { command -v "$1" >/dev/null 2>&1; }

# 0. Environnement
{
  echo "date: $(date)"
  go version
  echo "GOMAXPROCS: $(go env GOMAXPROCS)"
} > "$OUT/env.txt"

# 1. Build
echo "[1/6] build…"
go build -o "$OUT/crs.exe" . 2>&1 | tee "$OUT/build.log"

# 2. Micro-benchmark du tick (benchstat si dispo)
echo "[2/6] BenchmarkUpdate (-benchmem, -count=10)…"
go test -bench='^BenchmarkUpdate$' -benchmem -count=10 -run='^$' 2>&1 | tee "$OUT/update.txt"

# 3. Loi d'échelle O(n²)->O(n)
echo "[3/6] loi d'échelle…"
go test -bench='^BenchmarkUpdateScale$' -count=3 -run='^$' 2>&1 | tee "$OUT/scale.txt"

# 4. Modes de parallélisation (échec constructif : pool vs per-item vs seq)
echo "[4/6] modes de parallélisation…"
go test -bench='^BenchmarkScan(Pool|PerItem|Seq)$' -count=10 -run='^$' 2>&1 | tee "$OUT/modes.txt"
if have benchstat; then
  grep ScanPool "$OUT/modes.txt" | sed 's/ScanPool/X/' > "$OUT/_pool.txt"
  grep ScanPerItem "$OUT/modes.txt" | sed 's/ScanPerItem/X/' > "$OUT/_peritem.txt"
  echo "-- benchstat pool -> per-item --" | tee "$OUT/modes-benchstat.txt"
  benchstat "$OUT/_pool.txt" "$OUT/_peritem.txt" 2>&1 | tee -a "$OUT/modes-benchstat.txt"
  rm -f "$OUT/_pool.txt" "$OUT/_peritem.txt"
fi

# 5. Profil CPU (pprof -top)
echo "[5/6] profil CPU…"
go test -bench='^BenchmarkUpdate$' -run='^$' -cpuprofile="$OUT/cpu.out" -benchtime=3s >/dev/null 2>&1
go tool pprof -top -nodecount=12 "$OUT/cpu.out" 2>/dev/null | tee "$OUT/pprof-top.txt" | head -20

if [ "$MODE" = "full" ]; then
  # 6a. Hyperfine : binaire naïf (HEAD) vs optimisé (si git + hyperfine dispo)
  echo "[6/6] Hyperfine naïf vs optimisé…"
  if have hyperfine && git rev-parse --git-dir >/dev/null 2>&1; then
    cp "$OUT/crs.exe" "$OUT/opti.exe"
    git stash push -q -- main.go 2>/dev/null && STASHED=1 || STASHED=0
    go build -o "$OUT/naif.exe" . 2>/dev/null
    [ "$STASHED" = 1 ] && git stash pop -q 2>/dev/null
    hyperfine --warmup 1 --runs 5 \
      "$OUT/naif.exe -bench -ticks=40" "$OUT/opti.exe -bench -ticks=40" \
      --export-markdown "$OUT/hyperfine.md" 2>&1 | tee "$OUT/hyperfine.txt" || true
  else
    echo "hyperfine ou git absent — étape sautée" | tee "$OUT/hyperfine.txt"
  fi
  # 6b. Démo SQL (batch insert + index B-Tree)
  echo "[6/6] démo SQL…"
  "$OUT/crs.exe" -sqldemo -db "$OUT/runs.db" -runs 40 -ticks 1000 -protesters 150 -cops 20 \
    2>&1 | tee "$OUT/sql.txt"
  rm -f "$OUT/runs.db" "$OUT/naif.exe" "$OUT/opti.exe"
fi

echo "== Terminé. Synthèse dans $OUT =="
