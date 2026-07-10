// embed.go ports console's app/embed-status/route.ts into the unified binary at
// GET /v1/embed-status (task #41). It answers ONE question for the console's
// data-product modules (Content Studio / ERP / Help Center): is this brand's shared
// embedded app provisioned and reachable, so the module can decide embed-vs-provision
// panel? A cross-origin browser can't read another origin's status (SOP + CORS), so
// this server route probes it once and returns an honest verdict.
//
// TWO real jobs (why it is a handler, not a vanishing proxy):
//
//   - ENTITLEMENT (server-authoritative). cms/erp/help are each a SINGLE shared
//     per-BRAND instance, so only a member of the owning brand org — or a global
//     admin — may frame them; a customer org gets the honest provision panel, never
//     a cross-tenant frame. The caller's org is the VALIDATED X-Org-Id (never a
//     browser claim); the owning org is the deployment brand.
//
//   - SSRF SAFETY. The probe target is ALWAYS `<app>.<brand-domain>` where the brand
//     is the deployment's OWN brand (deps.Brand, fixed at deploy) and app ∈
//     {cms,erp,help}. There is NO client-controlled host in the target at all — a
//     forged Host header can never steer this into probing an arbitrary origin
//     (strictly tighter than route.ts, which clamped a client Host).
package account

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// embedApps are the apps this route resolves and the in-app landing path each
// embeds. Mirrors embed-probe.ts EMBED_APPS (verified ground truth).
var embedApps = map[string]string{
	"cms":  "/admin",    // Payload admin
	"erp":  "/app",      // ERPNext desk
	"help": "/helpdesk", // Frappe Helpdesk
}

// embedBrandDomains maps a deployment brand to the registrable domain its shared
// apps live on. These are the app-hosting (`.cloud`) domains, DISTINCT from a
// brand's marketing domain (brand.go's Domain) — lux apps are on lux.cloud, not
// lux.network. Mirrors embed-probe.ts KNOWN_BRAND_DOMAINS/DEFAULT_BRAND_DOMAIN.
var embedBrandDomains = map[string]string{
	"hanzo": "hanzo.ai",
	"lux":   "lux.cloud",
	"zoo":   "zoo.cloud",
	"pars":  "pars.cloud",
}

const defaultEmbedDomain = "hanzo.ai"

// embedBrandDomain returns the app-hosting domain for a brand, defaulting to the
// hanzo domain for an unknown brand (the SSRF-safe fallback).
func embedBrandDomain(brand string) string {
	if d, ok := embedBrandDomains[strings.ToLower(strings.TrimSpace(brand))]; ok {
		return d
	}
	return defaultEmbedDomain
}

// embedUp classifies a probe HTTP status as "app is up": a 2xx/3xx (landing or SSO
// redirect) or an app-level 401/403 (running, wants login) is up; 404 (no such app)
// or any 5xx (the unprovisioned 502/503/504 state) is down. Mirrors embed-probe.ts
// isUp. A network/timeout error is handled by the caller as down.
func embedUp(status int) bool {
	if status >= 500 || status == 404 {
		return false
	}
	return status > 0
}

// embedStatusResp is the verdict the module reads. Mirrors the route.ts JSON.
type embedStatusResp struct {
	App       string `json:"app"`
	Origin    string `json:"origin"`
	EmbedURL  string `json:"embedUrl"`
	Reachable bool   `json:"reachable"`
	Entitled  bool   `json:"entitled"`
	Phase     string `json:"phase"`
}

// reachProbe reports whether an embed origin answers "up". It is a package var so
// the handler's entitlement + shaping logic is testable without a live network hop;
// Mount uses the real, time-boxed probe.
var reachProbe = liveReachProbe

// embedStatus is GET /v1/embed-status?app=cms|erp|help. Mirrors
// GET app/embed-status/route.ts.
func embedStatus(s *cloud.Service[state], c *zip.Ctx) error {
	cr, ok := resolveCaller(c, false) // validated; a customer org (owner set) is fine
	if !ok {
		return zip.ErrForbidden("sign in to continue")
	}
	app := strings.ToLower(strings.TrimSpace(c.Query("app")))
	landing, known := embedApps[app]
	if !known {
		return zip.ErrBadRequest("unknown embed app")
	}

	origin := "https://" + app + "." + embedBrandDomain(s.Brand)
	embedURL := origin + landing

	// SERVER-SIDE entitlement gate: a brand-owned app frames only for a member of the
	// owning brand org (cr.owner == deps.Brand) or a global admin. A non-entitled
	// caller NEVER receives the embed URL and we don't even probe — the module shows
	// the provision panel. This is the authoritative gate (the client check only
	// avoids a flash).
	entitled := (cr.owner != "" && cr.owner == strings.ToLower(strings.TrimSpace(s.Brand))) || c.IsAdmin()
	if !entitled {
		return c.JSON(http.StatusOK, embedStatusResp{App: app, Origin: origin, EmbedURL: "", Reachable: false, Entitled: false, Phase: "not-entitled"})
	}

	up := reachProbe(c.Context(), origin)
	phase := "not-provisioned"
	if up {
		phase = "ready"
	}
	return c.JSON(http.StatusOK, embedStatusResp{App: app, Origin: origin, EmbedURL: embedURL, Reachable: up, Entitled: true, Phase: phase})
}

// liveReachProbe does a time-boxed GET of the origin root. `redirect: manual` so an
// SSO 302 counts as up (we don't follow it — only the liveness signal is needed). A
// DNS failure / refused connection / timeout is down (app not provisioned yet).
func liveReachProbe(ctx context.Context, origin string) bool {
	cctx, cancel := context.WithTimeout(ctx, 4500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, origin, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "text/html")
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return embedUp(resp.StatusCode)
}

// noRedirectClient never follows a redirect — an SSO 302 is a liveness signal, not a
// hop to chase (chasing it could itself become an SSRF vector).
var noRedirectClient = &http.Client{
	Timeout:       5 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}
