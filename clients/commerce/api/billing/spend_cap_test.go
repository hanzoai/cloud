package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/spendalert"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// capCtx builds a gin context scoped to org `org` (name == namespace) — the exact
// plumbing the gateway/middleware injects, so the handlers resolve the ae SQLite
// datastore in that org's namespace.
func capCtx(w http.ResponseWriter, org *organization.Organization) *gin.Context {
	c, _ := gin.CreateTestContext(w)
	c.Set("organization", org)
	c.Set("context", nscontext.WithNamespace(context.Background(), org.Name))
	return c
}

// userCtx is capCtx as a VALIDATED IAM user with the given subject — the identity
// ownership derives from (never ?user=). subject "" means an unauthenticated
// caller (no validated claim).
func userCtx(w http.ResponseWriter, org *organization.Organization, subject string) *gin.Context {
	c := capCtx(w, org)
	claims := &auth.IAMClaims{}
	claims.Subject = subject // promoted from the embedded StandardClaims.
	c.Set("iam_claims", claims)
	return c
}

// createCap drives CreateSpendAlert as IAM user "owner" (so the row has a
// deterministic owner) and returns the status.
func createCap(t *testing.T, org *organization.Organization, body string) int {
	t.Helper()
	w := httptest.NewRecorder()
	c := userCtx(w, org, "owner")
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/billing/spend-alerts", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	CreateSpendAlert(c)
	if w.Code != 201 {
		t.Fatalf("CreateSpendAlert status = %d, body=%s", w.Code, w.Body.String())
	}
	return w.Code
}

// recordUsage drives RecordUsage with a JSON body and returns the status.
func recordUsage(t *testing.T, org *organization.Organization, body string) int {
	t.Helper()
	w := httptest.NewRecorder()
	c := capCtx(w, org)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/billing/usage", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	RecordUsage(c)
	return w.Code
}

// authorize drives AuthorizeSpendCap and returns the parsed verdict.
func authorize(t *testing.T, org *organization.Organization, query string) authorizeResult {
	t.Helper()
	w := httptest.NewRecorder()
	c := capCtx(w, org)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/billing/spend-alerts/authorize?"+query, nil)
	AuthorizeSpendCap(c)
	if w.Code != 200 {
		t.Fatalf("AuthorizeSpendCap status = %d, body=%s", w.Code, w.Body.String())
	}
	var res authorizeResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode authorize: %v (body=%s)", err, w.Body.String())
	}
	return res
}

// (i) HARD spend cap: a $1 enforced cap allows spend up to the cap, then DENIES
// the next request with reason spend_cap once period spend reaches the ceiling.
func TestSpendCap_HardEnforce_DeniesAtCap(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	org := &organization.Organization{}
	org.Name = "cap-hard"

	// $1.00 hard cap on the org-wide default scope.
	createCap(t, org, `{"title":"org cap","threshold":100,"enforce":true}`)

	// Before any spend, a $0.50 request is authorized.
	if v := authorize(t, org, "user=cap-hard&amount=50"); !v.Allow {
		t.Fatalf("pre-spend authorize denied: %+v", v)
	}

	// Spend the full $1.00.
	if code := recordUsage(t, org, `{"user":"cap-hard","amount":100,"requestId":"r1"}`); code != 201 {
		t.Fatalf("RecordUsage status = %d", code)
	}

	// The next request (any positive amount) must be DENIED — funded or not, the
	// tenant's own cap is reached.
	v := authorize(t, org, "user=cap-hard&amount=1")
	if v.Allow {
		t.Fatalf("post-cap authorize ALLOWED, want deny: %+v", v)
	}
	if v.Reason != "spend_cap" {
		t.Fatalf("reason = %q, want spend_cap", v.Reason)
	}
	if v.CapCents != 100 || v.SpentCents != 100 {
		t.Fatalf("cap/spent = %d/%d, want 100/100", v.CapCents, v.SpentCents)
	}
}

