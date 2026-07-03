// Package agents mounts the Hanzo Cloud /v1/agents surface: per-org autonomous
// agent definitions and their runs. An agent is a model + a system prompt
// (instructions) + a set of tool names; running one executes a real chat
// completion through the in-process AI client (the SAME gateway path the rest
// of the console uses) and records the run. Tenant isolation is the
// gateway-minted X-Org-Id (HIP-0026) enforced as the org column on every
// query, so one tenant can never read, run, or delete another's agents.
//
// Surface (all org-scoped; console2's AgentsModule reads {agents:[...]}):
//
//	GET    /v1/agents                list agents for the org      -> {agents:[...]}
//	POST   /v1/agents                create an agent              -> Agent
//	GET    /v1/agents/:name          agent detail + recent runs   -> AgentDetail
//	PATCH  /v1/agents/:name          update an agent              -> Agent
//	DELETE /v1/agents/:name          delete an agent (+ its runs)
//	POST   /v1/agents/:name/run      run the agent {input}        -> RunResult
//	GET    /v1/agents/:name/runs     run history                  -> {runs:[...]}
//
// The store is SQLite in deps.DataDir (Base/SQLite-only). It holds definitions
// and run I/O only — never a secret; tool credentials live in KMS by reference.
package agents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/types"
	"github.com/hanzoai/commerce/metering"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// nameRE is the org-unique handle AND the URL path segment — the traversal
// guard at the boundary.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

const (
	maxInstructions = 32 * 1024 // system prompt cap
	maxInput        = 128 * 1024
	// maxRef bounds the free-text bot-lifecycle references (compute machine id,
	// service-account id). They are opaque identifiers, not documents — a
	// generous 256 keeps a client from bloating the per-org SQLite with a
	// multi-megabyte "id".
	maxRef = 256

	// agentFeeEnvPrefix is the operator knob for the flat per-run fee. The
	// effective fee is cloud.ResourceFeeCents(agentFeeEnvPrefix, meterKind): a
	// global CLOUD_AGENT_FEE_CENTS override wins over the $1.00 default; set it
	// to 0 to make agent runs free (and therefore un-gated). This is a per-RUN
	// fee — the honest, policy-set unit an agent run bills. Token-based pricing
	// is intentionally NOT used here: the in-process AIClient returns only the
	// completion content (types.ChatResponse{Content}), no token counts, so
	// charging per-token would be fabricated. Duration is recorded on the run.
	agentFeeEnvPrefix = "CLOUD_AGENT_FEE_CENTS"
	// meterKind is the commerce "provider"/attribution label for agent spend —
	// the task's product:"agent". One value so every agent run (HTTP or
	// scheduled) is attributed identically.
	meterKind = "agent"
	// schedulerActor is the Actor recorded on a scheduled run that has no IAM
	// service account bound. Real service-account identity (the keystone) rides
	// in Agent.ServiceAccountID when present.
	schedulerActor = "scheduler"
	// maxLongRunningPerOrg caps an org's scheduler footprint: how many scheduled
	// long-running agents it may create. Each scheduled agent adds recurring
	// gate+run+debit load to the shared store, so a per-org bound stops one
	// tenant from self-amplifying the once-a-minute scan. Overridable by ops via
	// CLOUD_AGENT_MAX_LONG_RUNNING.
	maxLongRunningPerOrg = 100
	longRunningCapEnv    = "CLOUD_AGENT_MAX_LONG_RUNNING"
)

