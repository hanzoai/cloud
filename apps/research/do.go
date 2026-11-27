package research

// do.go implements Hanzo Dō (HIP-1400 through HIP-1404) on the single research
// evidence store:
//
//   - HIP-1400: Component conformance manifest (/.well-known/hanzo-standard.json
//     and /v1/research/standard), embedded from hanzo-standard.json so the file
//     at rest and the wire answer cannot drift.
//   - HIP-1401: Evolutionary Candidate & Population Protocol — immutable,
//     content-addressed EvolutionCandidate DAG nodes restricted to the five
//     permitted mutation surfaces, multi-objective Pareto non-dominated frontier
//     selection, and independent qualification / rollback gating.
//   - HIP-1402: Hanzo XP — deterministic, non-transferable, retraction-reversible
//     read projection computed solely from qualified EvolutionCandidates and
//     independently replicated confirmatory ResearchClaims across the six skill
//     domains. Never stored as a mutable counter and never manually edited.
//   - HIP-1403: Autonomous Scientific Research Protocol — pre-registered
//     HypothesisRecords ordered by the store's monotone append clock (seq),
//     confirmatory vs exploratory claim separation (run.seq > hypothesis.seq),
//     independent clean-room replication, and CounterEvidence retraction.
//   - HIP-1404: Errors Become Fixes — structured regression records linking an
//     observed failure or CounterEvidence to the gate test that reproduces it
//     and the qualified EvolutionCandidate that resolves it.

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/hanzoai/orm"

	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

//go:embed hanzo-standard.json
var rawStandardManifest []byte

const (
	kindHypothesis = "research-hypothesis"
	kindClaim      = "research-claim"
	kindCandidate  = "research-candidate"
	kindRegression = "research-regression"

	xpRuleVersion = "1.0.0"
)

func init() {
	orm.Register[hypothesisRecord](kindHypothesis)
	orm.Register[claimRecord](kindClaim)
	orm.Register[candidateRecord](kindCandidate)
	orm.Register[regressionRecord](kindRegression)
}

// Closed vocabularies defined by HIP-1401, HIP-1402, HIP-1403 and HIP-1404.
var (
	mutationSurfaces = map[string]string{
		"harness.policy":    "harness",
		"engine.strategy":   "engine",
		"router.cascade":    "model",
		"model.adapter":     "model",
		"research.protocol": "science",
	}

	xpDomains = map[string]bool{
		"harness": true,
		"engine":  true,
		"model":   true,
		"sandbox": true,
		"science": true,
		"spec":    true,
	}

	claimModes = map[string]bool{
		"confirmatory": true,
		"exploratory":  true,
	}

	candidateStatuses = map[string]bool{
		"proposed":  true,
		"qualified": true,
		"promoted":  true,
		"retired":   true,
		"retracted": true,
	}

	regressionSources = map[string]bool{
		"eval_failure":        true,
		"production_error":    true,
		"counter_evidence":    true,
		"conformance_failure": true,
	}
)

// ── HIP-1400: Component Conformance Manifest ──────────────────────────────────

// ImplementedHIP declares one HIP specification version this component satisfies.
type ImplementedHIP struct {
	// HIP is the four-digit zero-padded HIP identifier (for example "1401").
	HIP string `json:"hip"`
	// Version is the semantic version of that HIP's contract implemented here.
	Version string `json:"version"`
	// Status is conformant or partial.
	Status string `json:"status"`
}

// StandardManifest is the HIP-1400 §2 component conformance manifest served by
// the research subsystem.
type StandardManifest struct {
	// SpecVersion is the target Hanzo OS standard.json semantic version.
	SpecVersion string `json:"spec_version"`
	// Component is the canonical layer and component identifier (research/cloud).
	Component string `json:"component"`
	// Version is this component's semantic release version.
	Version string `json:"version"`
	// ImplementedHIPs lists every HIP specification this component implements.
	ImplementedHIPs []ImplementedHIP `json:"implemented_hips"`
	// Capabilities lists the HIP-0139 capability identifiers served here.
	Capabilities []string `json:"capabilities"`
}

var parsedStandardManifest = func() StandardManifest {
	var m StandardManifest
	if err := json.Unmarshal(rawStandardManifest, &m); err != nil {
		panic("invalid embedded hanzo-standard.json: " + err.Error())
	}
	return m
}()

// GetResearchStandard returns the HIP-1400 component conformance manifest for
// the research, evolution, evidence, and XP plane.
func (o ops) getStandard(ctx context.Context, _ *cloud.Unit) (*StandardManifest, error) {
	if _, _, err := o.orgStore(ctx); err != nil {
		return nil, err
	}
	out := parsedStandardManifest
	return &out, nil
}

// ── HIP-1403: Hypotheses, Claims, Replication & Counter-Evidence ──────────────

// HypothesisBudget bounds the compute and dollar spend permitted for confirmatory
// runs under a pre-registered hypothesis (HIP-1403 §1.1 & §2.2).
type HypothesisBudget struct {
	// MaxComputeSeconds is the maximum wall-clock or sandbox compute budget in
	// seconds permitted for one confirmatory execution; 0 leaves duration unbounded.
	MaxComputeSeconds int64 `json:"max_compute_seconds"`
	// MaxCostUSD is the maximum spend in US dollars permitted for one confirmatory
	// execution; 0 leaves dollar spend unbounded.
	MaxCostUSD float64 `json:"max_cost_usd"`
}

// HypothesisRecord is an immutable pre-registered falsifiable hypothesis under
// HIP-1403 §1.1. Its server-assigned append sequence number (seq) MUST precede
// the sequence number of any confirmatory Run that tests it.
type HypothesisRecord struct {
	// Project is the sub-scope inside the org this hypothesis belongs to, set by
	// the server from the caller's project scope on write.
	Project string `json:"project"`
	// HypothesisID is the content-addressed sha256:<hex> digest of the canonical
	// hypothesis specification, computed inside the trust boundary.
	HypothesisID string `json:"hypothesis_id"`
	// Statement is the falsifiable claim predicting a measurable directional effect.
	Statement string `json:"statement"`
	// NullHypothesis states the condition under which the hypothesis is falsified.
	NullHypothesis string `json:"null_hypothesis"`
	// GateRef names the evaluation benchmark or gate suite (for example
	// benchmark://swe-bench-verified@1.2.0 or a ResearchBenchmark id).
	GateRef string `json:"gate_ref"`
	// DatasetRefs lists the content-addressed dataset digests (sha256:...) fixed
	// before execution begins.
	DatasetRefs []string `json:"dataset_refs"`
	// Budget bounds the compute seconds and US dollar cost allowed for confirmatory runs.
	Budget HypothesisBudget `json:"budget"`
	// Proposer is the human or agent principal (did:hanzo:... or IAM user) filing
	// the hypothesis; stamped from the authenticated caller when omitted.
	Proposer string `json:"proposer"`
	// RegisteredAt is the RFC 3339 UTC server timestamp when this hypothesis was
	// recorded.
	RegisteredAt string `json:"registered_at"`
	// Seq is the store's monotone append sequence number establishing strict
	// pre-registration ordering before confirmatory runs.
	Seq int64 `json:"seq"`
}

type hypothesisRecord struct {
	Project        string           `json:"project"`
	HypothesisID   string           `json:"hypothesis_id"`
	Statement      string           `json:"statement"`
	NullHypothesis string           `json:"null_hypothesis"`
	GateRef        string           `json:"gate_ref"`
	DatasetRefs    []string         `json:"dataset_refs"`
	Budget         HypothesisBudget `json:"budget"`
	Proposer       string           `json:"proposer"`
	RegisteredAt   string           `json:"registered_at"`
	Seq            int64            `json:"seq"`
}

func (r hypothesisRecord) stable() string { return r.Project + "\x00" + r.HypothesisID }
func (r hypothesisRecord) key() string    { return "h:" + hashJoin(r.Project, r.HypothesisID) }
func (r hypothesisRecord) clock() int64   { return r.Seq }
func (r hypothesisRecord) gone() bool     { return false }

func (r hypothesisRecord) wire() HypothesisRecord {
	ds := r.DatasetRefs
	if ds == nil {
		ds = []string{}
	}
	return HypothesisRecord{
		Project:        r.Project,
		HypothesisID:   r.HypothesisID,
		Statement:      r.Statement,
		NullHypothesis: r.NullHypothesis,
		GateRef:        r.GateRef,
		DatasetRefs:    ds,
		Budget:         r.Budget,
		Proposer:       r.Proposer,
		RegisteredAt:   r.RegisteredAt,
		Seq:            r.Seq,
	}
}

func hashHypothesis(r hypothesisRecord) (string, error) {
	core := map[string]any{
		"budget": map[string]any{
			"max_compute_seconds": r.Budget.MaxComputeSeconds,
			"max_cost_usd":        r.Budget.MaxCostUSD,
		},
		"dataset_refs":    nonNilStrings(r.DatasetRefs),
		"gate_ref":        r.GateRef,
		"null_hypothesis": r.NullHypothesis,
		"proposer":        r.Proposer,
		"statement":       r.Statement,
	}
	raw, err := json.Marshal(core)
	if err != nil {
		return "", err
	}
	c, err := canon(raw)
	if err != nil {
		return "", err
	}
	return "sha256:" + sha(c), nil
}

// RegisterHypothesis pre-registers one falsifiable HypothesisRecord (HIP-1403 §1)
// before confirmatory runs begin. Re-submitting identical hypothesis content is
// idempotent and preserves the original sequence number and timestamp.
//
// Example: {"statement": "Tree-search verification raises SWE-bench Verified pass@1 by >= 3.0% at <= $0.80/task.", "null_hypothesis": "Pass@1 lift is < 3.0% or cost exceeds $0.80/task.", "gate_ref": "swe-bench-verified", "dataset_refs": ["sha256:4a8b1f"], "budget": {"max_compute_seconds": 3600, "max_cost_usd": 50}}
func (o ops) postHypothesis(ctx context.Context, in *HypothesisRecord) (*HypothesisRecord, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := holdable(in); err != nil {
		return nil, err
	}
	proj := project(ctx)
	actor := actorPrincipal(ctx)
	proposer := normalizePrincipal(in.Proposer, actor)
	rec := hypothesisRecord{
		Project:        proj,
		Statement:      strings.TrimSpace(in.Statement),
		NullHypothesis: strings.TrimSpace(in.NullHypothesis),
		GateRef:        strings.TrimSpace(in.GateRef),
		DatasetRefs:    cleanStrings(in.DatasetRefs),
		Budget:         in.Budget,
		Proposer:       proposer,
		RegisteredAt:   now(),
	}
	if rec.Statement == "" || rec.NullHypothesis == "" || rec.GateRef == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "hypothesis requires statement, null_hypothesis, and gate_ref")
	}
	if rec.Budget.MaxComputeSeconds < 0 || rec.Budget.MaxCostUSD < 0 {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "hypothesis budget bounds must be non-negative")
	}
	hid, err := hashHypothesis(rec)
	if err != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "invalid hypothesis specification: %v", err)
	}
	if sent := strings.TrimSpace(in.HypothesisID); sent != "" && !strings.EqualFold(sent, hid) {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "hypothesis_id mismatch: canonical specification hashes to %s", hid)
	}
	rec.HypothesisID = hid

	var saved hypothesisRecord
	err = o.inTx(ctx, st, org, "hypothesis write", func(tx orm.DB) error {
		var existing hypothesisRecord
		if e := tx.Get(ctx, tx.NewKey(kindHypothesis, rec.key(), 0, nil), &existing); e == nil && existing.HypothesisID != "" {
			saved = existing
			return nil
		}
		if _, e := appendTx(ctx, tx, kindHypothesis, []hypothesisRecord{rec}, func(r *hypothesisRecord, seq int64) { r.Seq = seq }); e != nil {
			return e
		}
		return tx.Get(ctx, tx.NewKey(kindHypothesis, rec.key(), 0, nil), &saved)
	})
	if err != nil {
		return nil, err
	}
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	out := saved.wire()
	return &out, nil
}

