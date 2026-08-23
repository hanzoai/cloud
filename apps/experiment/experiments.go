package experiment

// experiments.go — the /v1/experiment surface and the composition that wires
// flags (assignment) + analytics (measurement) + research (evidence) into the one
// experiment lifecycle. This file is the ORCHESTRATION; the statistics (analyze.go),
// the registry (store.go), and the model (model.go) are the pieces it composes.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/event"
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

// MetricSource is the measurement client: per-subject outcomes for two event names
// over a window, org-scoped by the impl. Production is analyticsSource (the ONE
// analytics events plane); tests inject a fake. This is the only client to the
// measurement half — there is no second event store.
type MetricSource interface {
	Outcomes(ctx context.Context, org, exposureEvent, metricEvent string, start, end time.Time) ([]MetricOutcome, error)
}

// analyticsSource composes clients/event.Outcomes — the events plane read,
// tenant-isolated by the eventsWhere invariant.
type analyticsSource struct{}

func (analyticsSource) Outcomes(ctx context.Context, org, exposureEvent, metricEvent string, start, end time.Time) ([]MetricOutcome, error) {
	rows, err := event.Outcomes(ctx, org, exposureEvent, metricEvent, start, end)
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

// mounted is the process-wide handle the in-process clients (Assign/Analyze that
// clients/campaign composes) reach the registry + measurement through — the same
// pattern flags exposes for admission.
var mounted *state

// storeFor is the ONE way this package reaches a store: it names the database
// through cloud.OrgNamespace — the single entry point a validated org resolves
// through — and asks the registry for that name. Nothing else here resolves a
// store, so "which file does this request touch" has one answer from one input.
//
// org MUST already be validated: principal.Org for a request, or the caller's
// own server-side resolution for an in-process client.
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

// mountedStore is storeFor for an in-process client, whose caller has already
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

// Mount opens the per-org registry stores, installs the process client, and registers
// the /v1/experiment surface.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if deps.DataDir == "" {
		return fmt.Errorf("experiment.Mount: empty deps.DataDir")
	}
	b := cloud.NewBase(deps, "experiment")
	mounted = &state{
		stores: cloud.NewOrgStore[*store](b, "experiments", openStore),
		metric: analyticsSource{},
		log:    b.Log,
	}
	// The Service holds the SAME *state as the process client, so the /v1 handlers and
	// the in-process clients (Assign/Analyze) share one instance — one measurement
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

// routes declares the seven ops of this surface, all typed: the input and the
// answer are Go types, so the schema, the prose, the MCP tool, the CLI command
// and every generated SDK method are projections of the handler itself.
//
// zipdoc lifts the doc comment off each op and its In/Out fields into
// zipdoc_gen.go, which is the only way prose reaches the published registry: Go
// drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc
func routes(app cloud.Router, s *cloud.Service[*state]) {
	o := ops{s: s}
	za := cloud.ZipApp(app)
	g := app.Group("/v1/experiment")

	zip.Get(g, "/health", o.health) // static before :id
	zip.Post(za, "/v1/experiment", o.create, zip.WithStatus(http.StatusCreated))
	zip.Get(za, "/v1/experiment", o.list)
	zip.Get(g, "/:id", o.get)
	zip.Get(g, "/:id/assign", o.assign)
	zip.Post(g, "/:id/analyze", o.analyze)
	zip.Post(g, "/:id/decide", o.decide)
}

// ops binds the service to the typed ops. A TypedHandler takes no service
// parameter, so the service arrives as a RECEIVER and every op is a method value
// — also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[*state] }

// tenant resolves the org (the isolation KEY) and the project sub-scope from the
// validated principal cloud.Bridge parked — the ONE place tenant identity is
// derived, never a client field. Fails closed: no validated principal, no tenant.
func tenant(ctx context.Context) (org, project string, err error) {
	org, err = principal.Acting(ctx)
	if err != nil {
		return "", "", err
	}
	project = principal.ProjectFrom(ctx)
	if principal.IsDefaultProject(project) {
		project = ""
	}
	return org, project, nil
}

// actor is the credential's email — what a create or a decision is stamped with.
// It needs the REQUEST rather than the tenant, because the identity lives in a
// header principal.OrgFrom does not carry. Empty off the HTTP path.
func actorOf(ctx context.Context) string {
	c, ok := cloud.Request(ctx)
	if !ok {
		return ""
	}
	return c.UserEmail()
}

