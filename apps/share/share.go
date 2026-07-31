package share

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

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

// routes — the ONE registration point. Both ops are zip TYPED ops, so the REST
// route, the OpenAPI document, the MCP tool and the CLI command all come from the
// one declaration; the bridge goes on FIRST because a typed op is handed only a
// context, so the validated principal is parked there.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	z := cloud.ZipApp(app)
	app.Group("/v1/share").Use(cloud.Bridge())

	zip.Post(z, "/v1/share/enable", o.enable, zip.WithOperationID("enableShare")) // provision + hand the CLI its credential
	zip.Get(z, "/v1/share", o.listShares, zip.WithOperationID("listShares"))      // the org's active shares (CLI + console)
}

// ops binds the service to share's typed handlers. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value, the only bound form
// cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// gate resolves the org and enforces the fail-closed 503 in ONE place before any
// handler touches the controller.
func gate(ctx context.Context, s *cloud.Service[state]) (string, error) {
	if !s.State.cl.configured() {
		return "", zip.Errorf(http.StatusServiceUnavailable, "share is not configured on this deployment")
	}
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	return org, nil
}

// Credential is what `hanzo share` needs to run the tunnel: the org's zrok
// account token, the controller endpoint to enable against, the namespace the
// public frontend lives in, and the URL shape for a friendly hint.
type Credential struct {
	// AccountToken is the org's zrok account token the CLI enables with.
	AccountToken string `json:"accountToken"`
	// Controller is the public zrok controller endpoint to enable against.
	Controller string `json:"controller"`
	// Namespace is the public frontend the share is published under.
	Namespace string `json:"namespace,omitempty"`
	// URLTemplate is the share URL shape, with the token placeholder left in.
	URLTemplate string `json:"urlTemplate"`
}

// enable provisions the caller org's share account and returns its credential.
//
// Provisioning is idempotent and the account is keyed deterministically off the
// org, so this is a pure function of the validated identity — a caller can only
// ever provision their OWN org's account.
//
// Response: {"accountToken": "tok_live", "controller": "https://share.hanzo.ai", "namespace": "public", "urlTemplate": "https://{token}.share.hanzo.ai"}
func (o ops) enable(ctx context.Context, _ *struct{}) (*Credential, error) {
	org, err := gate(ctx, o.s)
	if err != nil {
		return nil, err
	}
	tok, err := o.s.State.cl.token(ctx, org, true)
	if err != nil {
		o.s.Log.Warn("share provision failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "share controller unavailable")
	}
	return &Credential{
		AccountToken: tok,
		Controller:   publicController(),
		Namespace:    namespaceToken(),
		URLTemplate:  urlTemplate(),
	}, nil
}

// Share is one active share, projected for the CLI + console.
type Share struct {
	// Token is the share's zrok token, the key in its public URL.
	Token string `json:"token"`
	// URL is the share's public URL.
	URL string `json:"url"`
	// BackendMode is how the share serves its backend, e.g. proxy or web.
	BackendMode string `json:"backendMode,omitempty"`
	// Backend is the local endpoint the share proxies to.
	Backend string `json:"backend,omitempty"`
	// CreatedAt is the share's creation time, unix seconds.
	CreatedAt int64 `json:"createdAt,omitempty"`
}

// ShareList is the caller org's active-share roster.
type ShareList struct {
	// Shares is one row per active share across the org's environments.
	Shares []Share `json:"shares"`
}

// listShares returns the caller org's active shares.
//
// A READ degrades to an honest-empty list when the org has no share account yet
// or the controller is unreachable, so the console never error-toasts on load.
//
// Response: {"shares": [{"token": "abc123", "url": "https://abc123.share.hanzo.ai", "backendMode": "proxy", "backend": "http://localhost:3000", "createdAt": 1780000000}]}
func (o ops) listShares(ctx context.Context, _ *struct{}) (*ShareList, error) {
	s := o.s
	if !s.State.cl.configured() {
		return &ShareList{Shares: []Share{}}, nil
	}
	org, err := gate(ctx, s)
	if err != nil {
		return nil, err
	}
	tok, err := s.State.cl.token(ctx, org, false)
	if err != nil {
		// Not provisioned yet (errNoAccount) or controller down → no shares.
		// Honest empty, not 500 — the console never error-toasts on load.
		return &ShareList{Shares: []Share{}}, nil
	}
	ov, err := s.State.cl.overview(ctx, tok)
	if err != nil {
		s.Log.Warn("share overview failed", "org", org, "err", err)
		return &ShareList{Shares: []Share{}}, nil
	}
	out := make([]Share, 0, 8)
	for _, env := range ov.Environments {
		for _, sh := range env.Shares {
			out = append(out, Share{
				Token:       sh.Token,
				URL:         shareURL(sh.Token),
				BackendMode: sh.BackendMode,
				Backend:     sh.BackendProxyEndpoint,
				CreatedAt:   sh.CreatedAt,
			})
		}
	}
	return &ShareList{Shares: out}, nil
}

// shareURL renders a share token into its public URL via SHARE_URL_TEMPLATE.
func shareURL(token string) string {
	t := urlTemplate()
	return replaceToken(t, token)
}
