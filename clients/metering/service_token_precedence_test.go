// Copyright © 2026 Hanzo AI. MIT License.

package metering_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerceinproc"
	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/metering"
	"github.com/hanzoai/commerce/datastore"
	"github.com/hanzoai/commerce/db"
	commercemid "github.com/hanzoai/commerce/middleware"
	"github.com/hanzoai/commerce/middleware/svcorg"
	"github.com/hanzoai/commerce/util/bit"
	"github.com/hanzoai/commerce/util/permission"
)

// seedInMemDatastore installs a fresh in-memory default datastore so the
// service-token branch's svcorg.Resolve (GetOrCreate on the org table) succeeds
// and the request reaches the handler — the funded path — rather than 503ing on a
// resolve failure. Mirrors svcorg/resolver_test.go's setup.
func seedInMemDatastore(t *testing.T) {
	t.Helper()
	mgr, err := db.NewManager(&db.Config{DataDir: t.TempDir(), EnableVectorSearch: false, IsDev: true})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	shared, err := mgr.Org("system") // Organization is a global kind → shared default DB.
	if err != nil {
		t.Fatalf("mgr.Org(system): %v", err)
	}
	datastore.SetDefaultDB(shared)
	t.Cleanup(func() { datastore.SetDefaultDB(nil) })
}

// stampIAMForS2S mimics IAMTokenRequired's effect for an IN-PROCESS S2S dispatch:
// the metering/billing request carries X-Org-Id (to select the tenant namespace),
// so IAMTokenRequired stamps iam_authenticated=true; it carries NO X-User-Permissions
// (an S2S call has no per-user scope), so the minted permissions are ZERO. This is
// the exact context state under which the bug fired.
func stampIAMForS2S(c *gin.Context) {
	c.Set("iam_authenticated", true)
	c.Set("permissions", bit.Field(0))
	c.Next()
}

// TestInProcMeteringDispatch_ServiceTokenAuthPath exercises the FULL in-process
// metering dispatch end to end — the metering client → commerceinproc self-routing
// transport → the real commerce middleware chain (IAM stamp + Admin-masked
// TokenRequired) → the balance handler → back to a gate verdict — with finance NOT
// co-resident (the split-deploy / live topology whose balance read goes over the wire
// instead of the finance-direct seam). It proves that a low-scope IAM principal's
// debit gate is processed via the SERVICE-TOKEN path: a funded org is ALLOWED and an
// unfunded org gets a clean 402 (ErrInsufficientBalance), never a 403/500 from the
// scope gate. The client is fail-CLOSED, so a scope-gate rejection would surface as a
// generic error (→ 503), not a silent allow — the prepaid gate stays fail-closed.
func TestInProcMeteringDispatch_ServiceTokenAuthPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const svc = "svc-token-metering-xyz789"
	t.Setenv("COMMERCE_SERVICE_TOKEN", svc)
	seedInMemDatastore(t)
	svcorg.Invalidate("funded")
	svcorg.Invalidate("broke")

	// finance NOT co-resident → the metering balance read goes over commerceinproc,
	// through the real commerce middleware chain, exactly like the topology that 500'd.
	finance.Publish(nil)

	eng := gin.New()
	eng.Use(stampIAMForS2S)
	grp := eng.Group("v1/billing")
	grp.Use(commercemid.TokenRequired(permission.Admin)) // the money group: /balance, /tier, /usage
	grp.GET("/balance", func(c *gin.Context) {
		avail := int64(0)
		if c.Query("user") == "funded" {
			avail = 500
		}
		c.JSON(http.StatusOK, gin.H{"user": c.Query("user"), "currency": "usd", "available": avail})
	})
	commerceinproc.SetHandler(eng)
	t.Cleanup(func() { commerceinproc.SetHandler(nil) })

	m, err := metering.New(metering.Config{
		BaseURL:    commerceinproc.PlaceholderBase,
		Token:      svc,
		Org:        "hanzo",
		FailOpen:   false, // fail-closed: a scope-gate 403 must surface, never silently allow.
		HTTPClient: commerceinproc.Client(0),
	})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}

	// Funded low-scope IAM principal: the debit gate must ALLOW — the service-token
	// path read the balance. A non-nil error here means the Admin scope gate 403'd the
	// S2S read (the bug).
	if err := m.Authorize(context.Background(), metering.AuthInput{User: "funded", Org: "funded"}); err != nil {
		t.Fatalf("funded org must be authorized via the service-token path, got %v", err)
	}

	// Unfunded principal: a clean 402 (ErrInsufficientBalance), NOT a 500/scope error.
	err = m.Authorize(context.Background(), metering.AuthInput{User: "broke", Org: "broke"})
	if !errors.Is(err, metering.ErrInsufficientBalance) {
		t.Fatalf("unfunded org must map to 402 insufficient_balance, got %v "+
			"(a generic error means the read 403/500'd instead of returning a balance)", err)
	}
}
