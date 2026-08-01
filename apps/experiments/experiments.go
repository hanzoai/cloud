package experiments

// experiments.go — the /v1/experiments surface and the composition that wires
// flags (assignment) + analytics (measurement) + research (evidence) into the one
// experiment lifecycle. This file is the ORCHESTRATION; the statistics (analyze.go),
// the registry (store.go), and the model (model.go) are the pieces it composes.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/analytics"
	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/research"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// evidenceKind is the research discriminator A/B analysis rows carry — a sibling of
// benchmark | kernel-perf | training | ablation | policy-eval.
const evidenceKind = "ab"

// defaultWindow is the look-back an analyze with no explicit window measures over.
const defaultWindow = 30 * 24 * time.Hour

// slugRE bounds an experiment id / flag key: a storage + flag key, so it must be a
// safe slug (no path/'/' tricks, no leading dot). Same shape flags keys use.
var slugRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// MetricOutcome is one subject's exposure + conversion for an experiment window —
// the experiments-package grain of a measurement, decoupled from the analytics wire
// type so the pure analysis core never imports the warehouse.
type MetricOutcome struct {
	Subject   string
	Exposed   bool
	Converted bool
}

// MetricSource is the measurement seam: per-subject outcomes for two event names
// over a window, org-scoped by the impl. Production is analyticsSource (the ONE
// analytics events plane); tests inject a fake. This is the only seam to the
// measurement half — there is no second event store.
type MetricSource interface {
	Outcomes(ctx context.Context, org, exposureEvent, metricEvent string, start, end time.Time) ([]MetricOutcome, error)
}

// analyticsSource composes clients/analytics.Outcomes — the events plane read,
// tenant-isolated by the eventsWhere invariant.
type analyticsSource struct{}

func (analyticsSource) Outcomes(ctx context.Context, org, exposureEvent, metricEvent string, start, end time.Time) ([]MetricOutcome, error) {
	rows, err := analytics.Outcomes(ctx, org, exposureEvent, metricEvent, start, end)
	if err != nil {
		return nil, err
	}
	out := make([]MetricOutcome, len(rows))
	for i, r := range rows {
		out[i] = MetricOutcome{Subject: r.Subject, Exposed: r.Exposed, Converted: r.Converted}
	}
	return out, nil
}

// state is the subsystem's own data: the per-org registry stores, the (swappable)
// measurement source, and the subsystem logger (so the composition's best-effort
// evidence write is never a silent failure).
type state struct {
	stores *cloud.OrgStore[*store]
	metric MetricSource
	log    luxlog.Logger
}

// mounted is the process-wide handle the in-process seams (Assign/Analyze that
// clients/campaign composes) reach the registry + measurement through — the same
// pattern flags exposes for admission.
var mounted *state

// storeFor is the ONE way this package reaches a store: it names the database
// through cloud.OrgNamespace — the single door a validated org walks through —
// and asks the registry for that name. Nothing else here resolves a store, so
// "which file does this request touch" has one answer from one input.
//
// org MUST already be validated: principal.Org for a request, or the caller's
// own server-side resolution for an in-process seam.
//
// experiments is project-scoped: the IAM project is a physical partition of the
// org, so it rides in the namespace rather than in a column.
func storeFor(s *cloud.Service[*state], org, project string) (*store, error) {
	ns, err := cloud.OrgNamespace(org, project)
	if err != nil {
		return nil, err
	}
	return s.State.stores.For(ns)
}

// mountedStore is storeFor for an in-process seam, whose caller has already
// resolved the org server-side and states that as its contract.
func mountedStore(org, project string) (*store, error) {
	if mounted == nil || mounted.stores == nil {
		return nil, fmt.Errorf("experiments: not mounted")
	}
	ns, err := cloud.OrgNamespace(org, project)
	if err != nil {
		return nil, err
	}
	return mounted.stores.For(ns)
}

