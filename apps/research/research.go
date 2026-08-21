// Package research is every experiment you have ever run, kept and comparable.
//
// The R&D EVIDENCE plane (HIP-0512 §"Hanzo Research"). Every experiment across
// every product — a benchmark run is ONE kind; kernel-perf (hanzo-engine),
// training (hanzo-ml), ablations, and policy-evals are the others — accrues
// here as VERSIONED, append-only evidence under one discriminator, kind ∈
// benchmark | kernel-perf | training | ablation | policy-eval.
//
// Two planes, never one (HIP-0512): each org's transactional SQLite (store.go, per
// HIP-0302, physically file-isolated) is the local source of truth; it rolls up into
// hanzoai/datastore — the column-oriented OLAP aggregate plane (datastore.go) — for the
// unified cross-project query surface. Durability today is VERSIONED · APPEND-ONLY;
// cloud mirroring is ROLLING OUT (the roll-up is best-effort; retry + reconciliation +
// replication + backup/restore are still on the critical path, so the stronger
// "immutable · replicated · recovery-tested" claim is NOT yet made).
//
// Versioned, not write-once: a correction APPENDS a new version under the same stable id
// and the prior version is RETAINED (superseded), never mutated. RETAINED is the full
// history (the truth); CANONICAL is the deterministic deduped view over it — so dedup
// never reads as loss. faulted/failed runs are retained (negative results are evidence).
// Ingest is idempotent by content (measurement + provenance) — re-running the backfill
// appends nothing.
//
// Provenance is first-class + queryable: project, git (sha/branch/dirty), and
// lib_versions travel as columns/structured fields, so the board answers "which lib
// version regressed LiveCodeBench" over the longitudinal record.
//
// Private-by-default: an upload records visibility='private' and grants NO training or
// commons-publication rights. Public board visibility, training rights, and commons
// publication are each a SEPARATE authorized grant (POST /v1/research/grants), never
// implied by uploading a run.
//
// Surface (every route org-scoped by the validated principal; project is a sub-
// dimension column, so the org's ops board reads across its projects in one query):
//
//	POST /v1/research/experiments   ingest a batch (idempotent) → SQLite → roll up
//	GET  /v1/research/experiments   list canonical (?project= ?kind=)
//	GET  /v1/research/projects      every project + real totals (canonical + retained)
//	GET  /v1/research/totals        headline aggregate + per-kind (?project=)
//	POST /v1/research/grants        set visibility/consent for a stable id (separate auth)
//	POST /v1/research/artifacts     record a diary artifact, content-addressed
//	GET  /v1/research/artifacts     the diary feed, newest first (metadata only)
//	GET  /v1/research/artifacts/:sha256  the artifact's bytes, by content hash
//
// Its own binary (plugin/research) states Name/Price/Mount; the host learns the
// prefix from its manifest row.
package research

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// maxArtifactBytes bounds one diary artifact's content (the server hashes + stores the
// bytes to content-address it). A board snapshot is well under this.
const maxArtifactBytes = 16 << 20

// maxBatchItems bounds one ingest so a single request cannot pin the writer with an
// unbounded batch. The uploader chunks well under this; the global body limit is the
// coarse guard, this is the honest per-batch one.
const maxBatchItems = 20000

// visibilities is the allowed set for a visibility grant. private is the default and the
// only value an upload ever records; org/public require the separate grant.
var visibilities = map[string]bool{"private": true, "org": true, "public": true}

