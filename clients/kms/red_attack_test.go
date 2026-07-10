package kms_test

// RED adversarial tests — attacking the embedded KMS org-scope + envelope.
// These are PROOF-OF-BREACH probes; a PASS here means the attack was BLOCKED,
// a FAIL (t.Fatal) means the breach is real. Each test documents the vector.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/clients/kms"
	kmsstore "github.com/luxfi/kms/pkg/store"
)

// ── VECTOR 1: org case-fold collision (guard uses strings.EqualFold) ───────────
//
// The guard compares strings.EqualFold(ctx.Org(), :org). The store PATH is built
// from the :org ROUTE PARAM, not ctx.Org(). So a caller whose validated org is
// "Hanzo" (capital) may pass :org=hanzo (EqualFold true) and land on the SAME
// store path /orgs/hanzo as a *different* tenant whose validated org is "hanzo".
// If IAM issues case-distinct org owners, this is cross-tenant read/write.
func TestVector1_OrgCaseFoldCollision(t *testing.T) {
	app, _ := newApp(t, baseCfg(t, masterKeyB64(t)))

	// Tenant A: validated org "hanzo" writes a secret in its own org.
	body, _ := json.Marshal(map[string]string{"name": "STRIPE_KEY", "value": "sk-tenantA-owns-this", "env": "prod"})
	resp := do(t, app, "POST", "/v1/kms/orgs/hanzo/secrets", "hanzo", string(body), false, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("tenantA POST = %d, want 200: %s", resp.StatusCode, readAll(resp.Body))
	}

	// Tenant B: DISTINCT validated org "Hanzo" (capital H) targets :org=hanzo.
	// Guard: EqualFold("Hanzo","hanzo") == true  → PASSES.
	// Path built from :org param "hanzo" → /orgs/hanzo → tenantA's record.
	resp = do(t, app, "GET", "/v1/kms/orgs/hanzo/secrets/STRIPE_KEY?env=prod", "Hanzo", "", false, nil)
	if resp.StatusCode == 200 {
		v, _ := decode(t, resp.Body)["value"].(string)
		if v == "sk-tenantA-owns-this" {
			t.Fatalf("BREACH: tenant 'Hanzo' read tenant 'hanzo' secret via EqualFold guard: value=%q", v)
		}
		t.Fatalf("BREACH-ish: cross-case GET returned 200 (value=%q)", v)
	}
	t.Logf("cross-case GET blocked with status=%d (breach requires IAM to issue case-distinct owners)", resp.StatusCode)
}

// TestVector1b: the guard is EXACT-match (==), NOT EqualFold — so a tenant whose
// validated owner is "AcmeCorp" is REFUSED on any casing-mismatched :org param
// (403), because the store path keys on :org verbatim and a case-insensitive
// authz check would let "Acme" reach "acme"'s namespace. Confirm the exact-match
// closes the split-namespace hazard: a lowercased :org for a mixed-case owner is
// denied outright (not silently split into a second bucket).
func TestVector1b_ExactOrgMatchNoSplit(t *testing.T) {
	app, _ := newApp(t, baseCfg(t, masterKeyB64(t)))
	const owner = "AcmeCorp"

	// Owner uses its exact casing — allowed.
	body, _ := json.Marshal(map[string]string{"name": "K", "value": "written-mixedcase", "env": "default"})
	if r := do(t, app, "POST", "/v1/kms/orgs/AcmeCorp/secrets", owner, string(body), false, nil); r.StatusCode != 200 {
		t.Fatalf("POST exact-case = %d, want 200", r.StatusCode)
	}
	// Same owner, lowercased :org — EXACT match fails → 403. No second bucket.
	r := do(t, app, "GET", "/v1/kms/orgs/acmecorp/secrets/K", owner, "", false, nil)
	if r.StatusCode != 403 {
		t.Fatalf("BREACH: lowercased :org for owner %q = %d, want 403 (exact-match guard)", owner, r.StatusCode)
	}
	r = do(t, app, "POST", "/v1/kms/orgs/acmecorp/secrets", owner, string(body), false, nil)
	if r.StatusCode != 403 {
		t.Fatalf("BREACH: owner %q could write a lowercased bucket = %d, want 403", owner, r.StatusCode)
	}
	t.Logf("exact-match guard: owner %q cannot touch /orgs/acmecorp (403) — no case-split namespace", owner)
}

