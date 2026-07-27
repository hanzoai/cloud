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
	"github.com/hanzoai/cloud/clients/analytics"
	"github.com/hanzoai/cloud/clients/flags"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/research"
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

	st, err := s.State.stores.For(org, project)
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
	st, err := s.State.stores.For(org, project)
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
	st, err := s.State.stores.For(org, project)
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
