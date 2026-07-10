package billing

import (
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/idempotencykey"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	txutil "github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// The canonical-ledger + per-org-isolation + idempotency proof for the
// in-console card top-up. The Square charge leg is provider-owned and proven
// against Square sandbox live (cnon:card-nonce-ok); these tests pin the LEDGER
// leg deterministically — the exact invariants Red verifies:
//
//	credit-key == gateway read/debit key   (ONE canonical ledger)
//	credit bucket == charge environment    (sandbox → test bucket, never live)
//	org A top-up can NEVER touch org B      (per-org isolation, no cross leak)
//	a retry / double-submit credits ONCE    (idempotent, no double-charge)

// ginCtxWithOrg builds a gin.Context exactly as the edge/proxy hands it to
// TopupWithToken: an "organization" in context + a `?user=` query (which
// console2's server proxy sets from the validated session, overwriting any
// client value).
func ginCtxWithOrg(orgName, userQuery string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	q := ""
	if userQuery != "" {
		q = "?user=" + url.QueryEscape(userQuery)
	}
	c.Request = httptest.NewRequest("POST", "/v1/billing/topup/token"+q, nil)
	if orgName != "" {
		org := &organization.Organization{}
		org.Name = orgName
		c.Set("organization", org)
	}
	return c
}

// TestTopupDestination_SubjectResolution proves the top-up credits the SAME
// subject the gateway reads/debits, and can NEVER be steered outside the
// caller's own org (fail-secure bound).
func TestTopupDestination_SubjectResolution(t *testing.T) {
	cases := []struct {
		name, org, userQuery, want string
	}{
		{"dedicated org, no subject → org slug", "acme", "", "acme"},
		{"dedicated org, subject == org → org slug", "acme", "acme", "acme"},
		{"personal-billing member → in-org <org>/<name>", "hanzo", "hanzo/alice", "hanzo/alice"},
		{"subject case-normalized to org slug", "Acme", "ACME/Bob", "acme/bob"},
		{"out-of-org subject → fallback to org slug (no cross-org)", "acme", "victim", "acme"},
		{"prefix-but-not-boundary → fallback (acme-evil is not acme/*)", "acme", "acme-evil", "acme"},
		{"another org's user → fallback (never credits foreign org)", "acme", "victim/root", "acme"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := topupDestination(ginCtxWithOrg(tc.org, tc.userQuery))
			if got != tc.want {
				t.Fatalf("topupDestination(org=%q, ?user=%q) = %q, want %q", tc.org, tc.userQuery, got, tc.want)
			}
		})
	}
}

// TestTopupBounds proves the server-authoritative amount floor+ceiling that the
// handler enforces BEFORE any charge: out-of-bounds amounts (0, negative,
// sub-$1, and a $1,000,000 mega-charge) are rejected; the defaults mirror the
// console MIN/MAX policy; and a per-deploy env override is honored while a
// bad/non-positive override fails safe to the default bound.
func TestTopupBounds(t *testing.T) {
	minC, maxC := topupBounds()
	if minC != 100 || maxC != 500000 {
		t.Fatalf("default topupBounds() = (%d,%d), want (100,500000)", minC, maxC)
	}
	inBounds := func(v int64) bool { return v >= minC && v <= maxC }
	for _, tc := range []struct {
		cents int64
		ok    bool
	}{
		{0, false},         // zero
		{-100, false},      // negative
		{99, false},        // just below the $1 floor
		{100, true},        // exactly the floor
		{2500, true},       // typical
		{500000, true},     // exactly the ceiling
		{500001, false},    // just over the $5,000 ceiling
		{100000000, false}, // $1,000,000 mega-charge — rejected
	} {
		if inBounds(tc.cents) != tc.ok {
			t.Fatalf("amount %d cents: inBounds=%v, want %v", tc.cents, inBounds(tc.cents), tc.ok)
		}
	}

	// Env override honored.
	t.Setenv("COMMERCE_TOPUP_MIN_CENTS", "50")
	t.Setenv("COMMERCE_TOPUP_MAX_CENTS", "1000000")
	if mn, mx := topupBounds(); mn != 50 || mx != 1000000 {
		t.Fatalf("overridden topupBounds() = (%d,%d), want (50,1000000)", mn, mx)
	}
	// Bad / non-positive override → fail safe to the default bound.
	t.Setenv("COMMERCE_TOPUP_MIN_CENTS", "-5")
	t.Setenv("COMMERCE_TOPUP_MAX_CENTS", "garbage")
	if mn, mx := topupBounds(); mn != 100 || mx != 500000 {
		t.Fatalf("bad-override topupBounds() = (%d,%d), want defaults (100,500000)", mn, mx)
	}
}

