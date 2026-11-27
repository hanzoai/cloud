package share

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

// The codes a share call answers under when the tunnel controller behind it
// cannot do what was asked. Both are 503, never 502: Cloudflare, in front of
// api.hanzo.ai, replaces an origin's 502 and 504 with its own "error code: 502"
// page, dropping cloud's body and request id (#765).
const (
	// codeControllerUnavailable: the controller could not be reached, or failed.
	codeControllerUnavailable = "share_controller_unavailable"
	// codeAccountOutOfReach: the controller holds the account under a credential
	// this deployment does not derive, so a rotation cannot reach it.
	codeAccountOutOfReach = "share_account_out_of_reach"
)

// state is share's own data; shared deps live in the embedded cloud.Base.
type state struct {
	cl    controller
	plans host.PlanChecker
}

// Mount wires the share surface onto app. Mirrors clients/zt: one line over the
// generic subsystem entrypoint; routes() is the ONE place routes are declared,
// Express-style via app.Group.
func Use(app cloud.Router, deps cloud.Deps) error {
	plans, _ := deps.Commerce.(host.PlanChecker)
	return cloud.Use(app, deps, "share", func(b cloud.Base) (state, error) {
		cl := newController()
		if !cl.configured() {
			b.Log.Warn("share surface mounted fail-closed: ZROK_ADMIN_TOKEN not set (all ops 503 until configured)",
				"controller", cl.base)
		} else {
			b.Log.Info("share surface mounted", "controller", cl.base, "brand", b.Brand, "env", b.Env)
		}
		return state{cl: cl, plans: plans}, nil
	}, routes)
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes — the ONE registration point. Static before :param.
func routes(app cloud.Router, s *cloud.Service[state]) {
	// A typed op receives only a context, so the validated org has to be parked
	// there. cloud.Bridge parks it, and the COMPOSER installs it, not this
	// subsystem: the fused host once at its root (serve.go), and a plugin
	// program's constructor likewise. An install here would hang middleware on
	// prefixes with no routes beneath them, a program zip refuses to compose.
	o := shareOps{s: s}
	g := app.Group("/v1/share")
	g.Post("/enable", o.enable) // provision + hand the CLI its credential
	g.Post("/rotate", o.rotate) // replace that credential, ending what the old one enabled
	g.Get("", o.listShares)     // the org's active shares (CLI + console)
}

// shareOps is the receiver the share ops hang off. A method value is the only bound
// form cmd/zipdoc can lift prose from, so ops are methods and not closures.
type shareOps struct{ s *cloud.Service[state] }

// gate resolves the org the call acts in and enforces the fail-closed 503 in ONE
// place before any handler touches the controller. The org comes from
// principal.Acting — the typed-op reader of the validated org cloud.Bridge
// parked — never from an In field, which is caller-supplied and would make a
// tenant key the caller's to assert.
func gate(s *cloud.Service[state], ctx context.Context) (string, error) {
	if !s.State.cl.configured() {
		return "", zip.Errorf(http.StatusServiceUnavailable, "share is not configured on this deployment")
	}
	return principal.Acting(ctx)
}

// holding is one tunnel account on the controller: an org's own, which its
// members share, or — person set — one person's in a held org.
//
// The two are separate KINDS, not two spellings of one string. The org's account
// is derived from the org's name, and anyone can found an org with nearly any
// name, so a person's account must not be reachable from a name: person is set
// only by holders, from who the caller is, and the controller derives a person's
// credential under labels and an address no org's derivation produces
// (httpController.credential).
type holding struct {
	org    string
	person string // hex SHA-256 of the key the person's account is under; "" for the org's own account
}

// String names the account in a log line without the subject behind it.
func (h holding) String() string {
	if h.person == "" {
		return h.org
	}
	return h.org + "/person:" + h.person[:12]
}

// holders names the tunnel accounts a caller acting in org holds, the one to
// provision first.
//
// An org's account is the org's: its members share the org's tunnels. A held org
// (authz.IsHeldOrg) is not a team — every self-serve signup lands in it beside the
// platform's staff — so there every caller's account is their own, an admin of
// the org and a SuperAdmin included. It is keyed on the full hash of who they are
// as one person (principal.SelfFrom): their IAM row's id, which their sign-in
// and every API key of theirs resolve to alike, so one person holds one account
// whichever credential they call with.
//
// An account provisioned before was keyed on the subject of the credential that
// asked: a sign-in's subject is the id, so its account is already this one, and
// an API key's is the person's row, `<owner>/<name>`. That one follows the id's
// here. It keeps working — enable hands it back to a person who has no account
// under the id, by either credential — the list shows its tunnels, and a
// rotation ends it with the id's. The controller cannot move an account to
// another address, so it stays where it is until it is rotated.
//
// Nothing serves a held org's own account to anyone, so the credential that
// lists and opens one person's shells is never handed to another; rotate is the
// one call that touches it.
func holders(ctx context.Context, org string) ([]holding, error) {
	if !authz.IsHeldOrg(org) {
		return []holding{{org: org}}, nil
	}
	p, err := iam.SelfFrom(ctx)
	if err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, zip.ErrForbidden("a tunnel in " + org + " is a person's own, and this call names no person")
	}
	out := []holding{held(org, p.ID)}
	if p.Row != "" && p.Row != p.ID {
		out = append(out, held(org, p.Row))
	}
	return out, nil
}

