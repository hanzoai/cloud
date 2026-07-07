package cloud

// Integration tests for the audit middleware. They drive REAL requests through
// the zip/fiber stack (app.Fiber().Test) with the audit middleware in front of a
// handler, backed by a REAL on-disk audit store, then read the store back to
// assert what was (and was not) recorded. No mocks — the whole capture path runs.
//
// The middleware trusts SanitizeIdentity to have already validated identity, so
// these tests set the sanitized X-User-* headers directly (as SanitizeIdentity
// would after verifying a JWT) — that is the contract boundary under test here.
// The forgery/bypass properties of SanitizeIdentity itself are proven in
// middleware_identity_test.go; here we prove the middleware records the VALIDATED
// identity and the correct outcome for every security-relevant request.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/audit"
	"github.com/zap-proto/zip"
)

// newAuditApp wires a zip app with the audit middleware in front of a small set
// of routes covering the coverage matrix: a mutating POST, a safe GET, an
// admin-gated route that 403s, and a route that echoes a (secret-bearing) body
// so we can prove the body never reaches a record.
func newAuditApp(t *testing.T) (*zip.App, *audit.Recorder) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	rec, err := audit.Open(path, nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })

	app := zip.New(zip.Config{})
	app.Use(AuditTrail(rec))

	// Mutating route — must be audited.
	app.Post("/v1/kms/secrets", func(c *zip.Ctx) error {
		return c.JSON(http.StatusCreated, map[string]string{"id": "sec_1"})
	})
	// Safe read — must NOT be audited (not a mutation, not admin, not a denial).
	app.Get("/v1/pricing/models", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})
	// Admin route that denies (as the real admin guard would) — the 403 is a
	// security event and MUST be audited even though it is a GET.
	app.Get("/v1/admin/orgs", func(c *zip.Ctx) error {
		if !c.IsAdmin() {
			return zip.ErrForbidden("global admin required")
		}
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})
	// A mutation whose request body carries secrets — used to prove the body is
	// never captured. The handler ignores the body; the point is what the
	// middleware records (metadata only).
	app.Post("/v1/iam/users", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})
	return app, rec
}

// asAdmin sets the sanitized identity headers a VALIDATED global admin would
// carry after SanitizeIdentity (X-User-IsAdmin=true, org=admin).
func asAdmin(req *http.Request) {
	req.Header.Set("X-User-Id", "z@hanzo.ai")
	req.Header.Set("X-User-Email", "z@hanzo.ai")
	req.Header.Set("X-Org-Id", "admin")
	req.Header.Set("X-User-IsAdmin", "true")
	req.Header.Set("Authorization", "Bearer eyJ.validated.jwt") // shape only; classifies as jwt
}

// asUser sets sanitized headers for a normal (non-admin) validated principal.
func asUser(req *http.Request) {
	req.Header.Set("X-User-Id", "alice")
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("Authorization", "Bearer eyJ.validated.jwt")
}

func mustTest(t *testing.T, app *zip.App, req *http.Request) *http.Response {
	t.Helper()
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", req.Method, req.URL.Path, err)
	}
	return resp
}

