// Package bots mounts the Hanzo Cloud POST /v1/bots/run surface: launch a
// computer-using agent (a "bot") — a booted desktop or terminal sandbox the
// operative computer-use runtime drives to do a task — and hand back a LIVE
// session (the URL the hanzo.app /vnc panel embeds to watch/attach).
//
// This handler is a THIN ORCHESTRATOR, deliberately. It does NOT boot machines,
// speak VNC, or reimplement visor: those live in the separate TS `bot` service
// (its gateway exposes the browser<->gateway<->node HMAC VNC tunnel at
// /vnc?nodeId=<id>) and in visor (machine provisioning). This handler owns the
// three things a cloud control-plane owns:
//
//  1. authenticate the caller (a run MOVES MONEY, so a VALIDATED principal is
//     required — never a bare, forgeable org header);
//  2. gate + meter the run against the caller's OWN org ledger (a flat per-run
//     fee, the same ResourceMeter path every non-LLM resource uses);
//  3. mint the run id and return the session descriptor whose sessionUrl points
//     at the bot VNC gateway for that run.
//
// Tenant isolation is the gateway-minted X-Org-Id (HIP-0026), resolved via
// principal.Org and NEVER read from the request body — so one tenant can
// never launch, or bill, a bot against another's org.
//
// Surface (org-scoped; the CLI `hanzo bot run` calls it):
//
//	POST /v1/bots/run  {task, surface, gpu, timeout}  -> {runId, status, sessionUrl}
//
// The billed unit is a flat per-RUN fee — the honest, policy-set unit a bot
// launch bills. GB-seconds / GPU-hour metering is intentionally NOT fabricated
// here: this endpoint launches the run, it does not observe its runtime
// footprint (visor/bot-gateway do). The fee is ops-configurable per deployment
// via cloud.ResourceFeeCents(botFeeEnvPrefix, meterKind); set it to 0 to make
// bot launches free (and therefore un-gated), exactly like the agents run fee.
package bots

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/commerce/metering"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

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

	// serverGatewayURLEnv is the IN-CLUSTER, server-side bot-gateway base the
	// control plane calls to list/stop live runs. It is the SAME knob clients/bot's
	// reverse proxy uses (BOT_GATEWAY_URL, default http://bot-gateway.hanzo.svc) —
	// NOT the browser-facing gatewayURLEnv above (a pod-internal DNS name a browser
	// can't reach). List/stop are server->server calls, so they ride the in-cluster
	// target; only the returned sessionUrl carries the public origin.
	serverGatewayURLEnv     = "BOT_GATEWAY_URL"
	defaultServerGatewayURL = "http://bot-gateway.hanzo.svc"

	// gatewayCallTimeout bounds a single list/stop round-trip to the bot-gateway so
	// a hung gateway can't stall the control-plane request; on timeout list is
	// honest-empty and stop is a clean 502.
	gatewayCallTimeout = 15 * time.Second

	// maxTask bounds the launch task/prompt at the create boundary.
	maxTask = 32 * 1024
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

// identityHeaders are the gateway-minted tenant-context headers forwarded on a
// server-side list/stop call so the bot-gateway scopes the operation to the SAME
// caller — the exact set clients/bot's reverse proxy forwards. X-Org-Id is set
// explicitly to the validated org (never the raw request header), so a forged
// org can never reach the gateway; the rest ride through for the audit trail.
var identityHeaders = []string{
	"Authorization", "X-User-Id", "X-User-Email", "X-Project-Id", "X-Environment",
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
	// serverGateway is the in-cluster bot-gateway base (no trailing slash) the
	// control plane calls server-side to list + stop an org's live runs.
	serverGateway string
	// cc is the outbound client for those server->server calls; a bounded timeout
	// keeps a hung gateway from stalling the request (list falls back to empty).
	cc *http.Client
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

// gatewayBot is the bot-gateway's session shape: the caller's run minus the
// sessionUrl (control-plane-derived). Its own /v1/bots emits exactly these
// fields; a shape drift that fails to decode collapses to honest-empty.
type gatewayBot struct {
	RunID     string `json:"runId"`
	Task      string `json:"task"`
	Surface   string `json:"surface"`
	Status    string `json:"status"`
	StartedAt string `json:"startedAt"`
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
			bill:          cloud.NewResourceMeter(deps, meterKind),
			gateway:       gatewayBase(),
			serverGateway: serverGatewayBase(),
			cc:            &http.Client{Timeout: gatewayCallTimeout},
		},
	}
	routes(app, s)
	s.Log.Info("bots surface mounted", "gateway", s.State.gateway,
		"serverGateway", s.State.serverGateway, "billing", s.State.bill.Enabled(),
		"brand", deps.Brand)
	return nil
}

