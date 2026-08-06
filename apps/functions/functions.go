// Package functions is your serverless code: publish it, call it over HTTP,
// watch every run and what it cost.
//
// The /v1/functions registry is per-org — one function, one owner. Every
// function belongs to exactly one org (the
// gateway-minted X-Org-Id, HIP-0026); org isolation is the org column,
// enforced on every query. The registry stores a function's runtime, source,
// resource limits, and the NAMES of the secrets it mounts — never a secret
// value (values live in KMS by reference, the Secret-Manager principle).
//
// Surface (the shape console's FunctionsModule / functions.ts consume):
//
//	GET    /v1/functions                    list functions           -> {functions:[...]}
//	POST   /v1/functions                    create / redeploy        -> ServerlessFunction
//	GET    /v1/functions/metrics            invocations chart + donut -> {series,status,costCents}
//	GET    /v1/functions/triggers           all triggers (HTTP)      -> {triggers:[...]}
//	GET    /v1/functions/deployments        current deployments      -> {functions:[...]}
//	GET    /v1/functions/secrets            mounted secret NAMES      -> {secrets:[...]}
//	GET    /v1/functions/:name              detail + triggers + calls -> FunctionDetail
//	DELETE /v1/functions/:name              delete (+ its invocations)
//	GET    /v1/functions/:name/invocations  recent invocations       -> {invocations:[...]}
//	GET    /v1/functions/:name/logs         last invocation output   -> {logs:"..."}
//	POST   /v1/functions/:name/invoke       run the function {input} -> Invocation
//
// Invoke delegates to a SANDBOX through apps/exec — this
// binary NEVER runs org code in-process. When the sandbox is not configured
// invoke fails closed (503) and fabricates nothing. Every metric the Overview
// shows is DERIVED from real invocation rows; there is no invented rollup.
package functions

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var reserved = map[string]bool{"metrics": true, "triggers": true, "deployments": true, "secrets": true}

// runtimes is the closed set of function runtimes the registry accepts. It maps
// to the sandbox executor's language identifiers; "container" means BYO image.
var runtimes = map[string]bool{
	"node": true, "python": true, "go": true, "deno": true, "bash": true, "container": true,
}

const (
	maxCode  = 256 * 1024
	window7d = 7 * 24 * 60 * 60
)

// invokeFeeEnvPrefix is the operator knob for the per-invocation compute fee.
// The effective fee is cloud.ResourceFeeCents(invokeFeeEnvPrefix, "invoke"): the
// global CLOUD_FUNCTION_FEE_CENTS override, else the $1.00 default. Set it to 0
// to make invocations free (and therefore un-gated), mirroring the edge gate's
// price==0 short-circuit. A serverless invocation runs real sandbox compute, so
// it is billed the SAME way provisioning bills a create and ml bills a submit —
// via the ONE shared cloud.ResourceMeter (product "functions"); there is no
// second metering path.
const invokeFeeEnvPrefix = "CLOUD_FUNCTION_FEE_CENTS"

// state is functions's own data; shared deps (logger, billing meter) live in the
// embedded cloud.Base, reached as s.Log / s.Bill.
type state struct {
	stores *cloud.OrgStore[*Store] // per-org functions DBs, opened once each
	exec   *execClient
}

var mounted *cloud.Service[state]

// storeFor is the ONE way this package reaches a store: it names the database
// through cloud.OrgNamespace — the single door a validated org walks through —
// and asks the registry for that name. Nothing else here resolves a store, so
// "which file does this request touch" has one answer from one input.
//
// org MUST already be validated: principal.Org for a request, or the caller's
// own server-side resolution for an in-process seam.
func storeFor(s *cloud.Service[state], org string) (*Store, error) {
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	return s.State.stores.For(ns)
}

// ---- HTTP response shapes (console functions.ts contract) ----

type functionView struct {
	Name           string   `json:"name"`
	Namespace      string   `json:"namespace"`
	Environment    string   `json:"environment"`
	Status         string   `json:"status"`
	Image          string   `json:"image,omitempty"`
	Endpoint       string   `json:"endpoint"`
	EnvCount       int      `json:"envCount"`
	TimeoutSec     int      `json:"timeoutSec"`
	MemoryLimit    string   `json:"memoryLimit"`
	Invocations7d  *int     `json:"invocations7d,omitempty"`
	SuccessRate    *float64 `json:"successRate,omitempty"`
	AvgDurationMs  *float64 `json:"avgDurationMs,omitempty"`
	Errors7d       *int     `json:"errors7d,omitempty"`
	Target         string   `json:"target,omitempty"`
	CreatedAt      string   `json:"createdAt"`
	LastDeployedAt string   `json:"lastDeployedAt"`
}

