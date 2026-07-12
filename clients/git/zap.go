package git

import (
	"errors"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// zap.go is git's ZAP transport — the SECOND transport over the ONE control-plane
// core (core.go). It establishes the canonical pattern every Hanzo subsystem
// copies to "go ZAP": there is no per-service ZAP server and no gRPC.
//
// # THE STANDARD (copy this for the next service)
//
// The cloud binary already serves ONE ZAP-over-WebSocket plane: zapface.Handler,
// mounted at /zap in serve.go. It is a pure transport bridge — it replays every
// inbound ZAP frame as an in-process HTTP request against the SAME Fiber app,
// so ANY /v1 route is automatically reachable as a ZAP procedure. A service does
// NOT stand up its own zapclient.Server; doing so would be a redundant parallel
// path (one-and-only-one-way).
//
// The zapface bridge unwraps a `{status, msg, data}` envelope as the ZAP result.
// A service's REST handlers return RAW JSON (the git CLI + REST clients want the
// resource verbatim), so a service exposes ZAP procedures as a THIN, envelope-
// shaping adapter layer at:
//
//	POST /v1/<service>/zap/<procedure>
//
// mapped by zapface from the ZAP method "<service>/zap/<procedure>". Each adapter
// resolves the org EXACTLY as the REST handler does (principal.Org →
// X-Org-Id, minted by the identity middleware from the browser's replayed
// credential), then calls the SAME core func the REST handler calls, and wraps
// the result in the envelope. So the REST handler and the ZAP procedure are two
// thin adapters over ONE core func each — the business logic lives once.
//
// git's procedures (all over the core in core.go):
//
//	git/zap/createRepo  -> coreCreate   git/zap/deleteRepo -> coreDelete
//	git/zap/listRepos   -> coreList     git/zap/usage      -> coreUsage
//	git/zap/getRepo     -> coreGet
//
// The next service (e.g. crm, prompts) copies this file's shape: one mountZAP
// registering /v1/<service>/zap/<proc> envelope adapters over its own core funcs.
// Nothing else — the /zap plane does the rest.

// mountZAP registers git's ZAP procedure adapters. Called from routes(). The
// procedures are ordinary /v1 routes; the shared /zap plane turns them into ZAP
// procedures for the browser/service ZAP client.
func mountZAP(app *zip.App, s *cloud.Service[state]) {
	app.Post("/v1/git/zap/createRepo", cloud.Handle(s, zapCreate))
	app.Post("/v1/git/zap/listRepos", cloud.Handle(s, zapList))
	app.Post("/v1/git/zap/getRepo", cloud.Handle(s, zapGet))
	app.Post("/v1/git/zap/deleteRepo", cloud.Handle(s, zapDelete))
	app.Post("/v1/git/zap/usage", cloud.Handle(s, zapUsage))
}

// ---- envelope ----

// okEnvelope is the success shape the zapface bridge unwraps (data → the ZAP
// result). It is the SAME shape every /v1 ZAP-facing handler returns, so the
// bridge stays fully generic.
func okEnvelope(c *zip.Ctx, data any) error {
	return c.JSON(http.StatusOK, map[string]any{"status": "ok", "msg": "", "data": data})
}

// errEnvelope reports a handler error in the envelope with the given HTTP status
// (the bridge maps non-ok status → a ZAP dispatch error the client observes).
func errEnvelope(c *zip.Ctx, status int, msg string) error {
	return c.JSON(status, map[string]any{"status": "error", "msg": msg})
}

// zapErr maps a core sentinel error to an envelope response — the ZAP twin of
// the REST status mapping (createErr etc.), so both transports agree on which
// failure is a 400/404/409 while returning their own wire shape.
func zapErr(c *zip.Ctx, err error) error {
	switch {
	case errors.Is(err, errBadInput):
		return errEnvelope(c, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "git: invalid input: "))
	case errors.Is(err, errConflict):
		return errEnvelope(c, http.StatusConflict, "repo name already exists in this scope")
	case errors.Is(err, errNotFound):
		return errEnvelope(c, http.StatusNotFound, "repo not found")
	default:
		return errEnvelope(c, http.StatusInternalServerError, err.Error())
	}
}

// ---- procedure adapters (thin — one core call each) ----

// zapProcReq is the JSON body a ZAP client sends to a git procedure. Which
// fields matter depends on the procedure (createRepo reads all; listRepos reads
// none; getRepo/deleteRepo read name). Org + project scope come from the
// request identity, NEVER the body — the body cannot widen the caller's org.
type zapProcReq struct {
	Name        string `json:"name"`
	Project     string `json:"project"`
	Description string `json:"description"`
}

func zapCreate(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return errEnvelope(c, http.StatusForbidden, "X-Org-Id required")
	}
	var body zapProcReq
	if err := c.Bind(&body); err != nil {
		return errEnvelope(c, http.StatusBadRequest, "invalid body")
	}
	view, err := coreCreate(s, c.Context(), org, projectScope(c), createReq{
		Name: body.Name, Project: body.Project, Description: body.Description,
	})
	if err != nil {
		return zapErr(c, err)
	}
	return okEnvelope(c, view)
}

func zapList(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return errEnvelope(c, http.StatusForbidden, "X-Org-Id required")
	}
	out, err := coreList(s, c.Context(), org, projectScope(c))
	if err != nil {
		return zapErr(c, err)
	}
	return okEnvelope(c, out)
}

func zapGet(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return errEnvelope(c, http.StatusForbidden, "X-Org-Id required")
	}
	var body zapProcReq
	if err := c.Bind(&body); err != nil {
		return errEnvelope(c, http.StatusBadRequest, "invalid body")
	}
	view, err := coreGet(s, c.Context(), org, projectScope(c), body.Name)
	if err != nil {
		return zapErr(c, err)
	}
	return okEnvelope(c, view)
}

func zapDelete(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return errEnvelope(c, http.StatusForbidden, "X-Org-Id required")
	}
	var body zapProcReq
	if err := c.Bind(&body); err != nil {
		return errEnvelope(c, http.StatusBadRequest, "invalid body")
	}
	if err := coreDelete(s, c.Context(), org, projectScope(c), body.Name); err != nil {
		return zapErr(c, err)
	}
	return okEnvelope(c, map[string]any{"deleted": true, "name": normalizeName(body.Name)})
}

func zapUsage(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return errEnvelope(c, http.StatusForbidden, "X-Org-Id required")
	}
	out, err := coreUsage(s, c.Context(), org)
	if err != nil {
		return zapErr(c, err)
	}
	return okEnvelope(c, out)
}
