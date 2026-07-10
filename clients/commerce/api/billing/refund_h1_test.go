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
	"github.com/hanzoai/cloud/clients/commerce/mintauth"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// invokeMoneyHandler drives a single billing handler directly with a real
// org + datastore context and an optional header set — the harness for the H1
// money-correctness tests (Deposit/Refund behavior, independent of the C1 gate,
// which is proven separately). The org and ctx are shared with the test's setup
// so the handler's org.Namespaced(c) resolves the SAME namespace + defaultDB the
// setup wrote to.
func invokeMoneyHandler(org *organization.Organization, ctx context.Context, h gin.HandlerFunc, body string, headers map[string]string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("organization", org)
	// These money handlers (Deposit/Refund) run in production ONLY behind
	// middleware.PlatformOnly, which calls AuthorizeMint. Invoking them directly
	// here skips that middleware, so authorize the datastore context to faithfully
	// reproduce the post-gate state; the GATE itself (org-admin → 403 / ledger-sink
	// refusal) is proven separately in mint_surface_test.go and the C1 tests.
	c.Set("context", mintauth.WithAuthorized(ctx))
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/x", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	c.Request = req
	h(c)
	return w
}

func txID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response json: %v (%s)", err, w.Body.String())
	}
	id, _ := resp["transactionId"].(string)
	return id
}

// seedCharge writes an original Withdraw (charge) to the org's ledger and returns
// its id — the thing a refund reverses.
func seedCharge(t *testing.T, org *organization.Organization, ctx context.Context, subject string, cents int64) string {
	t.Helper()
	db := datastore.New(org.Namespaced(ctx))
	tr := transaction.New(db)
	tr.Type = transaction.Withdraw
	tr.DestinationId = subject
	tr.DestinationKind = "iam-user"
	tr.Currency = currency.USD
	tr.Amount = currency.Cents(cents)
	tr.MustCreate()
	return tr.Id()
}

func moneyOrg(name string) *organization.Organization {
	o := &organization.Organization{}
	o.Name = name
	o.Live = true
	return o
}

// TestRefund_MissingOriginal_404 — H1: a refund naming a non-existent original
// transaction is rejected (no silent mint against a phantom id).
func TestRefund_MissingOriginal_404(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	org := moneyOrg("h1r-missing")
	body := `{"user":"h1r-missing/alice","amount":100,"originalTransactionId":"does-not-exist"}`
	w := invokeMoneyHandler(org, ctx, Refund, body, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing original: status=%d body=%s, want 404", w.Code, w.Body.String())
	}
}

// TestRefund_OverOriginalAmount_400 — H1: a refund cannot exceed the original charge.
func TestRefund_OverOriginalAmount_400(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	org := moneyOrg("h1r-overcap")
	const subject = "h1r-overcap/alice"
	origID := seedCharge(t, org, ctx, subject, 1000)

	body := `{"user":"` + subject + `","amount":1500,"originalTransactionId":"` + origID + `"}`
	w := invokeMoneyHandler(org, ctx, Refund, body, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("over-cap refund: status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

// TestRefund_WrongSubject_400 — H1: a refund cannot route one subject's charge to
// another subject's balance.
func TestRefund_WrongSubject_400(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	org := moneyOrg("h1r-subject")
	origID := seedCharge(t, org, ctx, "h1r-subject/alice", 1000)

	body := `{"user":"h1r-subject/bob","amount":100,"originalTransactionId":"` + origID + `"}`
	w := invokeMoneyHandler(org, ctx, Refund, body, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("wrong-subject refund: status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

// TestRefund_NotACharge_400 — H1: only a Withdraw (charge) is refundable; refunding
// a Deposit/credit would double it.
func TestRefund_NotACharge_400(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	org := moneyOrg("h1r-type")
	const subject = "h1r-type/alice"

	// Seed a DEPOSIT (credit), not a charge, and try to "refund" it.
	db := datastore.New(org.Namespaced(ctx))
	dep := transaction.New(db)
	dep.Type = transaction.Deposit
	dep.DestinationId = subject
	dep.DestinationKind = "iam-user"
	dep.Currency = currency.USD
	dep.Amount = 1000
	dep.MustCreate()

	body := `{"user":"` + subject + `","amount":100,"originalTransactionId":"` + dep.Id() + `"}`
	w := invokeMoneyHandler(org, ctx, Refund, body, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("refund-of-credit: status=%d body=%s, want 400 (not a refundable charge)", w.Code, w.Body.String())
	}
}

// TestRefund_CurrencyMismatch_400 — H1: the refund currency must match the original.
func TestRefund_CurrencyMismatch_400(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	org := moneyOrg("h1r-cur")
	const subject = "h1r-cur/alice"
	origID := seedCharge(t, org, ctx, subject, 1000) // USD

	body := `{"user":"` + subject + `","amount":100,"currency":"eur","originalTransactionId":"` + origID + `"}`
	w := invokeMoneyHandler(org, ctx, Refund, body, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("currency-mismatch refund: status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

// TestRefund_Idempotent_OneRefundPerOriginal — H1: AT MOST ONE refund per original
// transaction. A valid refund succeeds (201); a SECOND refund of the same original
// — even with a different amount — replays the first (200, same transactionId) and
// does NOT mint a second credit.
func TestRefund_Idempotent_OneRefundPerOriginal(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	org := moneyOrg("h1r-idem")
	const subject = "h1r-idem/alice"
	origID := seedCharge(t, org, ctx, subject, 1000)

	first := `{"user":"` + subject + `","amount":400,"originalTransactionId":"` + origID + `"}`
	w1 := invokeMoneyHandler(org, ctx, Refund, first, nil)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first refund: status=%d body=%s, want 201", w1.Code, w1.Body.String())
	}
	tid1 := txID(t, w1)
	if tid1 == "" {
		t.Fatal("first refund missing transactionId")
	}

	// Second refund of the SAME original with a DIFFERENT amount must not double-mint.
	second := `{"user":"` + subject + `","amount":200,"originalTransactionId":"` + origID + `"}`
	w2 := invokeMoneyHandler(org, ctx, Refund, second, nil)
	if w2.Code != http.StatusOK {
		t.Fatalf("second refund of same original: status=%d body=%s, want 200 (idempotent replay)", w2.Code, w2.Body.String())
	}
	if tid2 := txID(t, w2); tid2 != tid1 {
		t.Fatalf("second refund minted a NEW transaction %q (first was %q) — double refund", tid2, tid1)
	}

	// Ledger truth: exactly ONE refund-tagged transaction exists for the subject.
	if n := countRefunds(t, org, ctx, subject); n != 1 {
		t.Fatalf("refund-tagged transactions for %s = %d, want exactly 1", subject, n)
	}
}

// countRefunds returns how many refund-tagged deposits exist for a subject.
func countRefunds(t *testing.T, org *organization.Organization, ctx context.Context, subject string) int {
	t.Helper()
	db := datastore.New(org.Namespaced(ctx))
	rootKey := db.NewKey("synckey", "", 1, nil)
	got := make([]*transaction.Transaction, 0)
	if _, err := transaction.Query(db).Ancestor(rootKey).
		Filter("DestinationId=", subject).
		Filter("Tags=", "refund").GetAll(&got); err != nil {
		t.Fatalf("query refunds: %v", err)
	}
	return len(got)
}
