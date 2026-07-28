// Package iam folds Hanzo IAM into the unified hanzoai/cloud binary as an
// in-process subsystem (HIP-0106) — the LAST binary-consolidation piece:
// "one Go binary (hanzoai/cloud) embeds IAM + KMS + o11y".
//
// CLEAN IAM (v2), NOT CASDOOR. This subsystem embeds github.com/hanzoai/iam —
// the clean-room identity rewrite on the native Hanzo stack (zip + hanzoai/orm +
// hanzoai/sqlite). The retired Casdoor/Beego fork (github.com/hanzoai/iam-v1) is
// GONE from cloud's graph: there is no beego process-global to corrupt, no
// InitEmbed, no session-manager hook, no shared-AppConfig co-residence hazard with
// the sibling `ai` casdoor fork. iamserver.Route registers the whole IAM v2 surface
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
	ormdb "github.com/hanzoai/orm/db"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/cek"
)

// Prefixes are the canonical absolute prefixes the IAM identity surface owns —
// the ONE list. It registers the real routes (safeMount), serves the fail-closed 503
// when IAM cannot boot, and is the MountSpec.Prefixes apps.Wire() hands MountAll, so
// IAM's middleware can only ever land on identity's own subtrees. Everything outside
// them belongs to cloud, so the console catch-all keeps serving the SPA.
//
// The bare /healthz is deliberately excluded — it is a shared-liveness path, not an
// auth surface, so 503-ing it would mask the binary's own health rather than an
// identity outage. It is also why iam2 must not be co-mingled: iam2 serves its OWN
// /healthz, which silently took over the shared binary's.
var Prefixes = []string{
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
// deployment provides (non-fatal), and registers the whole IAM v2 surface at the
// prefixes identity owns (Prefixes). Called once by cloud.MountAll when "iam" is
// enabled.
func Mount(app cloud.Router, deps cloud.Deps) error {
	// The identity store lives here, so the roster read is published here.
	exposeRoster()

	log := deps.Logger.New("subsystem", "iam")

	dbPath, initDataPath := paths(deps)

	// SQLite does not create parent dirs; ensure it exists (0700 — identity data).
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		log.Error("iam data dir create failed — serving fail-closed 503 (cloud stays up)", "err", err, "dir", filepath.Dir(dbPath))
		mountFailClosed(app)
		return nil
	}

	db, err := openStore(dbPath)
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

	// iamserver.Route registers the whole surface at the canonical absolute paths. It
	// PANICS only if a registered enterprise feature fails to mount (none today);
	// recover so a future boot-misconfig degrades to fail-closed 503 instead of crashing
	// the shared binary — the same blast-radius isolation the whole fold gives.
	if err := safeMount(app, db); err != nil {
		log.Error("iam mount failed — serving fail-closed 503 (cloud stays up)", "err", err)
		embeddedDB = nil // fail-closed: no half-mounted store leaks to in-process readers
		mountFailClosed(app)
		return nil
	}

	log.Info("iam embedded in-process (clean iam-v2, zip-native + hanzoai/orm — Casdoor iam-v1 retired)", "db", dbPath, "prefixes", Prefixes)
	return nil
}

// The store's place and name. The directory is the namespace, so the file does not
// repeat it; what the file says instead is which PRINCIPAL PARTITION it holds.
//
// This store is IAM's GLOBAL partition — the cross-org configuration every tenant is
// resolved against: orgs, applications, providers, signing certs. That is not a label
// chosen for flavour, it is the same word the key derivation uses: cek opens it under
// sqlitedrv.PrincipalGlobal, whose own doc calls it "the cross-org global/platform
// database (certs, providers, the admin org catalog)". Naming the file after its
// principal means the name and the key agree, and it leaves room for the partitions
// that do not exist yet — a per-org or per-user IAM store would derive under
// PrincipalOrg/PrincipalUser and be named for THAT, so the split is visible on disk
// instead of inferred.
//
// A version number never appears here. One that exists only to not be a lower one is
// scar tissue, and a version in a filename is a migration waiting to be mistaken for an
// identity.
const (
	storeDir  = "iam"
	storeFile = "global.db"
)

