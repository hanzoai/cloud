package affiliate

import (
	"github.com/gin-gonic/gin"

	payoutcron "github.com/hanzoai/cloud/clients/commerce/cron/payout/contributor"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/contributor"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
)

// registerContributor allows a user to register as an OSS contributor.
func registerContributor(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	contrib := contributor.New(db)
	if err := json.Decode(c.Request.Body, contrib); err != nil {
		http.Fail(c, 400, "Failed to decode request body", err)
		return
	}

	if contrib.GitLogin == "" || contrib.GitEmail == "" {
		http.Fail(c, 400, "gitLogin and gitEmail are required", nil)
		return
	}

	// Check if contributor already exists with this git login
	existing := contributor.New(db)
	if _, ok, _ := contributor.Query(db).Filter("GitLogin=", contrib.GitLogin).First(existing); ok {
		http.Render(c, 200, existing)
		return
	}

	contrib.Active = true
	if err := contrib.Create(); err != nil {
		http.Fail(c, 500, "Failed to create contributor", err)
		return
	}

	c.Writer.Header().Add("Location", c.Request.URL.Path+"/"+contrib.Id())
	http.Render(c, 201, contrib)
}

// contributorCreate returns the admin create override for contributor CRUD.
func contributorCreate(r *rest.Rest) func(*gin.Context) {
	return func(c *gin.Context) {
		if !r.CheckPermissions(c, "create") {
			return
		}

		org := middleware.GetOrganization(c)
		db := datastore.New(org.Namespaced(c))
		contrib := contributor.New(db)

		if err := json.Decode(c.Request.Body, contrib); err != nil {
			r.Fail(c, 400, "Failed to decode request body", err)
			return
		}

		if contrib.GitLogin == "" {
			r.Fail(c, 400, "gitLogin is required", nil)
			return
		}

		if err := contrib.Create(); err != nil {
			r.Fail(c, 500, "Failed to create contributor", err)
			return
		}

		c.Writer.Header().Add("Location", c.Request.URL.Path+"/"+contrib.Id())
		r.Render(c, 201, contrib)
	}
}

// contributorGetByLogin looks up a contributor by their git login.
func contributorGetByLogin(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	login := c.Params.ByName("login")

	contrib := contributor.New(db)
	if _, ok, _ := contributor.Query(db).Filter("GitLogin=", login).First(contrib); !ok {
		http.Fail(c, 404, "No contributor found with login: "+login, nil)
		return
	}

	http.Render(c, 200, contrib)
}

// getEarnings returns a contributor's earnings summary.
func getEarnings(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("contributorid")

	contrib := contributor.New(db)
	if err := contrib.GetById(id); err != nil {
		http.Fail(c, 404, "Contributor not found: "+id, err)
		return
	}

	http.Render(c, 200, gin.H{
		"contributorId": contrib.Id(),
		"gitLogin":      contrib.GitLogin,
		"totalEarned":   contrib.TotalEarned,
		"totalPending":  contrib.TotalPending,
		"linesAuthored": contrib.TotalLinesAuthored,
		"payoutMethod":  contrib.PayoutMethod,
		"currency":      contrib.Currency,
		"lastPaid":      contrib.LastPaid,
	})
}

// getAttributions returns a contributor's SBOM attributions.
func getAttributions(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("contributorid")

	contrib := contributor.New(db)
	if err := contrib.GetById(id); err != nil {
		http.Fail(c, 404, "Contributor not found: "+id, err)
		return
	}

	http.Render(c, 200, contrib.Attributions)
}

// createSBOMEntry creates or updates an SBOM entry for a component.
func createSBOMEntry(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	entry := contributor.NewSBOM(db)
	if err := json.Decode(c.Request.Body, entry); err != nil {
		http.Fail(c, 400, "Failed to decode request body", err)
		return
	}

	if entry.Component == "" {
		http.Fail(c, 400, "component is required", nil)
		return
	}

	if err := entry.Create(); err != nil {
		http.Fail(c, 500, "Failed to create SBOM entry", err)
		return
	}

	http.Render(c, 201, entry)
}

// listSBOMEntries returns all SBOM entries.
func listSBOMEntries(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	entries := make([]contributor.SBOMEntry, 0)
	if _, err := contributor.QuerySBOM(db).GetAll(&entries); err != nil {
		http.Fail(c, 500, "Failed to query SBOM entries", err)
		return
	}

	http.Render(c, 200, entries)
}

