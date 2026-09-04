// Package bot is a bot doing your work on a real desktop, live, while you watch.
//
// It is the whole cloud side of the headless bot: the CONTROL PLANE for a bot run
// — a task executed on a surface (a desktop or terminal sandbox the bot drives)
// with a LIVE session, the URL the hanzo.app /vnc panel embeds to watch or attach
// — TOGETHER WITH the transport to the service that executes it, @hanzo/bot.
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
// executor (/v1/compute/bots — a machine you rent), and it is not a NODE
// you already own and connect (apps/nodes, /v1/node).
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
//     the CLI `hanzo bot run` call it). All three are typed ops, so each is one
//     registry entry with five projections — the REST route, the OpenAPI
//     operation with its schema, an MCP tool, a CLI command and an SDK method:
//
//     GET  /v1/bot/runs              -> {bots:[{runId,task,surface,status,sessionUrl,startedAt}]}
//     POST /v1/bot/runs              -> 501: no executor launch operation exists yet
//     POST /v1/bot/runs/:runId/stop  -> {runId, status}
//
//   - RELAYED, the executor's own operational paths at /v1/bot/runtime/*
//     (relay.go). A liveness probe is not a tenant-scoped resource, so it stays a
//     relay rather than being reimplemented in Go.
//
// The transport itself (transport.go) knows how to MOVE BYTES and nothing about
// what they mean: a caller states WHAT it wants done (a Call) and gets back a
// domain-shaped outcome — never an *http.Response, a status code, or a framing
// detail. Today those bytes move over HTTP; per HIP-0106/HIP-0120 they should move
// over ZAP, and that swap is meant to be a change to transport.go plus each
// caller's one stub, not a rewrite. apps/coding dispatches its coding tasks to the
// same executor and uses the same Call.
//
// run.go is the run plane and this package's Mount; relay.go is the executor's
// ops face; transport.go moves the bytes between them.
package bot