// hypothesesIn narrows a hypothesis listing via query parameters.
type hypothesesIn struct {
	// Project narrows to one project; empty reads across every project in the org.
	Project string `json:"project"`
	// ID narrows to one hypothesis by its sha256:... hypothesis_id.
	ID string `json:"id"`
}

// hypothesesOut is a canonical listing of pre-registered hypotheses.
type hypothesesOut struct {
	// Data are the pre-registered hypotheses, newest sequence number first.
	Data []HypothesisRecord `json:"data"`
	// Total is len(data).
	Total int `json:"total"`
}

// ListHypotheses returns the org's pre-registered HypothesisRecords newest-first,
// each carrying its server-assigned sequence number so confirmatory run ordering
// is auditable.
func (o ops) listHypotheses(ctx context.Context, in *hypothesesIn) (*hypothesesOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	recs, err := latest[hypothesisRecord](ctx, st, kindHypothesis)
	if err != nil {
		return nil, o.failed("hypothesis read", org, err)
	}
	wantID := strings.TrimSpace(in.ID)
	wantProj := strings.TrimSpace(in.Project)
	out := []HypothesisRecord{}
	for _, r := range recs {
		if wantProj != "" && r.Project != wantProj {
			continue
		}
		if wantID != "" && r.HypothesisID != wantID {
			continue
		}
		out = append(out, r.wire())
	}
	return &hypothesesOut{Data: out, Total: len(out)}, nil
}

// ClaimReplication records one independent clean-room verification run against a
// ResearchClaim (HIP-1403 §3.1).
type ClaimReplication struct {
	// RunID is the completed independent Run that reproduced the primary claim.
	RunID string `json:"run_id"`
	// Verifier is the independent principal (distinct from the claim's proposer)
	// who executed and filed this replication.
	Verifier string `json:"verifier"`
	// Value is the measured value observed on the replication run for the claim's metric.
	Value float64 `json:"value"`
	// ConfidenceLo is the optional lower confidence bound reported by the replication run.
	ConfidenceLo *float64 `json:"confidence_lo,omitempty"`
	// ConfidenceHi is the optional upper confidence bound reported by the replication run.
	ConfidenceHi *float64 `json:"confidence_hi,omitempty"`
	// Agreed reports whether the replication satisfied directional and interval
	// agreement with the primary claim.
	Agreed bool `json:"agreed"`
	// ReplicatedAt is the RFC 3339 UTC server timestamp when this replication was recorded.
	ReplicatedAt string `json:"replicated_at"`
}

// CounterEvidence records verified evidence invalidating or refuting a previously
// filed claim or candidate (HIP-1403 §4.2 & HIP-1402 §3).
type CounterEvidence struct {
	// EvidenceRef is the content-addressed Evidence or artifact digest (sha256:...
	// or ev_...) demonstrating the refutation or regression.
	EvidenceRef string `json:"evidence_ref"`
	// RunID is the optional completed Run ID whose negative or contradictory
	// measurement refuted the claim.
	RunID string `json:"run_id,omitempty"`
	// Summary states concisely how the prior claim or candidate failed verification.
	Summary string `json:"summary"`
	// FiledBy is the principal who submitted the counter-evidence.
	FiledBy string `json:"filed_by"`
	// FiledAt is the RFC 3339 UTC server timestamp when the retraction was recorded.
	FiledAt string `json:"filed_at"`
}

// AttributionShare assigns a fractional credit weight in (0, 1] to one
// contributing principal on a candidate or claim (HIP-1402 §3).
type AttributionShare struct {
	// Principal is the contributing human or agent identity (did:hanzo:... or user id).
	Principal string `json:"principal"`
	// Weight is this contributor's fractional share; shares across a record sum to 1.0.
	Weight float64 `json:"weight"`
}

// ResearchClaim is a structured scientific finding resting on a completed Run
// (HIP-1403 §1.2 & §3). Confirmatory claims require a prior HypothesisRecord
// (run.seq > hypothesis.seq) and begin as unreplicated, earning zero XP until an
// independent principal replicates them.
type ResearchClaim struct {
	// Project is the sub-scope inside the org this claim belongs to, set by the
	// server on write.
	Project string `json:"project"`
	// ClaimID is the content-addressed sha256:<hex> identity of the claim's core
	// assertion, computed by the server.
	ClaimID string `json:"claim_id"`
	// HypothesisID is the pre-registered HypothesisRecord (sha256:...) this claim
	// tests; required when mode is confirmatory.
	HypothesisID string `json:"hypothesis_id,omitempty"`
	// RunID is the completed primary Run ID supporting this claim.
	RunID string `json:"run_id"`
	// CandidateRef is the optional EvolutionCandidate digest (sha256:...) evaluated
	// by this claim.
	CandidateRef string `json:"candidate_ref,omitempty"`
	// Mode is confirmatory (pre-registered before execution) or exploratory
	// (post-hoc observation, ineligible for confirmatory promotion or XP).
	Mode string `json:"mode"`
	// Metric names the measured quantity on the primary and replication runs.
	Metric string `json:"metric"`
	// Value is the primary run's measured value on Metric, derived from the run
	// record when present.
	Value float64 `json:"value"`
	// EffectSize is the directional delta over the baseline or null threshold.
	EffectSize float64 `json:"effect_size"`
	// ConfidenceLo is the optional lower confidence interval bound for Value.
	ConfidenceLo *float64 `json:"confidence_lo,omitempty"`
	// ConfidenceHi is the optional upper confidence interval bound for Value.
	ConfidenceHi *float64 `json:"confidence_hi,omitempty"`
	// Proposer is the principal filing the claim.
	Proposer string `json:"proposer"`
	// Contributors holds optional multi-principal attribution shares summing to 1.0.
	Contributors []AttributionShare `json:"contributors"`
	// EvidenceRefs lists content-addressed Evidence or artifact digests backing the claim.
	EvidenceRefs []string `json:"evidence_refs"`
	// Status is unreplicated, replicated, or retracted — derived and enforced by
	// the server's verification state machine.
	Status string `json:"status"`
	// WasReplicated records whether this claim ever achieved independent
	// replication prior to a retraction, so XP reversal is auditable.
	WasReplicated bool `json:"was_replicated"`
	// Replications lists independent replication attempts recorded against this claim.
	Replications []ClaimReplication `json:"replications"`
	// CounterEvidence holds the refutation record when status is retracted.
	CounterEvidence *CounterEvidence `json:"counter_evidence,omitempty"`
	// CreatedAt is the RFC 3339 UTC timestamp when the claim was first filed.
	CreatedAt string `json:"created_at"`
	// UpdatedAt is the RFC 3339 UTC timestamp of the latest state transition.
	UpdatedAt string `json:"updated_at"`
	// Seq is the store's monotone append sequence number for this version.
	Seq int64 `json:"seq"`
}

type claimRecord struct {
	Project         string             `json:"project"`
	ClaimID         string             `json:"claim_id"`
	ContentHash     string             `json:"content_hash"`
	HypothesisID    string             `json:"hypothesis_id,omitempty"`
	RunID           string             `json:"run_id"`
	CandidateRef    string             `json:"candidate_ref,omitempty"`
	Mode            string             `json:"mode"`
	Metric          string             `json:"metric"`
	Value           float64            `json:"value"`
	EffectSize      float64            `json:"effect_size"`
	ConfidenceLo    *float64           `json:"confidence_lo,omitempty"`
	ConfidenceHi    *float64           `json:"confidence_hi,omitempty"`
	Proposer        string             `json:"proposer"`
	Contributors    []AttributionShare `json:"contributors"`
	EvidenceRefs    []string           `json:"evidence_refs"`
	Status          string             `json:"status"`
	WasReplicated   bool               `json:"was_replicated"`
	Replications    []ClaimReplication `json:"replications"`
	CounterEvidence *CounterEvidence   `json:"counter_evidence,omitempty"`
	CreatedAt       string             `json:"created_at"`
	UpdatedAt       string             `json:"updated_at"`
	Seq             int64              `json:"seq"`
}

func (r claimRecord) stable() string { return r.Project + "\x00" + r.ClaimID }
func (r claimRecord) key() string    { return "cl:" + hashJoin(r.Project, r.ClaimID, r.ContentHash) }
func (r claimRecord) clock() int64   { return r.Seq }
func (r claimRecord) gone() bool     { return false } // retracted stays visible with status="retracted" per HIP-1403 §4.1

func (r *claimRecord) seal() error {
	raw, err := json.Marshal(struct {
		ClaimID         string             `json:"claim_id"`
		Status          string             `json:"status"`
		WasReplicated   bool               `json:"was_replicated"`
		Replications    []ClaimReplication `json:"replications"`
		CounterEvidence *CounterEvidence   `json:"counter_evidence,omitempty"`
	}{
		ClaimID:         r.ClaimID,
		Status:          r.Status,
		WasReplicated:   r.WasReplicated,
		Replications:    r.Replications,
		CounterEvidence: r.CounterEvidence,
	})
	if err != nil {
		return err
	}
	c, err := canon(raw)
	if err != nil {
		return err
	}
	r.ContentHash = sha(c)
	return nil
}

func (r claimRecord) wire() ResearchClaim {
	reps := r.Replications
	if reps == nil {
		reps = []ClaimReplication{}
	}
	evs := r.EvidenceRefs
	if evs == nil {
		evs = []string{}
	}
	contribs := r.Contributors
	if len(contribs) == 0 {
		contribs = []AttributionShare{{Principal: r.Proposer, Weight: 1}}
	}
	return ResearchClaim{
		Project:         r.Project,
		ClaimID:         r.ClaimID,
		HypothesisID:    r.HypothesisID,
		RunID:           r.RunID,
		CandidateRef:    r.CandidateRef,
		Mode:            r.Mode,
		Metric:          r.Metric,
		Value:           r.Value,
		EffectSize:      r.EffectSize,
		ConfidenceLo:    r.ConfidenceLo,
		ConfidenceHi:    r.ConfidenceHi,
		Proposer:        r.Proposer,
		Contributors:    contribs,
		EvidenceRefs:    evs,
		Status:          r.Status,
		WasReplicated:   r.WasReplicated,
		Replications:    reps,
		CounterEvidence: r.CounterEvidence,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
		Seq:             r.Seq,
	}
}

func hashClaimID(r claimRecord) (string, error) {
	core := map[string]any{
		"candidate_ref": r.CandidateRef,
		"confidence_hi": r.ConfidenceHi,
		"confidence_lo": r.ConfidenceLo,
		"effect_size":   r.EffectSize,
		"hypothesis_id": r.HypothesisID,
		"metric":        r.Metric,
		"mode":          r.Mode,
		"proposer":      r.Proposer,
		"run_id":        r.RunID,
		"value":         r.Value,
	}
	raw, err := json.Marshal(core)
	if err != nil {
		return "", err
	}
	c, err := canon(raw)
	if err != nil {
		return "", err
	}
	return "sha256:" + sha(c), nil
}

