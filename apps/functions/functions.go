// Package functions mounts the Hanzo Cloud /v1/functions surface: a per-org
// serverless function registry. Every function belongs to exactly one org (the
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
// Invoke delegates to the sandboxed code executor (CODE_EXEC_UPSTREAM) — this
// binary NEVER runs org code in-process. When the sandbox is not configured
// invoke fails closed (503) and fabricates nothing. Every metric the Overview
// shows is DERIVED from real invocation rows; there is no invented rollup.
package functions

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/tools"
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

// storeFor resolves the caller's org-scoped functions store, opening the per-org
// file ({DataDir}/orgs/{orgSlug}/functions.db) once via the shared cache.
func storeFor(s *cloud.Service[state], org string) (*Store, error) {
	return s.State.stores.For(org, "")
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
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "functions"), State: state{
		stores: cloud.NewOrgStore(deps.DataDir, "functions", openStore),
		exec:   newExecClient(),
	}}
	mounted = s
	routes(app, s)
	// Register user functions into the unified tool plane (SourceFunction).
	tools.Register(functionToolProvider{})
	s.Log.Info("functions mounted", "exec", s.State.exec.configured(), "brand", s.Brand, "billing", s.Bill.Enabled())
	return nil
}

// routes registers the functions surface. Static sub-routes before the :name
// param route so a real function can never shadow
// /metrics|/triggers|/deployments|/secrets.
//
// Every route is a TYPED op — registered on the App with its ABSOLUTE path,
// because the op registry (the one value OpenAPI, MCP and the CLI are projected
// from) keys on it — except two:
//
//   - GET /:name answers functionDetail, which EMBEDS functionView so JSON
//     promotes its 17 fields to the top level. zip's schema walker skips an
//     unexported field and names an exported embedded one as a nested property,
//     so no Out it can express matches the wire. It stays a raw handler rather
//     than publishing a shape clients would generate wrong.
//   - POST /:name/invoke is METERED: a billing denial answers the fleet-wide
//     contract (cloud.DenyResource — 402 insufficient_balance /
//     spend_cap_exceeded, 503 balance_unavailable, each a
//     {"error":{"code","message"}} body) that zip's error type cannot express,
//     and a run whose program failed answers 502 with the same invocation body,
//     which is not a success status an op can declare.
func routes(app cloud.Router, s *cloud.Service[state]) {
	z := cloud.ZipApp(app)
	o := ops{s: s}
	// The bridge FIRST: fiber runs middleware in registration order, so one
	// installed after these leaves would never run — and every op below resolves
	// its tenant through it.
	app.Group("/v1/functions").Use(cloud.Bridge())
	// Collection root stays flat: Group("/v1/functions").Get("") would register
	// "/v1/functions/", not the bare collection path.
	zip.Get(z, "/v1/functions", o.list)
	zip.Post(z, "/v1/functions", o.create, zip.WithStatus(http.StatusCreated))

	g := app.Group("/v1/functions")
	zip.Get(z, "/v1/functions/metrics", o.metrics)
	zip.Get(z, "/v1/functions/triggers", o.triggers)
	zip.Get(z, "/v1/functions/deployments", o.deployments)
	zip.Get(z, "/v1/functions/secrets", o.secrets)
	g.Get("/:name", cloud.Handle(s, get)) // embedded-view Out — see above
	zip.Delete(z, "/v1/functions/:name", o.del)
	zip.Get(z, "/v1/functions/:name/invocations", o.invocations)
	zip.Get(z, "/v1/functions/:name/logs", o.logs)
	g.Post("/:name/invoke", cloud.Handle(s, invoke)) // metered — see above
}

// ops binds the service to functions' typed handlers: a typed handler takes only
// a context and its decoded In, so the service arrives as a RECEIVER.
type ops struct{ s *cloud.Service[state] }