// Experiment is one versioned run record, keyed by its stable id (<kind>:<subject>:
// <task>) within its project. Ingest reads the measurement + revision/status +
// provenance (git*, lib_versions); it IGNORES visibility/trainable/publishable (forced
// private/withheld — a grant is separate). Reads return the canonical version with its
// derived Canonical flag and current visibility/consent. TS (unix seconds) orders
// versions. Endpoint is the OPTIONAL BYO arm the run measured — SSRF-gated at ingest.
type Experiment struct {
	// Project is the sub-scope inside the org this run is filed under. On ingest
	// it is the SERVER's value — the caller's project scope — and a project sent
	// in the body is ignored; on a read it is the value the run was filed under.
	Project string `json:"project"`
	// ID is the run's stable id, `<kind>:<subject>:<task>` by convention. It is
	// the key versions accrue under: a correction re-sent with this same id
	// appends a new version beside the old one rather than replacing it.
	// Required on ingest.
	ID string `json:"id"`
	// Revision is the producer's declared kind of this version: original,
	// corrected or retracted. Anything else, including empty, is read as
	// original. A retracted latest version WITHDRAWS the id — it has no canonical
	// version at all — and every earlier version is still retained.
	Revision string `json:"revision"`
	// Status is the run's execution state: planning, running, complete or
	// faulted. Empty is complete (a backfilled historic run). An unrecognized
	// value is kept verbatim rather than flattened, so a producer's own failure
	// label is never displayed as a success. faulted and failed runs are retained
	// but are left out of the answered headline counts.
	Status string `json:"status"`
	// Canonical is DERIVED on read, never stored and never taken from an upload:
	// a version is canonical when it is the latest-appended non-retracted version
	// of its stable id. The listing returns only canonical versions, so it is
	// true on every row it answers with.
	Canonical bool `json:"canonical"`
	// Visibility is private, org or public. Ingest forces private whatever the
	// body says; only a grant moves it.
	Visibility string `json:"visibility"`
	// Trainable is consent to train on this run. Ingest forces false; only a
	// grant sets it. An upload conveys no training right.
	Trainable bool `json:"trainable"`
	// Publishable is consent to publish this run to the commons. Ingest forces
	// false; only a grant sets it.
	Publishable bool `json:"publishable"`
	// Kind is the discriminator the whole plane is partitioned by: benchmark,
	// kernel-perf, training, ablation, policy-eval, or ab for an A/B trial's
	// per-arm evidence. Required on ingest, and open — an unknown kind is stored,
	// not refused.
	Kind string `json:"kind"`
	// Subject is what was measured: the model, the engine, the A/B arm. Required
	// on ingest.
	Subject string `json:"subject"`
	// Task is what it was measured ON — gpqa_diamond, livecodebench, prefill, or
	// the trial's id for A/B evidence.
	Task string `json:"task"`
	// Metric names the quantity Value carries — accuracy, tok/s, a conversion
	// event. It is the only place Value's unit is stated; this plane declares no
	// vocabulary of metrics.
	Metric string `json:"metric"`
	// Value is the measurement, in whatever unit Metric names. It is NEVER
	// normalized: one producer files accuracy as 0.81 and another as 94.3 and
	// both are stored as sent, so compare values only within one (subject, task,
	// metric).
	Value float64 `json:"value"`
	// N is the sample size behind Value — items scored, or exposures for an A/B
	// arm.
	N int `json:"n"`
	// NTotal is the denominator the producer reports N against — the size of the
	// set N was drawn from. The plane derives neither from the other, so a
	// producer that reports only N leaves this 0.
	NTotal int `json:"n_total"`
	// CostUSD is what the run cost in US DOLLARS (not cents), as the producer
	// reports it. It is summed as-is into the per-project and per-kind totals.
	CostUSD float64 `json:"cost_usd"`
	// Meta is whatever else the producer wants to carry, as a JSON object — the
	// A/B rows put lift, z and p here. It is part of the version's content
	// identity, canonicalized (sorted keys, no whitespace) first so that a
	// re-serialization of the same object does not mint a spurious version.
	// Absent or unparseable is stored as {}.
	Meta json.RawMessage `json:"meta,omitempty"`
	// GitSHA is the commit the run was measured at. Provenance is part of the
	// version's content identity, so the same number measured on a different
	// commit is a new RETAINED version rather than an idempotent no-op.
	GitSHA string `json:"git_sha"`
	// GitBranch is the branch that commit was on.
	GitBranch string `json:"git_branch"`
	// GitDirty says the working tree carried uncommitted changes, so GitSHA does
	// not fully describe what ran.
	GitDirty bool `json:"git_dirty"`
	// LibVersions is the libraries in force, as a JSON object of name → version
	// ({"harness":"0.1.0"}). It is a queryable column and part of the content
	// identity, which is what makes "which lib version regressed this task"
	// answerable over the longitudinal record. Absent is {}.
	LibVersions json.RawMessage `json:"lib_versions,omitempty"`
	// TS is when the run was measured, unix SECONDS. It is display-only and it
	// does NOT decide supersession — a server-assigned append clock does, so a
	// live correction that sends no ts still supersedes a backfill that stamped
	// one. 0 means the producer sent none.
	TS int64 `json:"ts"`
	// Endpoint is the optional BYO URL naming the arm the run measured. It is
	// checked at ingest — https only, and every address the host resolves to must
	// be publicly routable — and then dropped: nothing stores it, no read returns
	// it, and this plane never dials it.
	Endpoint string `json:"endpoint,omitempty"`
}

