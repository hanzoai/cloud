package test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/gin-gonic/gin"

	billingApi "github.com/hanzoai/cloud/clients/commerce/api/billing"
	"github.com/hanzoai/cloud/clients/commerce/auth"
	"github.com/hanzoai/cloud/clients/commerce/models/paymentmethod"
	"github.com/hanzoai/cloud/clients/commerce/util/bit"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ginclient"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

// Regression suite for the #43 billing IDOR fix.
//
// The LIVE, reachable gap is the user-group (no-mask TokenRequired()) payment-method
// handlers: ANY authenticated org member reaches them, so the
// callerMayReachBillingSubject ownership gate there is load-bearing — it is the
// only thing between a non-owner member and another subject's saved card
// (read / mutate / detach / set-default / enumerate).
//
// The admin-mask money routes (topup, void-grant, get-bank-transfer, get-event)
// are protected by TokenRequired(permission.Admin) ALONE: a non-admin is 403 before
// the handler runs, so a per-subject guard inside those handlers would be dead code
// and is intentionally absent. The final real-router spec PROVES that gate (403)
// rather than asserting an in-handler deny that could never fire.
//
// EdgeAuth/the gateway are not mounted in this unit harness, so idorShim reproduces
// the gin context each real caller profile lands with after identity middleware:
//   - nonpriv : an ordinary org member — no Admin bit, iam_claims.Owner == org slug.
//   - service : a verified COMMERCE_SERVICE_TOKEN — Admin bit, no IAM principal
//               (EdgeAuth strips its identity headers); the sole privilege signal.
// The namespace is the shared fixture org for both; the gap under test is INTRA-org
// (cross-org is already closed by namespace scoping). The differentiator is the
// per-user owner (CustomerId/UserId) of the seeded record vs the caller's own
// subject (orgBillingKey == org slug).

const (
	idorService = "service"
	idorNonPriv = "nonpriv"
)

// idorSubject is the caller's per-org billing subject — the org slug, exactly what
// orgBillingKey resolves and what EdgeAuth pins owned records under.
func idorSubject() string { return strings.ToLower(strings.TrimSpace(org.Name)) }

func idorShim(c *gin.Context) {
	c.Set("organization", org)
	switch c.GetHeader("X-Test-Profile") {
	case idorService:
		// TokenRequired's verified-service-token branch sets Admin perms + an org;
		// EdgeAuth stripped identity headers so there is no IAM principal. The Admin
		// permission bit is the ONLY privilege signal.
		c.Set("permissions", bit.Field(permission.Admin|permission.Live))
	default: // idorNonPriv: authenticated ordinary member, own subject only.
		c.Set("permissions", bit.Field(0))
		c.Set("iam_claims", &auth.IAMClaims{Owner: idorSubject()})
	}
	c.Next()
}

var idorCl *ginclient.Client

func idorReq(method, uri, profile string, body io.Reader, ct string) *httptest.ResponseRecorder {
	r := idorCl.NewRequest(method, uri, body)
	if ct != "" {
		r.Header.Set("Content-Type", ct)
	}
	r.Header.Set("X-Test-Profile", profile)
	w := httptest.NewRecorder()
	idorCl.Router.ServeHTTP(w, r)
	return w
}

func idorGet(uri, profile string) *httptest.ResponseRecorder {
	return idorReq(http.MethodGet, uri, profile, nil, "")
}

func idorPost(uri, profile string, body interface{}) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	return idorReq(http.MethodPost, uri, profile, bytes.NewReader(b), "application/json")
}

func idorPatch(uri, profile string, body interface{}) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	return idorReq(http.MethodPatch, uri, profile, bytes.NewReader(b), "application/json")
}

func idorDelete(uri, profile string) *httptest.ResponseRecorder {
	return idorReq(http.MethodDelete, uri, profile, nil, "")
}

// customerIdsIn decodes a ListPaymentMethods array response into the set of
// customerId values it exposed — the leak surface for the enumeration guard.
func customerIdsIn(w *httptest.ResponseRecorder) map[string]bool {
	var got []map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	out := map[string]bool{}
	for _, m := range got {
		if cu, ok := m["customerId"].(string); ok {
			out[cu] = true
		}
	}
	return out
}

// seedPaymentMethod creates a card in the fixture-org namespace owned by `owner`
// (CustomerId == UserId == owner). The handler reads the same namespace via
// org.Namespaced(c).
func seedPaymentMethod(owner string) string {
	pm := paymentmethod.New(db)
	pm.CustomerId = owner
	pm.UserId = owner
	pm.Type = "card"
	pm.ProviderRef = "ccof:test"
	pm.Card = &paymentmethod.CardDetails{Brand: "visa", Last4: "4242", ExpMonth: 12, ExpYear: 2032}
	Expect(pm.Create()).To(Succeed())
	return pm.Id()
}

