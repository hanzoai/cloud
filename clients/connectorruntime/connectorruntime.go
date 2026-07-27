package connectorruntime

import (
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// Mount wires the native single-connector execution surface onto the cloud
// binary, per HIP-0126 / HIP-0106:
//
//	POST /v1/automations/connectors/:id/run   run one connector action in-process
//
// This is the native replacement for the standalone ActivePieces Node engine's
// /v1/auto/pieces/{piece}/run — same {action,auth,props} -> {ok,output,error}
// contract, executed in goja in-process (no `auto` pod). It is org-gated: only
// a validated principal may run a connector, and the caller's resolved
// credential travels in the request `auth`. The route is DISTINCT from
// automations' GET /v1/automations/connectors (the catalogue), so the two
// subsystems compose without collision.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("connectorruntime.Mount: nil app")
	}
	log := deps.Logger
	app.Post("/v1/automations/connectors/:id/run", runHandler)
	if log != nil {
		log.New("subsystem", "connectorruntime").Info(
			"connector runtime mounted", "connectors", len(Connectors()))
	}
	return nil
}

// runReq mirrors the ActivePieces piece-run body.
type runReq struct {
	Action string         `json:"action"`
	Auth   any            `json:"auth"`
	Props  map[string]any `json:"props"`
}

// runResp mirrors the ActivePieces piece-run response. A connector-level
// failure is ok:false with a message (HTTP 200) — an unknown connector or a
// missing action is a 4xx, matching the old engine's infra-vs-piece split.
type runResp struct {
	Ok     bool   `json:"ok"`
	Output any    `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

func runHandler(c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	id := c.Param("id")
	if !Has(id) {
		return zip.Errorf(http.StatusNotFound, "unknown connector %q", id)
	}
	var body runReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if body.Action == "" {
		return zip.Errorf(http.StatusUnprocessableEntity, "action is required")
	}
	out, err := Run(c.Context(), org, id, body.Action, body.Auth, body.Props)
	if err != nil {
		// The action ran but failed (or the action name is unknown): surface as
		// a piece-level failure, not an infra 5xx — the caller inspects ok.
		return c.JSON(http.StatusOK, runResp{Ok: false, Error: err.Error()})
	}
	return c.JSON(http.StatusOK, runResp{Ok: true, Output: out})
}
