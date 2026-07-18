package admin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/zap-proto/zip"
)

// The SuperAdmin usage-cap + promo control plane, twinning /v1/admin/flags. It owns
// no store: it FORWARDS to commerce (the billing source of truth) over the ONE
// service-token seam —
//
//	promos      → commerce /v1/platform/promo   (the admin-configured plan promo)
//	spend-caps  → commerce /v1/billing/spend-alerts (a per-org usage cap override)
//
// so admin.hanzo.ai configures the 50%-off promo and oversees/overrides any org's
// caps without a parallel model. Promo routes are platform-only (core.Guard); cap
// routes are org-scoped (core.GuardScoped) so a SuperAdmin targets any org via ?org=
// while a lesser admin is hard-pinned to their own.

// limitRoutes registers the promo + cap control plane. Called from routes().
func limitRoutes(app *zip.App, s *cloud.Service[core.State]) {
	// Platform plan promo — SuperAdmin only.
	app.Get("/v1/admin/promos", core.Guard(s, getPromo))
	app.Put("/v1/admin/promos", core.Guard(s, putPromo))

	// Per-org usage-cap oversight/override — SuperAdmin (any org via ?org=) or an org
	// admin (own org only). Reuses the customer's OWN self-service spend-alert CRUD,
	// so a platform override and a customer edit are the same rows.
	app.Get("/v1/admin/spend-caps", core.GuardScoped(s, listSpendCaps))
	app.Post("/v1/admin/spend-caps", core.GuardScoped(s, createSpendCap))
	app.Patch("/v1/admin/spend-caps/:id", core.GuardScoped(s, updateSpendCap))
	app.Delete("/v1/admin/spend-caps/:id", core.GuardScoped(s, deleteSpendCap))
}

// getPromo returns the current platform plan promo. X-Org-Id is the admin org —
// commerce stores the singleton in the reserved platform namespace regardless, and
// the service token is what passes commerce's RequirePlatformAdmin.
func getPromo(s *cloud.Service[core.State], c *zip.Ctx) error {
	raw, status, err := s.State.Commerce.Forward(c.Context(), http.MethodGet, "/v1/platform/promo", s.State.AdminOrg, nil)
	return relay(c, raw, status, err)
}

// putPromo upserts the platform plan promo from the SuperAdmin's {percentOff,start,
// end,plans,active} body — the ONE place the 50%-off offer is configured.
func putPromo(s *cloud.Service[core.State], c *zip.Ctx) error {
	raw, status, err := s.State.Commerce.Forward(c.Context(), http.MethodPut, "/v1/platform/promo", s.State.AdminOrg, c.Body())
	return relay(c, raw, status, err)
}

// listSpendCaps returns a target org's usage caps (spend-alerts + derived period
// spend/over/warn/resetsAt). The org is the SuperAdmin's ?org= or, for a scoped
// admin, their own — never a client-widened scope.
func listSpendCaps(s *cloud.Service[core.State], c *zip.Ctx) error {
	org, ok := targetOrg(s, c)
	if !ok {
		return core.Fail(c, "org required")
	}
	raw, status, err := s.State.Commerce.Forward(c.Context(), http.MethodGet, "/v1/billing/spend-alerts", org, nil)
	return relay(c, raw, status, err)
}

// createSpendCap sets a cap on a target org (platform override of a customer budget).
func createSpendCap(s *cloud.Service[core.State], c *zip.Ctx) error {
	org, ok := targetOrg(s, c)
	if !ok {
		return core.Fail(c, "org required")
	}
	raw, status, err := s.State.Commerce.Forward(c.Context(), http.MethodPost, "/v1/billing/spend-alerts", org, c.Body())
	return relay(c, raw, status, err)
}

// updateSpendCap edits a target org's cap by id (raise/lower the ceiling, flip enforce).
func updateSpendCap(s *cloud.Service[core.State], c *zip.Ctx) error {
	org, ok := targetOrg(s, c)
	if !ok {
		return core.Fail(c, "org required")
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return core.Fail(c, "cap id required")
	}
	raw, status, err := s.State.Commerce.Forward(c.Context(), http.MethodPatch, "/v1/billing/spend-alerts/"+url.PathEscape(id), org, c.Body())
	return relay(c, raw, status, err)
}

// deleteSpendCap removes a target org's cap by id.
func deleteSpendCap(s *cloud.Service[core.State], c *zip.Ctx) error {
	org, ok := targetOrg(s, c)
	if !ok {
		return core.Fail(c, "org required")
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return core.Fail(c, "cap id required")
	}
	raw, status, err := s.State.Commerce.Forward(c.Context(), http.MethodDelete, "/v1/billing/spend-alerts/"+url.PathEscape(id), org, nil)
	return relay(c, raw, status, err)
}

// targetOrg resolves which org a cap operation acts on: a SuperAdmin names it with
// ?org=; a scoped admin is hard-pinned to their own subtree (?org= ignored). Empty
// (false) when unresolvable, so the handler fails closed rather than acting on a
// guessed tenant.
func targetOrg(s *cloud.Service[core.State], c *zip.Ctx) (string, bool) {
	sc := core.ResolveScope(s, c)
	if sc.Super {
		if org := strings.TrimSpace(c.Query("org")); org != "" {
			return org, true
		}
		return "", false
	}
	if len(sc.Orgs) > 0 && strings.TrimSpace(sc.Orgs[0]) != "" {
		return sc.Orgs[0], true
	}
	return "", false
}

// relay surfaces commerce's OWN verdict in the /v1 envelope: a 2xx passes the raw
// JSON through as data (so the console decodes the exact SpendAlert/Promo shape), a
// non-2xx becomes an honest failure carrying commerce's status + message rather than
// masking a 400 validation as success.
func relay(c *zip.Ctx, raw []byte, status int, err error) error {
	if err != nil {
		return core.Fail(c, err.Error())
	}
	if status < 200 || status >= 300 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = http.StatusText(status)
		}
		return core.Fail(c, msg)
	}
	if len(raw) == 0 {
		return core.OK(c, map[string]any{"ok": true})
	}
	return core.OKRaw(c, json.RawMessage(raw), 0)
}