// held is the account of one person in a held org, under key.
func held(org, key string) holding {
	sum := sha256.Sum256([]byte(key))
	return holding{org: org, person: hex.EncodeToString(sum[:])}
}

// enableResp is what `hanzo share` needs to run the tunnel: the org's zrok
// account token, the controller endpoint to enable against, the namespace the
// public frontend lives in, and the URL shape for a friendly hint.
type enableResp struct {
	// AccountToken is the caller's tunnel-account credential: their org's, or in a
	// held org their own. Treat it as a secret: it is what the CLI enables an
	// environment with.
	AccountToken string `json:"accountToken"`
	// Controller is the public controller endpoint the CLI enables against.
	Controller string `json:"controller"`
	// Namespace is the public frontend a share is published into, when the
	// deployment names one.
	Namespace string `json:"namespace,omitempty"`
	// URLTemplate is the shape a share token expands to, so the CLI can print the
	// resulting URL without asking again.
	URLTemplate string `json:"urlTemplate"`
}

// Enable provisions the caller's tunnel account — their org's, or in a held org,
// where every self-serve signup lands, their own — and returns the credential the
// `hanzo share` CLI needs to run a tunnel. It is idempotent: the account is keyed
// deterministically off the VALIDATED org and, in a held org, the caller's
// attested subject, so a repeat call hands back the same account rather than
// creating a second one, and a caller can only ever provision their OWN. 503 when
// the deployment has no share controller configured; 503 when that controller is
// unreachable.
func (o shareOps) enable(ctx context.Context, _ *cloud.Unit) (*enableResp, error) {
	s := o.s
	org, err := gate(s, ctx)
	if err != nil {
		return nil, err
	}
	hs, err := holders(ctx, org)
	if err != nil {
		return nil, err
	}
	// READ-BACK FIRST, and it is free. An org that already has an account is
	// fetching its own credential, which costs nobody anything — so it is neither
	// gated nor billed, and a tenant at zero balance can always run the tunnels
	// they already paid to enable. Only a MISS of every account the caller holds
	// goes on to provision, and it provisions the first.
	for _, h := range hs {
		tok, err := s.State.cl.token(ctx, h, false)
		if err == nil {
			return &enableResp{
				AccountToken: tok,
				Controller:   publicController(),
				Namespace:    namespaceToken(),
				URLTemplate:  urlTemplate(),
			}, nil
		}
		if !errors.Is(err, errNoAccount) {
			s.Log.Warn("share lookup failed", "account", h, "err", err)
			return nil, controllerDown("no tunnel account was read or made, and nothing was charged")
		}
	}
	h := hs[0]

	// A miss means we are about to mint an account on the PLATFORM's admin
	// credential. Authorized before that happens. See meter.go.
	ch, err := afford(s, ctx, org)
	if err != nil {
		return nil, cloud.Denied(err)
	}
	defer ch.Release()
	tok, err := s.State.cl.token(ctx, h, true)
	if err != nil {
		s.Log.Warn("share provision failed", "account", h, "err", err)
		return nil, controllerDown("your tunnel account was not made, and nothing was charged")
	}
	charge(ch)
	return &enableResp{
		AccountToken: tok,
		Controller:   publicController(),
		Namespace:    namespaceToken(),
		URLTemplate:  urlTemplate(),
	}, nil
}

