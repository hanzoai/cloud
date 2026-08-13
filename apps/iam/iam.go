// Package iam is Hanzo's identity provider: users, organizations, applications,
// and the OIDC/OAuth2 endpoints every Hanzo service authenticates against.
//
// It folds Hanzo IAM into the unified hanzoai/cloud binary as an in-process
// subsystem (HIP-0106) — the LAST binary-consolidation piece: "one Go binary
// (hanzoai/cloud) embeds IAM + KMS + o11y".
//
// CLEAN IAM (v2). This subsystem embeds github.com/hanzoai/iam —
// the clean-room identity rewrite on the native Hanzo stack (zip + hanzoai/orm +
// hanzoai/sqlite). The retired fork (github.com/hanzoai/iam-v1) is GONE from
// cloud's graph: there is no process-global to corrupt, no InitEmbed, no
// session-manager hook, no shared-AppConfig co-residence hazard with the sibling
// `ai` fork. The whole IAM v2 surface (OIDC discovery/JWKS, oauth
// authorize/token/userinfo/introspect/revoke, get-app-login, signin, the v2 entity
// CRUD, and the legacy verb-alias compat layer) is COMPOSED in process by one line:
// `host.Use(iamserver.NewApp(db))`, so cloud's router learns IAM's route patterns AND
// its op registry, while IAM's own router keeps IAM's behaviour.
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
// FAIL-CLOSED, NOT FAIL-LOUD. A broken or misconfigured IAM does NOT crash the
// consolidated binary. A store that will not open is handed to IAM as nil, and IAM's
// own App then refuses every identity request 503 — while every co-resident subsystem
// (KMS, o11y, …) stays up. That is the blast-radius isolation the consolidation exists
// for, mirroring the KMS "no master key → health-only" pattern.
//
// The DEGRADE DOES NOT MOVE THE ROUTE TABLE, and that is the part worth stating. It
// used to: an absent volume registered five `app.All` wildcards instead of the App, so
// the document, the MCP tool list, the SDKs and the CLI carried 15 undescribed
// catch-alls where 94 typed operations belong — one program with two route tables,
// chosen at boot by a stat() call. Now there is one registration and the addresses are
// whatever IAM declares; only the answer changes.
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
// Each brand emits its OWN issuer, resolved from the request Host. That needs the
// Host to survive the hop to this child, which it did not until zap-proto/http
// v0.3.2 stopped dropping it on the wire; every brand fell back to one issuer,
// and a token claiming the wrong iss is rejected by that brand's own clients.
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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/brand"
	iamstore "github.com/hanzoai/iam/pkg/store"
	iamserver "github.com/hanzoai/iam/server"
	"github.com/hanzoai/orm"
)

// Prefixes are the canonical absolute prefixes the IAM identity surface owns — this
// subsystem's own list, and what the light host's router is told to hand over.
// Everything outside them belongs to cloud, so the console catch-all keeps serving the
// SPA. The registration itself does not read this list: composing IAM's App registers
// exactly the patterns IAM declares, which is narrower and cannot drift from them.
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

	log := luxlog.Default().New("subsystem", "iam")

	// The brand map is derived from the registry rather than written down: it was a
	// thirteen-entry blob duplicated in two deployment files, so a new brand was
	// three edits away from minting under another brand's issuer. An operator who
	// pins one still wins.
	if os.Getenv("IAM_ISSUER_MAP") == "" {
		if b, err := json.Marshal(brand.IssuerByHost()); err == nil {
			_ = os.Setenv("IAM_ISSUER_MAP", string(b))
		}
	}

	dir, initDataPath := paths(deps)

	// A store that will not open is LOUD and does not change the route table. IAM's
	// own App refuses every request 503 when it is handed no store (iamserver.NewApp),
	// so the addresses this process serves are the ones IAM declares either way —
	// which is what keeps the published document, the MCP tool list, the SDKs and the
	// CLI from depending on whether a volume happened to be mounted.
	db, err := openStore(dir)
	if err != nil {
		log.Error("iam store absent — every identity op answers 503 (cloud stays up)", "err", err, "dir", dir)
	}
	// Published for in-process readers (DB()) — nil while there is no store, so a
	// reader can tell.
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
	// Seeding reaches the database, so it is the one step here a nil store cannot
	// take — and skipping it is not a degrade: there is nothing to write to.
	if db != nil {
		if sum, serr := iamserver.Seed(context.Background(), db, initDataPath); serr != nil {
			log.Warn("iam seed skipped (non-fatal)", "err", serr, "init_data", initDataPath)
		} else if sum != nil {
			log.Info("iam seed applied", "created", sum.Created, "skipped", sum.Skipped, "init_data", initDataPath)
		}
	}

	// An EMPTY store is not a store that can serve, and openStore cannot see it:
	// it refuses a file that is ABSENT, while one that exists and holds nothing
	// opens cleanly and answers jwks with {"keys":[]} at 200. A relying party
	// reads that as "this token does not verify", not as an outage, so every token
	// in the fleet fails with nothing reporting a fault. Checked AFTER Seed, which
	// is what fills a new store.
	if db != nil {
		certs, cerr := iamstore.ListCerts(context.Background(), db)
		if cerr != nil || len(certs) == 0 {
			log.Error("iam store carries no signing certificate — identity answers 503 rather than an empty keyset (cloud stays up)",
				"err", cerr, "store", StorePath(dir))
			db = nil
			embeddedDB = nil
		}
	}

	// ONE registration. An App is a Component, so composing IAM is the same verb as
	// adding middleware, and cloud's router learns IAM's route patterns and its op
	// registry — every one of the typed operations a wildcard used to swallow.
	host := cloud.ZipApp(app)
	if host == nil {
		return fmt.Errorf("iam: the router is not a zip App, so IAM cannot be composed")
	}
	host.Use(iamserver.NewApp(db))

	log.Info("iam embedded in-process (clean iam-v2, zip-native + hanzoai/orm — iam-v1 retired)", "store", StorePath(dir), "prefixes", Prefixes)
	return nil
}

