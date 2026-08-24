package team

// The usage/wallet surface — /v1/team/billing. Two things, decomplected:
//
//   - GET /v1/team/billing/ui/*  serves the go:embed'd @hanzo/ui wallet page
//     (clients/team/wallet, a static Vite export) — session-gated, so the page
//     never renders for an anonymous caller.
//   - GET /v1/team/billing/plan  answers plan + seats for the caller's OWN org,
//     resolved from the VERIFIED session token (never a client header), through
//     the SAME commerce/plan clients the workspace-login gate uses (entitle.go).
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
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/types"
)

// The prose for the two UNTYPED /ui routes. They serve the embedded page's
// bytes under a per-asset Content-Type, which no typed Out can carry, so zipdoc
// has nothing to lift and without this they publish an operationId and nothing
// else. The plan read documents itself from its own doc comment.
//
// The path keys are the FIBER patterns exactly as registered below; the `*` is
// what the document renders as {wildcard1}.
func init() {
	// A route that CANNOT be typed still owes its BODIES. Both of these published
	// an operationId and nothing else, which a document consumer cannot tell from a
	// route that returns none — so every generated SDK offered the wallet page with
	// no stated answer at all. openapi.Bytes is the honest declaration for a body
	// that is not JSON, and it names no component, so neither adds a bare property.
	//
	// The two media types differ because the ROUTES differ. The bare page resolves
	// an empty capture to index.html unconditionally (ui), so text/html is what it
	// always serves. The wildcard's type is derived per asset from the name
	// (walletContentType), so naming one would be false for the rest; empty Type is
	// OpenAPI's "opaque bytes", which is the true statement about a route whose
	// media type varies. TestDeclaredMediaTypesAreTheServedOnes reads both off the
	// live wire rather than trusting these two literals.
	openapi.Register("/v1/team/billing/ui", http.MethodGet, nil,
		openapi.Bytes{Type: "text/html; charset=utf-8"})
	openapi.Register("/v1/team/billing/ui/*", http.MethodGet, nil, openapi.Bytes{})
	openapi.Describe("/v1/team/billing/ui", http.MethodGet,
		"Open the wallet page",
		"Serves the usage-and-wallet page the Team front links to — HTML, not JSON. It is a "+
			"static React build compiled into this binary, so there is no upstream to be down "+
			"and no build step at request time.\n\n"+
			"SESSION-GATED: without a verified team session token — bearer, else the HttpOnly "+
			"account cookie — the caller gets 401 and not one byte of the page, so an anonymous "+
			"browser meets a refusal rather than a shell that then fails to load anything.\n\n"+
			"The page is markup only. It reads money SAME-ORIGIN from cloud's own balance and "+
			"usage endpoints, which pin the org from the IAM cookie the team OAuth callback set "+
			"— nothing under this path proxies a money read, so there is no second auth "+
			"mechanism here to get wrong. A deployment whose page was never built answers 503 "+
			"naming the missing bundle, never a blank 200.")
	openapi.Describe("/v1/team/billing/ui/*", http.MethodGet,
		"Load an asset of the wallet page",
		"Serves one file of the embedded wallet bundle — a content-hashed script or stylesheet "+
			"under assets/, an icon, or the page shell itself.\n\n"+
			"A path that names NO REAL FILE falls back to the shell instead of 404ing, which is "+
			"what makes a deep link into the page's own routes survive a hard refresh. So a 200 "+
			"here is not proof the asset exists — a typo answers HTML.\n\n"+
			"assets/ is immutable for a year (the names carry the content hash); the shell is "+
			"no-cache, so a deploy is picked up on the next load. Gated exactly like the page: "+
			"401 without a verified session, 503 when the bundle was never built.")
}

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
	ident    *identity
	degraded bool
}

func (b *billingService) register(app cloud.Router, guard guardFn) {
	// The group is built HERE so cmd/zipdoc can resolve the typed op's prefix from
	// this file — see bots.go for why.
	g := app.Group(teamPrefix)
	// TYPED: the plan read is a JSON value with a name.
	zip.Get(g, "/billing/plan", b.readPlan)
	// UNTYPED: both answer the embedded bundle's BYTES, and the wildcard is blocked
	// a second time by being GREEDY — the registry and the router spell that segment
	// differently, so Fold would refuse the WHOLE document rather than publish one
	// bad path. untypedByDesign cites both, and the byte replies are DECLARED below.
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
	_, org, err := sessionOf(ctx, b.ident)
	if err != nil {
		return nil, zip.ErrUnauthorized("sign in to view billing")
	}
	seats, guests, err := b.accounts.Seats(ctx, org)
	if err != nil {
		// A real seat-read failure must surface, not render as a false "0 members".
		return nil, zip.Errorf(http.StatusBadGateway, "seat count unavailable")
	}
	out := planInfo{Seats: seats, Guests: guests, UpgradeURL: upgradeURL}
	// Best-effort licensing read — the SAME clients entitle() gates login with.
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
	if _, _, err := orgPrincipal(c, b.ident); err != nil {
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

// orgPrincipal resolves (account, org) from the request's VERIFIED caller
// (identity.who — an IAM access token, else team's own HS256 token, on a header or
// a cookie), refusing one that carries no org — the ONE credential→tenant
// resolution the files and billing planes share.
func orgPrincipal(c *zip.Ctx, id *identity) (account, org string, err error) {
	cl, err := id.who(c)
	if err != nil {
		return "", "", err
	}
	if cl.org == "" {
		return "", "", errNoOrg
	}
	return cl.account, cl.org, nil
}