// rotated is what a rotation ended.
type rotated struct {
	// Ended is how many environments the rotated accounts had enabled. Each is
	// ended with its shares, so a machine that ran a tunnel on an account enables
	// again with the new credential.
	Ended int `json:"ended"`
	// Pending says what this call could not rotate, and is empty when it rotated
	// everything. In a held org an admin rotates their own account first and the
	// org's own after it, so the org's failing to rotate is said here rather than
	// keeping them from their own; calling again tries it again.
	Pending string `json:"pending,omitempty"`
}

// Rollout, once for each held org: POST /v1/share/rotate, by an admin of that org
// or a SuperAdmin acting in it. It rotates the org's own tunnel account, which no
// call in a held org serves (holders), so no credential to it issued earlier
// opens anything; it needs no tunnel, account or subject of the caller's, and a
// repeat rotates again and ends nothing.

// RotateShare replaces the credential of a tunnel account and ends every
// environment enabled with it, shares and all, so a credential handed out before
// — to a member who has left, or to anyone who read it — opens nothing from now
// on. The account is kept: enable hands its holder the new credential.
//
// In a customer's org the account is the org's, which its members share, and an
// admin of the org or a SuperAdmin rotates it. In a held org each person's
// account is their own, and they rotate it — every account they hold, the one
// an API key of theirs was handed before included, so none stays live. An admin
// of the org or a SuperAdmin then rotates the org's own account, which no call
// there hands out, whether or not they hold an account of their own; when that
// one cannot be rotated, `pending` says so and their own are rotated still.
//
// An account never provisioned has nothing to rotate and counts 0. 503 when the
// deployment has no share controller configured; 503 when that controller is
// unreachable, or holds the caller's account under a credential this deployment
// does not derive — out of a rotation's reach, so it is refused rather than
// reported done.
func (o shareOps) rotate(ctx context.Context, _ *cloud.Unit) (*rotated, error) {
	s := o.s
	org, err := gate(s, ctx)
	if err != nil {
		return nil, err
	}
	who := zip.CallerOf(ctx)
	admin := who.OrgAdmin || who.Admin
	var own []holding
	switch {
	case !authz.IsHeldOrg(org) && !admin:
		return nil, zip.ErrForbidden("the tunnel account of " + org + " is rotated by an admin of the org")
	case !authz.IsHeldOrg(org):
		own = []holding{{org: org}}
	default:
		hs, err := holders(ctx, org)
		if err != nil && !admin {
			return nil, err
		}
		own = hs
	}
	out := &rotated{}
	for _, h := range own {
		n, err := s.State.cl.rotate(ctx, h)
		out.Ended += n
		if refused := rotateRefusal(s, h, n, err); refused != nil {
			return nil, refused
		}
		if err == nil {
			s.Log.Info("share account rotated", "account", h, "ended", n, "by", who.User)
		}
	}
	if authz.IsHeldOrg(org) && admin {
		h := holding{org: org}
		n, err := s.State.cl.rotate(ctx, h)
		out.Ended += n
		switch refused := rotateRefusal(s, h, n, err); {
		case refused != nil:
			out.Pending = "the tunnel account of " + org + " itself was not rotated: " + refused.Error() + "; call again to retry it"
		case err == nil:
			s.Log.Info("share account rotated", "account", h, "ended", n, "by", who.User)
		}
	}
	return out, nil
}

