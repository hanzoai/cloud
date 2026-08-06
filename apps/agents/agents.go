// Package agents is autonomous agents for your org: define them, run them, keep
// every run.
//
// An agent is a model + a system prompt (instructions) + a set of tool names;
// running one executes a real chat completion through the in-process AI client
// (the SAME gateway path the rest of the console uses) and records the run.
//
// Tenant isolation is the FILE: each org's records live in its own SQLite at
// {DataDir}/orgs/{slug}/agents.db (HIP-0302), named from the gateway-minted
// X-Org-Id (HIP-0026) and nothing else. One tenant cannot read, run or delete
// another's agents because the query never reaches the database they are in.
// tenancy.go is the whole of that argument and is the only file that resolves a
// store; the org predicate every statement still carries is what makes a
// mis-resolved store fail closed rather than answer.
//
// Surface (all org-scoped; console's AgentsModule reads {agents:[...]}):
//
//	GET    /v1/agents               list agents for the org      -> {agents:[...]}
//	POST   /v1/agents               create an agent              -> Agent
//	GET    /v1/agents/:ref          agent detail + recent runs   -> AgentDetail
//	PATCH  /v1/agents/:ref          update an agent              -> Agent
//	DELETE /v1/agents/:ref          delete an agent (+ its runs)
//	POST   /v1/agents/:ref/run      run the agent {input}        -> RunResult
//	GET    /v1/agents/:ref/runs     run history                  -> {runs:[...]}
//
// :ref is either the agent's public id (the `agent_...` handle create and list
// return) OR its org-unique name — resolved by Store.Resolve, so a created agent
// is immediately gettable and runnable by whatever create/list handed back.
//
// The stores are per-org SQLite under deps.DataDir (Base/SQLite-only), opened
// through cloud.OrgStore like every other per-org subsystem. They hold
// definitions and run I/O only — never a secret; tool credentials live in KMS by
// reference.
package agents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// agentTracer emits the per-run/per-step agent spans (shipped over ZAP to
// o11y). A run is one root span; each step nests an LLM GenAI client span.
var agentTracer = otel.Tracer("hanzo.ai/cloud/agents")

// nameRE constrains an agent's org-unique name at the create boundary — the one
// place a name is written. Path addressing (Store.Resolve, parameterized) accepts
// the name OR the `agent_...` id, so it needs no separate path validation.
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

// state is agents' own data; the shared deps (logger, brand, KMS) live in the
// embedded cloud.Base, reached as s.Log etc. bill is KEPT here on purpose: it is
// the "agent"-provider meter (the commerce attribution + spend-cap scope key),
// deliberately distinct from the subsystem's own Base.Bill, so it is NOT lifted.
type state struct {
	// stores is the per-org agents database set: one SQLite file per org under
	// {DataDir}/orgs/{slug}/agents.db, opened on first touch and cached. It
	// replaced a single fleet-wide agents.db whose one connection every org's
	// every event append queued behind. Reached ONLY through storeFor — see
	// tenancy.go for why that is the whole isolation argument.
	stores *cloud.OrgStore[*Store]
	ai     types.AIClient
	// failoverModel is the reliable model a run falls over to when the agent's own
	// model stays throttled (429/overloaded) after bounded retries
	// (deps.AIFallbackModel, default "best"). It makes an autonomous bot reply
	// still land when the throttled default flash model is overloaded. Empty
	// disables failover (retry-only). Only the run path reads it — interactive
	// chat is untouched.
	failoverModel string
	// bill is the shared per-org gate+meter (reuses deps.Metering, the ONE
	// commerce client — the same object ml/provisioning use). Nil/!Enabled()
	// makes Gate allow and Meter a no-op, so an unconfigured deployment runs
	// agents without billing rather than failing closed on a missing ledger.
	bill *cloud.ResourceMeter
	// sched is the long-running-agent scheduler; nil until started, stopped on
	// Shutdown. It shares the Service so it runs agents through the SAME runAgent path.
	sched *scheduler
	// bus is the in-process fan-out behind the live session/event stream (SSE +
	// ZAP). Set in Mount; nil-safe (a direct-construct unit test skips fan-out).
	bus *bus
	// tasks is the seam to the hanzoai/tasks durable-execution engine that control
	// commands forward to for task-backed sessions. Defaults to the disabled
	// controller (record-only) until a live tasks client is wired in Mount.
	tasks TaskController
}

var mounted *cloud.Service[state]

// Ready reports whether the session store is in THIS process, so a caller can
// tell "no sessions" from "ask the process that owns them" before it reads a
// count as a fact. It is the same question apps/projects.Ready answers for the
// site catalog, and it exists here for the same reason: agents ships as its own
// binary, so the honest answer for an in-process caller is usually "no".
func Ready() bool { return mounted != nil }

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

// agentDetail is one agent plus what only the detail read carries: the system
// prompt and its most recent runs. agentView is EMBEDDED (promoted inline on the
// wire) rather than spelled out again, because it is the shared list projection
// and a second copy of its 12 fields is a second thing to forget to update.
//
// zip's schema walk takes only EXPORTED fields, and an embedded field of an
// unexported type is not one, so the published response schema for this shape
// currently lists `instructions` and `recentRuns` alone. That is a zip gap (the
// same one that leaves the shipped PATCH /v1/agents/targets/{id} body schema
// holding only `id`), not a wire difference — encoding/json promotes the inner
// fields exactly as it always has. It is fixed once, in zip's structSchema, for
// every embedded shape in the fleet; flattening it here would trade one
// incomplete schema for a field that silently stops being sent.
type agentDetail struct {
	agentView
	Instructions string         `json:"instructions"`
	RecentRuns   []agentRunView `json:"recentRuns"`
}