// TestAudit_RecordsMutation proves a mutating request is captured with the
// correct validated actor, action, resource, outcome, and auth context — and the
// record is hash-chained (has a hash) and verifies.
func TestAudit_RecordsMutation(t *testing.T) {
	app, rec := newAuditApp(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/kms/secrets", nil)
	asUser(req)
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	req.Header.Set("User-Agent", "test-agent/1.0")
	resp := mustTest(t, app, req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	rows, total, err := rec.Query(t.Context(), audit.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("recorded %d events, want exactly 1", total)
	}
	r := rows[0]
	if r.Actor.Org != "acme" || r.Actor.Sub != "alice" {
		t.Errorf("actor = %+v, want org=acme sub=alice (the VALIDATED identity)", r.Actor)
	}
	if r.Method != "POST" || r.Path != "/v1/kms/secrets" {
		t.Errorf("method/path = %s %s, want POST /v1/kms/secrets", r.Method, r.Path)
	}
	if r.Resource.Type != "secrets" {
		t.Errorf("resource type = %q, want secrets", r.Resource.Type)
	}
	if r.Outcome.Result != "success" || r.Outcome.Status != 201 {
		t.Errorf("outcome = %+v, want success/201", r.Outcome)
	}
	if r.Auth.Method != "jwt" {
		t.Errorf("auth method = %q, want jwt", r.Auth.Method)
	}
	if r.SourceIP != "203.0.113.9" {
		t.Errorf("source ip = %q, want the left-most XFF entry", r.SourceIP)
	}
	if r.UserAgent != "test-agent/1.0" {
		t.Errorf("user agent = %q, want test-agent/1.0", r.UserAgent)
	}
	if r.Hash == "" {
		t.Error("record has no hash — not chained")
	}
	if iv, _ := rec.Verify(t.Context()); !iv.OK {
		t.Errorf("chain broke after one record at %d (%s)", iv.BrokenAt, iv.Reason)
	}
}

// TestAudit_RecordsDenial proves a 403 (access-control denial) is audited even on
// a GET, with outcome result="deny", and records the actor who was denied.
func TestAudit_RecordsDenial(t *testing.T) {
	app, rec := newAuditApp(t)

	// A NON-admin hits the admin route → 403.
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/orgs", nil)
	asUser(req) // not admin
	resp := mustTest(t, app, req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}

	rows, total, err := rec.Query(t.Context(), audit.Filter{Result: "deny"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != 1 {
		t.Fatalf("recorded %d denials, want 1", total)
	}
	r := rows[0]
	if r.Outcome.Result != "deny" || r.Outcome.Status != 403 {
		t.Errorf("outcome = %+v, want deny/403", r.Outcome)
	}
	if r.Actor.Sub != "alice" {
		t.Errorf("denied actor sub = %q, want alice", r.Actor.Sub)
	}
	if r.Auth.IsAdmin {
		t.Error("denied non-admin recorded as admin")
	}
}

// TestAudit_SkipsSafeReads proves an ordinary successful GET on a non-admin route
// is NOT audited — the trail captures security events, not read-log noise.
func TestAudit_SkipsSafeReads(t *testing.T) {
	app, rec := newAuditApp(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/pricing/models", nil)
	asUser(req)
	resp := mustTest(t, app, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	_, total, err := rec.Query(t.Context(), audit.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != 0 {
		t.Fatalf("a safe GET was audited (%d rows) — trail should skip non-security reads", total)
	}
}

// TestAudit_AdminReadIsAudited proves a SUCCESSFUL admin read is audited (admin
// access itself is an AC-relevant event), distinguishing it from a normal read.
func TestAudit_AdminReadIsAudited(t *testing.T) {
	app, rec := newAuditApp(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/orgs", nil)
	asAdmin(req)
	resp := mustTest(t, app, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	rows, total, err := rec.Query(t.Context(), audit.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != 1 {
		t.Fatalf("admin read recorded %d, want 1", total)
	}
	if !rows[0].Auth.IsAdmin || rows[0].Outcome.Result != "success" {
		t.Errorf("admin read record = auth.isAdmin=%v outcome=%+v, want admin+success", rows[0].Auth.IsAdmin, rows[0].Outcome)
	}
}

// TestAudit_NeverCapturesRequestBody is the secret-safety proof: a mutation whose
// body is FULL of credentials is audited, but the stored record contains NONE of
// the body — the middleware captures metadata only, so a secret in a body can
// never leak into the trail. This is the "reuse RedactUserSecrets" guarantee at
// its strongest: the code that could leak a secret never reads it.
func TestAudit_NeverCapturesRequestBody(t *testing.T) {
	app, rec := newAuditApp(t)

	secretBody := `{"username":"bob","password":"hunter2","apiKey":"sk-live-DEADBEEF","token":"ghp_SECRET"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/iam/users", strings.NewReader(secretBody))
	req.Header.Set("Content-Type", "application/json")
	asAdmin(req)
	// Also stuff a secret into a header value that is NOT an identity header — it
	// must not be captured either (we only record User-Agent + XFF, never arbitrary
	// headers, and never Authorization's value).
	req.Header.Set("Authorization", "Bearer eyJsuper.secret.token.value")
	resp := mustTest(t, app, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	rows, total, err := rec.Query(t.Context(), audit.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != 1 {
		t.Fatalf("recorded %d, want 1", total)
	}
	// Serialize the WHOLE record and scan for any secret substring.
	blob, _ := json.Marshal(rows[0])
	for _, secret := range []string{"hunter2", "sk-live-DEADBEEF", "ghp_SECRET", "super.secret.token.value"} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("SECRET LEAKED into audit record: %q found in %s", secret, blob)
		}
	}
	// But the metadata IS there: the auth method is classified without the token.
	if rows[0].Auth.Method != "jwt" {
		t.Errorf("auth method = %q, want jwt (classified from prefix, token not stored)", rows[0].Auth.Method)
	}
	if rows[0].Before != nil || rows[0].After != nil {
		t.Errorf("middleware set before/after (%s / %s) — it must never read bodies", rows[0].Before, rows[0].After)
	}
}

// TestAudit_ScrubsCredentialInPath proves a credential-shaped token that rides in
// the URL PATH (e.g. a KMS route where a caller wrongly puts an sk-/hk- key in the
// path) is never recorded verbatim in either Path or resource.ID — defense in
// depth beyond "bodies are never read". A normal identifier is untouched.
func TestAudit_ScrubsCredentialInPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	rec, err := audit.Open(path, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	app := zip.New(zip.Config{})
	app.Use(AuditTrail(rec))
	app.Delete("/v1/kms/secrets/*", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})

	// Three credential shapes smuggled into the path across three requests: a
	// prefixed key, a raw high-entropy hex secret (NO telltale prefix), and a JWT.
	paths := []struct{ path, secret string }{
		{"/v1/kms/secrets/sk-live-SUPERSECRETKEY1234567890", "SUPERSECRETKEY"},
		{"/v1/kms/secrets/deadbeefcafe0123456789abcdef0123456789abcdef0123", "deadbeefcafe0123"},
		{"/v1/kms/secrets/eyJhbGciOiJIUzI1NiJ9.cGF5bG9hZA.c2ln", "eyJhbGciOiJIUzI1NiJ9"},
	}
	for _, tc := range paths {
		req := httptest.NewRequest(http.MethodDelete, tc.path, nil)
		asAdmin(req)
		if resp := mustTest(t, app, req); resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", tc.path, resp.StatusCode)
		}
	}

	rows, total, err := rec.Query(t.Context(), audit.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != len(paths) {
		t.Fatalf("recorded %d, want %d", total, len(paths))
	}
	blob, _ := json.Marshal(rows)
	for _, tc := range paths {
		if strings.Contains(string(blob), tc.secret) {
			t.Fatalf("credential in path leaked into record: %q present in %s", tc.secret, blob)
		}
	}
	for _, r := range rows {
		if !strings.Contains(r.Path, "[REDACTED-TOKEN]") {
			t.Errorf("path token not scrubbed: %q", r.Path)
		}
	}
}

// TestAudit_ScrubsSecretInUserAgent proves a bearer/API-key embedded in the
// client-controlled User-Agent is scrubbed, and a normal UA is untouched.
func TestAudit_ScrubsSecretInUserAgent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	rec, err := audit.Open(path, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	app := zip.New(zip.Config{})
	app.Use(AuditTrail(rec))
	app.Post("/v1/kms/secrets", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]string{"ok": "1"}) })

	req := httptest.NewRequest(http.MethodPost, "/v1/kms/secrets", nil)
	asAdmin(req)
	req.Header.Set("User-Agent", "myclient/1.0 Bearer eyJhbGciOiJI.pay.sig key=sk-live-LEAKME99999")
	mustTest(t, app, req)

	rows, _, _ := rec.Query(t.Context(), audit.Filter{})
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	for _, secret := range []string{"eyJhbGciOiJI", "sk-live-LEAKME99999"} {
		if strings.Contains(rows[0].UserAgent, secret) {
			t.Errorf("UA secret leaked: %q in %q", secret, rows[0].UserAgent)
		}
	}
	// The non-secret UA prefix survives (audit usefulness preserved).
	if !strings.Contains(rows[0].UserAgent, "myclient/1.0") {
		t.Errorf("UA over-scrubbed, lost the client name: %q", rows[0].UserAgent)
	}
}

// TestScrubToken_NoFalsePositives proves legitimate identifiers are NEVER
// scrubbed — the guard fires only on genuinely secret-shaped segments, so the
// audit trail keeps its query precision for normal resource ids.
func TestScrubToken_NoFalsePositives(t *testing.T) {
	for _, id := range []string{
		"acme-corp", "gpt-4o-mini", "my_project_123", "user@example.com",
		"550e8400-e29b-41d4-a716-446655440000", // uuid (hyphens)
		"claude-opus-4-20250514", "text-embedding-3-large",
		"my-cool-project-name", "feature-branch-xyz",
		"deployment-2024-01-15", "report.pdf", "data.json",
		// Red re-review round 2 — hyphenated model ids MUST pass (AU-3: an auditor
		// must still see WHICH model a config change touched).
		"claude-3-5-sonnet-20241022", "claude-3-5-haiku-20241022",
		"claude-sonnet-4-20250514", "claude-3-7-sonnet-20250219",
		"claude-3-5-sonnet-latest", "deepseek-r1-distill-qwen-32b",
		"claude-3-opus-20240229", "stable-diffusion-xl-base",
		"mixtral-8x7b-instruct", "llama-3-1-8b-instruct", "whisper-large-v3-turbo",
		"v1", "models", "sync", "12345", "a", "",
	} {
		if got := scrubToken(id); got != id {
			t.Errorf("false scrub: legit id %q → %q", id, got)
		}
	}
	// And genuine secrets ARE scrubbed.
	for _, sec := range []string{
		"sk-live-abcdef", "hk-1234567890abcdef", "eyJhbG.payload.signature",
		"deadbeefcafe0123456789abcdef0123456789abcdef0123", // 48-char raw hex
	} {
		if scrubToken(sec) == sec {
			t.Errorf("missed secret: %q not scrubbed", sec)
		}
	}
}

// TestScrubToken_RedReviewBypassClasses is the regression for the 4 scrub-bypass
// classes Red found: base64url with -/_, all-alpha opaque >=len, percent-encoded
// prefixes, and delimiter-glued UA tokens. Each MUST now be redacted.
func TestScrubToken_RedReviewBypassClasses(t *testing.T) {
	for _, sec := range []string{
		"AbCdEf-GhIjKl_MnOpQrStUvWxYz012345",       // base64url with - and _
		"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN", // all-alpha opaque, no digit
		"hk%2DROTATEKEY0001SECRETKEY",              // percent-encoded hk-
		"sk%5Flive%5FBYPASS0001SECRETKEY",          // percent-encoded sk_
		// Red re-review round 2 — dotted-exemption + encoding + standard-base64:
		"deadbeefcafe0123456789abcdef0123456789abcdef.x", // ".x" tail forces dotted exemption
		"AbCdEfGhIjKlMnOpQrStUvWxYz012345.json",          // secret with a filename-ish suffix
		"hk%252DROTATEKEY0001SECRETKEY",                  // double percent-encoded hk-
		"AbCdEfGhIjKlMnOpQrStUvWx0123456789",             // 34-char opaque run (standard/url b64)
		// Red re-review round 3 — interior-hyphen raw secret (NOT a structured id:
		// only 1 hyphen, long parts) must still redact despite '-' in the run.
		"aaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbbbbbbbbbb",
		// Red final re-review — a secret CHUNKED to satisfy the structured-id shape
		// (>=3 groups <=12 chars) must STILL redact: the lexical (word-like) test
		// rejects mixed-case base64 chunks and hex chunks (>= hexChunkMinLen=4).
		"AbCdEfGhIjKl-MnOpQrStUvWx-YzAbCdEfGhIj", // mixed-case base64 chunks
		"A1b2C3d4-E5f6G7h8-I9j0K1l2",             // 128-bit mixed-case chunks
		"deadbeef-cafebabe-01234567-89abcdef",    // all-hex chunks (8-char groups)
		"abcdef01-23456789-abcdef01",             // hex chunks
		// Red final polish — SMALL hex chunks (md5/sha "xxxx-xxxx" display) now
		// caught by the len>=4 hex rule.
		"abcd-ef01-2345-6789-abcd-ef01", // 4-char hex groups
		"dead-beef-cafe-babe-0123-4567", // 4-char hex groups
	} {
		if got := scrubToken(sec); got != "[REDACTED-TOKEN]" {
			t.Errorf("Red bypass STILL OPEN: %q → %q (want redacted)", sec, got)
		}
	}
	// UA with a secret glued by :/()[]= — must be scrubbed, client name kept.
	ua := scrubFreeText("myapp/1.0 (token:sk-live-BYPASS0001) [key=hk-1234567890abcdef]")
	for _, leak := range []string{"sk-live-BYPASS0001", "hk-1234567890abcdef"} {
		if strings.Contains(ua, leak) {
			t.Errorf("UA bypass: %q leaked in %q", leak, ua)
		}
	}
	if !strings.Contains(ua, "myapp/1.0") {
		t.Errorf("UA over-scrubbed, lost client name: %q", ua)
	}
	// Red re-review round 2 — a key glued by . @ # ~ must NOT survive.
	for _, glued := range []string{
		"client@sk-live-SECRETKEY00001", "app.sk-live-SECRETKEY00001",
		"build#hk-SECRETKEY000000001", "v1~sk-live-SECRETKEY00001",
	} {
		if got := scrubFreeText(glued); strings.Contains(got, "SECRETKEY") {
			t.Errorf("UA glue-char bypass: %q → %q (secret survives)", glued, got)
		}
	}
	// Normal UAs must be byte-identical (no false scrub).
	for _, ua := range []string{
		"console", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)",
		"curl/8.1.2", "Go-http-client/2.0",
	} {
		if got := scrubFreeText(ua); got != ua {
			t.Errorf("UA false positive: %q → %q", ua, got)
		}
	}
}

// TestAudit_HealthSuffixCannotEvadeAudit proves a mutating request whose path
// ENDS in /health (an attacker-named wildcard segment) is STILL audited — the
// liveness exemption is exact and never suppresses a mutation or a denial. This
// closes an audit-evasion hole where POST /v1/admin/orgs/x/health would slip past.
func TestAudit_HealthSuffixCannotEvadeAudit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	rec, err := audit.Open(path, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	app := zip.New(zip.Config{})
	app.Use(AuditTrail(rec))
	app.Post("/v1/admin/orgs/*", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]string{"ok": "1"}) })
	app.Get("/v1/kms/health", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]string{"status": "ok"}) })

	// (a) A mutating POST ending in /health MUST be audited.
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/orgs/evil/health", nil)
	asAdmin(req)
	mustTest(t, app, req)
	_, total, _ := rec.Query(t.Context(), audit.Filter{})
	if total != 1 {
		t.Fatalf("EVASION: mutating POST ending /health audited %d times, want 1", total)
	}

	// (b) A genuine liveness GET /v1/kms/health MUST still be skipped.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/kms/health", nil)
	mustTest(t, app, req2)
	_, total2, _ := rec.Query(t.Context(), audit.Filter{})
	if total2 != 1 {
		t.Fatalf("liveness probe was audited (total went %d→%d) — should be exempt", total, total2)
	}
}

// TestAudit_AnonRequestNotAttributedToForgedOrg proves an UNAUTHENTICATED
// attacker cannot forge a false attribution: sending X-Org-Id/X-User-Id/
// X-User-IsAdmin with no validated principal records an ANONYMOUS actor (empty
// org+sub, not admin), never the claimed victim org. Runs the REAL SanitizeIdentity
// (nil validator ⇒ strips authority, restores client X-Org-Id for the data path)
// ahead of AuditTrail, exactly as serve.go wires them.
func TestAudit_AnonRequestNotAttributedToForgedOrg(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	rec, err := audit.Open(path, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(nil, "admin")) // trust boundary
	app.Use(AuditTrail(rec))
	app.Post("/v1/kms/secrets", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]string{"ok": "1"}) })

	req := httptest.NewRequest(http.MethodPost, "/v1/kms/secrets", nil)
	// Anonymous attacker forging every identity header.
	req.Header.Set("X-Org-Id", "victim-org")
	req.Header.Set("X-User-Id", "victim-user")
	req.Header.Set("X-User-IsAdmin", "true")
	mustTest(t, app, req)

	rows, _, _ := rec.Query(t.Context(), audit.Filter{})
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	r := rows[0]
	if r.Auth.IsAdmin {
		t.Error("forged X-User-IsAdmin survived into the record")
	}
	if r.Actor.Sub != "" {
		t.Errorf("forged X-User-Id recorded as actor.Sub = %q", r.Actor.Sub)
	}
	if r.Actor.Org == "victim-org" {
		t.Errorf("FALSE ATTRIBUTION: anonymous request stamped with claimed org %q", r.Actor.Org)
	}
	if r.Auth.Method != "none" {
		t.Errorf("auth method = %q, want none (no valid credential)", r.Auth.Method)
	}
	// The event is still recorded (a mutation), honestly anonymous.
	if r.Outcome.Result != "success" {
		t.Errorf("outcome = %+v, want the anonymous mutation recorded", r.Outcome)
	}
}

// TestAudit_NoopWhenUnconfigured proves a nil Recorder makes the middleware a
// pass-through (an unconfigured deployment is never blocked), exactly like
// BillingGate's nil-client behavior.
func TestAudit_NoopWhenUnconfigured(t *testing.T) {
	app := zip.New(zip.Config{})
	app.Use(AuditTrail(nil))
	var ran bool
	app.Post("/v1/kms/secrets", func(c *zip.Ctx) error {
		ran = true
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/kms/secrets", nil)
	resp := mustTest(t, app, req)
	if resp.StatusCode != http.StatusOK || !ran {
		t.Fatalf("nil-recorder gate must pass through: status=%d ran=%v", resp.StatusCode, ran)
	}
}

// TestAudit_FailsClosedOnWriteError proves that when the audit store cannot
// record a security-relevant event, the request is failed CLOSED (503) rather
// than allowed to succeed unlogged (AU-5). We force the failure by closing the
// store's DB before the request, so Append errors.
func TestAudit_FailsClosedOnWriteError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	rec, err := audit.Open(path, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Close the underlying store so every subsequent Append fails.
	_ = rec.Close()

	app := zip.New(zip.Config{})
	app.Use(AuditTrail(rec))
	var ran bool
	app.Post("/v1/kms/secrets", func(c *zip.Ctx) error {
		ran = true
		return c.JSON(http.StatusCreated, map[string]string{"id": "x"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/kms/secrets", nil)
	asAdmin(req)
	resp := mustTest(t, app, req)
	// The handler may have run (audit wraps AFTER the chain), but the response the
	// CLIENT sees must be the fail-closed 503, not the handler's 201 — a
	// security-relevant action that could not be recorded is not acknowledged as
	// success.
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (fail-closed when audit write fails)", resp.StatusCode)
	}
	_ = ran
}
