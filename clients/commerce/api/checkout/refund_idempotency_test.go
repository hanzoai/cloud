package checkout

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
	"github.com/hanzoai/cloud/clients/commerce/models/idempotencykey"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// TestRefund_Idempotency_ReplayReturnsStored proves the refund guard: once a
// refund with key K has completed, a SECOND POST with the same K returns the
// stored response and NEVER re-enters refund() (which would hit the gateway and
// double-refund). We assert this by pre-seeding a completed guard for the order
// and checking the replay returns exactly the stored body — no Square creds,
// no second money move.
func TestRefund_Idempotency_ReplayReturnsStored(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	const ns = "acme"
	base := nscontext.WithNamespace(context.Background(), ns)
	db := datastore.New(base)

	// Seed an order in the org namespace so getOrganizationAndOrder resolves it.
	ord := order.New(db)
	ord.Total = 5000
	ord.Paid = 5000
	if err := ord.Create(); err != nil {
		t.Fatalf("seed order: %v", err)
	}

	// Pre-record a COMPLETED refund guard for this order + key, with a sentinel
	// response body — as if a first refund already succeeded.
	scope := "refund:" + ord.Id()
	rec, replay, err := idempotencykey.Begin(db, scope, "refund_key_1")
	if err != nil || replay {
		t.Fatalf("seed guard begin: err=%v replay=%v", err, replay)
	}
	sentinel := `{"id":"` + ord.Id() + `","refunded":1500,"sentinel":true}`
	if err := idempotencykey.Complete(rec, sentinel); err != nil {
		t.Fatalf("seed guard complete: %v", err)
	}

	// Now drive the Refund handler with the SAME key. It must short-circuit to
	// the stored response, returning 200 + the sentinel body verbatim.
	org := &organization.Organization{}
	org.Name = ns

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("organization", org)
	c.Set("context", base)
	// Refund is admin-gated (money move); inject the verified admin claim the
	// gateway/EdgeAuth would mint so middleware.RequireAdmin authorizes.
	c.Set("iam_authenticated", true)
	c.Set("iam_claims", &auth.IAMClaims{Owner: ns, IsAdmin: true})
	c.Params = gin.Params{{Key: "orderid", Value: ord.Id()}}
	body, _ := json.Marshal(map[string]any{"amount": 1500})
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/order/"+ord.Id()+"/refund", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("X-Idempotency-Key", "refund_key_1")

	Refund(c)

	if w.Code != 200 {
		t.Fatalf("replay status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("replay body not JSON: %s", w.Body.String())
	}
	if got["sentinel"] != true {
		t.Fatalf("replay did not return the stored response (would have re-refunded!); body=%s", w.Body.String())
	}
}

// TestRefund_Idempotency_InFlightRejected proves that while a refund with key K
// is in-flight (guard started, not completed), a concurrent second request with
// the same key is rejected with 409 rather than allowed to double-refund.
func TestRefund_Idempotency_InFlightRejected(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	const ns = "acme"
	base := nscontext.WithNamespace(context.Background(), ns)
	db := datastore.New(base)

	ord := order.New(db)
	ord.Total = 5000
	ord.Paid = 5000
	if err := ord.Create(); err != nil {
		t.Fatalf("seed order: %v", err)
	}

	// Start (but do NOT complete) a guard — simulating an in-flight refund.
	scope := "refund:" + ord.Id()
	if _, replay, err := idempotencykey.Begin(db, scope, "inflight_key"); err != nil || replay {
		t.Fatalf("seed in-flight guard: err=%v replay=%v", err, replay)
	}

	org := &organization.Organization{}
	org.Name = ns

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("organization", org)
	c.Set("context", base)
	// Refund is admin-gated (money move); inject the verified admin claim the
	// gateway/EdgeAuth would mint so middleware.RequireAdmin authorizes.
	c.Set("iam_authenticated", true)
	c.Set("iam_claims", &auth.IAMClaims{Owner: ns, IsAdmin: true})
	c.Params = gin.Params{{Key: "orderid", Value: ord.Id()}}
	body, _ := json.Marshal(map[string]any{"amount": 1500})
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/order/"+ord.Id()+"/refund", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("X-Idempotency-Key", "inflight_key")

	Refund(c)

	if w.Code != 409 {
		t.Fatalf("in-flight replay status = %d, want 409 (must not double-refund); body=%s", w.Code, w.Body.String())
	}
}
