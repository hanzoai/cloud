// Package bots is a bot doing your work on a real desktop, live, while you watch.
//
// It is the whole cloud side of the headless bot: the CONTROL PLANE for a bot run
// — a task executed on a surface (a desktop or terminal sandbox the bot drives)
// with a LIVE session, the URL the hanzo.app /vnc panel embeds to watch or attach
// — TOGETHER WITH the door to the service that executes it, @hanzo/bot.
//
// One product, not two. The control plane and the transport to the executor were
// separate apps once (apps/runtime), which made a LANGUAGE boundary look like a
// product boundary: the surface is Go, the executor is TS, and nothing else
// distinguished them. They answer for the same thing and now live in one place.
//
// CLOUD OWNS POLICY, THE EXECUTOR OWNS THE RUN. The sandbox lives in @hanzo/bot,
// keyed in its own store under the tenant that started it; that store is the only
// thing that knows whether a run is alive. So this package keeps no second copy of
// it. It owns what a control plane owns — who you are, which org you are, and
// whether you may — and then asks the executor, which IS the registry. Copying
// that state into cloud would create a second id space agreeing with nothing:
// listing runs that do not exist and stopping runs never started.
//
// A bot run is ONE value with ONE home. It is not the bot MACHINE that hosts an
// executor (visor's /v1/compute/bots — a machine you rent).
//
// Isolation: the org is the gateway-minted X-Org-Id (HIP-0026) resolved via
// principal.Org, NEVER a request field, and it is what cloud sends the executor,
// which keys every run under tenants/{org}/. A caller cannot name another tenant's
// org, so it cannot read or stop another tenant's runs; a foreign run id resolves
// under the CALLER's org, where it does not exist, and answers 404.
//
// Two faces, and the split between them is what a tenant can ACT on:
//
//   - NATIVE + TYPED, the run control plane (org-scoped; the console BotsApi and
//     the CLI `hanzo bot run` call it):
//
//     POST /v1/bots/run           -> 501: no executor launch operation exists yet
//     GET  /v1/bots               -> {bots:[{runId,task,surface,status,sessionUrl,startedAt}]}
//     POST /v1/bots/:runId/stop   -> {runId, status}
//
//   - RELAYED, the executor's own operational paths at /v1/bot/* (relay.go). A
//     liveness probe is not a tenant-scoped resource, so it stays a relay rather
//     than being reimplemented in Go.
//
// The transport itself (transport.go) knows how to MOVE BYTES and nothing about
// what they mean: a caller states WHAT it wants done (a Call) and gets back a
// domain-shaped outcome — never an *http.Response, a status code, or a framing
// detail. Today those bytes move over HTTP; per HIP-0106/HIP-0120 they should move
// over ZAP, and that swap is meant to be a change to transport.go plus each
// caller's one stub, not a rewrite. apps/coding dispatches its coding tasks to the
// same executor and uses the same Call.
package bots

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/bots openapi` and by the Dockerfile before every build.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

const (
	// gatewayURLEnv configures the browser-facing bot VNC gateway base — the public
	// origin the TS bot service serves /vnc?nodeId=<id> from, which the hanzo.app
	// /vnc panel embeds. It is DISTINCT from the runtime's in-cluster address
	// (transport.go's BOT_GATEWAY_URL, a pod-internal DNS name a browser cannot
	// reach): a session URL must be publicly embeddable, so it carries its own knob.
	gatewayURLEnv     = "CLOUD_BOT_GATEWAY_URL"
	defaultGatewayURL = "https://bot.hanzo.ai"

	// maxRunID bounds the :runId path param before it is sent onward — an oversize
	// id is not a run this org owns, so it is a 404 like any other miss.
	maxRunID = 128

	// statusRunning is what a listed run reports when the runtime names no status of
	// its own. statusStopped is the terminal outcome a stop reports.
	statusRunning = "running"
	statusStopped = "stopped"
)

// Runtime is the seam onto the run registry — the bot runtime, which owns the
// sandboxes and is therefore the only truthful answer to "what is running". Bound
// to the real transport in wire.go; a fake in tests.
//
// Every method takes org FIRST and the runtime scopes by it. The seam carries no
// authority: cloud decides WHETHER a caller may ask, the runtime answers WHAT it
// holds for that org.
type Runtime interface {
	List(ctx context.Context, org string) ([]Run, error)
	Stop(ctx context.Context, org, runID string) error
}

// Run is one bot run as the runtime reports it.
type Run struct {
	ID        string
	Task      string
	Surface   string
	Status    string
	StartedAt string // RFC3339, as the runtime stamps it
}

type state struct {
	// gateway is the browser-facing bot VNC gateway base (no trailing slash) that
	// every returned sessionUrl is derived from.
	gateway string
	// runtime is the run registry — what list reads and what stop drives.
	runtime Runtime
}

// BotRun is one row of GET /v1/bots — the console list item. sessionUrl is
// derived control-plane side from runId (the ONE place a session URL is built), so
// the runtime never has to know its own public origin.
//
// The name is qualified because the fleet's schema namespace is FLAT and
// apps/visor already publishes a `botView` for a bot MACHINE (a box you rent).
// This is a bot RUN. Two values, two names.
type BotRun struct {
	// RunID is the run's id in the bot runtime, and the node id its live VNC session
	// is registered under.
	RunID string `json:"runId"`
	// Task is the instruction the bot is executing.
	Task string `json:"task"`
	// Surface is what the bot drives: the desktop or terminal sandbox it runs in.
	Surface string `json:"surface"`
	// Status is the run's state as the runtime reports it; "running" when the runtime
	// names none of its own.
	Status string `json:"status"`
	// SessionURL is the live session the hanzo.app /vnc panel embeds to watch or
	// attach to this run. Derived here from the run id, never sent by the runtime.
	SessionURL string `json:"sessionUrl"`
	// StartedAt is when the run began, RFC 3339, as the runtime stamped it.
	StartedAt string `json:"startedAt"`
}

// BotRuns is the GET /v1/bots envelope; Bots is always non-nil so an org with no
// runs serializes as {"bots":[]}, never {"bots":null}.
type BotRuns struct {
	// Bots is the org's live runs. Always an array, never null.
	Bots []BotRun `json:"bots"`
}

// BotStopped is the POST /v1/bots/{runId}/stop receipt.
type BotStopped struct {
	// RunID is the run that was stopped.
	RunID string `json:"runId"`
	// Status is the run's terminal state: "stopped".
	Status string `json:"status"`
}

// stopBotIn addresses one run. The id is URL-borne only: `json:"-"` keeps it out of
// the published request body (the route has never accepted a run id there), and
// `url:"runId"` binds it from the path segment the router matched on.
type stopBotIn struct {
	RunID string `json:"-" url:"runId"`
}

// noArgs is the input of an op that takes none: no body, no query, no path param.
type noArgs struct{}

// Mount wires the bots surface onto app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("bots.Mount: nil app")
	}
	s := &cloud.Service[state]{
		Base:  cloud.NewBase(deps, "bots"),
		State: state{gateway: gatewayBase(), runtime: wire{}},
	}
	routes(app, s)
	// The executor's ops face, on the same product: /v1/bot/* relayed verbatim.
	// It mounts SECOND because the native control plane above is what a tenant
	// acts on, and a relay must never be able to shadow it.
	if err := mountRelay(app, deps); err != nil {
		return err
	}
	s.Log.Info("bots surface mounted", "gateway", s.State.gateway, "brand", deps.Brand)
	return nil
}

// ops binds the mounted Service so each op can be a method value — the only bound
// form cmd/zipdoc can lift prose from. It carries STATE and no logic.
type ops struct{ s *cloud.Service[state] }

// routes registers the bots surface. The static /run literal and the :runId param
// are resolved by specificity, so /v1/bots/run can never bind as a run id.
//
// The list and the stop are TYPED ops — one registry entry from which the REST
// route, the OpenAPI operation, the MCP tool, the CLI command and every generated
// SDK method follow. POST /v1/bots/run stays a raw handler; see run for why.
func routes(app cloud.Router, s *cloud.Service[state]) {
	// The composer owns cloud.Bridge: the fused host installs it once at its root
	// and the plugin constructor does the same for a plugin program, so no
	// subsystem installs it.

	// UNIFIED PAYWALL (server-side enforcement). To gate this group behind the
	// caller's plan, prepend the middleware to the group:
	//   g := app.Group("/v1/bots", entitlements.RequireProduct(deps.Commerce, "bot"))
	// DEFERRED — DO NOT ENABLE YET: the "bot" product is ABSENT from @hanzo/plans
	// licensing.product_ids (v1.4.4), so enforcing now would 402 every org. Flip on
	// once the catalog licenses "bot" to a tier. See clients/entitlements.
	g := app.Group("/v1/bots")
	o := ops{s: s}
	g.Post("/run", cloud.Handle(s, run))
	// Declared on the /v1 PARENT with a non-empty leaf: zip.Get(g, "") would
	// normalise to "/v1/bots/", and op.Path is the identity every projection keys on,
	// so the document, the operationId, the MCP tool and every generated SDK would
	// carry a trailing slash for a path this API has never served.
	zip.Get(app.Group("/v1"), "/bots", o.list)
	zip.Post(g, "/:runId/stop", o.stop)
}

// The prose for the one operation here that cannot be a typed op. The list and the
// stop are typed and zipdoc lifts their doc comments into zipdoc_gen.go; the launch
// stays a raw handler (run says why — a typed op publishes a success this route has
// none of), so there is no comment for anything to lift and the published document
// would carry an operationId and nothing else. That is the worst case for exactly
// this route: an SDK method and a CLI command with no help text, for an operation
// whose whole content is a refusal a caller must be told about up front. Declared
// through the same registry Register uses, so it renders only while the router
// actually serves the route.
func init() {
	openapi.Describe("/v1/bots/run", http.MethodPost,
		"Reserved address for launching a bot run — not implemented, always 501",
		"Answers 501 to every call. The bot runtime exposes no launch operation, so nothing "+
			"here can start a sandbox, and this address is published rather than dropped because "+
			"it is reserved: routes resolve by specificity, so the `run` literal can never bind "+
			"as a run id against its neighbour `/v1/bots/:runId/stop`.\n\n"+
			"The refusal is total and takes no input. The handler never reads the body, so any "+
			"bytes at all — malformed JSON included — get the same 501; no run id is minted, no "+
			"session URL is handed back, and no per-run fee is charged. That is the point: the "+
			"earlier version minted an id the runtime had never heard of, pointed it at a VNC "+
			"node that did not exist, and took real money for it.\n\n"+
			"Listing and stopping runs are live and org-scoped. Only the launch is missing, and "+
			"it returns in the same change that can prove a bot boots.")
}

// run reports that launching is not implemented.
//
// There is no launch operation on the bot runtime, so nothing in cloud can start a
// sandbox. This endpoint used to mint a run id, charge a flat per-run fee, and hand
// back a sessionUrl for a bot that never booted — an id the runtime had never heard
// of, pointing at a VNC node that did not exist, for money that was really taken.
// 501 is the truth, and the truth is cheaper than a plausible lie.
//
// Restoring it needs a runtime-side launch operation first (TS, cross-repo); the
// gate and the meter belong in the same change that can prove a bot boots.
//
// UNTYPED BY DESIGN, for the reason apps/books gives for its two bank stubs: a typed
// op publishes a SUCCESS response, and this route has no success. Typing it would
// declare a 200 body it can never send, and mint an MCP tool and a CLI command for
// an operation that cannot succeed — a model reading the tool list would call it.
// It is also BODY-TOLERANT today, which zip cannot express: the handler never reads
// the body, so any bytes at all — malformed JSON included — answer 501, while
// op.invoke 400s on an unparseable non-empty body before the handler runs. It gets
// typed in the same change that can prove a bot boots, and not before.
func run(_ *cloud.Service[state], _ *zip.Ctx) error {
	return zip.Errorf(http.StatusNotImplemented,
		"launching a bot is not implemented: the bot runtime exposes no launch operation, so cloud cannot start one")
}

// List returns the caller org's live bot runs, read from the bot runtime and projected
// into the console contract with each run's live session URL derived here.
//
// The org is ALWAYS the validated principal's org, NEVER a request field, and it is
// what scopes the runtime's answer — so one tenant can never enumerate another's
// runs. A runtime that cannot answer is an error, not an empty list: [] would tell
// the caller "your org has no runs", which is a different claim from "we could not
// ask", and the difference is the whole reason this endpoint exists.
func (o ops) list(ctx context.Context, _ *noArgs) (*BotRuns, error) {
	// principal.OrgFrom parks nothing unless the request carried a VALIDATED
	// principal, so this single check is both gates the raw handler spelled out: a
	// bare, forgeable X-Org-Id (the direct-to-pod path) never reaches here.
	org, err := principal.RequireOrg(ctx)
	if err != nil {
		return nil, err
	}
	runs, err := o.s.State.runtime.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "bots: the runtime could not list this org's runs: %v", err)
	}
	out := make([]BotRun, 0, len(runs))
	for _, r := range runs {
		out = append(out, toBotRun(o.s, r))
	}
	return &BotRuns{Bots: out}, nil
}

// toBotRun projects a run into one list row, deriving sessionUrl from the run id.
func toBotRun(s *cloud.Service[state], r Run) BotRun {
	status := strings.TrimSpace(r.Status)
	if status == "" {
		status = statusRunning
	}
	return BotRun{
		RunID:      r.ID,
		Task:       r.Task,
		Surface:    r.Surface,
		Status:     status,
		SessionURL: sessionURL(s, r.ID),
		StartedAt:  r.StartedAt,
	}
}

// Stop terminates one of the caller org's own bot runs and reports its terminal state.
//
// The own-key guard is the org: it is the caller's validated org, never theirs to
// choose, and the runtime resolves the run id UNDER it. A run belonging to another
// tenant is not among this org's runs, so it answers absent — the same 404 a
// nonexistent id gets, which is what keeps this from being an oracle.
//
// Absence is honoured ONLY when the runtime answers it. A runtime that does not
// serve stop reports nothing about the run, and reporting "stopped" on that basis
// would be a stop that cannot fail — so it is a 502.
func (o ops) stop(ctx context.Context, in *stopBotIn) (*BotStopped, error) {
	org, err := principal.RequireOrg(ctx)
	if err != nil {
		return nil, err
	}
	runID := strings.TrimSpace(in.RunID)
	if runID == "" {
		return nil, zip.ErrBadRequest("runId is required")
	}
	if len(runID) > maxRunID {
		return nil, zip.ErrNotFound("no such bot for this org")
	}
	switch err := o.s.State.runtime.Stop(ctx, org, runID); {
	case err == nil:
		o.s.Log.Info("bot stopped", "org", org, "run", runID)
		return &BotStopped{RunID: runID, Status: statusStopped}, nil
	case errors.Is(err, ErrNotFound):
		return nil, zip.ErrNotFound("no such bot for this org")
	case errors.Is(err, ErrNotServed):
		return nil, zip.Errorf(http.StatusBadGateway,
			"bots: the runtime does not serve stop, so this run's state is unknown — it was NOT stopped")
	default:
		return nil, zip.Errorf(http.StatusBadGateway, "bots: the runtime could not stop this run: %v", err)
	}
}

// sessionURL derives the live VNC session URL for a run: the browser-facing bot
// gateway base + the node's VNC path. The run id IS the node id the runtime
// registers the session under, so the tunnel is addressable by exactly the id the
// client holds.
func sessionURL(s *cloud.Service[state], runID string) string {
	return s.State.gateway + "/vnc?" + url.Values{"nodeId": {runID}}.Encode()
}

// gatewayBase resolves the browser-facing bot VNC gateway base (no trailing slash)
// from CLOUD_BOT_GATEWAY_URL, falling back to the public default.
func gatewayBase() string {
	base := strings.TrimSpace(os.Getenv(gatewayURLEnv))
	if base == "" {
		base = defaultGatewayURL
	}
	return strings.TrimRight(base, "/")
}