type triggerView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	Enabled      bool   `json:"enabled"`
	Target       string `json:"target"`
	FunctionName string `json:"functionName"`
}

type invocationView struct {
	ID         string `json:"id"`
	StatusCode int    `json:"statusCode"`
	Status     string `json:"status"`
	Method     string `json:"method"`
	Time       string `json:"time"`
	DurationMs int64  `json:"durationMs"`
}

type functionDetail struct {
	functionView
	Triggers          []triggerView    `json:"triggers"`
	RecentInvocations []invocationView `json:"recentInvocations"`
	Secrets           []string         `json:"secrets"`
}

func rfc3339(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func endpointFor(name string) string { return "/v1/functions/" + name + "/invoke" }

// toView maps a Function to the ServerlessFunction shape, folding in the REAL
// 7-day invocation rollup (nil pointers → omitted → the UI shows "—", never a
// fabricated 0).
func toView(s *cloud.Service[state], f Function, st InvStats) functionView {
	v := functionView{
		Name: f.Name, Namespace: f.Namespace, Environment: f.Runtime, Status: f.Status,
		Image: f.Image, Endpoint: endpointFor(f.Name), EnvCount: len(f.EnvNames),
		TimeoutSec: f.TimeoutSec, MemoryLimit: f.MemoryLimit, Target: f.Target,
		CreatedAt: rfc3339(f.CreatedAt), LastDeployedAt: rfc3339(f.LastDeployAt),
	}
	if st.Count > 0 {
		inv := st.Count
		errs := st.Errors
		succ := float64(st.Count-st.Errors) / float64(st.Count)
		avg := float64(st.SumDuration) / float64(st.Count)
		v.Invocations7d = &inv
		v.Errors7d = &errs
		v.SuccessRate = &succ
		v.AvgDurationMs = &avg
	}
	return v
}

func httpTrigger(f Function) triggerView {
	return triggerView{
		ID: f.Name + "-http", Name: f.Name + " (HTTP)", Type: "HTTP", Enabled: true,
		Target: endpointFor(f.Name), FunctionName: f.Name,
	}
}

// Mount wires the functions surface onto app per HIP-0106. Complex flavour: it
// holds a package-global (mounted) so Shutdown can close every per-org store, so
// it constructs the Service value directly rather than via cloud.Mount.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("functions.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("functions.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("functions.Mount: empty DataDir")
	}
	b := cloud.NewBase(deps, "functions")
	s := &cloud.Service[state]{Base: b, State: state{
		stores: cloud.NewOrgStore(b, "functions", openStore),
		exec:   newExecClient(),
	}}
	mounted = s
	routes(app, s)
	// Register user functions into the unified tool plane (SourceFunction).
	tools.Register(functionToolProvider{})
	s.Log.Info("functions mounted", "exec", "sandboxes", "brand", s.Brand, "billing", s.Bill.Enabled())
	return nil
}

// routes registers the functions surface. Static sub-routes before the :name
// param route so a real function can never shadow
// /metrics|/triggers|/deployments|/secrets.
func routes(app cloud.Router, s *cloud.Service[state]) {
	// Collection root stays flat: Group("/v1/functions").Get("") would register
	// "/v1/functions/", not the bare collection path.
	app.Get("/v1/functions", cloud.Handle(s, list))
	app.Post("/v1/functions", cloud.Handle(s, create))

	g := app.Group("/v1/functions")
	g.Get("/metrics", cloud.Handle(s, metrics))
	g.Get("/triggers", cloud.Handle(s, triggers))
	g.Get("/deployments", cloud.Handle(s, deployments))
	g.Get("/secrets", cloud.Handle(s, secrets))
	g.Get("/:name", cloud.Handle(s, get))
	g.Delete("/:name", cloud.Handle(s, del))
	g.Get("/:name/invocations", cloud.Handle(s, invocations))
	g.Get("/:name/logs", cloud.Handle(s, logs))
	g.Post("/:name/invoke", cloud.Handle(s, invoke))
}

