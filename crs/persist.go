package main

// Étage de persistance (axe I/O & persistance, module J4_PM de l'audit).
//
// On enregistre les statistiques par tick de plusieurs simulations dans SQLite,
// puis on démontre trois optimisations du cours :
//   1. Batch insert : une transaction par run (équivalent SQLite de pgx.CopyFrom)
//      vs insert ligne-par-ligne en autocommit.
//   2. Index B-Tree composite (run_id, tick_index) : une requête de plage passe
//      d'un scan séquentiel O(n) à un SEARCH USING INDEX O(log n).
//   3. Preuve par EXPLAIN QUERY PLAN (équivalent SQLite d'EXPLAIN ANALYZE :
//      montre SCAN vs SEARCH) + chronométrage avant/après.

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// enregistreTicks exécute nRuns simulations de ticks ticks chacune et insère les
// stats par tick en batch (une transaction par run). Retourne (nbLignes, durée).
func enregistreTicks(db *sql.DB, nRuns, ticks, nProt, nCops int) (int, time.Duration) {
	start := time.Now()
	rows := 0
	for r := 0; r < nRuns; r++ {
		c := defaultConfig()
		c.Seed = MasterSeed + int64(r)
		c.Protesters = nProt
		c.Cops = nCops
		initSimulation(c)

		tx, err := db.Begin()
		if err != nil {
			panic(err)
		}
		stmt, err := tx.Prepare(`INSERT INTO tick_stats VALUES(?,?,?,?,?,?,?,?)`)
		if err != nil {
			panic(err)
		}
		for t := 0; t < ticks; t++ {
			updateSimulation()
			active, _, _, _, _, _, _ := getCounts()
			if _, err := stmt.Exec(r, t, active, passed, arrested, killed, pavesThrown, copsEliminated); err != nil {
				panic(err)
			}
			rows++
		}
		stmt.Close()
		if err := tx.Commit(); err != nil {
			panic(err)
		}
	}
	return rows, time.Since(start)
}

// compareInsertion insère k lignes factices en autocommit (ligne-par-ligne) puis
// en une transaction (batch), dans une table temporaire, et renvoie les deux durées.
func compareInsertion(db *sql.DB, k int) (perRow, batch time.Duration) {
	db.Exec(`DROP TABLE IF EXISTS tmp_insert`)
	db.Exec(`CREATE TABLE tmp_insert(a INTEGER, b INTEGER)`)

	// Ligne-par-ligne : chaque INSERT est sa propre transaction (fsync à chaque fois).
	t0 := time.Now()
	for i := 0; i < k; i++ {
		db.Exec(`INSERT INTO tmp_insert VALUES(?,?)`, i, i*2)
	}
	perRow = time.Since(t0)

	db.Exec(`DELETE FROM tmp_insert`)

	// Batch : une seule transaction + statement préparé.
	t1 := time.Now()
	tx, _ := db.Begin()
	stmt, _ := tx.Prepare(`INSERT INTO tmp_insert VALUES(?,?)`)
	for i := 0; i < k; i++ {
		stmt.Exec(i, i*2)
	}
	stmt.Close()
	tx.Commit()
	batch = time.Since(t1)

	db.Exec(`DROP TABLE tmp_insert`)
	return perRow, batch
}

// planRequete renvoie le plan d'exécution (EXPLAIN QUERY PLAN) concaténé.
func planRequete(db *sql.DB, query string, args ...any) string {
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		return "erreur: " + err.Error()
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			return "erreur scan: " + err.Error()
		}
		parts = append(parts, detail)
	}
	return strings.Join(parts, " | ")
}

// chronoRequete exécute la requête n fois et renvoie la durée moyenne.
func chronoRequete(db *sql.DB, n int, query string, args ...any) time.Duration {
	start := time.Now()
	for i := 0; i < n; i++ {
		var cnt, sum int
		row := db.QueryRow(query, args...)
		row.Scan(&cnt, &sum)
	}
	return time.Since(start) / time.Duration(n)
}

// runSQLDemo orchestre toute la démonstration SQL de l'audit.
func runSQLDemo(dbPath string, nRuns, ticks, nProt, nCops int) {
	os.Remove(dbPath)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Println("open:", err)
		return
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE tick_stats(
		run_id     INTEGER,
		tick_index INTEGER,
		active     INTEGER,
		passed     INTEGER,
		arrested   INTEGER,
		killed     INTEGER,
		paves      INTEGER,
		cops_gone  INTEGER
	)`); err != nil {
		fmt.Println("schema:", err)
		return
	}

	fmt.Printf("=== Étage SQL (SQLite) — %d runs x %d ticks (%d manifestants, %d CRS) ===\n",
		nRuns, ticks, nProt, nCops)

	// --- 1. Enregistrement batch des stats par tick ---
	rows, insDur := enregistreTicks(db, nRuns, ticks, nProt, nCops)
	fmt.Printf("\n[1] Enregistrement batch : %d lignes en %v (%.0f lignes/s)\n",
		rows, insDur.Round(time.Millisecond), float64(rows)/insDur.Seconds())

	// --- 2. Batch vs ligne-par-ligne (mise en évidence) ---
	k := 3000
	perRow, batch := compareInsertion(db, k)
	fmt.Printf("\n[2] Insertion de %d lignes :\n", k)
	fmt.Printf("    ligne-par-ligne (autocommit) : %v\n", perRow.Round(time.Millisecond))
	fmt.Printf("    batch (1 transaction)        : %v\n", batch.Round(time.Millisecond))
	if batch > 0 {
		fmt.Printf("    => batch %.0fx plus rapide\n", float64(perRow)/float64(batch))
	}

	// --- 3. Index B-Tree composite : requête de plage avant/après ---
	midRun := nRuns / 2
	tLo, tHi := ticks/4, 3*ticks/4
	query := `SELECT COUNT(*), COALESCE(SUM(arrested),0) FROM tick_stats WHERE run_id=? AND tick_index BETWEEN ? AND ?`
	const reps = 300

	planAvant := planRequete(db, query, midRun, tLo, tHi)
	tAvant := chronoRequete(db, reps, query, midRun, tLo, tHi)

	if _, err := db.Exec(`CREATE INDEX idx_run_tick ON tick_stats(run_id, tick_index)`); err != nil {
		fmt.Println("index:", err)
		return
	}
	db.Exec(`ANALYZE`)

	planApres := planRequete(db, query, midRun, tLo, tHi)
	tApres := chronoRequete(db, reps, query, midRun, tLo, tHi)

	fmt.Printf("\n[3] Requête de plage WHERE run_id=%d AND tick_index BETWEEN %d AND %d (moyenne sur %d) :\n",
		midRun, tLo, tHi, reps)
	fmt.Printf("    SANS index : %-9v  | plan: %s\n", tAvant.Round(time.Microsecond), planAvant)
	fmt.Printf("    AVEC index : %-9v  | plan: %s\n", tApres.Round(time.Microsecond), planApres)
	if tApres > 0 {
		fmt.Printf("    => %.1fx plus rapide (scan sequentiel O(n) -> SEARCH USING INDEX O(log n))\n",
			float64(tAvant)/float64(tApres))
	}

	fmt.Printf("\nBase écrite dans %s\n", dbPath)
}