// Mount opens the per-org registry stores, installs the process seam, and registers
// the /v1/experiments surface.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if deps.Logger == nil {
		return fmt.Errorf("experiments.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("experiments.Mount: empty deps.DataDir")
	}
	b := cloud.NewBase(deps, "experiments")
	mounted = &state{
		stores: cloud.NewOrgStore[*store](b, "experiments", openStore),
		metric: analyticsSource{},
		log:    b.Log,
	}
	// The Service holds the SAME *state as the process seam, so the /v1 handlers and
	// the in-process seams (Assign/Analyze) share one instance — one measurement
	// source, one registry.
	svc := &cloud.Service[*state]{Base: b, State: mounted}
	routes(app, svc)
	b.Log.Info("experiments mounted")
	return nil
}

// Shutdown closes every open per-org registry store.
func Shutdown() error {
	if mounted == nil || mounted.stores == nil {
		return nil
	}
	return mounted.stores.CloseAll()
}

func routes(app cloud.Router, s *cloud.Service[*state]) {
	g := app.Group("/v1/experiments")
	g.Get("/health", cloud.Handle(s, health)) // static before :id
	app.Post("/v1/experiments", cloud.Handle(s, create))
	app.Get("/v1/experiments", cloud.Handle(s, list))
	g.Get("/:id", cloud.Handle(s, get))
	g.Get("/:id/assign", cloud.Handle(s, assignRoute))
	g.Post("/:id/analyze", cloud.Handle(s, analyzeRoute))
	g.Post("/:id/decide", cloud.Handle(s, decideRoute))
}

