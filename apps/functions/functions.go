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
	"cmp"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/internal/mint"
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
// via the ONE shared cloud.Meter (product "functions"); there is no
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
// through cloud.OrgNamespace — the single path a validated org takes —
// and asks the registry for that name. Nothing else here resolves a store, so
// "which file does this request touch" has one answer from one input.
//
// org MUST already be validated: principal.Org for a request, or the caller's
// own server-side resolution for an in-process client.
func storeFor(s *cloud.Service[state], org string) (*Store, error) {
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	return s.State.stores.For(ns)
}

// ---- HTTP response shapes (console functions.ts contract) ----

// functionView is one published function and its real 7-day rollup.
type functionView struct {
	Name           string   `json:"name"`                    // the function's org-unique handle
	Namespace      string   `json:"namespace"`               // the display group it belongs to; the org is the isolation key
	Environment    string   `json:"environment"`             // the language it runs under
	Status         string   `json:"status"`                  // whether it is ready to serve
	Image          string   `json:"image,omitempty"`         // the prebuilt image it runs, when it runs one instead of source
	Endpoint       string   `json:"endpoint"`                // the path that invokes it
	EnvCount       int      `json:"envCount"`                // how many secret NAMES it mounts; values are never carried
	TimeoutSec     int      `json:"timeoutSec"`              // its per-invocation deadline
	MemoryLimit    string   `json:"memoryLimit"`             // the memory it runs with, and the multiplier on its compute charge
	Invocations7d  *int     `json:"invocations7d,omitempty"` // runs in the last 7 days; ABSENT, never 0, when it has not run
	SuccessRate    *float64 `json:"successRate,omitempty"`   // share of those runs that succeeded, 0..1
	AvgDurationMs  *float64 `json:"avgDurationMs,omitempty"` // mean wall-clock of those runs
	Errors7d       *int     `json:"errors7d,omitempty"`      // how many of those runs failed
	Target         string   `json:"target,omitempty"`        // where it runs: empty for the sandbox, "fleet" for the org's GPU fleet
	CreatedAt      string   `json:"createdAt"`               // when it was first published
	LastDeployedAt string   `json:"lastDeployedAt"`          // when its code last changed
}

// triggerView is how one function is reached.
type triggerView struct {
	ID           string `json:"id"`           // the trigger's handle
	Name         string `json:"name"`         // a human label for it
	Type         string `json:"type"`         // what kind of trigger it is; HTTP is the only one today
	Enabled      bool   `json:"enabled"`      // whether it currently fires
	Target       string `json:"target"`       // the path it calls
	FunctionName string `json:"functionName"` // the function it calls
}

// invocationView is one recorded run.
//
// It also states the HTTP status an invoke reply carries, because that status is a
// property of the RUN — clean, failed, or never attempted because this deployment
// has no sandbox — and a value that states its own status cannot disagree with the
// one on the wire.
type invocationView struct {
	ID string `json:"id"` // the invocation's handle
	// Code is the status the function's OWN code answered with, which is not the
	// status of the reply — a program can answer 500 through a healthy sandbox.
	Code       int    `json:"statusCode"`
	Status     string `json:"status"`     // how the run ended: ok, error or timeout
	Method     string `json:"method"`     // the HTTP method that triggered it
	Time       string `json:"time"`       // when it ran, RFC3339
	DurationMs int64  `json:"durationMs"` // how long it took
	// reply is the status the invoke ANSWER carries. It is off the wire: the
	// status is the status, and repeating it in the body would be a second place
	// for it to disagree. Zero on a row read back from the store, which answers
	// nothing.
	reply int `json:"-"`
}

// StatusCode is 200 when the org's code ran clean, 502 when it ran and failed, and
// 503 when this deployment cannot run code at all. "This deployment cannot run
// code" is not "the run failed" — it is a deployment fact, and 503 is the status a
// caller retries against a healthy replica on.
func (v *invocationView) StatusCode() int { return v.reply }

// functionDetail is one function with everything a detail page needs in one
// round-trip. It EMBEDS functionView, so the wire carries that shape's fields
// alongside these three.
type functionDetail struct {
	functionView
	// Triggers is how this function is reached.
	Triggers []triggerView `json:"triggers"`
	// RecentInvocations is its twenty most recent runs, newest first.
	RecentInvocations []invocationView `json:"recentInvocations"`
	// Secrets are the NAMES it mounts. Values are never read or returned.
	Secrets []string `json:"secrets"`
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
// it constructs the Service value directly rather than via cloud.Use.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("functions.Use:  nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("functions.Use:  empty DataDir")
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
	s.Log.Info("functions mounted", "exec", "sandboxes", "brand", s.Brand)
	return nil
}