// scope is the ONE gate every typed op opens with: the VALIDATED org and the
// per-org store it addresses. Off the HTTP path there is no validated org, so
// the op refuses rather than reading across tenants.
func (o ops) scope(ctx context.Context) (string, *Store, error) {
	orgID, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", nil, zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(o.s, orgID)
	if err != nil {
		return "", nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	return orgID, store, nil
}

// ---- declared shapes ----

// functionRef addresses one function by name.
type functionRef struct {
	// Name is the function name from the path.
	Name string `json:"name"`
}

// invocationQuery is the invocation-history read.
type invocationQuery struct {
	// Name is the function name from the path.
	Name string `json:"name"`
	// Limit caps the rows returned; 0 or absent means 100.
	Limit int `json:"limit"`
}

// functionList is the function inventory: the same shape for the collection read
// and for the deployment inventory, since a function's current record IS its
// live deployment.
type functionList struct {
	// Functions is one entry per function in the caller's org.
	Functions []functionView `json:"functions"`
}

// triggerList is the trigger inventory.
type triggerList struct {
	// Triggers is one entry per function's HTTP trigger.
	Triggers []triggerView `json:"triggers"`
}

// secretList is the secret inventory — NAMES only, never values.
type secretList struct {
	// Secrets is one entry per distinct (namespace, name) a function mounts.
	Secrets []secretView `json:"secrets"`
}

// invocationList is the invocation history of one function.
type invocationList struct {
	// Invocations is the recorded runs, most recent first.
	Invocations []invocationView `json:"invocations"`
}

// logsView is the last invocation's output, or its error when it failed.
type logsView struct {
	// Logs is the last run's stderr when it errored, else its stdout. Empty when
	// the function has never run.
	Logs string `json:"logs"`
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

// create registers a function in the caller's org and answers 201.
// Name is required and must be unreserved; runtime defaults to node, timeout to
// 30s (clamped to 900), memory to 256Mi. Re-creating an existing name replaces it.
//
// Example: {"name": "resize", "runtime": "python", "code": "def handler(x): return x", "timeoutSec": 30}
func (o ops) create(ctx context.Context, in *createReq) (*functionView, error) {
	orgID, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	org, body := orgID, *in
	name := strings.TrimSpace(body.Name)
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
	runtime := strings.ToLower(strings.TrimSpace(firstNonEmpty(body.Runtime, body.Environment)))
	if runtime == "" {
		runtime = "node"
	}
	if !runtimes[runtime] {
		return nil, zip.ErrBadRequest("unsupported runtime")
	}
	if len(body.Code) > maxCode {
		return nil, zip.ErrBadRequest("code too large")
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
		return nil, zip.ErrBadRequest("target must be sandbox or fleet")
	}
	if target == "fleet" && runtime != "python" {
		return nil, zip.ErrBadRequest("target=fleet supports runtime=python only")
	}
	id, err := genID("fn")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	f := Function{
		ID: id, Org: org, Name: name, Namespace: sanitizeNs(body.Namespace), Runtime: runtime,
		Image: strings.TrimSpace(body.Image), Code: body.Code, Handler: strings.TrimSpace(body.Handler),
		TimeoutSec: timeout, MemoryLimit: mem, EnvNames: cleanList(body.EnvNames),
		Target: target, Status: "ready", LastDeployAt: now,
	}
	saved, err := store.Upsert(ctx, f)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	v := toView(o.s, saved, InvStats{})
	return &v, nil
}

// list returns the caller org's functions, each with its 7-day invocation stats.
// Only the caller's own org is ever read: the store is opened per org.
//
// Response: {"functions": [{"name": "resize", "environment": "python", "status": "ready", "endpoint": "/v1/functions/resize/invoke"}]}
func (o ops) list(ctx context.Context, _ *struct{}) (*functionList, error) {
	orgID, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := store.List(ctx, orgID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	since := time.Now().Unix() - window7d
	out := make([]functionView, 0, len(rows))
	for _, f := range rows {
		st, err := store.StatsSince(ctx, orgID, f.Name, since)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "stats: %v", err)
		}
		out = append(out, toView(o.s, f, st))
	}
	return &functionList{Functions: out}, nil
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

// del deletes one function of the caller's org and answers 204.
// A name in another org is a 404, never a delete — the store is opened per org.
//
// Example: {"name": "resize"}
func (o ops) del(ctx context.Context, in *functionRef) (*struct{}, error) {
	orgID, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := store.Delete(ctx, orgID, strings.TrimSpace(in.Name))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("function not found")
	}
	return nil, nil
}

// invocations returns one function's recorded runs, most recent first.
// limit caps the rows; anything non-positive or absent means 100.
//
// Example: {"name": "resize", "limit": 20}
func (o ops) invocations(ctx context.Context, in *invocationQuery) (*invocationList, error) {
	orgID, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 100
	}
	invs, err := store.ListInvocations(ctx, orgID, strings.TrimSpace(in.Name), limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "invocations: %v", err)
	}
	return &invocationList{Invocations: toInvViews(invs)}, nil
}

// logs returns the last invocation's output, or its error when that run failed.
// Empty when the function has never run.
//
// Example: {"name": "resize"}
func (o ops) logs(ctx context.Context, in *functionRef) (*logsView, error) {
	orgID, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	invs, err := store.ListInvocations(ctx, orgID, strings.TrimSpace(in.Name), 1)
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
	return &logsView{Logs: out}, nil
}

// triggers lists the HTTP trigger of every function in the caller's org.
// Each function has exactly one: the POST endpoint that invokes it.
//
// Response: {"triggers": [{"id": "resize-http", "name": "HTTP", "type": "http", "enabled": true, "target": "/v1/functions/resize/invoke", "functionName": "resize"}]}
func (o ops) triggers(ctx context.Context, _ *struct{}) (*triggerList, error) {
	orgID, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := store.List(ctx, orgID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "triggers: %v", err)
	}
	out := make([]triggerView, 0, len(rows))
	for _, f := range rows {
		out = append(out, httpTrigger(f))
	}
	return &triggerList{Triggers: out}, nil
}

// deployments lists the live deployment of every function in the caller's org.
// Each function's current record IS its deployment, so this answers the same
// shape as the function list, without the invocation stats.
func (o ops) deployments(ctx context.Context, _ *struct{}) (*functionList, error) {
	orgID, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := store.List(ctx, orgID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "deployments: %v", err)
	}
	out := make([]functionView, 0, len(rows))
	for _, f := range rows {
		out = append(out, toView(o.s, f, InvStats{}))
	}
	return &functionList{Functions: out}, nil
}

type secretView struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	MountedBy string `json:"mountedBy,omitempty"`
}

// secrets lists the secret NAMES the caller org's functions mount, never a value.
// One entry per distinct (namespace, name), attributed to the function that
// mounts it. Values are never read or returned.
//
// Response: {"secrets": [{"name": "API_KEY", "namespace": "default", "mountedBy": "resize"}]}
func (o ops) secrets(ctx context.Context, _ *struct{}) (*secretList, error) {
	orgID, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := store.List(ctx, orgID)
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