// The prose for this surface, declared beside the wire fact above.
//
// A typed op's prose is lifted from its handler's doc comment by zipdoc; every
// route here is a raw cloud.Handle, so there is no typed op to lift from and
// openapi.Describe is the seam. Without it these seven publish an operationId and
// nothing else — an SDK method that cannot explain itself, an MCP tool with no
// description, a CLI command with no help. Keyed by the fiber pattern verbatim, so
// a description whose route is not in the router simply never renders and the
// document still cannot disagree with the route table.
func init() {
	openapi.Describe("/v1/experiments/health", http.MethodGet,
		"Whether the experiments subsystem is mounted and serving in this process.",
		"Answers {\"ok\":true,\"subsystem\":\"experiments\"} unconditionally. It proves exactly "+
			"one thing — that this binary registered the experiments routes and is dispatching "+
			"them — and deliberately no more: it reads no principal, opens no per-org registry, "+
			"and touches neither the flags engine nor the analytics plane, so a 200 here says "+
			"nothing about whether a given tenant's store will open or whether an analysis can "+
			"run. It is the only route on this surface that needs no org.\n\n"+
			"The static path is registered ahead of the /:id read, so it always wins the "+
			"first-match scan. `health` is a legal experiment id, which means an experiment "+
			"created under that id can never be fetched by id — pick another.")

	openapi.Describe("/v1/experiments", http.MethodPost,
		"Create a controlled experiment and put its assignment flag live.",
		"Registers the experiment AND writes its multivariate assignment flag, in that order, "+
			"so the arms start bucketing subjects the moment this returns 201 — the flag is "+
			"created active at 100% rollout, with each variant weighted as declared. There is no "+
			"separate start call; creating IS starting.\n\n"+
			"The body names the experiment (`id`, a slug that is claimed once), what it measures "+
			"(`metricEvent`, required; `exposureEvent` defaults to the SDK's "+
			"`$feature_flag_called` marker), the unit it assigns (`subjectKind`: user, org, "+
			"session or audience — user by default), and at least two `variants`. A variant "+
			"carries an opaque `payload` this primitive never interprets: a feature config, an "+
			"ad-creative id, a subject line, a model id. Weights that are all zero become an even "+
			"split; otherwise they must sum to 100. At most one variant may be flagged `control`; "+
			"with none, the first arm is the baseline. `flagKey` defaults to `exp_<id>`.\n\n"+
			"Requires a validated principal, and refuses without one. The org and project are "+
			"taken from that principal and the creator is stamped from the credential — none of "+
			"the three is a body field, so an experiment cannot be filed against another tenant. "+
			"An id already used in this project is a conflict, never a silent overwrite: "+
			"re-creating would stomp the assignment flag of a run in progress.\n\n"+
			"It fails closed on the flag write. An experiment whose assignment flag does not "+
			"exist would assign nobody, so if that write fails nothing is registered.")

	openapi.Describe("/v1/experiments", http.MethodGet,
		"Every experiment in the caller's org, with its variants, status and decision.",
		"Returns {data, total} ordered by project then id. Scoped to the org resolved from the "+
			"validated principal — a distinct org is a distinct physical store, so no query here "+
			"can reach another tenant's rows — and further narrowed to the caller's project scope "+
			"when the credential carries one. A principal with NO project scope sees the org's "+
			"experiments across all of its projects, which is the answer a reader most often "+
			"expects to be filtered and is not.\n\n"+
			"Requires a validated principal; refuses without one rather than answering an empty "+
			"list.")

	openapi.Describe("/v1/experiments/:id", http.MethodGet,
		"One experiment's definition and lifecycle: variants, weights, control arm, status and "+
			"winner.",
		"Reads the registry row only — the definition and the decision, never live measurements. "+
			"Assignment lives in the flags plane and outcomes in analytics; this is the value that "+
			"names both.\n\n"+
			"Scoped to the caller's org and project from the validated principal, so another "+
			"tenant's experiment of the same id is simply not found. An id that is not a legal "+
			"slug is answered the same way, without a store read — the shape check and the "+
			"existence check are one answer, so neither leaks the other.")

	openapi.Describe("/v1/experiments/:id/assign", http.MethodGet,
		"The variant one subject is bucketed into, and the payload that variant carries.",
		"Evaluates the experiment's assignment flag for the `subject` in the query and answers "+
			"{experiment, subject, variant, on, payload}. The bucketing is a deterministic hash of "+
			"the subject, so the same subject gets the same arm on every call for as long as the "+
			"flag definition is unchanged — and this is a pure READ: it records nothing. In "+
			"particular it does NOT record an exposure. The caller's SDK must emit the "+
			"experiment's exposure event itself, or the analysis has an empty denominator and "+
			"every arm measures zero.\n\n"+
			"`subject` is required. `props` may carry a JSON object of person properties for "+
			"targeting; a `props` value that is not valid JSON is dropped silently rather than "+
			"refused, so a malformed one changes the bucketing without saying so.\n\n"+
			"An empty `variant` with `on` false is not an error — it means the flag returned "+
			"nothing for this subject, so the subject is not enrolled. A flags engine that is "+
			"unavailable refuses rather than defaulting to an arm. Requires a validated "+
			"principal, and the experiment must exist in the caller's org and project.")

	openapi.Describe("/v1/experiments/:id/analyze", http.MethodPost,
		"Per-variant conversion, lift and statistical significance against the control arm.",
		"Reads per-subject outcomes from the analytics plane over a window, folds them into "+
			"per-variant samples, and returns each arm's exposed count, conversions, rate, lift "+
			"versus control, two-proportion z, two-tailed p-value and whether it clears alpha. "+
			"Arms with no data still appear with zero exposed, so the read is complete over the "+
			"experiment's declared arms; the control arm sorts first. The pooled-variance "+
			"estimator is used and the p-value is exact; a degenerate comparison (an empty arm, no "+
			"variance) answers z 0 and p 1 — not significant, never an error.\n\n"+
			"The window is `start`/`end` in RFC3339 if given, otherwise the last `days` (1 to 365, "+
			"30 by default) up to now. `alpha` overrides the 0.05 two-tailed threshold when it "+
			"lies strictly between 0 and 1; anything else leaves the default in place.\n\n"+
			"Only EXPOSED subjects are counted, and each is joined to its arm by re-evaluating "+
			"the assignment flag AT ANALYSIS TIME — not from what was in force during the window. "+
			"That is the one rule to get right: analyzing an experiment after its winner has been "+
			"promoted re-buckets every subject into the promoted arm, collapsing the control to "+
			"zero exposed and making the result meaningless. Read the analysis before deciding. A "+
			"subject the flag cannot place is dropped rather than allowed to poison the fold.\n\n"+
			"`winner` in the response is ADVISORY — the significant, control-beating arm with the "+
			"highest rate, or empty when inconclusive. It promotes nothing; the decision is a "+
			"separate, explicit act.\n\n"+
			"Every plane read is scoped to the caller's org. Per-variant samples are also written "+
			"to the research evidence plane as immutable `ab` rows, best-effort: the analysis is "+
			"still returned if that write fails, because the samples are recomputable, and the "+
			"failure is logged rather than swallowed.")

	openapi.Describe("/v1/experiments/:id/decide", http.MethodPost,
		"Promote one variant to the whole rollout and record who decided.",
		"Rewrites the assignment flag so the named `winner` serves 100% of the rollout and every "+
			"other arm 0%, preserving the flag's targeting groups and payloads, then stamps the "+
			"experiment decided with the winner, the deciding credential and the time. This is a "+
			"production behaviour change that takes effect immediately for every subject the flag "+
			"evaluates.\n\n"+
			"Requires an ORG ADMIN of the caller's own org — a stricter gate than the rest of "+
			"this surface, matching the flags write plane, because promoting is a flag write. The "+
			"admin check runs AFTER the experiment is found, so a caller from another tenant is "+
			"answered not-found rather than forbidden and learns nothing about what exists.\n\n"+
			"`winner` is required and must name one of the experiment's own variants. An "+
			"experiment whose assignment flag has gone missing is a conflict rather than a silent "+
			"no-op — there is nothing to promote.\n\n"+
			"Deciding is NOT terminal. A second call re-promotes a different variant and "+
			"re-stamps the row; the status stays decided and the previous winner is overwritten "+
			"with no record that it was ever chosen. Nothing here reverts the flag to its original "+
			"weights either, so an experiment cannot be un-decided through this route — restoring "+
			"a split means writing the flag definition back through the flags plane.")
}

