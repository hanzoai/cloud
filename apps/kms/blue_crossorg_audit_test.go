package kms_test

// BLUE — CROSS-ORG SECRET ACCESS IS AUDITED.
//
// RED proved KMS org isolation holds: no URL traversal, no forged header, no
// audience trick reaches another tenant's secret (red_orgscope_isolation_test).
// The ONE capability it found and deliberately RETAINED is platform sudo — a
// HUMAN SuperAdmin, holding a signed membership of the reserved admin org, may
// switch into another tenant via X-Org-Id and read it. Unforgeable, human-only,
// machine-denied, and identical to how every other subsystem in the fleet works.
//
// The capability was never the problem. The problem was that it left NO TRACE.
// A cross-org secret read is a plain 200 GET on a plain tenant route, so the
// audit middleware's coverage predicate — mutations, /v1/admin/*, and denials —
// classified the single highest-value read in the fleet as request-log noise.
// An unaudited cross-org secret read is the actual finding; this file pins the
// fix.
//
// FOUR PROPERTIES, one per test:
//   1. the retained capability still WORKS (RED's decision is not silently reverted)
//      and now EMITS a record naming actor, home org, target org, path, outcome;
//   2. the record carries the secret's PATH and never its VALUE;
//   3. a NON-admin cross-org attempt is still refused and is NOT recorded as an
//      impersonation (RED's isolation stays green, and the new field cannot be
//      spoofed into meaning something it doesn't);
//   4. an ordinary SAME-ORG read stays UNaudited — the fix targets impersonation,
//      it does not turn the trail into a request log.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	luxlog "github.com/luxfi/log"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// auditedWorld stands up the REAL pipeline with the audit middleware installed in
// its production position — after the identity boundary, wrapping every subsystem
// (serve.go: SanitizeIdentity → AuditTrail → subsystems). newAppWithIdentity omits
// AuditTrail, so this cannot reuse it: the whole point is to observe what the
// trail records.
//
// Two orgs are seeded at the IDENTICAL coordinate with DISTINCT plaintexts, the
// same self-identifying-leak technique RED used: whichever string comes back
// names the org that actually answered.
func auditedWorld(t *testing.T) (*zip.App, *audit.Recorder, *rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	cfg := e2eCfg(t, e2eJWKS(t, &key.PublicKey).URL)

	rec, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })

	deps := cloud.BuildDeps(cfg)
	app := zip.New(zip.Config{Logger: luxlog.Default()})
	app.Use(middleware.Recover())
	app.Use(cloud.IdentityMiddleware(cfg))
	app.Use(cloud.AuditTrail(rec))
	if err := cloud.UseAll(app, mountSpecs(), cfg, deps); err != nil {
		t.Fatalf("UseAll: %v", err)
	}

	sealPlatformSecret(t, deps.KMS, paasOrgA, paasValueA) // maxpower — the target
	sealPlatformSecret(t, deps.KMS, paasOrgB, isoValueB)  // acme, SAME coordinate
	sealPlatformSecret(t, deps.KMS, "admin", adminSecret) // the admin's own

	return app, rec, key, "/v1/kms" + paasEnvPath
}

// mustJSON serializes v for a whole-record assertion, so a leak is caught in ANY
// field rather than only the ones a test remembered to check.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// adminSecret is the admin org's OWN secret, distinct from both tenants' so a
// same-org read is unambiguously identifiable in an assertion.
const adminSecret = "s3kr3t-of-admin-AUDIT"

// impersonations returns every CROSS-ORG record in the trail. Filter.Impersonated
// is the query this control exists to answer — "every time an admin acted inside
// a tenant that was not their own" — so the test asks the same question an
// auditor would, through the same API, rather than reaching into SQL.
func impersonations(t *testing.T, rec *audit.Recorder) []audit.Record {
	t.Helper()
	rows, _, err := rec.Query(context.Background(), audit.Filter{Impersonated: true})
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	return rows
}

// verifyChain asserts the hash chain is intact. Run after every test that writes
// records: the impersonation field added a COLUMN, and a field that is hashed but
// not persisted would rehydrate empty and be reported as tampering. This catches
// that class of mistake at the only place it shows up.
func verifyChain(t *testing.T, rec *audit.Recorder) {
	t.Helper()
	got, err := rec.Verify(context.Background())
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Verdict != audit.Intact {
		t.Fatalf("audit chain %s at seq %d: %s", got.Verdict, got.BrokenAt, got.Reason)
	}
	t.Logf("chain %s intact: %d records, head %s", got.Name, got.Count, got.Head[:16])
}