// Attempt is one versioned measured attempt on one item, keyed by (project, benchmark,
// item, model). A re-scored/re-run attempt is a new version; the prior is retained.
// response is the raw artifact (own retention class; may carry personal/licensed
// material). status=faulted retains a negative result.
type Attempt struct {
	// Benchmark is the suite the item belongs to — gpqa_diamond, livecodebench.
	// Part of the stable id (project, benchmark, item, model). Required on ingest.
	Benchmark string `json:"benchmark"`
	// Item is the one question or case that was scored, by the benchmark's own
	// id for it. Part of the stable id. Required on ingest.
	Item string `json:"item"`
	// Model is what answered the item. Part of the stable id. Required on ingest.
	Model string `json:"model"`
	// Revision is the producer's declared kind of this version: original,
	// corrected or retracted. Anything else, including empty, is read as
	// original. A retracted latest version withdraws the item's canonical answer;
	// every earlier version stays retained.
	Revision string `json:"revision"`
	// Status is this attempt's execution state, the same vocabulary a run uses:
	// empty is complete, faulted retains a negative result, and an unrecognized
	// value is kept verbatim. faulted and failed are excluded from the answered
	// counts but never dropped.
	Status string `json:"status"`
	// Gold is the expected answer for the item, as the benchmark states it.
	Gold string `json:"gold"`
	// Answer is what the model answered, after the harness extracted it from the
	// raw generation.
	Answer string `json:"answer"`
	// Correct is the producer's grade of Answer against Gold. This plane records
	// the verdict and re-grades nothing, so it is only ever as good as the
	// harness that sent it.
	Correct bool `json:"correct"`
	// Response is the raw generation the model produced. It is its own retention
	// class — it may carry personal or licensed material — and it is part of the
	// version's content identity, so a re-run that produced different text is a
	// new retained version.
	Response string `json:"response"`
	// Source names who measured this attempt. Empty is recorded as
	// hanzo-measured; any other value is stored as sent, so an imported
	// third-party score is distinguishable from one this fleet ran.
	Source string `json:"source"`
	// TS is when the attempt was measured, unix SECONDS. Display-only: a
	// server-assigned append clock, not this, decides which version is canonical.
	// 0 means the producer sent none.
	TS int64 `json:"ts"`
}

// IngestRequest is one upload batch: experiments and their attempts as two flat arrays —
// the shape of the two tables, so the uploader streams rows through with no reshaping.
type IngestRequest struct {
	// Experiments are the run versions in this batch. Each needs an id, a kind
	// and a subject. Re-sending byte-identical content appends nothing.
	Experiments []Experiment `json:"experiments"`
	// Attempts are the per-item attempt versions. Each needs a benchmark, an item
	// and a model. The two arrays may not BOTH be empty, and they hold at most
	// 20000 members between them — split a larger upload.
	Attempts []Attempt `json:"attempts"`
}

// artifactKinds is the allowed set for a diary artifact: a rendered snapshot of the
// canonical board, or a gateway-generated report page.
var artifactKinds = map[string]bool{"snapshot": true, "report": true}

// ResearchArtifact is one research-diary record — a raw-artifact-class record tied to a run: a
// snapshot (a PNG of the canonical board) or a report (a generated page). The caller
// submits the bytes (base64 content); the SERVER hashes them and that sha256 is the
// identity + ref (sha256:<hash>) — never a client-asserted hash or ref, so it is genuinely
// content-addressed and un-poisonable. A re-POST of the same bytes is a no-op. Private by
// default; public only via the separate visibility grant. Carries the same provenance as a
// run.
type ResearchArtifact struct {
	// SHA256 is the SERVER's hash of the submitted bytes, hex, and the artifact's
	// identity — the key the blob route is addressed by. A caller MAY send one;
	// it is not trusted, only checked, and a mismatch with the bytes refuses the
	// write.
	SHA256 string `json:"sha256"`
	// Content is the artifact's bytes, base64, on WRITE only, at most 16 MiB.
	// Never returned: a read answers metadata and the address, and the bytes come
	// from GET /v1/research/artifacts/{sha256}.
	Content string `json:"content,omitempty"`
	// Kind is snapshot or report, and nothing else is accepted. It decides how
	// the bytes are served: image/png for a snapshot, application/octet-stream
	// for a report — from the recorded kind, never from sniffing the bytes.
	Kind string `json:"kind"`
	// Ref is the content address, "sha256:<hash>", composed by the server from
	// the same bytes. A ref sent in the body is discarded, so it can never name
	// somewhere else.
	Ref string `json:"ref"`
	// RunID is the experiment stable id this artifact belongs to, which is what
	// the diary feed's ?run= filters on. It is free text: nothing checks that the
	// run exists.
	RunID string `json:"run_id"`
	// Project is the project the artifact is filed under — the SERVER's value on
	// write. It is part of the read predicate too: fetching a hash from the wrong
	// project is the same 404 as a hash that was never recorded.
	Project string `json:"project"`
	// Visibility is private, org or public. A write forces private; only a grant
	// by sha256 moves it.
	Visibility string `json:"visibility"`
	// RetentionClass is the retention bucket the bytes fall in. Every write is
	// recorded as raw-artifact — the class for material that may be personal or
	// licensed — so it is the server's word, not the caller's.
	RetentionClass string `json:"retention_class"`
	// GitSHA is the commit that produced the artifact. Unlike a run's, an
	// artifact's provenance is NOT part of its identity: the bytes are.
	GitSHA string `json:"git_sha"`
	// GitBranch is the branch that commit was on.
	GitBranch string `json:"git_branch"`
	// GitDirty says the working tree carried uncommitted changes.
	GitDirty bool `json:"git_dirty"`
	// LibVersions is the libraries in force, as a JSON object of name → version.
	// Absent is {}.
	LibVersions json.RawMessage `json:"lib_versions,omitempty"`
	// TS is when the artifact was recorded, unix SECONDS, as the caller gives it
	// — it is what the feed sorts newest-first on and what ?since= is compared
	// against. 0 leaves the run unordered against timestamped siblings.
	TS int64 `json:"ts"`
}

