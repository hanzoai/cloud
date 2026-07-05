// ha-nodeproof drives the REAL internal/hareplica.Manager (the production code
// path, not a re-implementation) through an orderly primary handoff against a
// REAL SeaweedFS S3 backend, in an isolated scratch bucket. It proves the
// crown-jewel DATA-SAFETY properties of the app wiring:
//
//  1. election  — s3.Leaser CAS gives exactly one primary; the second Manager
//     correctly stays a standby.
//  2. shipping  — the primary's tracker.db WAL is streamed to S3.
//  3. catch-up  — a standby's RestoreAll pulls the primary's committed state.
//  4. handoff   — Drain does a final Sync + releases the lease; a FRESH pod
//     (the roll's replacement) then acquires the lease and, via
//     RestoreAll, holds EVERY row the drained primary committed —
//     ZERO data loss — with PRAGMA integrity_check = ok.
//  5. read probe — a tight-loop read of the served copy never fails / regresses
//     across the handoff.
//
// The k8s read-availability (zero 502) is a Service/StatefulSet property proven
// separately by the operator readiness-gating + the scratch manifests; this
// harness isolates and proves the data layer that Red must vet before any prod
// cutover.
//
// Run (SeaweedFS port-forwarded to :9900):
//
//	HA_S3_ENDPOINT=http://localhost:9900 HA_S3_ACCESS_KEY=... HA_S3_SECRET_KEY=... \
//	GOWORK=off CGO_ENABLED=1 go run ./hack/ha-nodeproof
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	_ "github.com/hanzoai/sqlite"

	"github.com/hanzoai/cloud/internal/hareplica"
	luxlog "github.com/luxfi/log"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func openTracker(dir string) *sql.DB {
	db, err := sql.Open("sqlite", filepath.Join(dir, "tracker.db"))
	must(err)
	db.SetMaxOpenConns(1)
	for _, p := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON"} {
		_, err := db.Exec(p)
		must(err)
	}
	return db
}