// ── 1. the retained capability works AND is recorded ──────────────────────────
//
// This is the test the task asks for: a SuperAdmin cross-org read SUCCEEDS and
// EMITS an audit record naming the target org + path. It pins BOTH halves, so
// neither can regress silently — stripping platform sudo fails the first half,
// and dropping the audit fails the second.
func TestBlueAudit_SuperAdminCrossOrgRead_IsAudited(t *testing.T) {
	app, rec, key, path := auditedWorld(t)

	// The RETAINED capability: a HUMAN member of the reserved admin org switches
	// into maxpower and reads maxpower's secret.
	isoGet(t, app, "SuperAdmin + X-Org-Id:maxpower", path,
		isoTok{owner: "admin", isAdmin: true}.mint(t, key),
		map[string]string{"X-Org-Id": paasOrgA}).
		isValue(t, paasValueA)

	rows := impersonations(t, rec)
	if len(rows) != 1 {
		t.Fatalf("cross-org read produced %d impersonation records, want exactly 1 — an unaudited cross-org secret read is the finding this test exists to prevent", len(rows))
	}
	r := rows[0]

	// WHO, FROM WHERE, INTO WHERE — the three facts that make the record useful.
	// Without Home, actor.org=="maxpower" is indistinguishable from a genuine
	// maxpower member reading their own secret.
	if r.Actor.Home != "admin" {
		t.Errorf("actor.home = %q, want %q (the org the admin came FROM)", r.Actor.Home, "admin")
	}
	if r.Actor.Org != paasOrgA {
		t.Errorf("actor.org = %q, want %q (the org acted IN — the TARGET tenant)", r.Actor.Org, paasOrgA)
	}
	if r.Actor.Sub == "" {
		t.Error("actor.sub is empty — the record must name WHICH admin")
	}
	if !r.Auth.IsAdmin {
		t.Error("auth.isAdmin = false, want true — the record must show this was platform sudo")
	}

	// WHAT and WITH WHAT RESULT.
	if !strings.Contains(r.Path, "/v1/kms/secrets/") {
		t.Errorf("path = %q, want the KMS secret path", r.Path)
	}
	if r.Resource.Type != "secrets" {
		t.Errorf("resource.type = %q, want %q", r.Resource.Type, "secrets")
	}
	if r.Outcome.Result != "success" || r.Outcome.Status != 200 {
		t.Errorf("outcome = %+v, want success/200 (the read DID happen — the record must say so)", r.Outcome)
	}

	t.Logf("AUDITED: %s %s — actor=%s home=%s target=%s result=%s",
		r.Method, r.Path, r.Actor.Sub, r.Actor.Home, r.Actor.Org, r.Outcome.Result)

	verifyChain(t, rec)
}

// ── 2. the record carries the PATH, never the VALUE ───────────────────────────
//
// An audit trail that records secret VALUES is a second copy of the secret store
// with weaker access control. The middleware never reads bodies, so this is
// structural — but "structural" is exactly the kind of claim that rots, so it is
// asserted over the whole serialized record rather than field by field.
func TestBlueAudit_RecordCarriesPathNotValue(t *testing.T) {
	app, rec, key, path := auditedWorld(t)

	isoGet(t, app, "SuperAdmin cross-org read", path,
		isoTok{owner: "admin", isAdmin: true}.mint(t, key),
		map[string]string{"X-Org-Id": paasOrgA}).
		isValue(t, paasValueA)

	rows := impersonations(t, rec)
	if len(rows) != 1 {
		t.Fatalf("want 1 impersonation record, got %d", len(rows))
	}

	// The whole record, every field, as the console would serialize it.
	blob := mustJSON(t, rows[0].ToWire())
	if strings.Contains(blob, paasValueA) {
		t.Fatalf("SECRET VALUE LEAKED INTO THE AUDIT TRAIL: record contains %q: %s", paasValueA, blob)
	}
	// The path IS recorded — the record is useless if it cannot say WHICH secret.
	if !strings.Contains(blob, paasKey) {
		t.Errorf("record does not name the secret %q — an audit record must identify what was read: %s", paasKey, blob)
	}
	t.Logf("record (no value, path present): %s", blob)
}

