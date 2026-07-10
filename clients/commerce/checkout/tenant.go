// Package checkout is the hosted multi-tenant checkout SPA embedded into
// commerce. The Vite build lives under ui/ and ships into ui/dist via the
// Dockerfile's checkout-build stage; embed.go exposes it to the Go binary.
//
// Security posture:
//   - Tenant resolution is exact-match on the Host header after port/case
//     normalization. Suffix-match tricks ("pay.example.com.evil.com") are
//     rejected by design.
//   - The public tenant JSON endpoint (GET /v1/commerce/tenant) exposes
//     ONLY branding, public IAM client ID + issuer, return-URL allowlist,
//     and the NAMES of enabled payment providers. No secrets, no KMS
//     paths, no client secrets, no webhook keys.
//   - Writes are scoped to the resolved tenant; cross-tenant mutations are
//     handled at the API layer (see deposits.go + admin/tenant handlers)
//     by cross-checking the IAM claim against the resolved tenant name.
package checkout

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
)

// ErrUnknownTenant is returned when the incoming Host header does not map
// to a configured tenant. Callers should respond with 404 (never 500) and
// MUST NOT echo the Host back in the response body — that would be a free
// fingerprinting primitive for attackers.
var ErrUnknownTenant = errors.New("checkout: unknown tenant")

// Tenant is the full tenant config. Only the fields tagged `json:"..."`
// (no `-` suffix) flow to the public GET /v1/commerce/tenant endpoint via
// the PublicView projection — everything else (secrets, backend creds) is
// dropped before serialization.
type Tenant struct {
	// Name is the stable tenant identifier (also the Hanzo IAM org name /
	// commerce organization.Name). Used to scope KMS paths, DB queries,
	// and IAM owner-claim comparisons.
	Name string `json:"name"`

	// Brand controls what the SPA renders.
	Brand Brand `json:"brand"`

	// IAM points the SPA at the correct identity provider and app. Only
	// the public fields (Issuer, ClientID) project to PublicView; the rest
	// stay server-side and are used by the checkout API handlers.
	IAM IAMConfig `json:"iam"`

	// IDV (identity verification) is opaque to the server: the SPA just
	// reads it, renders a redirect/prompt, and trusts the IDV provider's
	// completion webhook (handled by the tenant's back end, not here).
	IDV IDVConfig `json:"idv"`

	// Providers is the per-tenant enable/disable list for payment
	// providers. The PublicView projection strips all credential fields
	// before emission.
	Providers []Provider `json:"providers"`

	// ReturnURLAllowlist bounds the ?return= query param the SPA may
	// bounce to. Prevents open-redirect phishing pivots.
	ReturnURLAllowlist []string `json:"returnUrlAllowlist"`

	// Square carries the PUBLIC Square Web Payments config (application id +
	// location id + environment) the SPA's card iframe needs. Resolved from the
	// IAM org via the single test-mode authority (see payment.SquarePublicConfig)
	// so the browser tokenizes with the exact application commerce will charge.
	// Every field is public — no secret ever crosses this boundary.
	Square SquarePublic `json:"square"`

	// Backend tells the checkout API how to proxy deposit intents. For
	// the example tenant this resolves to BD; other tenants supply
	// their own URL. Kind is an opaque free-form label ("bd", "custom").
	Backend BackendConfig `json:"-"`
}

// SquarePublic is the public Square config surfaced to the checkout SPA. It
// mirrors payment.SquarePublic minus the ledger `live` flag (the SPA drives its
// sandbox-vs-prod script off Environment). All fields are public.
type SquarePublic struct {
	ApplicationID string `json:"applicationId"`
	LocationID    string `json:"locationId"`
	Environment   string `json:"environment"`
}

// Brand controls visible white-label surface.
type Brand struct {
	DisplayName  string `json:"displayName"`
	LogoURL      string `json:"logoUrl"`
	PrimaryColor string `json:"primaryColor"`
}

// IAMConfig: Issuer + ClientID are OIDC-public (they already ship in the
// well-known discovery doc). ClientSecret and AdminSecret are server-side
// and MUST NOT project to PublicView.
type IAMConfig struct {
	Issuer       string `json:"issuer"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"-"`
	AdminSecret  string `json:"-"`
}

// IDVConfig: opaque to commerce. Provider is a label the SPA switches on;
// Endpoint is the URL the SPA opens for the IDV flow. RequiredFields is a
// whitelist of claims the tenant requires from the IDV provider.
type IDVConfig struct {
	Provider       string   `json:"provider"`
	Endpoint       string   `json:"endpoint"`
	RequiredFields []string `json:"requiredFields,omitempty"`
}