// FileResearchClaim records a scientific claim backed by a completed Run
// (HIP-1403 §1.2 & §3). A confirmatory claim MUST reference an existing
// HypothesisRecord whose sequence number strictly precedes the supporting Run's
// sequence number, and begins in the unreplicated state.
//
// Example: {"hypothesis_id": "sha256:...", "run_id": "swe-verified-cand-1", "mode": "confirmatory", "metric": "pass@1", "effect_size": 0.042}
func (o ops) postClaim(ctx context.Context, in *ResearchClaim) (*ResearchClaim, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := holdable(in); err != nil {
		return nil, err
	}
	proj := project(ctx)
	actor := actorPrincipal(ctx)
	proposer := normalizePrincipal(in.Proposer, actor)
	contribs, err := normalizeContributors(in.Contributors, proposer)
	if err != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "%s", err.Error())
	}

	mode := strings.TrimSpace(strings.ToLower(in.Mode))
	if mode == "" {
		if strings.TrimSpace(in.HypothesisID) != "" {
			mode = "confirmatory"
		} else {
			mode = "exploratory"
		}
	}
	if !claimModes[mode] {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "mode must be confirmatory or exploratory")
	}

	runID := strings.TrimSpace(in.RunID)
	metric := strings.TrimSpace(in.Metric)
	if runID == "" || metric == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "claim requires run_id and metric")
	}
	if in.ConfidenceLo != nil && in.ConfidenceHi != nil && *in.ConfidenceLo > *in.ConfidenceHi {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "confidence_lo cannot exceed confidence_hi")
	}

	var saved claimRecord
	err = o.inTx(ctx, st, org, "claim write", func(tx orm.DB) error {
		runRec, ok, err := canonicalRunInTx(ctx, tx, proj, runID)
		if err != nil {
			return err
		}
		if !ok {
			return zip.ErrNotFound("no canonical run " + strconv.Quote(runID))
		}
		runWire := runRec.toRun()
		if runWire.Completion != completionComplete {
			return zip.Errorf(http.StatusUnprocessableEntity, "supporting run %q is not complete (completion %s)", runID, runWire.Completion)
		}
		val, lo, hi, hasMetric := measureFromRun(runWire, metric)
		if !hasMetric {
			val = in.Value
		}
		cLo, cHi := in.ConfidenceLo, in.ConfidenceHi
		if cLo == nil && lo != nil {
			cLo = lo
		}
		if cHi == nil && hi != nil {
			cHi = hi
		}

		hypID := strings.TrimSpace(in.HypothesisID)
		if mode == "confirmatory" {
			if hypID == "" {
				return zip.Errorf(http.StatusUnprocessableEntity, "confirmatory claim requires a pre-registered hypothesis_id")
			}
			hyp, hok, err := hypothesisInTx(ctx, tx, proj, hypID)
			if err != nil {
				return err
			}
			if !hok {
				return zip.ErrNotFound("no pre-registered hypothesis " + strconv.Quote(hypID))
			}
			// HIP-1403 §1.2: HypothesisRecord sequence number MUST precede the Run's.
			if runRec.Seq <= hyp.Seq {
				return zip.Errorf(http.StatusUnprocessableEntity,
					"post-hoc confirmatory claim refused: run %q (seq %d) was recorded before or alongside hypothesis %q (seq %d); file as exploratory or execute a fresh run",
					runID, runRec.Seq, hypID, hyp.Seq)
			}
			if hyp.Budget.MaxCostUSD > 0 {
				if cost, ok := costFromRun(runWire); ok && cost > hyp.Budget.MaxCostUSD {
					return zip.Errorf(http.StatusUnprocessableEntity,
						"run %q spent $%.4f, exceeding hypothesis budget max_cost_usd $%.4f",
						runID, cost, hyp.Budget.MaxCostUSD)
				}
			}
		}

		t := now()
		rec := claimRecord{
			Project:       proj,
			HypothesisID:  hypID,
			RunID:         runID,
			CandidateRef:  strings.TrimSpace(in.CandidateRef),
			Mode:          mode,
			Metric:        metric,
			Value:         val,
			EffectSize:    in.EffectSize,
			ConfidenceLo:  cLo,
			ConfidenceHi:  cHi,
			Proposer:      proposer,
			Contributors:  contribs,
			EvidenceRefs:  cleanStrings(in.EvidenceRefs),
			Status:        "unreplicated",
			WasReplicated: false,
			Replications:  []ClaimReplication{},
			CreatedAt:     t,
			UpdatedAt:     t,
		}
		cid, err := hashClaimID(rec)
		if err != nil {
			return zip.Errorf(http.StatusUnprocessableEntity, "invalid claim: %v", err)
		}
		if sent := strings.TrimSpace(in.ClaimID); sent != "" && !strings.EqualFold(sent, cid) {
			return zip.Errorf(http.StatusUnprocessableEntity, "claim_id mismatch: canonical claim hashes to %s", cid)
		}
		rec.ClaimID = cid
		if cur, exists, err := claimInTx(ctx, tx, proj, cid); err != nil {
			return err
		} else if exists {
			saved = cur
			return nil
		}
		if err := rec.seal(); err != nil {
			return err
		}
		if _, err := appendTx(ctx, tx, kindClaim, []claimRecord{rec}, func(r *claimRecord, seq int64) { r.Seq = seq }); err != nil {
			return err
		}
		return tx.Get(ctx, tx.NewKey(kindClaim, rec.key(), 0, nil), &saved)
	})
	if err != nil {
		return nil, err
	}
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	out := saved.wire()
	return &out, nil
}

// claimsIn narrows a ResearchClaim listing via query parameters.
type claimsIn struct {
	// Project narrows to one project; empty reads across every project in the org.
	Project string `json:"project"`
	// ID narrows to one claim by its sha256:... claim_id.
	ID string `json:"id"`
	// HypothesisID narrows to claims referencing one pre-registered hypothesis.
	HypothesisID string `json:"hypothesis_id"`
	// Status narrows to unreplicated, replicated, or retracted.
	Status string `json:"status"`
}

// claimsOut is a canonical listing of ResearchClaims.
type claimsOut struct {
	// Data are the canonical ResearchClaims, newest sequence number first.
	Data []ResearchClaim `json:"data"`
	// Total is len(data).
	Total int `json:"total"`
}

// ListResearchClaims returns the org's canonical ResearchClaims newest-first.
func (o ops) listClaims(ctx context.Context, in *claimsIn) (*claimsOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	recs, err := latest[claimRecord](ctx, st, kindClaim)
	if err != nil {
		return nil, o.failed("claim read", org, err)
	}
	wantProj := strings.TrimSpace(in.Project)
	wantID := strings.TrimSpace(in.ID)
	wantHyp := strings.TrimSpace(in.HypothesisID)
	wantStatus := strings.TrimSpace(strings.ToLower(in.Status))
	out := []ResearchClaim{}
	for _, r := range recs {
		if wantProj != "" && r.Project != wantProj {
			continue
		}
		if wantID != "" && r.ClaimID != wantID {
			continue
		}
		if wantHyp != "" && r.HypothesisID != wantHyp {
			continue
		}
		if wantStatus != "" && r.Status != wantStatus {
			continue
		}
		out = append(out, r.wire())
	}
	return &claimsOut{Data: out, Total: len(out)}, nil
}

// replicateClaimIn submits an independent replication run for a ResearchClaim.
type replicateClaimIn struct {
	// ID is the target claim's sha256:... claim_id from the URL path.
	ID string `json:"id" path:"id"`
	// RunID is the completed independent clean-room Run reproducing the claim.
	RunID string `json:"run_id"`
	// Value is the optional replication measurement when the run's measures array
	// omits the named metric.
	Value *float64 `json:"value,omitempty"`
	// ConfidenceLo is the optional lower confidence bound of the replication run.
	ConfidenceLo *float64 `json:"confidence_lo,omitempty"`
	// ConfidenceHi is the optional upper confidence bound of the replication run.
	ConfidenceHi *float64 `json:"confidence_hi,omitempty"`
	// Tolerance is the maximum absolute difference permitted when neither run
	// carries confidence intervals; defaults to 0.05 (5% relative or absolute).
	Tolerance *float64 `json:"tolerance,omitempty"`
}

// ReplicateResearchClaim verifies an unreplicated confirmatory ResearchClaim
// using an independent completed Run (HIP-1403 §3.1). Self-replication by the
// proposer or any listed contributor is refused 403, and exploratory claims
// cannot be replicated into confirmatory truth.
func (o ops) replicateClaim(ctx context.Context, in *replicateClaimIn) (*ResearchClaim, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := holdable(in); err != nil {
		return nil, err
	}
	proj := project(ctx)
	verifier := actorPrincipal(ctx)
	if verifier == "" {
		return nil, zip.ErrForbidden("replication requires an authenticated verifier principal")
	}
	claimID := strings.TrimSpace(in.ID)
	repRunID := strings.TrimSpace(in.RunID)
	if claimID == "" || repRunID == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "replication requires claim id and run_id")
	}

	var updated claimRecord
	err = o.inTx(ctx, st, org, "claim replication", func(tx orm.DB) error {
		cur, ok, err := claimInTx(ctx, tx, proj, claimID)
		if err != nil {
			return err
		}
		if !ok {
			return zip.ErrNotFound("no claim " + strconv.Quote(claimID))
		}
		if cur.Status == "retracted" {
			return zip.ErrConflict("claim " + strconv.Quote(claimID) + " has been retracted")
		}
		if cur.Mode != "confirmatory" {
			return zip.Errorf(http.StatusUnprocessableEntity, "exploratory claims cannot transition to replicated; pre-register a new HypothesisRecord first (HIP-1403 §1.2)")
		}
		if samePrincipal(verifier, cur.Proposer) || hasContributor(cur.Contributors, verifier) {
			return zip.ErrForbidden("independent replication requires a verifier principal distinct from the claim proposer and contributors (HIP-1403 §3.1)")
		}
		if repRunID == cur.RunID {
			return zip.Errorf(http.StatusUnprocessableEntity, "replication run_id must be an independent execution distinct from primary run %q", cur.RunID)
		}

		primaryRec, pok, err := canonicalRunInTx(ctx, tx, cur.Project, cur.RunID)
		if err != nil {
			return err
		}
		repRec, rok, err := canonicalRunInTx(ctx, tx, cur.Project, repRunID)
		if err != nil {
			return err
		}
		if !rok {
			return zip.ErrNotFound("no canonical replication run " + strconv.Quote(repRunID))
		}
		repWire := repRec.toRun()
		if repWire.Completion != completionComplete {
			return zip.Errorf(http.StatusUnprocessableEntity, "replication run %q is not complete (completion %s)", repRunID, repWire.Completion)
		}
		if pok {
			primWire := primaryRec.toRun()
			if primWire.Benchmark != repWire.Benchmark || primWire.Split != repWire.Split {
				return zip.Errorf(http.StatusUnprocessableEntity,
					"replication run %q measured (%s, %s), which does not match primary run (%s, %s)",
					repRunID, repWire.Benchmark, repWire.Split, primWire.Benchmark, primWire.Split)
			}
		}

		repVal, rLo, rHi, hasM := measureFromRun(repWire, cur.Metric)
		if !hasM {
			if in.Value == nil {
				return zip.Errorf(http.StatusUnprocessableEntity, "replication run %q does not report metric %q and no value was supplied", repRunID, cur.Metric)
			}
			repVal = *in.Value
		}
		if in.ConfidenceLo != nil {
			rLo = in.ConfidenceLo
		}
		if in.ConfidenceHi != nil {
			rHi = in.ConfidenceHi
		}

		tol := 0.05
		if in.Tolerance != nil && *in.Tolerance >= 0 {
			tol = *in.Tolerance
		}
		agreed := replicationAgrees(cur.Value, cur.ConfidenceLo, cur.ConfidenceHi, repVal, rLo, rHi, tol)

		next := cur
		next.Replications = append(slices.Clone(cur.Replications), ClaimReplication{
			RunID:        repRunID,
			Verifier:     canonicalDID(verifier),
			Value:        repVal,
			ConfidenceLo: rLo,
			ConfidenceHi: rHi,
			Agreed:       agreed,
			ReplicatedAt: now(),
		})
		if agreed {
			next.Status = "replicated"
			next.WasReplicated = true
		}
		next.UpdatedAt = now()
		if err := next.seal(); err != nil {
			return err
		}
		if _, err := appendTx(ctx, tx, kindClaim, []claimRecord{next}, func(r *claimRecord, seq int64) { r.Seq = seq }); err != nil {
			return err
		}
		return tx.Get(ctx, tx.NewKey(kindClaim, next.key(), 0, nil), &updated)
	})
	if err != nil {
		return nil, err
	}
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	out := updated.wire()
	return &out, nil
}

