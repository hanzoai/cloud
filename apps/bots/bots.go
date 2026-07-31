// Package bots is the CONTROL PLANE for a bot run: a task the bot runtime
// executes on a surface — a desktop or terminal sandbox it drives — with a LIVE
// session (the URL the hanzo.app /vnc panel embeds to watch/attach).
//
// A bot run is ONE value with ONE home. It is not the bot MACHINE that hosts a
// runtime (visor's /v1/compute/bots — a machine you rent), and it is not the
// runtime service itself (clients/runtime — the transport to the executor).
//
// CLOUD OWNS POLICY, THE RUNTIME OWNS THE RUN. The sandbox lives in the runtime,
// keyed in the runtime's own store under the tenant that started it; that store is
// the only thing that knows whether a run is alive. So this package keeps no
// second copy of it. It owns what a control plane owns — who you are, which org
// you are, and whether you may — and then asks the runtime, which IS the registry.
// Copying that state into cloud would create a second id space agreeing with
// nothing: listing runs that do not exist and stopping runs never started.
//
// Isolation: the org is the gateway-minted X-Org-Id (HIP-0026) resolved via
// principal.Org, NEVER a request field, and it is what cloud sends the runtime,
// which keys every run under tenants/{org}/. A caller cannot name another tenant's
// org, so it cannot read or stop another tenant's runs; a foreign run id resolves
// under the CALLER's org, where it does not exist, and answers 404.
//
// Surface (org-scoped; the console BotsApi and the CLI `hanzo bot run` call it):
//
//	POST /v1/bots/run           -> 501: no runtime launch operation exists yet
//	GET  /v1/bots               -> {bots:[{runId,task,surface,status,sessionUrl,startedAt}]}
//	POST /v1/bots/:runId/stop   -> {runId, status}
package bots

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

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
	"github.com/hanzoai/cloud/apps/runtime"
	"github.com/zap-proto/zip"
)

const (
	// gatewayURLEnv configures the browser-facing bot VNC gateway base — the public
	// origin the TS bot service serves /vnc?nodeId=<id> from, which the hanzo.app
	// /vnc panel embeds. It is DISTINCT from the runtime's in-cluster address
	// (clients/runtime's BOT_GATEWAY_URL, a pod-internal DNS name a browser cannot
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

// BotRun is one row of GET /v1/bots — the console list item.
type BotRun struct {
	// RunID is the run's id, which is also the VNC node id its session is
	// addressable by.
	RunID string `json:"runId"`
	// Task is the task text the run was started with, as the runtime recorded it.
	Task string `json:"task"`
	// Surface is what the bot drives — a desktop or terminal sandbox.
	Surface string `json:"surface"`
	// Status is the runtime's own status for the run, defaulting to "running" when
	// it names none.
	Status string `json:"status"`
	// SessionURL is the live VNC URL the console embeds to watch or attach. It is
	// derived control-plane side from runId (the ONE place a session URL is built),
	// so the runtime never has to know its own public origin.
	SessionURL string `json:"sessionUrl"`
	// StartedAt is RFC3339, as the runtime stamps it.
	StartedAt string `json:"startedAt"`
}

// BotRunList is the GET /v1/bots envelope; Bots is always non-nil so an org with no
// runs serializes as {"bots":[]}, never {"bots":null}.
type BotRunList struct {
	// Bots is the caller org's live runs. Never null.
	Bots []BotRun `json:"bots"`
}

// BotRunRef addresses one run by the id in the path.
type BotRunRef struct {
	// RunID is the run id, as returned by list. A run belonging to another org is
	// not among this org's runs and reads as not found.
	RunID string `json:"runId"`
}

// BotStopped is the POST /v1/bots/:runId/stop response.
type BotStopped struct {
	// RunID is the run that was stopped.
	RunID string `json:"runId"`
	// Status is "stopped".
	Status string `json:"status"`
}

// Mount wires the bots surface onto app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("bots.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("bots.Mount: nil deps.Logger")
	}
	s := &cloud.Service[state]{
		Base:  cloud.NewBase(deps, "bots"),
		State: state{gateway: gatewayBase(), runtime: wire{}},
	}
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("bots.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	routes(zapp, s)
	s.Log.Info("bots surface mounted", "gateway", s.State.gateway, "brand", deps.Brand)
	return nil
}

