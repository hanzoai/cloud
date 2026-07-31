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
		stores: cloud.NewOrgStore[*store](deps.DataDir, "experiments", openStore),
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

// ops binds the registry to the typed handlers: a TypedHandler takes only
// (context, *In), so the service arrives as a RECEIVER.
type ops struct{ s *cloud.Service[*state] }

func routes(app cloud.Router, s *cloud.Service[*state]) {
	z := cloud.ZipApp(app)
	if z == nil {
		panic("experiments.routes: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	o := ops{s: s}
	// The bridge FIRST — a typed op is handed only a context, so the request its
	// tenant, project scope and actor are read from is parked there. Bounded to the
	// subsystem's own prefix; Serve installs one app-wide too and nesting is harmless.
	app.Use(cloud.Bridge())
	// Static sub-routes register before the :id param route so a real experiment id
	// can never shadow a collection route.
	zip.Get(z, "/v1/experiments/health", o.health, opID("experimentsHealth"))
	zip.Post(z, "/v1/experiments", o.create, opID("createExperiment"), zip.WithStatus(http.StatusCreated))
	zip.Get(z, "/v1/experiments", o.list, opID("listExperiments"))
	zip.Get(z, "/v1/experiments/:id", o.get, opID("getExperiment"))
	zip.Get(z, "/v1/experiments/:id/assign", o.assign, opID("assignExperiment"))
	// POST /v1/experiments/:id/analyze stays an untyped handler: its window and alpha
	// ride the QUERY string, and zip declares query parameters only on a bodyless
	// method — a typed POST would publish them as a request body no caller sends.
	app.Post("/v1/experiments/:id/analyze", cloud.Handle(s, analyzeRoute))
	zip.Post(z, "/v1/experiments/:id/decide", o.decide, opID("decideExperiment"))
}

// opID is the per-route stable operation id — the name the OpenAPI document, the MCP
// tool and the CLI command all take. The summary is NOT set here: cmd/zipdoc lifts it
// from each handler's own doc comment.
func opID(id string) zip.OpOption { return zip.WithOperationID(id) }

// tenant resolves the org (the isolation KEY) from the validated principal and the
// project sub-scope — the ONE place tenant identity is derived, never a client field.
func tenant(c *zip.Ctx) (org, project string, ok bool) {
	org, ok = principal.Org(c)
	return org, principal.ProjectScope(c), ok
}

// scope is tenant() for a typed op: the request a typed handler cannot see is parked
// on its context by cloud.Bridge. Fails CLOSED off the HTTP path (the CLI projection
// carries no request, so there is no identity to derive an org from).
func scope(ctx context.Context) (*zip.Ctx, string, string, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, "", "", zip.ErrForbidden("X-Org-Id required")
	}
	org, project, ok := tenant(c)
	if !ok {
		return nil, "", "", zip.ErrForbidden("X-Org-Id required")
	}
	return c, org, project, nil
}

// Health is the subsystem's liveness answer.
type Health struct {
	// OK is true whenever the subsystem is mounted.
	OK bool `json:"ok"`
	// Subsystem names the subsystem that answered ("experiments").
	Subsystem string `json:"subsystem"`
}

// health reports that the experiments subsystem is mounted and serving. It reads no
// store and needs no identity.
//
// Response: {"ok": true, "subsystem": "experiments"}
func (o ops) health(ctx context.Context, _ *struct{}) (*Health, error) {
	return &Health{OK: true, Subsystem: "experiments"}, nil
}

// ── create ───────────────────────────────────────────────────────────────────

// createBody is the definition a caller submits to open an experiment.
type createBody struct {
	// ID is the experiment slug, unique within the org's project.
	ID string `json:"id"`
	// Name is the human label.
	Name string `json:"name"`
	// SubjectKind is the unit assigned and measured: user, org, session or
	// audience. Empty means user.
	SubjectKind SubjectKind `json:"subjectKind"`
	// FlagKey names the assignment flag; empty derives exp_<id>.
	FlagKey string `json:"flagKey"`
	// ExposureEvent is the enrollment marker; empty means $feature_flag_called.
	ExposureEvent string `json:"exposureEvent"`
	// MetricEvent is the conversion event. Required.
	MetricEvent string `json:"metricEvent"`
	// Variants is the arms; at least two, weights summing to 100 (all-zero
	// weights become an even split).
	Variants []Variant `json:"variants"`
}

// create opens an experiment and answers 201. It writes the multivariate ASSIGNMENT
// flag first, then the registry row. The project and the author are server-stamped
// from the validated principal. Fails closed if the flag write fails — an experiment
// with no assignment flag would assign nothing.
//
// Example: {"id": "checkout-copy", "name": "Checkout copy", "subjectKind": "user", "metricEvent": "purchase", "variants": [{"key": "control", "weight": 50, "control": true}, {"key": "bold", "weight": 50}]}
func (o ops) create(ctx context.Context, in *createBody) (*Experiment, error) {
	c, org, project, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	exp, err := normalize(*in, project, c.UserEmail())
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}

	st, err := o.s.State.stores.For(org, project)
	if err != nil {
		return nil, err
	}
	if _, found, err := st.get(ctx, project, exp.ID); err != nil {
		return nil, err
	} else if found {
		return nil, zip.Errorf(http.StatusConflict, "experiment %q already exists", exp.ID)
	}

	def, err := exp.flagDef()
	if err != nil {
		return nil, err
	}
	if err := flags.PutDef(org, project, exp.FlagKey, def, c.UserEmail()); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "register assignment flag: %v", err)
	}
	if err := st.create(ctx, exp); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create experiment: %v", err)
	}
	return &exp, nil
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

