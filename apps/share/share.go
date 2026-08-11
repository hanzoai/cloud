package share

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// state is share's own data; shared deps live in the embedded cloud.Base.
type state struct {
	cl controller
}

// Mount wires the share surface onto app. Mirrors clients/zt: one line over the
// generic subsystem entrypoint; routes() is the ONE place routes are declared,
// Express-style via app.Group.
func Mount(app cloud.Router, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "share", build, routes)
}

func build(b cloud.Base) (state, error) {
	cl := newController()
	if !cl.configured() {
		b.Log.Warn("share surface mounted fail-closed: ZROK_ADMIN_TOKEN not set (all ops 503 until configured)",
			"controller", cl.base)
	} else {
		b.Log.Info("share surface mounted", "controller", cl.base, "brand", b.Brand, "env", b.Env)
	}
	return state{cl: cl}, nil
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
	zapp := cloud.ZipApp(app)
	// The collection ROOT is declared on the app with its whole path, never as an
	// EMPTY leaf on a /v1/share group: joinPath normalises "" to "/", which would
	// name /v1/share/ — a path this API has never served — in the document, the
	// operationId, the MCP tool and every generated SDK's URL.
	zip.Post(zapp, "/v1/share/enable", o.enable) // provision + hand the CLI its credential
	zip.Get(zapp, "/v1/share", o.listShares)     // the org's active shares (CLI + console)
}

// shareOps is the receiver the share ops hang off. A method value is the only bound
// form cmd/zipdoc can lift prose from, so ops are methods and not closures.
type shareOps struct{ s *cloud.Service[state] }

// noInput is the input of an op the URL fully addresses.
type noInput struct{}

// gate resolves the org and enforces the fail-closed 503 in ONE place before any
// handler touches the controller. The org comes from principal.OrgFrom — the
// typed-op reader of the validated org cloud.Bridge parked — never from an In
// field, which is caller-supplied and would make a tenant key the caller's to
// assert.
func gate(s *cloud.Service[state], ctx context.Context) (string, error) {
	if !s.State.cl.configured() {
		return "", zip.Errorf(http.StatusServiceUnavailable, "share is not configured on this deployment")
	}
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", principal.RefusedFrom(ctx)
	}
	return org, nil
}

// enableResp is what `hanzo share` needs to run the tunnel: the org's zrok
// account token, the controller endpoint to enable against, the namespace the
// public frontend lives in, and the URL shape for a friendly hint.
type enableResp struct {
	// AccountToken is the org's own tunnel-account credential. Treat it as a secret:
	// it is what the CLI enables an environment with.
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

// Enable provisions the caller org's tunnel account and returns the credential the
// `hanzo share` CLI needs to run a tunnel. It is idempotent: the account is keyed
// deterministically off the VALIDATED org, so a repeat call hands back the same
// account rather than creating a second one, and a caller can only ever provision
// their OWN org's account. 503 when the deployment has no share controller
// configured; 502 when that controller is unreachable.
func (o shareOps) enable(ctx context.Context, _ *noInput) (*enableResp, error) {
	s := o.s
	org, err := gate(s, ctx)
	if err != nil {
		return nil, err
	}
	tok, err := s.State.cl.token(ctx, org, true)
	if err != nil {
		s.Log.Warn("share provision failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "share controller unavailable")
	}
	return &enableResp{
		AccountToken: tok,
		Controller:   publicController(),
		Namespace:    namespaceToken(),
		URLTemplate:  urlTemplate(),
	}, nil
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

// ListShares returns the tunnel shares the caller's org currently has open, across
// every environment that org has enabled. It is a READ and it degrades honestly: an
// unconfigured deployment, an org that has not provisioned yet, and an unreachable
// controller all answer an EMPTY list at 200 rather than an error, so the console
// never error-toasts on load.
func (o shareOps) listShares(ctx context.Context, _ *noInput) (*sharesOut, error) {
	s := o.s
	if !s.State.cl.configured() {
		return &sharesOut{Shares: []shareView{}}, nil
	}
	org, err := gate(s, ctx)
	if err != nil {
		return nil, err
	}
	tok, err := s.State.cl.token(ctx, org, false)
	if err != nil {
		// Not provisioned yet (errNoAccount) or controller down → no shares.
		// Honest empty, not 500 — the console never error-toasts on load.
		return &sharesOut{Shares: []shareView{}}, nil
	}
	ov, err := s.State.cl.overview(ctx, tok)
	if err != nil {
		s.Log.Warn("share overview failed", "org", org, "err", err)
		return &sharesOut{Shares: []shareView{}}, nil
	}
	out := make([]shareView, 0, 8)
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
	return &sharesOut{Shares: out}, nil
}

// shareURL renders a share token into its public URL via SHARE_URL_TEMPLATE.
func shareURL(token string) string {
	t := urlTemplate()
	return replaceToken(t, token)
}