// The prose for this surface, declared beside the route table it describes.
//
// None of these handlers is a typed op — they bind and answer through zip.Ctx
// (c.Bind, c.Query, c.Param, map bodies), so zipdoc has no doc comment to lift
// and the document would otherwise publish an operationId and nothing else:
// eleven SDK methods that cannot explain themselves and eleven CLI commands with
// no help text. Describe is the seam for exactly that, keyed by the fiber pattern
// verbatim, so a route that leaves the table takes its prose with it.
func init() {
	openapi.Describe("/v1/functions", http.MethodGet,
		"Every serverless function the caller's org has published, with its real 7-day rollup",
		"A row carries the function's runtime, resource limits, deployment target and its "+
			"invoke endpoint, plus envCount — how many secrets it mounts. The registry holds "+
			"secret NAMES only; a value never enters this store and is never returned.\n\n"+
			"The rollup (invocations7d, errors7d, successRate, avgDurationMs) is counted from "+
			"real invocation rows over the trailing 7 days and is OMITTED for a function with "+
			"no calls in that window rather than sent as zero, so a consumer must render "+
			"absence as unknown, not as an idle function. Ordered most-recently-deployed first.\n\n"+
			"Scoped to the caller's own org — one store per org, with the org column on every "+
			"query. Requires a validated principal: an org claim with no verified credential "+
			"behind it is refused, never answered with an empty list.")

	openapi.Describe("/v1/functions", http.MethodPost,
		"Publish a function, or redeploy an existing one under the same name",
		"Org and name together identify a function, so a second call for a name the org "+
			"already owns is a REDEPLOY: the spec is replaced, the deploy version advances, "+
			"and the original creation time is kept. There is no separate update call, and no "+
			"way to take over a name another org owns.\n\n"+
			"What is accepted is a closed set. runtime is one of node, python, go, deno, bash "+
			"or container; name must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ and may not be one "+
			"of the reserved static names (metrics, triggers, deployments, secrets); source is "+
			"capped at 256 KiB. timeoutSec is CLAMPED to the 900s ceiling rather than rejected, "+
			"and an absent one defaults to 30s with 256Mi of memory. target=fleet runs the "+
			"function on the org's own linked GPU fleet and is accepted for runtime=python "+
			"only; everything else runs on the shared sandbox.\n\n"+
			"envNames declares which secrets the function mounts BY NAME — values live in KMS "+
			"and are resolved sandbox-side at run time, so no secret value is sent here or "+
			"stored here. Scoped to the caller's org; requires a validated principal.")

	openapi.Describe("/v1/functions/metrics", http.MethodGet,
		"Invocation chart and status breakdown across every function in the caller's org",
		"One series per function that actually ran in the window, bucketed, plus a "+
			"success/timeout/error donut over the same rows. Every point is a COUNT of real "+
			"invocation rows that fell in that bucket — nothing is interpolated, and a function "+
			"with no invocations in the window has no series at all.\n\n"+
			"The `range` query selects the window and its bucket count: 1H, 6H, 24H, 7D or 30D. "+
			"An absent or unrecognized value falls back to 24H rather than failing. At most the "+
			"5000 newest rows are read, so a very busy org's oldest buckets in a wide range can "+
			"undercount.\n\n"+
			"costCents is always null: this view has no per-invocation cost source, and reports "+
			"nothing rather than a fabricated figure. Scoped to the caller's org; requires a "+
			"validated principal.")

	openapi.Describe("/v1/functions/triggers", http.MethodGet,
		"Every trigger attached to the caller's org's functions",
		"A function has exactly ONE trigger today and it is derived, not stored: an "+
			"always-enabled HTTP trigger whose target is that function's own invoke endpoint, "+
			"listed once per function.\n\n"+
			"There is no trigger table behind this and no call that creates, disables or "+
			"deletes one. The list is a projection of the function registry, so it changes only "+
			"when a function is published or deleted.\n\n"+
			"Scoped to the caller's org; requires a validated principal.")

	openapi.Describe("/v1/functions/deployments", http.MethodGet,
		"The live deployment of every function in the caller's org",
		"A function's current record IS its deployment, so this answers in the same shape the "+
			"function list does — runtime, resource limits, target, endpoint, and when it was "+
			"last deployed.\n\n"+
			"Two things not to assume. The invocation rollup is never populated here, even for "+
			"a function that has run: those fields are omitted unconditionally, and the function "+
			"list is where they are filled in. And this is an inventory of what is live, not a "+
			"history — there is exactly one entry per function, and a redeploy replaces it "+
			"rather than appending to it.\n\n"+
			"Scoped to the caller's org; requires a validated principal.")

	openapi.Describe("/v1/functions/secrets", http.MethodGet,
		"The names of the secrets mounted by the caller's org's functions",
		"NAMES only. A secret's value is not held by this subsystem and is not read on this "+
			"path — values live in KMS and are resolved sandbox-side when a function runs — so "+
			"nothing in this answer is a credential.\n\n"+
			"The list is derived from the mount declarations on the function records and "+
			"deduplicated by namespace and name, so a name mounted by several functions appears "+
			"ONCE: mountedBy names the first function that claimed it in deploy order, not every "+
			"function that mounts it. Read it as a hint about origin, not as a complete usage "+
			"map.\n\n"+
			"Scoped to the caller's org; requires a validated principal.")

	openapi.Describe("/v1/functions/:name", http.MethodGet,
		"One function in full: spec, trailing-7-day rollup, trigger, latest runs and mounted secret names",
		"Extends the list row with the function's single derived HTTP trigger, its 20 most "+
			"recent invocations (newest first, metadata only — no captured output), and "+
			"`secrets`, the NAMES of the secrets it mounts. No secret value is stored or "+
			"returned.\n\n"+
			"Lookup is keyed on (org, name), so a function that exists but belongs to another "+
			"org answers exactly as one that never existed — not found, never a signal that the "+
			"name is taken elsewhere. The 7-day rollup fields are omitted rather than zeroed "+
			"when the function has not run in the window.\n\n"+
			"Requires a validated principal.")

	openapi.Describe("/v1/functions/:name", http.MethodDelete,
		"Delete a function and its entire invocation history",
		"One transaction removes the function record and every invocation row recorded "+
			"against its name, so that history also leaves the metrics chart and the invocation "+
			"list. This is not a soft delete and there is no restore.\n\n"+
			"Deletion is keyed on (org, name): a name owned by another org is not found here, "+
			"exactly like a name that never existed, so the call cannot be used to probe for or "+
			"destroy another tenant's function. A successful delete answers with no body.\n\n"+
			"Requires a validated principal.")

	openapi.Describe("/v1/functions/:name/invocations", http.MethodGet,
		"Recent invocation history for one function, newest first",
		"Each entry is invocation METADATA — id, status, HTTP status code, wall-clock "+
			"duration and when it ran. The captured stdout/stderr is not on this path; the logs "+
			"call returns it, for the latest run only.\n\n"+
			"`limit` defaults to 100 and is clamped: at or below zero, above 500, or not a "+
			"number at all, it falls back to 100. An unknown function name is NOT an error here "+
			"— nothing has ever run under it, so the answer is an empty list rather than a not-"+
			"found, and a caller testing existence must ask for the function itself.\n\n"+
			"Scoped to the caller's org, so it can only ever return the calling tenant's own "+
			"runs. Requires a validated principal.")

	openapi.Describe("/v1/functions/:name/logs", http.MethodGet,
		"The captured output of a function's most recent invocation",
		"One string, from the LATEST invocation only. This is not a log stream and carries no "+
			"history; the invocations list is where earlier runs are enumerated.\n\n"+
			"When that run failed, the string is its ERROR text rather than its stdout — the two "+
			"share one field, so success cannot be told from failure by this value alone and the "+
			"invocation's status is what answers that. Output was truncated to 64 KiB when the "+
			"run was recorded, error text to 16 KiB.\n\n"+
			"A function that has never run — or a name that does not exist in the caller's org — "+
			"answers with an empty string, not a not-found. Scoped to the caller's org; requires "+
			"a validated principal.")

	openapi.Describe("/v1/functions/:name/invoke", http.MethodPost,
		"Run a function and get back the recorded invocation",
		"The body's `input` is handed to the function on stdin. Execution NEVER happens in "+
			"this process: the runtime and source go to the sandboxed code executor, or, for a "+
			"function published with target=fleet, to the org's own linked GPU fleet as an "+
			"fn.run job this call blocks on until it finishes. Either way it is bounded by the "+
			"function's own timeout, itself capped at 900s.\n\n"+
			"The answer is the invocation record — id, status, duration — and its HTTP status is "+
			"about the RUN, not about this API: a function whose own code fails answers 502 "+
			"with a recorded `error` invocation, which is a successful invocation of a failing "+
			"program. The captured output is not in this reply; the logs call returns it.\n\n"+
			"MONEY. The caller's org ledger is gated BEFORE any compute runs, so an org out of "+
			"credit or over its spend cap is refused 402 and nothing executes, and a billing "+
			"plane that cannot answer refuses rather than granting free compute. A run that "+
			"actually executed is then debited twice — a flat per-invocation fee, and GB-seconds "+
			"of compute derived from the measured duration and the function's configured memory. "+
			"A run that never reached its executor (unreachable, or timed out in transport) "+
			"consumed nothing and is not charged; a run whose code exited non-zero DID consume "+
			"compute and is. An operator who prices either half at zero makes it a no-op, and a "+
			"zero request fee removes the balance gate with it.\n\n"+
			"When the sandbox is not configured on this deployment, a non-fleet function fails "+
			"closed before anything is recorded — no execution and no fabricated output. Scoped "+
			"to the caller's org; requires a validated principal.")
}

