// Package iam folds Hanzo IAM into the unified hanzoai/cloud binary as an
// in-process subsystem (HIP-0106) — the LAST binary-consolidation piece:
// "one Go binary (hanzoai/cloud) embeds IAM + KMS + o11y".
//
// CLEAN IAM (v2), NOT CASDOOR. This subsystem embeds github.com/hanzoai/iam —
// the clean-room identity rewrite on the native Hanzo stack (zip + hanzoai/orm +
// hanzoai/sqlite). The retired Casdoor/Beego fork (github.com/hanzoai/iam-v1) is
// GONE from cloud's graph: there is no beego process-global to corrupt, no
// InitEmbed, no session-manager hook, no shared-AppConfig co-residence hazard with
// the sibling `ai` casdoor fork. iamserver.Mount registers the whole IAM v2 surface
// (OIDC discovery/JWKS, oauth authorize/token/userinfo/introspect/revoke,
// get-app-login, signin, the v2 entity CRUD, and the Casdoor verb-alias compat
// layer) ZIP-NATIVELY onto cloud's shared app — no net/http adaptor round-trip. The
// specific self-service routes layered in front (account, agentskills) still win by
// Fiber's in-order match, so the fold is collision-free.
//
// The store is embedded SQLite under {DataDir}/iam (server.OpenSQLite, WAL) — this
// embed owns its OWN orm.DB outright, so the old Casdoor-fork "ai bootstrap unable to
// open database file (14)" crash is gone. Config (orgs/apps/providers/signing certs) is
// seeded from the same init_data.json the deployment already provides (server.Seed,
// new-only + idempotent), so hanzo.id's OAuth/OIDC semantics are preserved.
//
// IN-PROCESS STORE ACCESS. DB() exposes the opened orm.DB to sibling subsystems that
// REFLECT the IAM-owned Project resource in-process (clients/platform, clients/deploy)
// via github.com/hanzoai/iam/pkg/store — no HTTP hop to /v1/iam. It is nil until
// Mount runs (the same lifecycle the retired iam-v1 object-store global ormer had),
// so those callers guard a nil DB and degrade to a clean 503 until IAM is mounted.
//
// FAIL-CLOSED, NOT FAIL-LOUD. A broken/misconfigured IAM does NOT crash the
// consolidated binary: an open/seed/mount failure degrades THIS subsystem to a 503
// fail-closed on every IAM prefix (mountFailClosed) while every co-resident
// subsystem (KMS, o11y, …) stays up — the blast-radius isolation the whole
// consolidation exists for, mirroring the KMS "no master key → health-only" pattern.
//
// Mounted in-process (the whole IAM v2 surface, registered at its canonical paths):
//
//	/v1/iam/*      OIDC/OAuth2 (/v1/iam/oauth/{authorize,token,userinfo,introspect,
//	               revoke,...}) + OIDC discovery (/v1/iam/.well-known/*) + signin +
//	               get-app-login + the v2 entity CRUD + the Casdoor verb-alias compat
//	/login/oauth/* browser authorize surface (the /v1/iam/oauth/authorize 302 target)
//
// STAGING (security-critical): activation is the standard enable-list gate — the
// operator adds "iam" to the cloud deployment's --enable only AFTER the v2 config
// (init_data + KMS signing keys) is present and the fold is verified
// (login/authorize/token/jwks + the operator SSO chain). Until then hanzo.id is
// served by the standalone iam pod via ingress. If a broken config slips through,
// the subsystem serves 503 fail-closed rather than crashing cloud.
package iam

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	iamserver "github.com/hanzoai/iam/server"
	"github.com/hanzoai/orm"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
)

// iamPrefixes are the canonical absolute prefixes the IAM identity surface owns,
// used ONLY for the fail-closed 503 — on success iamserver.Mount registers the real
// routes itself. The bare /healthz is deliberately excluded — it is a shared-liveness
// path, not an auth surface, so 503-ing it would mask the binary's own health rather
// than an identity outage.
var iamPrefixes = []string{
	"/v1/iam",      // OIDC/OAuth2 + entity CRUD + the Casdoor verb-alias compat layer
	"/login/oauth", // browser authorize surface (the /v1/iam/oauth/authorize 302 target)
}

// embeddedDB is the orm.DB Mount opens for the embedded IAM store, published to
// sibling subsystems via DB(). nil until a successful Mount — the same lifecycle the
// retired iam-v1 object store's package-global ormer had, so in-process readers guard
// a nil DB the way they used to guard a nil ormer.
var embeddedDB orm.DB

