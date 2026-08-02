// Package iam is Hanzo's identity provider: users, organizations, applications,
// and the OIDC/OAuth2 endpoints every Hanzo service authenticates against.
//
// It folds Hanzo IAM into the unified hanzoai/cloud binary as an in-process
// subsystem (HIP-0106) — the LAST binary-consolidation piece: "one Go binary
// (hanzoai/cloud) embeds IAM + KMS + o11y".
//
// CLEAN IAM (v2). This subsystem embeds github.com/hanzoai/iam —
// the clean-room identity rewrite on the native Hanzo stack (zip + hanzoai/orm +
// hanzoai/sqlite). The retired Beego fork (github.com/hanzoai/iam-v1) is
// GONE from cloud's graph: there is no beego process-global to corrupt, no
// InitEmbed, no session-manager hook, no shared-AppConfig co-residence hazard with
// the sibling `ai` legacy fork. The whole IAM v2 surface (OIDC discovery/JWKS, oauth
// authorize/token/userinfo/introspect/revoke, get-app-login, signin, the v2 entity
// CRUD, and the legacy verb-alias compat layer) is GRAFTED in process (safeMount):
// zip.Graft composes iamserver.NewApp(db) so cloud's router learns IAM's route
// patterns AND its op registry, while IAM's own router keeps IAM's behaviour.
//
// IT IS THEREFORE NOT OPAQUE ANY MORE, and that is the whole point of the change.
// It used to be: the surface was hung on five `app.All` wildcards through
// zip.AdaptNetHTTP, which takes an http.Handler and returns a closure — the App
// went in and a bare function came out, taking IAM's 94 typed ops with it. cloud
// published FIVE path keys and 35 placeholder operations where 78 real paths and 94
// typed operations were, so not one of them had a schema, an MCP tool, a CLI command
// or an SDK method. The refusal apps/iam/typed_wire_test.go used to gate was a
// property of that SEAM, never of IAM, and the seam is gone.
//
// The specific self-service routes layered in front (agentskills) still win, because
// zip matches the most specific pattern. The two addresses that were NOT specificity
// but SHADOWING — /v1/iam/keys and /v1/iam/onboard, where apps/account registered
// deprecated aliases at addresses IAM already owns and serves — are gone from
// apps/account: a graft refuses a duplicate address at compose time rather than
// letting registration order decide silently, and at api.hanzo.ai those two were
// already answered by IAM anyway (ingress routes /v1/iam/* there).
//
// The store is embedded SQLite under {DataDir}/iam (server.OpenSQLite, WAL) — this
// embed owns its OWN orm.DB outright, so the old fork's "ai bootstrap unable to
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
// Grafted in process (the whole IAM v2 surface, at its canonical paths — every
// pattern IAM's own router declares, and nothing else):
//
//	/v1/iam/…      OIDC/OAuth2 (/v1/iam/oauth/{authorize,token,userinfo,introspect,
//	               revoke,...}) + OIDC discovery (/v1/iam/.well-known/*) + signin +
//	               get-app-login + the v2 entity CRUD + the legacy verb-alias compat
//	/login/oauth/… browser authorize surface (the /v1/iam/oauth/authorize 302 target)
//	/.well-known/… OIDC discovery + JWKS at the issuer root (RFC 8414)
//
// STAGING (security-critical): activation is the standard enable-list gate — the
// operator adds "iam" to the cloud deployment's --enable only AFTER the v2 config
// (init_data + KMS signing keys) is present and the fold is verified
// (login/authorize/token/jwks + the operator SSO chain). Until then hanzo.id is
// served by the standalone iam pod via ingress. If a broken config slips through,
// the subsystem serves 503 fail-closed rather than crashing cloud.
package iam

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches a consumer — Go drops
// comments at compile time. The grafted surface carries its own prose from
// github.com/hanzoai/iam (each op's WithSummary/WithTags, which a graft copies
// verbatim); the one op THIS package owns is the internal-plane roster read in
// roster_rpc.go.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"

	"github.com/hanzoai/cloud/sqlpool"
	iamserver "github.com/hanzoai/iam/server"
	"github.com/hanzoai/orm"
	ormdb "github.com/hanzoai/orm/db"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cek"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/namespace"
)