// retractIn submits verified CounterEvidence retracting a claim or candidate.
type retractIn struct {
	// ID is the target claim or candidate sha256:... identifier from the URL path.
	ID string `json:"id" path:"id"`
	// EvidenceRef is the content-addressed Evidence or artifact digest proving the
	// refutation or post-deployment regression.
	EvidenceRef string `json:"evidence_ref"`
	// RunID is the optional completed Run ID demonstrating the refutation.
	RunID string `json:"run_id,omitempty"`
	// Summary explains concisely why the prior claim or candidate is retracted.
	Summary string `json:"summary"`
}

// RetractResearchClaim files CounterEvidence against a ResearchClaim, marking it
// retracted while preserving the full historical version chain and reversing any
// derived science XP (HIP-1403 §4.2 & HIP-1402 §3).
func (o ops) retractClaim(ctx context.Context, in *retractIn) (*ResearchClaim, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := holdable(in); err != nil {
		return nil, err
	}
	proj := project(ctx)
	claimID := strings.TrimSpace(in.ID)
	evRef := strings.TrimSpace(in.EvidenceRef)
	summary := strings.TrimSpace(in.Summary)
	if claimID == "" || evRef == "" || summary == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "retraction requires id, evidence_ref, and summary")
	}

	var updated claimRecord
	err = o.inTx(ctx, st, org, "claim retraction", func(tx orm.DB) error {
		cur, ok, err := claimInTx(ctx, tx, proj, claimID)
		if err != nil {
			return err
		}
		if !ok {
			return zip.ErrNotFound("no claim " + strconv.Quote(claimID))
		}
		t := now()
		next := cur
		next.Status = "retracted"
		next.CounterEvidence = &CounterEvidence{
			EvidenceRef: evRef,
			RunID:       strings.TrimSpace(in.RunID),
			Summary:     summary,
			FiledBy:     canonicalDID(actorPrincipal(ctx)),
			FiledAt:     t,
		}
		next.UpdatedAt = t
		if err := next.seal(); err != nil {
			return err
		}
		if _, err := appendTx(ctx, tx, kindClaim, []claimRecord{next}, func(r *claimRecord, seq int64) { r.Seq = seq }); err != nil {
			return err
		}
		return tx.Get(ctx, tx.NewKey(kindClaim, next.key(), 0, nil), &updated)
	})
	if err != nil {
		return nil, err
	}
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	out := updated.wire()
	return &out, nil
}

// ── HIP-1401: Evolutionary Candidate & Population Protocol ────────────────────

// ProductFitness is the Level 1 multi-objective task fitness vector of an
// EvolutionCandidate (HIP-1401 §3.1).
type ProductFitness struct {
	// VerifiedSuccess is the pass rate on deterministic conformance and holdout
	// suites in [0.0, 1.0] (maximize).
	VerifiedSuccess float64 `json:"verified_success"`
	// CostPerSuccessUSD is total token plus sandbox compute spend in US dollars
	// per verified task success (minimize).
	CostPerSuccessUSD float64 `json:"cost_per_success_usd"`
	// LatencyP50Ms is median wall-clock task completion latency in milliseconds (minimize).
	LatencyP50Ms float64 `json:"latency_p50_ms"`
	// LatencyP95Ms is 95th-percentile wall-clock task completion latency in milliseconds (minimize).
	LatencyP95Ms float64 `json:"latency_p95_ms"`
	// Robustness is the pass rate under adversarial perturbation and environment
	// variation in [0.0, 1.0] (maximize).
	Robustness float64 `json:"robustness"`
	// Safety is 1.0 minus the policy/sandbox violation rate in [0.0, 1.0]; must
	// equal 1.0 (zero violations) for qualification.
	Safety float64 `json:"safety"`
}

// EvolutionFitness is the Level 2 meta-evolutionary fitness vector measuring how
// effectively a research protocol or candidate produces verified improvements
// per unit compute (HIP-1401 §3.2).
type EvolutionFitness struct {
	// ResearchYield is verified fitness delta achieved per US dollar of compute spent.
	ResearchYield float64 `json:"research_yield"`
	// SuccessorLift is the best verified fitness improvement over parent lineage
	// within a fixed compute horizon.
	SuccessorLift float64 `json:"successor_lift"`
	// DiscoveryRate is the fraction of generated child candidates that pass
	// independent holdout qualification.
	DiscoveryRate float64 `json:"discovery_rate"`
	// TransferRate is the fraction of improvements on source tasks that transfer
	// without regression to out-of-distribution target tasks.
	TransferRate float64 `json:"transfer_rate"`
	// Reproducibility is 1.0 minus variance across independent clean-room reruns
	// in [0.0, 1.0].
	Reproducibility float64 `json:"reproducibility"`
}

// CandidateAttestation is a verifier signature or qualification stamp attached
// to an EvolutionCandidate (HIP-1401 §1.1 & §5.1).
type CandidateAttestation struct {
	// Verifier is the independent principal attesting to the candidate's evaluation.
	Verifier string `json:"verifier"`
	// Role names the attestation stage: harness, qualifier, or promoter.
	Role string `json:"role"`
	// EvidenceRef is the content-addressed Evidence or Run digest verified.
	EvidenceRef string `json:"evidence_ref"`
	// Signature is the optional cryptographic signature (ed25519:...) over candidate_id.
	Signature string `json:"signature,omitempty"`
	// AttestedAt is the RFC 3339 UTC timestamp when the attestation was recorded.
	AttestedAt string `json:"attested_at"`
}

// EvolutionCandidate is a content-addressed node in the evolutionary lineage DAG
// (HIP-1401 §1.1). Mutations are restricted to the five permitted surfaces so a
// candidate can never weaken sandbox isolation, evidence hashing, or its own
// evaluation gate.
type EvolutionCandidate struct {
	// Project is the sub-scope inside the org this candidate belongs to, set by
	// the server on write.
	Project string `json:"project"`
	// CandidateID is the sha256:<hex> digest of the canonical candidate payload
	// (excluding signature, attestations, and mutable lifecycle fields).
	CandidateID string `json:"candidate_id"`
	// ParentIDs lists one or more parent candidate_ids (or ["root"] for genesis
	// baselines), forming a directed acyclic lineage graph.
	ParentIDs []string `json:"parent_ids"`
	// SpecVersion is the target Hanzo OS standard.json semantic version (for
	// example "1.0.0").
	SpecVersion string `json:"spec_version"`
	// MutationSurface names the single permitted architectural surface modified:
	// harness.policy, engine.strategy, router.cascade, model.adapter, or research.protocol.
	MutationSurface string `json:"mutation_surface"`
	// ArtifactDigest is the content-addressed digest (sha256:...) of the mutated
	// artifact bundle.
	ArtifactDigest string `json:"artifact_digest"`
	// DiffRef is the content-addressed unified diff or patch bundle against parent_ids[0].
	DiffRef string `json:"diff_ref"`
	// Proposer is the principal (did:hanzo:... or IAM user) proposing the candidate.
	Proposer string `json:"proposer"`
	// Contributors holds fractional attribution shares summing to 1.0 for XP projection.
	Contributors []AttributionShare `json:"contributors"`
	// CreatedAt is the RFC 3339 UTC timestamp when the candidate was proposed.
	CreatedAt string `json:"created_at"`
	// GateRef names the evaluation benchmark or conformance suite version gating
	// this candidate.
	GateRef string `json:"gate_ref"`
	// EvidenceRefs lists content-addressed Evidence or Run digests produced by
	// evaluating this candidate.
	EvidenceRefs []string `json:"evidence_refs"`
	// Signature is the proposer's optional cryptographic signature over candidate_id.
	Signature string `json:"signature,omitempty"`
	// Attestations lists independent verification and qualification attestations.
	Attestations []CandidateAttestation `json:"attestations"`
	// ProductFitness holds the Level 1 multi-objective task metrics once evaluated.
	ProductFitness *ProductFitness `json:"product_fitness,omitempty"`
	// EvolutionFitness holds optional Level 2 meta-evolutionary yield and lift metrics.
	EvolutionFitness *EvolutionFitness `json:"evolution_fitness,omitempty"`
	// Status is proposed, qualified, promoted, retired, or retracted.
	Status string `json:"status"`
	// WasQualified records whether this candidate passed independent qualification
	// before any subsequent retraction, enabling exact XP reversal.
	WasQualified bool `json:"was_qualified"`
	// QualifiedBy is the independent principal who qualified or promoted the candidate.
	QualifiedBy string `json:"qualified_by,omitempty"`
	// QualifiedAt is the RFC 3339 UTC timestamp when the candidate was qualified.
	QualifiedAt string `json:"qualified_at,omitempty"`
	// RollbackTarget is the pinned parent candidate_id or artifact digest preserved
	// for instant rollback on promotion (HIP-1401 §5.2).
	RollbackTarget string `json:"rollback_target,omitempty"`
	// RetractedBy holds the CounterEvidence digest when status is retracted.
	RetractedBy string `json:"retracted_by,omitempty"`
	// Seq is the store's monotone append sequence number for this version.
	Seq int64 `json:"seq"`
}

type candidateRecord struct {
	Project          string                 `json:"project"`
	CandidateID      string                 `json:"candidate_id"`
	ContentHash      string                 `json:"content_hash"`
	ParentIDs        []string               `json:"parent_ids"`
	SpecVersion      string                 `json:"spec_version"`
	MutationSurface  string                 `json:"mutation_surface"`
	ArtifactDigest   string                 `json:"artifact_digest"`
	DiffRef          string                 `json:"diff_ref"`
	Proposer         string                 `json:"proposer"`
	Contributors     []AttributionShare     `json:"contributors"`
	CreatedAt        string                 `json:"created_at"`
	GateRef          string                 `json:"gate_ref"`
	EvidenceRefs     []string               `json:"evidence_refs"`
	Signature        string                 `json:"signature,omitempty"`
	Attestations     []CandidateAttestation `json:"attestations"`
	ProductFitness   *ProductFitness        `json:"product_fitness,omitempty"`
	EvolutionFitness *EvolutionFitness      `json:"evolution_fitness,omitempty"`
	Status           string                 `json:"status"`
	WasQualified     bool                   `json:"was_qualified"`
	QualifiedBy      string                 `json:"qualified_by,omitempty"`
	QualifiedAt      string                 `json:"qualified_at,omitempty"`
	RollbackTarget   string                 `json:"rollback_target,omitempty"`
	RetractedBy      string                 `json:"retracted_by,omitempty"`
	Seq              int64                  `json:"seq"`
}

func (r candidateRecord) stable() string { return r.Project + "\x00" + r.CandidateID }
func (r candidateRecord) key() string {
	return "ec:" + hashJoin(r.Project, r.CandidateID, r.ContentHash)
}
func (r candidateRecord) clock() int64 { return r.Seq }
func (r candidateRecord) gone() bool   { return false }

func (r *candidateRecord) seal() error {
	raw, err := json.Marshal(struct {
		CandidateID      string                 `json:"candidate_id"`
		EvidenceRefs     []string               `json:"evidence_refs"`
		Attestations     []CandidateAttestation `json:"attestations"`
		ProductFitness   *ProductFitness        `json:"product_fitness,omitempty"`
		EvolutionFitness *EvolutionFitness      `json:"evolution_fitness,omitempty"`
		Status           string                 `json:"status"`
		WasQualified     bool                   `json:"was_qualified"`
		QualifiedBy      string                 `json:"qualified_by,omitempty"`
		RollbackTarget   string                 `json:"rollback_target,omitempty"`
		RetractedBy      string                 `json:"retracted_by,omitempty"`
	}{
		CandidateID:      r.CandidateID,
		EvidenceRefs:     r.EvidenceRefs,
		Attestations:     r.Attestations,
		ProductFitness:   r.ProductFitness,
		EvolutionFitness: r.EvolutionFitness,
		Status:           r.Status,
		WasQualified:     r.WasQualified,
		QualifiedBy:      r.QualifiedBy,
		RollbackTarget:   r.RollbackTarget,
		RetractedBy:      r.RetractedBy,
	})
	if err != nil {
		return err
	}
	c, err := canon(raw)
	if err != nil {
		return err
	}
	r.ContentHash = sha(c)
	return nil
}