// Provider is a payment provider the tenant has enabled. All credential
// fields are `json:"-"` so json.Marshal drops them — the PublicView
// projection relies on this.
type Provider struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`

	// The following are server-side only. KMSPath is the KMS folder that
	// holds this provider's credentials; AccessToken et al are optional
	// fallbacks for bootstrap / local dev.
	KMSPath             string `json:"-"`
	ApplicationID       string `json:"-"`
	AccessToken         string `json:"-"`
	PrivateKey          string `json:"-"`
	WebhookSignatureKey string `json:"-"`
}

// BackendConfig describes where the checkout API forwards deposit
// intents. Backend.Kind is tenant-specific.
// For generic tenants, Kind="custom" and URL is the tenant's own endpoint.
type BackendConfig struct {
	Kind string
	URL  string
}

// ─── Resolver ────────────────────────────────────────────────────────────

// Resolver resolves a Host header to a Tenant. The default implementation
// is a StaticResolver driven by a hostname→Tenant map; in production the
// resolver is backed by the commerce organization model (hosts stored on
// the organization record).
type Resolver interface {
	Resolve(host string) (Tenant, error)
}

// StaticResolver is an in-memory Resolver used for tests and bootstrap.
// Hostname keys are lowercased and assumed to have no port suffix; the
// Resolver handles normalization before lookup.
type StaticResolver struct {
	mu      sync.RWMutex
	hostMap map[string]Tenant
}

// NewStaticResolver copies the input map and lowercases keys so the caller
// need not normalize ahead of time.
func NewStaticResolver(hosts map[string]Tenant) *StaticResolver {
	m := make(map[string]Tenant, len(hosts))
	for h, t := range hosts {
		m[strings.ToLower(strings.TrimSpace(h))] = t
	}
	return &StaticResolver{hostMap: m}
}

// Set replaces the tenant for a host (or inserts). Host is normalized.
// Intended for runtime reconfig (admin API) — thread-safe.
func (r *StaticResolver) Set(host string, t Tenant) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hostMap[strings.ToLower(strings.TrimSpace(host))] = t
}

// Resolve returns the Tenant for host, or ErrUnknownTenant. Exact-match
// only: suffix spoofing ("pay.example.com.evil.com") does not match.
func (r *StaticResolver) Resolve(host string) (Tenant, error) {
	h := normalizeHost(host)
	if h == "" {
		return Tenant{}, ErrUnknownTenant
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if t, ok := r.hostMap[h]; ok {
		return t, nil
	}
	return Tenant{}, ErrUnknownTenant
}

// normalizeHost strips :port and lowercases. Any malformed input —
// embedded whitespace, control bytes, empty string — is rejected (returns
// ""). We deliberately do NOT trim: a well-formed Host header has none,
// and silently repairing input turns a bug into an attack surface.
func normalizeHost(host string) string {
	if host == "" {
		return ""
	}
	for i := 0; i < len(host); i++ {
		b := host[i]
		if b <= 0x20 || b == 0x7f {
			return "" // whitespace / DEL / control byte → reject
		}
	}
	// Strip :port. IPv6 literals start with '[' and are not supported as
	// tenant keys; they'd fall through to ErrUnknownTenant anyway.
	if i := strings.IndexByte(host, ':'); i >= 0 && !strings.HasPrefix(host, "[") {
		host = host[:i]
	}
	return strings.ToLower(host)
}

// ─── Public tenant JSON ──────────────────────────────────────────────────

// publicView is the JSON shape served at GET /v1/commerce/tenant. It's a
// deliberate projection — anything not explicitly listed here cannot leak.
type publicView struct {
	Name               string           `json:"name"`
	Brand              Brand            `json:"brand"`
	IAM                publicIAM        `json:"iam"`
	IDV                IDVConfig        `json:"idv"`
	Providers          []publicProvider `json:"providers"`
	ReturnURLAllowlist []string         `json:"returnUrlAllowlist"`
	Square             SquarePublic     `json:"square"`
}

type publicIAM struct {
	Issuer   string `json:"issuer"`
	ClientID string `json:"clientId"`
}

type publicProvider struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// toPublicView drops every field that is not safe to expose to an
// anonymous client. This is the single source of truth for "what does the
// SPA get to see" — tenant_test.go pins this contract.
func toPublicView(t Tenant) publicView {
	enabled := make([]publicProvider, 0, len(t.Providers))
	for _, p := range t.Providers {
		if !p.Enabled {
			continue
		}
		enabled = append(enabled, publicProvider{Name: p.Name, Enabled: true})
	}
	return publicView{
		Name:      t.Name,
		Brand:     t.Brand,
		IAM:       publicIAM{Issuer: t.IAM.Issuer, ClientID: t.IAM.ClientID},
		IDV:       t.IDV,
		Providers: enabled,
		// Always a non-nil slice so the JSON is `[]` not `null`. A nil
		// allowlist otherwise serializes as null, and the pay SPA iterates it
		// (firstOrigin/isAllowedReturnUrl) → "e is not iterable", which breaks
		// the entire /onboard page ("Could not load tenant configuration").
		ReturnURLAllowlist: append([]string{}, t.ReturnURLAllowlist...),
		Square:             t.Square,
	}
}

// TenantJSON returns an http.Handler for GET /v1/commerce/tenant. The
// handler:
//  1. Extracts and normalizes the Host header.
//  2. Resolves to a Tenant (or 404 with no Host echo on failure).
//  3. Projects through toPublicView and JSON-encodes.
//
// Cache policy: short public cache (60s) to absorb SPA boot storms
// without leaking per-user state. Tenant config is not user-specific.
func TenantJSON(r Resolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		t, err := r.Resolve(req.Host)
		if err != nil {
			// Do NOT include the Host in the 404 body. Attackers probing
			// for tenant existence should see a constant response.
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"unknown tenant"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=60")
		_ = json.NewEncoder(w).Encode(toPublicView(t))
	})
}
