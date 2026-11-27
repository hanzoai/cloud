package bot

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/hanzoai/cloud/sdk/env"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"

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

	// codeRuntimeRefused is the code a list or stop answers under when the bot
	// runtime refused the sign-in cloud relayed for the caller.
	codeRuntimeRefused = "bot_runtime_refused_sign_in"
)

// Runtime is the client onto the run registry — the bot runtime, which owns the
// sandboxes and is therefore the only truthful answer to "what is running". Bound
// to the real transport in wire.go; a fake in tests.
//
// Every method takes the Caller FIRST and the runtime scopes by it. The client
// carries no authority of its own: cloud decides WHETHER a caller may ask, the
// runtime answers WHAT it holds for that org.
type Runtime interface {
	List(ctx context.Context, who Caller) ([]Run, error)
	Stop(ctx context.Context, who Caller, runID string) error
}

// Caller is who a call to the runtime is made for: the validated org, and the
// person's own hanzo.id token, relayed unchanged (cloud.CallerBearer).
//
// The runtime decides the org from that token. It admits its shared gateway token
// and gives it no org at all, because nodes that run customer code hold that token
// too (hanzoai/bot src/gateway/bots-http.ts, callerOrg). So cloud never asks in a
// person's place with its own credential: the runtime would answer for nobody, and
// an org with runs would read as an org with none.
type Caller struct {
	Org    string
	Bearer string
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
	if app == nil {
		return errors.New("bot: nil app")
	}
	if err := mountRunPlane(app, deps); err != nil {
		return err
	}
	return mountRelay(app, deps)
}

// mountRunPlane wires the run control plane onto app. Mount calls it: one
// capability, two families, one entry point.
func mountRunPlane(app cloud.Router, deps cloud.Deps) error {
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
// The run family lived at /v1/bot once, with a literal one segment away from
// /v1/bot/:runId/stop that had to out-rank its param sibling or bind as a run id.
// Under /v1/bot/runs the verb is the method (HIP-0128 §1) and there is no literal
// to shadow.
//
// BOTH ARE TYPED OPS — one registry entry each, from which the REST route, the
// OpenAPI operation, the MCP tool, the CLI command and every generated SDK method
// follow.
func mountRuns(app cloud.Router, s *cloud.Service[executor]) {
	g := app.Group("/v1/bot")
	// cloud.Bridge parks the request facts a typed op's signature drops — the
	// validated org, validated-ness itself, the brand and the project — on the
	// subsystem's own group, ahead of the leaves, because fiber runs middleware in
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
	g.Use(cloud.Bridge())

	// UNIFIED PAYWALL (server-side enforcement). To hold this surface to the
	// caller's plan, ask entitlement in each operation's PREAMBLE. Not on the
	// group: these are typed operations, and a group's middleware reaches the REST
	// route only — MCP, the call plane, the graph and the CLI reach the operation
	// with the caller already authenticated, so a group-wrapped paywall bills the
	// browser and nobody else.
	// DEFERRED — DO NOT ENABLE YET: the "bot" product is ABSENT from @hanzo/plans
	// licensing.product_ids (v1.4.4), so enforcing now would 402 every org. Flip on
	// once the catalog licenses "bot" to a tier. See clients/entitlements.
	o := runOps{s: s}
	g.Get("/runs", o.list)
	runs := g.Group("/runs")
	runs.Post("/:runId/stop", o.stop)

	// The roster — this org's bots as members of its team spaces. team holds the
	// rows and answers over the plane; the ADDRESS is here, because a bot is a bot
	// wherever its membership happens to be stored (members.go).
	m := memberOps{}
	g.Get("/members", m.list)
	g.Post("/members/sync", m.sync)
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
	who, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	runs, err := o.s.State.runtime.List(ctx, who)
	if errors.Is(err, ErrRefused) {
		return nil, o.refused(who, "list", err, "this org's runs could not be read")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "bots: the runtime could not list this org's runs: %v", err)
	}
	out := make([]BotRun, 0, len(runs))
	for _, r := range runs {
		out = append(out, toBotRun(o.s, r))
	}
	return &BotRuns{Bots: out}, nil
}

// caller resolves who the runtime is asked for, and refuses before it is asked
// when that cannot be said truthfully.
//
// principal.OrgFrom parks nothing unless the request carried a VALIDATED
// principal, so the org check is both gates the raw handler spelled out: a bare,
// forgeable X-Org-Id (the direct-to-pod path) never gets past it.
//
// The token is the person's own (cloud.CallerBearer), and there is none for an API
// key, which the runtime cannot validate, nor off the HTTP path. Asking without one
// would make the runtime answer for no org: an empty list read as "this org has no
// runs" and a 404 read as "no such run". So that caller is refused here instead.
func caller(ctx context.Context) (Caller, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return Caller{}, err
	}
	var bearer string
	if c, ok := cloud.Request(ctx); ok {
		bearer = cloud.CallerBearer(c)
	}
	if bearer == "" {
		return Caller{}, zip.Errorf(http.StatusForbidden,
			"bots: sign in with hanzo.id to list or stop bot runs; an API key cannot be passed on to the bot runtime")
	}
	return Caller{Org: org, Bearer: bearer}, nil
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
	who, err := caller(ctx)
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
	switch err := o.s.State.runtime.Stop(ctx, who, runID); {
	case err == nil:
		o.s.Log.Info("bot stopped", "org", who.Org, "run", runID)
		return &BotStopped{RunID: runID, Status: statusStopped}, nil
	case errors.Is(err, ErrNotFound):
		return nil, zip.ErrNotFound("no such bot for this org")
	case errors.Is(err, ErrNotServed):
		return nil, zip.Errorf(http.StatusBadGateway,
			"bots: the runtime does not serve stop, so this run's state is unknown — it was NOT stopped")
	case errors.Is(err, ErrRefused):
		return nil, o.refused(who, "stop", err, "this run was NOT stopped")
	default:
		return nil, zip.Errorf(http.StatusBadGateway, "bots: the runtime could not stop this run: %v", err)
	}
}

// refused is the answer when the runtime turned away the sign-in cloud relayed for
// who: 503, code bot_runtime_refused_sign_in, saying what did not happen.
//
// Not the 502 every other runtime failure is, for two reasons. The runtime did
// answer, and clearly; what failed is a platform that cannot serve this caller,
// which is what 503 says (training_unavailable, balance_unavailable). And a 502 is
// an answer nobody reads: Cloudflare, in front of api.hanzo.ai, replaces an
// origin's 502 and 504 with its own "Bad gateway, try again in a few minutes" page,
// dropping cloud's body and request id. That told a person whose sign-in the
// runtime refuses every time to keep retrying (#780).
//
// Cloud does not ask again with the shared service token. That token acts in no
// org on hanzoai/bot main, so the answer would read as "no runs"; and on the
// deployed gateway it acts in whatever org X-Org-Id names, a trust main removed.
// The runtime's own words go to the log, not to the caller.
func (o runOps) refused(who Caller, op string, err error, outcome string) error {
	o.s.Log.Warn("bots: the runtime refused the relayed sign-in", "op", op, "org", who.Org, "err", err)
	return &zip.HTTPError{
		Status: http.StatusServiceUnavailable,
		Code:   codeRuntimeRefused,
		Msg: "bots: the bot runtime refused the sign-in cloud passed on for you, so " + outcome +
			"; cloud accepted that sign-in, so signing in again will not help",
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
	base := env.Or(gatewayURLEnv, "")
	if base == "" {
		base = defaultGatewayURL
	}
	return strings.TrimRight(base, "/")
}