func (r candidateRecord) wire() EvolutionCandidate {
	evs := r.EvidenceRefs
	if evs == nil {
		evs = []string{}
	}
	atts := r.Attestations
	if atts == nil {
		atts = []CandidateAttestation{}
	}
	contribs := r.Contributors
	if len(contribs) == 0 {
		contribs = []AttributionShare{{Principal: r.Proposer, Weight: 1}}
	}
	return EvolutionCandidate{
		Project:          r.Project,
		CandidateID:      r.CandidateID,
		ParentIDs:        nonNilStrings(r.ParentIDs),
		SpecVersion:      r.SpecVersion,
		MutationSurface:  r.MutationSurface,
		ArtifactDigest:   r.ArtifactDigest,
		DiffRef:          r.DiffRef,
		Proposer:         r.Proposer,
		Contributors:     contribs,
		CreatedAt:        r.CreatedAt,
		GateRef:          r.GateRef,
		EvidenceRefs:     evs,
		Signature:        r.Signature,
		Attestations:     atts,
		ProductFitness:   r.ProductFitness,
		EvolutionFitness: r.EvolutionFitness,
		Status:           r.Status,
		WasQualified:     r.WasQualified,
		QualifiedBy:      r.QualifiedBy,
		QualifiedAt:      r.QualifiedAt,
		RollbackTarget:   r.RollbackTarget,
		RetractedBy:      r.RetractedBy,
		Seq:              r.Seq,
	}
}

func hashCandidateID(r candidateRecord) (string, error) {
	core := map[string]any{
		"artifact_digest":  r.ArtifactDigest,
		"diff_ref":         r.DiffRef,
		"gate_ref":         r.GateRef,
		"mutation_surface": r.MutationSurface,
		"parent_ids":       nonNilStrings(r.ParentIDs),
		"proposer":         r.Proposer,
		"spec_version":     r.SpecVersion,
	}
	raw, err := json.Marshal(core)
	if err != nil {
		return "", err
	}
	c, err := canon(raw)
	if err != nil {
		return "", err
	}
	return "sha256:" + sha(c), nil
}

// ProposeEvolutionCandidate records a new EvolutionCandidate node in the
// evolutionary lineage DAG (HIP-1401 §§1–2). Mutations outside the five
// permitted surfaces (such as sandbox isolation, audit hashing, or evaluation
// gates) are deterministically rejected 422. Every new candidate enters as
// proposed and requires independent qualification to earn XP or promote.
//
// Example: {"parent_ids": ["root"], "spec_version": "1.0.0", "mutation_surface": "engine.strategy", "artifact_digest": "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", "diff_ref": "sha256:deadbeef", "gate_ref": "swe-bench-verified@1.2.0"}
func (o ops) postCandidate(ctx context.Context, in *EvolutionCandidate) (*EvolutionCandidate, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := holdable(in); err != nil {
		return nil, err
	}
	proj := project(ctx)
	actor := actorPrincipal(ctx)
	proposer := normalizePrincipal(in.Proposer, actor)
	contribs, err := normalizeContributors(in.Contributors, proposer)
	if err != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "%s", err.Error())
	}

	surface := strings.TrimSpace(in.MutationSurface)
	if _, ok := mutationSurfaces[surface]; !ok {
		return nil, zip.Errorf(http.StatusUnprocessableEntity,
			"forbidden mutation_surface %q: must be one of harness.policy, engine.strategy, router.cascade, model.adapter, research.protocol (HIP-1401 §2)",
			surface)
	}
	parents := cleanStrings(in.ParentIDs)
	if len(parents) == 0 {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "parent_ids must contain at least [\"root\"] or one parent candidate_id")
	}
	specVer := strings.TrimSpace(in.SpecVersion)
	if specVer == "" {
		specVer = parsedStandardManifest.SpecVersion
	}
	artDigest := strings.TrimSpace(in.ArtifactDigest)
	diffRef := strings.TrimSpace(in.DiffRef)
	gateRef := strings.TrimSpace(in.GateRef)
	if !strings.HasPrefix(artDigest, "sha256:") || len(strings.TrimPrefix(artDigest, "sha256:")) < 6 {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "artifact_digest must be a content-addressed sha256:<hex> digest")
	}
	if diffRef == "" || gateRef == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "candidate requires diff_ref and gate_ref")
	}
	if err := validateProductFitness(in.ProductFitness); err != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "%s", err.Error())
	}

	rec := candidateRecord{
		Project:          proj,
		ParentIDs:        parents,
		SpecVersion:      specVer,
		MutationSurface:  surface,
		ArtifactDigest:   artDigest,
		DiffRef:          diffRef,
		Proposer:         proposer,
		Contributors:     contribs,
		CreatedAt:        now(),
		GateRef:          gateRef,
		EvidenceRefs:     cleanStrings(in.EvidenceRefs),
		Signature:        strings.TrimSpace(in.Signature),
		Attestations:     []CandidateAttestation{},
		ProductFitness:   in.ProductFitness,
		EvolutionFitness: in.EvolutionFitness,
		Status:           "proposed",
	}
	cid, err := hashCandidateID(rec)
	if err != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "invalid candidate: %v", err)
	}
	if sent := strings.TrimSpace(in.CandidateID); sent != "" && !strings.EqualFold(sent, cid) {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "candidate_id mismatch: canonical candidate hashes to %s", cid)
	}
	if slices.Contains(parents, cid) {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "candidate parent_ids cannot contain its own candidate_id")
	}
	rec.CandidateID = cid

	var saved candidateRecord
	err = o.inTx(ctx, st, org, "candidate write", func(tx orm.DB) error {
		// Verify every non-root parent exists in the org's lineage DAG.
		for _, pid := range parents {
			if pid == "root" {
				continue
			}
			if _, ok, err := candidateInTx(ctx, tx, proj, pid); err != nil {
				return err
			} else if !ok {
				return zip.ErrNotFound("parent candidate " + strconv.Quote(pid) + " not found in project")
			}
		}
		if cur, exists, err := candidateInTx(ctx, tx, proj, cid); err != nil {
			return err
		} else if exists {
			saved = cur
			return nil
		}
		if err := rec.seal(); err != nil {
			return err
		}
		if _, err := appendTx(ctx, tx, kindCandidate, []candidateRecord{rec}, func(r *candidateRecord, seq int64) { r.Seq = seq }); err != nil {
			return err
		}
		return tx.Get(ctx, tx.NewKey(kindCandidate, rec.key(), 0, nil), &saved)
	})
	if err != nil {
		return nil, err
	}
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	out := saved.wire()
	return &out, nil
}

// candidatesIn narrows a candidate listing or Pareto frontier query.
type candidatesIn struct {
	// Project narrows to one project; empty reads across every project in the org.
	Project string `json:"project"`
	// ID narrows to one candidate by its sha256:... candidate_id.
	ID string `json:"id"`
	// MutationSurface narrows to one permitted surface (for example engine.strategy).
	MutationSurface string `json:"mutation_surface"`
	// Status narrows to proposed, qualified, promoted, retired, or retracted.
	Status string `json:"status"`
}

// candidatesOut is a canonical listing of EvolutionCandidates.
type candidatesOut struct {
	// Data are the matching canonical EvolutionCandidates.
	Data []EvolutionCandidate `json:"data"`
	// Total is len(data).
	Total int `json:"total"`
}

// ListEvolutionCandidates returns the org's canonical EvolutionCandidates,
// newest sequence number first.
func (o ops) listCandidates(ctx context.Context, in *candidatesIn) (*candidatesOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	recs, err := latest[candidateRecord](ctx, st, kindCandidate)
	if err != nil {
		return nil, o.failed("candidate read", org, err)
	}
	out := filterCandidates(recs, in)
	return &candidatesOut{Data: out, Total: len(out)}, nil
}

// GetCandidateFrontier computes the multi-objective non-dominated Pareto
// frontier over active EvolutionCandidates with zero safety violations
// (HIP-1401 §4.1), maximizing verified_success and robustness while minimizing
// cost_per_success_usd and latency_p50_ms.
func (o ops) getCandidateFrontier(ctx context.Context, in *candidatesIn) (*candidatesOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	recs, err := latest[candidateRecord](ctx, st, kindCandidate)
	if err != nil {
		return nil, o.failed("candidate frontier", org, err)
	}
	filtered := filterCandidates(recs, in)
	pool := make([]EvolutionCandidate, 0, len(filtered))
	for _, c := range filtered {
		if c.Status == "retired" || c.Status == "retracted" {
			continue
		}
		if c.ProductFitness == nil || c.ProductFitness.Safety < 1.0 {
			continue
		}
		pool = append(pool, c)
	}
	frontier := paretoFrontier(pool)
	return &candidatesOut{Data: frontier, Total: len(frontier)}, nil
}

// qualifyCandidateIn submits an independent qualification or promotion verdict
// for an EvolutionCandidate (HIP-1401 §5).
type qualifyCandidateIn struct {
	// ID is the target candidate's sha256:... candidate_id from the URL path.
	ID string `json:"id" path:"id"`
	// EvidenceRefs lists the holdout Evidence or Run digests establishing fitness.
	EvidenceRefs []string `json:"evidence_refs"`
	// ProductFitness is the verified Level 1 fitness vector measured by the qualifier.
	ProductFitness *ProductFitness `json:"product_fitness"`
	// EvolutionFitness is the optional Level 2 meta-evolutionary fitness vector.
	EvolutionFitness *EvolutionFitness `json:"evolution_fitness,omitempty"`
	// Promote transitions the candidate directly to promoted when true; otherwise
	// status becomes qualified.
	Promote bool `json:"promote"`
	// RollbackTarget names the pinned parent candidate_id or baseline artifact
	// digest to roll back to if post-promotion monitoring regresses; defaults to parent_ids[0].
	RollbackTarget string `json:"rollback_target,omitempty"`
	// Signature is the qualifier's optional cryptographic signature over candidate_id.
	Signature string `json:"signature,omitempty"`
}

