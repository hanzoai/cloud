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
// The specific self-service routes layered in front (skills) still win, because
// zip matches the most specific pattern. The two addresses that were NOT specificity
// but SHADOWING — /v1/iam/keys and /v1/iam/onboard, where apps/account registered
// deprecated aliases at addresses IAM already owns and serves — are gone from
// apps/account: a graft refuses a duplicate address at compose time rather than
// letting registration order decide silently, and at api.hanzo.ai those two were
// already answered by IAM anyway (ingress routes /v1/iam/* there).
//
// THE STORE IS THE IDENTITY STORE — {DataDir}/iam/iam.db, opened with IAM's own
// opener (iamstore.Open: plain SQLite, WAL, busy timeout). It is the same file the
// standalone iam is pointed at with --db, which is the whole point: mount the identity
// volume there and this graft serves the identities that exist, rather than a second
// database that agrees with none of them. See openStore for what that costs and why
// the alternative is worse. This embed owns its OWN orm.DB outright, so the old fork's
// "ai bootstrap unable to open database file (14)" crash is gone. Config
// (orgs/apps/providers/signing certs) is seeded from the same init_data.json the
// deployment already provides (server.Seed, new-only + idempotent), so hanzo.id's
// OAuth/OIDC semantics are preserved.
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
// verbatim); the ops THIS package owns are the internal-plane reads of the store
// it holds — the roster (roster_rpc.go), an org's projects (projects_rpc.go) and
// a caller's waitlist state (approval_rpc.go).
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	iamstore "github.com/hanzoai/iam/pkg/store"
	iamserver "github.com/hanzoai/iam/server"
	"github.com/hanzoai/orm"
	"github.com/zap-proto/zip"
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

// DB returns the embedded IAM store's orm.DB for in-process readers (clients/platform,
// clients/deploy) that reflect the IAM-owned Project resource via
// github.com/hanzoai/iam/pkg/store. It is nil until Mount has run (IAM not enabled, or
// a boot failure that fail-closed the subsystem); callers MUST nil-guard and degrade to
// 503 rather than dereference it.
func DB() orm.DB { return embeddedDB }

// Shutdown releases the embedded IAM store. Idempotent.
//
// The ORM owns the pool it opened (iamstore.Open), so closing the orm.DB closes the
// database — which the standalone iam does the same way, from its own shutdown hook.
func Shutdown() error {
	db := embeddedDB
	embeddedDB = nil
	if db == nil {
		return nil
	}
	return db.Close()
}

// Mount opens IAM's embedded store, seeds config from the same init_data.json the
// deployment provides (non-fatal), and registers the whole IAM v2 surface at the
// prefixes identity owns (Prefixes). Called once by cloud.MountAll when "iam" is
// enabled.
func Mount(app cloud.Router, deps cloud.Deps) error {
	// The identity store lives here, so every read of it is published here.
	exposeRoster()
	exposeProjects()
	exposeApproval()

	log := deps.Logger.New("subsystem", "iam")

	dir, initDataPath := paths(deps)

	db, err := openStore(dir)
	if err != nil {
		log.Error("iam store open failed — serving fail-closed 503 (cloud stays up)", "err", err, "dir", dir)
		mountFailClosed(app)
		return nil
	}
	// Publish the opened store for in-process readers (DB()) — set only after a clean
	// open so DB() is nil whenever the subsystem is fail-closed.
	embeddedDB = db

	// Bind the transport that carries a verification code to a person (sender.go).
	// Binding is an ASSERTION: it tells IAM that a code handed over will reach
	// someone, and email/SMS sign-in plus both delivered second factors turn on
	// together because of it. So the only question here is whether this deployment
	// has a notify to reach at all.
	//
	// plane.Reach settles it with the ROUTER, which owns the app list — and starts a
	// cold notify while it is there, so the first person to ask for a code does not
	// pay for its boot. Nothing observable at this instant could have answered:
	// notify's socket is bound after Mount runs, and in the fleet it belongs to a
	// sibling child the router may not have started yet.
	//
	// ErrNoPeer is the ONLY answer that may hide the method — it means no such app is
	// deployed here, and saying so is honest. EVERY OTHER failure is an outage and
	// delivery is bound anyway: a send will then report the real fault, where reading
	// it as an absence would hide the method permanently. That distinction is not
	// pedantry — a stale socket read as "up" once left commerce unwoken for three days
	// and refused every prepaid-balance read behind it.
	bindDelivery(log)

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

	log.Info("iam embedded in-process (clean iam-v2, zip-native + hanzoai/orm — iam-v1 retired)", "store", dbPath(dir), "prefixes", Prefixes)
	return nil
}

// dbPath resolves the identity database within a data directory: {dir}/iam/iam.db.
//
// It is IAM's own name for its own file — the standalone iam binary is pointed at
// exactly this with --db. That is the point: ONE file, opened by whichever process is
// serving identity, never copied and never mirrored. Mounting the identity volume at
// {dir}/iam is therefore the whole of what a deployment has to say, and this function
// is the one place the name is spelled.
func dbPath(dir string) string { return filepath.Join(dir, "iam", "iam.db") }