// Prefixes are the canonical absolute prefixes the IAM identity surface owns — this
// subsystem's own list, from which patterns() derives every address it registers, both
// the real routes (safeMount) and the fail-closed 503 (mountFailClosed). Everything
// outside them belongs to cloud, so the console catch-all keeps serving the SPA.
//
// The host has a SECOND list and that is deliberate, the same split apps/commerce
// documents: manifest.Apps' iam row states what the light host's ROUTER may hand this
// binary (manifest/apps.go), while this states what the binary itself serves and
// fail-closes. Importing one into the other would re-fatten the host, which links
// manifest and zip and nothing else. They are not required to be equal, and today are
// not: the router does not name /.well-known, which manifest/router_test.go records in
// its `unreachable` ledger.
//
// The bare /healthz is deliberately excluded — it is a shared-liveness path, not an
// auth surface, so 503-ing it would mask the binary's own health rather than an
// identity outage. It is also why iam2 must not be co-mingled: iam2 serves its OWN
// /healthz, which silently took over the shared binary's.
var Prefixes = []string{
	"/v1/iam",      // OIDC/OAuth2 + entity CRUD + the legacy verb-alias compat layer
	"/login/oauth", // browser authorize surface (the /v1/iam/oauth/authorize 302 target)
}

// embeddedDB is the orm.DB Mount opens for the embedded IAM store, published to
// sibling subsystems via DB(). nil until a successful Mount — the same lifecycle the
// retired iam-v1 object store's package-global ormer had, so in-process readers guard
// a nil DB the way they used to guard a nil ormer.
var embeddedDB orm.DB

// embeddedConn is the connection embeddedDB was adapted from. orm's adapter takes a
// BORROWED handle and deliberately never closes one it did not open — "the caller
// closes what the caller opened" — and this package is that caller, so the handle is
// held here and closed by Shutdown. It has to be closed: the database is written back
// at close, so a store nothing ever closes is a store nothing ever persists.
var embeddedConn *sql.DB

// DB returns the embedded IAM store's orm.DB for in-process readers (clients/platform,
// clients/deploy) that reflect the IAM-owned Project resource via
// github.com/hanzoai/iam/pkg/store. It is nil until Mount has run (IAM not enabled, or
// a boot failure that fail-closed the subsystem); callers MUST nil-guard and degrade to
// 503 rather than dereference it.
func DB() orm.DB { return embeddedDB }

// Shutdown releases the embedded IAM store. Idempotent.
func Shutdown() error {
	conn := embeddedConn
	embeddedDB, embeddedConn = nil, nil
	if conn == nil {
		return nil
	}
	return conn.Close()
}

// Mount opens IAM's embedded store, seeds config from the same init_data.json the
// deployment provides (non-fatal), and registers the whole IAM v2 surface at the
// prefixes identity owns (Prefixes). Called once by cloud.MountAll when "iam" is
// enabled.
func Mount(app cloud.Router, deps cloud.Deps) error {
	// The identity store lives here, so the roster read is published here.
	exposeRoster()

	log := deps.Logger.New("subsystem", "iam")

	dir, initDataPath := paths(deps)

	db, conn, err := openStore(dir)
	if err != nil {
		log.Error("iam store open failed — serving fail-closed 503 (cloud stays up; standalone iam pod unaffected)", "err", err, "dir", dir)
		mountFailClosed(app)
		return nil
	}
	// Publish the opened store for in-process readers (DB()) — set only after a clean
	// open so DB() is nil whenever the subsystem is fail-closed.
	embeddedDB, embeddedConn = db, conn

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
		// Fail-closed: no half-mounted store leaks to in-process readers, and the
		// handle this package opened is released rather than left dangling.
		_ = Shutdown()
		mountFailClosed(app)
		return nil
	}

	log.Info("iam embedded in-process (clean iam-v2, zip-native + hanzoai/orm — iam-v1 retired)", "dir", dir, "store", storeSubsystem, "prefixes", Prefixes)
	return nil
}