// routes registers the functions surface. Static sub-routes before the :name
// param route so a real function can never shadow
// /metrics|/triggers|/deployments|/secrets.
//
// Every op is TYPED — its input and its answer are Go types — so the schema, the
// prose, the MCP tool, the CLI command and every generated SDK method are
// projections of the handler itself. zipdoc lifts the doc comments into
// zipdoc_gen.go, which is the only way prose reaches the published registry: Go
// drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	za := cloud.ZipApp(app)

	// Collection root stays flat: Group("/v1/functions").Get("") would register
	// "/v1/functions/", not the bare collection path.
	zip.Get(za, "/v1/functions", o.list)
	zip.Post(za, "/v1/functions", o.create, zip.WithStatus(http.StatusCreated))

	g := app.Group("/v1/functions")
	// cloud.DenyEnvelope BEFORE the leaves, because fiber runs middleware in
	// registration order and one installed after its leaves never runs. An invoke
	// gates on the caller's balance, and the envelope is what makes that refusal
	// the fleet's own nested {"error":{"code","message"}}.
	g.Use(cloud.DenyEnvelope())

	zip.Get(g, "/metrics", o.metrics)
	zip.Get(g, "/triggers", o.triggers)
	zip.Get(g, "/deployments", o.deployments)
	zip.Get(g, "/secrets", o.secrets)
	zip.Get(g, "/:name", o.get)
	zip.Delete(g, "/:name", o.del, zip.WithStatus(http.StatusNoContent))
	zip.Get(g, "/:name/invocations", o.invocations)
	zip.Get(g, "/:name/logs", o.logs)
	// An invoke answers the invocation record whatever happened to it: 200 when the
	// org's code ran clean, 502 when it ran and failed, 503 when this deployment
	// cannot run code at all. The record IS the evidence, so it rides the failure
	// rather than being replaced by an error envelope.
	zip.Post(g, "/:name/invoke", o.invoke,
		zip.WithStatus(http.StatusOK, http.StatusBadGateway, http.StatusServiceUnavailable))
}

// ops binds the service to the typed ops. A TypedHandler takes no service
// parameter, so the service arrives as a RECEIVER and every op is a method value
// — also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// ---- handlers ----

// noIn is the input of an op that takes nothing: no body, no path parameter, no
// query.
type noIn struct{}

// fnRef addresses one of the caller org's functions.
type fnRef struct {
	// Name is the function the URL names.
	Name string `json:"name"`
}

// none is the answer of an op that removes something: there is nothing left to
// describe, so it answers 204 and no body.
type none struct{}

// fnList is a set of functions.
type fnList struct {
	// Functions is one row per published function.
	Functions []functionView `json:"functions"`
}

// triggerList is what calls the org's functions.
type triggerList struct {
	// Triggers is one row per function, describing how it is reached.
	Triggers []triggerView `json:"triggers"`
}

// secretList is the NAMES of the secrets the org's functions mount.
type secretList struct {
	// Secrets is one row per distinct (namespace, name) a function mounts. Values
	// are NEVER read or returned.
	Secrets []secretView `json:"secrets"`
}

// invocationList is a page of past invocations.
type invocationList struct {
	// Invocations is one row per past run, newest first.
	Invocations []invocationView `json:"invocations"`
}

// invocationPage bounds an invocation listing.
type invocationPage struct {
	// Name is the function the URL names.
	Name string `json:"name"`
	// Limit caps the page, defaulting to 100.
	Limit int `json:"limit"`
}

// logLines is the output of the most recent run.
type logLines struct {
	// Logs is that run's error text when it failed, else its output. It is empty
	// when the function has never run.
	Logs string `json:"logs"`
}

// definition publishes a serverless function.
type definition struct {
	// Name is the function's org-unique handle and the segment that addresses it,
	// matching ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$. The names that would shadow a
	// collection route are reserved.
	Name string `json:"name" validate:"required"`
	// Environment is a second spelling of runtime, accepted so a console that says
	// "environment" needs no translation.
	Environment string `json:"environment"`
	// Runtime is the language the code runs under: node, python or deno.
	Runtime string `json:"runtime"`
	// Namespace groups functions for display. It is cosmetic — the org is the
	// isolation key — and is normalised to a DNS-safe label.
	Namespace string `json:"namespace"`
	// Image names a prebuilt image to run instead of source.
	Image string `json:"image"`
	// Code is the source to run, capped so one function cannot amplify the store.
	Code string `json:"code"`
	// Handler is the entry point within the code.
	Handler string `json:"handler"`
	// TimeoutSec is the per-invocation deadline, defaulting to 30 and clamped at
	// 900 — a larger value is capped rather than reset to the default.
	TimeoutSec int `json:"timeoutSec"`
	// MemoryLimit is the memory the function runs with, defaulting to 256Mi. It is
	// also the multiplier on the GB-seconds compute charge.
	MemoryLimit string `json:"memoryLimit"`
	// EnvNames are the secret NAMES to mount. Values live in the secret store and
	// are never carried here.
	EnvNames []string `json:"envNames"`
	// Target is where the function runs: sandbox (the default) or fleet, the org's
	// own GPU fleet. fleet supports runtime=python only.
	Target string `json:"target"`
}