type agentRunView struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Model      string `json:"model"`
	Input      string `json:"input"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"durationMs"`
	CreatedAt  string `json:"createdAt"`
}

// ---- overview shapes (console Agents dashboard: metrics + activity) ----
//
// These mirror the console's normalizers EXACTLY (console src/lib/api/agents.ts:
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

// toView projects a stored agent onto the wire. Model goes out through
// cloud.ZenModel: writes already normalize, so in steady state this changes
// nothing — it is the backstop that keeps a row written before the normalization
// existed (or restored from an old backup) from publishing an upstream name.
func toView(a Agent, runs int) agentView {
	return agentView{
		ID: a.ID, Name: a.Name, Model: cloud.ZenModel(a.Model), Description: a.Description,
		Tools: nonNil(a.Tools), Status: a.Status,
		ExecutionMode: a.ExecutionMode, Schedule: a.Schedule,
		ComputeRef: a.ComputeRef, ServiceAccountID: a.ServiceAccountID,
		Runs:      runs,
		CreatedAt: rfc3339(a.CreatedAt), UpdatedAt: rfc3339(a.UpdatedAt),
	}
}

// toRunView projects one execution onto the wire. Run history is customer-visible
// too, and a run recorded before the migration carries the model it actually ran
// on — so it is guarded the same way the agent is.
func toRunView(r Run) agentRunView {
	return agentRunView{
		ID: r.ID, Status: r.Status, Model: cloud.ZenModel(r.Model), Input: r.Input, Output: r.Output,
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
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("agents.Mount: nil app")
	}
	log := deps.Logger
	if log == nil {
		return fmt.Errorf("agents.Mount: nil deps.Logger")
	}
	log = log.New("subsystem", "agents")
	if deps.DataDir == "" {
		return fmt.Errorf("agents.Mount: empty DataDir")
	}
	// The typed-op registry lives on the App: it is what makes each op a document
	// operation, an MCP tool, a CLI command and an SDK method rather than only a
	// route. A Router that cannot reach it must fail the mount rather than serve
	// routes no projection knows about.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("agents.Mount: router carries no typed-op registry")
	}
	// deps.AI may be nil when no gateway is configured; run() degrades honestly.
	// agents is a "complex" mount (package-global `mounted`, a background scheduler,
	// a shutdown teardown), so it builds the Service value directly.
	b := cloud.NewBase(deps, "agents")
	s := &cloud.Service[state]{
		Base: b,
		State: state{
			stores:        cloud.NewOrgStore[*Store](b, "agents", openStore),
			ai:            deps.AI,
			failoverModel: cloud.FallbackModel,
			bill:          cloud.NewResourceMeter(deps, meterKind),
			bus:           newBus(),
			// TASKS PLUG-IN POINT: durable execution rides hanzoai/tasks, not a
			// bespoke engine. Default is record-only; wiring client.Dial(TASKS_URL)
			// from github.com/hanzoai/tasks/pkg/sdk/client here makes control forward
			// to the engine's Signal/Cancel API (see sessions_tasks.go).
			tasks: disabledTaskController{},
		},
	}
	// Split a pre-existing fleet-wide agents database into per-org files BEFORE a
	// route exists to read them, and fail the mount if it cannot be done: an empty
	// registry served over live rows is the one outcome worse than not booting.
	if err := fanOutLegacy(context.Background(), deps.DataDir, &s.State); err != nil {
		_ = s.State.stores.CloseAll()
		return fmt.Errorf("agents.Mount: %w", err)
	}
	mounted = s
	// The login-manager teardown, for the link process that has no session store
	// in it — two doors onto the ONE StopSessions (sessions_rpc.go).
	exposeSessions()
	exposeRunOnBehalf()

	o := agentOps{s: s}
	// Bridge FIRST, and at the door this SUBSYSTEM is, not on one node inside it: a
	// typed op receives only a context, so the validated org reaches it by being
	// parked there — never as an In field, which is caller-supplied and would be a
	// cross-tenant read the caller asserted for itself.
	//
	// IT IS INSTALLED ON THE ROUTER, NOT ON THE /v1/agents GROUP, because this
	// surface is not composed under that group. A group's middleware wraps the
	// routes in its OWN subtree, and three quarters of this surface is registered
	// somewhere else: the collection root and the two sub-planes go on the Router by
	// absolute path (zip.Get(zapp, "/v1/agents"), mountSessions(s, app),
	// mountTargets(s, app)) and only /metrics, /activity and the :ref leaves are
	// composed beneath g. So a Bridge on g parked no org for /v1/agents/targets or
	// /v1/agents/sessions, and every op there answered 403 "X-Org-Id required" to a
	// request that carried one. Serve installs one app-wide, which is why serving
	// was unaffected and only the tests — which Mount onto a bare app — could see
	// it; a gate whose absence just one door down is invisible in production is the
	g := app.Group("/v1/agents")
	// cloud.Bridge parks the validated org on the context a typed op receives; it
	// is the composer's install — once at the root of every program — so this
	// package does not install its own.
	//
	// The root of the surface. Declared on the App with its WHOLE path, not on the
	// group with an empty leaf: joining "/v1/agents" with "" yields "/v1/agents/",
	// a different path from the one these two have always served.
	zip.Get(zapp, "/v1/agents", o.list)
	zip.Post(zapp, "/v1/agents", o.create, zip.WithStatus(http.StatusCreated))
	// The static org-wide surfaces are listed before the :ref wildcard for reading
	// order, not for matching: the router resolves by SPECIFICITY, so a literal
	// beats a param whatever order they register in ("metrics" is never captured as
	// a ref). Registration order decides nothing here — it only decides which
	// handler silently wins when two patterns are byte-identical, which is a
	// collision, not a precedence.
	zip.Get(g, "/metrics", o.metrics)
	zip.Get(g, "/activity", o.activity)
	// Live agent-session control plane: /v1/agents/sessions[/...].
	mountSessions(s, app)
	// Agent targets: /v1/agents/targets[/...] — the #48 dispatch destinations a
	// session runs on.
	mountTargets(s, app)
	zip.Get(g, "/:ref", o.get)
	zip.Patch(g, "/:ref", o.update)
	zip.Delete(g, "/:ref", o.del)
	// UNTYPED, and it has to be: a run answers 502 with the RECORDED RUN as its
	// body (the execution happened and its error is the product), and a balance
	// denial answers the fleet-wide 402/503 contract through cloud.DenyResource
	// ({"error":{"code","message"}}). zip's error type carries {status,code,error}
	// and nothing else, so a typed op would silently reshape both. It is the
	// conditional-BODY twin of the conditional-status class — see LLM.md.
	g.Post("/:ref/run", cloud.Handle(s, run))
	zip.Get(g, "/:ref/runs", o.runs)

	// Long-running scheduler: invokes each long-running agent's run on its cron
	// cadence through the SAME runAgent path as the HTTP handler (one run path,
	// one gate, one meter). Only started when inference is wired — with no AI a
	// scheduled run could never execute, so there is nothing to schedule.
	if s.State.ai != nil {
		s.State.sched = newScheduler(s, log)
		s.State.sched.start()
	}

	// Register agents into the unified tool plane (SourceAgent): an agent is callable
	// as a tool via RunOnBehalf, activation-gated by the plane.
	tools.Register(agentToolProvider{})

	// Close the deploy seam clients/projects left open: a site going live becomes
	// the last turn of the session that built it, so the story a visitor reads
	// ends where the product starts (provenance.go).
	mountProvenance(s)

	log.Info("agents mounted", "ai", s.State.ai != nil, "billing", s.State.bill.Enabled(),
		"scheduler", s.State.sched != nil, "brand", deps.Brand)
	return nil
}

