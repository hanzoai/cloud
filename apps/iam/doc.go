// Package iam is Hanzo's identity provider: users, organizations, applications,
// and the OIDC/OAuth2 endpoints every Hanzo service authenticates against.
//
// It folds Hanzo IAM into the unified hanzoai/cloud binary as an in-process
// subsystem (HIP-0106) — the LAST binary-consolidation provider: "one Go binary
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
// property of that CLIENT, never of IAM, and the client is gone.
//
// The specific self-service routes layered in front (skills) still win, because
// zip matches the most specific pattern. The two addresses that were NOT specificity
// but SHADOWING — /v1/iam/keys and /v1/iam/onboard, where apps/account registered
// deprecated aliases at addresses IAM already owns and serves — are gone from
// apps/account: zip refuses a duplicate address at build rather than
// letting registration order decide silently, and at api.hanzo.ai those two were
// already answered by IAM anyway (ingress routes /v1/iam/* there).
//
// THE STORE IS THE IDENTITY STORE — {DataDir}/iam/iam.db, ENCRYPTED AT REST under the
// key cek derives for it, and converted in place the first time it is opened if it
// arrived plaintext. It is the same file the standalone iam is pointed at with --db,
// which is the whole point: mount the identity volume there and this subsystem serves the
// identities that exist, rather than a second database that agrees with none of them.
// See openStore for how both of those are true at once, which they were not before.
// This embed owns its OWN orm.DB outright, so the old fork's
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
// Composed in process (the whole IAM v2 surface, at its canonical paths — every
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