// QualifyEvolutionCandidate records independent holdout qualification (and
// optional promotion) for an EvolutionCandidate (HIP-1401 §5). The qualifier
// principal MUST be distinct from the proposer and contributors, safety MUST
// equal 1.0 (zero violations), and a rollback target is pinned automatically.
func (o ops) qualifyCandidate(ctx context.Context, in *qualifyCandidateIn) (*EvolutionCandidate, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := holdable(in); err != nil {
		return nil, err
	}
	proj := project(ctx)
	qualifier := actorPrincipal(ctx)
	if qualifier == "" {
		return nil, zip.ErrForbidden("qualification requires an authenticated qualifier principal")
	}
	cid := strings.TrimSpace(in.ID)
	evs := cleanStrings(in.EvidenceRefs)
	if cid == "" || len(evs) == 0 {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "qualification requires candidate id and at least one evidence_ref")
	}
	if err := validateProductFitness(in.ProductFitness); err != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "%s", err.Error())
	}

	var updated candidateRecord
	err = o.inTx(ctx, st, org, "candidate qualification", func(tx orm.DB) error {
		cur, ok, err := candidateInTx(ctx, tx, proj, cid)
		if err != nil {
			return err
		}
		if !ok {
			return zip.ErrNotFound("no candidate " + strconv.Quote(cid))
		}
		if cur.Status == "retracted" {
			return zip.ErrConflict("candidate " + strconv.Quote(cid) + " has been retracted")
		}
		if samePrincipal(qualifier, cur.Proposer) || hasContributor(cur.Contributors, qualifier) {
			return zip.ErrForbidden("independent qualification requires a principal distinct from the candidate proposer and contributors (HIP-1401 §5.1)")
		}

		pf := in.ProductFitness
		if pf == nil {
			pf = cur.ProductFitness
		}
		if pf == nil {
			return zip.Errorf(http.StatusUnprocessableEntity, "qualification requires product_fitness metrics")
		}
		if pf.Safety < 1.0 {
			return zip.Errorf(http.StatusUnprocessableEntity, "qualification refused: safety is %.4f, must be 1.0 (zero policy/sandbox violations)", pf.Safety)
		}

		rollback := strings.TrimSpace(in.RollbackTarget)
		if rollback == "" {
			rollback = cur.ParentIDs[0]
		}
		role := "qualifier"
		status := "qualified"
		if in.Promote {
			role = "promoter"
			status = "promoted"
		}
		t := now()
		next := cur
		next.ProductFitness = pf
		if in.EvolutionFitness != nil {
			next.EvolutionFitness = in.EvolutionFitness
		}
		next.EvidenceRefs = mergeStrings(cur.EvidenceRefs, evs)
		next.Status = status
		next.WasQualified = true
		next.QualifiedBy = canonicalDID(qualifier)
		next.QualifiedAt = t
		next.RollbackTarget = rollback
		next.Attestations = append(slices.Clone(cur.Attestations), CandidateAttestation{
			Verifier:    canonicalDID(qualifier),
			Role:        role,
			EvidenceRef: evs[0],
			Signature:   strings.TrimSpace(in.Signature),
			AttestedAt:  t,
		})
		if err := next.seal(); err != nil {
			return err
		}
		if _, err := appendTx(ctx, tx, kindCandidate, []candidateRecord{next}, func(r *candidateRecord, seq int64) { r.Seq = seq }); err != nil {
			return err
		}
		return tx.Get(ctx, tx.NewKey(kindCandidate, next.key(), 0, nil), &updated)
	})
	if err != nil {
		return nil, err
	}
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	out := updated.wire()
	return &out, nil
}

// RetractEvolutionCandidate files CounterEvidence retracting a previously
// proposed or qualified EvolutionCandidate and reversing any derived XP
// (HIP-1401 §5.2 & HIP-1402 §3).
func (o ops) retractCandidate(ctx context.Context, in *retractIn) (*EvolutionCandidate, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := holdable(in); err != nil {
		return nil, err
	}
	proj := project(ctx)
	cid := strings.TrimSpace(in.ID)
	evRef := strings.TrimSpace(in.EvidenceRef)
	if cid == "" || evRef == "" || strings.TrimSpace(in.Summary) == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "retraction requires id, evidence_ref, and summary")
	}

	var updated candidateRecord
	err = o.inTx(ctx, st, org, "candidate retraction", func(tx orm.DB) error {
		cur, ok, err := candidateInTx(ctx, tx, proj, cid)
		if err != nil {
			return err
		}
		if !ok {
			return zip.ErrNotFound("no candidate " + strconv.Quote(cid))
		}
		next := cur
		next.Status = "retracted"
		next.RetractedBy = evRef
		if err := next.seal(); err != nil {
			return err
		}
		if _, err := appendTx(ctx, tx, kindCandidate, []candidateRecord{next}, func(r *candidateRecord, seq int64) { r.Seq = seq }); err != nil {
			return err
		}
		return tx.Get(ctx, tx.NewKey(kindCandidate, next.key(), 0, nil), &updated)
	})
	if err != nil {
		return nil, err
	}
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	out := updated.wire()
	return &out, nil
}

// ── HIP-1402: Hanzo XP Deterministic Read Projection ──────────────────────────

// XPProjection is one deterministic, content-addressed contribution credit or
// retraction reversal derived from the Evidence store (HIP-1402 §2.1).
type XPProjection struct {
	// ProjectionID is the deterministic sha256:<hex> digest of
	// (evidence_ref, principal, domain, reason, rule_version).
	ProjectionID string `json:"projection_id"`
	// Principal is the human or agent identity receiving this non-transferable delta.
	Principal string `json:"principal"`
	// Domain is one of the six closed skill domains: harness, engine, model,
	// sandbox, science, or spec.
	Domain string `json:"domain"`
	// Delta is the signed integer XP change (positive on verified qualification
	// or replication, negative on retraction reversal).
	Delta int `json:"delta"`
	// Reason is candidate_qualified, claim_replicated, conformance_resolved, or retracted.
	Reason string `json:"reason"`
	// EvidenceRef is the Evidence or Run digest backing this XP entry.
	EvidenceRef string `json:"evidence_ref"`
	// CandidateRef is the EvolutionCandidate or ResearchClaim digest that earned
	// or lost this XP.
	CandidateRef string `json:"candidate_ref,omitempty"`
	// RuleVersion is the semantic version of the pure XP projection function ("1.0.0").
	RuleVersion string `json:"rule_version"`
	// EffectiveAt is the RFC 3339 UTC timestamp of the underlying verification or retraction.
	EffectiveAt string `json:"effective_at"`
	// RetractedBy names the CounterEvidence digest when this credit was reversed,
	// or null/empty while active.
	RetractedBy string `json:"retracted_by,omitempty"`
}

// XPBalance summarizes one principal's non-transferable verified XP across
// skill domains (HIP-1402 §1).
type XPBalance struct {
	// Principal is the human or agent identity whose verified reputation this summarizes.
	Principal string `json:"principal"`
	// TotalXP is the sum of net verified XP across all six domains.
	TotalXP int `json:"total_xp"`
	// Domains maps each active skill domain (harness, engine, model, sandbox,
	// science, spec) to its net verified integer XP.
	Domains map[string]int `json:"domains"`
}

// xpIn filters the read-only Hanzo XP projection via query parameters.
type xpIn struct {
	// Project narrows the projection to one project; empty folds the whole org.
	Project string `json:"project"`
	// Principal narrows to one principal (did:hanzo:... or user id).
	Principal string `json:"principal"`
	// Domain narrows to one skill domain (harness, engine, model, sandbox, science, spec).
	Domain string `json:"domain"`
}

// xpOut is the deterministic HIP-1402 Hanzo XP read projection.
type xpOut struct {
	// RuleVersion is the semantic version of the deterministic projection rules ("1.0.0").
	RuleVersion string `json:"rule_version"`
	// NonTransferable is always true: Hanzo XP is soulbound reputation, never a
	// transferable token or currency (HIP-1402 §1).
	NonTransferable bool `json:"non_transferable"`
	// Balances lists per-principal domain XP totals sorted by total_xp descending.
	Balances []XPBalance `json:"balances"`
	// Projections lists every deterministic XPProjection event folded into Balances.
	Projections []XPProjection `json:"projections"`
}

// GetResearchXP computes the deterministic, non-transferable Hanzo XP read
// projection (HIP-1402) from canonical qualified EvolutionCandidates and
// independently replicated confirmatory ResearchClaims. Unqualified proposals,
// exploratory runs, and unreplicated claims yield strictly zero XP, and any
// retracted claim or candidate emits a compensating negative projection that
// reverses the credit to net zero.
func (o ops) getXP(ctx context.Context, in *xpIn) (*xpOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	wantDom := strings.TrimSpace(strings.ToLower(in.Domain))
	if wantDom != "" && !xpDomains[wantDom] {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "domain must be harness, engine, model, sandbox, science, or spec")
	}
	cands, err := latest[candidateRecord](ctx, st, kindCandidate)
	if err != nil {
		return nil, o.failed("xp candidate read", org, err)
	}
	claims, err := latest[claimRecord](ctx, st, kindClaim)
	if err != nil {
		return nil, o.failed("xp claim read", org, err)
	}
	regs, err := latest[regressionRecord](ctx, st, kindRegression)
	if err != nil {
		return nil, o.failed("xp regression read", org, err)
	}

	out := projectXP(cands, claims, regs, strings.TrimSpace(in.Project), strings.TrimSpace(in.Principal), wantDom)
	return &out, nil
}

