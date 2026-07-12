package costs

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth"
	"github.com/hanzoai/cloud/clients/commerce/util/bit"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
)

// gateCtx builds a gin test context that optionally carries IAM claims and/or a
// permissions bit-field, mirroring what the auth middleware would have set.
func gateCtx(w http.ResponseWriter, claims *auth.IAMClaims, perms *bit.Field) *gin.Context {
	c, _ := gin.CreateTestContext(w)
	// A real gin context always carries a Request; GetIAMClaims reads headers as a
	// fallback, so mirror production and give it one (no identity headers set).
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/costs", nil)
	if claims != nil {
		c.Set("iam_claims", claims)
	}
	if perms != nil {
		c.Set("permissions", *perms)
	}
	return c
}

// iamUser builds claims for a JWT-verified IAM user (a non-empty Subject, the sub
// the gateway always mints). Subject is promoted from the embedded StandardClaims.
func iamUser(subject, owner string, isAdmin, superAdmin bool) *auth.IAMClaims {
	cl := &auth.IAMClaims{Owner: owner, IsAdmin: isAdmin}
	if superAdmin {
		cl.HomeOrg = "admin"
	}
	cl.Subject = subject
	return cl
}

func adminPerms() *bit.Field {
	f := bit.Field(permission.Admin | permission.Live)
	return &f
}

// TestRequireCostsAdmin is the CRITICAL authz gate: the vendor-cost god-view is
// platform spend, so ONLY a SuperAdmin (or the trusted M2M service token) may
// read it. The route-level TokenRequired(permission.Admin) is a no-op on the IAM
// path, so this in-handler gate is the real boundary.
func TestRequireCostsAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name   string
		claims *auth.IAMClaims
		perms  *bit.Field
		want   bool
	}{
		// A SuperAdmin is admitted.
		{"SuperAdmin (explicit claim)", iamUser("u1", "acme", false, true), adminPerms(), true},
		// The built-in admin org (owner=="admin") is a SuperAdmin.
		{"SuperAdmin (admin org)", iamUser("u1", "admin", false, false), adminPerms(), true},
		// The trusted M2M service token: Admin bit AND no IAM user identity (empty
		// Subject). This is the console's own SuperAdmin-gated proxy forwarding.
		{"service token (Admin bit, no IAM user)", &auth.IAMClaims{}, adminPerms(), true},
		{"service token (no claims at all, Admin bit)", nil, adminPerms(), true},

		// ── The attacks that MUST be refused ──
		// An org-level admin tenant: the gateway mints permission.Admin from IsAdmin
		// (edgeauth.permsHeader), so the Admin bit is present — but the caller carries
		// an IAM Subject, so the service-token branch must NOT admit, and IsAdmin is
		// NOT SuperAdmin. This is the headline privilege-escalation the gate closes.
		{"org admin tenant (Admin bit + IAM subject) — REJECT", iamUser("u2", "acme", true, false), adminPerms(), false},
		// A plain tenant with no admin at all.
		{"plain tenant — REJECT", iamUser("u3", "acme", false, false), nil, false},
		// An IAM user with NO permissions bit set is still refused.
		{"iam user, no perms — REJECT", iamUser("u4", "acme", true, false), nil, false},
		// No auth context at all (handler mounted without the token gate): fail closed.
		{"no auth context — REJECT", nil, nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c := gateCtx(w, tc.claims, tc.perms)
			got := requireCostsAdmin(c)
			if got != tc.want {
				t.Fatalf("requireCostsAdmin = %v, want %v (status=%d body=%s)", got, tc.want, w.Code, w.Body.String())
			}
			if !tc.want && w.Code != 403 {
				t.Fatalf("rejected caller status = %d, want 403 (body=%s)", w.Code, w.Body.String())
			}
		})
	}
}

// TestModelCostCentsClampsNegativeTokens: a malformed usage row with negative
// token counts must NOT produce negative COGS (which would understate cost and
// dishonestly inflate margin). Negatives clamp to 0; COGS never subtracts.
func TestModelCostCentsClampsNegativeTokens(t *testing.T) {
	// gpt-4o: 250c/Mtok in, 1000c/Mtok out.
	if got, ok := modelCostCents("gpt-4o", -1_000_000, -1_000_000); !ok || got != 0 {
		t.Errorf("negative tokens = %d ok=%v, want 0 true (clamped, never negative)", got, ok)
	}
	// A mixed row: negative prompt clamps to 0, positive completion still counts.
	if got, ok := modelCostCents("gpt-4o", -5, 1_000_000); !ok || got != 1000 {
		t.Errorf("mixed neg/pos tokens = %d ok=%v, want 1000 true", got, ok)
	}
}