// ExperimentRef addresses one experiment by its path id.
type ExperimentRef struct {
	// ID is the experiment id from the path.
	ID string `json:"id"`
}

// get returns one of the caller org's experiments. An id belonging to another org
// reads as not found.
//
// Example: {"id": "checkout-copy"}
func (o ops) get(ctx context.Context, in *ExperimentRef) (*Experiment, error) {
	_, org, project, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	exp, found, err := loadExperiment(o.s, ctx, org, project, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, zip.ErrNotFound("experiment not found")
	}
	return &exp, nil
}

// ExperimentList is the caller's experiment registry.
type ExperimentList struct {
	// Data is every experiment in the caller's org and project scope.
	Data []Experiment `json:"data"`
	// Total is how many were returned.
	Total int `json:"total"`
}

// list returns every experiment in the caller's org and project scope.
func (o ops) list(ctx context.Context, _ *struct{}) (*ExperimentList, error) {
	_, org, project, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	st, err := o.s.State.stores.For(org, project)
	if err != nil {
		return nil, err
	}
	exps, err := st.list(ctx, project)
	if err != nil {
		return nil, err
	}
	return &ExperimentList{Data: exps, Total: len(exps)}, nil
}

// ── assign (composes flags) ────────────────────────────────────────────────────

// AssignQuery names the subject an assignment is resolved for.
type AssignQuery struct {
	// ID is the experiment id from the path.
	ID string `json:"id"`
	// Subject is the subject to assign — the distinct id the bucketing hashes.
	// Required.
	Subject string `json:"subject" validate:"required"`
	// Props is an optional JSON object of person properties the flag's targeting
	// reads. Invalid JSON is ignored.
	Props string `json:"props"`
}

// Assignment is one subject's arm in one experiment.
type Assignment struct {
	// Experiment is the experiment id that was resolved.
	Experiment string `json:"experiment"`
	// Subject is the subject the assignment was computed for.
	Subject string `json:"subject"`
	// Variant is the arm the subject falls in.
	Variant string `json:"variant"`
	// On is whether the assignment flag evaluated on for this subject.
	On bool `json:"on"`
	// Payload is the variant's opaque payload — a feature config, a creative id,
	// a model id. The experiment primitive never interprets it; null when the
	// variant carries none.
	Payload json.RawMessage `json:"payload"`
}