// create publishes a serverless function under the caller's org and answers 201
// with it.
//
// The name is the key and is claimed once; the names that would shadow a
// collection route are reserved. runtime and environment are the same field —
// either spelling is accepted — and default to node.
//
// Bounds are clamped rather than refused where a clamp is honest: a timeout above
// the 900-second ceiling becomes the ceiling instead of silently reverting to the
// 30-second default, and an omitted memory limit becomes 256Mi. target=fleet runs
// on the org's own GPU fleet and supports runtime=python only.
//
// Requires a validated principal; the function is owned by that principal's org.
func (o ops) create(ctx context.Context, in *definition) (*functionView, error) {
	s := o.s
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	if reserved[strings.ToLower(name)] {
		return nil, zip.ErrBadRequest("name is reserved")
	}
	if !nameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("name must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	// environment (functions.ts) and runtime are the same field; accept either.
	runtime := strings.ToLower(cmp.Or(strings.TrimSpace(in.Runtime), strings.TrimSpace(in.Environment)))
	if runtime == "" {
		runtime = "node"
	}
	if !runtimes[runtime] {
		return nil, zip.ErrBadRequest("unsupported runtime")
	}
	if len(in.Code) > maxCode {
		return nil, zip.ErrBadRequest("code too large")
	}
	timeout := in.TimeoutSec
	if timeout <= 0 {
		timeout = 30
	} else if timeout > 900 {
		timeout = 900 // clamp to the ceiling, don't silently reset to the default
	}
	mem := strings.TrimSpace(in.MemoryLimit)
	if mem == "" {
		mem = "256Mi"
	}
	target := strings.ToLower(strings.TrimSpace(in.Target))
	if target == "sandbox" {
		target = ""
	}
	if target != "" && target != "fleet" {
		return nil, zip.ErrBadRequest("target must be sandbox or fleet")
	}
	if target == "fleet" && runtime != "python" {
		return nil, zip.ErrBadRequest("target=fleet supports runtime=python only")
	}
	f := Function{
		ID: mint.ID("fn"), Org: org, Name: name, Namespace: sanitizeNs(in.Namespace), Runtime: runtime,
		Image: strings.TrimSpace(in.Image), Code: in.Code, Handler: strings.TrimSpace(in.Handler),
		TimeoutSec: timeout, MemoryLimit: mem, EnvNames: cleanList(in.EnvNames),
		Target: target, Status: "ready", LastDeployAt: time.Now().Unix(),
	}
	saved, err := store.Upsert(ctx, f)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	out := toView(s, saved, InvStats{})
	return &out, nil
}

// list is every serverless function the caller's org has published, each with its
// real 7-day rollup.
//
// A row carries the function's runtime, resource limits, deployment target and its
// invoke endpoint, plus envCount — how many secrets it mounts. The rollup fields
// are ABSENT rather than zero when the function has not run in the window, so a
// console renders "—" instead of a fabricated 0.
//
// Requires a validated principal; the listing is scoped to its org.
func (o ops) list(ctx context.Context, _ *noIn) (*fnList, error) {
	s := o.s
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	rows, err := store.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	since := time.Now().Unix() - window7d
	out := make([]functionView, 0, len(rows))
	for _, f := range rows {
		st, err := store.StatsSince(ctx, org, f.Name, since)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "stats: %v", err)
		}
		out = append(out, toView(s, f, st))
	}
	return &fnList{Functions: out}, nil
}

// get is one function with everything a detail page needs in one round-trip: its
// definition, its 7-day rollup, its trigger, its twenty most recent invocations
// and the NAMES of the secrets it mounts.
//
// Secret values are never read or returned. A name the caller's org does not hold
// is 404, which is also what another tenant's function looks like from here.
func (o ops) get(ctx context.Context, in *fnRef) (*functionDetail, error) {
	s := o.s
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	name := strings.TrimSpace(in.Name)
	f, err := store.Get(ctx, org, name)
	if err == errNotFound {
		return nil, zip.ErrNotFound("function not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	since := time.Now().Unix() - window7d
	st, _ := store.StatsSince(ctx, org, name, since)
	invs, err := store.ListInvocations(ctx, org, name, 20)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "invocations: %v", err)
	}
	return &functionDetail{
		functionView:      toView(s, f, st),
		Triggers:          []triggerView{httpTrigger(f)},
		RecentInvocations: toInvViews(invs),
		Secrets:           nonNil(f.EnvNames),
	}, nil
}