// The store's name — which PRINCIPAL PARTITION it holds.
//
// This store is IAM's GLOBAL partition — the cross-org configuration every tenant is
// resolved against: orgs, applications, providers, signing certs. Naming it after its
// partition leaves room for the ones that do not exist yet: a per-org or per-user IAM
// store would be a different namespace and be named for THAT, so the split is visible
// on disk instead of inferred.
//
// A version number never appears here. One that exists only to not be a lower one is
// scar tissue, and a version in a name is a migration waiting to be mistaken for an
// identity.
const storeSubsystem = "global"

// openStore opens IAM's store through cek — the SAME opener every other cloud store
// opens through — and layers the ORM over that handle.
//
// It replaces iamserver.OpenSQLite, which builds its own pool from a plain path and
// has no key to give it: orm's SQLiteDBConfig carries no master key, so that path
// wrote the store with the literal `SQLite format 3` header — every identity, org
// membership, and credential hash readable from a lifted PV snapshot or an in-cluster
// volume read. That is precisely the exposure encryption-at-rest exists to remove; this
// store was the counterexample. The hashes are argon2id, so a lifted file was never a
// password disclosure — but the identity graph and every credential record were in the
// clear. Now the key is derived from the process master and this database's name, and a
// process with no master opens nothing.
//
// ONE connection serves reads and writes, which is what AdaptSQLDB documents and what
// every per-org store already does (OrgDB pins MaxOpenConns(1)). It is also required
// rather than merely tidy: on a pure-Go build the codec envelope is single-writer, so
// a second pool over the same keyed file is not an option to begin with.
//
// It returns the ORM handle AND the connection it was adapted from: orm's adapter
// borrows the handle and never closes one it did not open, so the connection is the
// caller's to close — and it must be closed, because that is when the database is
// written back.
func openStore(dir string) (orm.DB, *sql.DB, error) {
	conn, err := cek.Open(namespace.System(), storeSubsystem, dir)
	if err != nil {
		return nil, nil, fmt.Errorf("iam: open store: %w", err)
	}
	// The same single-writer posture openOrgDB applies: cek returns a keyed handle,
	// not a pooled one.
	sqlpool.Single(conn)
	sdb, err := ormdb.AdaptSQLDB(conn)
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("iam: adapt store: %w", err)
	}
	return orm.AdaptDB(sdb), conn, nil
}

// paths derives the data directory IAM's store lives under and its init_data.json
// path from cloud.Deps. DataDir empty falls back to CWD, exactly as the standalone
// iam default does; namespace renders the file's place within it. init_data.json is
// CWD-relative "init_data.json" (the standalone iam conf default), honoring the same
// `initDataFile` env override so a deployment points BOTH the embedded and standalone
// iam at one file (DRY, one source of seed truth).
func paths(deps cloud.Deps) (dir, initDataPath string) {
	dir = deps.DataDir
	if dir == "" {
		dir = "."
	}

	initDataPath = os.Getenv("initDataFile")
	if initDataPath == "" {
		initDataPath = "init_data.json"
	}
	return dir, initDataPath
}

// patterns is the ONE list of route patterns the identity surface occupies — every
// address IAM answers, spelled once. Both registrations read it: safeMount hangs the
// real handler on them and mountFailClosed hangs the 503 on the SAME set, so the
// degraded surface is exactly the mounted surface and neither can drift from the
// other.
//
// It is derived from Prefixes rather than restating them, plus the one address that is
// not under any of them: OIDC discovery + JWKS live at the ROOT by spec (RFC 8414 /
// OIDC Discovery 1.0), because a relying party reads
// /.well-known/openid-configuration off the ISSUER host, not off an API subtree. That
// wildcard is part of the identity contract, not a catch-all, and it is narrow by
// construction — it cannot shadow the console, and the deeper static routes under it
// (agentskills' /.well-known/agent-skills/*, cloud's own /.well-known/openapi.json)
// still win, because zip's matcher takes the most specific pattern regardless of
// registration order.
//
// The bare prefix is NOT listed separately: fiber's greedy `/*` matches the empty
// remainder, so `/v1/iam/*` already answers `/v1/iam`. safeMount registers the bare
// form too — the document then publishes /v1/iam as its own path rather than only
// /v1/iam/{wildcard1} — but the FAIL-CLOSED half needs no such entry, and adding one
// would be a second way to say the same thing.
func patterns() []string {
	out := make([]string, 0, len(Prefixes)+1)
	for _, p := range Prefixes {
		out = append(out, p+"/*")
	}
	return append(out, "/.well-known/*")
}