// GrantRequest is a SEPARATE authorization decision for a stable id's records:
// visibility (private/org/public) and the training/commons consent flags. A nil field is
// left unchanged. This is the only path that elevates a record beyond private.
type GrantRequest struct {
	// Project locates WHICH project inside the caller's org holds the target;
	// empty is the caller's own project scope. It is not a tenant key — the org
	// comes from the validated principal — so naming a project can only ever
	// address a record the caller's org already owns.
	Project string `json:"project"`
	// ID addresses an experiment (run) by its stable id. The grant lands on EVERY
	// retained version of that id, because the decision is about the run and not
	// about one version of it.
	ID string `json:"id"`
	// SHA256 addresses an artifact by its content hash instead, and an artifact
	// grant sets visibility only. One of ID and SHA256 is required; when both are
	// sent SHA256 wins and the run is untouched.
	SHA256 string `json:"sha256"`
	// Visibility is private, org or public. Null leaves it unchanged, and it is
	// the one field an artifact grant requires.
	Visibility *string `json:"visibility"`
	// Trainable is consent to train on the run's records. Null leaves it
	// unchanged; an artifact grant ignores it.
	Trainable *bool `json:"trainable"`
	// Publishable is consent to publish the run to the commons. Null leaves it
	// unchanged; an artifact grant ignores it.
	Publishable *bool `json:"publishable"`
}

// maxArtifactPage bounds a diary feed read.
const maxArtifactPage = 500

// runStatus normalizes a run's execution state: empty (backfilled-historic) is
// `complete`; a recognized state passes through; an UNKNOWN non-empty state is preserved
// verbatim so a producer's `faulted` is never silently shown as `complete` (masking a
// failure is worse than an unrecognized label).
func runStatus(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "complete"
	}
	return s
}

type state struct {
	stores *cloud.OrgStore[*store]
	wh     *warehouse
}

func Mount(app cloud.Router, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "research", build, routes)
}

func build(b cloud.Base) (state, error) {
	// Each org's research.db rides the HA path the deployment configured (ha-elected
	// single writer + hydrate-on-open + fenced ship). b.Durable is nil on a local/dev
	// deployment, where the store is exactly the pre-durability local cache.
	return state{
		stores: cloud.NewOrgStore(b, "research", openStore),
		wh:     &warehouse{},
	}, nil
}

// Shutdown closes every open per-org store (registered as the subsystem's Shutdown in
// apps.go), so a graceful stop flushes and releases each org's SQLite file.
func Shutdown() error { return shutdownStores() }

// zipdoc lifts the doc comment off each typed op — and off each field of its In
// and Out — into zipdoc_gen.go, which hands them to zip.Describe at init. Go
// drops comments at compile time, so this build-time pass is the ONLY way that
// prose reaches the published document, the MCP tool list and the generated SDKs.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops carries the subsystem's state onto every typed op. A typed handler takes a
// context and its decoded In and nothing else, so the state rides on the receiver.
type ops struct{ s *cloud.Service[state] }

func routes(app cloud.Router, s *cloud.Service[state]) {
	shutdownStores = s.State.stores.CloseAll
	mountedStores = s.State.stores // the in-process evidence seam (compose.go)
	// cloud.Bridge is not installed here: the composer owns it — the fused host
	// installs it once at its root, and the plugin constructor does the same for a
	// plugin program — and typed ops read the validated org and the project scope
	// off the request it parks on the context.
	g := app.Group("/v1/research")
	o := ops{s: s}
	zip.Post(g, "/experiments", o.postExperiments) // ingest (idempotent) → SQLite → roll up
	zip.Get(g, "/experiments", o.listExperiments)  // list canonical
	zip.Get(g, "/projects", o.getProjects)         // every project + real totals
	zip.Get(g, "/totals", o.getTotals)             // headline aggregate + per-kind
	zip.Post(g, "/grants", o.postGrant)            // visibility/consent (separate auth)
	zip.Post(g, "/artifacts", o.postArtifact)      // record a diary artifact (content-addressed)
	zip.Get(g, "/artifacts", o.listArtifacts)      // chronological diary feed (newest-first)
	// UNTYPED, and it has to be: this route streams the artifact's raw BYTES with the
	// artifact's own Content-Type (image/png for a snapshot, application/octet-stream
	// otherwise). A typed op answers application/json from a Go value — it has no
	// vocabulary for a binary body — so typing it would change what every caller
	// receives. See LLM.md.
	g.Get("/artifacts/:sha256", cloud.Handle(s, getArtifactBlob)) // retrieve the bytes by content hash
}

