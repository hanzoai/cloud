package producttaxonomy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/productoption"
	"github.com/hanzoai/cloud/clients/commerce/models/productoptionvalue"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// wire builds a gin engine with org+context injected, then calls Route. It
// asserts Route does NOT panic — the sibling-wildcard risk: rest's default
// /:product-optionid route and the custom /:product-optionid/values subroute
// must share the SAME wildcard name or gin panics at wiring.
func wire(t *testing.T, base context.Context, ns string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	eng := gin.New()
	eng.Use(func(c *gin.Context) {
		org := &organization.Organization{}
		org.Name = ns
		c.Set("organization", org)
		c.Set("context", base)
		c.Next()
	})
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("producttaxonomy.Route panicked at wiring (sibling wildcard?): %v", r)
		}
	}()
	Route(eng) // no token middleware — exercise the handlers directly
	return eng
}

func TestRoute_WiresAndOptionValuesSubrouteWorks(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	base := nscontext.WithNamespace(context.Background(), "acme")
	eng := wire(t, base, "acme")

	// Seed an option in the acme namespace.
	db := datastore.New(base)
	opt := productoption.New(db)
	opt.Title = "Size"
	opt.ProductId = "prod_1"
	if err := opt.Create(); err != nil {
		t.Fatalf("seed option: %v", err)
	}

	// POST a value through the wired router (proves the :product-optionid param
	// plumbs from the URL to the handler's ByName lookup).
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/product-option/"+opt.Id()+"/values",
		strings.NewReader(`{"value":"Large"}`))
	req.Header.Set("Content-Type", "application/json")
	eng.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("add value status = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	// GET the values back.
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/product-option/"+opt.Id()+"/values", nil)
	eng.ServeHTTP(w2, req2)
	if w2.Code != 200 {
		t.Fatalf("list values status = %d, want 200; body=%s", w2.Code, w2.Body.String())
	}
	var vals []*productoptionvalue.ProductOptionValue
	if err := json.Unmarshal(w2.Body.Bytes(), &vals); err != nil {
		t.Fatalf("values not JSON: %s", w2.Body.String())
	}
	if len(vals) != 1 || vals[0].Value != "Large" || vals[0].OptionId != opt.Id() {
		t.Fatalf("values = %+v, want one {Large, %s}", vals, opt.Id())
	}
}
