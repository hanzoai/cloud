package git

import (
	"errors"
	"github.com/hanzoai/cloud/apps/principal"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
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
// mapped by zapface from the ZAP method "POST <service>/zap/<procedure>". The
// verb is CARRIED, never inferred (zapface/dispatch.go splitMethod) — a method
// with no verb is refused rather than defaulted, because on a RESTful surface
// one path answers GET, PATCH and DELETE and a guess could send a delete as a
// post. The path is rooted at /v1. Each adapter
// resolves the org EXACTLY as the REST handler does (principal.Org →
// X-Org-Id, minted by the identity middleware from the browser's replayed
// credential), then calls the SAME core func the REST handler calls, and wraps
// the result in the envelope. So the REST handler and the ZAP procedure are two
// thin adapters over ONE core func each — the business logic lives once.
//
// git's procedures (all over the core in core.go):
//
//	POST git/zap/createRepo -> coreCreate   POST git/zap/deleteRepo -> coreDelete
//	POST git/zap/listRepos  -> coreList     POST git/zap/usage      -> coreUsage
//	POST git/zap/getRepo    -> coreGet
//
// The next service (e.g. crm, prompts) copies this file's shape: one mountZAP
// registering /v1/<service>/zap/<proc> envelope adapters over its own core funcs.
// Nothing else — the /zap plane does the rest.

// zapProcedure is what every one of the five shares, because they are five thin
// adapters over one core each with one identical preamble. Stated once.
const zapProcedure = "\n\nA ZAP PROCEDURE, not a REST resource. It answers the " +
	"bridge's {status, msg, data} envelope rather than the raw view the /v1 route " +
	"returns — which is a wire shape a typed op cannot produce, and the reason this " +
	"stays a raw handler — and it calls the SAME core function the REST route " +
	"calls, so the two transports cannot diverge in behaviour. Org and project " +
	"scope come from the request identity and NEVER from the body: the body cannot " +
	"widen the caller's scope. Without a validated org the answer is a 403 envelope."

// The prose for git's five ZAP procedures. Three of them state the body they read
// through openapi.Register (webhook.go); none can state what it DOES that way,
// because reflection reads Go types and not comments, so all five published a
// name and nothing a caller could act on. Kept together here, beside the
// registration that creates them, rather than split by which ones happen to have
// a body worth declaring — the family is one thing and reads as one.
func init() {
	openapi.Describe("/v1/git/zap/createRepo", http.MethodPost,
		"Create a repository over the ZAP transport",
		"Creates a repository in the caller's org and project scope and answers with "+
			"its record. `name` is required and `description` is optional; `project` "+
			"narrows the scope within the org. A name already taken in that scope is a "+
			"409 envelope and an invalid name a 400."+zapProcedure)

	openapi.Describe("/v1/git/zap/listRepos", http.MethodPost,
		"List your repositories over the ZAP transport",
		"Answers every repository in the caller's org and project scope. It reads NO "+
			"body — the scope is entirely the caller's identity — so a request with an "+
			"empty object is correct."+zapProcedure)

	openapi.Describe("/v1/git/zap/getRepo", http.MethodPost,
		"Read one repository over the ZAP transport",
		"Answers a single repository's record, named by `name`. A repository outside "+
			"the caller's org and project scope is a 404 envelope, the same answer one "+
			"that does not exist gets."+zapProcedure)

	openapi.Describe("/v1/git/zap/deleteRepo", http.MethodPost,
		"Delete a repository over the ZAP transport",
		"Deletes the repository named by `name` and answers with the deleted name. "+
			"A repository outside the caller's org and project scope is a 404 envelope, "+
			"so a delete can never reach another tenant's repository."+zapProcedure)

	openapi.Describe("/v1/git/zap/usage", http.MethodPost,
		"Report your org's git storage footprint over the ZAP transport",
		"Answers every repository in the caller's org with its size in bytes, plus the "+
			"org's total — what git storage is actually being used, and by which "+
			"repository. It reads NO body, and it is scoped to the caller's own org, so "+
			"it is that org's footprint and never the fleet's."+zapProcedure)
}

// mountZAP registers git's ZAP procedure adapters. Called from routes(). The
// procedures are ordinary /v1 routes; the shared /zap plane turns them into ZAP
// procedures for the browser/service ZAP client.
//
// These stay RAW handlers, unlike the control plane they wrap. A typed op
// answers a failure by RETURNING an error, which zip renders as its own
// {status, code, error} body; the envelope contract here is a non-2xx status
// carrying a {status:"error", msg} body instead. That is a wire shape a typed
// op cannot produce, so typing these would break the bridge's clients.
//
// They are also the surface a reader should expect to shrink. The /v1 routes
// they adapt are now typed ops (ops.go), so the shared /zap plane already
// replays them frame-for-frame — this file is the older, hand-written path to
// the same five core funcs, kept because its envelope is a published contract.
func mountZAP(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/git")
	g.Post("/zap/createRepo", cloud.Handle(s, zapCreate))
	g.Post("/zap/listRepos", cloud.Handle(s, zapList))
	g.Post("/zap/getRepo", cloud.Handle(s, zapGet))
	g.Post("/zap/deleteRepo", cloud.Handle(s, zapDelete))
	g.Post("/zap/usage", cloud.Handle(s, zapUsage))
}

// ---- envelope ----

// okEnvelope is the success shape the zapface bridge unwraps (data → the ZAP
// result). It is the SAME shape every /v1 ZAP-facing handler returns, so the
// bridge stays fully generic.
func okEnvelope(c *zip.Ctx, data any) error {
	return cloud.OK(c, data)
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
		return errEnvelope(c, http.StatusForbidden, principal.Refusal(c))
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
		return errEnvelope(c, http.StatusForbidden, principal.Refusal(c))
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
		return errEnvelope(c, http.StatusForbidden, principal.Refusal(c))
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
		return errEnvelope(c, http.StatusForbidden, principal.Refusal(c))
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
		return errEnvelope(c, http.StatusForbidden, principal.Refusal(c))
	}
	out, err := coreUsage(s, c.Context(), org)
	if err != nil {
		return zapErr(c, err)
	}
	return okEnvelope(c, out)
}