// The blob route's prose, declared beside the wire fact that keeps it raw.
//
// Every other op on this surface is typed, so zipdoc lifts its prose from the
// handler's doc comment and hands it to the document, the MCP tool list and the
// generated SDK. This one cannot be typed — it answers binary — and zipdoc can lift
// nothing from a raw handler, so without this declaration the one retrieval half of
// hash-addressing publishes an operationId and nothing else. Keyed by the fiber
// pattern verbatim, so it renders only while the router serves the route.
func init() {
	openapi.Describe("/v1/research/artifacts/:sha256", http.MethodGet,
		"Fetch one recorded artifact's bytes by its content hash.",
		"Streams the artifact's stored bytes — the retrieval half of hash-addressing, where the "+
			"diary feed hands out hashes and this hands back what they name. The Content-Type is "+
			"image/png when the artifact was recorded as a snapshot and "+
			"application/octet-stream otherwise; it comes from the recorded KIND, not from "+
			"sniffing the bytes, so an artifact filed as a report always arrives as opaque "+
			"bytes.\n\n"+

			"The hash is an address, and the read is NOT global. The store file IS the org, so "+
			"the same bytes recorded by two tenants are two artifacts, and a hash that exists but "+
			"belongs to somebody else is a 404 exactly like one that was never recorded — "+
			"knowing a content hash is never enough to read it. A caller with no validated org "+
			"is refused 403 outright.\n\n"+

			"Project narrows further INSIDE that org: the artifact's project must equal the "+
			"caller's, which is `?project=` when given and otherwise the caller's own project "+
			"scope, defaulting to the default project. So an artifact filed under a named "+
			"project is not found until the caller names that project — a mismatch is the same "+
			"404 an unknown hash gets, never a distinguishable refusal.\n\n"+

			"The address can be trusted because the WRITE derived it: the server hashes the "+
			"bytes it stores, inside the trust boundary, and refuses a client-supplied sha256 "+
			"that disagrees with them, so poisoning a first write would take a preimage. This "+
			"read does not re-hash — it looks the hash up as a key.\n\n"+

			"One shape to expect: this route writes its errors IN-BAND as {\"error\": …} at the "+
			"real status code, not the {status, error} envelope the typed ops beside it return. "+
			"It is mounted under an error-flattening filter that would otherwise rewrite its 4xx, "+
			"so the body is written before that filter runs. A store that cannot be opened is a "+
			"500.")
}

var shutdownStores = func() error { return nil }

// orgStore resolves the caller's org-scoped store (the physical tenant boundary). Every
// op leads with it, so tenant isolation is derived in ONE place from the validated
// principal, never a client field: the org is parked on the context by cloud.Bridge
// and an In field could only ever be a tenant key the caller asserted for itself.
func (o ops) orgStore(ctx context.Context) (*store, string, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, "", err
	}
	st, err := storeFor(o.s.State.stores, org)
	if err != nil {
		o.s.Log.Error("research store open failed", "org", org, "err", err)
		return nil, "", zip.Errorf(http.StatusInternalServerError, "research store unavailable")
	}
	return st, org, nil
}

// project is the caller's project sub-scope — a column inside the org, never a
// tenant key. Absent off the HTTP path, where the default project is the honest
// answer (the whole-org view).
func project(ctx context.Context) string {
	if c, ok := cloud.Request(ctx); ok {
		return principal.Project(c)
	}
	return principal.DefaultProject
}

// shipDurable is the ship-before-ack step every research WRITE runs after its SQLite
// commit: it snapshots the org's DB and ships it to the durable object fenced at the
// lease round. A ship that is not acknowledged — this replica was deposed mid-request
// or is running read-only — is NOT reported as success: the handler returns 503 so
// the client retries and the shard router routes it to the org's current owner, so no
// acknowledged write is ever lost. On a local-only deployment (no Durability) it acks
// trivially, so the call is a harmless no-op there. Returns nil to proceed, or a
// written error response to return.
func (o ops) shipDurable(org string) error {
	acked, err := shipFor(o.s.State.stores, org)
	if err != nil {
		o.s.Log.Warn("research durable ship failed", "org", org, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "research store failover in progress; retry")
	}
	if !acked {
		return zip.Errorf(http.StatusServiceUnavailable, "research store failover in progress; retry")
	}
	return nil
}