// del removes one of the caller org's functions and answers 204.
//
// A name this org does not hold is 404 — never a silent success — and a name
// belonging to another tenant is the same 404, because the delete is predicated on
// the validated org.
func (o ops) del(ctx context.Context, in *fnRef) (*none, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	deleted, err := store.Delete(ctx, org, strings.TrimSpace(in.Name))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("function not found")
	}
	return nil, nil
}

// invocations is one function's past runs, newest first — each with its status,
// HTTP code, method, time and duration.
//
// These are real recorded rows, not a projection: an invocation appears here only
// once it actually ran. Requires a validated principal; the read is scoped to its
// org.
func (o ops) invocations(ctx context.Context, in *invocationPage) (*invocationList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 100
	}
	invs, err := store.ListInvocations(ctx, org, strings.TrimSpace(in.Name), limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "invocations: %v", err)
	}
	return &invocationList{Invocations: toInvViews(invs)}, nil
}

// logs is the output of a function's most recent run — its error text when that
// run failed, else what it printed.
//
// It is the LAST run only, and it is empty when the function has never run. There
// is no log retention behind this beyond the recorded invocation itself.
func (o ops) logs(ctx context.Context, in *fnRef) (*logLines, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	invs, err := store.ListInvocations(ctx, org, strings.TrimSpace(in.Name), 1)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "logs: %v", err)
	}
	out := ""
	if len(invs) > 0 {
		if invs[0].Error != "" {
			out = invs[0].Error
		} else {
			out = invs[0].Output
		}
	}
	return &logLines{Logs: out}, nil
}

// triggers is what calls the caller org's functions — one row per function.
//
// Every function has exactly one trigger today, its HTTP invoke endpoint, so this
// is the function list read as "how is each of these reached".
func (o ops) triggers(ctx context.Context, _ *noIn) (*triggerList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	rows, err := store.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "triggers: %v", err)
	}
	out := make([]triggerView, 0, len(rows))
	for _, f := range rows {
		out = append(out, httpTrigger(f))
	}
	return &triggerList{Triggers: out}, nil
}

// deployments is what is live right now — each function's current record IS its
// live deployment, so this is the deployment inventory.
//
// There is no deployment history behind it: a function has one record, and
// publishing replaces it. The 7-day rollup is deliberately absent here, because
// this read is about what is deployed rather than about how it has performed.
func (o ops) deployments(ctx context.Context, _ *noIn) (*fnList, error) {
	s := o.s
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	rows, err := store.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "deployments: %v", err)
	}
	out := make([]functionView, 0, len(rows))
	for _, f := range rows {
		out = append(out, toView(s, f, InvStats{}))
	}
	return &fnList{Functions: out}, nil
}

// secretView is one mounted secret, by NAME.
type secretView struct {
	// Name is the environment variable the function mounts.
	Name string `json:"name"`
	// Namespace is the group the mounting function belongs to.
	Namespace string `json:"namespace,omitempty"`
	// MountedBy is a function that mounts it.
	MountedBy string `json:"mountedBy,omitempty"`
}

// secrets is the NAMES of the secrets the caller org's functions mount.
//
// Values are NEVER read or returned — this surface knows which names a function
// asks for and nothing about what is behind them, which is what makes it safe to
// list at all. One row per distinct (namespace, name).
func (o ops) secrets(ctx context.Context, _ *noIn) (*secretList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	rows, err := store.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "secrets: %v", err)
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
	return &secretList{Secrets: out}, nil
}

// ---- helpers ----

func toInvViews(invs []Invocation) []invocationView {
	out := make([]invocationView, 0, len(invs))
	for _, iv := range invs {
		out = append(out, invocationView{
			ID: iv.ID, Code: iv.StatusCode, Status: iv.Status, Method: iv.Method,
			Time: rfc3339(iv.CreatedAt), DurationMs: iv.DurationMs,
		})
	}
	return out
}

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

// Shutdown closes every open per-org functions store. Idempotent.
func Shutdown() error {
	if mounted == nil {
		return nil
	}
	err := mounted.State.stores.CloseAll()
	mounted = nil
	return err
}