// ---- handlers ----

// agentOps binds the service to the typed agent ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value (o.list), which is also
// the only bound form cmd/zipdoc can lift prose from.
type agentOps struct{ s *cloud.Service[state] }

// agentRef addresses one agent. The ref is the path segment, and the URL is the
// addressing authority, so it binds from there whatever a body says.
type agentRef struct {
	// Ref is the agent's public id (the agent_… handle create and list return) or
	// its org-unique name, from the path. Either resolves the same agent.
	Ref string `json:"ref"`
}

// agentList is every agent defined in the caller's org.
type agentList struct {
	// Agents is the org's agents, each carrying its recorded run count.
	Agents []agentView `json:"agents"`
}

// runsQuery addresses one agent's run history.
type runsQuery struct {
	// Ref is the agent's public id or its org-unique name, from the path.
	Ref string `json:"ref"`
	// Limit caps how many runs come back, newest first. Absent, zero or out of
	// range (1..200) reads as 50.
	Limit int `json:"limit"`
}

// runList is a page of one agent's recorded executions.
type runList struct {
	// Runs is the agent's executions, newest first.
	Runs []agentRunView `json:"runs"`
}

// metricsQuery selects the dashboard window.
type metricsQuery struct {
	// Range is the window to bucket: 24H, 7D or 30D. Anything else reads as 30D.
	Range string `json:"range"`
}

// activityFeed is the org-wide recent-activity feed.
type activityFeed struct {
	// Activity is the merged run/create/update events, newest first, capped at 50.
	Activity []activityView `json:"activity"`
}

