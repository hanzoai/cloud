// Package iam folds Hanzo IAM into the unified hanzoai/cloud binary as an
// in-process subsystem (HIP-0106) — the LAST binary-consolidation piece:
// "one Go binary (hanzoai/cloud) embeds IAM + KMS + o11y".
//
// CLEAN IAM (v2), NOT CASDOOR. This subsystem embeds github.com/hanzoai/iam —
// the clean-room identity rewrite on the native Hanzo stack (zip + hanzoai/orm +
// hanzoai/sqlite), NOT the retired Casdoor/Beego fork (hanzoai/iam-v1, dead). The
// clean iam is DESIGNED for exactly this embed: server.Handler(db) is "the drop-in
// shape hanzoai/cloud uses to swap the legacy Beego IAM catch-all" — the whole
// IAM v2 surface (OIDC discovery/JWKS, oauth authorize/token/userinfo/introspect/
// revoke, get-app-login, signin, the v2 entity CRUD, SCIM) behind ONE wildcard,
// so the specific self-service routes layered in front still win by Fiber
// specificity. Same topology the Beego mount had — collision-free — but on the
// clean stack, with iam-v1/beego GONE from cloud's graph.
//
// The store is embedded SQLite under <DataDir>/iam (server.OpenSQLite, WAL) — no
// Postgres, no shared-database co-residence hazard (the old Casdoor-fork "ai
// bootstrap unable to open database file (14)" crash is gone: v2 owns its own
// orm.DB). Config (orgs/apps/providers/signing certs) is seeded from the same
// init_data.json the deployment already provides (server.Seed, new-only +
// idempotent), so hanzo.id's OAuth/OIDC semantics are preserved.
//
// FAIL-CLOSED, NOT FAIL-LOUD. A broken/misconfigured IAM does NOT crash the
// consolidated binary: an open/seed failure degrades THIS subsystem to a 503
// fail-closed on every IAM prefix (mountFailClosed) while every co-resident
// subsystem (KMS, o11y, …) stays up — the blast-radius isolation the whole
// consolidation exists for, mirroring the KMS "no master key → health-only"
// pattern.
//
// Mounted in-process (whole IAM v2 handler, full request path preserved):
//
//	/v1/iam/*      API + OAuth (/v1/iam/oauth/{authorize,token,userinfo,introspect,
//	               revoke,...}) + OIDC (/v1/iam/.well-known/{openid-configuration,
//	               jwks,...}) + signin + get-app-login + the entity CRUD
//	/.well-known/* legacy root OIDC discovery + JWKS (relying-party compatibility)
//	/login/oauth/* browser authorize surface (the /v1/iam/oauth/authorize 302 target)
//	/scim/*        SCIM 2.0 user/group provisioning
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
	"net/http"
	"os"
	"path/filepath"

	iamserver "github.com/hanzoai/iam/server"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
)

// iamPrefixes is every root path prefix the IAM handler owns. The whole handler
// is mounted at each via app.All(prefix+"/*", zip.AdaptNetHTTP(h)), which
// preserves the full request path. The handler answers only paths IAM registered;
// an unknown path under a prefix 404s exactly as standalone IAM does, so a broad
// mount cannot leak another subsystem's surface. (CAS and the /_/iam login SPA
// that the retired Beego fork served are intentionally NOT here: CAS is a legacy
// protocol v2 does not carry, and the login UI is served by the standalone
// hanzo/id SPA, not embedded.)
var iamPrefixes = []string{
	"/v1/iam",      // API + oauth + .well-known + signin + get-app-login + entity CRUD
	"/.well-known", // legacy root OIDC discovery + JWKS (RP compatibility)
	"/login/oauth", // browser authorize surface (the /v1/iam/oauth/authorize 302 target)
	"/scim",        // SCIM 2.0
}

// Mount boots the in-process clean IAM (v2) over an embedded SQLite store and
// attaches its http.Handler to cloud's shared zip.App. Called once by
// cloud.MountAll when "iam" is enabled.
func Mount(app *zip.App, deps cloud.Deps) error {
	log := deps.Logger.New("subsystem", "iam")

	// IAM persists its SQLite store under <DataDir>/iam — its OWN file, so there is
	// no shared-database co-residence with the sibling `ai` subsystem (the v2 rewrite
	// owns its orm.DB outright; the old Casdoor-fork crash is gone).
	dataDir := filepath.Join(deps.DataDir, "iam")
	if deps.DataDir != "" {
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			log.Error("iam data dir failed — serving fail-closed 503 (cloud stays up)", "err", err)
			mountFailClosed(app)
			return nil
		}
	}

	db, err := iamserver.OpenSQLite(filepath.Join(dataDir, "iam.db"))
	if err != nil {
		log.Error("iam store open failed — serving fail-closed 503 (cloud stays up; standalone iam pod unaffected)", "err", err)
		mountFailClosed(app)
		return nil
	}

	// Seed config (orgs/apps/providers/signing certs) from the deployment's
	// init_data.json — the SAME file the standalone iam uses. New-only + idempotent,
	// so a re-mount never clobbers live rows. A seed error is non-fatal: an
	// already-provisioned store keeps working; only a first boot with no config and
	// no init_data would be empty (and would then fail closed on token mint).
	if initData := firstEnv("initDataFile", "IAM_INIT_DATA", "IAM_INIT_DATA_FILE"); initData != "" {
		if sum, serr := iamserver.Seed(context.Background(), db, initData); serr != nil {
			log.Warn("iam seed (init_data) failed; continuing with the existing store", "path", initData, "err", serr)
		} else if sum != nil {
			log.Info("iam seeded from init_data", "created", sum.Created, "skipped", sum.Skipped)
		}
	}

	// server.Handler is the clean iam's designed drop-in: the whole IAM v2 surface
	// as ONE net/http handler, routed on the full request path. Mount it at every
	// prefix IAM owns — the same topology the Beego catch-all had.
	mountHandler(app, iamserver.Handler(db))

	log.Info("iam embedded in-process (clean iam-v2, zip + hanzoai/orm — Casdoor iam-v1 retired)", "data_dir", dataDir, "prefixes", iamPrefixes)
	return nil
}

// firstEnv returns the first non-empty environment value among keys, else "".
func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// mountFailClosed serves an honest JSON 503 on every IAM prefix when the embed
// cannot boot, so /v1/iam/* answers "iam unavailable" instead of falling through
// to the console SPA catch-all (which would return HTML 200 for an auth path).
// cloud and every other subsystem stay up — the fold's blast-radius isolation.
// During staged rollout hanzo.id is still served by the standalone iam pod via
// ingress, so clients never see this path until cutover.
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

// mountHandler attaches the IAM http.Handler at every prefix IAM owns. Split from
// Mount so the routing plumbing — app.All(prefix+"/*", zip.AdaptNetHTTP(h))
// dispatching to the handler with the ORIGINAL request path preserved — is
// unit-testable without booting the full IAM runtime.
func mountHandler(app *zip.App, handler http.Handler) {
	for _, p := range iamPrefixes {
		app.All(p+"/*", zip.AdaptNetHTTP(handler))
	}
}