// ingestOut is what one ingest batch did, in BOTH views: what this call added and
// what the org's store now holds.
type ingestOut struct {
	// Project is the project the batch was filed under — the SERVER's value, never the body's.
	Project string `json:"project"`
	// ExperimentsIngested is how many experiment versions this call appended.
	ExperimentsIngested int `json:"experiments_ingested"`
	// AttemptsIngested is how many attempt versions this call appended.
	AttemptsIngested int `json:"attempts_ingested"`
	// CanonicalExperiments is the deduped experiment count the store now holds.
	CanonicalExperiments int `json:"canonical_experiments"`
	// ExperimentsRetained is the full versioned experiment history the store now holds.
	ExperimentsRetained int `json:"experiments_retained"`
	// CanonicalAttempts is the deduped attempt count the store now holds.
	CanonicalAttempts int `json:"canonical_attempts"`
	// AttemptsRetained is the full versioned attempt history the store now holds.
	AttemptsRetained int `json:"attempts_retained"`
	// RolledUp is false when the OLAP roll-up was skipped; the SQLite write still stands.
	RolledUp bool `json:"rolled_up"`
}

// IngestExperiments appends one batch of experiment and attempt versions to the
// caller org's evidence store, idempotently by content, then rolls it up to the
// analytics plane best-effort. The project is the SERVER's value and visibility is
// forced private — an upload grants no training or publication right, which is a
// separate call. A run carrying a BYO endpoint is SSRF-checked before the store is
// touched. The answer carries BOTH the canonical (deduped) and retained (full
// history) counts, so a caller sees the versioned truth rather than a dedup that
// reads as loss.
//
// Example: {"experiments": [{"id": "benchmark:zen-1:mmlu", "kind": "benchmark", "subject": "zen-1", "metric": "accuracy", "value": 0.81, "ts": 1750000000}], "attempts": []}
func (o ops) postExperiments(ctx context.Context, in *IngestRequest) (*ingestOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	proj := project(ctx)

	if len(in.Experiments) == 0 && len(in.Attempts) == 0 {
		return nil, zip.ErrBadRequest("ingest needs at least one experiment or attempt")
	}
	if n := len(in.Experiments) + len(in.Attempts); n > maxBatchItems {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "batch too large; split the upload")
	}
	for i := range in.Experiments {
		e := &in.Experiments[i]
		if e.ID == "" || e.Kind == "" || e.Subject == "" {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "experiment needs id, kind, and subject")
		}
		if e.Endpoint != "" {
			if err := ssrfSafe(e.Endpoint); err != nil {
				return nil, zip.Errorf(http.StatusUnprocessableEntity, "%s", err.Error())
			}
		}
	}
	for i := range in.Attempts {
		a := &in.Attempts[i]
		if a.Benchmark == "" || a.Item == "" || a.Model == "" {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "attempt needs benchmark, item, and model")
		}
	}

	expAdded, attAdded, err := st.ingest(ctx, proj, in.Experiments, in.Attempts)
	if err != nil {
		o.s.Log.Error("research ingest failed", "org", org, "project", proj, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research ingest failed")
	}
	// Ship the committed ingest durably BEFORE acknowledging (and before the external
	// roll-up), so a failover never loses an acked write.
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}

	// Roll up best-effort — the SQLite write above is the source of truth, so a datastore
	// outage degrades to rolled_up:false, never a failed ingest.
	rolledUp := true
	if err := o.s.State.wh.rollUp(ctx, org, proj, in.Experiments, in.Attempts, time.Now()); err != nil {
		rolledUp = false
		o.s.Log.Warn("research roll-up skipped; SQLite retains the write", "org", org, "err", err)
	}

	cnt, err := st.counts(ctx)
	if err != nil {
		o.s.Log.Error("research counts failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research counts failed")
	}
	return &ingestOut{
		Project:              proj,
		ExperimentsIngested:  expAdded,
		AttemptsIngested:     attAdded,
		CanonicalExperiments: cnt.ExperimentsCanonical,
		ExperimentsRetained:  cnt.ExperimentsRetained,
		CanonicalAttempts:    cnt.AttemptsCanonical,
		AttemptsRetained:     cnt.AttemptsRetained,
		RolledUp:             rolledUp,
	}, nil
}

// listIn narrows a canonical experiment listing. Both are query parameters.
type listIn struct {
	// Project narrows to one project. Empty reads the org's whole set across projects.
	Project string `json:"project"`
	// Kind narrows to one discriminator: benchmark, kernel-perf, training, ablation or policy-eval.
	Kind string `json:"kind"`
}

// experimentsOut is a canonical experiment listing.
type experimentsOut struct {
	// Data are the canonical experiment versions.
	Data []Experiment `json:"data"`
	// Total is len(data) — the rows in this answer, not the store's history.
	Total int `json:"total"`
}

// ListExperiments returns the caller org's CANONICAL experiments — the deterministic
// deduped view over the versioned history. With no ?project= it reads the org's
// whole set across projects (the ops board's cross-project view, since a project is
// a sub-scope of the one tenant); ?project= narrows to one and ?kind= to one
// discriminator.
func (o ops) listExperiments(ctx context.Context, in *listIn) (*experimentsOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	exps, err := st.listExperiments(ctx, in.Project, in.Kind)
	if err != nil {
		o.s.Log.Error("research list failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research list failed")
	}
	return &experimentsOut{Data: exps, Total: len(exps)}, nil
}