// routes registers the bots surface: launch, list, and stop. list/stop are the
// read/lifecycle half — org-scoped proxies onto the bot-gateway's live runs.
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

	runID, err := genID("bot")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}

	// Pre-authorize the CALLER's org balance BEFORE returning a session (fail-
	// closed): an unfunded org gets 402 and no bot, an unreachable commerce 503.
	// project = the caller's validated org sub-scope, so a per-scope spend cap is
	// enforced on a bot launch exactly as on the request edge. fee<=0 or
	// unconfigured billing makes this a no-op (allow).
	fee := cloud.ResourceFeeCents(botFeeEnvPrefix, meterKind)
	project, projectValidated := principal.ValidatedProject(c)
	if gateErr := s.State.bill.Gate(c.Context(), org, project, projectValidated, meterKind, fee); gateErr != nil {
		return cloud.DenyResource(c, gateErr)
	}

	// The launch is authorized. The durable, attributable record of this run is
	// the commerce ledger debit (product=bot, the surface as the billed unit, the
	// acting principal for the audit trail) — fire-and-forget, exactly like the
	// agents run fee. GPU/surface ride the log line for operator visibility.
	s.State.bill.MeterUsage(org, meterKind, metering.Usage{
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

// list returns the caller org's live bot runs. It proxies the bot-gateway's
// org-scoped session list (server-side, forwarding the caller's tenant context)
// and normalizes each row into the console contract, deriving sessionUrl here.
//
// The org is ALWAYS the validated principal's org, NEVER a request param — one
// tenant can never enumerate another's runs. It is honest-empty by construction:
// an unconfigured or unreachable gateway, a non-2xx, or a shape it can't decode
// all yield {"bots":[]} (a 200), never a 5xx — the console renders "no bots"
// rather than an error when the runtime plane is simply down.
func list(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	// Org-scoping is only trustworthy behind a validated principal: a bare,
	// forgeable X-Org-Id (the direct-to-pod path) must not enumerate a victim
	// tenant's runs. Same guard clients/bot's proxy applies before handing the
	// gateway a tenant context.
	if !principal.Validated(c) {
		return zip.ErrForbidden("a validated principal is required to list bots")
	}
	return c.JSON(http.StatusOK, botsView{Bots: fetchBots(s, c, org)})
}

// stop terminates one of the caller org's live runs. It proxies the bot-gateway's
// org-scoped stop; a run the caller's org does not own is a 404 (never a 200,
// never another tenant's teardown). An unreachable gateway is a clean 502 — a
// stop that could not reach the runtime must not claim the run was stopped.
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
	code, err := stopBot(s, c, org, runID)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "bots: gateway unreachable: %v", err)
	}
	switch {
	case code >= 200 && code < 300:
		s.Log.Info("bot stopped", "org", org, "run", runID)
		return c.JSON(http.StatusOK, stopView{RunID: runID, Status: statusStopped})
	case code == http.StatusNotFound:
		return zip.ErrNotFound("no such bot for this org")
	default:
		return zip.Errorf(http.StatusBadGateway, "bots: gateway rejected stop (%d)", code)
	}
}

// fetchBots calls the bot-gateway's GET /v1/bots scoped to org and maps the
// result into the contract. Every failure path — no server gateway configured,
// build error, transport error, non-2xx, or an undecodable body — returns a
// non-nil empty slice so the caller serializes {"bots":[]} and never a 5xx.
func fetchBots(s *cloud.Service[state], c *zip.Ctx, org string) []botView {
	out := []botView{}
	if s.State.serverGateway == "" {
		return out
	}
	req, err := gatewayRequest(c, http.MethodGet, s.State.serverGateway+"/v1/bots", org, nil)
	if err != nil {
		return out
	}
	resp, err := s.State.cc.Do(req)
	if err != nil {
		s.Log.Warn("bots list: gateway unreachable, returning empty", "org", org, "err", err)
		return out
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.Log.Warn("bots list: gateway non-2xx, returning empty", "org", org, "status", resp.StatusCode)
		return out
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return out
	}
	var decoded struct {
		Bots []gatewayBot `json:"bots"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		s.Log.Warn("bots list: undecodable gateway body, returning empty", "org", org, "err", err)
		return out
	}
	for _, b := range decoded.Bots {
		runID := strings.TrimSpace(b.RunID)
		if runID == "" {
			continue
		}
		status := strings.TrimSpace(b.Status)
		if status == "" {
			status = statusRunning
		}
		out = append(out, botView{
			RunID:      runID,
			Task:       b.Task,
			Surface:    b.Surface,
			Status:     status,
			SessionURL: sessionURL(s, runID),
			StartedAt:  b.StartedAt,
		})
	}
	return out
}

// stopBot calls the bot-gateway's POST /v1/bots/{runId}/stop scoped to org and
// returns the gateway status code. A transport failure is a non-nil error the
// caller maps to 502; the status code drives 200-vs-404.
func stopBot(s *cloud.Service[state], c *zip.Ctx, org, runID string) (int, error) {
	if s.State.serverGateway == "" {
		return 0, fmt.Errorf("bot gateway not configured")
	}
	target := s.State.serverGateway + "/v1/bots/" + url.PathEscape(runID) + "/stop"
	req, err := gatewayRequest(c, http.MethodPost, target, org, bytes.NewReader(nil))
	if err != nil {
		return 0, err
	}
	resp, err := s.State.cc.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, nil
}

// gatewayRequest builds a server-side call to the bot-gateway carrying the
// caller's tenant context: X-Org-Id is pinned to the validated org, the other
// identity headers ride through, so the gateway scopes to exactly this caller.
func gatewayRequest(c *zip.Ctx, method, target, org string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(c.Context(), method, target, body)
	if err != nil {
		return nil, err
	}
	for _, h := range identityHeaders {
		if v := c.Header(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	// Pin the validated org last so a forged X-Org-Id in the incoming headers can
	// never override the tenant the gateway scopes to.
	req.Header.Set("X-Org-Id", org)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
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

// serverGatewayBase resolves the in-cluster bot-gateway base (no trailing slash)
// the control plane calls server-side for list/stop, from BOT_GATEWAY_URL — the
// SAME knob clients/bot's reverse proxy uses — falling back to the in-cluster
// service DNS default.
func serverGatewayBase() string {
	base := strings.TrimSpace(os.Getenv(serverGatewayURLEnv))
	if base == "" {
		base = defaultServerGatewayURL
	}
	return strings.TrimRight(base, "/")
}

func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}