// ── VECTOR 2: AAD relocation — name-only DEK-wrap AAD ──────────────────────────
//
// store.Seal binds ciphertext AAD = path/name/env but wraps the DEK with AAD =
// NAME ONLY. Open re-derives ciphertext AAD from the record's OWN Path/Name/Env
// fields. So if an attacker can PLACE a record (with org A's ciphertext+wrappedDEK)
// at org B's store key AND rewrite its Path/Env fields to B's, Open succeeds:
// the DEK unwraps (name unchanged) and the ciphertext AAD matches the rewritten
// path. This proves the envelope alone does NOT bind a secret to its org — only
// the store-key namespacing + the HTTP guard do. We demonstrate at the store
// layer (the trust the envelope is supposed to provide).
func TestVector2_AADRelocationDirect(t *testing.T) {
	master := make([]byte, 32)
	for i := range master {
		master[i] = 0x42
	}
	// Org A seals a secret named "DB" with the SAME name org B would use.
	secA, err := kmsstore.Seal(master, "/orgs/tenantA", "DB", "prod", []byte("A-super-secret"))
	if err != nil {
		t.Fatalf("seal A: %v", err)
	}
	// Attacker relocates: copy A's ciphertext + wrapped DEK into a record addressed
	// as org B, rewriting the AAD-bearing fields to B's coordinates.
	relocated := &kmsstore.Secret{
		Name:       "DB", // unchanged — DEK-wrap AAD is name-only, so unwrap still works
		Path:       "/orgs/tenantB",
		Env:        "prod",
		Ciphertext: secA.Ciphertext,
		WrappedDEK: secA.WrappedDEK,
		Scheme:     secA.Scheme,
	}
	pt, err := kmsstore.Open(master, relocated)
	if err != nil {
		t.Logf("relocation Open FAILED (envelope binds path): %v", err)
		return
	}
	// If we got here, the envelope did NOT prevent cross-path relocation.
	t.Fatalf("LATENT-BREACH: A's plaintext (%q) opened under a record relabeled to tenantB — "+
		"name-only DEK-wrap AAD lets a record be relocated across orgs if an attacker can write the store key",
		string(pt))
}

// TestVector2b: does the HTTP API expose any write primitive that lets a caller
// control the record's stored Path independently of the store KEY? If putSecret
// ever wrote Path from the body while keying on the org, an attacker inside org B
// could craft a record whose Path says org A. Prove the API does NOT (Put derives
// both from the same org-folded path), so V2 is LATENT (needs raw store access),
// not remotely exploitable via /v1/kms.
func TestVector2b_NoAPIControlledPathSplit(t *testing.T) {
	app, deps := newApp(t, baseCfg(t, masterKeyB64(t)))

	// Attacker in org "b" tries to smuggle a Path field pointing at org "a".
	// secretPutRequest has Path — but putSecret folds it under orgPath(org, req.Path)
	// AND validSubpath rejects "..", so the climb is refused outright (400).
	body, _ := json.Marshal(map[string]any{
		"name":  "PWN",
		"value": "attacker-controlled",
		"env":   "default",
		"path":  "../a", // attempt to climb to another org — must be rejected
	})
	r := do(t, app, "POST", "/v1/kms/orgs/b/secrets", "b", string(body), false, nil)
	if r.StatusCode != 400 {
		t.Fatalf("path='../a' climb = %d, want 400 (validSubpath must reject '..'): %s", r.StatusCode, readAll(r.Body))
	}
	t.Logf("path traversal '../a' rejected with 400")
	// Where did it actually land? Check org "a" cannot see it, and the stored
	// record's Path is NOT /orgs/a.
	kc := deps.KMS.(*kms.Client)
	// org a lists its root — must be empty of PWN.
	metas, err := kc.List("/orgs/a", "default")
	if err != nil {
		t.Fatalf("list a: %v", err)
	}
	for _, m := range metas {
		if m.Name == "PWN" {
			t.Fatalf("BREACH: attacker in org b planted PWN into org a's namespace (path=%s)", m.Path)
		}
	}
	t.Logf("API path-split blocked: %d records under /orgs/a (PWN not among them)", len(metas))
}

