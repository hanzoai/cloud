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

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/runtime"
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

// botView is one row of GET /v1/bots — the console list item. sessionUrl is
// derived control-plane side from runId (the ONE place a session URL is built), so
// the runtime never has to know its own public origin.
type botView struct {
	RunID      string `json:"runId"`
	Task       string `json:"task"`
	Surface    string `json:"surface"`
	Status     string `json:"status"`
	SessionURL string `json:"sessionUrl"`
	StartedAt  string `json:"startedAt"`
}

// botsView is the GET /v1/bots envelope; Bots is always non-nil so an org with no
// runs serializes as {"bots":[]}, never {"bots":null}.
type botsView struct {
	Bots []botView `json:"bots"`
}

// stopView is the POST /v1/bots/:runId/stop response.
type stopView struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
}

// Mount wires the bots surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("bots.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("bots.Mount: nil deps.Logger")
	}
	s := &cloud.Service[state]{
		Base:  cloud.NewBase(deps, "bots"),
		State: state{gateway: gatewayBase(), runtime: wire{}},
	}
	routes(app, s)
	s.Log.Info("bots surface mounted", "gateway", s.State.gateway, "brand", deps.Brand)
	return nil
}

// routes registers the bots surface. The static /run literal and the :runId param
// are resolved by specificity, so /v1/bots/run can never bind as a run id.
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Post("/v1/bots/run", cloud.Handle(s, run))
	app.Get("/v1/bots", cloud.Handle(s, list))
	app.Post("/v1/bots/:runId/stop", cloud.Handle(s, stop))
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
func run(_ *cloud.Service[state], _ *zip.Ctx) error {
	return zip.Errorf(http.StatusNotImplemented,
		"launching a bot is not implemented: the bot runtime exposes no launch operation, so cloud cannot start one")
}

// list returns the caller org's live bot runs, read from the runtime and projected
// into the console contract with sessionUrl derived here.
//
// The org is ALWAYS the validated principal's org, NEVER a request param, and it is
// what scopes the runtime's answer — so one tenant can never enumerate another's
// runs. A runtime that cannot answer is an error, not an empty list: [] would tell
// the caller "your org has no runs", which is a different claim from "we could not
// ask", and the difference is the whole reason this endpoint exists.
func list(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	// Org-scoping is only trustworthy behind a validated principal: a bare,
	// forgeable X-Org-Id (the direct-to-pod path) must not enumerate a victim
	// tenant's runs.
	if !principal.Validated(c) {
		return zip.ErrForbidden("a validated principal is required to list bots")
	}
	runs, err := s.State.runtime.List(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "bots: the runtime could not list this org's runs: %v", err)
	}
	out := make([]botView, 0, len(runs))
	for _, r := range runs {
		out = append(out, toBotView(s, r))
	}
	return c.JSON(http.StatusOK, botsView{Bots: out})
}

// toBotView projects a run into one list row, deriving sessionUrl from the run id.
func toBotView(s *cloud.Service[state], r Run) botView {
	status := strings.TrimSpace(r.Status)
	if status == "" {
		status = statusRunning
	}
	return botView{
		RunID:      r.ID,
		Task:       r.Task,
		Surface:    r.Surface,
		Status:     status,
		SessionURL: sessionURL(s, r.ID),
		StartedAt:  r.StartedAt,
	}
}

// stop terminates one of the caller org's own runs.
//
// The own-key guard is the org: it is the caller's validated org, never theirs to
// choose, and the runtime resolves the run id UNDER it. A run belonging to another
// tenant is not among this org's runs, so it answers absent — the same 404 a
// nonexistent id gets, which is what keeps this from being an oracle.
//
// Absence is honoured ONLY when the runtime answers it. A runtime that does not
// serve stop reports nothing about the run, and reporting "stopped" on that basis
// would be a stop that cannot fail — so it is a 502.
func stop(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	if !principal.Validated(c) {
		return zip.ErrForbidden("a validated principal is required to stop a bot")
	}
	runID := strings.TrimSpace(c.Param("runId"))
	if runID == "" {
		return zip.ErrBadRequest("runId is required")
	}
	if len(runID) > maxRunID {
		return zip.ErrNotFound("no such bot for this org")
	}
	switch err := s.State.runtime.Stop(c.Context(), org, runID); {
	case err == nil:
		s.Log.Info("bot stopped", "org", org, "run", runID)
		return c.JSON(http.StatusOK, stopView{RunID: runID, Status: statusStopped})
	case errors.Is(err, runtime.ErrNotFound):
		return zip.ErrNotFound("no such bot for this org")
	case errors.Is(err, runtime.ErrNotServed):
		return zip.Errorf(http.StatusBadGateway,
			"bots: the runtime does not serve stop, so this run's state is unknown — it was NOT stopped")
	default:
		return zip.Errorf(http.StatusBadGateway, "bots: the runtime could not stop this run: %v", err)
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
