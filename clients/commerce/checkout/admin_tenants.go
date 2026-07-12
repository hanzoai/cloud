// Package checkout — admin tenant handlers backed by the hanzo/base store.
//
// Two handlers:
//
//	POST /_/commerce/tenants   SuperAdmin-only create (SuperAdmin: owner==admin
//	                           or isSuperAdmin — NOT org-level isAdmin)
//	GET  /_/commerce/providers tenant-admin list current tenant's providers
//
// Security invariants (Red-1 H-1 precedent):
//   - Cross-tenant probes MUST return a 404 with a byte-identical body to
//     the "tenant you belong to doesn't exist" case. No existence oracle.
//   - Tenant scope derives from the session's IAM claim (`owner` — the org
//     name). It is NEVER read from the request body or query string; if
//     the handler ever does, that is a trust-boundary collapse.
//   - Every mutation logs an admin_mutation audit entry. This slice logs to
//     stdout JSON via slog; a later slice moves it to a durable
//     commerce_admin_audit collection with 7-year retention.
package checkout

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth"
	"github.com/hanzoai/cloud/clients/commerce/middleware/iammiddleware"
	"github.com/hanzoai/cloud/clients/commerce/store"
)

// TenantAdminAPI wires the /_/commerce/* endpoints that drive the tenant
// record in the new hanzo/base-backed store. This is distinct from the
// legacy AdminAPI in admin.go — that one still speaks to the old Resolver;
// this one speaks to store.Store. Both coexist during migration.
type TenantAdminAPI struct {
	Store *store.Store
}

// NewTenantAdminAPI constructs the handler set.
func NewTenantAdminAPI(s *store.Store) *TenantAdminAPI {
	return &TenantAdminAPI{Store: s}
}

// ─── request / response DTOs ────────────────────────────────────────────

// createTenantRequest is the admin POST body. It is intentionally smaller
// than the in-store Tenant: an admin creating a row is not allowed to
// preset id or timestamps, and hostnames are normalized server-side.
type createTenantRequest struct {
	Name               string            `json:"name"`
	Hostnames          []string          `json:"hostnames"`
	Brand              store.BrandConfig `json:"brand"`
	IAM                store.IAMConfig   `json:"iam"`
	IDV                store.IDVConfig   `json:"idv"`
	Providers          []store.Provider  `json:"providers"`
	BDEndpoint         string            `json:"bd_endpoint"`
	ReturnURLAllowlist []string          `json:"return_url_allowlist"`
}

// createTenantResponse echoes the server-assigned id + timestamps. It does
// NOT echo full provider records back — the caller posted them; sending
// them again is noise.
type createTenantResponse struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// ─── handler: POST /_/commerce/tenants ──────────────────────────────────

// CreateTenant creates a new tenant row. Only PLATFORM SuperAdmins —
// SuperAdmin(): owner==admin or the explicit isSuperAdmin claim — may call
// this. Org owners (org-level isAdmin) and tenant-admins get 403;
// unauthenticated callers get 401.
func (a *TenantAdminAPI) CreateTenant(c *gin.Context) {
	// GetIAMClaims is non-nil by contract, so authentication is decided by
	// IsIAMAuthenticated (gateway identity present), not a nil check — an
	// anonymous caller must get 401, never the 403 that leaks admin-gating.
	if !iammiddleware.IsIAMAuthenticated(c) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	claims := iammiddleware.GetIAMClaims(c)
	if !isSuperadmin(claims) {
		c.JSON(http.StatusForbidden, gin.H{"error": "SuperAdmin required"})
		return
	}

	// Bounded body read — the JSONField max is 64KB per column; with six
	// JSON columns the sum is ~400KB. Cap at 512KB to leave headroom and
	// block a lazy DoS.
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 512*1024))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	defer c.Request.Body.Close()

	var req createTenantRequest
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON"})
		return
	}

	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name required"})
		return
	}

	tenant := &store.Tenant{
		Name:               req.Name,
		Hostnames:          req.Hostnames,
		Brand:              req.Brand,
		IAM:                req.IAM,
		IDV:                req.IDV,
		Providers:          req.Providers,
		BDEndpoint:         req.BDEndpoint,
		ReturnURLAllowlist: req.ReturnURLAllowlist,
	}

	if err := a.Store.Tenants.Create(tenant); err != nil {
		switch err {
		case store.ErrDuplicateTenant:
			c.JSON(http.StatusConflict, gin.H{"error": "tenant with that name already exists"})
		case store.ErrInvalidHostname:
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid hostname"})
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid tenant"})
		}
		return
	}

	// Audit: superadmin-created a tenant. Payload hashes (not contents)
	// protect secrets if any ever slip into the body — the audit log is
	// meant for forensic reconstruction, not for replaying creds.
	logAdminMutation(
		actorFromClaims(claims),
		"tenant.create",
		tenant.ID,
		nil,
		req,
	)

	c.JSON(http.StatusCreated, createTenantResponse{
		ID:      tenant.ID,
		Name:    tenant.Name,
		Created: tenant.Created,
		Updated: tenant.Updated,
	})
}

