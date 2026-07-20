package team

// The usage/wallet surface — /v1/team/billing. Two things, decomplected:
//
//   - GET /v1/team/billing/ui/*  serves the go:embed'd @hanzo/ui wallet page
//     (clients/team/wallet, a static Vite export) — session-gated, so the page
//     never renders for an anonymous caller.
//   - GET /v1/team/billing/plan  answers plan + seats for the caller's OWN org,
//     resolved from the VERIFIED session token (never a client header), through
//     the SAME commerce/plan seams the workspace-login gate uses (entitle.go).
//
// Money reads are deliberately NOT re-proxied here (one way): the page calls
// cloud's own /v1/billing/balance and /v1/usage/summary same-origin, where the
// identity boundary validates the hanzo_iam_token cookie the team OAuth
// callback set (middleware_identity cookieTokenNames) and pins the org from the
// VERIFIED owner claim — the per-tenant trust pattern the console billing proxy
// rides, with no second auth mechanism.

import (
	"context"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/clients/team/wallet"
	"github.com/hanzoai/cloud/types"
)

// errNoOrg refuses a verified token that carries no tenant claim — such a token
// can name no org's data.
var errNoOrg = errors.New("token carries no org")

// billingService serves the wallet page + the plan/seats read.
type billingService struct {
	accounts *accountStore
	commerce types.CommerceClient
	planEnt  func(context.Context, string) (map[string]any, error)
	secret   string
}

func (b *billingService) register(r zip.Router, guard guardFn) {
	r.Get("/billing/plan", guard(b.plan))
	r.Get("/billing/ui", guard(b.ui))
	r.Get("/billing/ui/*", guard(b.ui))
}

// planInfo is the GET /v1/team/billing/plan body. Plan/Active come from the
// commerce entitlement (empty plan = unverifiable here — the page shows an
// honest dash, never a fabricated tier); Seats/Guests are the org's distinct
// active human members; GuestLimit is the plan's team.guests cap when the plan
// carries one.
type planInfo struct {
	Plan       string `json:"plan"`
	Active     bool   `json:"active"`
	Seats      int    `json:"seats"`
	Guests     int    `json:"guests"`
	GuestLimit int    `json:"guestLimit,omitempty"`
	UpgradeURL string `json:"upgradeUrl"`
}

func (b *billingService) plan(c *zip.Ctx) error {
	_, org, err := orgPrincipal(c, b.secret)
	if err != nil {
		return zip.ErrUnauthorized("sign in to view billing")
	}
	seats, guests := b.accounts.Seats(c.Context(), org)
	out := planInfo{Seats: seats, Guests: guests, UpgradeURL: upgradeURL}
	// Best-effort licensing read — the SAME seams entitle() gates login with.
	// An infra absence (nil commerce, resolution error) leaves plan empty.
	if b.commerce != nil {
		if ent, err := b.commerce.CheckEntitlement(c.Context(), org, productTeam); err == nil && ent != nil {
			out.Plan, out.Active = ent.Plan, ent.Active
			if b.planEnt != nil && ent.Plan != "" {
				if ents, err := b.planEnt(c.Context(), ent.Plan); err == nil {
					if limit, ok := intEntitlement(ents[guestCapKey]); ok {
						out.GuestLimit = limit
					}
				}
			}
		}
	}
	// Per-tenant plan/seat data must never be cached by an intermediary.
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, out)
}

// ui serves the embedded wallet page: the exact asset when it exists, else
// index.html (the SPA shell). Session-gated — an anonymous caller gets 401,
// never the page. Fingerprinted assets/ cache hard; the shell never caches.
func (b *billingService) ui(c *zip.Ctx) error {
	if _, _, err := orgPrincipal(c, b.secret); err != nil {
		return zip.ErrUnauthorized("sign in to view billing")
	}
	root := wallet.FS()
	name := strings.TrimPrefix(path.Clean("/"+c.Param("*")), "/")
	if name == "" {
		name = "index.html"
	}
	body, err := fs.ReadFile(root, name)
	if err != nil {
		// Not a real file → the SPA shell (deep-link fallback), or 503 when the
		// bundle is absent — loud in staging, never a blank page.
		name = "index.html"
		if body, err = fs.ReadFile(root, name); err != nil {
			return zip.Errorf(http.StatusServiceUnavailable, "wallet UI not built (see clients/team/wallet)")
		}
	}
	if strings.HasPrefix(name, "assets/") {
		c.SetHeader("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		c.SetHeader("Cache-Control", "no-cache")
	}
	c.SetHeader("Content-Type", walletContentType(name))
	return c.Bytes(http.StatusOK, body)
}

// walletContentType maps an asset name to its MIME type by extension,
// defaulting to octet-stream.
func walletContentType(name string) string {
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// orgPrincipal resolves (account, org) from the request's VERIFIED session or
// workspace token (bearer or the HttpOnly account cookie), refusing a token
// that carries no org — the ONE token→tenant resolution the files and billing
// planes share.
func orgPrincipal(c *zip.Ctx, secret string) (account, org string, err error) {
	t, _, err := sessionToken(c, secret)
	if err != nil {
		return "", "", err
	}
	org, _ = t.Extra["org"].(string)
	if strings.TrimSpace(org) == "" {
		return "", "", errNoOrg
	}
	return t.Account, org, nil
}