// projectsOut is every project in the org with its real totals.
type projectsOut struct {
	// Data are the org's projects with canonical + retained counts.
	Data []ProjectSummary `json:"data"`
	// Total is len(data).
	Total int `json:"total"`
}

// ListResearchProjects returns every research project in the caller's org with its
// real totals — canonical and retained side by side — which is the ops board's
// "every project + real totals" view.
func (o ops) getProjects(ctx context.Context, _ *noInput) (*projectsOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	ps, err := st.projectSummaries(ctx)
	if err != nil {
		o.s.Log.Error("research projects failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research projects failed")
	}
	return &projectsOut{Data: ps, Total: len(ps)}, nil
}

// totalsIn narrows the headline aggregate.
type totalsIn struct {
	// Project narrows the aggregate to one project. Empty aggregates the whole org.
	Project string `json:"project"`
}

// GetResearchTotals returns the caller org's headline aggregate plus a per-kind
// breakdown — the observatory's poll target. Canonical and retained counts travel
// together, so a deduped view never reads as loss. ?project= narrows to one project.
func (o ops) getTotals(ctx context.Context, in *totalsIn) (*ResearchTotals, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	t, err := st.totals(ctx, in.Project)
	if err != nil {
		o.s.Log.Error("research totals failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research totals failed")
	}
	return &t, nil
}

// grantOut reports how many records a grant moved.
type grantOut struct {
	// Updated is the number of records the grant applied to. Zero is a 404, never a silent no-op.
	Updated int `json:"updated"`
}

// GrantResearchVisibility records the SEPARATE authorization an upload never
// implies: a record's visibility (private, org or public) and, for a run, its
// training and commons-publication consent. Address a run by its stable id or an
// artifact by its sha256; an artifact grant sets visibility only. The ORG is the
// tenant boundary and comes from the validated principal, so a caller can only ever
// grant within its own org; `project` locates WHICH record inside it and defaults to
// the caller's project scope.
//
// Example: {"id": "benchmark:zen-1:mmlu", "visibility": "public", "trainable": true}
func (o ops) postGrant(ctx context.Context, in *GrantRequest) (*grantOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if in.ID == "" && in.SHA256 == "" {
		return nil, zip.ErrBadRequest("grant needs a stable id or an artifact sha256")
	}
	if in.Visibility != nil && !visibilities[*in.Visibility] {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "visibility must be private, org, or public")
	}
	// project locates WHICH run/artifact in the caller's org to grant — a run's stable
	// id is (project, id), so the target project is an INPUT, defaulting to the
	// caller's project scope. This is intentionally not server-forced (as ingest's is):
	// the ORG (the physical file, orgStore→principal.OrgFrom) is the tenant boundary, and
	// project is an org-internal label the org's own board reads across, so a caller
	// granting for any project in ITS OWN org is within its authority — never cross-org.
	proj := in.Project
	if proj == "" {
		proj = project(ctx)
	}
	// An artifact grant (by sha256) sets only visibility — the same private-by-default
	// rule as runs. A run grant (by id) sets visibility + consent flags.
	var n int
	if in.SHA256 != "" {
		if in.Visibility == nil {
			return nil, zip.ErrBadRequest("artifact grant needs a visibility")
		}
		n, err = st.setArtifactVisibility(ctx, proj, in.SHA256, *in.Visibility)
	} else {
		n, err = st.setGrant(ctx, proj, in.ID, in.Visibility, in.Trainable, in.Publishable)
	}
	if err != nil {
		o.s.Log.Error("research grant failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research grant failed")
	}
	if n == 0 {
		return nil, zip.ErrNotFound("no records for that target")
	}
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	return &grantOut{Updated: n}, nil
}

// artifactOut is the content address the server derived for the submitted bytes.
type artifactOut struct {
	// SHA256 is the SERVER's hash of the bytes — the artifact's identity.
	SHA256 string `json:"sha256"`
	// Ref is the content address, "sha256:<hash>".
	Ref string `json:"ref"`
	// Created is false when these exact bytes were already recorded — the write is a no-op.
	Created bool `json:"created"`
	// RolledUp is false when the OLAP roll-up was skipped; the SQLite write still stands.
	RolledUp bool `json:"rolled_up"`
}