var _ = Describe("billing IDOR scoping", Ordered, ContinueOnFailure, func() {
	var subject, other string
	// realCl mounts the ACTUAL billing router (admin/user groups + their real
	// middleware) with a non-admin authenticated IAM identity, to prove the admin
	// gate — not any in-handler guard — is what protects the admin-only money routes.
	var realCl *ginclient.Client

	BeforeAll(func() {
		subject = idorSubject()
		// A per-user owner that is NOT the caller's org slug — the "other subject"
		// whose records a non-privileged caller must never reach.
		other = subject + "-victim"

		idorCl = ginclient.New(ctx)
		idorCl.Router.Use(idorShim)
		idorCl.Router.GET("/billing/payment-methods", billingApi.ListPaymentMethods)
		idorCl.Router.GET("/billing/payment-methods/:id", billingApi.GetPaymentMethod)
		idorCl.Router.PATCH("/billing/payment-methods/:id", billingApi.UpdatePaymentMethod)
		idorCl.Router.DELETE("/billing/payment-methods/:id", billingApi.DetachPaymentMethod)
		idorCl.Router.POST("/billing/customers/:id/default-payment-method", billingApi.SetDefaultPaymentMethod)

		realCl = ginclient.New(ctx)
		// A non-admin authenticated IAM member: iam_authenticated + zero permissions.
		realCl.Router.Use(func(c *gin.Context) {
			c.Set("organization", org)
			c.Set("iam_authenticated", true)
			c.Set("permissions", bit.Field(0))
			c.Next()
		})
		billingApi.Route(realCl.Router)
	})

	// ── read / mutate a card by :id (unpinned path-param) ────────────────────
	Context("payment-method read/mutate", func() {
		pmPath := func(id string) string { return "/billing/payment-methods/" + id }

		It("DENIES a non-privileged caller reading another subject's card (404 — no existence oracle)", func() {
			Expect(idorGet(pmPath(seedPaymentMethod(other)), idorNonPriv).Code).To(Equal(404))
		})
		It("ALLOWS the owning subject to read its own card (200)", func() {
			Expect(idorGet(pmPath(seedPaymentMethod(subject)), idorNonPriv).Code).To(Equal(200))
		})
		It("ALLOWS a service token to read any subject's card (privilege bypass → 200)", func() {
			Expect(idorGet(pmPath(seedPaymentMethod(other)), idorService).Code).To(Equal(200))
		})

		It("DENIES a non-privileged caller updating another subject's card (404) and does NOT mutate it", func() {
			id := seedPaymentMethod(other)
			Expect(idorPatch(pmPath(id), idorNonPriv, map[string]interface{}{
				"metadata": map[string]interface{}{"tamper": "x"},
			}).Code).To(Equal(404))
			pm := paymentmethod.New(db)
			Expect(pm.GetById(id)).To(Succeed())
			Expect(pm.Metadata["tamper"]).To(BeNil())
		})
		It("ALLOWS the owning subject to update its own card (200)", func() {
			Expect(idorPatch(pmPath(seedPaymentMethod(subject)), idorNonPriv, map[string]interface{}{
				"metadata": map[string]interface{}{"note": "ok"},
			}).Code).To(Equal(200))
		})

		It("DENIES a non-privileged caller detaching another subject's card (404) and does NOT delete it", func() {
			id := seedPaymentMethod(other)
			Expect(idorDelete(pmPath(id), idorNonPriv).Code).To(Equal(404))
			pm := paymentmethod.New(db)
			Expect(pm.GetById(id)).To(Succeed()) // still present
		})
		It("ALLOWS the owning subject to detach its own card (200)", func() {
			Expect(idorDelete(pmPath(seedPaymentMethod(subject)), idorNonPriv).Code).To(Equal(200))
		})
	})

	// ── set-default (two unpinned owner refs: :id customer + body paymentMethodId) ──
	Context("set-default payment method", func() {
		setDefault := func(customerId, pmID, profile string) *httptest.ResponseRecorder {
			return idorPost("/billing/customers/"+customerId+"/default-payment-method", profile,
				map[string]interface{}{"paymentMethodId": pmID})
		}

		It("DENIES a non-privileged caller targeting another subject's customer id (404)", func() {
			Expect(setDefault(other, seedPaymentMethod(other), idorNonPriv).Code).To(Equal(404))
		})
		It("DENIES a non-privileged caller defaulting another subject's card under its own customer id (404) and does NOT flip it", func() {
			victim := seedPaymentMethod(other)
			Expect(setDefault(subject, victim, idorNonPriv).Code).To(Equal(404))
			pm := paymentmethod.New(db)
			Expect(pm.GetById(victim)).To(Succeed())
			Expect(pm.IsDefault).To(BeFalse())
		})
		It("ALLOWS the owning subject to default its own card (200)", func() {
			Expect(setDefault(subject, seedPaymentMethod(subject), idorNonPriv).Code).To(Equal(200))
		})
	})

	// ── list enumeration (EdgeAuth pins a PRESENT filter; the absent-filter gap) ──
	Context("list payment methods (enumeration scoping)", func() {
		It("EXCLUDES another subject's methods from an unfiltered non-privileged list", func() {
			seedPaymentMethod(other)
			seedPaymentMethod(subject)
			w := idorGet("/billing/payment-methods", idorNonPriv)
			Expect(w.Code).To(Equal(200))
			custs := customerIdsIn(w)
			Expect(custs[other]).To(BeFalse())  // victim's methods never leak
			Expect(custs[subject]).To(BeTrue()) // own methods still visible
		})
		It("INCLUDES any subject for a privileged (service) unfiltered list — cloud-api unchanged", func() {
			seedPaymentMethod(other)
			w := idorGet("/billing/payment-methods", idorService)
			Expect(w.Code).To(Equal(200))
			Expect(customerIdsIn(w)[other]).To(BeTrue())
		})
	})

	// ── admin-only money routes: the TokenRequired(Admin) gate IS the protection ──
	// These handlers carry no per-subject guard (it would be unreachable). Prove the
	// gate: a non-admin authenticated IAM member is 403 before the handler runs.
	Context("admin-gated money routes (real router)", func() {
		It("403s a non-admin on the admin-only money routes (admin gate is the sole, sufficient guard)", func() {
			for _, uri := range []string{"/billing/topup", "/billing/credit-grants/anyid/void"} {
				r := realCl.NewRequest(http.MethodPost, uri, strings.NewReader("{}"))
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				realCl.Router.ServeHTTP(w, r)
				Expect(w.Code).To(Equal(403), uri)
			}
		})
	})
})
