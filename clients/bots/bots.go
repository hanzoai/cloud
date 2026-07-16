// Package bots is the CONTROL PLANE for a bot run: a task the bot runtime
// executes on a surface — a booted desktop or terminal sandbox it drives — with a
// LIVE session (the URL the hanzo.app /vnc panel embeds to watch/attach).
//
// A "bot run" is ONE value with ONE home. It is not the bot MACHINE that hosts a
// runtime (visor's /v1/compute/bots — a machine you rent), and it is not the
// runtime service itself (clients/bot — the executor behind the seam below).
//
// Cloud is the backend: this package owns everything a control plane owns, and
// the runtime owns only EXECUTION.
//
//  1. authenticate the caller (a run MOVES MONEY, so a VALIDATED principal is
//     required — never a bare, forgeable org header);
//  2. gate + meter the run against the caller's OWN org ledger (a flat per-run
//     fee, the same ResourceMeter path every non-LLM resource uses);
//  3. RECORD the run in the org-scoped session plane (clients/agents) — the
//     registry of record for what an agent is doing, which coding runs already
//     share — and derive the sessionUrl from its id;
//  4. authorize list/stop against THAT record, then drive the runtime.
//
// Isolation is a property of this package, not of the runtime. The org is the
// gateway-minted X-Org-Id (HIP-0026) resolved via principal.Org, NEVER a request
// field, and every read/stop resolves (org, runId) TOGETHER against the org-scoped
// store: another tenant's run is simply not found. The runtime is told which run
// to halt only AFTER ownership is proven here, so a buggy or hostile runtime
// cannot widen a caller's reach — it is an executor, never an authority.
//
// Surface (org-scoped; the console BotsApi and the CLI `hanzo bot run` call it):
//
//	POST /v1/bots/run           {task, surface, gpu, timeout} -> {runId, status, sessionUrl}
//	GET  /v1/bots                                             -> {bots:[{runId,task,surface,status,sessionUrl,startedAt}]}
//	POST /v1/bots/:runId/stop                                 -> {runId, status}
//
// The billed unit is a flat per-RUN fee — the honest, policy-set unit a bot
// launch bills. GB-seconds / GPU-hour metering is intentionally NOT fabricated
// here: this endpoint launches the run, it does not observe its runtime
// footprint (visor/bot-gateway do). The fee is ops-configurable per deployment
// via cloud.ResourceFeeCents(botFeeEnvPrefix, meterKind); set it to 0 to make
// bot launches free (and therefore un-gated), exactly like the agents run fee.
package bots

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/metering"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// rfc3339 renders a unix timestamp for the wire; 0 (never started/ended) is "".
func rfc3339(sec int64) string {
	if sec == 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

const (
	// meterKind is the commerce "provider"/attribution label for bot spend — the
	// product:"bot". One value so every bot launch is attributed identically on
	// the ledger.
	meterKind = "bot"

	// botFeeEnvPrefix is the operator knob for the flat per-run launch fee. The
	// effective fee is cloud.ResourceFeeCents(botFeeEnvPrefix, meterKind): a
	// CLOUD_BOT_FEE_CENTS override wins over the $1.00 default; set it to 0 to
	// make bot launches free (and therefore un-gated). A flat per-RUN fee — the
	// policy-set unit a launch bills; NOT a fabricated GB-second/GPU-hour price.
	botFeeEnvPrefix = "CLOUD_BOT_FEE_CENTS"

	// gatewayURLEnv configures the browser-facing bot VNC gateway base — the
	// public origin the TS bot service serves /vnc?nodeId=<id> from, which the
	// hanzo.app /vnc panel embeds. It is DISTINCT from clients/bot's server-side
	// BOT_GATEWAY_URL (the in-cluster reverse-proxy target http://bot-gateway.hanzo.svc,
	// which a browser cannot reach): a session URL must be publicly embeddable, so
	// it carries its own knob. Ops sets it per deployment/brand.
	gatewayURLEnv     = "CLOUD_BOT_GATEWAY_URL"
	defaultGatewayURL = "https://bot.hanzo.ai"

	// maxTask bounds the launch task/prompt at the create boundary.
	maxTask = 32 * 1024
	// maxRunID bounds the :runId path param before it reaches the registry — an
	// oversize id is not a run this org owns, so it is a 404 like any other miss.
	maxRunID = 128
	// maxTimeout caps the requested wall-clock so a client can't ask for an
	// unbounded run; the runtime enforces the real limit, this is the sane input
	// bound.
	maxTimeout = 24 * time.Hour

	surfaceDesktop  = "desktop"
	surfaceTerminal = "terminal"

	// statusRunning is the launch outcome this endpoint reports: the run was
	// authorized, metered, and its live session URL is returned for the client to
	// attach to. The bot's fine-grained runtime lifecycle (booting/ready/stopped)
	// is visor/bot-gateway's concern and is observed there — this orchestration
	// record carries ONE status: the launch succeeded.
	statusRunning = "running"

	// statusStopped is the terminal outcome POST /v1/bots/:runId/stop reports once
	// the bot-gateway has torn the run's live session down.
	statusStopped = "stopped"
)

// Runs is the run-registry seam: the org-scoped record of every bot run, which
// is the ONE thing the control plane authorizes against. Backed in process by the
// agents session plane (adapters.go); a fake in tests. Every method takes org as
// its FIRST argument and the implementation must scope by it — Get on another
// tenant's id returns found=false, never that run.
type Runs interface {
	Open(ctx context.Context, org, actor, task, surface string) (string, error)
	List(ctx context.Context, org string) ([]Run, error)
	Get(ctx context.Context, org, runID string) (Run, bool, error)
	Stop(ctx context.Context, org, runID, reason string) (bool, error)
}

// Runtime is the bot-runtime seam: the TS service that EXECUTES a run (channels
// and skills live there and are never reimplemented in Go). Cloud drives it only
// after authorizing against Runs, so this seam carries no authority — swapping
// the transport (or faking it in a test) cannot change who may stop what.
type Runtime interface {
	Stop(ctx context.Context, org, runID string) error
}

// Run is one bot run as the control plane knows it — the projection of the
// registry row this package serves and authorizes against.
type Run struct {
	ID        string
	Task      string
	Surface   string
	Status    string
	StartedAt int64 // unix seconds
}

// state is bots' own data; shared deps live in the embedded cloud.Base.
type state struct {
	// bill is the shared per-org gate+meter (reuses deps.Metering, the ONE
	// commerce client the agents/provisioning/ml subsystems use). Nil/!Enabled()
	// makes Gate allow and Meter a no-op, so an unconfigured deployment launches
	// bots without billing rather than failing closed on a missing ledger. It stays
	// here (not Base.Bill) because its provider label is meterKind ("bot"), which
	// diverges from the subsystem name ("bots") — Base.Bill would attribute to the
	// wrong product.
	bill *cloud.ResourceMeter
	// gateway is the browser-facing bot VNC gateway base (no trailing slash) that
	// every returned sessionUrl is derived from.
	gateway string
	// runs is the registry of record for this org's runs — what list reads and
	// what stop authorizes against.
	runs Runs
	// runtime is the executor a stop drives once ownership is proven.
	runtime Runtime
}

// runReq is the boot-a-computer-using-agent body — the exact shape the CLI
// (cli/bot.go BotRunReq) sends. Org is NEVER here: it is the gateway-minted
// X-Org-Id, resolved from the validated principal, never the body.
type runReq struct {
	Task    string `json:"task"`
	Surface string `json:"surface"` // desktop | terminal
	GPU     bool   `json:"gpu"`
	Timeout string `json:"timeout"` // optional wall-clock, e.g. "30m"
}

// runView is the live-session descriptor — the exact shape the CLI
// (cli/bot.go BotRunResult) decodes: the run id, its status, and the URL the
// hanzo.app /vnc panel embeds.
type runView struct {
	RunID      string `json:"runId"`
	Status     string `json:"status"`
	SessionURL string `json:"sessionUrl"`
}

// botView is one row of GET /v1/bots — the console list item. sessionUrl is
// derived control-plane side from runId (the ONE place a session URL is built,
// sessionURL below), so the gateway never has to know its own public origin.
type botView struct {
	RunID      string `json:"runId"`
	Task       string `json:"task"`
	Surface    string `json:"surface"`
	Status     string `json:"status"`
	SessionURL string `json:"sessionUrl"`
	StartedAt  string `json:"startedAt"`
}

// botsView is the GET /v1/bots envelope; Bots is always non-nil so an empty org
// (or an unreachable gateway) serializes as {"bots":[]}, never {"bots":null}.
type botsView struct {
	Bots []botView `json:"bots"`
}

// stopView is the POST /v1/bots/:runId/stop response.
type stopView struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
}