import (
	"context"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/internal/environ"
	"net/http"
	"net/url"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"

	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/bot describe` and by the Dockerfile before every build.
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

// Runtime is the client onto the run registry — the bot runtime, which owns the
// sandboxes and is therefore the only truthful answer to "what is running". Bound
// to the real transport in wire.go; a fake in tests.
//
// Every method takes org FIRST and the runtime scopes by it. The client carries no
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

type executor struct {
	// gateway is the browser-facing bot VNC gateway base (no trailing slash) that
	// every returned sessionUrl is derived from.
	gateway string
	// runtime is the run registry — what list reads and what stop drives.
	runtime Runtime
}

// BotRun is one row of GET /v1/bot/runs — the console list item. sessionUrl is
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

// BotRuns is the GET /v1/bot/runs envelope; Bots is always non-nil so an org with no
// runs serializes as {"bots":[]}, never {"bots":null}.
type BotRuns struct {
	// Bots is the org's live runs. Always an array, never null.
	Bots []BotRun `json:"bots"`
}

// BotStopped is the POST /v1/bot/runs/{runId}/stop receipt.
type BotStopped struct {
	// RunID is the run that was stopped.
	RunID string `json:"runId"`
	// Status is the run's terminal state: "stopped".
	Status string `json:"status"`
}

// stopBotIn addresses one run.
//
// BOTH TAGS NAME THE SAME SEGMENT, and the pair is what makes the op reachable on
// every projection. `url:"runId"` binds it from the path segment the router
// matched on, which is the addressing authority — zip binds body, then query,
// then path, so a body naming another run cannot redirect the stop
// (TestStopIsAddressedByTheURLAlone). `json:"runId"` is what lets the OTHER
// projections name the target at all: an MCP tools/call reaches op.invoke with a
// NIL path map and the arguments as the body (zip v1.36.3 mcp.go:654), and zip's
// schema builder skips a field whose json name is "-" (openapi.go:719-722). With
// `json:"-"` the MCP tool published an EMPTY input schema and answered "runId is
// required" to every call — measured, and the reason this tag changed.
//
// It costs no request body: hasRequestBody (openapi.go:405-435) publishes one only
// for a field the URL does not already carry, and this one is `:runId`.
type stopBotIn struct {
	// RunID is the run to stop, as the bot runtime named it. It is read from the
	// URL — the `{runId}` segment the router matched on — and a body carrying a
	// different id cannot redirect the stop.
	RunID string `json:"runId" url:"runId"`
}

// Mount registers the whole /v1/bot surface: the run control plane, then the
// relay. Order matters and specificity does not decide it for us — the run family
// is static leaves and the relay is a greedy wildcard, so the wildcard goes last
// and under its own segment, where it can never shadow a sibling above it.
//
// The MACHINE half is gone from here: a node is not a kind of bot, and it answers
// under its own name in apps/nodes.
func Use(app cloud.Router, deps cloud.Deps) error {
	if err := mountRunPlane(app, deps); err != nil {
		return err
	}
	return mountRelay(app, deps)
}

// mountRunPlane wires the run control plane onto app. Mount calls it: one
// capability, two families, one entry point.
func mountRunPlane(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("bot.Use:  nil app")
	}
	s := &cloud.Service[executor]{
		Base:  cloud.NewBase(deps, "bot"),
		State: executor{gateway: gatewayBase(), runtime: wire{}},
	}
	mountRuns(app, s)
	s.Log.Info("run plane mounted", "gateway", s.State.gateway, "brand", cloud.Brand())
	return nil
}

// runOps binds the mounted Service so each op can be a method value — the only
// bound form cmd/zipdoc can lift prose from. It carries STATE and no logic.
type runOps struct{ s *cloud.Service[executor] }

// mountRuns registers the run control plane at /v1/bot/runs.
//
// THE LAUNCH IS A POST TO THE COLLECTION, and that is what the fold bought. The
// run family lived at /v1/bot with the launch at the literal /v1/bot/run, one
// segment away from /v1/bot/:runId/stop — a literal that had to out-rank its
// param sibling or bind as a run id. Under /v1/bot/runs the verb is the method
// (HIP-0128 §1): GET lists, POST launches, and there is no literal to shadow.
//
// ALL THREE ARE TYPED OPS — one registry entry each, from which the REST route,
// the OpenAPI operation, the MCP tool, the CLI command and every generated SDK
// method follow. The launch answers 501 and says so in the document: zip declares
// a status rather than assuming 200 (zip.WithStatus, typed.go:154-176), so an
// operation whose only answer is a refusal is describable like any other.
func mountRuns(app cloud.Router, s *cloud.Service[executor]) {
	// cloud.Bridge parks the request facts a typed op's signature drops — the
	// validated org, validated-ness itself, the brand and the project — on the
	// subsystem's own router, ahead of the leaves, because fiber runs middleware in
	// registration order and one installed after them never runs. Serve installs it
	// at the binary's root as well and the nesting is harmless (cloud/typed.go: the
	// inner one is the one the handler sees, the outer finds nothing to apply).
	//
	// MEASURED, so nobody writes a test that cannot fail: removing this line changes
	// NOTHING either of today's org-scoped ops answers, because both ask
	// principal.Acting, and principal.OrgFrom falls back to zip.CallerOf — the
	// request's own identity headers — when the slot is empty. So this install is
	// not what makes the two reads work; what it buys is that the package does not
	// DEPEND on its host for them, and that the next op here can read
	// principal.ProjectFrom or the brand, which have no such fallback. Do not add a
	// test asserting the org resolves: it passes with the line deleted.
	app.Use(cloud.Bridge())

	// UNIFIED PAYWALL (server-side enforcement). To hold this surface to the
	// caller's plan, ask entitlement in each operation's PREAMBLE. Not on the
	// group: these are typed operations, and a group's middleware reaches the REST
	// route only — MCP, the call plane, the graph and the CLI reach the operation
	// with the caller already authenticated, so a group-wrapped paywall bills the
	// browser and nobody else.
	// DEFERRED — DO NOT ENABLE YET: the "bot" product is ABSENT from @hanzo/plans
	// licensing.product_ids (v1.4.4), so enforcing now would 402 every org. Flip on
	// once the catalog licenses "bot" to a tier. See clients/entitlements.
	//
	// The collection is declared on the /v1/bot PARENT with a non-empty leaf:
	// zip.Get(g, "") would normalise to "/v1/bot/runs/", and op.Path is the
	// identity every projection keys on, so the document, the operationId, the MCP
	// tool and every generated SDK would carry a trailing slash for a path this
	// API does not serve.
	parent := app.Group("/v1/bot")
	g := app.Group("/v1/bot/runs")
	o := runOps{s: s}
	zip.Get(parent, "/runs", o.list)
	// The declared status is the whole of what this op answers: 501, never 200.
	// zip publishes the SET an op declared and sends nothing outside it
	// (statusOf, typed.go:192-212), so the document, the SDKs and the tool list
	// all say up front that this address refuses.
	zip.Post(parent, "/runs", o.run, zip.WithStatus(http.StatusNotImplemented))
	zip.Post(g, "/:runId/stop", o.stop)

	// The roster — this org's bots as members of its team spaces. team holds the
	// rows and answers over the plane; the ADDRESS is here, because a bot is a bot
	// wherever its membership happens to be stored (members.go).
	m := memberOps{}
	zip.Get(parent, "/members", m.list)
	zip.Post(parent, "/members/sync", m.sync)
}

// run answers 501 to every call: launching a bot run is not implemented.
//
// The bot runtime exposes no launch operation, so nothing here can start a sandbox.
// This address is published rather than dropped because it is the collection every
// run is created in: GET lists them, POST would launch one.
//
// The refusal is total and takes no input. No run id is minted, no session URL is
// handed back, and no per-run fee is charged. That is the point: the earlier version
// minted an id the runtime had never heard of, pointed it at a VNC node that did not
// exist, and took real money for it. 501 is the truth, and the truth is cheaper than
// a plausible lie.
//
// Listing and stopping runs are live and org-scoped. Only the launch is missing, and
// it returns in the same change that can prove a bot boots — a runtime-side launch
// operation first (TS, cross-repo), with the entitlement gate and the meter beside
// it.
func (o runOps) run(context.Context, *cloud.Unit) (*cloud.Unit, error) {
	return nil, zip.Errorf(http.StatusNotImplemented,
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
func (o runOps) list(ctx context.Context, _ *cloud.Unit) (*BotRuns, error) {
	// principal.OrgFrom parks nothing unless the request carried a VALIDATED
	// principal, so this single check is both gates the raw handler spelled out: a
	// bare, forgeable X-Org-Id (the direct-to-pod path) never reaches here.
	org, err := principal.Acting(ctx)
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
func toBotRun(s *cloud.Service[executor], r Run) BotRun {
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
func (o runOps) stop(ctx context.Context, in *stopBotIn) (*BotStopped, error) {
	org, err := principal.Acting(ctx)
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
func sessionURL(s *cloud.Service[executor], runID string) string {
	return s.State.gateway + "/vnc?" + url.Values{"nodeId": {runID}}.Encode()
}

// gatewayBase resolves the browser-facing bot VNC gateway base (no trailing slash)
// from CLOUD_BOT_GATEWAY_URL, falling back to the public default.
func gatewayBase() string {
	base := environ.Or(gatewayURLEnv, "")
	if base == "" {
		base = defaultGatewayURL
	}
	return strings.TrimRight(base, "/")
}