// ---- handlers ----

type createReq struct {
	Name        string   `json:"name"`
	Environment string   `json:"environment"`
	Runtime     string   `json:"runtime"`
	Namespace   string   `json:"namespace"`
	Image       string   `json:"image"`
	Code        string   `json:"code"`
	Handler     string   `json:"handler"`
	TimeoutSec  int      `json:"timeoutSec"`
	MemoryLimit string   `json:"memoryLimit"`
	EnvNames    []string `json:"envNames"`
	Target      string   `json:"target"` // ""|"sandbox" = sandbox, "fleet" = org GPU fleet
}

func create(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	var body createReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return zip.ErrBadRequest("name is required")
	}
	if reserved[strings.ToLower(name)] {
		return zip.ErrBadRequest("name is reserved")
	}
	if !nameRE.MatchString(name) {
		return zip.ErrBadRequest("name must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	// environment (functions.ts) and runtime are the same field; accept either.
	runtime := strings.ToLower(strings.TrimSpace(firstNonEmpty(body.Runtime, body.Environment)))
	if runtime == "" {
		runtime = "node"
	}
	if !runtimes[runtime] {
		return zip.ErrBadRequest("unsupported runtime")
	}
	if len(body.Code) > maxCode {
		return zip.ErrBadRequest("code too large")
	}
	timeout := body.TimeoutSec
	if timeout <= 0 {
		timeout = 30
	} else if timeout > 900 {
		timeout = 900 // clamp to the ceiling, don't silently reset to the default
	}
	mem := strings.TrimSpace(body.MemoryLimit)
	if mem == "" {
		mem = "256Mi"
	}
	target := strings.ToLower(strings.TrimSpace(body.Target))
	if target == "sandbox" {
		target = ""
	}
	if target != "" && target != "fleet" {
		return zip.ErrBadRequest("target must be sandbox or fleet")
	}
	if target == "fleet" && runtime != "python" {
		return zip.ErrBadRequest("target=fleet supports runtime=python only")
	}
	id, err := genID("fn")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	f := Function{
		ID: id, Org: org, Name: name, Namespace: sanitizeNs(body.Namespace), Runtime: runtime,
		Image: strings.TrimSpace(body.Image), Code: body.Code, Handler: strings.TrimSpace(body.Handler),
		TimeoutSec: timeout, MemoryLimit: mem, EnvNames: cleanList(body.EnvNames),
		Target: target, Status: "ready", LastDeployAt: now,
	}
	saved, err := store.Upsert(c.Context(), f)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	return c.JSON(http.StatusCreated, toView(s, saved, InvStats{}))
}

func list(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	rows, err := store.List(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	since := time.Now().Unix() - window7d
	out := make([]functionView, 0, len(rows))
	for _, f := range rows {
		st, err := store.StatsSince(c.Context(), org, f.Name, since)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "stats: %v", err)
		}
		out = append(out, toView(s, f, st))
	}
	return c.JSON(http.StatusOK, map[string]any{"functions": out})
}