// tenant resolves the org (the isolation KEY) from the validated principal and the
// project sub-scope — the ONE place tenant identity is derived, never a client field.
func tenant(c *zip.Ctx) (org, project string, ok bool) {
	org, ok = principal.Org(c)
	return org, principal.ProjectScope(c), ok
}

func health(s *cloud.Service[*state], c *zip.Ctx) error {
	return c.JSON(http.StatusOK, map[string]any{"ok": true, "subsystem": "experiments"})
}

// ── create ───────────────────────────────────────────────────────────────────

type createBody struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	SubjectKind   SubjectKind `json:"subjectKind"`
	FlagKey       string      `json:"flagKey"`
	ExposureEvent string      `json:"exposureEvent"`
	MetricEvent   string      `json:"metricEvent"`
	Variants      []Variant   `json:"variants"`
}

// create registers a new experiment: it writes the multivariate ASSIGNMENT flag
// (flags.PutDef) then the registry row. Project + identity are server-stamped. Fails
// closed if the flag write fails — an experiment with no assignment flag would assign
// nothing.
func create(s *cloud.Service[*state], c *zip.Ctx) error {
	org, project, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body createBody
	if err := c.Bind(&body); err != nil {
		return zip.ErrBadRequest("invalid experiment body")
	}
	exp, err := normalize(body, project, c.UserEmail())
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}

	st, err := storeFor(s, org, project)
	if err != nil {
		return err
	}
	if _, found, err := st.get(c.Context(), project, exp.ID); err != nil {
		return err
	} else if found {
		return zip.Errorf(http.StatusConflict, "experiment %q already exists", exp.ID)
	}

	def, err := exp.flagDef()
	if err != nil {
		return err
	}
	if err := flags.PutDef(org, project, exp.FlagKey, def, c.UserEmail()); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "register assignment flag: %v", err)
	}
	if err := st.create(c.Context(), exp); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "create experiment: %v", err)
	}
	return c.JSON(http.StatusCreated, exp)
}