// ─── handler: GET /_/commerce/providers ─────────────────────────────────

// ListProviders returns the current tenant's provider list. The tenant is
// derived from the IAM `owner` claim — never from the body or query. If
// the authenticated user has no tenant row, the response is a byte-
// identical 404 to the cross-tenant-probe case — same status, same body.
func (a *TenantAdminAPI) ListProviders(c *gin.Context) {
	// Authentication via IsIAMAuthenticated (GetIAMClaims is non-nil by
	// contract); anonymous → 401, never the authorization 403.
	if !iammiddleware.IsIAMAuthenticated(c) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	claims := iammiddleware.GetIAMClaims(c)

	// Tenant-admin (org-scoped) OR SuperAdmin may list. A plain authenticated
	// user without an admin signal gets 403.
	if !isSuperadmin(claims) && !isTenantAdmin(claims) {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin role required"})
		return
	}

	owner := claims.Owner
	if owner == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	// Look up the tenant row by name. No cross-tenant query is possible:
	// we never accept a tenant_id parameter from the client.
	tenant, err := a.findTenantByOwner(owner)
	if err != nil {
		// Byte-identical 404 whether the tenant row doesn't exist at all
		// or the caller is asking about someone else's tenant. We never
		// reach a state where an authenticated user probes a different
		// tenant's providers — the owner claim pins scope.
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	// Defense-in-depth: project to public view that strips KMS paths —
	// the caller doesn't need to know where secrets live; an admin UI
	// just needs the name + enabled flag. Full KMS path is visible only
	// to the KMS-facing admin handlers, which are a separate route.
	projected := make([]providerListItem, 0, len(tenant.Providers))
	for _, p := range tenant.Providers {
		projected = append(projected, providerListItem{
			Name:    p.Name,
			Enabled: p.Enabled,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"tenant":    tenant.Name,
		"providers": projected,
	})
}

type providerListItem struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// findTenantByOwner resolves a tenant row by IAM owner claim. We use the
// store's list-by-filter indirectly via the `name` unique index — a
// dedicated repo method would be cleaner and comes in the next slice.
// This helper stays in the handler package because scoping-by-owner is a
// trust-boundary decision, not a raw persistence concern.
func (a *TenantAdminAPI) findTenantByOwner(owner string) (*store.Tenant, error) {
	// Linear scan, bounded by List(500). With a few dozen tenants at most
	// in prod, this is fine; a dedicated FindByName lands in the Orders
	// migration slice along with other name-keyed lookups.
	tenants, err := a.Store.Tenants.List(500, 0)
	if err != nil {
		return nil, err
	}
	for _, t := range tenants {
		if t.Name == owner {
			return t, nil
		}
	}
	return nil, store.ErrTenantNotFound
}

// ─── tenant JSON (public, read-only) — refactored to use store ──────────

// TenantJSONFromStore is the store-backed variant of TenantJSON. The legacy
// TenantJSON(Resolver) remains in tenant.go for callers that still hold a
// StaticResolver; new callers should use this one. Once every deployment
// is on base, TenantJSON becomes a thin wrapper around this function and
// StaticResolver is deleted.
func TenantJSONFromStore(s *store.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		host, err := readHostFromRequest(req)
		if err != nil {
			// Constant 404 body — never echo host.
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write(unknownTenant404)
			return
		}

		t, err := s.Tenants.FindByHostname(host)
		if err != nil {
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write(unknownTenant404)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=60")
		_ = json.NewEncoder(w).Encode(publicTenantDTO(t))
	})
}

// unknownTenant404 is the canonical 404 body. Stored as a byte slice so the
// cross-tenant-probe test can assert byte-identical responses.
var unknownTenant404 = []byte(`{"error":"unknown tenant"}`)

// readHostFromRequest extracts the Host header via the same normalization
// rules the repo uses. Kept separate so the handler can pre-reject
// malformed input without opening a DB connection.
func readHostFromRequest(req *http.Request) (string, error) {
	if req == nil {
		return "", store.ErrInvalidHostname
	}
	return normalizeHostForLookup(req.Host)
}

// normalizeHostForLookup mirrors checkout/tenant.go normalizeHost exactly,
// so the public endpoint and the admin endpoint agree on "what is a valid
// host header".
func normalizeHostForLookup(host string) (string, error) {
	h := normalizeHost(host) // from tenant.go
	if h == "" {
		return "", store.ErrInvalidHostname
	}
	return h, nil
}

// publicTenantDTO is the same public-only projection as toPublicView, but
// on the store.Tenant type so the new handler doesn't need to convert
// through the legacy Tenant struct. Credentials, bd_endpoint, and KMS
// references are ALL dropped.
func publicTenantDTO(t *store.Tenant) publicStoreView {
	enabled := make([]publicProvider, 0, len(t.Providers))
	for _, p := range t.Providers {
		if !p.Enabled {
			continue
		}
		enabled = append(enabled, publicProvider{Name: p.Name, Enabled: true})
	}
	return publicStoreView{
		Name:               t.Name,
		Brand:              t.Brand,
		IAM:                t.IAM, // only Issuer + ClientID are on IAMConfig
		IDV:                t.IDV,
		Providers:          enabled,
		ReturnURLAllowlist: t.ReturnURLAllowlist,
	}
}

// publicStoreView is the store-typed JSON shape served to anonymous
// clients. Matches publicView's shape from tenant.go but on store types so
// there is no accidental type-assertion seam.
type publicStoreView struct {
	Name               string            `json:"name"`
	Brand              store.BrandConfig `json:"brand"`
	IAM                store.IAMConfig   `json:"iam"`
	IDV                store.IDVConfig   `json:"idv"`
	Providers          []publicProvider  `json:"providers"`
	ReturnURLAllowlist []string          `json:"returnUrlAllowlist"`
}

// ─── role predicates ────────────────────────────────────────────────────

// isSuperadmin returns true ONLY for a Hanzo PLATFORM SuperAdmin:
// the explicit isSuperAdmin claim, or membership in the admin org. It does
// NOT trust org-level IsAdmin (an org owner carries it within their own org)
// nor any org-mintable role NAME like "superadmin"/"platform-admin" — either
// would let an org owner create tenants and perform cross-tenant ops (Red
// HIGH: org-admin → superadmin escalation). One robust predicate, defined on
// the claims type (auth.IAMClaims.SuperAdmin) and shared with the edge
// billing boundary. nil-safe.
func isSuperadmin(c *auth.IAMClaims) bool {
	return c.IsSuperAdmin()
}

// isTenantAdmin returns true for an ORG-level admin — the robust isAdmin claim
// IAM sets for an org's owner/admins, NOT a fragile, org-mintable role-name
// string (Red: role-name conflation). It is ORG-SCOPED: every handler that
// consults it derives the tenant from the caller's own `owner` claim, so it can
// only ever act on the caller's own org. A SuperAdmin is covered separately
// by isSuperadmin. nil-safe.
func isTenantAdmin(c *auth.IAMClaims) bool {
	return c != nil && c.IsAdmin
}

// actorFromClaims formats a stable human-readable actor string for the
// audit log. Subject is the stable IAM user id; email is appended for
// operator readability but is NOT the identity primary key.
func actorFromClaims(c *auth.IAMClaims) string {
	if c == nil {
		return "unknown"
	}
	if c.Email != "" {
		return c.Subject + " <" + c.Email + ">"
	}
	return c.Subject
}

// logAdminMutation is the stub audit logger. Stdout JSON via slog —
// production deployments ship slog to the central log pipeline. The full
// commerce_admin_audit collection (durable, 7-year retention, query API)
// lands in a separate slice after all writers migrate to this helper.
//
// Critically: the `before` / `after` payloads are hashed, not logged raw.
// A tenant create could carry KMS paths or provider identifiers that are
// sensitive in aggregate; a SHA-256 digest proves that "the same payload
// was posted" without exposing content.
func logAdminMutation(actor, action, target string, before, after any) {
	slog.Info("admin_mutation",
		"actor", actor,
		"action", action,
		"target", target,
		"before_sha", sha256JSON(before),
		"after_sha", sha256JSON(after),
		"ts", time.Now().UTC().Format(time.RFC3339),
	)
}

func sha256JSON(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		// A marshal error on an admin payload is itself a finding —
		// return a sentinel rather than propagate, since the audit
		// log is defensive-only.
		return "marshal_error"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