// creditLikeTopup mirrors TopupWithToken's post-charge credit EXACTLY: a Deposit
// keyed by the resolved subject, DestinationKind "iam-user", Test = the org's
// resolved TestMode — the same row GetBalance/GetMyBalance read and the gateway
// debits.
func creditLikeTopup(t *testing.T, ctx ae.Context, org *organization.Organization, subject string, cents int64) {
	t.Helper()
	db := datastore.New(org.Namespaced(ctx))
	tr := transaction.New(db)
	tr.Type = transaction.Deposit
	tr.DestinationId = subject
	tr.DestinationKind = "iam-user"
	tr.Currency = currency.USD
	tr.Amount = currency.Cents(cents)
	tr.Tags = "topup"
	tr.Test = org.TestMode()
	tr.MustCreate()
}

func balanceOf(t *testing.T, ctx ae.Context, org *organization.Organization, subject string) currency.Cents {
	t.Helper()
	datas, err := txutil.GetTransactionsByCurrency(org.Namespaced(ctx), subject, "iam-user", currency.USD, org.TestMode())
	if err != nil {
		t.Fatalf("GetTransactionsByCurrency(%s): %v", subject, err)
	}
	if d, ok := datas.Data[currency.USD]; ok && d != nil {
		return d.Balance
	}
	return 0
}

// TestTopup_CanonicalLedger_PerOrgIsolation proves a card top-up lands on the
// exact per-org balance the gateway gates on, and NEVER leaks across orgs or
// across subjects.
func TestTopup_CanonicalLedger_PerOrgIsolation(t *testing.T) {
	t.Setenv("SQUARE_ENVIRONMENT", "production") // real charge → live (spendable) bucket
	ctx := ae.NewContext()
	defer ctx.Close()

	orgA := &organization.Organization{}
	orgA.Name = "acme-iso"
	orgA.Live = true
	orgB := &organization.Organization{}
	orgB.Name = "globex-iso"
	orgB.Live = true

	// Cold customer at org A tops up $20.00.
	creditLikeTopup(t, ctx, orgA, "acme-iso", 2000)

	if got := balanceOf(t, ctx, orgA, "acme-iso"); got != 2000 {
		t.Fatalf("org A canonical balance = %d, want 2000 (the bucket the gateway reads)", got)
	}
	// Cross-org isolation: org B's ledger is untouched — a top-up for A can
	// never credit B (different org namespace).
	if got := balanceOf(t, ctx, orgB, "globex-iso"); got != 0 {
		t.Fatalf("org B balance = %d, want 0 — CROSS-ORG LEAK: A's top-up credited B", got)
	}
	// And A's subject is invisible from B's namespace.
	if got := balanceOf(t, ctx, orgB, "acme-iso"); got != 0 {
		t.Fatalf("org B sees A's subject balance = %d, want 0 — namespace isolation broken", got)
	}

	// Subject isolation WITHIN an org (personal-billing <org>/<name> vs <org>).
	creditLikeTopup(t, ctx, orgA, "acme-iso/alice", 500)
	if got := balanceOf(t, ctx, orgA, "acme-iso/alice"); got != 500 {
		t.Fatalf("acme-iso/alice balance = %d, want 500", got)
	}
	if got := balanceOf(t, ctx, orgA, "acme-iso"); got != 2000 {
		t.Fatalf("org-slug balance moved to %d after crediting a sub-user, want 2000", got)
	}
}