// normalize validates + fills a create body into a stored Experiment. It is pure
// (no I/O), so the create-validation tests drive it directly.
func normalize(body createBody, project, actor string) (Experiment, error) {
	id := strings.TrimSpace(body.ID)
	if !slugRE.MatchString(id) {
		return Experiment{}, fmt.Errorf("id must be a slug [A-Za-z0-9][A-Za-z0-9._-]{0,127}")
	}
	if strings.TrimSpace(body.MetricEvent) == "" {
		return Experiment{}, fmt.Errorf("metricEvent is required")
	}
	kind := body.SubjectKind
	if kind == "" {
		kind = SubjectUser
	}
	if !kind.valid() {
		return Experiment{}, fmt.Errorf("subjectKind must be one of user|org|session|audience")
	}
	if len(body.Variants) < 2 {
		return Experiment{}, fmt.Errorf("an experiment needs at least 2 variants")
	}
	flagKey := strings.TrimSpace(body.FlagKey)
	if flagKey == "" {
		flagKey = "exp_" + id
	}
	if !slugRE.MatchString(flagKey) {
		return Experiment{}, fmt.Errorf("flagKey must be a slug")
	}
	variants, err := normalizeVariants(body.Variants)
	if err != nil {
		return Experiment{}, err
	}
	exposure := strings.TrimSpace(body.ExposureEvent)
	if exposure == "" {
		exposure = defaultExposureEvent
	}
	return Experiment{
		Project:       project,
		ID:            id,
		Name:          strings.TrimSpace(body.Name),
		SubjectKind:   kind,
		FlagKey:       flagKey,
		ExposureEvent: exposure,
		MetricEvent:   strings.TrimSpace(body.MetricEvent),
		Variants:      variants,
		Status:        StatusRunning,
		CreatedBy:     actor,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// normalizeVariants validates variant keys (unique, slug-safe) and weights: all-zero
// weights become an even split; otherwise the weights must sum to ~100. At most one
// variant may be flagged control; when none is, the first arm is the baseline.
func normalizeVariants(in []Variant) ([]Variant, error) {
	seen := map[string]bool{}
	controls := 0
	var sum float64
	for _, v := range in {
		key := strings.TrimSpace(v.Key)
		if !slugRE.MatchString(key) {
			return nil, fmt.Errorf("variant key %q must be a slug", v.Key)
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate variant key %q", key)
		}
		seen[key] = true
		if v.Weight < 0 {
			return nil, fmt.Errorf("variant %q weight must be >= 0", key)
		}
		sum += v.Weight
		if v.Control {
			controls++
		}
	}
	if controls > 1 {
		return nil, fmt.Errorf("at most one variant may be the control")
	}
	out := make([]Variant, len(in))
	for i, v := range in {
		out[i] = Variant{Key: strings.TrimSpace(v.Key), Weight: v.Weight, Control: v.Control, Payload: v.Payload}
	}
	if sum == 0 {
		even := round4(100.0 / float64(len(out)))
		for i := range out {
			out[i].Weight = even
		}
	} else if sum < 99.9 || sum > 100.1 {
		return nil, fmt.Errorf("variant weights must sum to 100 (got %.2f)", sum)
	}
	return out, nil
}

// flagDef builds the PostHog-compatible multivariate flag definition the native
// evaluator consumes: a 100%-rollout group (targeting is a follow-on) + the variants
// as weighted multivariate arms + per-variant payloads. The variant KIND is
// irrelevant here — the payload is opaque JSON.
func (e Experiment) flagDef() (json.RawMessage, error) {
	variants := make([]map[string]any, 0, len(e.Variants))
	payloads := map[string]json.RawMessage{}
	for _, v := range e.Variants {
		variants = append(variants, map[string]any{"key": v.Key, "rollout_percentage": v.Weight})
		if len(v.Payload) > 0 {
			payloads[v.Key] = v.Payload
		}
	}
	def := map[string]any{
		"key":    e.FlagKey,
		"active": true,
		"filters": map[string]any{
			"groups":       []map[string]any{{"properties": []any{}, "rollout_percentage": 100}},
			"multivariate": map[string]any{"variants": variants},
			"payloads":     payloads,
		},
	}
	return json.Marshal(def)
}

// ── read ─────────────────────────────────────────────────────────────────────

func get(s *cloud.Service[*state], c *zip.Ctx) error {
	org, project, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	exp, found, err := loadExperiment(s, c.Context(), org, project, idParam(c))
	if err != nil {
		return err
	}
	if !found {
		return zip.ErrNotFound("experiment not found")
	}
	return c.JSON(http.StatusOK, exp)
}

func list(s *cloud.Service[*state], c *zip.Ctx) error {
	org, project, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	st, err := storeFor(s, org, project)
	if err != nil {
		return err
	}
	exps, err := st.list(c.Context(), project)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"data": exps, "total": len(exps)})
}