// openStore opens THE identity store — with IAM's own opener.
//
// The store is not cloud's to open. iamstore.Open is the ONE path IAM's serving binary
// and its migrator both take, so a store this process writes and a store the iam CLI
// reads are byte-compatible by construction rather than by agreement: the same WAL
// mode, the same busy timeout, the same file. Opening it any other way makes a second
// format for one database, and the second one only ever has the wrong rows in it.
//
// IT USED TO OPEN THROUGH cek, and the reason it stopped is worth keeping. cek gives a
// keyed handle, which made this store encrypted at rest — a real property, and the
// argument for it was sound: an identity graph and every credential record readable
// from a lifted volume is exactly the exposure encryption exists to remove. What the
// argument missed is that it was protecting the WRONG FILE. cek derives its own path,
// so this opened {DataDir}/orgs/_platform/global.db while every identity in production
// lives in a plain SQLite file the standalone iam wrote; the encrypted store held four
// kilobytes and no users, and asking it for the signing keys returned {"keys":[]}. A
// fully-mounted, completely empty identity service is not a security posture.
//
// cek cannot be pointed at the real file either — not "should not": it derives a key
// unconditionally and refuses a plaintext database at open, which is measured, not
// assumed. So the choice is between an encrypted store with no identities in it and
// the store that has them. Encrypting the one that has them means re-keying an
// existing file, which is a migration, and the directive here is forward-only: one
// store, pointed at, never converted.
//
// SO THE IDENTITY STORE IS PLAINTEXT AT REST, exactly as it is today, and that is a
// cost named rather than hidden. The only shape that changes it without a migration is
// a NEW store born encrypted with the old one retired, and that is a separate decision
// this open cannot smuggle in.
//
// cek keeps opening cloud's OWN stores. Each store is opened by whoever owns it.
func openStore(dir string) (orm.DB, error) {
	// REFUSE TO CREATE ONE. This process is pointed at an identity store that
	// already exists — that is the whole shape: one store, pointed at, never
	// converted. iamstore.Open creates the file when it is absent, which is right
	// for the standalone iam (it OWNS the store and must be able to make one) and
	// catastrophic here: if the volume holding it is not mounted, or is mounted
	// somewhere else, or is shadowed by an emptyDir, this would mint a fresh empty
	// database and serve it. Identity would simply be gone — every account absent,
	// /v1/iam/.well-known/jwks answering {"keys":[]} so every token in the fleet
	// fails to verify — and nothing would have failed to say so, because from the
	// code's point of view opening an empty database is a success.
	//
	// An absent store is therefore a MOUNTING FAULT, and the honest answer to a
	// mounting fault is to say so. Mount's caller already knows what to do with
	// that: it serves an honest 503 on every identity address (mountFailClosed)
	// rather than falling through to a catch-all that would answer HTML on an auth
	// path. A loud 503 is recoverable in a minute; a silently empty identity
	// service is not recoverable at all, because by then clients have been told
	// their accounts do not exist.
	path := dbPath(dir)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("iam: the identity store is not at %s, so this process has nothing to serve — check that the volume holding it is mounted there: %w", path, err)
	}
	db, err := iamstore.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("iam: open store: %w", err)
	}
	return db, nil
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
// (skills' /.well-known/agent-skills/*, cloud's own /.well-known/openapi.json)
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

// The device-approval lookup states itself HERE because the graft leaves it nowhere
// else to. It is an untyped route in another module, so neither seam that normally
// carries prose reaches it: zipdoc lifts doc comments off TYPED ops, and the doc
// comment on its handler therefore never enters zip's extraction for a host to read.
// openapi.Describe is the seam for exactly that route, and it is additive metadata on
// a route the router already carries — it cannot add, move or rename an operation.
//
// This is the host speaking for a route it mounts, so it is second-best by
// construction: the sentence belongs upstream, on the operation, where the handler
// lives. When github.com/hanzoai/iam gives the op its own prose, delete this.
func init() {
	openapi.Describe("/v1/iam/oauth/device/info", http.MethodPost,
		"Name the application a pending device code is asking to sign in.",
		"Answers \"what am I approving?\" for a pending user_code, so the approval page can "+
			"name the application a human is about to authorize. Both fields come off the "+
			"pending code's OWN application — never off the portal the browser happens to be "+
			"on — so the screen cannot name one application while the code belongs to "+
			"another.\n\n"+
			"Requires a signed-in session, resolved from the browser's session cookie exactly "+
			"as the approval itself resolves it. Not signed in is not a refusal to explain: it "+
			"carries the stable login-required code the approval page branches on to sign the "+
			"human in first.\n\n"+
			"POST for a read, deliberately, for the same reason RFC 7662 introspection beside "+
			"it is POST: the argument is a SECRET. A user_code in a request line is copied into "+
			"ingress and proxy access logs, which a POST body is not.\n\n"+
			"Unknown, expired, already used and already approved all get ONE opaque refusal — "+
			"the same one the approval attempt would get. The user_code carries only 40 bits, "+
			"so an answer that distinguished those states would be an oracle for hunting live "+
			"codes; gated and opaque, this reveals strictly less than the approval the same "+
			"caller could already attempt.")
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
	// zip v1.23: Use is the ONE composition verb, and an *App IS a Component, so
	// the child is included by reference. Graft refused an address conflict at the
	// call; Use defers that verdict to Build, where the whole program is known.
	host.Use(iamserver.NewApp(db))
	return nil
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