func projectXP(cands []candidateRecord, claims []claimRecord, regs []regressionRecord, wantProj, wantPrincipal, wantDom string) xpOut {
	// Order chronologically by Seq ascending so projections fold deterministically.
	slices.SortFunc(cands, func(a, b candidateRecord) int { return int(a.Seq - b.Seq) })
	slices.SortFunc(claims, func(a, b claimRecord) int { return int(a.Seq - b.Seq) })
	slices.SortFunc(regs, func(a, b regressionRecord) int { return int(a.Seq - b.Seq) })

	var projs []XPProjection
	appendEntry := func(p XPProjection) {
		if wantPrincipal != "" && !samePrincipal(p.Principal, wantPrincipal) {
			return
		}
		if wantDom != "" && p.Domain != wantDom {
			return
		}
		p.ProjectionID = "sha256:" + hashJoin(p.EvidenceRef, p.Principal, p.Domain, p.Reason, p.CandidateRef, p.RuleVersion)
		projs = append(projs, p)
	}

	// 1. Qualified / Promoted EvolutionCandidates (HIP-1401 -> HIP-1402).
	for _, c := range cands {
		if wantProj != "" && c.Project != wantProj {
			continue
		}
		if !c.WasQualified {
			continue // No XP for unqualified activity (HIP-1402 §1).
		}
		domain := mutationSurfaces[c.MutationSurface]
		if domain == "" {
			domain = "engine"
		}
		baseXP := 100.0
		if c.ProductFitness != nil {
			baseXP += 100.0 * math.Max(0, math.Min(1, c.ProductFitness.VerifiedSuccess))
		}
		evRef := c.CandidateID
		if len(c.EvidenceRefs) > 0 {
			evRef = c.EvidenceRefs[0]
		}
		contribs := c.Contributors
		if len(contribs) == 0 {
			contribs = []AttributionShare{{Principal: c.Proposer, Weight: 1}}
		}
		for _, sh := range contribs {
			award := int(math.Round(baseXP * sh.Weight))
			if award <= 0 {
				continue
			}
			pr := canonicalDID(sh.Principal)
			appendEntry(XPProjection{
				Principal:    pr,
				Domain:       domain,
				Delta:        award,
				Reason:       "candidate_qualified",
				EvidenceRef:  evRef,
				CandidateRef: c.CandidateID,
				RuleVersion:  xpRuleVersion,
				EffectiveAt:  firstNonEmpty(c.QualifiedAt, c.CreatedAt),
				RetractedBy:  c.RetractedBy,
			})
			if c.Status == "retracted" {
				appendEntry(XPProjection{
					Principal:    pr,
					Domain:       domain,
					Delta:        -award,
					Reason:       "retracted",
					EvidenceRef:  firstNonEmpty(c.RetractedBy, evRef),
					CandidateRef: c.CandidateID,
					RuleVersion:  xpRuleVersion,
					EffectiveAt:  firstNonEmpty(c.QualifiedAt, c.CreatedAt),
					RetractedBy:  c.RetractedBy,
				})
			}
		}
	}

	// 2. Independently Replicated Confirmatory ResearchClaims (HIP-1403 -> HIP-1402).
	for _, cl := range claims {
		if wantProj != "" && cl.Project != wantProj {
			continue
		}
		if cl.Mode != "confirmatory" || !cl.WasReplicated {
			continue // Exploratory and unreplicated claims earn 0 XP (HIP-1402 §1 & HIP-1403 §3.1).
		}
		evRef := cl.RunID
		if len(cl.EvidenceRefs) > 0 {
			evRef = cl.EvidenceRefs[0]
		}
		retractedBy := ""
		if cl.CounterEvidence != nil {
			retractedBy = cl.CounterEvidence.EvidenceRef
		}
		contribs := cl.Contributors
		if len(contribs) == 0 {
			contribs = []AttributionShare{{Principal: cl.Proposer, Weight: 1}}
		}
		for _, sh := range contribs {
			award := int(math.Round(100.0 * sh.Weight))
			if award <= 0 {
				continue
			}
			pr := canonicalDID(sh.Principal)
			appendEntry(XPProjection{
				Principal:    pr,
				Domain:       "science",
				Delta:        award,
				Reason:       "claim_replicated",
				EvidenceRef:  evRef,
				CandidateRef: cl.ClaimID,
				RuleVersion:  xpRuleVersion,
				EffectiveAt:  cl.UpdatedAt,
				RetractedBy:  retractedBy,
			})
			if cl.Status == "retracted" {
				appendEntry(XPProjection{
					Principal:    pr,
					Domain:       "science",
					Delta:        -award,
					Reason:       "retracted",
					EvidenceRef:  firstNonEmpty(retractedBy, evRef),
					CandidateRef: cl.ClaimID,
					RuleVersion:  xpRuleVersion,
					EffectiveAt:  cl.UpdatedAt,
					RetractedBy:  retractedBy,
				})
			}
		}
		// Independent verifiers whose replication agreed earn 50 science XP (also reversed if claim retracted).
		seenVerifier := map[string]bool{}
		for _, rep := range cl.Replications {
			if !rep.Agreed {
				continue
			}
			vp := canonicalDID(rep.Verifier)
			if seenVerifier[vp] {
				continue
			}
			seenVerifier[vp] = true
			appendEntry(XPProjection{
				Principal:    vp,
				Domain:       "science",
				Delta:        50,
				Reason:       "claim_replicated",
				EvidenceRef:  rep.RunID,
				CandidateRef: cl.ClaimID,
				RuleVersion:  xpRuleVersion,
				EffectiveAt:  rep.ReplicatedAt,
				RetractedBy:  retractedBy,
			})
			if cl.Status == "retracted" {
				appendEntry(XPProjection{
					Principal:    vp,
					Domain:       "science",
					Delta:        -50,
					Reason:       "retracted",
					EvidenceRef:  firstNonEmpty(retractedBy, rep.RunID),
					CandidateRef: cl.ClaimID,
					RuleVersion:  xpRuleVersion,
					EffectiveAt:  cl.UpdatedAt,
					RetractedBy:  retractedBy,
				})
			}
		}
	}

	// 3. Resolved Conformance / Regression Fixes (HIP-1404 -> HIP-1402).
	candByID := map[string]candidateRecord{}
	for _, c := range cands {
		candByID[c.CandidateID] = c
	}
	for _, rg := range regs {
		if wantProj != "" && rg.Project != wantProj {
			continue
		}
		if rg.Status != "fixed" || rg.FixCandidateRef == "" {
			continue
		}
		fix, ok := candByID[rg.FixCandidateRef]
		if !ok || !fix.WasQualified || fix.Status == "retracted" {
			continue
		}
		dom := "harness"
		if rg.SourceKind == "conformance_failure" {
			dom = "spec"
		}
		appendEntry(XPProjection{
			Principal:    canonicalDID(fix.Proposer),
			Domain:       dom,
			Delta:        50,
			Reason:       "conformance_resolved",
			EvidenceRef:  rg.RegressionID,
			CandidateRef: fix.CandidateID,
			RuleVersion:  xpRuleVersion,
			EffectiveAt:  rg.UpdatedAt,
		})
	}

	// Fold Projections into per-principal domain balances.
	byPrincipal := map[string]map[string]int{}
	for _, p := range projs {
		dm := byPrincipal[p.Principal]
		if dm == nil {
			dm = map[string]int{}
			byPrincipal[p.Principal] = dm
		}
		dm[p.Domain] += p.Delta
	}
	balances := make([]XPBalance, 0, len(byPrincipal))
	for pr, dm := range byPrincipal {
		tot := 0
		for _, v := range dm {
			tot += v
		}
		balances = append(balances, XPBalance{
			Principal: pr,
			TotalXP:   tot,
			Domains:   dm,
		})
	}
	sort.Slice(balances, func(i, j int) bool {
		if balances[i].TotalXP != balances[j].TotalXP {
			return balances[i].TotalXP > balances[j].TotalXP
		}
		return balances[i].Principal < balances[j].Principal
	})
	if projs == nil {
		projs = []XPProjection{}
	}
	return xpOut{
		RuleVersion:     xpRuleVersion,
		NonTransferable: true,
		Balances:        balances,
		Projections:     projs,
	}
}

// ── HIP-1404: Errors Become Fixes (Regression Gate Loop) ──────────────────────

// RegressionRecord captures one production error, evaluation failure, or
// conformance failure and binds it to the deterministic gate test that
// reproduces it and the qualified EvolutionCandidate that fixes it (HIP-1404).
type RegressionRecord struct {
	// Project is the sub-scope inside the org this regression belongs to, set by
	// the server on write.
	Project string `json:"project"`
	// RegressionID is the content-addressed sha256:<hex> identity of the captured failure.
	RegressionID string `json:"regression_id"`
	// SourceKind classifies where the failure originated: eval_failure,
	// production_error, counter_evidence, or conformance_failure.
	SourceKind string `json:"source_kind"`
	// ErrorSignature is the normalized failure fingerprint or invariant identifier.
	ErrorSignature string `json:"error_signature"`
	// Summary describes the failure mode and reproduction condition.
	Summary string `json:"summary"`
	// FailingRunID is the optional Run ID or trace digest that exhibited the error.
	FailingRunID string `json:"failing_run_id,omitempty"`
	// GateTestRef names the deterministic regression test added to the evaluation
	// gate so the failure can never recur silently.
	GateTestRef string `json:"gate_test_ref,omitempty"`
	// FixCandidateRef names the qualified EvolutionCandidate (sha256:...) that
	// resolved the regression.
	FixCandidateRef string `json:"fix_candidate_ref,omitempty"`
	// Status is captured (error recorded), gated (reproduction test wired), or
	// fixed (qualified candidate verified against the gate test).
	Status string `json:"status"`
	// ReportedBy is the principal or daemon that captured the failure.
	ReportedBy string `json:"reported_by"`
	// CreatedAt is the RFC 3339 UTC timestamp when the regression was first captured.
	CreatedAt string `json:"created_at"`
	// UpdatedAt is the RFC 3339 UTC timestamp of the latest transition.
	UpdatedAt string `json:"updated_at"`
	// Seq is the store's monotone append sequence number for this version.
	Seq int64 `json:"seq"`
}