// ── assign (composes flags) ────────────────────────────────────────────────────

func assignRoute(s *cloud.Service[*state], c *zip.Ctx) error {
	org, project, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	subject := strings.TrimSpace(c.Query("subject"))
	if subject == "" {
		return zip.ErrBadRequest("subject query parameter is required")
	}
	exp, found, err := loadExperiment(s, c.Context(), org, project, idParam(c))
	if err != nil {
		return err
	}
	if !found {
		return zip.ErrNotFound("experiment not found")
	}
	var props json.RawMessage
	if p := strings.TrimSpace(c.Query("props")); p != "" && json.Valid([]byte(p)) {
		props = json.RawMessage(p)
	}
	a, err := flags.Assign(org, project, exp.FlagKey, subject, props)
	if err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "assign: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"experiment": exp.ID,
		"subject":    subject,
		"variant":    a.Variant,
		"on":         a.On,
		"payload":    a.Payload,
	})
}

// ── analyze (composes analytics + flags + research) ────────────────────────────

func analyzeRoute(s *cloud.Service[*state], c *zip.Ctx) error {
	org, project, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	exp, found, err := loadExperiment(s, c.Context(), org, project, idParam(c))
	if err != nil {
		return err
	}
	if !found {
		return zip.ErrNotFound("experiment not found")
	}
	start, end := windowFrom(c)
	alpha := alphaFrom(c)
	res, err := runAnalyze(s.State, c.Context(), org, exp, start, end, alpha)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "analyze: %v", err)
	}
	return c.JSON(http.StatusOK, res)
}

// runAnalyze is the analysis composition: read per-subject outcomes from the
// measurement plane, fold them per variant by joining each subject to its flags
// assignment, persist the per-variant samples to the research evidence plane, and
// return the computed lift + significance. Pure statistics; impure only at the three
// plane reads/writes, each org-scoped.
func runAnalyze(st *state, ctx context.Context, org string, exp Experiment, start, end time.Time, alpha float64) (Analysis, error) {
	outcomes, err := st.metric.Outcomes(ctx, org, exp.ExposureEvent, exp.MetricEvent, start, end)
	if err != nil {
		return Analysis{}, err
	}
	assign := func(subject string) (string, error) {
		a, err := flags.Assign(org, exp.Project, exp.FlagKey, subject, nil)
		if err != nil {
			return "", err
		}
		return a.Variant, nil
	}
	samples := foldOutcomes(outcomes, assign)
	analysis := computeAnalysis(exp, samples, alpha)

	// Persist per-variant samples as immutable research evidence (kind "ab"), so the
	// R&D board renders them and the OLAP plane rolls them up — the evidence of
	// record. Best-effort: the analysis is returned even if the evidence plane is
	// momentarily unavailable (the samples are recomputable), but the failure is
	// logged — never silent.
	if err := research.Record(ctx, org, exp.Project, evidenceRows(exp, analysis, end)); err != nil && st.log != nil {
		st.log.Warn("experiment evidence write skipped; analysis stands (samples recomputable)",
			"org", org, "experiment", exp.ID, "err", err)
	}
	return analysis, nil
}