// assign resolves one subject's arm by evaluating the experiment's assignment flag.
// It is deterministic — the same subject always lands in the same variant — and reads
// nothing but the flag definition, so it never records an exposure.
//
// Example: {"id": "checkout-copy", "subject": "u_1", "props": "{\"plan\":\"pro\"}"}
// Response: {"experiment": "checkout-copy", "subject": "u_1", "variant": "bold", "on": true}
func (o ops) assign(ctx context.Context, in *AssignQuery) (*Assignment, error) {
	_, org, project, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	subject := strings.TrimSpace(in.Subject)
	if subject == "" {
		return nil, zip.ErrBadRequest("subject query parameter is required")
	}
	exp, found, err := loadExperiment(o.s, ctx, org, project, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, zip.ErrNotFound("experiment not found")
	}
	var props json.RawMessage
	if p := strings.TrimSpace(in.Props); p != "" && json.Valid([]byte(p)) {
		props = json.RawMessage(p)
	}
	a, err := flags.Assign(org, project, exp.FlagKey, subject, props)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "assign: %v", err)
	}
	return &Assignment{
		Experiment: exp.ID,
		Subject:    subject,
		Variant:    a.Variant,
		On:         a.On,
		Payload:    a.Payload,
	}, nil
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

// decideBody names the winning arm to promote.
type decideBody struct {
	// ID is the experiment id from the path.
	ID string `json:"id"`
	// Winner is the variant key to serve to 100% of the rollout. Required, and it
	// must be one of the experiment's variants.
	Winner string `json:"winner" validate:"required"`
}

// decide promotes a winning variant to the whole rollout. It rewrites the assignment
// flag so the winner serves 100% and every other arm 0%, then records the decision and
// returns the decided experiment. Requires own-org admin — the promotion IS a
// production flag write. An unknown experiment reads as not found before the admin
// check, so an id is never confirmed to a caller who cannot see it.
//
// Example: {"id": "checkout-copy", "winner": "bold"}
func (o ops) decide(ctx context.Context, in *decideBody) (*Experiment, error) {
	c, org, project, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	winner := strings.TrimSpace(in.Winner)
	if winner == "" {
		return nil, zip.ErrBadRequest("winner is required")
	}
	exp, found, err := loadExperiment(o.s, ctx, org, project, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, zip.ErrNotFound("experiment not found")
	}
	// Promoting a winner rewrites the assignment flag to serve one variant to 100%
	// of the org's users — a production behavior change. Gate on own-org admin
	// (parity with the flags/connector write plane), after the found-check so a
	// cross-tenant caller gets 404 (no existence leak) rather than 403.
	if !principal.IsOrgAdmin(c) {
		return nil, zip.ErrForbidden("promoting an experiment winner requires org admin")
	}
	if !exp.hasVariant(winner) {
		return nil, zip.ErrBadRequest(fmt.Sprintf("winner %q is not a variant of this experiment", winner))
	}

	def, ok, err := flags.GetDef(org, project, exp.FlagKey)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, zip.Errorf(http.StatusConflict, "assignment flag %q missing", exp.FlagKey)
	}
	promoted, err := promoteVariant(def, winner)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	if err := flags.PutDef(org, project, exp.FlagKey, promoted, c.UserEmail()); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "promote winner: %v", err)
	}
	st, err := o.s.State.stores.For(org, project)
	if err != nil {
		return nil, err
	}
	if err := st.decide(ctx, project, exp.ID, winner, c.UserEmail(), time.Now().UTC().Format(time.RFC3339)); err != nil {
		return nil, err
	}
	exp, _, _ = st.get(ctx, project, exp.ID)
	return &exp, nil
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
	st, err := mounted.stores.For(org, project)
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
	st, err := mounted.stores.For(org, project)
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
	st, err := s.State.stores.For(org, project)
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