type createAgentIn struct {
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

// CreateAgent defines an agent in the caller's org: a model, a system prompt
// (instructions) and a set of tool names. The name must be unique in the org and
// match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$. An omitted model takes the
// deployment's configured default; a named one is checked against the gateway's
// served catalog, so a model this deployment never serves is refused here rather
// than failing at run time. A long-running agent must carry a 5-field cron
// schedule (the scheduler would otherwise never fire it) and counts against a
// per-org cap on scheduled agents.
//
// Example: {"name": "helper", "model": "enso-flash", "instructions": "be terse"}
func (o agentOps) create(ctx context.Context, in *createAgentIn) (*agentView, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	body := *in
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	if !nameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("name must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	// Model resolution: a client-supplied model is validated against the
	// gateway's served catalog (a clean 400 for e.g. claude-sonnet-4-5 that this
	// gateway never serves, instead of a confusing run-time 502). An OMITTED
	// model falls back to the deployment default (a valid catalog model the
	// operator configured) — trusted, not re-validated — so a bot launched
	// without a model still runs. If neither is present the model is required.
	//
	// Then cloud.ZenModel normalizes: an upstream family name never enters the
	// registry, so it can never be served back out of one. The registry stores
	// exactly what we would show, which keeps the read guard in toView a no-op
	// rather than a lie about what the agent runs on.
	model := strings.TrimSpace(body.Model)
	if model == "" {
		model = cloud.DefaultModel
	} else if err := validateModel(s, ctx, model); err != nil {
		return nil, err
	}
	model = cloud.ZenModel(model)
	if len(body.Instructions) > maxInstructions {
		return nil, zip.ErrBadRequest("instructions too large")
	}
	mode, schedule, err := validateLifecycle(body.ExecutionMode, body.Schedule)
	if err != nil {
		return nil, err
	}
	computeRef, err := validateRef("computeRef", body.ComputeRef)
	if err != nil {
		return nil, err
	}
	serviceAccountID, err := validateRef("serviceAccountId", body.ServiceAccountID)
	if err != nil {
		return nil, err
	}
	// Cap the org's scheduler footprint (Red LOW-1): a tenant cannot create an
	// unbounded number of scheduled agents that each add recurring load to the
	// shared store. Only counts when this create is itself long-running.
	if mode == ModeLongRunning {
		n, err := sto.CountLongRunning(ctx, org)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "count: %v", err)
		}
		if n >= longRunningCap() {
			return nil, zip.Errorf(http.StatusConflict,
				"long-running agent limit reached for this org (max %d)", longRunningCap())
		}
	}
	id, err := genID("agent")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	a := Agent{
		ID: id, Org: org, Name: name, Model: model, Instructions: body.Instructions,
		Description: strings.TrimSpace(body.Description), Tools: cleanList(body.Tools),
		Status: "ready", ExecutionMode: mode, Schedule: schedule,
		ComputeRef: computeRef, ServiceAccountID: serviceAccountID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := sto.Create(ctx, a); err != nil {
		if err == errConflict {
			return nil, zip.ErrConflict("agent already exists in this org")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	v := toView(a, 0)
	return &v, nil
}

// ListAgents returns every agent defined in the caller's org, each with the
// number of runs recorded against it.
func (o agentOps) list(ctx context.Context, _ *noInput) (*agentList, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	rows, err := sto.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]agentView, 0, len(rows))
	for _, a := range rows {
		n, err := sto.CountRuns(ctx, org, a.Name)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "runs: %v", err)
		}
		out = append(out, toView(a, n))
	}
	return &agentList{Agents: out}, nil
}

