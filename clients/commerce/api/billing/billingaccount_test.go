package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/billingaccount"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// createAccount drives CreateBillingAccount as IAM user "owner" and returns the id.
func createAccount(t *testing.T, org *organization.Organization, body string) string {
	t.Helper()
	w := httptest.NewRecorder()
	c := userCtx(w, org, "owner")
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/billing/accounts", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	CreateBillingAccount(c)
	if w.Code != 201 {
		t.Fatalf("CreateBillingAccount status = %d, body=%s", w.Code, w.Body.String())
	}
	var r struct {
		Id string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil || r.Id == "" {
		t.Fatalf("create account id decode: %v (body=%s)", err, w.Body.String())
	}
	return r.Id
}

// bindProject drives BindAccountProject (PUT /accounts/:id/projects/:project).
func bindProject(t *testing.T, org *organization.Organization, accountID, project string) {
	t.Helper()
	w := httptest.NewRecorder()
	c := userCtx(w, org, "owner")
	c.Params = gin.Params{{Key: "id", Value: accountID}, {Key: "project", Value: project}}
	c.Request = httptest.NewRequest(http.MethodPut, "/v1/billing/accounts/"+accountID+"/projects/"+project, nil)
	BindAccountProject(c)
	if w.Code != 200 && w.Code != 201 {
		t.Fatalf("BindAccountProject status = %d, body=%s", w.Code, w.Body.String())
	}
}

// A billing account with a $1 LimitCents caps the spend it funds: once the
// account's calendar-month spend reaches the ceiling, the verdict DENIES with
// reason billing_account — layered on top of, and independent of, the per-scope
// spend caps. The org's first account is its default, so the default scope draws it.
func TestBillingAccount_LimitCap_DeniesAtCap(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	org := &organization.Organization{}
	org.Name = "acct-cap"

	createAccount(t, org, `{"name":"primary","limitCents":100}`)

	// Under the cap: a $0.50 request is authorized.
	if v := authorize(t, org, "user=acct-cap&amount=50"); !v.Allow {
		t.Fatalf("pre-spend authorize denied: %+v", v)
	}

	// Spend the full $1.00 — resolveAccountId attributes it to the default account.
	if code := recordUsage(t, org, `{"user":"acct-cap","amount":100,"requestId":"ac1"}`); code != 201 {
		t.Fatalf("RecordUsage status = %d", code)
	}

	// The next billable request is DENIED by the ACCOUNT cap.
	v := authorize(t, org, "user=acct-cap&amount=1")
	if v.Allow {
		t.Fatalf("post-cap authorize ALLOWED, want deny: %+v", v)
	}
	if v.Reason != "billing_account" {
		t.Fatalf("reason = %q, want billing_account", v.Reason)
	}
	if v.CapCents != 100 || v.SpentCents != 100 {
		t.Fatalf("cap/spent = %d/%d, want 100/100", v.CapCents, v.SpentCents)
	}
}

// A FROZEN account (Enabled=false) hard-denies any billable request drawing on it —
// the sub-second account kill-switch, independent of balance or cap. Freeze is a
// platform control (not exposed in the org CRUD), so the test sets it on the model.
func TestBillingAccount_Freeze_Denies(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	org := &organization.Organization{}
	org.Name = "acct-frozen"

	db := datastore.New(nscontext.WithNamespace(context.Background(), org.Name))
	a := billingaccount.New(db)
	a.OrgId = org.Name
	a.Name = "frozen"
	a.Default = true
	a.Enabled = false // FROZEN
	if err := a.Create(); err != nil {
		t.Fatalf("seed frozen account: %v", err)
	}

	v := authorize(t, org, "user=acct-frozen&amount=1")
	if v.Allow {
		t.Fatalf("frozen-account authorize ALLOWED, want deny: %+v", v)
	}
	if v.Reason != "account_frozen" {
		t.Fatalf("reason = %q, want account_frozen", v.Reason)
	}
}

// A project bound to a DEDICATED account draws that account's budget: spend on the
// bound project is capped by the dedicated account, while the default scope and
// other (unbound) projects draw the uncapped default account — per-project funding
// isolation, one account's spend never gating another's.
func TestBillingAccount_ProjectBinding_DedicatedFunding(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	org := &organization.Organization{}
	org.Name = "acct-bind"

	// Default account (uncapped) + a dedicated $1 account bound to project "P".
	createAccount(t, org, `{"name":"default","limitCents":0}`)
	dedicated := createAccount(t, org, `{"name":"proj-p","limitCents":100}`)
	bindProject(t, org, dedicated, "P")

	// Spend $1 on P → attributed to the dedicated account.
	if code := recordUsage(t, org, `{"user":"acct-bind","amount":100,"requestId":"b1","project":"P"}`); code != 201 {
		t.Fatalf("RecordUsage P status = %d", code)
	}

	// P is over its dedicated account's cap → denied (pv=1: validated project).
	if v := authorize(t, org, "user=acct-bind&project=P&amount=1&pv=1"); v.Allow {
		t.Fatalf("P authorize ALLOWED, want deny (dedicated account cap): %+v", v)
	} else if v.Reason != "billing_account" {
		t.Fatalf("P reason = %q, want billing_account", v.Reason)
	}

	// The default scope draws the UNCAPPED default account → allowed. The dedicated
	// account's exhaustion must not gate the default account.
	if v := authorize(t, org, "user=acct-bind&amount=1"); !v.Allow {
		t.Fatalf("default-scope authorize DENIED, want allow (dedicated cap must not gate default): %+v", v)
	}
	// An unbound project Q draws the uncapped default account → allowed.
	if v := authorize(t, org, "user=acct-bind&project=Q&amount=1&pv=1"); !v.Allow {
		t.Fatalf("Q authorize DENIED, want allow (unbound project draws uncapped default): %+v", v)
	}
}

// An org that has provisioned NO billing account is byte-preserved: debits carry
// AccountId "" (the org-wide pool), resolveAccountId returns "", and the account
// layer never denies — existing behavior is unchanged until an account exists.
func TestBillingAccount_NoAccounts_Unchanged(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	org := &organization.Organization{}
	org.Name = "acct-none"

	if code := recordUsage(t, org, `{"user":"acct-none","amount":100,"requestId":"n1"}`); code != 201 {
		t.Fatalf("RecordUsage status = %d", code)
	}
	// No account, no cap → allowed regardless of spend.
	if v := authorize(t, org, "user=acct-none&amount=100000"); !v.Allow {
		t.Fatalf("no-account authorize DENIED, want allow (pool unchanged): %+v", v)
	}

	db := datastore.New(nscontext.WithNamespace(context.Background(), org.Name))
	if id := resolveAccountId(db, ""); id != "" {
		t.Fatalf("resolveAccountId = %q, want \"\" (org-wide pool)", id)
	}
	spent, err := accountSpentCents(db, org.TestMode(), "")
	if err != nil {
		t.Fatalf("accountSpentCents: %v", err)
	}
	if spent != 100 {
		t.Fatalf("pool spent = %d, want 100 (pool sums AccountId \"\")", spent)
	}
}