// (ii) SOFT warn: a request under the cap but at/over the soft threshold reports a
// warnPct (drives the X-Spend-Warn header at the edge) without denying.
func TestSpendCap_SoftWarn_ReportsWarnPct(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	org := &organization.Organization{}
	org.Name = "cap-warn"

	// $1.00 cap, soft threshold 50%. Enforce=true so it also warns before the cap.
	createCap(t, org, `{"title":"warn cap","threshold":100,"enforce":true,"softPct":50}`)

	// Spend $0.60 — over the 50% soft threshold, under the cap.
	if code := recordUsage(t, org, `{"user":"cap-warn","amount":60,"requestId":"w1"}`); code != 201 {
		t.Fatalf("RecordUsage status = %d", code)
	}

	v := authorize(t, org, "user=cap-warn&amount=1")
	if !v.Allow {
		t.Fatalf("authorize denied under cap: %+v", v)
	}
	if v.WarnPct < 60 {
		t.Fatalf("warnPct = %d, want >= 60 (0.60/1.00 utilization over the 50%% soft threshold)", v.WarnPct)
	}
}

// (iv) Tenant isolation: a cap on org A / project P gates ONLY A/P. A different
// project Q in the same org, and a different org B, are NOT gated by it.
func TestSpendCap_TenantIsolation(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	orgA := &organization.Organization{}
	orgA.Name = "iso-a"
	orgB := &organization.Organization{}
	orgB.Name = "iso-b"

	// A has a $1 hard cap on project "P"; A/P has already spent $1.
	createCap(t, orgA, `{"title":"A/P cap","threshold":100,"enforce":true,"project":"P"}`)
	if code := recordUsage(t, orgA, `{"user":"iso-a","amount":100,"requestId":"a1","project":"P"}`); code != 201 {
		t.Fatalf("RecordUsage A/P status = %d", code)
	}

	// pv=1 so the project cap hard-enforces (isolation under HARD enforcement).
	// A/P is over cap → denied.
	if v := authorize(t, orgA, "user=iso-a&project=P&amount=1&pv=1"); v.Allow {
		t.Fatalf("A/P authorize ALLOWED, want deny: %+v", v)
	}
	// A/Q (different project, no cap covering it) → allowed.
	if v := authorize(t, orgA, "user=iso-a&project=Q&amount=1&pv=1"); !v.Allow {
		t.Fatalf("A/Q authorize DENIED, want allow (project P's cap must not gate Q): %+v", v)
	}
	// B/P (different org, no rows at all) → allowed. A's cap must never gate B.
	if v := authorize(t, orgB, "user=iso-b&project=P&amount=1&pv=1"); !v.Allow {
		t.Fatalf("B/P authorize DENIED, want allow (org A's cap must not gate org B): %+v", v)
	}
}

