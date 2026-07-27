package share

import (
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
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

// routes — the ONE registration point, Express-ish .Group. Static before :param.
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/share")
	g.Post("/enable", cloud.Handle(s, enable)) // provision + hand the CLI its credential
	g.Get("", cloud.Handle(s, listShares))     // the org's active shares (CLI + console)
}

// gate resolves the org and enforces the fail-closed 503 in ONE place before any
// handler touches the controller.
func gate(s *cloud.Service[state], c *zip.Ctx) (string, error) {
	if !s.State.cl.configured() {
		return "", zip.Errorf(http.StatusServiceUnavailable, "share is not configured on this deployment")
	}
	org, ok := principal.Org(c)
	if !ok {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	return org, nil
}

// enableResp is what `hanzo share` needs to run the tunnel: the org's zrok
// account token, the controller endpoint to enable against, the namespace the
// public frontend lives in, and the URL shape for a friendly hint.
type enableResp struct {
	AccountToken string `json:"accountToken"`
	Controller   string `json:"controller"`
	Namespace    string `json:"namespace,omitempty"`
	URLTemplate  string `json:"urlTemplate"`
}

// enable provisions (idempotently) the caller org's zrok account and returns the
// credential. The account is keyed deterministically off the org, so this is a
// pure function of the validated identity — a caller can only ever provision
// their OWN org's account.
func enable(s *cloud.Service[state], c *zip.Ctx) error {
	org, err := gate(s, c)
	if err != nil {
		return err
	}
	tok, err := s.State.cl.token(c.Context(), org, true)
	if err != nil {
		s.Log.Warn("share provision failed", "org", org, "err", err)
		return zip.Errorf(http.StatusBadGateway, "share controller unavailable")
	}
	return c.JSON(http.StatusOK, enableResp{
		AccountToken: tok,
		Controller:   publicController(),
		Namespace:    namespaceToken(),
		URLTemplate:  urlTemplate(),
	})
}

// shareView is one active share, projected for the CLI + console.
type shareView struct {
	Token       string `json:"token"`
	URL         string `json:"url"`
	BackendMode string `json:"backendMode,omitempty"`
	Backend     string `json:"backend,omitempty"`
	CreatedAt   int64  `json:"createdAt,omitempty"`
}

// listShares returns the org's active shares. A READ degrades to an honest-empty
// list when the controller is unreachable, so the console never error-toasts.
func listShares(s *cloud.Service[state], c *zip.Ctx) error {
	if !s.State.cl.configured() {
		return c.JSON(http.StatusOK, map[string]any{"shares": []shareView{}})
	}
	org, err := gate(s, c)
	if err != nil {
		return err
	}
	tok, err := s.State.cl.token(c.Context(), org, false)
	if err != nil {
		// Not provisioned yet (errNoAccount) or controller down → no shares.
		// Honest empty, not 500 — the console never error-toasts on load.
		return c.JSON(http.StatusOK, map[string]any{"shares": []shareView{}})
	}
	ov, err := s.State.cl.overview(c.Context(), tok)
	if err != nil {
		s.Log.Warn("share overview failed", "org", org, "err", err)
		return c.JSON(http.StatusOK, map[string]any{"shares": []shareView{}})
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
	return c.JSON(http.StatusOK, map[string]any{"shares": out})
}

// shareURL renders a share token into its public URL via SHARE_URL_TEMPLATE.
func shareURL(token string) string {
	t := urlTemplate()
	return replaceToken(t, token)
}