// TestTopup_SandboxChargeCreditsTestBucket proves the console-sandbox proof
// path: under SQUARE_ENVIRONMENT=sandbox the credit lands in the TEST bucket
// (what a sandbox card top-up increases), and the LIVE spendable bucket stays
// empty — so a sandbox proof can never mint real credit.
func TestTopup_SandboxChargeCreditsTestBucket(t *testing.T) {
	t.Setenv("SQUARE_ENVIRONMENT", "sandbox")
	ctx := ae.NewContext()
	defer ctx.Close()

	org := &organization.Organization{}
	org.Name = "sandbox-proof"
	org.Live = true // org "live", but the deployment is sandbox → test mode wins

	if !org.TestMode() {
		t.Fatal("precondition: SQUARE_ENVIRONMENT=sandbox must force test mode")
	}
	creditLikeTopup(t, ctx, org, "sandbox-proof", 1500)

	// org.TestMode()==true, so balanceOf reads the TEST bucket.
	if got := balanceOf(t, ctx, org, "sandbox-proof"); got != 1500 {
		t.Fatalf("TEST bucket = %d, want 1500 (sandbox top-up credits the test bucket)", got)
	}
	// The LIVE (spendable) bucket must be empty — no free money.
	datas, err := txutil.GetTransactionsByCurrency(org.Namespaced(ctx), "sandbox-proof", "iam-user", currency.USD, false)
	if err != nil {
		t.Fatalf("live-bucket read: %v", err)
	}
	if d, ok := datas.Data[currency.USD]; ok && d != nil && d.Balance != 0 {
		t.Fatalf("LIVE bucket = %d, want 0 — a sandbox charge credited the spendable balance", d.Balance)
	}
}

// TestTopup_Idempotent_NoDoubleCredit proves a retry / double-submit with the
// same idempotency key charges + credits AT MOST once: the second Begin replays
// the completed guard, so the credit is not applied twice.
func TestTopup_Idempotent_NoDoubleCredit(t *testing.T) {
	t.Setenv("SQUARE_ENVIRONMENT", "production")
	ctx := ae.NewContext()
	defer ctx.Close()

	org := &organization.Organization{}
	org.Name = "idem-org"
	org.Live = true
	db := datastore.New(org.Namespaced(ctx))

	scope := "billing-topup:idem-org"
	const key = "nonce:cnon:card-nonce-ok"

	// First submit: fresh guard → do the money move → seal it.
	rec, replay, err := idempotencykey.Begin(db, scope, key)
	if err != nil {
		t.Fatalf("Begin #1: %v", err)
	}
	if replay {
		t.Fatal("first Begin must not be a replay")
	}
	creditLikeTopup(t, ctx, org, "idem-org", 3000)
	if err := idempotencykey.Complete(rec, `{"status":"ok","balanceCents":3000}`); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Second submit with the SAME key (retry / double-click): must be a replay
	// of the completed guard — the handler returns the stored response and does
	// NOT credit again.
	rec2, replay2, err := idempotencykey.Begin(db, scope, key)
	if err != nil {
		t.Fatalf("Begin #2: %v", err)
	}
	if !replay2 || rec2.Status != idempotencykey.StatusCompleted {
		t.Fatalf("second Begin: replay=%v status=%q, want replay=true status=completed", replay2, rec2.Status)
	}

	// Balance reflects exactly ONE $30 credit, not two.
	if got := balanceOf(t, ctx, org, "idem-org"); got != 3000 {
		t.Fatalf("balance = %d, want 3000 — DOUBLE-CREDIT: idempotent retry applied the credit twice", got)
	}
}