// rotateRefusal is how a rotation of h that answered err is told: nothing for a
// rotation done or an account never provisioned, and a coded 503 for one the
// controller could not rotate.
func rotateRefusal(s *cloud.Service[state], h holding, ended int, err error) error {
	switch {
	case err == nil, errors.Is(err, errNoAccount):
		return nil
	case errors.Is(err, errForeign):
		s.Log.Warn("share rotate refused", "account", h, "err", err)
		return &zip.HTTPError{
			Status: http.StatusServiceUnavailable,
			Code:   codeAccountOutOfReach,
			Msg:    "the share controller holds this tunnel account under a credential this deployment does not derive; it was not rotated",
		}
	}
	s.Log.Warn("share rotate failed", "account", h, "ended", ended, "err", err)
	return controllerDown("this tunnel account was not rotated")
}

// controllerDown is the answer when the tunnel controller could not be reached or
// failed: 503, code share_controller_unavailable, saying what did not happen.
// The controller's own words go to the log, not to the caller.
//
// Not a 502. A 502 is an answer nobody reads: Cloudflare replaces it with its own
// "error code: 502" page. In #765 every enable failed that way for as long as the
// cloud's cluster had no controller to resolve, and the person retrying it nine
// times was never told why, or that it was not their account.
func controllerDown(outcome string) error {
	return &zip.HTTPError{
		Status: http.StatusServiceUnavailable,
		Code:   codeControllerUnavailable,
		Msg: "share: Hanzo's tunnel controller could not be reached, so " + outcome +
			". This is on Hanzo's side, not your sign-in or setup; try again in a few minutes",
	}
}

// shareView is one active share, projected for the CLI + console.
type shareView struct {
	// Token is the share's own identifier, the leaf of its public URL.
	Token string `json:"token"`
	// URL is the share's public address, rendered from the deployment's URL template.
	URL string `json:"url"`
	// BackendMode is how the tunnel serves the backend, e.g. proxy or web.
	BackendMode string `json:"backendMode,omitempty"`
	// Backend is the local endpoint the share proxies to.
	Backend string `json:"backend,omitempty"`
	// CreatedAt is when the share was opened, unix seconds.
	CreatedAt int64 `json:"createdAt,omitempty"`
}

// sharesOut is the answer of the share list.
type sharesOut struct {
	// Shares is the org's active shares — empty rather than absent when there are
	// none, or when the controller cannot be reached.
	Shares []shareView `json:"shares"`
}

// ListShares returns the tunnel shares the caller's account currently has open —
// their org's, or in a held org their own — across every environment it has
// enabled. It is a READ and it degrades honestly: an
// unconfigured deployment, an org that has not provisioned yet, and an unreachable
// controller all answer an EMPTY list at 200 rather than an error, so the console
// never error-toasts on load.
func (o shareOps) listShares(ctx context.Context, _ *cloud.Unit) (*sharesOut, error) {
	s := o.s
	if !s.State.cl.configured() {
		return &sharesOut{Shares: []shareView{}}, nil
	}
	org, err := gate(s, ctx)
	if err != nil {
		return nil, err
	}
	hs, err := holders(ctx, org)
	if err != nil {
		return nil, err
	}
	out := make([]shareView, 0, 8)
	for _, h := range hs {
		tok, err := s.State.cl.token(ctx, h, false)
		if err != nil {
			// Not provisioned (errNoAccount) or controller down → no shares from
			// it. Honest empty, not 500 — the console never error-toasts on load.
			if !errors.Is(err, errNoAccount) {
				s.Log.Warn("share lookup failed", "org", org, "err", err)
			}
			continue
		}
		ov, err := s.State.cl.overview(ctx, tok)
		if err != nil {
			s.Log.Warn("share overview failed", "account", h, "err", err)
			continue
		}
		for _, env := range ov.Environments {
			for _, sh := range env.Shares {
				out = append(out, shareView{
					Token:       sh.Token,
					URL:         shareURL(sh.Token),
					BackendMode: sh.BackendMode,
					Backend:     sh.BackendProxyEndpoint,
					CreatedAt:   sh.CreatedAt,
				})
			}
		}
	}
	return &sharesOut{Shares: out}, nil
}

// shareURL renders a share token into its public URL via SHARE_URL_TEMPLATE.
func shareURL(token string) string {
	t := urlTemplate()
	return replaceToken(t, token)
}