// health is the answer to the liveness read.
type health struct {
	// OK is true whenever this route answers at all: reaching the handler IS the
	// proof that the routes are registered and dispatching.
	OK bool `json:"ok"`
	// Subsystem names what answered, so a health response read out of context still
	// says which surface it came from.
	Subsystem string `json:"subsystem"`
}

// noIn is the input of an op that takes nothing: no body, no path parameter, no
// query.
type noIn struct{}

// health is whether the experiments subsystem is mounted and serving in this
// process.
//
// It answers unconditionally. It proves exactly one thing — that this binary
// registered the experiments routes and is dispatching them — and deliberately no
// more: it reads no principal, opens no per-org registry, and touches neither the
// flags engine nor the analytics plane, so a 200 here says nothing about whether a
// given tenant's store will open or whether an analysis can run. It is the only
// route on this surface that needs no org.
//
// The static path is registered ahead of the /:id read, so it always wins the
// first-match scan. "health" is a legal experiment id, which means an experiment
// created under that id can never be fetched by id — pick another.
func (o ops) health(ctx context.Context, _ *noIn) (*health, error) {
	return &health{OK: true, Subsystem: "experiments"}, nil
}

// ── create ───────────────────────────────────────────────────────────────────

// createBody declares a controlled experiment: what it measures, the unit it
// assigns, and the arms it splits between.
type createBody struct {
	// ID is the experiment's slug, claimed once per project. It must match
	// [A-Za-z0-9][A-Za-z0-9._-]{0,127}.
	ID string `json:"id" validate:"required"`
	// Name is free text for a reader; the id is what addresses the experiment.
	Name string `json:"name"`
	// SubjectKind is the unit assigned and measured: user (the default), org,
	// session or audience.
	SubjectKind SubjectKind `json:"subjectKind"`
	// FlagKey names the assignment flag this experiment writes, defaulting to
	// exp_<id>. It must be a slug.
	FlagKey string `json:"flagKey"`
	// ExposureEvent is the event that marks a subject as enrolled — the analysis
	// denominator — defaulting to the SDK's $feature_flag_called marker.
	ExposureEvent string `json:"exposureEvent"`
	// MetricEvent is the event that counts as a conversion — the analysis
	// numerator.
	MetricEvent string `json:"metricEvent" validate:"required"`
	// Arms are the arms, at least two. Weights that are all zero become an
	// even split; otherwise they must sum to 100, and at most one arm may be
	// flagged control.
	Arms []Arm `json:"variants" validate:"required"`
}

// experimentRef addresses one of the caller's experiments.
type experimentRef struct {
	// ID is the experiment the URL names.
	ID string `json:"id"`
}

// experimentList is the answer to an experiment listing.
type experimentList struct {
	// Data is the org's experiments, ordered by project then id.
	Data []Trial `json:"data"`
	// Total is how many rows Data holds.
	Total int `json:"total"`
}

// create registers a controlled experiment AND puts its assignment flag live, in
// that order, so the arms start bucketing subjects the moment this returns 201 —
// the flag is created active at 100% rollout, with each variant weighted as
// declared. There is no separate start call; creating IS starting.
//
// A variant carries an opaque payload this primitive never interprets: a feature
// config, an ad-creative id, a subject line, a model id.
//
// Requires a validated principal, and refuses without one. The org and project are
// taken from that principal and the creator is stamped from the credential — none
// of the three is a body field, so an experiment cannot be filed against another
// tenant. An id already used in this project is a conflict, never a silent
// overwrite: re-creating would stomp the assignment flag of a run in progress.
//
// It fails closed on the flag write. An experiment whose assignment flag does not
// exist would assign nobody, so if that write fails nothing is registered.
func (o ops) create(ctx context.Context, in *createBody) (*Trial, error) {
	org, project, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	actor := actorOf(ctx)
	exp, err := normalize(*in, project, actor)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}

	st, err := storeFor(o.s, org, project)
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
	if err := flags.PutDef(org, project, exp.FlagKey, def, actor); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "register assignment flag: %v", err)
	}
	if err := st.create(ctx, exp); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create experiment: %v", err)
	}
	return &exp, nil
}