// Mount wires the bots surface onto app per HIP-0106. Constructs the value directly
// (cloud.NewBase) because the metered launch fee uses a meter keyed to meterKind
// ("bot"), not the subsystem name — so it lives in State, built from Deps here.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("bots.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("bots.Mount: nil deps.Logger")
	}
	s := &cloud.Service[state]{
		Base: cloud.NewBase(deps, "bots"),
		State: state{
			bill:    cloud.NewResourceMeter(deps, meterKind),
			gateway: gatewayBase(),
			runs:    sessionRuns{},
			runtime: gatewayRuntime{},
		},
	}
	routes(app, s)
	s.Log.Info("bots surface mounted", "gateway", s.State.gateway,
		"billing", s.State.bill.Enabled(), "brand", deps.Brand)
	return nil
}

// routes registers the bots surface: launch, list, and stop. The static /run
// literal registers before the :runId param; the router resolves by specificity,
// so /v1/bots/run can never bind as a run id.
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Post("/v1/bots/run", cloud.Handle(s, run))
	app.Get("/v1/bots", cloud.Handle(s, list))
	app.Post("/v1/bots/:runId/stop", cloud.Handle(s, stop))
}

// run launches a computer-using bot: it authenticates the caller, gates+meters a
// flat per-run fee against the caller's OWN org, mints the run id, and returns
// the live VNC session descriptor. Every 200 reflects an authorized, metered
// launch — an unfunded org gets 402 and no session, an unreachable commerce 503.
func run(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	// A launch MOVES MONEY (debits the org's commerce ledger), so it requires a
	// VALIDATED principal — never a bare, forgeable X-Org-Id from the direct-to-pod
	// path. Same money-path guard the agents run + s3 + provisioning surfaces ship.
	if !principal.Validated(c) {
		return zip.ErrForbidden("a validated principal is required to launch a bot")
	}
	var body runReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	task := strings.TrimSpace(body.Task)
	if task == "" {
		return zip.ErrBadRequest("task is required")
	}
	if len(task) > maxTask {
		return zip.ErrBadRequest("task too large")
	}
	surface, err := validateSurface(body.Surface)
	if err != nil {
		return err
	}
	if err := validateTimeout(body.Timeout); err != nil {
		return err
	}

	// Pre-authorize the CALLER's org balance BEFORE launching anything (fail-
	// closed): an unfunded org gets 402 and no bot, an unreachable commerce 503.
	// project = the caller's validated org sub-scope, so a per-scope spend cap is
	// enforced on a bot launch exactly as on the request edge. fee<=0 or
	// unconfigured billing makes this a no-op (allow).
	fee := cloud.ResourceFeeCents(botFeeEnvPrefix, meterKind)
	project, projectValidated := principal.ValidatedProject(c)
	if gateErr := s.State.bill.Gate(c.Context(), principal.Payer(c), project, projectValidated, meterKind, fee); gateErr != nil {
		return cloud.DenyResource(c, gateErr)
	}

	// Record the run under the CALLER's org — this row IS the run, and its id is
	// the run id, so there is exactly one identity per run and list/stop can
	// authorize against it later. It is created BEFORE the debit so a metered run
	// always has a record (a registry failure is a 500 and costs the org nothing).
	runID, err := s.State.runs.Open(c.Context(), org, billingActor(org, c.User()), task, surface)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "bots: record run: %v", err)
	}

	// The launch is authorized and recorded. The attributable record of the SPEND
	// is the commerce ledger debit (product=bot, the surface as the billed unit,
	// the acting principal for the audit trail) — fire-and-forget, exactly like the
	// agents run fee. GPU/surface ride the log line for operator visibility.
	s.State.bill.MeterUsage(principal.Payer(c), meterKind, metering.Usage{
		AmountCents: fee,
		Model:       surface,
		Actor:       billingActor(org, c.User()),
		RequestID:   c.RequestID(),
		ClientIP:    cloud.ClientIP(c),
	})
	s.Log.Info("bot launched", "org", org, "run", runID,
		"surface", surface, "gpu", body.GPU)

	return c.JSON(http.StatusOK, runView{
		RunID:      runID,
		Status:     statusRunning,
		SessionURL: sessionURL(s, runID),
	})
}

