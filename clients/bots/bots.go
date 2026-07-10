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
	"crypto/rand"
	"encoding/hex"
	"fmt"
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
)

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
		Base:  cloud.NewBase(deps, "bots"),
		State: state{bill: cloud.NewResourceMeter(deps, meterKind), gateway: gatewayBase()},
	}
	routes(app, s)
	s.Log.Info("bots surface mounted", "gateway", s.State.gateway,
		"billing", s.State.bill.Enabled(), "brand", deps.Brand)
	return nil
}

// routes registers the bots surface.
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Post("/v1/bots/run", cloud.Handle(s, run))
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
	if gateErr := s.State.bill.Gate(c.Context(), org, principal.Project(c), meterKind, fee); gateErr != nil {
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

func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}