func initSchema(db *sql.DB) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS projects(
		id INTEGER PRIMARY KEY, org TEXT NOT NULL, key TEXT NOT NULL, created_at INTEGER NOT NULL)`)
	must(err)
}

func insert(db *sql.DB, n int) error {
	_, err := db.Exec(`INSERT INTO projects(id,org,key,created_at) VALUES(?,?,?,?)`,
		n, "ENG", fmt.Sprintf("ENG-%d", n), time.Now().UnixMilli())
	return err
}

// count opens a FRESH read-only handle (the served-copy read the k8s probe does)
// so it always observes the current on-disk state.
func count(dir string) (int, error) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "tracker.db")+"?mode=ro")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var n int
	err = db.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&n)
	return n, err
}

func integrityOK(dir string) (bool, string) {
	db, err := sql.Open("sqlite", filepath.Join(dir, "tracker.db"))
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

func baseCfg(dir, pod string) hareplica.Config {
	return hareplica.Config{
		Enabled:        true,
		DataDir:        dir,
		S3Endpoint:     env("HA_S3_ENDPOINT", "http://localhost:9900"),
		S3Bucket:       env("HA_S3_BUCKET", "cloud-ha-scratchproof"),
		S3Prefix:       "nodeproof-" + env("RUN_ID", fmt.Sprintf("%d", time.Now().Unix())),
		S3Region:       env("HA_S3_REGION", "us-east-1"),
		S3AccessKey:    os.Getenv("HA_S3_ACCESS_KEY"),
		S3SecretKey:    os.Getenv("HA_S3_SECRET_KEY"),
		ForcePathStyle: true,
		PodName:        pod,
		LeaseTTL:       15 * time.Second,
		RenewInterval:  5 * time.Second,
		ShipInterval:   400 * time.Millisecond,
	}
}

func ensureBucket(ctx context.Context, cfg hareplica.Config) {
	acfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.S3Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.S3AccessKey, cfg.S3SecretKey, "")))
	must(err)
	c := awss3.NewFromConfig(acfg, func(o *awss3.Options) {
		o.UsePathStyle = true
		o.BaseEndpoint = aws.String(cfg.S3Endpoint)
	})
	_, _ = c.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(cfg.S3Bucket)})
}

func main() {
	ctx := context.Background()
	log := luxlog.Noop()
	runID := fmt.Sprintf("%d", time.Now().Unix())
	_ = os.Setenv("RUN_ID", runID)

	root, err := os.MkdirTemp("", "ha-nodeproof-*")
	must(err)
	defer os.RemoveAll(root)
	dirA := filepath.Join(root, "podA")
	dirB := filepath.Join(root, "podB")
	dirC := filepath.Join(root, "podC")
	for _, d := range []string{dirA, dirB, dirC} {
		must(os.MkdirAll(d, 0o755))
	}

	cfgA := baseCfg(dirA, "cloud-ha-A")
	ensureBucket(ctx, cfgA)
	fmt.Printf("== ha-nodeproof: real hareplica.Manager over SeaweedFS bucket=%s prefix=%s ==\n", cfgA.S3Bucket, cfgA.S3Prefix)

	// ---- POD A boots: seed tracker.db, restore (empty), elect -> PRIMARY ----
	appA := openTracker(dirA)
	initSchema(appA)
	must(insert(appA, 1)) // seed
	mgrA := hareplica.New(cfgA, log)
	must(mgrA.RestoreAll(ctx)) // empty S3 -> no-op, keeps local seed
	must(mgrA.Start(ctx))
	if !mgrA.IsPrimary() {
		panic("POD A must win the lease and be primary")
	}
	fmt.Println("[A] elected PRIMARY, shipping WAL")

	// A serves writes for a bit (like live tracker traffic).
	for n := 2; n <= 30; n++ {
		must(insert(appA, n))
		time.Sleep(30 * time.Millisecond)
	}
	time.Sleep(1 * time.Second) // let the ship loop flush to S3

	// ---- POD B boots while A is primary: restore -> STANDBY, must be caught up ----
	mgrB := hareplica.New(baseCfg(dirB, "cloud-ha-B"), log)
	must(mgrB.RestoreAll(ctx))
	must(mgrB.Start(ctx))
	if mgrB.IsPrimary() {
		panic("POD B must NOT be primary while A holds the lease (single-writer)")
	}
	bRows, err := count(dirB)
	must(err)
	fmt.Printf("[B] STANDBY caught up: sees %d rows (A has written 30)\n", bRows)
	if bRows < 30 {
		panic(fmt.Sprintf("standby not caught up: B=%d < 30", bRows))
	}

	// read probe on the standby-served copy across the handoff.
	stop := make(chan struct{})
	probeFail := 0
	probeCount := 0
	lastSeen := 0
	go func() {
		t := time.NewTicker(20 * time.Millisecond)
		defer t.Stop()
		served := dirB
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				probeCount++
				n, err := count(served)
				if err != nil {
					probeFail++
					continue
				}
				if n < lastSeen {
					probeFail++ // a committed read going backwards = visible data loss
					continue
				}
				lastSeen = n
			}
		}
	}()

	// A writes a few MORE rows, then the ROLL: A drains (final ship + release).
	for n := 31; n <= 45; n++ {
		must(insert(appA, n))
		time.Sleep(30 * time.Millisecond)
	}
	var aFinal int
	must(appA.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&aFinal))
	fmt.Printf("== ROLL: A committed %d rows, draining (final Sync + release lease) ==\n", aFinal)
	appA.Close()
	must(mgrA.Drain(ctx)) // final ship + lease release + role-label drop

	// ---- POD C boots (the roll's replacement pod): restore -> gets A's FINAL
	// ship -> elect -> PRIMARY. Must hold EVERY row A committed (zero loss). ----
	mgrC := hareplica.New(baseCfg(dirC, "cloud-ha-C"), log)
	must(mgrC.RestoreAll(ctx))
	must(mgrC.Start(ctx))
	if !mgrC.IsPrimary() {
		panic("POD C must acquire the freed lease and become the new PRIMARY")
	}
	cRows, err := count(dirC)
	must(err)
	fmt.Printf("[C] new PRIMARY after handoff: holds %d rows (A committed %d)\n", cRows, aFinal)

	// C serves new writes as the promoted primary.
	appC := openTracker(dirC)
	for n := aFinal + 1; n <= aFinal+10; n++ {
		must(insert(appC, n))
		time.Sleep(20 * time.Millisecond)
	}
	var cFinal int
	must(appC.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&cFinal))
	appC.Close()

	time.Sleep(300 * time.Millisecond)
	close(stop)
	_ = mgrC.Drain(ctx)

	okB, resB := integrityOK(dirB)
	okC, resC := integrityOK(dirC)

	fmt.Println("\n==================== DATA-SAFETY PROOF ====================")
	fmt.Printf("A committed (pre-handoff)     : %d\n", aFinal)
	fmt.Printf("C holds at promotion          : %d  (zero-loss = %v)\n", cRows, cRows == aFinal)
	fmt.Printf("C after serving as new primary: %d\n", cFinal)
	fmt.Printf("standby(B) integrity_check    : %q ok=%v\n", resB, okB)
	fmt.Printf("primary(C) integrity_check    : %q ok=%v\n", resC, okC)
	fmt.Printf("read probes total             : %d\n", probeCount)
	fmt.Printf("read probe FAILURES/regress   : %d\n", probeFail)
	fmt.Println("==========================================================")

	pass := cRows == aFinal && cFinal == aFinal+10 && okB && okC && probeFail == 0
	if !pass {
		fmt.Println("RESULT: FAIL")
		os.Exit(1)
	}
	fmt.Println("RESULT: PASS — real Manager: single-primary election, WAL ship, standby catch-up,")
	fmt.Println("               orderly drain->handoff with ZERO data loss, integrity ok, 0 read failures.")
}