// safeMount GRAFTS the IAM app into cloud, under a recover so its only panic path —
// a registered enterprise feature failing to mount — becomes an error the caller
// fail-closes on, never a crash of the shared cloud binary.
//
// zip.Graft composes the App itself: cloud's router learns every route pattern IAM
// declares AND every op in IAM's registry, while IAM's own router keeps IAM's
// behaviour — its Use(authz.Guard) seam, its error handler, its config. Serving is
// unchanged and strictly cheaper than what it replaces (no net/http round trip, so
// the adapter's ~5% and its dropped fasthttp user-context both go).
//
// It replaces `app.All(pattern, zip.AdaptNetHTTP(iamserver.Handler(db)))`, and that
// line is where IAM's knowledge died. AdaptNetHTTP takes an http.Handler and returns
// a closure, so the App went in and a bare function came out — with IAM's 94 typed
// ops inside it. cloud published FIVE wildcard path keys and 35 placeholder
// operations where 78 real paths and 94 typed operations were: no schema, no MCP
// tool, no CLI command and no SDK method for a single one of them. That is what
// apps/iam/typed_wire_test.go used to hold as a permanent refusal; the refusal was
// a property of the SEAM, not of IAM, and the seam is gone.
//
// It also NARROWS the surface. A wildcard swallows every unknown path under its
// prefix; a graft registers only the patterns IAM declares, so a path IAM does not
// serve falls through to cloud instead of reaching IAM's 404.
//
// iamserver.Route is still the wrong call here, for the reason it always was: Route
// CO-MINGLES IAM's routes onto the host app, and IAM is a whole server. A graft is
// what confines it — IAM's routes run on IAM's router, and only the patterns IAM
// declares are reachable through cloud's.
func safeMount(app cloud.Router, db orm.DB) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("iam mount panicked: %v", r)
		}
	}()
	// A graft composes an App, so it needs the App and not the Router facade.
	// A subsystem that cannot reach it must fail its mount rather than serve
	// routes no projection knows (cloud.ZipApp's own rule).
	host := cloud.ZipApp(app)
	if host == nil {
		return fmt.Errorf("iam: the router is not a zip App, so IAM cannot be grafted")
	}
	return host.Graft(iamserver.NewApp(db))
}

// mountFailClosed serves an honest JSON 503 on every address IAM answers when IAM
// cannot boot, so an identity path says "iam unavailable" instead of falling through to
// the console SPA catch-all (webui.Mount's `/*`, registered last in every plugin
// binary), which would 200 HTML on an auth path. cloud and every other subsystem stay
// up — the fold's blast-radius isolation. During staged rollout hanzo.id is still
// served by the standalone iam pod via ingress, so clients never see this path until
// cutover.
//
// It covers patterns(), the same set safeMount hangs the real handler on, because a
// degraded surface SMALLER than the mounted one is the exact hole this function exists
// to close. It used to iterate Prefixes alone, which left /.well-known/* uncovered:
// with IAM down, `GET /.well-known/openid-configuration` — the FIRST call every relying
// party makes, and the one path here that is not under /v1 — reached the console and
// answered 200 with the SPA's HTML, so an OIDC client parsed a web page as its
// discovery document instead of seeing an outage.
func mountFailClosed(app cloud.Router) {
	failed := zip.AdaptNetHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"iam unavailable","code":503}`))
	}))
	for _, p := range patterns() {
		app.All(p, failed)
	}
}
