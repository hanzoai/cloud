// Pinning tests for the embedded billing admin SPA. These test the contract
// between hanzoai/billing (SPA source) and hanzoai/commerce (embedding host):
// the Dockerfile's billing-build stage MUST produce an index.html and a
// Next.js _next/ tree, and the go:embed directive MUST resolve them.
//
// When run on a fresh clone with only .gitkeep in billing/ui/dist, these
// tests are skipped — the real build hasn't happened yet. Once the
// billing-build stage runs (Docker) or a developer overlays dist/ locally,
// the tests enforce shape.
package billing

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth"
)

// TestUISub_Resolves confirms the go:embed fs.Sub on "ui/dist" returns a
// non-nil filesystem.
func TestUISub_Resolves(t *testing.T) {
	if UISub() == nil {
		t.Fatal("UISub() returned nil")
	}
}

// TestEmbeddedBillingUI_HasIndex checks that the Next.js build output is
// present. Skipped when only the .gitkeep placeholder exists (fresh clone,
// build not yet run).
func TestEmbeddedBillingUI_HasIndex(t *testing.T) {
	f := UISub()
	file, err := f.Open("index.html")
	if err != nil {
		t.Skipf("no embedded index.html — run billing-build stage first: %v", err)
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		t.Fatalf("stat index.html: %v", err)
	}
	if st.Size() < 50 {
		t.Errorf("index.html size=%d, too small to be a real bundle", st.Size())
	}
}

// TestEmbeddedBillingUI_HasNextBundle confirms the Next.js build output is
// present. Skipped when the placeholder is still in place (fresh clone,
// billing-build stage not yet run).
func TestEmbeddedBillingUI_HasNextBundle(t *testing.T) {
	f := UISub()
	if _, err := fs.Stat(f, "_next"); err != nil {
		t.Skipf("no _next/ tree — run billing-build stage first: %v", err)
	}
	var jsCount int
	_ = fs.WalkDir(f, "_next", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".js") {
			jsCount++
		}
		return nil
	})
	if jsCount == 0 {
		t.Error("expected at least one JS chunk under _next/, found none")
	}
}

// TestUIHandler_NoAuth_Returns404 locks in the fail-closed default: without
// a Bearer token, the route returns 404 (not 403/401) to avoid leaking the
// admin SPA's existence. iam=nil simulates IAM disabled.
func TestUIHandler_NoAuth_Returns404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/admin/billing", UIHandler("/admin/billing", nil))
	r.GET("/admin/billing/*filepath", UIHandler("/admin/billing", nil))

	cases := []string{
		"/admin/billing",
		"/admin/billing/",
		"/admin/billing/payment-methods",
		"/admin/billing/_next/static/chunks/x.js",
	}
	for _, path := range cases {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: expected 404, got %d", path, w.Code)
		}
	}
}

// TestUIHandler_MalformedBearer_Returns404 confirms a non-Bearer-prefixed
// token (e.g. "Basic foo") also 404s — the handler rejects anything that is
// not a valid "Bearer <token>" header.
func TestUIHandler_MalformedBearer_Returns404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/admin/billing/*filepath", UIHandler("/admin/billing", nil))

	req := httptest.NewRequest(http.MethodGet, "/admin/billing/plans", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for non-Bearer auth, got %d", w.Code)
	}
}

// TestIsAuthorized_Roles locks in the authorized-role set. Changes here MUST
// be paired with a security review — expanding the role set widens the
// attack surface on the billing admin SPA.
//
// The matrix covers:
//   - nil claims (no token resolved)   → false
//   - IsAdmin=true                     → true
//   - each admin role in adminRoles    → true
//   - benign roles ("user", "customer") → false
//   - empty claims                     → false
func TestIsAuthorized_Roles(t *testing.T) {
	if isAuthorized(nil) {
		t.Error("nil claims must not authorize")
	}

	cases := []struct {
		name    string
		isAdmin bool
		roles   []string
		want    bool
	}{
		{"empty claims", false, nil, false},
		{"is-admin flag", true, nil, true},
		{"admin role", false, []string{"admin"}, true},
		{"billing_admin role", false, []string{"billing_admin"}, true},
		{"owner role", false, []string{"owner"}, true},
		{"superadmin role", false, []string{"superadmin"}, true},
		{"benign user", false, []string{"user"}, false},
		{"customer", false, []string{"customer"}, false},
		{"mixed benign + admin", false, []string{"user", "admin"}, true},
		{"case sensitive ADMIN", false, []string{"ADMIN"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := &auth.IAMClaims{
				IsAdmin: tc.isAdmin,
				Roles:   auth.FlexRoles(tc.roles),
			}
			got := isAuthorized(claims)
			if got != tc.want {
				t.Errorf("isAuthorized(%+v) = %v, want %v", claims, got, tc.want)
			}
		})
	}
}