// normalize validates + fills a create body into a stored Trial. It is pure
// (no I/O), so the create-validation tests drive it directly.
func normalize(body createBody, project, actor string) (Trial, error) {
	id := strings.TrimSpace(body.ID)
	if !slugRE.MatchString(id) {
		return Trial{}, fmt.Errorf("id must be a slug [A-Za-z0-9][A-Za-z0-9._-]{0,127}")
	}
	if strings.TrimSpace(body.MetricEvent) == "" {
		return Trial{}, fmt.Errorf("metricEvent is required")
	}
	kind := body.SubjectKind
	if kind == "" {
		kind = SubjectUser
	}
	if !kind.valid() {
		return Trial{}, fmt.Errorf("subjectKind must be one of user|org|session|audience")
	}
	if len(body.Arms) < 2 {
		return Trial{}, fmt.Errorf("an experiment needs at least 2 variants")
	}
	flagKey := strings.TrimSpace(body.FlagKey)
	if flagKey == "" {
		flagKey = "exp_" + id
	}
	if !slugRE.MatchString(flagKey) {
		return Trial{}, fmt.Errorf("flagKey must be a slug")
	}
	variants, err := normalizeVariants(body.Arms)
	if err != nil {
		return Trial{}, err
	}
	exposure := strings.TrimSpace(body.ExposureEvent)
	if exposure == "" {
		exposure = defaultExposureEvent
	}
	return Trial{
		Project:       project,
		ID:            id,
		Name:          strings.TrimSpace(body.Name),
		SubjectKind:   kind,
		FlagKey:       flagKey,
		ExposureEvent: exposure,
		MetricEvent:   strings.TrimSpace(body.MetricEvent),
		Arms:          variants,
		Status:        StatusRunning,
		CreatedBy:     actor,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// normalizeVariants validates variant keys (unique, slug-safe) and weights: all-zero
// weights become an even split; otherwise the weights must sum to ~100. At most one
// variant may be flagged control; when none is, the first arm is the baseline.
func normalizeVariants(in []Arm) ([]Arm, error) {
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
	out := make([]Arm, len(in))
	for i, v := range in {
		out[i] = Arm{Key: strings.TrimSpace(v.Key), Weight: v.Weight, Control: v.Control, Payload: v.Payload}
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
func (e Trial) flagDef() (json.RawMessage, error) {
	variants := make([]map[string]any, 0, len(e.Arms))
	payloads := map[string]json.RawMessage{}
	for _, v := range e.Arms {
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

// get is one experiment's definition and lifecycle: variants, weights, control
// arm, status and winner.
//
// It reads the registry row only — the definition and the decision, never live
// measurements. Assignment lives in the flags plane and outcomes in analytics;
// this is the value that names both.
//
// Scoped to the caller's org and project from the validated principal, so another
// tenant's experiment of the same id is simply not found. An id that is not a
// legal slug is answered the same way, without a store read — the shape check and
// the existence check are one answer, so neither leaks the other.
func (o ops) get(ctx context.Context, in *experimentRef) (*Trial, error) {
	org, project, err := tenant(ctx)
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

// list is every experiment in the caller's org, with its variants, status and
// decision, ordered by project then id.
//
// Scoped to the org resolved from the validated principal — a distinct org is a
// distinct physical store, so no query here can reach another tenant's rows — and
// further narrowed to the caller's project scope when the credential carries one.
// A principal with NO project scope sees the org's experiments across all of its
// projects, which is the answer a reader most often expects to be filtered and is
// not.
//
// Requires a validated principal; refuses without one rather than answering an
// empty list.
func (o ops) list(ctx context.Context, _ *noIn) (*experimentList, error) {
	org, project, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	st, err := storeFor(o.s, org, project)
	if err != nil {
		return nil, err
	}
	exps, err := st.list(ctx, project)
	if err != nil {
		return nil, err
	}
	return &experimentList{Data: exps, Total: len(exps)}, nil
}

// ── assign (composes flags) ────────────────────────────────────────────────────

// assignQuery asks which arm one subject is bucketed into.
type assignQuery struct {
	// ID is the experiment the URL names.
	ID string `json:"id"`
	// Subject is the unit to bucket — a user, org, session or audience key,
	// matching the experiment's subjectKind.
	Subject string `json:"subject" validate:"required"`
	// Props is a JSON object of person properties for targeting. A value that is
	// not valid JSON is dropped rather than refused, so a malformed one changes
	// the bucketing without saying so.
	Props string `json:"props"`
}

// assignment is the arm one subject got, and what that arm carries.
type assignment struct {
	// Trial is the experiment that was evaluated.
	Trial string `json:"experiment"`
	// Subject is the unit that was bucketed.
	Subject string `json:"subject"`
	// Arm is the arm the subject falls in, empty when the flag enrolled it in
	// none.
	Arm string `json:"variant"`
	// On is false when the flag returned nothing for this subject, which means the
	// subject is not enrolled — not an error.
	On bool `json:"on"`
	// Payload is the opaque JSON the winning arm carries, which the caller
	// interprets: a feature config, an ad-creative id, a subject line, a model id.
	Payload json.RawMessage `json:"payload"`
}

// assign is the variant one subject is bucketed into, and the payload that
// variant carries.
//
// The bucketing is a deterministic hash of the subject, so the same subject gets
// the same arm on every call for as long as the flag definition is unchanged —
// and this is a pure READ: it records nothing. In particular it does NOT record an
// exposure. The caller's SDK must emit the experiment's exposure event itself, or
// the analysis has an empty denominator and every arm measures zero.
//
// An empty variant with on false is not an error — it means the flag returned
// nothing for this subject, so the subject is not enrolled. A flags engine that is
// unavailable refuses rather than defaulting to an arm. Requires a validated
// principal, and the experiment must exist in the caller's org and project.
func (o ops) assign(ctx context.Context, in *assignQuery) (*assignment, error) {
	org, project, err := tenant(ctx)
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
	return &assignment{
		Trial: exp.ID, Subject: subject,
		Arm: a.Variant, On: a.On, Payload: a.Payload,
	}, nil
}

// ── analyze (composes analytics + flags + research) ────────────────────────────

// analyzeQuery bounds an analysis: the window it reads and the significance
// threshold it judges by.
type analyzeQuery struct {
	// ID is the experiment the URL names.
	ID string `json:"id"`
	// Start is the window's inclusive start in RFC3339. Given, it wins over days.
	Start string `json:"start"`
	// End is the window's exclusive end in RFC3339, defaulting to now.
	End string `json:"end"`
	// Days is how far back to read when no start is given: 1 to 365, 30 by
	// default. A value outside that range leaves the default in place.
	Days int `json:"days"`
	// Alpha overrides the 0.05 two-tailed significance threshold when it lies
	// strictly between 0 and 1; anything else leaves the default in place.
	Alpha float64 `json:"alpha"`
}

// analyze is per-variant conversion, lift and statistical significance against
// the control arm.
//
// It reads per-subject outcomes from the analytics plane over a window, folds them
// into per-variant samples, and returns each arm's exposed count, conversions,
// rate, lift versus control, two-proportion z, two-tailed p-value and whether it
// clears alpha. Arms with no data still appear with zero exposed, so the read is
// complete over the experiment's declared arms; the control arm sorts first. The
// pooled-variance estimator is used and the p-value is exact; a degenerate
// comparison (an empty arm, no variance) answers z 0 and p 1 — not significant,
// never an error.
//
// Only EXPOSED subjects are counted, and each is joined to its arm by re-evaluating
// the assignment flag AT ANALYSIS TIME — not from what was in force during the
// window. That is the one rule to get right: analyzing an experiment after its
// winner has been promoted re-buckets every subject into the promoted arm,
// collapsing the control to zero exposed and making the result meaningless. Read
// the analysis before deciding. A subject the flag cannot place is dropped rather
// than allowed to poison the fold.
//
// The winner in the response is ADVISORY — the significant, control-beating arm
// with the highest rate, or empty when inconclusive. It promotes nothing; the
// decision is a separate, explicit act.
//
// Every plane read is scoped to the caller's org. Per-variant samples are also
// written to the research evidence plane as immutable ab rows, best-effort: the
// analysis is still returned if that write fails, because the samples are
// recomputable, and the failure is logged rather than swallowed.
func (o ops) analyze(ctx context.Context, in *analyzeQuery) (*Analysis, error) {
	org, project, err := tenant(ctx)
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
	start, end := in.window()
	res, err := runAnalyze(o.s.State, ctx, org, exp, start, end, in.alpha())
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "analyze: %v", err)
	}
	return &res, nil
}

// window resolves [start,end): explicit start/end (RFC3339) win, else the last
// days (30 by default) up to now.
func (q analyzeQuery) window() (time.Time, time.Time) {
	end := time.Now().UTC()
	if e := strings.TrimSpace(q.End); e != "" {
		if t, err := time.Parse(time.RFC3339, e); err == nil {
			end = t.UTC()
		}
	}
	if s := strings.TrimSpace(q.Start); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.UTC(), end
		}
	}
	days := defaultWindow
	if q.Days > 0 && q.Days <= 365 {
		days = time.Duration(q.Days) * 24 * time.Hour
	}
	return end.Add(-days), end
}

// alpha is the two-tailed significance threshold this analysis judges by.
func (q analyzeQuery) alpha() float64 {
	if q.Alpha > 0 && q.Alpha < 1 {
		return q.Alpha
	}
	return defaultAlpha
}

// runAnalyze is the analysis composition: read per-subject outcomes from the
// measurement plane, fold them per variant by joining each subject to its flags
// assignment, persist the per-variant samples to the research evidence plane, and
// return the computed lift + significance. Pure statistics; impure only at the three
// plane reads/writes, each org-scoped.
func runAnalyze(st *state, ctx context.Context, org string, exp Trial, start, end time.Time, alpha float64) (Analysis, error) {
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

// evidenceRows maps an analysis into research Trial rows — one per variant,
// keyed <expID>:<variant>, kind "ab". value is the conversion rate, n the exposed
// sample size; the pairwise stats (lift, z, p, significance) ride meta. The
// experiment's own project scopes them so the org's R&D board groups them.
func evidenceRows(exp Trial, a Analysis, at time.Time) []research.Experiment {
	rows := make([]research.Experiment, 0, len(a.Outcomes))
	for _, r := range a.Outcomes {
		meta, _ := json.Marshal(map[string]any{
			"experiment":  exp.ID,
			"variant":     r.Arm,
			"control":     r.Control,
			"converted":   r.Converted,
			"lift":        r.Lift,
			"z":           r.Z,
			"pValue":      r.PValue,
			"significant": r.Significant,
			"flagKey":     exp.FlagKey,
		})
		rows = append(rows, research.Experiment{
			ID:      exp.ID + ":" + r.Arm,
			Kind:    evidenceKind,
			Subject: r.Arm,
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

// decideBody promotes one arm to the whole rollout.
type decideBody struct {
	// ID is the experiment the URL names.
	ID string `json:"-" url:"id"`
	// Winner is the variant to promote. It must name one of this experiment's own
	// arms.
	Winner string `json:"winner" validate:"required"`
}

// decide promotes one variant to the whole rollout and records who decided.
//
// It rewrites the assignment flag so the named winner serves 100% of the rollout
// and every other arm 0%, preserving the flag's targeting groups and payloads,
// then stamps the experiment decided with the winner, the deciding credential and
// the time. This is a production behaviour change that takes effect immediately
// for every subject the flag evaluates.
//
// It requires an ORG ADMIN of the caller's own org — a stricter gate than the rest
// of this surface, matching the flags write plane, because promoting is a flag
// write. The admin check runs AFTER the experiment is found, so a caller from
// another tenant is answered not-found rather than forbidden and learns nothing
// about what exists.
//
// An experiment whose assignment flag has gone missing is a conflict rather than a
// silent no-op — there is nothing to promote.
//
// Deciding is NOT terminal. A second call re-promotes a different variant and
// re-stamps the row; the status stays decided and the previous winner is
// overwritten with no record that it was ever chosen. Nothing here reverts the
// flag to its original weights either, so an experiment cannot be un-decided
// through this route — restoring a split means writing the flag definition back
// through the flags plane.
func (o ops) decide(ctx context.Context, in *decideBody) (*Trial, error) {
	org, project, err := tenant(ctx)
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
	if !orgAdmin(ctx) {
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
	actor := actorOf(ctx)
	if err := flags.PutDef(org, project, exp.FlagKey, promoted, actor); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "promote winner: %v", err)
	}
	st, err := storeFor(o.s, org, project)
	if err != nil {
		return nil, err
	}
	if err := st.decide(ctx, project, exp.ID, winner, actor, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return nil, err
	}
	exp, _, _ = st.get(ctx, project, exp.ID)
	return &exp, nil
}

// orgAdmin reports that the caller administers its OWN org — the gate a flag
// write takes. It needs the REQUEST rather than the tenant, because admin-ness
// lives in a header principal.OrgFrom does not carry. False off the HTTP path:
// no request, no attested caller, no admin rights.
func orgAdmin(ctx context.Context) bool {
	c, ok := cloud.Request(ctx)
	return ok && principal.IsOrgAdmin(c)
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

// ── in-process clients (clients/campaign composes these) ─────────────────────────

// Assign is the in-process assignment client: resolve the experiment's flag and return
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

// Analyze is the in-process analysis client (campaign's scheduler / a cron composes it
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
func loadExperiment(s *cloud.Service[*state], ctx context.Context, org, project, id string) (Trial, bool, error) {
	if !slugRE.MatchString(id) {
		return Trial{}, false, nil
	}
	st, err := storeFor(s, org, project)
	if err != nil {
		return Trial{}, false, err
	}
	return st.get(ctx, project, id)
}
