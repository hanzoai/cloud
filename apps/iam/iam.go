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
// CRUD, and the legacy verb-alias compat layer) is served by iamserver.Handler — that
// standalone app adapted to net/http and hung on the wildcards this file registers
// (safeMount, which says why Handler and not iamserver.Route). The specific
// self-service routes layered in front (account, agentskills) still win, because zip
// matches the most specific pattern, so the fold is collision-free.
//
// It is therefore OPAQUE to cloud's document: the nested app holds 94 typed ops and
// cloud's route table holds five wildcards, so none of the 35 operations the iam
// subset publishes can become a typed op. apps/iam/typed_wire_test.go gates that,
// and cloud's LLM.md ("apps/iam (0 of 25, and why)") records what closing it needs.
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
// Mounted in-process (the whole IAM v2 surface, registered at its canonical paths):
//
//	/v1/iam/*      OIDC/OAuth2 (/v1/iam/oauth/{authorize,token,userinfo,introspect,
//	               revoke,...}) + OIDC discovery (/v1/iam/.well-known/*) + signin +
//	               get-app-login + the v2 entity CRUD + the legacy verb-alias compat
//	/login/oauth/* browser authorize surface (the /v1/iam/oauth/authorize 302 target)
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
// comments at compile time. iam's HTTP surface has no typed op to lift from (every
// address is a relay, and its prose is declared through openapi.Describe below);
// the one op this covers is the internal-plane roster read in roster_rpc.go.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	iamserver "github.com/hanzoai/iam/server"
	"github.com/hanzoai/orm"
	ormdb "github.com/hanzoai/orm/db"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/cek"
	"github.com/hanzoai/cloud/openapi"
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

	log.Info("iam embedded in-process (clean iam-v2, zip-native + hanzoai/orm — iam-v1 retired)", "db", dbPath, "prefixes", Prefixes)
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

// relayMethods are the methods the document publishes for an `app.All` route, and
// therefore the methods each relay's prose has to cover. SEVEN, not the five a REST
// reader expects: `app.All` binds nine and openapi.From projects seven of them —
// get/post/put/patch/delete plus OPTIONS and TRACE. Describing five would leave two
// operations per address publishing an operationId and nothing else, which is the
// exact hole this prose exists to close (typed_wire_test.go derives the same count
// rather than writing it down, for the same reason).
var relayMethods = []string{
	http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
	http.MethodDelete, http.MethodOptions, http.MethodTrace,
}

// The identity plane's prose, declared beside the wire fact that keeps it untyped.
//
// A typed op's prose is lifted from its handler's doc comment by zipdoc, and every
// address here is one `app.All` relaying a whole nested app (safeMount) — so there
// is no handler in this package to lift from, and openapi.Describe is the seam.
// Without it the 35 operations iam publishes carry an operationId and nothing else:
// an SDK method that cannot explain itself, an MCP tool with no description, a CLI
// command with no help.
//
// It is DERIVED from the same two lists safeMount registers, not restated beside
// them, so the described set is the served set by construction. Adding a prefix to
// Prefixes without prose for it panics at init — the same fail-loud posture Describe
// itself takes on empty prose — rather than quietly publishing a bare operation.
func init() {
	for _, path := range append(append([]string{}, Prefixes...), patterns()...) {
		summary, description := prose(path)
		for _, method := range relayMethods {
			openapi.Describe(path, method, summary, description)
		}
	}
}

// prose is the statement for one relayed address. It keys on the PREFIX because the
// bare address and its subtree are two registrations of one relay and therefore one
// fact; saying it twice would let the copies rot apart.
func prose(path string) (summary, description string) {
	switch {
	case strings.HasPrefix(path, "/v1/iam"):
		return identitySummary, identityDescription
	case strings.HasPrefix(path, "/login/oauth"):
		return authorizeSummary, authorizeDescription
	case strings.HasPrefix(path, "/.well-known"):
		return discoverySummary, discoveryDescription
	}
	panic("iam: no prose for relayed address " + path + " — an address this subsystem " +
		"serves without prose publishes an operationId and nothing else")
}