// createCapAs drives CreateSpendAlert as IAM user `subject` and returns the row id.
func createCapAs(t *testing.T, org *organization.Organization, subject, body string) string {
	t.Helper()
	w := httptest.NewRecorder()
	c := userCtx(w, org, subject)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/billing/spend-alerts", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	CreateSpendAlert(c)
	if w.Code != 201 {
		t.Fatalf("CreateSpendAlert status = %d, body=%s", w.Code, w.Body.String())
	}
	var r struct {
		Id string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil || r.Id == "" {
		t.Fatalf("create id decode: %v (body=%s)", err, w.Body.String())
	}
	return r.Id
}

// patchCapAs / deleteCapAs drive the mutating verbs as IAM user `caller`, with an
// attacker-chosen ?user= query that MUST be ignored for ownership.
func patchCapAs(t *testing.T, org *organization.Organization, id, caller, userQuery, body string) int {
	t.Helper()
	w := httptest.NewRecorder()
	c := userCtx(w, org, caller)
	c.Params = gin.Params{{Key: "id", Value: id}}
	c.Request = httptest.NewRequest(http.MethodPatch, "/v1/billing/spend-alerts/"+id+"?user="+userQuery, bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	UpdateSpendAlert(c)
	return w.Code
}

func deleteCapAs(t *testing.T, org *organization.Organization, id, caller, userQuery string) int {
	t.Helper()
	w := httptest.NewRecorder()
	c := userCtx(w, org, caller)
	c.Params = gin.Params{{Key: "id", Value: id}}
	c.Request = httptest.NewRequest(http.MethodDelete, "/v1/billing/spend-alerts/"+id+"?user="+userQuery, nil)
	DeleteSpendAlert(c)
	return w.Code
}

// listCaps drives ListSpendAlerts for the org and returns the parsed rows — the
// reader path ScopeRules and the console Budgets page both use.
func listCaps(t *testing.T, org *organization.Organization) []map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	c := capCtx(w, org)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/billing/spend-alerts", nil)
	ListSpendAlerts(c)
	if w.Code != 200 {
		t.Fatalf("ListSpendAlerts = %d, body=%s", w.Code, w.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode list: %v (body=%s)", err, w.Body.String())
	}
	return rows
}

// Caps are ORG-LEVEL policy (the keying fix): any member of the OWNING org may
// manage them; a caller in a DIFFERENT org cannot reach them — org-namespace
// isolation refuses the id (404), and a forged ?user= is ignored (org comes from
// the validated X-Org-Id).
func TestSpendCap_MutateOrgScoped_CrossOrgRefused(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	orgA := &organization.Organization{}
	orgA.Name = "own-a"
	orgB := &organization.Organization{}
	orgB.Name = "own-b"

	id := createCapAs(t, orgA, "alice", `{"title":"org cap","threshold":100,"enforce":true}`)

	// A DIFFERENT org (B) cannot mutate org A's cap by id — even forging ?user=own-a.
	if code := patchCapAs(t, orgB, id, "mallory", "own-a", `{"threshold":1}`); code != 404 {
		t.Fatalf("cross-org PATCH = %d, want 404 (namespace isolation; ?user= ignored)", code)
	}
	if code := deleteCapAs(t, orgB, id, "mallory", "own-a"); code != 404 {
		t.Fatalf("cross-org DELETE = %d, want 404", code)
	}

	// Any member of org A (different user, same org) MAY manage the org-level cap.
	if code := patchCapAs(t, orgA, id, "bob", "", `{"threshold":200}`); code != 200 {
		t.Fatalf("same-org PATCH = %d, want 200 (org-level policy)", code)
	}
	if code := deleteCapAs(t, orgA, id, "bob", ""); code != 204 {
		t.Fatalf("same-org DELETE = %d, want 204", code)
	}
}

// THE bug the live Playwright e2e caught (integration: writer-key vs reader-key).
// The WRITER (console POST — an IAM user whose SUB differs from the org) and the
// READER (the gate, keyed per-ORG) MUST key the cap identically. The cap must be
// stored under the ORG so the org-keyed reader finds it and enforcement binds; a
// cap stored under the sub (the old bug) is invisible to the org reader → no 402,
// ScopeRules 0 rows → no 429. Prior tests used matching keys on both sides and
// missed this.
func TestSpendCap_WriterReaderKeyMatch(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	org := &organization.Organization{}
	org.Name = "hanzo"
	const sub = "2d4d67ab-1111-2222-3333-444455556666" // the IAM sub — DIFFERENT from the org.

	// WRITER: console POST as the IAM user (sub != org).
	createCapAs(t, org, sub, `{"title":"org cap","threshold":100,"enforce":true,"rateLimitRpm":5}`)

	// READER (ScopeRules / console list), keyed per-ORG → FINDS the cap under "hanzo",
	// stored under the ORG (not the sub), with the rpm ScopeRules needs.
	rows := listCaps(t, org)
	if len(rows) != 1 {
		t.Fatalf("org reader returned %d rows, want 1 (cap must be found under the org)", len(rows))
	}
	if rows[0]["userId"] != "hanzo" {
		t.Fatalf("stored userId = %v, want \"hanzo\" (the ORG, NOT the sub %q)", rows[0]["userId"], sub)
	}
	if rpm, _ := rows[0]["rateLimitRpm"].(float64); int(rpm) != 5 {
		t.Fatalf("rateLimitRpm = %v, want 5 (ScopeRules must see it → 429 binds)", rows[0]["rateLimitRpm"])
	}

	// READER (the gate authorize, user=org): spend > cap → 402 (enforcement binds).
	if code := recordUsage(t, org, `{"user":"hanzo","amount":100,"requestId":"wr1"}`); code != 201 {
		t.Fatalf("RecordUsage = %d", code)
	}
	v := authorize(t, org, "user=hanzo&amount=1")
	if v.Allow || v.Reason != "spend_cap" {
		t.Fatalf("gate authorize = %+v, want deny spend_cap (the cap MUST bind)", v)
	}

	// REGRESSION: a cap stored the OLD way (UserId = the sub) is INVISIBLE to the
	// org reader — exactly why enforcement failed before this fix.
	db := datastore.New(nscontext.WithNamespace(context.Background(), org.Name))
	legacy := spendalert.New(db)
	legacy.UserId = sub
	legacy.Threshold = 50
	legacy.RateLimitRpm = 9
	if err := legacy.Create(); err != nil {
		t.Fatalf("seed legacy sub-keyed row: %v", err)
	}
	for _, r := range listCaps(t, org) {
		if r["userId"] == sub {
			t.Fatalf("org reader returned a sub-keyed row %q — keying bug NOT fixed", sub)
		}
	}
}

// (HIGH-2) Project-scope hard caps DEGRADE to soft when the project axis is not
// validated (pv=0): still records + warns, but does NOT 402 — a forgeable
// X-Project-Id can neither hard-stop nor be evaded. With pv=1 (a future validated
// project claim) the SAME cap hard-denies. Org/service axes stay hard regardless.
func TestSpendCap_ProjectDegradesToSoftWhenUnvalidated(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	org := &organization.Organization{}
	org.Name = "pv-org"

	// $1 HARD cap on project P; spend $1 on P.
	createCap(t, org, `{"title":"P cap","threshold":100,"enforce":true,"project":"P"}`)
	if code := recordUsage(t, org, `{"user":"pv-org","amount":100,"requestId":"pv1","project":"P"}`); code != 201 {
		t.Fatalf("RecordUsage status = %d", code)
	}

	// pv=0 (today): project axis unvalidated → DEGRADE to soft → allow + warn.
	soft := authorize(t, org, "user=pv-org&project=P&amount=1&pv=0")
	if !soft.Allow {
		t.Fatalf("pv=0 authorize = %+v, want allow (project cap degrades to soft)", soft)
	}
	if soft.WarnPct < 100 {
		t.Fatalf("pv=0 warnPct = %d, want >=100 (still warns at the ceiling)", soft.WarnPct)
	}

	// pv=1 (validated project claim): the SAME cap hard-denies.
	hard := authorize(t, org, "user=pv-org&project=P&amount=1&pv=1")
	if hard.Allow || hard.Reason != "spend_cap" {
		t.Fatalf("pv=1 authorize = %+v, want deny spend_cap (validated project hardens)", hard)
	}
}

// (v) Idempotency: recording the SAME requestId twice creates AT MOST ONE debit,
// so period spend (and thus the cap verdict) counts it once — no double count.
func TestSpendCap_Idempotent_NoDoubleCount(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	org := &organization.Organization{}
	org.Name = "cap-idem"

	body := `{"user":"cap-idem","amount":100,"requestId":"dup-1"}`
	if code := recordUsage(t, org, body); code != 201 {
		t.Fatalf("RecordUsage #1 status = %d", code)
	}
	// A retry of the same requestId must replay, not double-debit.
	if code := recordUsage(t, org, body); code != 200 && code != 201 {
		t.Fatalf("RecordUsage #2 (replay) status = %d", code)
	}

	db := datastore.New(nscontext.WithNamespace(context.Background(), org.Name))
	spent, err := scopeSpentCents(db, org.TestMode(), "", "")
	if err != nil {
		t.Fatalf("scopeSpentCents: %v", err)
	}
	if spent != 100 {
		t.Fatalf("period spent = %d, want 100 (a duplicate requestId must not double-count)", spent)
	}
}