// dbPath resolves the identity database within a data directory: {dir}/iam/iam.db.
//
// It is IAM's own name for its own file — the standalone iam binary is pointed at
// exactly this with --db. That is the point: ONE file, opened by whichever process is
// serving identity, never copied and never mirrored. Mounting the identity volume at
// {dir}/iam is therefore the whole of what a deployment has to say, and this function
// is the one place the name is spelled.
// StorePath is WHERE THE IDENTITY STORE LIVES under a data directory, named once
// so nobody spells it a second time.
//
// Exported because this process refuses to CREATE the store (see openStore), which
// makes its location something a caller has to be able to ask for rather than
// guess: anything standing a real embedded IAM up — the migrator, a sibling app
// proving it reads the real store and not a stand-in — needs IAM's own opener
// pointed here first. A second copy of this path is the mounting fault openStore
// exists to refuse, written by hand.
func StorePath(dir string) string { return filepath.Join(dir, "iam", "iam.db") }

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
	// An absent store is therefore a FAULT, and the honest answer to a fault is to
	// say so. Mount's caller already knows what to do with that: it hands IAM no
	// store, and IAM answers 503 on every identity address it declares rather than
	// letting one fall through to a catch-all that would answer HTML on an auth
	// path. A loud 503 is recoverable in a minute; a silently empty identity
	// service is not recoverable at all, because by then clients have been told
	// their accounts do not exist.

	// WHICH BACKEND HOLDS IDENTITY. Empty or "sqlite" is the embedded file this
	// has always been; "sql" is hanzoai/sql over ZAP, which is what production
	// should run and what makes the paragraph above stop being true.
	//
	// It is a knob because there is no other way to say it — the backend cannot be
	// derived from anything the process already knows — and it defaults to the
	// behaviour that exists today, so a deployment that sets nothing is unchanged.
	//
	// On "sql" the identity store stops being a file on one volume: no path, no
	// per-file key, and nothing a volume-wide encryption sweep can convert out
	// from under a plain-SQLite opener. That is not a hypothetical — it is what
	// took every brand's login down on 13 Aug, when this store was the one
	// plaintext database on a volume where everything else was cek-encrypted, and
	// a sweep did the obviously-right thing to it.
	backend := strings.TrimSpace(os.Getenv("IAM_STORE_BACKEND"))

	if backend != "" && backend != "sqlite" {
		// NO PATH TO CHECK, and the invariant below still has to hold. The file
		// stat was never about files: it was about refusing to serve an identity
		// store this process did not find, because minting an empty one answers
		// every account with "does not exist" and reports success.
		//
		// A remote backend gets the first half for free — an unreachable server
		// fails the open, loudly. It does NOT get the second half: a reachable but
		// EMPTY database opens fine and would be served. Closing that needs a
		// populated-store check through the entity API, which is not written yet,
		// so it is named here rather than assumed. Do not point this at an empty
		// database and expect to be told.
		db, err := iamstore.Open(backend, "")
		if err != nil {
			return nil, fmt.Errorf("iam: open the %s identity store: %w", backend, err)
		}
		return db, nil
	}

	path := StorePath(dir)
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