// evidenceRows maps an analysis into research Experiment rows — one per variant,
// keyed <expID>:<variant>, kind "ab". value is the conversion rate, n the exposed
// sample size; the pairwise stats (lift, z, p, significance) ride meta. The
// experiment's own project scopes them so the org's R&D board groups them.
func evidenceRows(exp Experiment, a Analysis, at time.Time) []research.Experiment {
	rows := make([]research.Experiment, 0, len(a.Results))
	for _, r := range a.Results {
		meta, _ := json.Marshal(map[string]any{
			"experiment":  exp.ID,
			"variant":     r.Variant,
			"control":     r.Control,
			"converted":   r.Converted,
			"lift":        r.Lift,
			"z":           r.Z,
			"pValue":      r.PValue,
			"significant": r.Significant,
			"flagKey":     exp.FlagKey,
		})
		rows = append(rows, research.Experiment{
			ID:      exp.ID + ":" + r.Variant,
			Kind:    evidenceKind,
			Subject: r.Variant,
			Task:    exp.ID,
			Metric:  exp.MetricEvent,
			Value:   r.Rate,
			N:       r.Exposed,
			Status:  "complete",
			Meta:    json.RawMessage(meta),
			TS:      at.UTC().Unix(),
		})
	}
	return rows
}

// ── decide (composes flags) ────────────────────────────────────────────────────

type decideBody struct {
	Winner string `json:"winner"`
}

// decideRoute promotes a winner: it rewrites the assignment flag so the winning
// variant serves 100% of the rollout (others 0%), then records the decision. Same
// authorization as any flag write (a validated principal in this org+project) — the
// promotion IS a flag write.
func decideRoute(s *cloud.Service[*state], c *zip.Ctx) error {
	org, project, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body decideBody
	if err := c.Bind(&body); err != nil {
		return zip.ErrBadRequest("invalid decide body")
	}
	winner := strings.TrimSpace(body.Winner)
	if winner == "" {
		return zip.ErrBadRequest("winner is required")
	}
	exp, found, err := loadExperiment(s, c.Context(), org, project, idParam(c))
	if err != nil {
		return err
	}
	if !found {
		return zip.ErrNotFound("experiment not found")
	}
	// Promoting a winner rewrites the assignment flag to serve one variant to 100%
	// of the org's users — a production behavior change. Gate on own-org admin
	// (parity with the flags/connector write plane), after the found-check so a
	// cross-tenant caller gets 404 (no existence leak) rather than 403.
	if !principal.IsOrgAdmin(c) {
		return zip.ErrForbidden("promoting an experiment winner requires org admin")
	}
	if !exp.hasVariant(winner) {
		return zip.ErrBadRequest(fmt.Sprintf("winner %q is not a variant of this experiment", winner))
	}

	def, ok, err := flags.GetDef(org, project, exp.FlagKey)
	if err != nil {
		return err
	}
	if !ok {
		return zip.Errorf(http.StatusConflict, "assignment flag %q missing", exp.FlagKey)
	}
	promoted, err := promoteVariant(def, winner)
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	if err := flags.PutDef(org, project, exp.FlagKey, promoted, c.UserEmail()); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "promote winner: %v", err)
	}
	st, err := storeFor(s, org, project)
	if err != nil {
		return err
	}
	if err := st.decide(c.Context(), project, exp.ID, winner, c.UserEmail(), time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	exp, _, _ = st.get(c.Context(), project, exp.ID)
	return c.JSON(http.StatusOK, exp)
}

// promoteVariant rewrites a flag definition's multivariate weights so winner serves
// 100% and every other arm 0%, preserving groups, payloads, and any other fields
// (read-modify-write over the generic JSON). It errors if winner is not one of the
// flag's variants.
func promoteVariant(def json.RawMessage, winner string) (json.RawMessage, error) {
	var m map[string]any
	if err := json.Unmarshal(def, &m); err != nil {
		return nil, fmt.Errorf("assignment flag is not a JSON object")
	}
	filters, _ := m["filters"].(map[string]any)
	if filters == nil {
		return nil, fmt.Errorf("assignment flag has no filters")
	}
	mv, _ := filters["multivariate"].(map[string]any)
	if mv == nil {
		return nil, fmt.Errorf("assignment flag has no multivariate variants")
	}
	variants, _ := mv["variants"].([]any)
	found := false
	for _, raw := range variants {
		vm, _ := raw.(map[string]any)
		if vm == nil {
			continue
		}
		if vm["key"] == winner {
			vm["rollout_percentage"] = float64(100)
			found = true
		} else {
			vm["rollout_percentage"] = float64(0)
		}
	}
	if !found {
		return nil, fmt.Errorf("winner %q is not a variant of the flag", winner)
	}
	return json.Marshal(m)
}