// getSBOMEntry returns a single SBOM entry by ID.
func getSBOMEntry(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("sbomid")

	entry := contributor.NewSBOM(db)
	if err := entry.GetById(id); err != nil {
		http.Fail(c, 404, "SBOM entry not found: "+id, err)
		return
	}

	http.Render(c, 200, entry)
}

// updateSBOMEntry updates an existing SBOM entry.
func updateSBOMEntry(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("sbomid")

	entry := contributor.NewSBOM(db)
	if err := entry.GetById(id); err != nil {
		http.Fail(c, 404, "SBOM entry not found: "+id, err)
		return
	}

	if err := json.Decode(c.Request.Body, entry); err != nil {
		http.Fail(c, 400, "Failed to decode request body", err)
		return
	}

	if err := entry.Update(); err != nil {
		http.Fail(c, 500, "Failed to update SBOM entry", err)
		return
	}

	http.Render(c, 200, entry)
}

// calculatePayouts runs the payout algorithm and returns results.
func calculatePayouts(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req struct {
		TotalRevenueCents int64                     `json:"totalRevenueCents"`
		ComponentRevenue  map[string]int64          `json:"componentRevenue"`
		Config            *contributor.PayoutConfig `json:"config,omitempty"`
	}

	if err := json.Decode(c.Request.Body, &req); err != nil {
		http.Fail(c, 400, "Failed to decode request body", err)
		return
	}

	if req.TotalRevenueCents <= 0 {
		http.Fail(c, 400, "totalRevenueCents must be positive", nil)
		return
	}

	// Load all active, verified contributors
	contributors := make([]contributor.Contributor, 0)
	if _, err := contributor.Query(db).Filter("Active=", true).Filter("Verified=", true).GetAll(&contributors); err != nil {
		http.Fail(c, 500, "Failed to query contributors", err)
		return
	}

	cfg := contributor.DefaultConfig()
	if req.Config != nil {
		cfg = *req.Config
	}

	summary := contributor.CalculatePayouts(
		req.TotalRevenueCents,
		contributors,
		req.ComponentRevenue,
		cfg,
	)

	http.Render(c, 200, summary)
}

// previewPayouts returns a dry-run of what payouts would look like
// using the default config and current SBOM revenue data.
func previewPayouts(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	// Load SBOM entries to build component revenue map
	entries := make([]contributor.SBOMEntry, 0)
	if _, err := contributor.QuerySBOM(db).GetAll(&entries); err != nil {
		http.Fail(c, 500, "Failed to query SBOM entries", err)
		return
	}

	// Same real per-component attribution the payout cron uses: usage, then
	// lines, then equal weight across shipped components.
	componentRevenue := contributor.ComponentWeightsFromSBOM(entries)
	var totalRevenue int64
	for _, w := range componentRevenue {
		totalRevenue += w
	}

	// Load contributors
	contributors := make([]contributor.Contributor, 0)
	if _, err := contributor.Query(db).Filter("Active=", true).Filter("Verified=", true).GetAll(&contributors); err != nil {
		http.Fail(c, 500, "Failed to query contributors", err)
		return
	}

	summary := contributor.CalculatePayouts(
		totalRevenue,
		contributors,
		componentRevenue,
		contributor.DefaultConfig(),
	)

	http.Render(c, 200, summary)
}

// executePayouts runs the OSS contributor payout EXECUTOR for the caller's org
// namespace: it computes the period's revenue, calculates the 25% split via
// SBOM attribution, and disburses each allocation — CreditGrants, queued
// Stripe transfers, and on-chain HUSD transfers (PayoutMethod="crypto").
//
// Dry-run by default (logs what would happen, pays nothing). Pass
// ?execute=true to actually disburse. Crypto payouts fail closed
// (ErrHUSDNotConfigured) unless HUSD_TOKEN_ADDRESS + HUSD_TREASURY_KEY are set
// from KMS, so an unconfigured deploy never silently mis-pays. Intended for
// admin / CronJob invocation (mirrors billing auto-recharge/run-all).
//
//	POST /payouts/execute              (dry-run)
//	POST /payouts/execute?execute=true (live disbursement)
func executePayouts(c *gin.Context) {
	org := middleware.GetOrganization(c)
	dryRun := c.Query("execute") != "true"

	if err := payoutcron.Payout(org.Namespaced(c), payoutcron.Config{
		Namespace: org.Namespace(),
		DryRun:    dryRun,
	}); err != nil {
		http.Fail(c, 500, "contributor payout failed", err)
		return
	}

	http.Render(c, 200, gin.H{"ok": true, "namespace": org.Namespace(), "dryRun": dryRun})
}
