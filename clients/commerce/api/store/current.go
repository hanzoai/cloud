package store

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
)

// getCurrent returns the authenticated org's default store. It resolves the store
// from the org's OWN namespaced datastore (NOT the shared system DB) and lazily
// provisions it on first use. The admin dashboard and the content storefront edge
// (clients/content/storefront.go currentStore) both call GET /store/current to
// resolve the active store id; reading the shared system DB always returned the
// phantom "default", which the storefront edge treats as unconfigured — so it
// skipped publishing product imagery for every org.
func getCurrent(c *gin.Context) {
	org, ok := middleware.GetOrganizationOK(c)
	if !ok {
		// No authenticated org in context: return a minimal default so the dashboard
		// still renders, exactly as before. The route now runs behind tokenRequired,
		// so real callers (dashboard IAM, storefront service token) always have an org.
		c.JSON(http.StatusOK, defaultStorePayload())
		return
	}

	// Resolve the caller org's OWN store — the SAME per-org namespace the write path
	// (listing.go orgNamespacedDB, rest.newEntity) persists into.
	db := datastore.NewNamespaced(org.Namespaced(c))

	// Return the org's existing store if it already has one (any slug).
	var stores []store.Store
	if _, err := store.New(db).Query().All().Limit(1).GetAll(&stores); err == nil && len(stores) > 0 {
		c.JSON(http.StatusOK, gin.H{"store": stores[0]})
		return
	}

	// First authenticated visit for an org with no store yet: lazily provision the
	// canonical default store (idempotent, org-scoped, no payment creds) so the
	// storefront edge resolves a REAL store id instead of the phantom "default".
	s, err := store.EnsureDefault(db)
	if err != nil {
		c.JSON(http.StatusOK, defaultStorePayload())
		return
	}
	c.JSON(http.StatusOK, gin.H{"store": s})
}

// defaultStorePayload is the minimal fallback used only when no org context is
// present or provisioning fails — never the normal path for an authenticated org.
func defaultStorePayload() gin.H {
	return gin.H{
		"store": gin.H{
			"id":               "default",
			"name":             "Default Store",
			"default_currency": "usd",
			"currencies":       []string{"usd"},
		},
	}
}