type svc struct {
	store *Store
	ai    types.AIClient
	log   luxlog.Logger
	// bill is the shared per-org gate+meter (reuses deps.Metering, the ONE
	// commerce client — the same object ml/provisioning use). Nil/!Enabled()
	// makes Gate allow and Meter a no-op, so an unconfigured deployment runs
	// agents without billing rather than failing closed on a missing ledger.
	bill *cloud.ResourceMeter
	// sched is the long-running-agent scheduler; nil until started, stopped on
	// Shutdown. It shares svc so it runs agents through the SAME runAgent path.
	sched *scheduler
	// bus is the in-process fan-out behind the live session/event stream (SSE +
	// ZAP). Set in Mount; nil-safe (a direct-construct unit test skips fan-out).
	bus *bus
	// tasks is the seam to the hanzoai/tasks durable-execution engine that control
	// commands forward to for task-backed sessions. Defaults to the disabled
	// controller (record-only) until a live tasks client is wired in Mount.
	tasks TaskController
}

var mounted *svc

// ---- HTTP response shapes (the published contract) ----

type agentView struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Model            string   `json:"model"`
	Description      string   `json:"description,omitempty"`
	Tools            []string `json:"tools"`
	Status           string   `json:"status"`
	ExecutionMode    string   `json:"executionMode"`
	Schedule         string   `json:"schedule,omitempty"`
	ComputeRef       string   `json:"computeRef,omitempty"`
	ServiceAccountID string   `json:"serviceAccountId,omitempty"`
	Runs             int      `json:"runs"`
	CreatedAt        string   `json:"createdAt"`
	UpdatedAt        string   `json:"updatedAt"`
}

type agentDetail struct {
	agentView
	Instructions string    `json:"instructions"`
	RecentRuns   []runView `json:"recentRuns"`
}

type runView struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Model      string `json:"model"`
	Input      string `json:"input"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"durationMs"`
	CreatedAt  string `json:"createdAt"`
}

// ---- overview shapes (console2 Agents dashboard: metrics + activity) ----
//
// These mirror the console's normalizers EXACTLY (console2 src/lib/api/agents.ts:
// normalizeMetrics reads {series:[{key,points:[{t,v}]}], resource:{...}};
// normalizeActivity reads {activity:[{id,kind,agent,message,at}]}). Every number
// is derived from real agent_runs rows — never a fabricated trend. A metric this
// store cannot source (CPU/mem/storage/cost metering) is emitted as JSON null so
// the shape is honest and the UI renders "—".

type seriesPoint struct {
	T string `json:"t"` // bucket start, RFC3339 UTC
	V int    `json:"v"` // real invocation count in the bucket
}

type seriesLine struct {
	Key    string        `json:"key"` // agent name
	Points []seriesPoint `json:"points"`
}

// resourceUsage is the Resource Usage panel rollup. This store holds agent
// definitions and run I/O only — it does NOT meter CPU/memory/storage/cost — so
// every field is nil, marshalling to explicit JSON null (honest "no data", not 0).
type resourceUsage struct {
	CPUVcpuHours   *float64 `json:"cpuVcpuHours"`
	MemGbHours     *float64 `json:"memGbHours"`
	StorageIoBytes *float64 `json:"storageIoBytes"`
	CostCents      *float64 `json:"costCents"`
}

type metricsView struct {
	Range    string        `json:"range"`  // echoes the requested window (24H|7D|30D)
	Series   []seriesLine  `json:"series"` // per-agent invocation histogram (real)
	Resource resourceUsage `json:"resource"`
}

