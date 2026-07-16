// Package iam2 mounts the clean-room Hanzo IAM v2 (zip-native, beego-free) into the
// unified hanzoai/cloud binary as the identity plane, selected OVER the legacy beego
// Casdoor embed (clients/iam) by CLOUD_IAM_IMPL=iam2. It is the either/or twin of
// clients/iam: both own the SAME absolute prefixes (/v1/iam/*, /login/oauth/*), so
// they CANNOT co-mount — EXACTLY ONE is wired per boot (apps.Wire → identitySpec).
// Default (CLOUD_IAM_IMPL unset) keeps the beego embed, so this package is completely
// inert until the flag flips — safe to land in a churning main.
//
// WHY iam2 is NOT staged like iam. "iam" is staged because iamserver.InitEmbed boots
// the WHOLE Beego runtime and mutates process-global Beego state (web.BeeApp / the
// shared AppConfig), which corrupts the sibling `ai` casdoor fork under mount-all.
// iam2 carries NO such process-global: it opens its OWN orm.DB and registers
// zip-native routes, so the shared-global hazard that pins iam to staged does not
// exist here. The deliberate CLOUD_IAM_IMPL=iam2 opt-in is itself the gate.
//
// FAIL-CLOSED, NOT FAIL-LOUD. A store-open or mount failure degrades THIS subsystem
// to a 503 on the identity prefixes (mountFailClosed) while every co-resident
// subsystem (KMS, o11y, ...) stays up — the fold's blast-radius isolation. It never
// panics the shared binary (iam2server.Mount's only panic path — a registered
// enterprise feature failing to mount — is recovered in safeMount).
package iam2

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	iam2server "github.com/hanzoai/iam2/server"
	"github.com/hanzoai/orm"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"

	// Enterprise IAM features (Apache-2.0), blank-imported for the init() that
	// self-registers each into iam2's feature registry (the database/sql driver
	// pattern). iam2server.Mount already calls feature.MountAll, so a registered
	// feature auto-mounts whenever CLOUD_IAM_IMPL=iam2 — no feature.Register call
	// belongs here (that would double-register → double-mount → route collision).
	// LDAP is deliberately absent: it links goldap (GPL-2.0), so it is opt-in only
	// under -tags iam2_ldap (see ldap_enabled.go), keeping the default binary
	// copyleft-free.
	_ "github.com/hanzoiam/saml" // SAML IdP + SP SSO — self-registers into feature
	_ "github.com/hanzoiam/scim" // SCIM 2.0 provisioning — self-registers into feature
)

// iam2Prefixes are the canonical absolute prefixes the iam2 identity surface owns,
// used ONLY for the fail-closed 503 — on success iam2server.Mount registers the real
// routes itself. Mirrors the identity subset of clients/iam.iamPrefixes; iam2's bare
// /healthz is deliberately excluded — it is a shared-liveness path, not an auth
// surface, so 503-ing it would mask the binary's own health rather than an identity
// outage.
var iam2Prefixes = []string{
	"/v1/iam",      // OIDC/OAuth2 + entity CRUD + the Casdoor verb-alias compat layer
	"/login/oauth", // browser authorize surface (the /v1/iam/oauth/authorize 302 target)
}

// Mount opens iam2's embedded store, seeds config from the same init_data.json the
// beego iam uses (non-fatal), and registers the whole iam2 surface onto cloud's shared
// zip.App. It matches the cloud.Typed contract (func(*zip.App, cloud.Deps) error) so
// apps.Wire references it via cloud.Typed exactly like clients/iam.Mount — cloud hands
// subsystems a cloud.Deps, not an orm.DB, so iam2 opens its own store here.
func Mount(app *zip.App, deps cloud.Deps) error {
	log := deps.Logger.New("subsystem", "iam2")

	dbPath, initDataPath := paths(deps)

	// SQLite does not create parent dirs; ensure it exists (0700 — identity data).
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		log.Error("iam2 data dir create failed — serving fail-closed 503 (cloud stays up)", "err", err, "dir", filepath.Dir(dbPath))
		mountFailClosed(app)
		return nil
	}

	db, err := iam2server.OpenSQLite(dbPath)
	if err != nil {
		log.Error("iam2 store open failed — serving fail-closed 503 (cloud stays up)", "err", err, "path", dbPath)
		mountFailClosed(app)
		return nil
	}

	// Seed is NON-FATAL: new-only + idempotent config bootstrap (orgs/apps/providers/
	// certs) from the SAME init_data.json the beego iam seeds from. A missing or partial
	// file leaves iam2 mounted-but-unseeded (honest degrade) rather than blocking the
	// identity plane; an already-seeded store simply skips everything.
	if sum, serr := iam2server.Seed(context.Background(), db, initDataPath); serr != nil {
		log.Warn("iam2 seed skipped (non-fatal)", "err", serr, "init_data", initDataPath)
	} else {
		log.Info("iam2 seed applied", "created", sum.Created, "skipped", sum.Skipped, "init_data", initDataPath)
	}

	// iam2server.Mount registers the whole surface at the canonical absolute paths. It
	// PANICS only if a registered enterprise feature fails to mount (none today); recover
	// so a future boot-misconfig degrades to fail-closed 503 instead of crashing the
	// shared binary — the same blast-radius isolation clients/iam gives.
	if err := safeMount(app, db); err != nil {
		log.Error("iam2 mount failed — serving fail-closed 503 (cloud stays up)", "err", err)
		mountFailClosed(app)
		return nil
	}

	log.Info("iam2 embedded in-process (zip-native, beego-free)", "db", dbPath, "prefixes", iam2Prefixes)
	return nil
}

// paths derives iam2's SQLite file and init_data.json path from cloud.Deps, mirroring
// how clients/iam sources them. The store lives under {DataDir}/iam2 — its OWN dir,
// parallel to the beego embed's {DataDir}/iam, so the two impls (either/or, never
// co-resident) keep independent stores and iam2 never opens beego's casdoor-schema
// file. DataDir empty falls back to CWD exactly as the beego default does.
// init_data.json is CWD-relative "init_data.json" — the beego iam's conf default —
// honoring the same `initDataFile` env override, so a deployment points BOTH impls at
// one file (DRY, one source of seed truth).
func paths(deps cloud.Deps) (dbPath, initDataPath string) {
	root := deps.DataDir
	if root == "" {
		root = "."
	}
	dbPath = filepath.Join(root, "iam2", "iam.db")

	initDataPath = os.Getenv("initDataFile")
	if initDataPath == "" {
		initDataPath = "init_data.json"
	}
	return dbPath, initDataPath
}

// safeMount runs iam2server.Mount under a recover so its only panic path — a registered
// enterprise feature failing to mount — becomes an error the caller fail-closes on,
// never a crash of the shared cloud binary. With zero features registered today it
// always returns nil.
func safeMount(app *zip.App, db orm.DB) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("iam2 mount panicked: %v", r)
		}
	}()
	iam2server.Mount(app, db)
	return nil
}

// mountFailClosed serves an honest JSON 503 on every identity prefix when iam2 cannot
// boot, so /v1/iam/* answers "iam unavailable" instead of falling through to the
// console SPA catch-all (which would 200 an auth path). cloud and every other
// subsystem stay up — the fold's blast-radius isolation. Byte-identical error contract
// to clients/iam.mountFailClosed so clients see one shape regardless of impl.
func mountFailClosed(app *zip.App) {
	failed := zip.AdaptNetHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"iam unavailable","code":503}`))
	}))
	for _, p := range iam2Prefixes {
		app.All(p+"/*", failed)
	}
}