// list returns the caller org's live bot runs, read from the registry and
// projected into the console contract with sessionUrl derived here.
//
// The org is ALWAYS the validated principal's org, NEVER a request param, and it
// is the leading predicate of the registry query — so one tenant can never
// enumerate another's runs. A registry failure is a 500: the store is ours and
// authoritative, so an empty list must mean "this org has no runs", never "the
// read broke" — reporting [] on error would hide the org's own runs from it.
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
	runs, err := s.State.runs.List(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "bots: list runs: %v", err)
	}
	out := make([]botView, 0, len(runs))
	for _, r := range runs {
		out = append(out, toBotView(s, r))
	}
	return c.JSON(http.StatusOK, botsView{Bots: out})
}

// toBotView projects a registry row into one list row, deriving sessionUrl from
// the run id — the ONE place a session URL is built.
func toBotView(s *cloud.Service[state], r Run) botView {
	return botView{
		RunID:      r.ID,
		Task:       r.Task,
		Surface:    r.Surface,
		Status:     r.Status,
		SessionURL: sessionURL(s, r.ID),
		StartedAt:  rfc3339(r.StartedAt),
	}
}

// stop terminates one of the caller org's own runs.
//
// The own-key guard is the FIRST thing that happens and it is decided on OUR
// record: (org, runId) resolve together against the org-scoped registry, so a run
// belonging to another tenant — or one that never existed — is an identical 404,
// and the runtime is never even asked about it. Only once ownership is proven is
// the executor driven; only once the executor confirms is the record closed.
//
// A runtime that cannot be reached is a clean 502 with the record left live: a
// stop that could not halt the sandbox must not claim it did. A run the runtime
// does not know is already not executing, so it closes normally (idempotent).
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
	if _, found, err := s.State.runs.Get(c.Context(), org, runID); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "bots: resolve run: %v", err)
	} else if !found {
		return zip.ErrNotFound("no such bot for this org")
	}
	if err := s.State.runtime.Stop(c.Context(), org, runID); err != nil {
		return zip.Errorf(http.StatusBadGateway, "bots: runtime unreachable: %v", err)
	}
	if _, err := s.State.runs.Stop(c.Context(), org, runID, "stopped via /v1/bots"); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "bots: close run: %v", err)
	}
	s.Log.Info("bot stopped", "org", org, "run", runID)
	return c.JSON(http.StatusOK, stopView{RunID: runID, Status: statusStopped})
}

