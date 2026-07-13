package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/events"
)

func init() { gin.SetMode(gin.TestMode) }

func testCtx(method, param, body string) *gin.Context {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, "/v1/product", strings.NewReader(body))
	if param != "" {
		c.Params = gin.Params{{Key: "productid", Value: param}}
	}
	return c
}

// writeSubject classifies a product REST request into the catalog event it should emit.
func TestWriteSubject(t *testing.T) {
	cases := []struct {
		method, param, want string
	}{
		{http.MethodPost, "", events.SubjectProductCreated},   // create on the collection
		{http.MethodPut, "abc", events.SubjectProductUpdated}, // replace an item
		{http.MethodPatch, "abc", events.SubjectProductUpdated},
		{http.MethodPost, "abc", ""}, // POST /:id is the ambiguous method-override — skip
		{http.MethodGet, "abc", ""},
		{http.MethodGet, "", ""},
		{http.MethodDelete, "abc", ""},
	}
	for _, tc := range cases {
		if got := writeSubject(testCtx(tc.method, tc.param, "")); got != tc.want {
			t.Errorf("writeSubject(%s param=%q) = %q, want %q", tc.method, tc.param, got, tc.want)
		}
	}
}

// peekProductBody extracts the slug/name/sku AND restores the body so the CRUD handler
// downstream can still decode it.
func TestPeekProductBodyRestoresBody(t *testing.T) {
	const raw = `{"slug":"valentina","name":"Valentina","sku":"SKU-1","price":100}`
	c := testCtx(http.MethodPost, "", raw)

	b := peekProductBody(c)
	if b.Slug != "valentina" || b.Name != "Valentina" || b.SKU != "SKU-1" {
		t.Fatalf("peek wrong: %+v", b)
	}
	// The handler must still see the full, unconsumed body.
	rest, err := io.ReadAll(c.Request.Body)
	if err != nil {
		t.Fatalf("body restore read: %v", err)
	}
	if string(rest) != raw {
		t.Fatalf("body not restored: got %q want %q", rest, raw)
	}
}

func TestPeekProductBodyToleratesGarbage(t *testing.T) {
	if b := peekProductBody(testCtx(http.MethodPost, "", "not json")); b.Slug != "" {
		t.Errorf("garbage body must yield empty slug, got %+v", b)
	}
	// A whitespace-only slug is trimmed to empty (no event fires for it).
	if b := peekProductBody(testCtx(http.MethodPost, "", `{"slug":"  "}`)); b.Slug != "" {
		t.Errorf("blank slug must trim to empty, got %q", b.Slug)
	}
}

// eventProductID prefers the URL item param (update), else the id the create handler put
// in the Location header, else "".
func TestEventProductID(t *testing.T) {
	if got := eventProductID(testCtx(http.MethodPut, "prod_9", "")); got != "prod_9" {
		t.Errorf("update id = %q, want prod_9", got)
	}
	c := testCtx(http.MethodPost, "", "")
	c.Writer.Header().Set("Location", "/v1/product/prod_created")
	if got := eventProductID(c); got != "prod_created" {
		t.Errorf("create id from Location = %q, want prod_created", got)
	}
	if got := eventProductID(testCtx(http.MethodPost, "", "")); got != "" {
		t.Errorf("no param/location = %q, want empty", got)
	}
}