// openStore opens IAM's store through cek — the SAME encryption-at-rest gate every
// other cloud store opens through — and layers the ORM over that handle.
//
// It replaces iamserver.OpenSQLite, which builds its own pool from a plain path and
// has no key to give it: orm's SQLiteDBConfig carries no master key, so that path
// wrote the store with the literal `SQLite format 3` header — every identity, org
// membership, and credential hash readable from a lifted PV snapshot or an in-cluster
// volume read. That is precisely the exposure cek exists to remove, and cek's own doc
// claims "encrypted at rest is a property of the open path"; this store was the
// counterexample. The hashes are argon2id, so a lifted file was never a password
// disclosure — but the identity graph and every credential record were in the clear.
//
// No new crypto: cek mints the per-file DEK, wraps it, migrates any existing plaintext
// file in place and shreds the plaintext copy once the encrypted store is proven
// readable, exactly as it does for ~50 other stores. ONE envelope, one owner.
//
// ONE connection serves reads and writes, which is what AdaptSQLDB documents and what
// every per-org store already does (OrgDB pins MaxOpenConns(1)). It is also required
// rather than merely tidy: on a pure-Go build the codec envelope is single-writer, so
// a second pool over the same keyed file is not an option to begin with.
func openStore(path string) (orm.DB, error) {
	conn, err := cek.Open(cek.Global, path)
	if err != nil {
		return nil, fmt.Errorf("iam: open store: %w", err)
	}
	// The same serialized-writer + WAL posture openOrgDB applies. cek returns a keyed
	// handle, not a configured one, so the pragmas are the caller's to set — and
	// iamserver.OpenSQLite used to set them via its own config.
	conn.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := conn.Exec(pragma); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("iam: pragma %q: %w", pragma, err)
		}
	}
	sdb, err := ormdb.AdaptSQLDB(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("iam: adapt store: %w", err)
	}
	return orm.AdaptDB(sdb), nil
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
	dbPath = filepath.Join(root, storeDir, storeFile)

	initDataPath = os.Getenv("initDataFile")
	if initDataPath == "" {
		initDataPath = "init_data.json"
	}
	return dbPath, initDataPath
}

// safeMount registers the IAM v2 surface behind WILDCARDS at the prefixes it owns,
// under a recover so its only panic path — a registered enterprise feature failing to
// mount — becomes an error the caller fail-closes on, never a crash of the shared
// cloud binary. With zero features registered today it always returns nil.
//
// It uses iamserver.Handler (a standalone iam2 app adapted to net/http), NOT
// iamserver.Route. Route CO-MINGLES iam2's routes onto the host app at absolute
// paths, and iam2 is a whole server: it owns a root catch-all and its own /healthz.
// Co-mingled into cloud, those SHADOW the console — the shared binary answered
// `/healthz` with {"binary":"iam2"} and 401'd `/` and `/signin`, so a logged-out
// person could not reach the login page at all. Handler is the shape the library
// documents for exactly this host ("registered at the /v1/iam/* and root
// /.well-known/* wildcards"), and it confines iam2 to the prefixes below, so cloud's
// console catch-all keeps serving everything else.
func safeMount(app cloud.Router, db orm.DB) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("iam mount panicked: %v", r)
		}
	}()
	h := zip.AdaptNetHTTP(iamserver.Handler(db))
	for _, prefix := range Prefixes {
		app.All(prefix, h)
		app.All(prefix+"/*", h)
	}
	// OIDC discovery + JWKS live at the ROOT by spec (RFC 8414 / OIDC Discovery
	// 1.0): a relying party reads /.well-known/openid-configuration off the issuer
	// host, so this one root wildcard is part of the identity contract, not a
	// catch-all. Narrow by construction — it cannot shadow the console.
	app.All("/.well-known/*", h)
	return nil
}

// mountFailClosed serves an honest JSON 503 on every identity prefix when IAM cannot
// boot, so /v1/iam/* answers "iam unavailable" instead of falling through to the
// console SPA catch-all (which would 200 an auth path). cloud and every other subsystem
// stay up — the fold's blast-radius isolation. During staged rollout hanzo.id is still
// served by the standalone iam pod via ingress, so clients never see this path until
// cutover.
func mountFailClosed(app cloud.Router) {
	failed := zip.AdaptNetHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"iam unavailable","code":503}`))
	}))
	for _, p := range Prefixes {
		app.All(p+"/*", failed)
	}
}