// GetAgent returns one agent with its system prompt and its 20 most recent runs.
// The ref is the agent's public id or its org-unique name — a created agent is
// immediately gettable by whatever create handed back.
//
// Example: {"ref": "helper"}
func (o agentOps) get(ctx context.Context, in *agentRef) (*agentDetail, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	a, err := sto.Resolve(ctx, org, strings.TrimSpace(in.Ref))
	if err == errNotFound {
		return nil, zip.ErrNotFound("agent not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	runs, err := sto.ListRuns(ctx, org, a.Name, 20)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "runs: %v", err)
	}
	rv := make([]agentRunView, 0, len(runs))
	for _, r := range runs {
		rv = append(rv, toRunView(r))
	}
	return &agentDetail{
		agentView: toView(a, len(runs)), Instructions: a.Instructions, RecentRuns: rv,
	}, nil
}

// updateAgentIn is a partial update. Every field is optional — a field the
// request omits is left alone — and the agent is addressed by the path.
//
// The mutable fields are spelled out HERE rather than in an embedded body struct
// that has exactly one user: zip's schema walk skips an embedded field of an
// unexported type, so an embedded body would publish a request schema holding
// only `ref` and every generated client would be unable to send anything.
type updateAgentIn struct {
	// Ref is the agent to update — its public id or org-unique name, from the path.
	Ref              string    `json:"ref"`
	Model            *string   `json:"model"`
	Instructions     *string   `json:"instructions"`
	Description      *string   `json:"description"`
	Tools            *[]string `json:"tools"`
	ExecutionMode    *string   `json:"executionMode"`
	Schedule         *string   `json:"schedule"`
	ComputeRef       *string   `json:"computeRef"`
	ServiceAccountID *string   `json:"serviceAccountId"`
}

// UpdateAgent changes an agent in place. Every field is optional; a field the
// request omits keeps its stored value. The resulting mode+schedule are
// re-validated together, so a partial update can never leave a long-running
// agent without the cron the scheduler needs to fire it, and a transition INTO
// long-running counts against the per-org cap on scheduled agents.
//
// Example: {"ref": "helper", "instructions": "be terse and cite sources"}
func (o agentOps) update(ctx context.Context, in *updateAgentIn) (*agentView, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	a, err := sto.Resolve(ctx, org, strings.TrimSpace(in.Ref))
	if err == errNotFound {
		return nil, zip.ErrNotFound("agent not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	body := *in
	if body.Model != nil {
		m := strings.TrimSpace(*body.Model)
		if m == "" {
			return nil, zip.ErrBadRequest("model cannot be empty")
		}
		if err := validateModel(s, ctx, m); err != nil {
			return nil, err
		}
		a.Model = cloud.ZenModel(m)
	}
	if body.Instructions != nil {
		if len(*body.Instructions) > maxInstructions {
			return nil, zip.ErrBadRequest("instructions too large")
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
			return nil, err
		}
	}
	if body.ServiceAccountID != nil {
		if a.ServiceAccountID, err = validateRef("serviceAccountId", *body.ServiceAccountID); err != nil {
			return nil, err
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
		return nil, err
	}
	// Enforce the per-org scheduler cap on a TRANSITION into long-running, so a
	// tenant can't sidestep the create-time cap by making N one-shot agents and
	// PATCHing them to long-running (Red LOW-1 follow-up). Only counts when the
	// agent was NOT already long-running (a no-op re-save of an existing
	// long-running agent must not 409 against its own row).
	if a.ExecutionMode == ModeLongRunning && !wasLongRunning {
		n, cerr := sto.CountLongRunning(ctx, org)
		if cerr != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "count: %v", cerr)
		}
		if n >= longRunningCap() {
			return nil, zip.Errorf(http.StatusConflict,
				"long-running agent limit reached for this org (max %d)", longRunningCap())
		}
	}
	a.UpdatedAt = time.Now().Unix()
	if err := sto.Update(ctx, a); err != nil {
		if err == errNotFound {
			return nil, zip.ErrNotFound("agent not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	n, _ := sto.CountRuns(ctx, org, a.Name)
	v := toView(a, n)
	return &v, nil
}

// DeleteAgent removes an agent and every run recorded against it. Answers 204.
//
// Example: {"ref": "helper"}
func (o agentOps) del(ctx context.Context, in *agentRef) (*noContent, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	// Resolve id-or-name first, then delete by the canonical name (agent_runs
	// cascades on agent_name). Deleting by a raw id would never match the store's
	// name key and silently 404 a real agent.
	a, err := sto.Resolve(ctx, org, strings.TrimSpace(in.Ref))
	if err == errNotFound {
		return nil, zip.ErrNotFound("agent not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "resolve: %v", err)
	}
	deleted, err := sto.Delete(ctx, org, a.Name)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("agent not found")
	}
	return nil, nil
}

type runReq struct {
	// Input is the caller's message for this run, composed with the agent's stored
	// instructions as the system prompt. Capped at 128 KiB.
	Input string `json:"input"`
}

// The run's prose, declared beside the wire fact that keeps it untyped (the
// registration above says why it cannot be a typed op: a 502 answers with the
// RECORDED RUN as its body and a balance denial answers the fleet-wide
// cloud.DenyResource envelope, and zip's error type can express neither). zipdoc
// lifts a typed op's prose from its doc comment; there is no typed op here, so
// without this the one operation that spends money publishes an operationId and
// nothing else.
func init() {
	openapi.Describe("/v1/agents/:ref/run", http.MethodPost,
		"Run one of your org's agents and get the recorded run back.",
		"Composes the agent's stored instructions with the caller's `input`, executes one real "+
			"chat completion through the same in-process AI client the rest of the console uses, "+
			"and answers with the run that was recorded: its id, status, model, output, duration "+
			"and error. Every run this returns reflects an execution that actually happened — a "+
			"model failure is recorded and reported, never hidden and never fabricated. A "+
			"transient upstream failure (429, 5xx, empty choices) is retried up to three times "+
			"with jittered backoff, and a configured failover model is tried before the run is "+
			"called an error.\n\n"+
			"`ref` is the agent's public `agent_…` id or its org-unique name; either resolves the "+
			"same agent, and it must belong to the caller's org, so an agent in another tenant is "+
			"a 404 exactly like one that does not exist. A validated principal is required and "+
			"the check is made twice on purpose: this route MOVES MONEY, so the debit's principal "+
			"requirement is asserted where the money moves rather than inherited from the tenant "+
			"lookup.\n\n"+
			"The org's balance is authorized BEFORE any inference, so an unfunded tenant gets 402 "+
			"and no free compute, and a billing plane that cannot answer gets 503 rather than a "+
			"free run. The flat per-run fee is an operator knob; setting it to zero makes runs "+
			"free and removes the balance gate with them. Only a SUCCESSFUL run is billed, "+
			"attributed to the model actually used — a failover run bills the model it fell over "+
			"to, not the one it started on. A deployment with no inference wired answers 503 "+
			"before any of this.\n\n"+
			"THE RULE A READER GETS WRONG: a failed run is a 502 whose body is the RUN, not an "+
			"error envelope. The execution happened, the run was persisted to this agent's "+
			"history, and its `error` field is the product — so a client that treats every "+
			"non-2xx as an opaque failure throws away the only account of what went wrong. Each "+
			"run also opens a root session in the live session registry, best-effort: a "+
			"bookkeeping failure there never fails the run, because the run and its billing "+
			"already happened.")
}

// run executes the agent: it composes the agent's instructions with the caller
// input and runs a real chat completion via the in-process AI client, then
// records the run. Every returned run reflects an execution that actually
// happened — an inference failure is recorded and returned as an error run, not
// hidden and not fabricated.
func run(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	// tenant() above already required a VALIDATED principal (principal.Org
	// returns ok only when c.User() — set solely from a JWT SanitizeIdentity
	// verified — is non-empty), so every path here, run included, is closed to the
	// no-bearer direct-to-pod forge path. This explicit re-assertion is a local,
	// money-path invariant: a run MOVES MONEY (debits the org's commerce ledger),
	// so the debit's principal requirement is stated where the money moves and
	// never silently depends on tenant()'s internals (Red MEDIUM-2). Same guard the
	// s3 / provisioning subsystems use.
	if strings.TrimSpace(c.User()) == "" {
		return zip.ErrForbidden("a validated principal is required to run an agent")
	}
	sto, err := s.State.storeFor(org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "store: %v", err)
	}
	a, err := sto.Resolve(c.Context(), org, refParam(c))
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
	if s.State.ai == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "inference is not configured on this deployment")
	}

	// Pre-authorize the caller's org balance BEFORE any inference (fail-closed).
	// The actor is the validated principal (org/sub) when present, else the bare
	// org — recorded on the debit for attribution. Gating here means an unfunded
	// org gets 402 and NO free inference; an unreachable commerce gets 503.
	actor := billingActor(org, c.User())
	r, gateErr := runAgent(s, c.Context(), a, body.Input, actor, c.RequestID(), cloud.ClientIP(c))
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
func runAgent(s *cloud.Service[state], ctx context.Context, a Agent, input, actor, requestID, clientIP string) (Run, error) {
	// Root span per run — the whole trace (balance gate → step → LLM call)
	// nests under it, shipped over ZAP to o11y.
	ctx, span := agentTracer.Start(ctx, "agent.run "+a.Name, trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()
	span.SetAttributes(
		attribute.String("hanzo.agent.name", a.Name),
		attribute.String("hanzo.agent.org", a.Org),
		attribute.String("gen_ai.request.model", a.Model),
	)

	fee := cloud.ResourceFeeCents(agentFeeEnvPrefix, meterKind)
	// Gate the AGENT's own org — never a caller default, never another tenant.
	// fee<=0 or unconfigured billing makes this a no-op (allows). Background run
	// path: no request principal, so the project axis is empty + unvalidated (soft).
	if err := s.State.bill.Gate(ctx, a.Org, "", false, meterKind, fee); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "balance gate denied")
		return Run{}, err
	}

	r := executeRun(ctx, s.State.ai, a.Org, actor, a, input, s.State.failoverModel)
	span.SetAttributes(
		attribute.String("hanzo.agent.run_id", r.ID),
		attribute.String("hanzo.agent.run_status", r.Status),
		attribute.Int64("hanzo.agent.duration_ms", r.DurationMs),
		attribute.String("gen_ai.response.model", r.Model),
	)
	if r.Status == "error" {
		span.SetStatus(codes.Error, r.Error)
	}
	// The agent's own org names the file its run history lands in — the run has
	// already been gated and executed under it, so this is bookkeeping, not a
	// second authorization.
	if sto, serr := s.State.storeFor(a.Org); serr != nil {
		s.Log.Warn("record run failed", "org", a.Org, "agent", a.Name, "err", serr)
	} else if err := sto.InsertRun(ctx, r); err != nil {
		s.Log.Warn("record run failed", "org", a.Org, "agent", a.Name, "err", err)
	}

	// Make the run visible in the live session registry as a ROOT session (the
	// same registry the @hanzo/dev outer-agent + subagent flows use). Best-effort:
	// it NEVER fails the run — the run and its billing already happened. DRY: this
	// is the ONE run path (HTTP + scheduler), so every run becomes a session here.
	openRunSession(s, ctx, a, r, actor)

	// Bill only a successful run (mirrors the edge gate: failed work is not
	// charged). Rich attribution: product=agent (Provider), the model ACTUALLY
	// used (r.Model — a failover run bills the reliable model it fell over to, not
	// the throttled one it started on), and the actor for the audit trail.
	// Fire-and-forget on a background context.
	if r.Status == "ok" {
		s.State.bill.MeterUsage(a.Org, meterKind, metering.Usage{
			AmountCents: fee,
			Model:       r.Model,
			Actor:       actor,
			RequestID:   requestID,
			ClientIP:    clientIP,
		})
	}
	return r, nil
}

// maxAttempts bounds retries of a SINGLE model's completion on a transient
// upstream failure (429 / 5xx / empty-choices / "Platform overloaded"). Retrying
// a completion is side-effect-free — nothing bills until it succeeds — so a
// bounded retry with jittered backoff turns an intermittent gateway 429 into a
// delivered reply instead of a dropped one.
const maxAttempts = 3

// retryBaseDelay / retryMaxDelay bound the exponential, equal-jittered backoff
// between attempts. Small by design: a gateway overload clears in well under a
// second, and a run must not stall a bot conversation.
const (
	retryBaseDelay = 150 * time.Millisecond
	retryMaxDelay  = 2 * time.Second
)

// executeRun composes the agent's instructions with the caller input and runs
// the agent — with a bounded retry on transient upstream overload and, if the
// agent's own model stays throttled, ONE failover to the deployment's reliable
// model (fallback) so an autonomous bot reply still lands. It returns the
// resulting Run — status "ok" with output and Model set to the model that
// ACTUALLY answered (so metering bills that model), or "error" with the final
// upstream failure. Pure of HTTP and persistence so it is directly testable; the
// caller records + responds. This reliability policy is the agent runner's ALONE
// — the interactive user-facing chat path is untouched.
//
// An agent that declares TOOLS and whose tools the plane actually offers runs the
// bounded tool loop instead of a single completion (tools.go). One with none —
// or one whose declared names resolve to nothing — takes the single completion
// this has always been, unchanged.
//
// actor is the run's billing identity (billingActor's "org/sub"), threaded so a
// tool dispatch runs as the principal the run is charged to.
func executeRun(ctx context.Context, ai types.AIClient, org, actor string, a Agent, input, fallback string) Run {
	// Child step span; the AI client opens its own GenAI span nested under this.
	ctx, span := agentTracer.Start(ctx, "agent.step", trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()
	span.SetAttributes(attribute.String("gen_ai.request.model", a.Model))

	prompt := a.Instructions
	if in := strings.TrimSpace(input); in != "" {
		if prompt != "" {
			prompt += "\n\n"
		}
		prompt += in
	}
	start := time.Now()
	var (
		resp  *types.ChatResponse
		used  string
		aiErr error
	)
	// An agent nested at the depth limit is offered nothing and has to answer for
	// itself — the one thing that stops a cycle of agents-as-tools, since each
	// level would otherwise start its round cap over (tools.go).
	var offer []string
	if agentDepth(ctx) < maxAgentDepth {
		offer = callableTools(a)
	}
	defs := runTools.catalog(ctx, org, actor, offer)
	// BOTH numbers, always. An agent that declares tools and is offered none is
	// the exact shape of the split-fleet gap tools.go describes, and it is only
	// diagnosable if the span says "declared 3, offered 0" rather than staying
	// silent about a run that quietly had no hands.
	span.SetAttributes(
		attribute.Int("hanzo.agent.tools_declared", len(a.Tools)),
		attribute.Int("hanzo.agent.tools", len(defs)),
	)
	if len(defs) > 0 {
		resp, used, aiErr = completeWithTools(ctx, ai, org, actor, prompt, a.Model, fallback, defs)
	} else {
		resp, used, aiErr = completeWithFailover(ctx, ai,
			&types.ChatRequest{Model: a.Model, Org: org, Prompt: prompt}, fallback)
	}
	dur := time.Since(start).Milliseconds()
	id, _ := genID("run")
	r := Run{
		ID: id, Org: org, AgentName: a.Name, Model: used, Input: input,
		DurationMs: dur, CreatedAt: time.Now().Unix(),
	}
	if aiErr != nil {
		span.RecordError(aiErr)
		span.SetStatus(codes.Error, "agent step failed")
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

// completeWithFailover runs one completion on req's own model with a bounded
// retry (completeWithRetry), then — only if that model is STILL throttled after
// its retries — fails over ONCE to fallback, a reliable model. It returns the
// response, the model that actually produced it (for honest metering), and the
// final error. A non-transient failure on either model returns immediately (the
// next model would fail identically). ONE ordered mechanism, no config sprawl.
//
// It takes the whole request rather than a prompt string because a tool round IS
// the request: the transcript so far and the tools on offer are part of what is
// being retried, and a helper that only knew a prompt would have to grow a second
// copy of this policy for the loop to reuse (tools.go). req.Model is set per
// attempt; everything else is the caller's.
func completeWithFailover(ctx context.Context, ai types.AIClient, req *types.ChatRequest, fallback string) (*types.ChatResponse, string, error) {
	model := req.Model
	models := []string{model}
	if f := strings.TrimSpace(fallback); f != "" && f != model {
		models = append(models, f)
	}
	var lastErr error
	for _, m := range models {
		req.Model = m
		resp, err := completeWithRetry(ctx, ai, req)
		if err == nil {
			return resp, m, nil
		}
		lastErr = err
		// Escalate to the next model ONLY on a transient overload; a hard error
		// (bad request, auth, unserved model) fails fast — failover cannot help.
		if !errors.Is(err, types.ErrUpstreamBusy) {
			return nil, m, err
		}
	}
	return nil, models[len(models)-1], lastErr
}

// completeWithRetry calls the completion up to maxAttempts times, retrying ONLY a
// transient upstream overload (types.ErrUpstreamBusy) with jittered backoff and
// respecting context cancellation. A non-transient error returns immediately.
func completeWithRetry(ctx context.Context, ai types.AIClient, req *types.ChatRequest) (*types.ChatResponse, error) {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, err := ai.ChatCompletion(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !errors.Is(err, types.ErrUpstreamBusy) {
			return nil, err // permanent — do not burn retries repeating it
		}
		if attempt == maxAttempts-1 {
			break
		}
		if err := sleepBackoff(ctx, attempt); err != nil {
			return nil, err // context cancelled/expired mid-backoff
		}
	}
	return nil, lastErr
}

// sleepBackoff waits an exponential, equal-jittered delay before the next
// attempt, or returns the context error if the caller's deadline fires first.
func sleepBackoff(ctx context.Context, attempt int) error {
	d := retryBaseDelay << attempt
	if d > retryMaxDelay {
		d = retryMaxDelay
	}
	// Equal jitter: half fixed, half random in [0, d/2) — spreads retries without
	// ever collapsing the delay to ~0 (guarantees forward progress under load).
	wait := d/2 + time.Duration(mrand.Int64N(int64(d/2)+1))
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ListAgentRuns returns one agent's execution history, newest first — each run's
// input, its output or its error, and how long it took. Every row is a run that
// actually happened.
//
// Example: {"ref": "helper", "limit": 20}
func (o agentOps) runs(ctx context.Context, in *runsQuery) (*runList, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	a, err := sto.Resolve(ctx, org, strings.TrimSpace(in.Ref))
	if err == errNotFound {
		return nil, zip.ErrNotFound("agent not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "resolve: %v", err)
	}
	// ListRuns owns the page bound: it reads 0 (absent) and anything outside
	// 1..200 as its own 50, which is exactly what an unparseable ?limit= produced
	// before — the binder leaves the field at zero for the same input.
	runs, err := sto.ListRuns(ctx, org, a.Name, in.Limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "runs: %v", err)
	}
	out := make([]agentRunView, 0, len(runs))
	for _, r := range runs {
		out = append(out, toRunView(r))
	}
	return &runList{Runs: out}, nil
}

// AgentMetrics serves the invocations-over-time histogram for the org's Agents
// dashboard. Every point is a REAL count of recorded runs in that time bucket —
// one series line per agent that ran in the window. The Resource Usage rollup is
// all-null because this store meters no CPU/memory/storage/cost; the console
// renders those as "—" rather than a fabricated figure. No runs => empty series
// (an honest "not connected / no activity yet"), never a synthesized trend.
//
// Example: {"range": "7D"}
func (o agentOps) metrics(ctx context.Context, in *metricsQuery) (*metricsView, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	rng, buckets, step := metricsWindow(in.Range)
	now := time.Now()
	start := now.Add(-time.Duration(buckets) * step) // last bucket ends at now
	runs, err := sto.RunsSince(ctx, org, start.Unix(), 10000)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "metrics: %v", err)
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
	return &metricsView{Range: rng, Series: series, Resource: resourceUsage{}}, nil
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

// AgentActivity serves the org-wide recent-activity feed. Events are REAL: each
// recorded run is an invoked (ok) or failed (error) event; each agent's own
// create/update timestamps are created/updated events. Merged, newest first,
// capped. Nothing is invented — an org with no agents and no runs gets [].
func (o agentOps) activity(ctx context.Context, _ *noInput) (*activityFeed, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	const limit = 50
	runs, err := sto.RunsSince(ctx, org, 0, 200)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "activity runs: %v", err)
	}
	rows, err := sto.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "activity agents: %v", err)
	}
	evs := make([]activityView, 0, len(runs)+2*len(rows))
	for _, r := range runs {
		kind, msg := "invoked", "Invoked "+cloud.ZenModel(r.Model)
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
	return &activityFeed{Activity: evs}, nil
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

// refParam is the URL path segment addressing an agent: its public id or its
// org-unique name. Store.Resolve accepts either — see the package doc.
func refParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("ref")) }

// tenant resolves the org — the tenant isolation KEY. It uses c.Org() EXACTLY
// as SanitizeIdentity minted it from the validated IAM owner claim (HIP-0026):
// never lowercased/stripped/truncated. Normalizing would collapse distinct
// owners into one bucket (Red HIGH-1). Reject only empty or pathologically
// long. No magic "admin" bucket — a SuperAdmin operating on per-org data
// carries an explicit org, so an empty org is a true 403.
func tenant(c *zip.Ctx) (string, bool) { return principal.Org(c) }

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

// validateModel rejects a client-supplied model that is NOT in this gateway's
// served catalog, turning a would-be run-time 502 (the gateway rejecting a model
// it never served) into a clean create/update-time 400 with the real reason. It
// is a best-effort UX guard, NOT a security boundary: it runs only when the AI
// client can enumerate the catalog (the real gateway client implements
// types.ModelLister) AND the catalog comes back non-empty; a disabled/RPC client
// or an unreachable/empty catalog skips the check (fail-open), so validation
// infrastructure never blocks a create. An empty model is the caller's cue to
// take the deployment default and is never routed here.
func validateModel(s *cloud.Service[state], ctx context.Context, model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	lister, ok := s.State.ai.(types.ModelLister)
	if !ok {
		return nil // this AI client cannot enumerate models — cannot validate
	}
	ids, err := lister.Models(ctx)
	if err != nil || len(ids) == 0 {
		return nil // catalog unreachable/empty — fail-open, never block on infra
	}
	for _, id := range ids {
		if id == model {
			return nil
		}
	}
	return zip.ErrBadRequest(fmt.Sprintf("model %q is not in this gateway's catalog", model))
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

// BillingActor is the exported form of the actor identity a session is recorded
// under. The login-manager adapter (the only external caller) uses it to scope a
// session stop/count to the REVOKING user's own actor, so a revoke can never reach a
// co-tenant's sessions. It mirrors what sessions.go stamps on Session.Actor, so a
// stop's actor predicate matches exactly the sessions that user created.
func BillingActor(org, sub string) string { return billingActor(org, sub) }

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
	if mounted.State.sched != nil {
		mounted.State.sched.stop(ctx)
	}
	// Close the live-stream bus so every open SSE/ZAP subscriber's loop returns
	// and its handler unblocks within the shutdown deadline.
	if mounted.State.bus != nil {
		mounted.State.bus.close()
	}
	var err error
	if mounted.State.stores != nil {
		err = mounted.State.stores.CloseAll()
	}
	mounted = nil
	return err
}
