// Prototype: zero-downtime SQLite HA for the `cloud` unified binary on the
// HANZO-NATIVE stack — NO LiteFS, NO Litestream, NO Postgres.
//
//   driver:      github.com/hanzoai/sqlite      (registers database/sql "sqlite", modernc/nocgo)
//   replication: github.com/hanzoai/replicate   (WAL->LTX streaming to an object store)
//   election:    github.com/hanzoai/replicate/file.Leaser (filesystem-CAS single-primary lease;
//                the dev analog of replicate/s3.Leaser that runs against SeaweedFS/hanzoai/vfs in prod)
//
// It models the real deploy problem: two "pods" (primary + warm standby) whose
// ONLY shared channel is the object store (a file:// dir here, SeaweedFS/S3 in
// prod). It proves:
//   1. a write on the PRIMARY appears on the STANDBY (WAL->LTX->restore),
//   2. a tight-loop /v1/tracker-style read probe stays green across an ORDERLY
//      primary handoff (the "rolling deploy"): 0 failed reads,
//   3. ZERO data loss on handoff (the promoted pod holds every committed row),
//   4. PRAGMA integrity_check = ok on the promoted DB (no corruption).
//
// Discipline mirroring two pods: goroutine A only touches primDir + store;
// goroutine B only touches replDir + store; the probe only reads via the
// standby's served copy. Neither pod ever opens the other's local DB file.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/hanzoai/sqlite" // registers database/sql driver "sqlite"

	"github.com/hanzoai/replicate"
	"github.com/hanzoai/replicate/file"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// openApp opens an application handle on a tracker-shaped SQLite DB using the
// ONE Hanzo driver, with the same pragmas clients/tracker/store.go uses.
func openApp(path string) *sql.DB {
	db, err := sql.Open("sqlite", path)
	must(err)
	db.SetMaxOpenConns(1) // single-writer serialization, exactly like tracker
	for _, p := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL", // replicate tails the WAL
		"PRAGMA foreign_keys=ON",
	} {
		_, err := db.Exec(p)
		must(err)
	}
	return db
}