// sessionURL derives the live VNC session URL for a run: the browser-facing bot
// gateway base + the node's VNC path. One id per run — the run id IS the node id
// the bot machine for this run registers under, so the tunnel is addressable by
// exactly the id the client holds.
func sessionURL(s *cloud.Service[state], runID string) string {
	return s.State.gateway + "/vnc?" + url.Values{"nodeId": {runID}}.Encode()
}

// validateSurface normalizes the requested sandbox. Empty defaults to desktop
// (the noVNC GUI); an unknown surface is a clean 400 rather than a launch the
// runtime can't honor.
func validateSurface(raw string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", surfaceDesktop:
		return surfaceDesktop, nil
	case surfaceTerminal:
		return surfaceTerminal, nil
	default:
		return "", zip.ErrBadRequest("surface must be 'desktop' or 'terminal'")
	}
}

// validateTimeout bounds the optional wall-clock at the boundary: it must parse
// as a Go duration and be within (0, maxTimeout]. Empty means "runtime default".
func validateTimeout(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return zip.ErrBadRequest("invalid 'timeout': " + err.Error())
	}
	if d <= 0 || d > maxTimeout {
		return zip.ErrBadRequest("timeout must be > 0 and <= 24h")
	}
	return nil
}

// billingActor is the "org/sub" identity recorded on a debit for the audit
// trail. It never selects which balance is gated — that is always the org — but
// attributes the spend to the acting principal. Falls back to the bare org when
// no validated subject is present.
func billingActor(org, sub string) string {
	sub = strings.TrimSpace(sub)
	if org != "" && sub != "" {
		return org + "/" + sub
	}
	if sub != "" {
		return sub
	}
	return org
}

// gatewayBase resolves the browser-facing bot VNC gateway base (no trailing
// slash) from CLOUD_BOT_GATEWAY_URL, falling back to the public default.
func gatewayBase() string {
	base := strings.TrimSpace(os.Getenv(gatewayURLEnv))
	if base == "" {
		base = defaultGatewayURL
	}
	return strings.TrimRight(base, "/")
}