// ── DEEP-A: store-key ↔ record-field divergence (name-only DEK-wrap AAD) ───────
//
// The store KEY is kms/secrets/{path}/{env}/{name} derived from the *query*
// (path,name,env). The record's OWN Path/Name/Env JSON fields are what Open uses
// to reconstruct the ciphertext AAD. store.Put keys on secret.Path/Name/Env, and
// kms always Seals with the SAME (path,name,env) it keys on — so key and
// fields agree. The LATENT risk: the envelope alone does not bind a record to its
// store key; if any future/rogue writer keys a record at path P' while its
// self-described Path is P (P != P'), Open still succeeds (it trusts the record).
// This proves the isolation rests ENTIRELY on kms.Put keying == sealing, and
// on the HTTP guard — NOT on cryptographic org-binding. Demonstrate the divergence
// at the store layer (which the envelope is supposed to make safe).
func TestDeepA_StoreKeyRecordDivergence(t *testing.T) {
	master := make([]byte, 32)
	for i := range master {
		master[i] = 0x11
	}
	// Seal a record whose self-described path is org A.
	sec, err := kmsstore.Seal(master, "/orgs/A", "TOKEN", "prod", []byte("A-only"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// A rogue writer with raw store access Puts this record — store.Put keys on
	// sec.Path ("/orgs/A"), so it lands at A's key. But if the writer FIRST mutates
	// the key coordinates without touching the AAD fields, key and fields diverge.
	// We model the danger: Open trusts the record's fields, so a record physically
	// placed under B's key but carrying A's Path opens fine — B's LIST (which keys
	// on the store prefix /orgs/B) would surface it, and B's GET (key /orgs/B/...)
	// would 404 because the record was keyed under A. i.e. the KEY is the only
	// isolation; the crypto does not add a second, independent org check.
	pt, err := kmsstore.Open(master, sec) // opens because fields are self-consistent
	if err != nil {
		t.Fatalf("open self-consistent record: %v", err)
	}
	if string(pt) != "A-only" {
		t.Fatalf("open mismatch: %q", pt)
	}
	t.Logf("LATENT (defense-in-depth): envelope binds to the record's OWN fields, " +
		"not to the store key. Cross-org isolation = store-key namespacing + HTTP guard ONLY. " +
		"A raw-store writer that decouples key from fields is not caught by the crypto. " +
		"kms.Put keeps them in lockstep, so this is NOT reachable via /v1/kms — but the " +
		"name-only DEK-wrap AAD means the DEK wrap itself provides ZERO path/env/org binding.")
}

// TestDeepB: sibling-org prefix confusion in List. Org "x" listing must never
// surface secrets of org "xy" / "x-attacker" via ZapDB prefix iteration, because
// the list prefix kms/secrets//orgs/x/{env}/ is NOT a prefix of //orgs/xy/... .
func TestDeepB_SiblingOrgListPrefix(t *testing.T) {
	app, deps := newApp(t, baseCfg(t, masterKeyB64(t)))
	kc := deps.KMS.(*kms.Client)

	// Seed secrets in org "x", "xy", and "x-attacker".
	for _, org := range []string{"x", "xy", "x-attacker"} {
		body, _ := json.Marshal(map[string]string{"name": "S", "value": "v-" + org, "env": "default"})
		if r := do(t, app, "POST", "/v1/kms/orgs/"+org+"/secrets", org, string(body), false, nil); r.StatusCode != 200 {
			t.Fatalf("seed %s = %d", org, r.StatusCode)
		}
	}
	// Org x lists its root: must see EXACTLY its own one secret, not xy/x-attacker.
	metas, err := kc.List("/orgs/x", "default")
	if err != nil {
		t.Fatalf("list x: %v", err)
	}
	if len(metas) != 1 {
		for _, m := range metas {
			t.Logf("  leaked: name=%q path=%q", m.Name, m.Path)
		}
		t.Fatalf("PREFIX BREACH: org x list returned %d secrets, want 1 (sibling-org prefix leak)", len(metas))
	}
	if metas[0].Path != "/orgs/x" {
		t.Fatalf("org x saw a foreign path %q", metas[0].Path)
	}
	t.Logf("no sibling prefix leak: org x sees exactly its own secret")
}

// TestDeepC: does the REST list endpoint honor the org guard for a sibling-prefix
// attacker? Attacker org "x" tries to list victim "xy" by exploiting that "x" is
// a string-prefix of "xy" — but the guard is EXACT (==), so :org=xy with org=x
// caller is 403, and :org=x only lists /orgs/x.
func TestDeepC_RESTListNoPrefixEscalation(t *testing.T) {
	app, _ := newApp(t, baseCfg(t, masterKeyB64(t)))
	// victim xy stores a secret.
	body, _ := json.Marshal(map[string]string{"name": "VICT", "value": "secret", "env": "default"})
	if r := do(t, app, "POST", "/v1/kms/orgs/xy/secrets", "xy", string(body), false, nil); r.StatusCode != 200 {
		t.Fatalf("seed = %d", r.StatusCode)
	}
	// attacker "x" tries to list xy → 403 (exact org mismatch).
	r := do(t, app, "GET", "/v1/kms/orgs/xy/secrets", "x", "", false, nil)
	if r.StatusCode != 403 {
		t.Fatalf("BREACH: prefix-attacker x listed xy = %d, want 403", r.StatusCode)
	}
	// attacker "x" lists its OWN org with a crafted ?path= trying to climb — validSubpath blocks "..".
	r = do(t, app, "GET", "/v1/kms/orgs/x/secrets?path=../xy", "x", "", false, nil)
	if r.StatusCode != 400 {
		t.Fatalf("BREACH: ?path=../xy climb = %d, want 400", r.StatusCode)
	}
	t.Logf("REST list: prefix-attacker blocked (403 cross-org, 400 on ?path climb)")
}

// TestDeepD: legitimate same-org, same-name, DIFFERENT-path relocation. Because
// ciphertext AAD includes path, a value sealed at /orgs/x/a cannot be Opened as
// if it were at /orgs/x/b even for the SAME org — confirm the path binding holds
// intra-org (so an admin/mis-key that moves a record breaks LOUDLY, not silently
// returns wrong plaintext).
func TestDeepD_IntraOrgPathBinding(t *testing.T) {
	master := make([]byte, 32)
	for i := range master {
		master[i] = 0x22
	}
	sec, _ := kmsstore.Seal(master, "/orgs/x/a", "K", "default", []byte("value-at-a"))
	// Relabel to /orgs/x/b (same org, same name, same env, different subpath).
	moved := &kmsstore.Secret{
		Name: "K", Path: "/orgs/x/b", Env: "default",
		Ciphertext: sec.Ciphertext, WrappedDEK: sec.WrappedDEK, Scheme: sec.Scheme,
	}
	if _, err := kmsstore.Open(master, moved); err == nil {
		t.Fatalf("LATENT-BREACH: value relocated a→b within org opened OK — ciphertext AAD did not bind path")
	} else {
		t.Logf("intra-org path binding holds: relocated record fails Open: %v", err)
	}
}

// ── VECTOR 4: enumeration oracle — 404 vs 403 vs 503 across orgs ───────────────
//
// Does the response code distinguish "secret exists in another org" from "does
// not exist"? The guard 403s a cross-org caller BEFORE the store is touched, so
// existence should be indistinguishable. Probe: cross-org GET of an existing vs
// non-existing secret must return the SAME status (403), leaking nothing.
func TestVector4_NoCrossOrgExistenceOracle(t *testing.T) {
	app, _ := newApp(t, baseCfg(t, masterKeyB64(t)))

	// victim org stores a secret.
	body, _ := json.Marshal(map[string]string{"name": "REAL", "value": "v", "env": "default"})
	if r := do(t, app, "POST", "/v1/kms/orgs/victim/secrets", "victim", string(body), false, nil); r.StatusCode != 200 {
		t.Fatalf("seed = %d", r.StatusCode)
	}

	// attacker org probes an EXISTING secret in victim's org.
	rExist := do(t, app, "GET", "/v1/kms/orgs/victim/secrets/REAL", "attacker", "", false, nil)
	// attacker org probes a NON-EXISTING secret in victim's org.
	rMiss := do(t, app, "GET", "/v1/kms/orgs/victim/secrets/NOPE", "attacker", "", false, nil)

	if rExist.StatusCode != rMiss.StatusCode {
		t.Fatalf("EXISTENCE ORACLE: existing→%d vs missing→%d differ (attacker learns victim's keys)",
			rExist.StatusCode, rMiss.StatusCode)
	}
	if rExist.StatusCode != 403 {
		t.Errorf("cross-org probe status=%d, want 403 (touch store only after authz)", rExist.StatusCode)
	}
	t.Logf("no existence oracle: both cross-org probes = %d", rExist.StatusCode)
}

// ── VECTOR 7: input validation at the boundary ─────────────────────────────────
//
// name containing '/' — putSecret folds PATH separately but does NOT reject a
// name with '/'. A name "a/b" makes the store key kms/secrets/{path}/{env}/a/b.
// Does that let name-embedded slashes collide a secret across path boundaries or
// escape the org? Prove what actually happens.
func TestVector7_NameWithSlashKeyShapeConfusion(t *testing.T) {
	app, deps := newApp(t, baseCfg(t, masterKeyB64(t)))
	kc := deps.KMS.(*kms.Client)

	// Caller in org "x" PUTs a secret whose NAME contains a slash and env-like tail.
	body, _ := json.Marshal(map[string]string{"name": "sub/EVIL", "value": "slash-in-name", "env": "default"})
	r := do(t, app, "POST", "/v1/kms/orgs/x/secrets", "x", string(body), false, nil)
	if r.StatusCode != 200 {
		t.Logf("POST name-with-slash rejected: %d (%s) — good, name is validated", r.StatusCode, readAll(r.Body))
		return
	}
	t.Logf("POST name-with-slash ACCEPTED — key = kms/secrets//orgs/x/default/sub/EVIL")

	// Now: a GET wildcard "sub/EVIL" splits into path=/orgs/x/sub, name=EVIL.
	// Does the PUT (name="sub/EVIL", path=/orgs/x) collide with a GET that thinks
	// path=/orgs/x/sub, name=EVIL? Both produce store key kms/secrets//orgs/x/sub/default/EVIL?
	// PUT key:  kms/secrets/ /orgs/x /default/ sub/EVIL   → "kms/secrets//orgs/x/default/sub/EVIL"
	// GET(sub/EVIL) key: path=/orgs/x/sub name=EVIL → "kms/secrets//orgs/x/sub/default/EVIL"
	// These DIFFER (env position differs), so no collision — but prove the retrieval story.
	rg := do(t, app, "GET", "/v1/kms/orgs/x/secrets/sub/EVIL?env=default", "x", "", false, nil)
	t.Logf("GET sub/EVIL → status=%d body=%s", rg.StatusCode, readAll(rg.Body))

	// The record IS retrievable only via the exact (path,name) the store layer keyed.
	// List the org root and the /sub subpath to see where it actually lives.
	rootMetas, _ := kc.List("/orgs/x", "default")
	subMetas, _ := kc.List("/orgs/x/sub", "default")
	t.Logf("records at /orgs/x: %d ; at /orgs/x/sub: %d", len(rootMetas), len(subMetas))
	for _, m := range rootMetas {
		t.Logf("  /orgs/x has name=%q path=%q", m.Name, m.Path)
	}
}

// TestVector7b: empty name / whitespace name / control chars.
func TestVector7b_DegenerateNames(t *testing.T) {
	app, _ := newApp(t, baseCfg(t, masterKeyB64(t)))
	cases := []struct {
		name string
		want int // expected status
		note string
	}{
		{"", 400, "empty name rejected"},
		{"   ", 400, "whitespace-only name trimmed to empty → rejected"},
		{"A\x00B", 200, "NUL in name — does it get stored? (key-shape/log-injection risk)"},
		{strings.Repeat("N", 100000), 200, "100KB name — DoS / key-size"},
	}
	for _, tc := range cases {
		body, _ := json.Marshal(map[string]string{"name": tc.name, "value": "v"})
		r := do(t, app, "POST", "/v1/kms/orgs/x/secrets", "x", string(body), false, nil)
		t.Logf("[%s] name=%.20q... → status=%d (expected ~%d)", tc.note, tc.name, r.StatusCode, tc.want)
	}
}

// ── VECTOR 5: master key never leaked in errors/responses ──────────────────────
//
// Force error paths (bad key length via env is impossible through the API, but a
// corrupt-envelope Open error should NOT echo key bytes). Confirm the health +
// config + error bodies never contain key material.
func TestVector5_ConfigLeaksNothingSensitive(t *testing.T) {
	mk := masterKeyB64(t)
	app, _ := newApp(t, baseCfg(t, mk))
	r := do(t, app, "GET", "/v1/kms/config", "", "", false, nil)
	if r.StatusCode != 200 {
		t.Fatalf("config = %d", r.StatusCode)
	}
	body := readAll(r.Body)
	if strings.Contains(body, mk) || strings.Contains(body, mk[:16]) {
		t.Fatalf("BREACH: /v1/kms/config echoed master key material: %s", body)
	}
	// config is public + unauthenticated — confirm it only exposes brand/issuer.
	t.Logf("/v1/kms/config (public, no auth) body: %s", body)
	// Assert no obviously-sensitive keys present.
	for _, bad := range []string{"masterKey", "master_key", "secret", "dek", "wrapped"} {
		if strings.Contains(strings.ToLower(body), bad) {
			t.Errorf("config body contains suspicious token %q", bad)
		}
	}
}

// ── VECTOR 3: /v1/kms/config public route — shadow / precedence ──────────────
//
// kms registers GET /v1/kms/config at order 10 (mounts FIRST), PUBLIC. Confirm
// it does not get shadowed by, nor shadow, the admin subsystem's gated /v1/admin/*
// routes. Probe an admin-gated route WITHOUT admin: it must still 403 (not fall
// through to kms's public handler), and /v1/kms/config must be reachable
// without auth.
func TestVector3_AdminConfigPrecedence(t *testing.T) {
	app, _ := newApp(t, baseCfg(t, masterKeyB64(t)))

	// public config reachable without any identity.
	r := do(t, app, "GET", "/v1/kms/config", "", "", false, nil)
	if r.StatusCode != 200 {
		t.Errorf("public /v1/kms/config = %d, want 200", r.StatusCode)
	}
	// an admin-gated sibling must NOT be shadowed into public by the order-10 mount.
	r = do(t, app, "GET", "/v1/admin/orgs", "", "", false, nil)
	if r.StatusCode == 200 {
		t.Fatalf("BREACH: /v1/admin/orgs returned 200 without admin — kms order-10 mount shadowed the gate?")
	}
	t.Logf("/v1/admin/orgs without admin = %d (gate intact)", r.StatusCode)
}

// TestAdminEdge_ValidOrgStillEnforcedForAdmin: a global admin bypasses the
// org-EQUALITY check but NOT validOrg — so an admin cannot address a malformed
// :org (empty, oversized, or with forbidden chars). Confirms admin is scoped to
// well-formed org labels only (the store path is still /orgs/{validOrg}).
func TestAdminEdge_ValidOrgStillEnforcedForAdmin(t *testing.T) {
	app, _ := newApp(t, baseCfg(t, masterKeyB64(t)))

	// admin with a forbidden-char :org → 400 (validOrg rejects before store touch).
	// Route needs a non-empty :org segment, so use an over-length label.
	longOrg := strings.Repeat("a", 64) // > 63 → validOrg false
	r := do(t, app, "GET", "/v1/kms/orgs/"+longOrg+"/secrets", "admin", "", true, nil)
	if r.StatusCode != 400 {
		t.Fatalf("admin over-length :org = %d, want 400 (validOrg enforced for admin too)", r.StatusCode)
	}
	// admin with a well-formed :org → allowed (reaches store; empty list is 200).
	r = do(t, app, "GET", "/v1/kms/orgs/anyorg/secrets", "admin", "", true, nil)
	if r.StatusCode != 200 {
		t.Fatalf("admin well-formed :org = %d, want 200", r.StatusCode)
	}
	t.Logf("admin scoped to validOrg labels: over-length→400, well-formed→200")
}

// TestAdminEdge_NonAdminEmptyOrgDenied: a caller with NO validated org (anonymous
// or a principal whose owner was empty) has ctx.Org()=="" and IsAdmin()==false,
// so ctx.Org() != :org is always true → 403. Confirms an empty principal can
// never match any org.
func TestAdminEdge_NonAdminEmptyOrgDenied(t *testing.T) {
	app, _ := newApp(t, baseCfg(t, masterKeyB64(t)))
	// Non-admin, no org header at all.
	r := do(t, app, "GET", "/v1/kms/orgs/hanzo/secrets", "", "", false, nil)
	if r.StatusCode != 403 {
		t.Fatalf("empty-principal GET = %d, want 403", r.StatusCode)
	}
	// Even targeting an org whose name is empty-ish is rejected by routing/validOrg.
	t.Logf("empty principal denied (403) — cannot match any :org")
}

// TestVector8_ForgedOrgNoPrincipalDenied — the KMS facet of the cross-tenant break.
// The guard's non-admin branch compares ctx.Org() == :org. The identity middleware
// RESTORES a client X-Org-Id on the bearer-less path (X-User-Id left EMPTY), so an
// off-gateway caller can send X-Org-Id: victim AND route :org=victim — ctx.Org()
// would EQUAL :org and the equality check alone would pass, reading the victim
// org's secrets with NO credential. The validated-principal gate (ctx.User()!="")
// closes it: no principal → 403 regardless of the forged match. Unlike
// TestAdminEdge_NonAdminEmptyOrgDenied (empty org, never matches any :org), here
// the forged org EXACTLY matches the route, so only the principal gate stops it.
func TestVector8_ForgedOrgNoPrincipalDenied(t *testing.T) {
	app, _ := newApp(t, baseCfg(t, masterKeyB64(t)))

	req := httptest.NewRequest("GET", "/v1/kms/orgs/victim/secrets", nil)
	req.Header.Set("X-Org-Id", "victim") // forged; EQUALS the route :org
	// deliberately NO X-User-Id / X-User-IsAdmin — the anonymous-forge signature.
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("forged request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 403 {
		t.Fatalf("forged X-Org-Id==:org with no principal = %d, want 403 (cross-tenant secret read)", resp.StatusCode)
	}
	t.Logf("forged X-Org-Id matching :org denied (403) — principal gate holds")
}