const (
	identitySummary = "The whole Hanzo identity surface: OIDC and OAuth2, sign-in, and the " +
		"user, organization, application, role and credential records every Hanzo service " +
		"authenticates against."

	identityDescription = "Relays github.com/hanzoai/iam verbatim. The remainder of the path " +
		"and the method together select the operation INSIDE IAM — OIDC discovery and JWKS, the " +
		"oauth authorize/token/userinfo/logout/introspect/revoke/device endpoints, credential " +
		"and wallet sign-in, the front door a hosted login page self-configures from, the typed " +
		"CRUD over users, organizations, applications, providers, roles, projects, workspaces, " +
		"permissions, certs, keys, invitations and audit logs, SCIM 2.0, service accounts, " +
		"memberships, TOTP enrollment, and the Casdoor verb aliases (get-users, " +
		"add-organization, …) the live consoles still call. cloud does not interpret the " +
		"remainder or rewrite the reply: the status, the bytes and the Content-Type are the " +
		"nested app's own.\n\n" +

		"WHO MAY CALL IT is structural inside IAM — decided by which group a route is " +
		"registered on, never by an allow-list that can drift. The protocol half is public by " +
		"construction, because a caller holding no token has to be able to reach it: discovery, " +
		"JWKS, authorize, token, userinfo, logout, introspect, revoke, credential sign-in, the " +
		"CAIP-122 wallet flow, and the Docker registry token endpoint. Everything else is behind " +
		"IAM's one Guard and fails closed at 401 — in IAM's OWN {\"status\":401,\"error\":" +
		"\"authentication required\"} envelope, which is NOT cloud's error shape, so a client " +
		"that only parses cloud errors will not recognise a refusal from here.\n\n" +

		"WHAT IT IS SCOPED TO is the verified bearer's own organization: a request parameter " +
		"can never widen a read past the caller's authority, because the owner queried is the " +
		"owner authorized. The single cross-tenant scope is membership of the reserved `admin` " +
		"organization, and only such a principal may write a platform-owned record — the gate " +
		"that stops a tenant from overwriting a signing cert and minting its own tokens. That " +
		"predicate is read from the token SUBJECT, never from its `owner` or `organization` " +
		"claims, which name the APPLICATION's org and diverge from the user's for a shared app. " +
		"An org admin manages only what its own org owns; an ordinary user gets self-service on " +
		"its own record and nothing more.\n\n" +

		"TWO RULES a reader otherwise gets wrong. The oauth token, introspect and revoke " +
		"endpoints take application/x-www-form-urlencoded bodies by RFC 6749/7662/7009, not " +
		"JSON — a generated client that posts JSON to everything under this prefix turns a " +
		"working token exchange into a 400. And the prefix is not exclusively IAM's: a more " +
		"specific route beats this wildcard, so /v1/iam/keys and /v1/iam/onboard are served by " +
		"the account app rather than by the relay, even though the nested app registers those " +
		"addresses too.\n\n" +

		"If IAM cannot open its encrypted store or finish mounting, every address here answers " +
		"a JSON 503 instead of falling through to the console's single-page app. An identity " +
		"path must say it is down rather than return HTML a client will try to parse."

	authorizeSummary = "The interactive browser leg of the authorize flow, claimed by the " +
		"identity plane."

	authorizeDescription = "This is where /v1/iam/oauth/authorize sends a human. Having " +
		"validated the client, the redirect URI and S256 PKCE, that endpoint answers 302 to the " +
		"requesting application's own signinUrl — or, when the application record carries none, " +
		"to /login/oauth/authorize here, with the request re-encoded as a clean query string " +
		"built from known parameters only. RFC 8628's device verification page " +
		"(/login/oauth/device) is the other address beneath the prefix.\n\n" +

		"Both are PAGES a person opens, not JSON operations, and IAM registers no route under " +
		"this prefix at all — it claims the prefix so its Guard covers it. What this relay " +
		"actually returns is therefore IAM's refusal rather than a rendered sign-in form: with " +
		"no verified bearer, the same {\"status\":401,\"error\":\"authentication required\"} " +
		"envelope every gated IAM path answers, identically for every method, since there is no " +
		"route here for the method to select. An application configured with its own signinUrl " +
		"never reaches this prefix.\n\n" +

		"The address is claimed rather than answered, and claiming it is the point: it is more " +
		"specific than the console's terminal catch-all, so while IAM is mounted nothing else in " +
		"the binary can serve the target the authorize endpoint redirects to. When IAM cannot " +
		"boot it answers the same fail-closed JSON 503 the rest of the identity plane does."

	discoverySummary = "OIDC discovery and the JWKS at the issuer root, where a relying party " +
		"looks before it can do anything else."

	discoveryDescription = "Serves the two documents every OpenID Connect client reads first: " +
		"/.well-known/openid-configuration (also at /.well-known/oauth-authorization-server, RFC " +
		"8414) and /.well-known/jwks. Public by construction — a client has no token yet — and " +
		"minted per request by IAM from the deployment's own registered applications and signing " +
		"certs, with the issuer derived from the host that was asked, so a strict client never " +
		"has to reconcile two spellings of the origin. What is advertised is what IAM implements: " +
		"the authorization-code flow, S256 PKCE, the grants it honours, and the signing " +
		"algorithms whose public keys the JWKS actually publishes.\n\n" +

		"The wildcard sits at the ROOT because the spec puts it there, not because it is a " +
		"catch-all, and it is narrow by construction: zip matches the most specific pattern " +
		"regardless of registration order, so the deeper routes under it — agentskills' " +
		"/.well-known/agent-skills/* — still win. A method IAM does not serve at one of these " +
		"documents is its 405, and an unknown name under the prefix is its 404; neither reaches " +
		"the console.\n\n" +

		"Fail-closed matters most here, because this is the FIRST call a relying party makes. " +
		"Before the degraded half covered this wildcard, a discovery request during an IAM " +
		"outage fell through to the console catch-all and answered 200 with the single-page " +
		"app's HTML, which the client then parsed as its discovery document. It now answers a " +
		"JSON 503."
)

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
//
// Every one of these is a RELAY of a whole nested app and can never become a typed op;
// apps/iam/typed_wire_test.go holds that refusal as a gate rather than a comment.
func safeMount(app cloud.Router, db orm.DB) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("iam mount panicked: %v", r)
		}
	}()
	h := zip.AdaptNetHTTP(iamserver.Handler(db))
	// The bare prefix as well as its subtree: `/v1/iam/*` already MATCHES `/v1/iam`,
	// but the published document derives its paths from the route table, so without
	// the bare form the resource's own address appears nowhere in it.
	for _, prefix := range Prefixes {
		app.All(prefix, h)
	}
	for _, p := range patterns() {
		app.All(p, h)
	}
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