// routes registers the bots surface. The static /run literal and the :runId param
// are resolved by specificity, so /v1/bots/run can never bind as a run id.
//
// EVERY route is a typed op: the registry entry zip.<Verb> makes is the ONE thing
// OpenAPI, MCP and the CLI project from, and it takes the ABSOLUTE path because the
// registry keys on it.
func routes(zapp *zip.App, s *cloud.Service[state]) {
	// UNIFIED PAYWALL (server-side enforcement). To gate these ops behind the
	// caller's plan, declare them through the gate:
	//   g := zapp.With(entitlements.RequireProduct(deps.Commerce, "bot"))
	// DEFERRED — DO NOT ENABLE YET: the "bot" product is ABSENT from @hanzo/plans
	// licensing.product_ids (v1.4.4), so enforcing now would 402 every org. Flip on
	// once the catalog licenses "bot" to a tier. See clients/entitlements.
	o := ops{s: s}
	zip.Post(zapp, "/v1/bots/run", o.run, zip.WithOperationID("botRun"))
	zip.Get(zapp, "/v1/bots", o.list, zip.WithOperationID("botList"))
	zip.Post(zapp, "/v1/bots/:runId/stop", o.stop, zip.WithOperationID("botStop"))
}

// ops binds the service to the typed handlers: a TypedHandler has no parameter for
// the service, so it arrives as a RECEIVER and every op is a method value — the only
// bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// run reports that launching a bot is not implemented, and answers 501. There is no
// launch operation on the bot runtime, so nothing in cloud can start a sandbox.
//
// This endpoint used to mint a run id, charge a flat per-run fee, and hand
// back a sessionUrl for a bot that never booted — an id the runtime had never heard
// of, pointing at a VNC node that did not exist, for money that was really taken.
// 501 is the truth, and the truth is cheaper than a plausible lie.
//
// Restoring it needs a runtime-side launch operation first (TS, cross-repo); the
// gate and the meter belong in the same change that can prove a bot boots. It reads
// no body and has no success response, which is why both sides are void.
func (o ops) run(_ context.Context, _ *struct{}) (*struct{}, error) {
	return nil, zip.Errorf(http.StatusNotImplemented,
		"launching a bot is not implemented: the bot runtime exposes no launch operation, so cloud cannot start one")
}

// list returns the caller org's live bot runs. They are read from the runtime and
// projected into the console contract, with sessionUrl derived here.
//
// The org is ALWAYS the validated principal's org, NEVER a request field, and it is
// what scopes the runtime's answer — so one tenant can never enumerate another's
// runs. A runtime that cannot answer is an error, not an empty list: [] would tell
// the caller "your org has no runs", which is a different claim from "we could not
// ask", and the difference is the whole reason this endpoint exists.
//
// Response: {"bots":[{"runId":"run_1","task":"summarise the inbox","surface":"desktop","status":"running","sessionUrl":"https://bot.hanzo.ai/vnc?nodeId=run_1","startedAt":"2026-07-26T18:00:00Z"}]}
func (o ops) list(ctx context.Context, _ *struct{}) (*BotRunList, error) {
	// Org-scoping is only trustworthy behind a validated principal: a bare,
	// forgeable X-Org-Id (the direct-to-pod path) must not enumerate a victim
	// tenant's runs. principal.OrgFrom composes that check.
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("X-Org-Id required")
	}
	runs, err := o.s.State.runtime.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "bots: the runtime could not list this org's runs: %v", err)
	}
	out := make([]BotRun, 0, len(runs))
	for _, r := range runs {
		out = append(out, toBotRun(o.s, r))
	}
	return &BotRunList{Bots: out}, nil
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

// stop terminates one of the caller org's own runs. The own-key guard is the org.
//
// It is the caller's validated org, never theirs to
// choose, and the runtime resolves the run id UNDER it. A run belonging to another
// tenant is not among this org's runs, so it answers absent — the same 404 a
// nonexistent id gets, which is what keeps this from being an oracle.
//
// Absence is honoured ONLY when the runtime answers it. A runtime that does not
// serve stop reports nothing about the run, and reporting "stopped" on that basis
// would be a stop that cannot fail — so it is a 502.
//
// Example: {"runId":"run_1"}
// Response: {"runId":"run_1","status":"stopped"}
func (o ops) stop(ctx context.Context, in *BotRunRef) (*BotStopped, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("X-Org-Id required")
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
	case errors.Is(err, runtime.ErrNotFound):
		return nil, zip.ErrNotFound("no such bot for this org")
	case errors.Is(err, runtime.ErrNotServed):
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