func initSchema(db *sql.DB) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS issues(
		id INTEGER PRIMARY KEY,
		org TEXT NOT NULL,
		key TEXT NOT NULL,
		title TEXT NOT NULL,
		created_at INTEGER NOT NULL)`)
	must(err)
}

func insertIssue(db *sql.DB, n int) error {
	_, err := db.Exec(`INSERT INTO issues(id, org, key, title, created_at) VALUES(?,?,?,?,?)`,
		n, "ENG", fmt.Sprintf("ENG-%d", n), fmt.Sprintf("issue %d", n), time.Now().UnixMilli())
	return err
}

// countIssues is the read the probe exercises (like GET /v1/tracker/... reads).
func countIssues(path string) (int, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var n int
	err = db.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&n)
	return n, err
}

func integrityOK(path string) (bool, string) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return false, err.Error()
	}
	defer db.Close()
	var r string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&r); err != nil {
		return false, err.Error()
	}
	return r == "ok", r
}

// attachReplica wires replicate onto a DB path with the file:// object store.
func attachReplica(dbPath, store string) *replicate.DB {
	rdb := replicate.NewDB(dbPath)
	rdb.MonitorInterval = 0 // manual, deterministic shipping via ship()
	rdb.Replica = replicate.NewReplicaWithClient(rdb, file.NewReplicaClient(store))
	return rdb
}

// ship pushes the primary's committed state to the object store: DB->shadow-WAL
// (db.Sync) then shadow-WAL->backend LTX (Replica.Sync). This is the on-stack
// equivalent of a continuous WAL shipper (the operator runs the replicate sidecar
// on an interval; here we do it after each write burst for determinism).
func ship(ctx context.Context, rdb *replicate.DB) error {
	if err := rdb.Sync(ctx); err != nil {
		return err
	}
	return rdb.Replica.Sync(ctx)
}

// restoreInto pulls the latest committed state from the object store into a
// fresh file, then atomically renames it to `served` so a concurrent reader
// never observes a half-written DB. This is the warm-standby catch-up.
func restoreInto(ctx context.Context, store, dir, served string, seq int) error {
	tmp := filepath.Join(dir, fmt.Sprintf("restore-%d.db", seq))
	_ = os.Remove(tmp)
	r := replicate.NewReplica(replicate.NewDB(tmp))
	r.Client = file.NewReplicaClient(store)
	opt := replicate.NewRestoreOptions()
	opt.OutputPath = tmp
	opt.IntegrityCheck = replicate.IntegrityCheckFull
	if err := r.Restore(ctx, opt); err != nil {
		return err
	}
	return os.Rename(tmp, served) // atomic swap of the served copy
}

func main() {
	root, err := os.MkdirTemp("", "litehz-*")
	must(err)
	defer os.RemoveAll(root)
	store := filepath.Join(root, "store")   // the object store (SeaweedFS/hanzoai/vfs in prod)
	primDir := filepath.Join(root, "primA") // pod A local NVMe (its own PVC)
	replDir := filepath.Join(root, "replB") // pod B local NVMe (its own PVC)
	for _, d := range []string{store, primDir, replDir} {
		must(os.MkdirAll(d, 0o755))
	}
	primPath := filepath.Join(primDir, "tracker.db")
	served := filepath.Join(replDir, "tracker.db") // the copy pod B serves reads from
	ctx := context.Background()

	// Single-primary election on the object store (filesystem CAS here; s3.Leaser
	// against SeaweedFS in prod). Only the lease holder may write.
	leaser := file.NewLeaser(store)
	leaser.Path = "cloud-tracker.lock"

	fmt.Println("== Hanzo-native SQLite HA prototype (sqlite + replicate) ==")

	// ---- PRIMARY A boots: acquire lease, seed, start WAL shipping ----
	leaseA, err := leaser.AcquireLease(ctx)
	must(err)
	fmt.Printf("[A] acquired primary lease gen=%d owner=%s\n", leaseA.Generation, leaseA.Owner)
	appA := openApp(primPath)
	initSchema(appA)
	must(insertIssue(appA, 1)) // seed so a WAL + first LTX exist
	rdbA := attachReplica(primPath, store)
	must(rdbA.Open())
	must(ship(ctx, rdbA)) // ship the seed to the store

	// ---- STANDBY B warms up BEFORE entering the read rotation ----
	// (k8s: a pod only joins Service endpoints after readiness — i.e. after it
	// has caught up. Poll restore until the seed lands, then require it present.)
	warm := false
	for i := 0; i < 40; i++ {
		if err := restoreInto(ctx, store, replDir, served, 0); err == nil {
			warm = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n, err := countIssues(served); !warm || err != nil || n < 1 {
		panic(fmt.Sprintf("standby not warm before probe: warm=%v n=%d err=%v", warm, n, err))
	}
	fmt.Println("[B] warm standby caught up (readiness OK) — entering read rotation")

	var (
		probes    atomic.Int64
		probeFail atomic.Int64
		lastSeen  atomic.Int64
		writtenA  atomic.Int64
		writtenB  atomic.Int64
		promoted  atomic.Bool
	)
	writtenA.Store(1)
	lastSeen.Store(1)

	var wg sync.WaitGroup
	runCtx, stop := context.WithCancel(ctx)

	// PROBE: the tight-loop /v1/tracker read against the standby-served copy.
	// The Service always has >=1 ready pod, so this never targets the draining
	// pod. Counts any failed or regressing read as a downtime hit.
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-t.C:
				probes.Add(1)
				n, err := countIssues(served)
				if err != nil {
					probeFail.Add(1)
					fmt.Printf("[PROBE] FAIL: %v\n", err)
					continue
				}
				if int64(n) < lastSeen.Load() {
					probeFail.Add(1) // a committed read going backwards = data-loss visible to clients
					fmt.Printf("[PROBE] REGRESS: saw %d < %d\n", n, lastSeen.Load())
					continue
				}
				lastSeen.Store(int64(n))
			}
		}
	}()

	// STANDBY restore loop: keep pulling committed state into the served copy
	// while A is primary. Stops once B is promoted (then B writes served directly).
	wg.Add(1)
	go func() {
		defer wg.Done()
		seq := 1
		t := time.NewTicker(120 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-t.C:
				if promoted.Load() {
					return
				}
				if err := restoreInto(runCtx, store, replDir, served, seq); err != nil {
					fmt.Printf("[B] restore skip: %v\n", err)
				}
				seq++
			}
		}
	}()

	// PRIMARY A write load: steady inserts (like live tracker traffic).
	wg.Add(1)
	go func() {
		defer wg.Done()
		n := 1
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-t.C:
				if promoted.Load() {
					return // A has drained; stop writing
				}
				n++
				if err := insertIssue(appA, n); err != nil {
					fmt.Printf("[A] write err: %v\n", err)
					n--
					continue
				}
				writtenA.Store(int64(n))
				if err := ship(runCtx, rdbA); err != nil && runCtx.Err() == nil {
					fmt.Printf("[A] ship err: %v\n", err)
				}
			}
		}
	}()

	// ---- let A serve for a while ----
	time.Sleep(3 * time.Second)

	// ================= THE ROLLING DEPLOY (orderly handoff) =================
	fmt.Println("== ROLL: draining primary A, promoting standby B ==")
	promoted.Store(true)       // signal A to quiesce writes, B restore-loop to stop
	time.Sleep(150 * time.Millisecond)
	// preStop on the draining pod: final WAL checkpoint + ship, then release lease.
	must(ship(ctx, rdbA))
	must(rdbA.Close(ctx))
	appA.Close()
	must(leaser.ReleaseLease(ctx, leaseA))
	aTotal := writtenA.Load()
	fmt.Printf("[A] drained. final committed rows on A = %d (lease released)\n", aTotal)

	// New pod acquires the lease and does a FINAL restore -> it now holds every
	// row A committed. Promotion: it opens the served DB for writing.
	leaseB, err := leaser.AcquireLease(ctx)
	must(err)
	must(restoreInto(ctx, store, replDir, served, 9000))
	fmt.Printf("[B] acquired primary lease gen=%d (handoff)\n", leaseB.Generation)
	appB := openApp(served)
	initSchema(appB)
	// verify no data loss at the moment of promotion
	var bAtPromote int
	must(appB.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&bAtPromote))
	fmt.Printf("[B] rows present at promotion = %d (expected %d from A)\n", bAtPromote, aTotal)
	if int64(bAtPromote) != aTotal {
		panic(fmt.Sprintf("DATA LOSS on handoff: B has %d, A committed %d", bAtPromote, aTotal))
	}

	// B keeps serving writes as the new primary; probe keeps reading `served`.
	wg.Add(1)
	go func() {
		defer wg.Done()
		n := int(aTotal)
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-t.C:
				n++
				if err := insertIssue(appB, n); err != nil {
					fmt.Printf("[B] write err: %v\n", err)
					n--
					continue
				}
				writtenB.Store(int64(n - int(aTotal)))
			}
		}
	}()

	time.Sleep(3 * time.Second)
	stop()
	wg.Wait()
	appB.Close()

	// ---------------------------- ASSERTIONS ----------------------------
	total := aTotal + writtenB.Load()
	var finalCount int
	db := openApp(served)
	must(db.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&finalCount))
	// contiguity: KEY numbers 1..total all present -> no gap, no dup
	var maxN, distinct int
	must(db.QueryRow(`SELECT COALESCE(MAX(id),0), COUNT(DISTINCT id) FROM issues`).Scan(&maxN, &distinct))
	db.Close()
	ok, res := integrityOK(served)

	fmt.Println("\n==================== PROOF ====================")
	fmt.Printf("rows written by A (pre-roll) : %d\n", aTotal)
	fmt.Printf("rows written by B (post-roll): %d\n", writtenB.Load())
	fmt.Printf("final row count on served DB : %d (expected %d)\n", finalCount, total)
	fmt.Printf("max id / distinct ids        : %d / %d (contiguous=%v)\n", maxN, distinct, maxN == distinct && maxN == finalCount)
	fmt.Printf("integrity_check              : %q (ok=%v)\n", res, ok)
	fmt.Printf("read probes total            : %d\n", probes.Load())
	fmt.Printf("read probe FAILURES          : %d\n", probeFail.Load())
	fmt.Println("==============================================")

	pass := probeFail.Load() == 0 &&
		int64(finalCount) == total &&
		maxN == finalCount && distinct == finalCount &&
		ok
	if !pass {
		fmt.Println("RESULT: FAIL")
		os.Exit(1)
	}
	fmt.Println("RESULT: PASS — zero read-downtime across an orderly primary handoff, zero data loss, no corruption.")
}