// ── 3. a NON-admin cross-org attempt: still refused, still not an impersonation ─
//
// RED's isolation property must stay green, and the new Home field must not be
// spoofable into existence. A non-admin selecting another org has its selection
// DISCARDED (it is not a member), so it reads its OWN secret — home == effective,
// so nothing is recorded as cross-org. The trail's impersonation set stays empty:
// a caller cannot manufacture an impersonation record, nor evade one.
func TestBlueAudit_NonAdminCrossOrg_RefusedAndNotImpersonation(t *testing.T) {
	app, rec, key, path := auditedWorld(t)

	// A validated acme principal naming maxpower: selection discarded, acme's own
	// secret returned, maxpower's plaintext never present.
	isoGet(t, app, "acme + X-Org-Id:maxpower", path,
		isoTok{owner: paasOrgB}.mint(t, key),
		map[string]string{"X-Org-Id": paasOrgA}).
		noLeak(t, paasValueA).
		isValue(t, isoValueB)

	// An ORG admin of a non-admin org is not platform sudo.
	isoGet(t, app, "acme ORG-admin + X-Org-Id:maxpower", path,
		isoTok{owner: paasOrgB, isAdmin: true}.mint(t, key),
		map[string]string{"X-Org-Id": paasOrgA}).
		noLeak(t, paasValueA).
		isValue(t, isoValueB)

	// A MACHINE principal in the admin org is denied the switch entirely. A GENERIC
	// one — no membership set, no owner-bound machine audience — is not positively
	// identified at all: its token is indistinguishable from a human's minted before
	// the `orgs` claim shipped, so it resolves NO org and is refused outright rather
	// than having one read out of the app-selected `owner` claim. The KMS-sync
	// identity, which its audience DOES vouch for, stays org-scoped and working
	// (TestRedIso_C_AdminCrossOrg covers both halves).
	isoGet(t, app, "generic admin-org MACHINE + X-Org-Id:maxpower", path,
		isoTok{owner: "admin", isAdmin: true, machine: true}.mint(t, key),
		map[string]string{"X-Org-Id": paasOrgA}).
		noLeak(t, paasValueA).
		noLeak(t, adminSecret)

	// Anonymous forge: no principal, so the org gate refuses it outright.
	isoGet(t, app, "forged X-Org-Id, NO bearer", path, "",
		map[string]string{"X-Org-Id": paasOrgA, "X-User-IsAdmin": "true", "X-User-Owner": "admin"}).
		noLeak(t, paasValueA).
		isStatus(t, 403)

	if rows := impersonations(t, rec); len(rows) != 0 {
		t.Fatalf("non-admin traffic produced %d impersonation records, want 0 — a caller must not be able to manufacture one: %+v", len(rows), rows)
	}

	// The 403 IS audited (it is a denial), so the trail is not simply empty —
	// proving the absence above is a real classification, not a dead recorder.
	all, _, err := rec.Query(context.Background(), audit.Filter{Result: "deny"})
	if err != nil {
		t.Fatalf("query denies: %v", err)
	}
	if len(all) == 0 {
		t.Error("no denial recorded — the recorder is not observing this pipeline at all, so the zero-impersonation result above proves nothing")
	}
	t.Logf("denials recorded: %d, impersonations: 0", len(all))

	verifyChain(t, rec)
}

// ── 4. an ordinary SAME-ORG read stays unaudited ──────────────────────────────
//
// The fix must not turn the audit trail into a request log. A SuperAdmin reading
// its OWN org — no switch header — is an ordinary read and stays out of the
// trail, exactly like any tenant reading its own secret.
func TestBlueAudit_SameOrgRead_StaysUnaudited(t *testing.T) {
	app, rec, key, path := auditedWorld(t)

	// SuperAdmin at home.
	isoGet(t, app, "SuperAdmin, no switch header", path,
		isoTok{owner: "admin", isAdmin: true}.mint(t, key), nil).
		isValue(t, adminSecret)

	// An ordinary tenant reading its own secret.
	isoGet(t, app, "maxpower reads its own", path,
		isoTok{owner: paasOrgA}.mint(t, key), nil).
		isValue(t, paasValueA)

	rows, total, err := rec.Query(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if total != 0 {
		t.Fatalf("same-org reads produced %d audit records, want 0 — auditing every read would bury the impersonation signal: %+v", total, rows)
	}
	t.Log("same-org reads: 0 records (correct — not a request log)")
}
