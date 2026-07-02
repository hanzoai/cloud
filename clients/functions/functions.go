// Package functions mounts the Hanzo Cloud /v1/functions surface: a per-org
// serverless function registry. Every function belongs to exactly one org (the
// gateway-minted X-Org-Id, HIP-0026); tenant isolation is the org column,
// enforced on every query. The registry stores a function's runtime, source,
// resource limits, and the NAMES of the secrets it mounts — never a secret
// value (values live in KMS by reference, the Secret-Manager principle).
//
// Surface (the shape console2's FunctionsModule / functions.ts consume):
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
// binary NEVER runs tenant code in-process. When the sandbox is not configured
// invoke fails closed (503) and fabricates nothing. Every metric the Overview
// shows is DERIVED from real invocation rows; there is no invented rollup.
package functions

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
	luxlog "github.com/luxfi/log"
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

type svc struct {
	store *Store
	exec  *execClient
	log   luxlog.Logger
	// bill is the shared per-org resource gate+meter (reuses deps.Metering, the
	// one commerce client). Nil/!Enabled() makes Gate allow and Meter a no-op.
	bill *cloud.ResourceMeter
}

var mounted *svc

// ---- HTTP response shapes (console2 functions.ts contract) ----

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
func (s *svc) toView(f Function, st InvStats) functionView {
	v := functionView{
		Name: f.Name, Namespace: f.Namespace, Environment: f.Runtime, Status: f.Status,
		Image: f.Image, Endpoint: endpointFor(f.Name), EnvCount: len(f.EnvNames),
		TimeoutSec: f.TimeoutSec, MemoryLimit: f.MemoryLimit,
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

// Mount wires the functions surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("functions.Mount: nil zip.App")
	}
	log := deps.Logger
	if log == nil {
		return fmt.Errorf("functions.Mount: nil deps.Logger")
	}
	log = log.New("subsystem", "functions")
	if deps.DataDir == "" {
		return fmt.Errorf("functions.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("functions.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "functions.db"))
	if err != nil {
		return fmt.Errorf("functions.Mount: open store: %w", err)
	}
	s := &svc{store: store, exec: newExecClient(), log: log, bill: cloud.NewResourceMeter(deps, "functions")}
	mounted = s

	// Static sub-routes before the :name param route so a real function can
	// never shadow /metrics|/triggers|/deployments|/secrets.
	app.Get("/v1/functions", s.list)
	app.Post("/v1/functions", s.create)
	app.Get("/v1/functions/metrics", s.metrics)
	app.Get("/v1/functions/triggers", s.triggers)
	app.Get("/v1/functions/deployments", s.deployments)
	app.Get("/v1/functions/secrets", s.secrets)
	app.Get("/v1/functions/:name", s.get)
	app.Delete("/v1/functions/:name", s.del)
	app.Get("/v1/functions/:name/invocations", s.invocations)
	app.Get("/v1/functions/:name/logs", s.logs)
	app.Post("/v1/functions/:name/invoke", s.invoke)

	log.Info("functions mounted", "exec", s.exec.configured(), "brand", deps.Brand, "billing", s.bill.Enabled())
	return nil
}

func init() {
	cloud.Register("functions", 128, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("functions.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
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
	id, err := genID("fn")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	f := Function{
		ID: id, Org: org, Name: name, Namespace: sanitizeNs(body.Namespace), Runtime: runtime,
		Image: strings.TrimSpace(body.Image), Code: body.Code, Handler: strings.TrimSpace(body.Handler),
		TimeoutSec: timeout, MemoryLimit: mem, EnvNames: cleanList(body.EnvNames),
		Status: "ready", LastDeployAt: now,
	}
	saved, err := s.store.Upsert(c.Context(), f)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	return c.JSON(http.StatusCreated, s.toView(saved, InvStats{}))
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
	since := time.Now().Unix() - window7d
	out := make([]functionView, 0, len(rows))
	for _, f := range rows {
		st, err := s.store.StatsSince(c.Context(), org, f.Name, since)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "stats: %v", err)
		}
		out = append(out, s.toView(f, st))
	}
	return c.JSON(http.StatusOK, map[string]any{"functions": out})
}

func (s *svc) get(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name := nameParam(c)
	f, err := s.store.Get(c.Context(), org, name)
	if err == errNotFound {
		return zip.ErrNotFound("function not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	since := time.Now().Unix() - window7d
	st, _ := s.store.StatsSince(c.Context(), org, name, since)
	invs, err := s.store.ListInvocations(c.Context(), org, name, 20)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "invocations: %v", err)
	}
	return c.JSON(http.StatusOK, functionDetail{
		functionView:      s.toView(f, st),
		Triggers:          []triggerView{httpTrigger(f)},
		RecentInvocations: toInvViews(invs),
		Secrets:           nonNil(f.EnvNames),
	})
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
		return zip.ErrNotFound("function not found")
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *svc) invocations(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name := nameParam(c)
	limit := 100
	if q := strings.TrimSpace(c.Query("limit")); q != "" {
		if n, err := strconv.Atoi(q); err == nil {
			limit = n
		}
	}
	invs, err := s.store.ListInvocations(c.Context(), org, name, limit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "invocations: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"invocations": toInvViews(invs)})
}

func (s *svc) logs(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	invs, err := s.store.ListInvocations(c.Context(), org, nameParam(c), 1)
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

func (s *svc) triggers(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := s.store.List(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "triggers: %v", err)
	}
	out := make([]triggerView, 0, len(rows))
	for _, f := range rows {
		out = append(out, httpTrigger(f))
	}
	return c.JSON(http.StatusOK, map[string]any{"triggers": out})
}

func (s *svc) deployments(c *zip.Ctx) error {
	// Each function's current record IS its live deployment; return them as the
	// deployment inventory (console2 normalizes this as a function list).
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := s.store.List(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "deployments: %v", err)
	}
	out := make([]functionView, 0, len(rows))
	for _, f := range rows {
		out = append(out, s.toView(f, InvStats{}))
	}
	return c.JSON(http.StatusOK, map[string]any{"functions": out})
}

type secretView struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	MountedBy string `json:"mountedBy,omitempty"`
}

func (s *svc) secrets(c *zip.Ctx) error {
	// NAMES only — values are NEVER read or returned (Secret-Manager principle).
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := s.store.List(c.Context(), org)
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

// tenant resolves the org — the tenant isolation KEY. It uses c.Org() EXACTLY
// as SanitizeIdentity minted it from the validated IAM owner claim (HIP-0026):
// never lowercased/stripped/truncated. Normalizing would collapse distinct
// owners into one bucket — a cross-tenant break (Red HIGH-1). Reject only empty
// or pathologically long. No magic "admin" bucket.
func tenant(c *zip.Ctx) (string, bool) {
	org := strings.TrimSpace(c.Org())
	if org == "" || len(org) > 128 {
		return "", false
	}
	return org, true
}

// sanitizeNs normalizes the function NAMESPACE — a cosmetic grouping/display
// field the caller supplies, NOT the tenant isolation key (that is the org).
// Lossy normalization here is safe: the namespace never gates cross-tenant
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

// Shutdown closes the functions store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.store == nil {
		return nil
	}
	err := mounted.store.Close()
	mounted = nil
	return err
}