type regressionRecord struct {
	Project         string `json:"project"`
	RegressionID    string `json:"regression_id"`
	ContentHash     string `json:"content_hash"`
	SourceKind      string `json:"source_kind"`
	ErrorSignature  string `json:"error_signature"`
	Summary         string `json:"summary"`
	FailingRunID    string `json:"failing_run_id,omitempty"`
	GateTestRef     string `json:"gate_test_ref,omitempty"`
	FixCandidateRef string `json:"fix_candidate_ref,omitempty"`
	Status          string `json:"status"`
	ReportedBy      string `json:"reported_by"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
	Seq             int64  `json:"seq"`
}

func (r regressionRecord) stable() string { return r.Project + "\x00" + r.RegressionID }
func (r regressionRecord) key() string {
	return "rg:" + hashJoin(r.Project, r.RegressionID, r.ContentHash)
}
func (r regressionRecord) clock() int64 { return r.Seq }
func (r regressionRecord) gone() bool   { return false }

func (r *regressionRecord) seal() {
	r.ContentHash = hashJoin(r.RegressionID, r.GateTestRef, r.FixCandidateRef, r.Status)
}

func (r regressionRecord) wire() RegressionRecord {
	return RegressionRecord{
		Project:         r.Project,
		RegressionID:    r.RegressionID,
		SourceKind:      r.SourceKind,
		ErrorSignature:  r.ErrorSignature,
		Summary:         r.Summary,
		FailingRunID:    r.FailingRunID,
		GateTestRef:     r.GateTestRef,
		FixCandidateRef: r.FixCandidateRef,
		Status:          r.Status,
		ReportedBy:      r.ReportedBy,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
		Seq:             r.Seq,
	}
}

// RecordResearchRegression captures or advances a HIP-1404 error-to-fix
// regression record. Supplying gate_test_ref advances status to gated; supplying
// a qualified fix_candidate_ref advances status to fixed.
//
// Example: {"source_kind": "conformance_failure", "error_signature": "hip-1403:posthoc-claim", "summary": "Harness attempted confirmatory claim before hypothesis registration", "gate_test_ref": "gate://research/posthoc_test"}
func (o ops) postRegression(ctx context.Context, in *RegressionRecord) (*RegressionRecord, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := holdable(in); err != nil {
		return nil, err
	}
	proj := project(ctx)
	src := strings.TrimSpace(strings.ToLower(in.SourceKind))
	sig := strings.TrimSpace(in.ErrorSignature)
	sum := strings.TrimSpace(in.Summary)
	if !regressionSources[src] {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "source_kind must be eval_failure, production_error, counter_evidence, or conformance_failure")
	}
	if sig == "" || sum == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "regression requires error_signature and summary")
	}
	rid := "sha256:" + hashJoin(src, sig)
	if sent := strings.TrimSpace(in.RegressionID); sent != "" && !strings.EqualFold(sent, rid) {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "regression_id mismatch: canonical signature hashes to %s", rid)
	}

	gateRef := strings.TrimSpace(in.GateTestRef)
	fixRef := strings.TrimSpace(in.FixCandidateRef)
	status := "captured"
	if gateRef != "" {
		status = "gated"
	}

	var saved regressionRecord
	err = o.inTx(ctx, st, org, "regression write", func(tx orm.DB) error {
		createdAt := now()
		if cur, ok, err := regressionInTx(ctx, tx, proj, rid); err != nil {
			return err
		} else if ok {
			createdAt = cur.CreatedAt
			if gateRef == "" {
				gateRef = cur.GateTestRef
			}
			if fixRef == "" {
				fixRef = cur.FixCandidateRef
			}
			if gateRef != "" {
				status = "gated"
			}
		}
		if fixRef != "" {
			if gateRef == "" {
				return zip.Errorf(http.StatusUnprocessableEntity, "regression cannot be marked fixed without a gate_test_ref reproducing the error (HIP-1404)")
			}
			cand, cok, err := candidateInTx(ctx, tx, proj, fixRef)
			if err != nil {
				return err
			}
			if !cok || !cand.WasQualified || cand.Status == "retracted" {
				return zip.Errorf(http.StatusUnprocessableEntity, "fix_candidate_ref %q must be an existing qualified non-retracted EvolutionCandidate", fixRef)
			}
			status = "fixed"
		}
		rec := regressionRecord{
			Project:         proj,
			RegressionID:    rid,
			SourceKind:      src,
			ErrorSignature:  sig,
			Summary:         sum,
			FailingRunID:    strings.TrimSpace(in.FailingRunID),
			GateTestRef:     gateRef,
			FixCandidateRef: fixRef,
			Status:          status,
			ReportedBy:      canonicalDID(actorPrincipal(ctx)),
			CreatedAt:       createdAt,
			UpdatedAt:       now(),
		}
		rec.seal()
		if _, err := appendTx(ctx, tx, kindRegression, []regressionRecord{rec}, func(r *regressionRecord, seq int64) { r.Seq = seq }); err != nil {
			return err
		}
		return tx.Get(ctx, tx.NewKey(kindRegression, rec.key(), 0, nil), &saved)
	})
	if err != nil {
		return nil, err
	}
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	out := saved.wire()
	return &out, nil
}

// regressionsIn filters a regression listing via query parameters.
type regressionsIn struct {
	// Project narrows to one project; empty reads across every project in the org.
	Project string `json:"project"`
	// Status narrows to captured, gated, or fixed.
	Status string `json:"status"`
}

// regressionsOut is a canonical listing of HIP-1404 RegressionRecords.
type regressionsOut struct {
	// Data are the canonical RegressionRecords, newest sequence number first.
	Data []RegressionRecord `json:"data"`
	// Total is len(data).
	Total int `json:"total"`
}

// ListResearchRegressions returns the org's canonical HIP-1404 RegressionRecords
// newest-first.
func (o ops) listRegressions(ctx context.Context, in *regressionsIn) (*regressionsOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	recs, err := latest[regressionRecord](ctx, st, kindRegression)
	if err != nil {
		return nil, o.failed("regression read", org, err)
	}
	wantProj := strings.TrimSpace(in.Project)
	wantStatus := strings.TrimSpace(strings.ToLower(in.Status))
	out := []RegressionRecord{}
	for _, r := range recs {
		if wantProj != "" && r.Project != wantProj {
			continue
		}
		if wantStatus != "" && r.Status != wantStatus {
			continue
		}
		out = append(out, r.wire())
	}
	return &regressionsOut{Data: out, Total: len(out)}, nil
}

// ── route registration & pure helpers ─────────────────────────────────────────

func doRoutes(app cloud.Router, o ops) {
	g := app.Group("/v1/research")
	g.Get("/standard", o.getStandard)
	g.Post("/hypotheses", o.postHypothesis)
	g.Get("/hypotheses", o.listHypotheses)
	g.Post("/claims", o.postClaim)
	g.Get("/claims", o.listClaims)
	g.Post("/claims/:id/replicate", o.replicateClaim)
	g.Post("/claims/:id/retract", o.retractClaim)
	g.Post("/candidates", o.postCandidate)
	g.Get("/candidates", o.listCandidates)
	g.Get("/candidates/frontier", o.getCandidateFrontier)
	g.Post("/candidates/:id/qualify", o.qualifyCandidate)
	g.Post("/candidates/:id/retract", o.retractCandidate)
	g.Get("/xp", o.getXP)
	g.Post("/regressions", o.postRegression)
	g.Get("/regressions", o.listRegressions)
}

func actorPrincipal(ctx context.Context) string {
	u := by(ctx)
	if u != "" {
		return u
	}
	if org := strings.TrimSpace(cloud.Who(ctx).Org); org != "" {
		return org
	}
	return ""
}

func canonicalDID(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "did:") {
		return p
	}
	return "did:hanzo:" + p
}

func barePrincipal(p string) string {
	p = strings.TrimSpace(strings.ToLower(p))
	p = strings.TrimPrefix(p, "did:hanzo:")
	return p
}

func samePrincipal(a, b string) bool {
	ba, bb := barePrincipal(a), barePrincipal(b)
	return ba != "" && ba == bb
}

func normalizePrincipal(given, actor string) string {
	g := strings.TrimSpace(given)
	if g != "" {
		return canonicalDID(g)
	}
	return canonicalDID(actor)
}

func normalizeContributors(in []AttributionShare, fallback string) ([]AttributionShare, error) {
	if len(in) == 0 {
		return []AttributionShare{{Principal: fallback, Weight: 1.0}}, nil
	}
	out := make([]AttributionShare, 0, len(in))
	sum := 0.0
	for _, s := range in {
		pr := canonicalDID(s.Principal)
		if pr == "" {
			return nil, fmt.Errorf("contributor principal cannot be empty")
		}
		if s.Weight <= 0 || s.Weight > 1 || math.IsNaN(s.Weight) || math.IsInf(s.Weight, 0) {
			return nil, fmt.Errorf("contributor weight %.4f must be in (0, 1]", s.Weight)
		}
		sum += s.Weight
		out = append(out, AttributionShare{Principal: pr, Weight: s.Weight})
	}
	if math.Abs(sum-1.0) > 1e-6 {
		return nil, fmt.Errorf("contributor attribution weights must sum to 1.0 (got %.6f)", sum)
	}
	return out, nil
}

func hasContributor(cs []AttributionShare, principal string) bool {
	for _, c := range cs {
		if samePrincipal(c.Principal, principal) {
			return true
		}
	}
	return false
}

func validateProductFitness(pf *ProductFitness) error {
	if pf == nil {
		return nil
	}
	for name, v := range map[string]float64{
		"verified_success": pf.VerifiedSuccess,
		"robustness":       pf.Robustness,
		"safety":           pf.Safety,
	} {
		if math.IsNaN(v) || v < 0 || v > 1 {
			return fmt.Errorf("product_fitness.%s must be in [0.0, 1.0]", name)
		}
	}
	for name, v := range map[string]float64{
		"cost_per_success_usd": pf.CostPerSuccessUSD,
		"latency_p50_ms":       pf.LatencyP50Ms,
		"latency_p95_ms":       pf.LatencyP95Ms,
	} {
		if math.IsNaN(v) || v < 0 {
			return fmt.Errorf("product_fitness.%s must be non-negative", name)
		}
	}
	return nil
}

func filterCandidates(recs []candidateRecord, in *candidatesIn) []EvolutionCandidate {
	wantProj := strings.TrimSpace(in.Project)
	wantID := strings.TrimSpace(in.ID)
	wantSurf := strings.TrimSpace(in.MutationSurface)
	wantStatus := strings.TrimSpace(strings.ToLower(in.Status))
	out := []EvolutionCandidate{}
	for _, r := range recs {
		if wantProj != "" && r.Project != wantProj {
			continue
		}
		if wantID != "" && r.CandidateID != wantID {
			continue
		}
		if wantSurf != "" && r.MutationSurface != wantSurf {
			continue
		}
		if wantStatus != "" && r.Status != wantStatus {
			continue
		}
		out = append(out, r.wire())
	}
	return out
}

// dominates reports whether candidate a Pareto-dominates b on the HIP-1401 §3.1
// objectives: higher verified_success, lower cost_per_success_usd, lower
// latency_p50_ms, higher robustness, higher safety.
func dominates(a, b ProductFitness) bool {
	atLeast := a.VerifiedSuccess >= b.VerifiedSuccess &&
		a.CostPerSuccessUSD <= b.CostPerSuccessUSD &&
		a.LatencyP50Ms <= b.LatencyP50Ms &&
		a.Robustness >= b.Robustness &&
		a.Safety >= b.Safety
	if !atLeast {
		return false
	}
	return a.VerifiedSuccess > b.VerifiedSuccess ||
		a.CostPerSuccessUSD < b.CostPerSuccessUSD ||
		a.LatencyP50Ms < b.LatencyP50Ms ||
		a.Robustness > b.Robustness ||
		a.Safety > b.Safety
}

func paretoFrontier(pool []EvolutionCandidate) []EvolutionCandidate {
	out := []EvolutionCandidate{}
	for i, cand := range pool {
		dominated := false
		for j, other := range pool {
			if i == j {
				continue
			}
			if dominates(*other.ProductFitness, *cand.ProductFitness) {
				dominated = true
				break
			}
		}
		if !dominated {
			out = append(out, cand)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		fi, fj := out[i].ProductFitness, out[j].ProductFitness
		if fi.VerifiedSuccess != fj.VerifiedSuccess {
			return fi.VerifiedSuccess > fj.VerifiedSuccess
		}
		if fi.CostPerSuccessUSD != fj.CostPerSuccessUSD {
			return fi.CostPerSuccessUSD < fj.CostPerSuccessUSD
		}
		return out[i].CandidateID < out[j].CandidateID
	})
	return out
}

func replicationAgrees(v1 float64, lo1, hi1 *float64, v2 float64, lo2, hi2 *float64, tol float64) bool {
	if lo1 != nil && hi1 != nil && lo2 != nil && hi2 != nil {
		return *lo1 <= *hi2 && *lo2 <= *hi1
	}
	if lo1 != nil && hi1 != nil {
		return v2 >= *lo1 && v2 <= *hi1
	}
	if lo2 != nil && hi2 != nil {
		return v1 >= *lo2 && v1 <= *hi2
	}
	scale := math.Max(1.0, math.Abs(v1))
	return math.Abs(v1-v2) <= tol*scale
}

func canonicalRunInTx(ctx context.Context, tx orm.DB, proj, id string) (runRecord, bool, error) {
	var recs []runRecord
	if _, err := tx.Query(kindRun).GetAll(ctx, &recs); err != nil {
		return runRecord{}, false, err
	}
	canonMap := canonical(recs)
	if r, ok := canonMap[proj+"\x00"+id]; ok {
		return r, true, nil
	}
	for _, r := range canonMap {
		if r.ID == id {
			return r, true, nil
		}
	}
	return runRecord{}, false, nil
}

func hypothesisInTx(ctx context.Context, tx orm.DB, proj, id string) (hypothesisRecord, bool, error) {
	var recs []hypothesisRecord
	if _, err := tx.Query(kindHypothesis).GetAll(ctx, &recs); err != nil {
		return hypothesisRecord{}, false, err
	}
	canonMap := canonical(recs)
	if r, ok := canonMap[proj+"\x00"+id]; ok {
		return r, true, nil
	}
	for _, r := range canonMap {
		if r.HypothesisID == id {
			return r, true, nil
		}
	}
	return hypothesisRecord{}, false, nil
}

func claimInTx(ctx context.Context, tx orm.DB, proj, id string) (claimRecord, bool, error) {
	var recs []claimRecord
	if _, err := tx.Query(kindClaim).GetAll(ctx, &recs); err != nil {
		return claimRecord{}, false, err
	}
	canonMap := canonical(recs)
	if r, ok := canonMap[proj+"\x00"+id]; ok {
		return r, true, nil
	}
	for _, r := range canonMap {
		if r.ClaimID == id {
			return r, true, nil
		}
	}
	return claimRecord{}, false, nil
}

func candidateInTx(ctx context.Context, tx orm.DB, proj, id string) (candidateRecord, bool, error) {
	var recs []candidateRecord
	if _, err := tx.Query(kindCandidate).GetAll(ctx, &recs); err != nil {
		return candidateRecord{}, false, err
	}
	canonMap := canonical(recs)
	if r, ok := canonMap[proj+"\x00"+id]; ok {
		return r, true, nil
	}
	for _, r := range canonMap {
		if r.CandidateID == id {
			return r, true, nil
		}
	}
	return candidateRecord{}, false, nil
}

func regressionInTx(ctx context.Context, tx orm.DB, proj, id string) (regressionRecord, bool, error) {
	var recs []regressionRecord
	if _, err := tx.Query(kindRegression).GetAll(ctx, &recs); err != nil {
		return regressionRecord{}, false, err
	}
	canonMap := canonical(recs)
	if r, ok := canonMap[proj+"\x00"+id]; ok {
		return r, true, nil
	}
	for _, r := range canonMap {
		if r.RegressionID == id {
			return r, true, nil
		}
	}
	return regressionRecord{}, false, nil
}

func measureFromRun(r Run, metric string) (float64, *float64, *float64, bool) {
	for _, m := range r.Measures {
		if m.Metric == metric && (m.Category == "all" || m.Category == "") {
			return m.Value, m.Lo, m.Hi, true
		}
	}
	for _, m := range r.Measures {
		if m.Metric == metric {
			return m.Value, m.Lo, m.Hi, true
		}
	}
	return 0, nil, nil, false
}

func costFromRun(r Run) (float64, bool) {
	for _, m := range r.Measures {
		if m.Metric == "cost_usd" && (m.Category == "all" || m.Category == "") {
			return m.Value, true
		}
	}
	return 0, false
}

func cleanStrings(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if s := strings.TrimSpace(x); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func nonNilStrings(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func mergeStrings(a, b []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, x := range append(slices.Clone(a), b...) {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}