// DB returns the embedded IAM store's orm.DB for in-process readers (clients/platform,
// clients/deploy) that reflect the IAM-owned Project resource via
// github.com/hanzoai/iam/pkg/store. It is nil until Mount has run (IAM not enabled, or
// a boot failure that fail-closed the subsystem); callers MUST nil-guard and degrade to
// 503 rather than dereference it.
func DB() orm.DB { return embeddedDB }

// Mount opens IAM's embedded store, seeds config from the same init_data.json the
// deployment provides (non-fatal), and registers the whole IAM v2 surface onto cloud's
// shared zip.App. Called once by cloud.MountAll when "iam" is enabled.
func Mount(app *zip.App, deps cloud.Deps) error {
	log := deps.Logger.New("subsystem", "iam")

	dbPath, initDataPath := paths(deps)

	// SQLite does not create parent dirs; ensure it exists (0700 — identity data).
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		log.Error("iam data dir create failed — serving fail-closed 503 (cloud stays up)", "err", err, "dir", filepath.Dir(dbPath))
		mountFailClosed(app)
		return nil
	}

	db, err := iamserver.OpenSQLite(dbPath)
	if err != nil {
		log.Error("iam store open failed — serving fail-closed 503 (cloud stays up; standalone iam pod unaffected)", "err", err, "path", dbPath)
		mountFailClosed(app)
		return nil
	}
	// Publish the opened store for in-process readers (DB()) — set only after a clean
	// open so DB() is nil whenever the subsystem is fail-closed.
	embeddedDB = db

	// Seed is NON-FATAL: new-only + idempotent config bootstrap (orgs/apps/providers/
	// certs) from the SAME init_data.json the standalone iam seeds from. A missing or
	// partial file leaves iam mounted-but-unseeded (honest degrade) rather than blocking
	// the identity plane; an already-seeded store simply skips everything.
	if sum, serr := iamserver.Seed(context.Background(), db, initDataPath); serr != nil {
		log.Warn("iam seed skipped (non-fatal)", "err", serr, "init_data", initDataPath)
	} else if sum != nil {
		log.Info("iam seed applied", "created", sum.Created, "skipped", sum.Skipped, "init_data", initDataPath)
	}

	// iamserver.Mount registers the whole surface at the canonical absolute paths. It
	// PANICS only if a registered enterprise feature fails to mount (none today);
	// recover so a future boot-misconfig degrades to fail-closed 503 instead of crashing
	// the shared binary — the same blast-radius isolation the whole fold gives.
	if err := safeMount(app, db); err != nil {
		log.Error("iam mount failed — serving fail-closed 503 (cloud stays up)", "err", err)
		embeddedDB = nil // fail-closed: no half-mounted store leaks to in-process readers
		mountFailClosed(app)
		return nil
	}

	log.Info("iam embedded in-process (clean iam-v2, zip-native + hanzoai/orm — Casdoor iam-v1 retired)", "db", dbPath, "prefixes", iamPrefixes)
	return nil
}

// paths derives IAM's SQLite file and init_data.json path from cloud.Deps. The store
// lives under {DataDir}/iam — its OWN dir. DataDir empty falls back to CWD, exactly as
// the standalone iam default does. init_data.json is CWD-relative "init_data.json" (the
// standalone iam conf default), honoring the same `initDataFile` env override so a
// deployment points BOTH the embedded and standalone iam at one file (DRY, one source
// of seed truth).
func paths(deps cloud.Deps) (dbPath, initDataPath string) {
	root := deps.DataDir
	if root == "" {
		root = "."
	}
	// iam2.db is the v2 store. iam.db is a different database entirely, and
	// opening it serves the wrong identities without failing.
	dbPath = filepath.Join(root, "iam", "iam2.db")

	initDataPath = os.Getenv("initDataFile")
	if initDataPath == "" {
		initDataPath = "init_data.json"
	}
	return dbPath, initDataPath
}

// safeMount runs iamserver.Mount under a recover so its only panic path — a registered
// enterprise feature failing to mount — becomes an error the caller fail-closes on,
// never a crash of the shared cloud binary. With zero features registered today it
// always returns nil.
func safeMount(app *zip.App, db orm.DB) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("iam mount panicked: %v", r)
		}
	}()
	iamserver.Mount(app, db)
	return nil
}

// mountFailClosed serves an honest JSON 503 on every identity prefix when IAM cannot
// boot, so /v1/iam/* answers "iam unavailable" instead of falling through to the
// console SPA catch-all (which would 200 an auth path). cloud and every other subsystem
// stay up — the fold's blast-radius isolation. During staged rollout hanzo.id is still
// served by the standalone iam pod via ingress, so clients never see this path until
// cutover.
func mountFailClosed(app *zip.App) {
	failed := zip.AdaptNetHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"iam unavailable","code":503}`))
	}))
	for _, p := range iamPrefixes {
		app.All(p+"/*", failed)
	}
}