// ── in-process seams (clients/campaign composes these) ─────────────────────────

// Assign is the in-process assignment seam: resolve the experiment's flag and return
// the subject's variant + payload. clients/campaign composes THIS to pick a creative
// (variant.payload) per subject — it never reinvents the bucketing. Org-scoped and
// fail-closed (an unknown experiment is an error, not a silent default).
func Assign(ctx context.Context, org, project, experimentID, subject string, props json.RawMessage) (flags.Assignment, error) {
	if mounted == nil || mounted.stores == nil {
		return flags.Assignment{}, fmt.Errorf("experiments: not mounted")
	}
	st, err := mountedStore(org, project)
	if err != nil {
		return flags.Assignment{}, err
	}
	exp, found, err := st.get(ctx, project, experimentID)
	if err != nil {
		return flags.Assignment{}, err
	}
	if !found {
		return flags.Assignment{}, fmt.Errorf("experiments: unknown experiment %q", experimentID)
	}
	return flags.Assign(org, project, exp.FlagKey, subject, props)
}

// Analyze is the in-process analysis seam (campaign's scheduler / a cron composes it
// to refresh an experiment's significance): resolve the experiment, then run the
// full analytics x flags x research analysis over [start,end).
func Analyze(ctx context.Context, org, project, experimentID string, start, end time.Time, alpha float64) (Analysis, error) {
	if mounted == nil || mounted.stores == nil {
		return Analysis{}, fmt.Errorf("experiments: not mounted")
	}
	st, err := mountedStore(org, project)
	if err != nil {
		return Analysis{}, err
	}
	exp, found, err := st.get(ctx, project, experimentID)
	if err != nil {
		return Analysis{}, err
	}
	if !found {
		return Analysis{}, fmt.Errorf("experiments: unknown experiment %q", experimentID)
	}
	return runAnalyze(mounted, ctx, org, exp, start, end, alpha)
}

// ── helpers ────────────────────────────────────────────────────────────────────

func idParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("id")) }

func loadExperiment(s *cloud.Service[*state], ctx context.Context, org, project, id string) (Experiment, bool, error) {
	if !slugRE.MatchString(id) {
		return Experiment{}, false, nil
	}
	st, err := storeFor(s, org, project)
	if err != nil {
		return Experiment{}, false, err
	}
	return st.get(ctx, project, id)
}

// windowFrom resolves [start,end): explicit ?start/?end (RFC3339) win, else the last
// ?days (default 30) up to now.
func windowFrom(c *zip.Ctx) (time.Time, time.Time) {
	end := time.Now().UTC()
	if e := strings.TrimSpace(c.Query("end")); e != "" {
		if t, err := time.Parse(time.RFC3339, e); err == nil {
			end = t.UTC()
		}
	}
	if s := strings.TrimSpace(c.Query("start")); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.UTC(), end
		}
	}
	days := defaultWindow
	if d := strings.TrimSpace(c.Query("days")); d != "" {
		if n, err := strconv.Atoi(d); err == nil && n > 0 && n <= 365 {
			days = time.Duration(n) * 24 * time.Hour
		}
	}
	return end.Add(-days), end
}

func alphaFrom(c *zip.Ctx) float64 {
	if a := strings.TrimSpace(c.Query("alpha")); a != "" {
		if f, err := strconv.ParseFloat(a, 64); err == nil && f > 0 && f < 1 {
			return f
		}
	}
	return defaultAlpha
}