func get(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	name := nameParam(c)
	f, err := store.Get(c.Context(), org, name)
	if err == errNotFound {
		return zip.ErrNotFound("function not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	since := time.Now().Unix() - window7d
	st, _ := store.StatsSince(c.Context(), org, name, since)
	invs, err := store.ListInvocations(c.Context(), org, name, 20)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "invocations: %v", err)
	}
	return c.JSON(http.StatusOK, functionDetail{
		functionView:      toView(s, f, st),
		Triggers:          []triggerView{httpTrigger(f)},
		RecentInvocations: toInvViews(invs),
		Secrets:           nonNil(f.EnvNames),
	})
}

func del(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	deleted, err := store.Delete(c.Context(), org, nameParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("function not found")
	}
	return c.NoContent(http.StatusNoContent)
}

func invocations(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	name := nameParam(c)
	limit := 100
	if q := strings.TrimSpace(c.Query("limit")); q != "" {
		if n, err := strconv.Atoi(q); err == nil {
			limit = n
		}
	}
	invs, err := store.ListInvocations(c.Context(), org, name, limit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "invocations: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"invocations": toInvViews(invs)})
}

func logs(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	invs, err := store.ListInvocations(c.Context(), org, nameParam(c), 1)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "logs: %v", err)
	}
	logs := ""
	if len(invs) > 0 {
		if invs[0].Error != "" {
			logs = invs[0].Error
		} else {
			logs = invs[0].Output
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"logs": logs})
}