type activityView struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`  // invoked|failed|created|updated (from real events)
	Agent   string `json:"agent"` // agent name
	Message string `json:"message,omitempty"`
	At      string `json:"at"` // RFC3339 UTC
}

func rfc3339(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func toView(a Agent, runs int) agentView {
	return agentView{
		ID: a.ID, Name: a.Name, Model: a.Model, Description: a.Description,
		Tools: nonNil(a.Tools), Status: a.Status,
		ExecutionMode: a.ExecutionMode, Schedule: a.Schedule,
		ComputeRef: a.ComputeRef, ServiceAccountID: a.ServiceAccountID,
		Runs:      runs,
		CreatedAt: rfc3339(a.CreatedAt), UpdatedAt: rfc3339(a.UpdatedAt),
	}
}

func toRunView(r Run) runView {
	return runView{
		ID: r.ID, Status: r.Status, Model: r.Model, Input: r.Input, Output: r.Output,
		Error: r.Error, DurationMs: r.DurationMs, CreatedAt: rfc3339(r.CreatedAt),
	}
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// Mount wires the agents surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("agents.Mount: nil zip.App")
	}
	log := deps.Logger
	if log == nil {
		return fmt.Errorf("agents.Mount: nil deps.Logger")
	}
	log = log.New("subsystem", "agents")
	if deps.DataDir == "" {
		return fmt.Errorf("agents.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("agents.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "agents.db"))
	if err != nil {
		return fmt.Errorf("agents.Mount: open store: %w", err)
	}
	// deps.AI may be nil when no gateway is configured; run() degrades honestly.
	s := &svc{
		store: store,
		ai:    deps.AI,
		log:   log,
		bill:  cloud.NewResourceMeter(deps, meterKind),
		bus:   newBus(),
		// TASKS PLUG-IN POINT: durable execution rides hanzoai/tasks, not a
		// bespoke engine. Default is record-only; wiring client.Dial(TASKS_URL)
		// from github.com/hanzoai/tasks/pkg/sdk/client here makes control forward
		// to the engine's Signal/Cancel API (see sessions_tasks.go).
		tasks: disabledTaskController{},
	}
	mounted = s

	app.Get("/v1/agents", s.list)
	app.Post("/v1/agents", s.create)
	// Static org-wide surfaces MUST register before the :name wildcard: Fiber
	// matches routes in registration order, so a bare `/v1/agents/:name` would
	// otherwise capture "metrics"/"activity"/"sessions" as a name and 404 them
	// (Red route audit). Registering the literals first makes them win.
	app.Get("/v1/agents/metrics", s.metrics)
	app.Get("/v1/agents/activity", s.activity)
	// Live agent-session control plane: /v1/agents/sessions[/...]. Registered
	// before :name for the same registration-order reason (and internally the
	// static /stream precedes /:id).
	s.mountSessions(app)
	app.Get("/v1/agents/:name", s.get)
	app.Patch("/v1/agents/:name", s.update)
	app.Delete("/v1/agents/:name", s.del)
	app.Post("/v1/agents/:name/run", s.run)
	app.Get("/v1/agents/:name/runs", s.runs)

	// Long-running scheduler: invokes each long-running agent's run on its cron
	// cadence through the SAME runAgent path as the HTTP handler (one run path,
	// one gate, one meter). Only started when inference is wired — with no AI a
	// scheduled run could never execute, so there is nothing to schedule.
	if s.ai != nil {
		s.sched = newScheduler(s, log)
		s.sched.start()
	}

	log.Info("agents mounted", "ai", s.ai != nil, "billing", s.bill.Enabled(),
		"scheduler", s.sched != nil, "brand", deps.Brand)
	return nil
}

func init() {
	cloud.RegisterWithShutdown("agents", 127, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("agents.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	}, func(ctx context.Context) error {
		// Graceful teardown: stop the scheduler (drain in-flight runs) and close
		// the store. Bounded by the caller's shutdown deadline so a stuck run
		// can't hang SIGTERM.
		return Shutdown(ctx)
	})
}

// ---- handlers ----

type createReq struct {
	Name             string   `json:"name"`
	Model            string   `json:"model"`
	Instructions     string   `json:"instructions"`
	Description      string   `json:"description"`
	Tools            []string `json:"tools"`
	ExecutionMode    string   `json:"executionMode"`
	Schedule         string   `json:"schedule"`
	ComputeRef       string   `json:"computeRef"`
	ServiceAccountID string   `json:"serviceAccountId"`
}

func (s *svc) create(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body createReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return zip.ErrBadRequest("name is required")
	}
	if !nameRE.MatchString(name) {
		return zip.ErrBadRequest("name must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	model := strings.TrimSpace(body.Model)
	if model == "" {
		return zip.ErrBadRequest("model is required")
	}
	if len(body.Instructions) > maxInstructions {
		return zip.ErrBadRequest("instructions too large")
	}
	mode, schedule, err := validateLifecycle(body.ExecutionMode, body.Schedule)
	if err != nil {
		return err
	}
	computeRef, err := validateRef("computeRef", body.ComputeRef)
	if err != nil {
		return err
	}
	serviceAccountID, err := validateRef("serviceAccountId", body.ServiceAccountID)
	if err != nil {
		return err
	}
	// Cap the org's scheduler footprint (Red LOW-1): a tenant cannot create an
	// unbounded number of scheduled agents that each add recurring load to the
	// shared store. Only counts when this create is itself long-running.
	if mode == ModeLongRunning {
		n, err := s.store.CountLongRunning(c.Context(), org)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "count: %v", err)
		}
		if n >= longRunningCap() {
			return zip.Errorf(http.StatusConflict,
				"long-running agent limit reached for this org (max %d)", longRunningCap())
		}
	}
	id, err := genID("agent")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	a := Agent{
		ID: id, Org: org, Name: name, Model: model, Instructions: body.Instructions,
		Description: strings.TrimSpace(body.Description), Tools: cleanList(body.Tools),
		Status: "ready", ExecutionMode: mode, Schedule: schedule,
		ComputeRef: computeRef, ServiceAccountID: serviceAccountID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.Create(c.Context(), a); err != nil {
		if err == errConflict {
			return zip.ErrConflict("agent already exists in this org")
		}
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	return c.JSON(http.StatusCreated, toView(a, 0))
}

func (s *svc) list(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := s.store.List(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]agentView, 0, len(rows))
	for _, a := range rows {
		n, err := s.store.CountRuns(c.Context(), org, a.Name)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "runs: %v", err)
		}
		out = append(out, toView(a, n))
	}
	return c.JSON(http.StatusOK, map[string]any{"agents": out})
}

func (s *svc) get(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name := nameParam(c)
	a, err := s.store.Get(c.Context(), org, name)
	if err == errNotFound {
		return zip.ErrNotFound("agent not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	runs, err := s.store.ListRuns(c.Context(), org, name, 20)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "runs: %v", err)
	}
	rv := make([]runView, 0, len(runs))
	for _, r := range runs {
		rv = append(rv, toRunView(r))
	}
	return c.JSON(http.StatusOK, agentDetail{
		agentView: toView(a, len(runs)), Instructions: a.Instructions, RecentRuns: rv,
	})
}

type updateReq struct {
	Model            *string   `json:"model"`
	Instructions     *string   `json:"instructions"`
	Description      *string   `json:"description"`
	Tools            *[]string `json:"tools"`
	ExecutionMode    *string   `json:"executionMode"`
	Schedule         *string   `json:"schedule"`
	ComputeRef       *string   `json:"computeRef"`
	ServiceAccountID *string   `json:"serviceAccountId"`
}

func (s *svc) update(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name := nameParam(c)
	a, err := s.store.Get(c.Context(), org, name)
	if err == errNotFound {
		return zip.ErrNotFound("agent not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	var body updateReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if body.Model != nil {
		m := strings.TrimSpace(*body.Model)
		if m == "" {
			return zip.ErrBadRequest("model cannot be empty")
		}
		a.Model = m
	}
	if body.Instructions != nil {
		if len(*body.Instructions) > maxInstructions {
			return zip.ErrBadRequest("instructions too large")
		}
		a.Instructions = *body.Instructions
	}
	if body.Description != nil {
		a.Description = strings.TrimSpace(*body.Description)
	}
	if body.Tools != nil {
		a.Tools = cleanList(*body.Tools)
	}
	if body.ComputeRef != nil {
		if a.ComputeRef, err = validateRef("computeRef", *body.ComputeRef); err != nil {
			return err
		}
	}
	if body.ServiceAccountID != nil {
		if a.ServiceAccountID, err = validateRef("serviceAccountId", *body.ServiceAccountID); err != nil {
			return err
		}
	}
	// Re-validate the lifecycle from the RESULTING mode+schedule so a partial
	// update can't leave a long-running agent without a valid cron (which the
	// scheduler would then skip forever). Absent fields keep the stored value.
	wasLongRunning := a.ExecutionMode == ModeLongRunning
	mode, schedule := a.ExecutionMode, a.Schedule
	if body.ExecutionMode != nil {
		mode = *body.ExecutionMode
	}
	if body.Schedule != nil {
		schedule = *body.Schedule
	}
	if a.ExecutionMode, a.Schedule, err = validateLifecycle(mode, schedule); err != nil {
		return err
	}
	// Enforce the per-org scheduler cap on a TRANSITION into long-running, so a
	// tenant can't sidestep the create-time cap by making N one-shot agents and
	// PATCHing them to long-running (Red LOW-1 follow-up). Only counts when the
	// agent was NOT already long-running (a no-op re-save of an existing
	// long-running agent must not 409 against its own row).
	if a.ExecutionMode == ModeLongRunning && !wasLongRunning {
		n, cerr := s.store.CountLongRunning(c.Context(), org)
		if cerr != nil {
			return zip.Errorf(http.StatusInternalServerError, "count: %v", cerr)
		}
		if n >= longRunningCap() {
			return zip.Errorf(http.StatusConflict,
				"long-running agent limit reached for this org (max %d)", longRunningCap())
		}
	}
	a.UpdatedAt = time.Now().Unix()
	if err := s.store.Update(c.Context(), a); err != nil {
		if err == errNotFound {
			return zip.ErrNotFound("agent not found")
		}
		return zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	n, _ := s.store.CountRuns(c.Context(), org, name)
	return c.JSON(http.StatusOK, toView(a, n))
}

func (s *svc) del(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	deleted, err := s.store.Delete(c.Context(), org, nameParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("agent not found")
	}
	return c.NoContent(http.StatusNoContent)
}

type runReq struct {
	Input string `json:"input"`
}

// run executes the agent: it composes the agent's instructions with the caller
// input and runs a real chat completion via the in-process AI client, then
// records the run. Every returned run reflects an execution that actually
// happened — an inference failure is recorded and returned as an error run, not
// hidden and not fabricated.
func (s *svc) run(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	// A run MOVES MONEY (it debits org's commerce ledger), so it requires a
	// VALIDATED principal — not merely a client-supplied X-Org-Id. SanitizeIdentity
	// sets X-User-Id (c.User()) ONLY from a JWT it verified, and on the no-bearer
	// direct-to-pod path it restores the client's raw X-Org-Id but leaves X-User-Id
	// EMPTY. Gating the run on c.User() refuses exactly that anonymous-forge path:
	// without it, a direct caller could set X-Org-Id to any tenant and charge a run
	// against that victim's balance (Red MEDIUM-2). Read/list/create are the softer
	// Phase-1 data path; the money action is held to the higher bar. Same guard the
	// s3 / provisioning subsystems use.
	if strings.TrimSpace(c.User()) == "" {
		return zip.ErrForbidden("a validated principal is required to run an agent")
	}
	name := nameParam(c)
	a, err := s.store.Get(c.Context(), org, name)
	if err == errNotFound {
		return zip.ErrNotFound("agent not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	var body runReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if len(body.Input) > maxInput {
		return zip.ErrBadRequest("input too large")
	}
	if s.ai == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "inference is not configured on this deployment")
	}

	// Pre-authorize the caller's org balance BEFORE any inference (fail-closed).
	// The actor is the validated principal (org/sub) when present, else the bare
	// org — recorded on the debit for attribution. Gating here means an unfunded
	// org gets 402 and NO free inference; an unreachable commerce gets 503.
	actor := billingActor(org, c.User())
	r, gateErr := s.runAgent(c.Context(), a, body.Input, actor, c.RequestID(), cloud.ClientIP(c))
	if gateErr != nil {
		return cloud.DenyResource(c, gateErr)
	}
	if r.Status != "ok" {
		// The run is recorded; surface the upstream failure honestly.
		return c.JSON(http.StatusBadGateway, toRunView(r))
	}
	return c.JSON(http.StatusOK, toRunView(r))
}

// runAgent is the ONE run path — shared by the HTTP handler and the scheduler.
// It (1) pre-authorizes the AGENT's OWN org balance (fail-closed) so no unfunded
// tenant ever gets free inference, (2) executes one real completion, (3) records
// the run regardless of outcome (the history is real), and (4) debits the run
// fee to the agent's org ONLY on success. A non-nil error is a BALANCE-GATE
// denial (out-of-funds / commerce-unknown) that the caller renders (402/503) —
// it means no run happened. A run that executed but the model failed returns a
// recorded error-status Run and a nil error.
func (s *svc) runAgent(ctx context.Context, a Agent, input, actor, requestID, clientIP string) (Run, error) {
	fee := cloud.ResourceFeeCents(agentFeeEnvPrefix, meterKind)
	// Gate the AGENT's own org — never a caller default, never another tenant.
	// fee<=0 or unconfigured billing makes this a no-op (allows).
	if err := s.bill.Gate(ctx, a.Org, meterKind, fee); err != nil {
		return Run{}, err
	}

	r := executeRun(ctx, s.ai, a.Org, a, input)
	if err := s.store.InsertRun(ctx, r); err != nil {
		s.log.Warn("record run failed", "org", a.Org, "agent", a.Name, "err", err)
	}

	// Make the run visible in the live session registry as a ROOT session (the
	// same registry the @hanzo/dev outer-agent + subagent flows use). Best-effort:
	// it NEVER fails the run — the run and its billing already happened. DRY: this
	// is the ONE run path (HTTP + scheduler), so every run becomes a session here.
	s.openRunSession(ctx, a, r, actor)

	// Bill only a successful run (mirrors the edge gate: failed work is not
	// charged). Rich attribution: product=agent (Provider), the agent's model,
	// and the actor for the audit trail. Fire-and-forget on a background context.
	if r.Status == "ok" {
		s.bill.MeterUsage(a.Org, meterKind, metering.Usage{
			AmountCents: fee,
			Model:       a.Model,
			Actor:       actor,
			RequestID:   requestID,
			ClientIP:    clientIP,
		})
	}
	return r, nil
}

// executeRun composes the agent's instructions with the caller input, runs one
// real chat completion through the AI client, and returns the resulting Run —
// status "ok" with output, or "error" with the upstream failure. Pure of HTTP
// and persistence so it is directly testable; the caller records + responds.
func executeRun(ctx context.Context, ai types.AIClient, org string, a Agent, input string) Run {
	prompt := a.Instructions
	if in := strings.TrimSpace(input); in != "" {
		if prompt != "" {
			prompt += "\n\n"
		}
		prompt += in
	}
	start := time.Now()
	resp, aiErr := ai.ChatCompletion(ctx, &types.ChatRequest{Model: a.Model, Prompt: prompt})
	dur := time.Since(start).Milliseconds()
	id, _ := genID("run")
	r := Run{
		ID: id, Org: org, AgentName: a.Name, Model: a.Model, Input: input,
		DurationMs: dur, CreatedAt: time.Now().Unix(),
	}
	if aiErr != nil {
		r.Status = "error"
		r.Error = aiErr.Error()
	} else {
		r.Status = "ok"
		if resp != nil {
			r.Output = resp.Content
		}
	}
	return r
}

func (s *svc) runs(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name := nameParam(c)
	limit := 50
	if q := strings.TrimSpace(c.Query("limit")); q != "" {
		if n, err := strconv.Atoi(q); err == nil {
			limit = n
		}
	}
	runs, err := s.store.ListRuns(c.Context(), org, name, limit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "runs: %v", err)
	}
	out := make([]runView, 0, len(runs))
	for _, r := range runs {
		out = append(out, toRunView(r))
	}
	return c.JSON(http.StatusOK, map[string]any{"runs": out})
}

// metrics serves the invocations-over-time histogram for the org's Agents
// dashboard. Every point is a REAL count of recorded runs in that time bucket —
// one series line per agent that ran in the window. The Resource Usage rollup is
// all-null because this store meters no CPU/memory/storage/cost; the console
// renders those as "—" rather than a fabricated figure. No runs => empty series
// (an honest "not connected / no activity yet"), never a synthesized trend.
func (s *svc) metrics(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rng, buckets, step := metricsWindow(c.Query("range"))
	now := time.Now()
	start := now.Add(-time.Duration(buckets) * step) // last bucket ends at now
	runs, err := s.store.RunsSince(c.Context(), org, start.Unix(), 10000)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "metrics: %v", err)
	}
	// Bucket real runs per agent. counts[agent][i] = invocations in bucket i.
	counts := map[string][]int{}
	var order []string
	for _, r := range runs {
		idx := int(time.Unix(r.CreatedAt, 0).Sub(start) / step)
		if idx < 0 {
			idx = 0
		}
		if idx >= buckets {
			idx = buckets - 1
		}
		if _, seen := counts[r.AgentName]; !seen {
			counts[r.AgentName] = make([]int, buckets)
			order = append(order, r.AgentName)
		}
		counts[r.AgentName][idx]++
	}
	sort.Strings(order) // deterministic series order
	series := make([]seriesLine, 0, len(order))
	for _, name := range order {
		pts := make([]seriesPoint, buckets)
		for i := 0; i < buckets; i++ {
			pts[i] = seriesPoint{
				T: start.Add(time.Duration(i) * step).UTC().Format(time.RFC3339),
				V: counts[name][i],
			}
		}
		series = append(series, seriesLine{Key: name, Points: pts})
	}
	return c.JSON(http.StatusOK, metricsView{Range: rng, Series: series, Resource: resourceUsage{}})
}

// metricsWindow maps a console range token to (canonical token, bucket count,
// bucket width). Unknown/empty defaults to 30D. Each range yields >=4 buckets so
// the console's trendPct has real halves to compare.
func metricsWindow(raw string) (rng string, buckets int, step time.Duration) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "24H":
		return "24H", 24, time.Hour
	case "7D":
		return "7D", 7, 24 * time.Hour
	default:
		return "30D", 30, 24 * time.Hour
	}
}

// activity serves the org-wide recent-activity feed. Events are REAL: each
// recorded run is an invoked (ok) or failed (error) event; each agent's own
// create/update timestamps are created/updated events. Merged, newest first,
// capped. Nothing is invented — an org with no agents and no runs gets [].
func (s *svc) activity(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	const limit = 50
	runs, err := s.store.RunsSince(c.Context(), org, 0, 200)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "activity runs: %v", err)
	}
	rows, err := s.store.List(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "activity agents: %v", err)
	}
	evs := make([]activityView, 0, len(runs)+2*len(rows))
	for _, r := range runs {
		kind, msg := "invoked", "Invoked "+r.Model
		if r.Status == "error" {
			kind, msg = "failed", trimMsg(r.Error)
		}
		evs = append(evs, activityView{ID: r.ID, Kind: kind, Agent: r.AgentName, Message: msg, At: rfc3339(r.CreatedAt)})
	}
	for _, a := range rows {
		evs = append(evs, activityView{ID: a.ID + ":created", Kind: "created", Agent: a.Name, Message: "Agent created", At: rfc3339(a.CreatedAt)})
		if a.UpdatedAt > a.CreatedAt {
			evs = append(evs, activityView{ID: a.ID + ":updated", Kind: "updated", Agent: a.Name, Message: "Configuration updated", At: rfc3339(a.UpdatedAt)})
		}
	}
	// Newest first. rfc3339 is UTC ("Z"), so lexical order == chronological.
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].At > evs[j].At })
	if len(evs) > limit {
		evs = evs[:limit]
	}
	return c.JSON(http.StatusOK, map[string]any{"activity": evs})
}

// trimMsg bounds an error string for the activity feed without hiding it.
func trimMsg(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "Run failed"
	}
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// ---- helpers ----

func nameParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("name")) }

// tenant resolves the org — the tenant isolation KEY. It uses c.Org() EXACTLY
// as SanitizeIdentity minted it from the validated IAM owner claim (HIP-0026):
// never lowercased/stripped/truncated. Normalizing would collapse distinct
// owners into one bucket (Red HIGH-1). Reject only empty or pathologically
// long. No magic "admin" bucket — a global admin operating on per-org data
// carries an explicit org, so an empty org is a true 403.
func tenant(c *zip.Ctx) (string, bool) { return principal.Tenant(c) }

// validateLifecycle normalizes and validates the execution mode + schedule.
// Empty mode defaults to one-shot. A long-running agent MUST carry a schedule
// that parses as a 5-field cron (else the scheduler would silently never fire
// it); a one-shot agent's schedule is cleared (it is meaningless without the
// scheduler). Returns the normalized (mode, schedule) or a 400.
func validateLifecycle(mode, schedule string) (string, string, error) {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = ModeOneShot
	}
	schedule = strings.TrimSpace(schedule)
	switch mode {
	case ModeOneShot:
		return ModeOneShot, "", nil // schedule is meaningless one-shot; drop it.
	case ModeLongRunning:
		if schedule == "" {
			return "", "", zip.ErrBadRequest("a long-running agent requires a 'schedule' (5-field cron)")
		}
		if _, err := parseCron(schedule); err != nil {
			return "", "", zip.ErrBadRequest("invalid 'schedule': " + err.Error())
		}
		return ModeLongRunning, schedule, nil
	default:
		return "", "", zip.ErrBadRequest("executionMode must be 'one-shot' or 'long-running'")
	}
}

// longRunningCap resolves the per-org scheduled-agent limit from the operator
// env override, falling back to the default. A non-positive/invalid override is
// ignored so a typo can never remove the cap.
func longRunningCap() int {
	if v := strings.TrimSpace(os.Getenv(longRunningCapEnv)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return maxLongRunningPerOrg
}

// validateRef bounds an opaque lifecycle reference (compute id / service-account
// id). Returns the trimmed value or a 400 when it exceeds maxRef.
func validateRef(field, v string) (string, error) {
	v = strings.TrimSpace(v)
	if len(v) > maxRef {
		return "", zip.ErrBadRequest(field + " too long")
	}
	return v, nil
}

// billingActor is the "org/sub" identity recorded on a debit for the audit
// trail. It never selects which balance is gated — that is always the org — but
// attributes the spend to a principal. Falls back to the bare org when no
// validated user subject is present (e.g. a service-token caller).
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

func cleanList(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x == "" || len(x) > 128 || seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
		if len(out) >= 64 {
			break
		}
	}
	return out
}

func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

// Shutdown stops the scheduler (draining in-flight runs, bounded by ctx) and
// closes the agents store. Idempotent — safe to call when nothing is mounted.
func Shutdown(ctx context.Context) error {
	if mounted == nil {
		return nil
	}
	if mounted.sched != nil {
		mounted.sched.stop(ctx)
	}
	// Close the live-stream bus so every open SSE/ZAP subscriber's loop returns
	// and its handler unblocks within the shutdown deadline.
	if mounted.bus != nil {
		mounted.bus.close()
	}
	var err error
	if mounted.store != nil {
		err = mounted.store.Close()
	}
	mounted = nil
	return err
}