// RecordResearchArtifact records one research-diary artifact — a board snapshot or a
// generated report — CONTENT-ADDRESSED inside the trust boundary. The caller submits
// the bytes as base64 `content`; the SERVER hashes them and THAT hash is the identity
// and the ref, so the address can never be poisoned by a client-asserted one. A
// client-supplied sha256, if present, must match the bytes. The project is the
// SERVER's value and visibility is forced private. Re-posting the same bytes is a
// no-op that reports created=false.
//
// Example: {"kind": "snapshot", "content": "iVBORw0KGgo=", "run_id": "benchmark:zen-1:mmlu"}
func (o ops) postArtifact(ctx context.Context, in *ResearchArtifact) (*artifactOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	proj := project(ctx)
	a := *in
	if !artifactKinds[a.Kind] {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "kind must be snapshot or report")
	}
	if a.Content == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "artifact needs content bytes (base64) to content-address")
	}
	content, err := base64.StdEncoding.DecodeString(a.Content)
	if err != nil || len(content) == 0 {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "content must be non-empty base64")
	}
	if len(content) > maxArtifactBytes {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "artifact content too large")
	}
	// Hash inside the trust boundary — the SERVER derives the identity from the bytes it
	// stores, so a poisoned first-write is impossible (a distinct hash needs a preimage).
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	if a.SHA256 != "" && !strings.EqualFold(a.SHA256, hash) {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "sha256 does not match the content bytes")
	}
	a.SHA256 = hash
	a.Ref = "sha256:" + hash // server-derived content address — never a client file:// ref
	a.Content = ""
	created, err := st.putArtifact(ctx, proj, a, content)
	if err != nil {
		o.s.Log.Error("research artifact failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research artifact failed")
	}
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	rolledUp := true
	if err := o.s.State.wh.rollUpArtifact(ctx, org, proj, a, time.Now()); err != nil {
		rolledUp = false
	}
	return &artifactOut{SHA256: a.SHA256, Ref: a.Ref, Created: created == 1, RolledUp: rolledUp}, nil
}

// artifactsIn filters the diary feed. All three are query parameters.
type artifactsIn struct {
	// Project narrows to one project. Empty takes the caller's project scope.
	Project string `json:"project"`
	// Run narrows to one run's artifacts by its stable id.
	Run string `json:"run"`
	// Since bounds the feed to artifacts recorded at or after this unix second.
	Since int64 `json:"since"`
}

// artifactsOut is the diary feed.
type artifactsOut struct {
	// Data are the artifacts, newest first. Content bytes are never returned here.
	Data []ResearchArtifact `json:"data"`
	// Total is len(data).
	Total int `json:"total"`
}

// ListResearchArtifacts returns the caller org's research-diary feed newest-first —
// the snapshots and reports tied to its runs, as metadata and content addresses;
// the bytes themselves are fetched by hash. ?run= narrows to one run, ?project=
// to one project (default the caller's project scope), and ?since= to a unix second.
func (o ops) listArtifacts(ctx context.Context, in *artifactsIn) (*artifactsOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	proj := in.Project
	if proj == "" {
		proj = project(ctx)
	}
	arts, err := st.listArtifacts(ctx, proj, in.Run, in.Since, maxArtifactPage)
	if err != nil {
		o.s.Log.Error("research artifacts list failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research artifacts list failed")
	}
	return &artifactsOut{Data: arts, Total: len(arts)}, nil
}

// noInput is the In of an op addressed entirely by the caller's principal: it takes
// nothing off the wire. ONE of these for the whole package.
type noInput struct{}

// getArtifactBlob serves one artifact's stored bytes, hash-addressed by :sha256 and
// org-scoped — the retrieval side of hash-addressing (the board fetches a snapshot by
// its content hash).
//
// It is the ONE route on this surface that is not a typed op, and it cannot be one:
// it answers the artifact's RAW BYTES under the artifact's own Content-Type
// (image/png for a snapshot, application/octet-stream otherwise). A typed op
// serialises a Go value as application/json and has no vocabulary for a binary body,
// so typing this would change what every caller receives — a wire break, not a
// description. Its errors therefore stay in-band, as the rest of this file's did.
func getArtifactBlob(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return errJSON(c, http.StatusForbidden, principal.Refusal(c))
	}
	st, err := storeFor(s.State.stores, org)
	if err != nil {
		s.Log.Error("research store open failed", "org", org, "err", err)
		return errJSON(c, http.StatusInternalServerError, "research store unavailable")
	}
	proj := c.Query("project")
	if proj == "" {
		proj = principal.Project(c)
	}
	content, kind, ok := st.artifactContent(c.Context(), proj, c.Param("sha256"))
	if !ok {
		return errJSON(c, http.StatusNotFound, "artifact not found")
	}
	ct := "application/octet-stream"
	if kind == "snapshot" {
		ct = "image/png"
	}
	c.SetHeader("Content-Type", ct)
	c.Status(http.StatusOK)
	return c.SendStream(bytes.NewReader(content))
}

// errJSON writes an error status in-band (status + {error} JSON, nil returned) — the
// arena family's pattern for a subsystem mounted under commerce's error-flatten filter
// (see service.Terminal): writing before the filter runs keeps the real 4xx. Only the
// blob route above needs it now; every other op on this surface is typed and RETURNS
// its error, which zip renders as {status, error} with the same status code.
func errJSON(c *zip.Ctx, status int, msg string) error {
	return c.JSON(status, map[string]string{"error": msg})
}