func triggers(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	rows, err := store.List(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "triggers: %v", err)
	}
	out := make([]triggerView, 0, len(rows))
	for _, f := range rows {
		out = append(out, httpTrigger(f))
	}
	return c.JSON(http.StatusOK, map[string]any{"triggers": out})
}

func deployments(s *cloud.Service[state], c *zip.Ctx) error {
	// Each function's current record IS its live deployment; return them as the
	// deployment inventory (console normalizes this as a function list).
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	rows, err := store.List(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "deployments: %v", err)
	}
	out := make([]functionView, 0, len(rows))
	for _, f := range rows {
		out = append(out, toView(s, f, InvStats{}))
	}
	return c.JSON(http.StatusOK, map[string]any{"functions": out})
}

type secretView struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	MountedBy string `json:"mountedBy,omitempty"`
}

func secrets(s *cloud.Service[state], c *zip.Ctx) error {
	// NAMES only — values are NEVER read or returned (Secret-Manager principle).
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	rows, err := store.List(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "secrets: %v", err)
	}
	seen := map[string]bool{}
	out := make([]secretView, 0)
	for _, f := range rows {
		for _, n := range f.EnvNames {
			key := f.Namespace + "/" + n
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, secretView{Name: n, Namespace: f.Namespace, MountedBy: f.Name})
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"secrets": out})
}

// ---- helpers ----

func toInvViews(invs []Invocation) []invocationView {
	out := make([]invocationView, 0, len(invs))
	for _, iv := range invs {
		out = append(out, invocationView{
			ID: iv.ID, StatusCode: iv.StatusCode, Status: iv.Status, Method: iv.Method,
			Time: rfc3339(iv.CreatedAt), DurationMs: iv.DurationMs,
		})
	}
	return out
}

func nameParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("name")) }

// org resolves the org — the org isolation KEY. It uses c.Org() EXACTLY
// as SanitizeIdentity minted it from the validated IAM owner claim (HIP-0026):
// never lowercased/stripped/truncated. Normalizing would collapse distinct
// owners into one bucket — a cross-org break (Red HIGH-1). Reject only empty
// or pathologically long. No magic "admin" bucket.
func org(c *zip.Ctx) (string, bool) { return principal.Org(c) }

// sanitizeNs normalizes the function NAMESPACE — a cosmetic grouping/display
// field the caller supplies, NOT the org isolation key (that is the org).
// Lossy normalization here is safe: the namespace never gates cross-org
// access (every query is already scoped by the exact org column).
func sanitizeNs(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 63 {
		out = strings.Trim(out[:63], "-")
	}
	if out == "" {
		return "default"
	}
	return out
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
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

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

// Shutdown closes every open per-org functions store. Idempotent.
func Shutdown() error {
	if mounted == nil {
		return nil
	}
	err := mounted.State.stores.CloseAll()
	mounted = nil
	return err
}
