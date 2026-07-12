package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth"
	"github.com/hanzoai/cloud/clients/commerce/models/catalogentry"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// ctxWith builds a gin test context whose GetContext resolves to the ae SQLite
// datastore, optionally carrying IAM claims.
func ctxWith(w http.ResponseWriter, claims *auth.IAMClaims) *gin.Context {
	c, _ := gin.CreateTestContext(w)
	c.Set("context", context.Background())
	if claims != nil {
		c.Set("iam_claims", claims)
	}
	return c
}

func TestPublic_ReturnsProjection_NoAuth(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	// Seed the catalog in the system namespace via a SuperAdmin seed call.
	sw := httptest.NewRecorder()
	sc := ctxWith(sw, &auth.IAMClaims{IsSuperAdmin: true})
	sc.Request = httptest.NewRequest(http.MethodPost, "/v1/catalog/seed", nil)
	SeedCatalog(sc)
	if sw.Code != 200 {
		t.Fatalf("seed status = %d; body=%s", sw.Code, sw.Body.String())
	}

	// Public GET — no auth at all.
	w := httptest.NewRecorder()
	c := ctxWith(w, nil)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/commerce/catalog?brand=hanzo", nil)
	Public(c)

	if w.Code != 200 {
		t.Fatalf("public catalog status = %d; body=%s", w.Code, w.Body.String())
	}
	var cat catalogentry.Catalog
	if err := json.Unmarshal(w.Body.Bytes(), &cat); err != nil {
		t.Fatalf("projection not JSON: %s", w.Body.String())
	}
	if cat.Brand != "hanzo" || len(cat.Categories) != 13 || len(cat.Products) == 0 {
		t.Fatalf("bad projection: brand=%s cats=%d products=%d", cat.Brand, len(cat.Categories), len(cat.Products))
	}
}

func TestCreateEntry_RequiresSuperAdmin(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	body, _ := json.Marshal(map[string]any{
		"slug": "myproduct", "brand": "hanzo", "name": "My Product",
		"category": "AI", "iconKey": "Box",
	})

	// Org-level admin (NOT global) — must be rejected 403.
	w := httptest.NewRecorder()
	c := ctxWith(w, &auth.IAMClaims{Owner: "acme", IsAdmin: true})
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/catalog/entries", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	CreateEntry(c)
	if w.Code != 403 {
		t.Fatalf("org-admin create status = %d, want 403 (platform-admin only); body=%s", w.Code, w.Body.String())
	}

	// SuperAdmin — allowed 201.
	w2 := httptest.NewRecorder()
	c2 := ctxWith(w2, &auth.IAMClaims{IsSuperAdmin: true})
	c2.Request = httptest.NewRequest(http.MethodPost, "/v1/catalog/entries", bytes.NewReader(body))
	c2.Request.Header.Set("Content-Type", "application/json")
	CreateEntry(c2)
	if w2.Code != 201 {
		t.Fatalf("SuperAdmin create status = %d, want 201; body=%s", w2.Code, w2.Body.String())
	}

	// The created entry is now visible in the public projection.
	w3 := httptest.NewRecorder()
	c3 := ctxWith(w3, nil)
	c3.Request = httptest.NewRequest(http.MethodGet, "/v1/commerce/catalog?brand=hanzo", nil)
	Public(c3)
	var cat catalogentry.Catalog
	_ = json.Unmarshal(w3.Body.Bytes(), &cat)
	found := false
	for _, p := range cat.Products {
		if p.Slug == "myproduct" {
			found = true
		}
	}
	if !found {
		t.Fatal("created entry did not appear in the public projection")
	}
}

func TestCreateEntry_DuplicateSlugRejected(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	body, _ := json.Marshal(map[string]any{
		"slug": "dup", "brand": "hanzo", "name": "Dup", "category": "AI", "iconKey": "Box",
	})
	admin := &auth.IAMClaims{IsSuperAdmin: true}

	w := httptest.NewRecorder()
	c := ctxWith(w, admin)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/catalog/entries", bytes.NewReader(body))
	CreateEntry(c)
	if w.Code != 201 {
		t.Fatalf("first create = %d, want 201", w.Code)
	}

	w2 := httptest.NewRecorder()
	c2 := ctxWith(w2, admin)
	c2.Request = httptest.NewRequest(http.MethodPost, "/v1/catalog/entries", bytes.NewReader(body))
	CreateEntry(c2)
	if w2.Code != 409 {
		t.Fatalf("duplicate create = %d, want 409", w2.Code)
	}
}
