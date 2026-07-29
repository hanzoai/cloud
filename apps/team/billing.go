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

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/team/wallet"
	"github.com/hanzoai/cloud/types"
)

// errNoOrg refuses a verified token that carries no tenant claim — such a token
// can name no org's data.
var errNoOrg = errors.New("token carries no org")

// billingService serves the wallet page + the plan/seats read. degraded is the
// fail-closed posture Mount resolved (no HS256 secret): a typed op cannot be
// wrapped by Mount's guard, so it asks for itself — see typed.go.
type billingService struct {
	accounts *accountStore
	commerce types.CommerceClient
	planEnt  func(context.Context, string) (map[string]any, error)
	secret   string
	degraded bool
}

func (b *billingService) register(app cloud.Router, guard guardFn) {
	// The group is built HERE so cmd/zipdoc can resolve the typed op's prefix from
	// this file — see bots.go for why.
	g := app.Group(teamPrefix)
	// TYPED: the plan read is a JSON value with a name. The two /ui routes below
	// stay untyped — they serve the embedded page's BYTES (html/js/css) under a
	// per-asset Content-Type, which is not a shape a typed Out can describe.
	zip.Get(g, "/billing/plan", b.readPlan)
	g.Get("/billing/ui", guard(b.ui))
	g.Get("/billing/ui/*", guard(b.ui))
}

// planInfo is the GET /v1/team/billing/plan body. Plan/Active come from the
// commerce entitlement (empty plan = unverifiable here — the page shows an
// honest dash, never a fabricated tier); Seats/Guests are the org's distinct
// active human members; GuestLimit is the plan's team.guests cap when the plan
// carries one.
type planInfo struct {
	// Plan is the licensed plan id, empty when it cannot be resolved here — an
	// honest dash on the page, never a fabricated tier.
	Plan string `json:"plan"`
	// Active is whether that plan's entitlement is live.
	Active bool `json:"active"`
	// Seats is the org's distinct active human members.
	Seats int `json:"seats"`
	// Guests is how many of those seats are guests.
	Guests int `json:"guests"`
	// GuestLimit is the plan's team.guests cap, when the plan carries one.
	GuestLimit int `json:"guestLimit,omitempty"`
	// UpgradeURL is where the page sends a caller who wants a bigger plan.
	UpgradeURL string `json:"upgradeUrl"`
}

// ReadPlan returns the plan and seat counts for the caller's OWN org, resolved
// from the VERIFIED team session token — never a client header. Seats and guests
// are the org's distinct active human members (a bot member is not a seat); the
// plan comes from the licensing entitlement and is empty when that read is
// unavailable, so the page shows an honest dash rather than a fabricated tier. A
// caller with no verified session gets 401, and a real seat-read failure is a
// 502 rather than a false "0 members".
//
// Response: {"plan": "pro", "active": true, "seats": 3, "guests": 1, "guestLimit": 3, "upgradeUrl": "https://billing.hanzo.ai"}
func (b *billingService) readPlan(ctx context.Context, _ *none) (*planInfo, error) {
	if b.degraded {
		return nil, unavailable()
	}
	_, org, err := sessionOf(ctx, b.secret)
	if err != nil {
		return nil, zip.ErrUnauthorized("sign in to view billing")
	}
	seats, guests, err := b.accounts.Seats(ctx, org)
	if err != nil {
		// A real seat-read failure must surface, not render as a false "0 members".
		return nil, zip.Errorf(http.StatusBadGateway, "seat count unavailable")
	}
	out := planInfo{Seats: seats, Guests: guests, UpgradeURL: upgradeURL}
	// Best-effort licensing read — the SAME seams entitle() gates login with.
	// An infra absence (nil commerce, resolution error) leaves plan empty.
	if b.commerce != nil {
		if ent, err := b.commerce.CheckEntitlement(ctx, org, productTeam); err == nil && ent != nil {
			out.Plan, out.Active = ent.Plan, ent.Active
			if b.planEnt != nil && ent.Plan != "" {
				if ents, err := b.planEnt(ctx, ent.Plan); err == nil {
					if limit, ok := intEntitlement(ents[guestCapKey]); ok {
						out.GuestLimit = limit
					}
				}
			}
		}
	}
	// Per-tenant plan/seat data must never be cached by an intermediary.
	noStore(ctx)
	return &out, nil
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
	org = t.Org()
	if org == "" {
		return "", "", errNoOrg
	}
	return t.Account, org, nil
}
