package research

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
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
	// Seq is the server-assigned monotone append clock stamped when this version
	// was recorded. Ignored on ingest; returned on reads so supersession order is
	// externally auditable.
	Seq int64 `json:"seq,omitempty"`
	// ContentHash is the server-computed SHA-256 identity over the version's
	// measurement, provenance, and canonicalized tags. Ignored on ingest; returned
	// on reads.
	ContentHash string `json:"content_hash,omitempty"`
	// ReceivedAt is the server wall-clock timestamp (unix seconds) stamped when
	// this version was first recorded. Ignored on ingest and excluded from
	// ContentHash so re-ingesting identical content stays idempotent.
	ReceivedAt int64 `json:"received_at"`
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
	// Tags are optional producer labels attached to the experiment, canonicalized
	// as sorted unique strings on write and queryable via ?tag= on GET /v1/research/experiments.
	// Included in ContentHash only when non-empty so pre-existing untagged hashes never move.
	Tags []string `json:"tags,omitempty"`
	// TS is when the run was measured, unix SECONDS. When 0 on ingest, the server
	// defaults it to ReceivedAt so newly recorded runs always carry a timestamp,
	// while Seq and ReceivedAt provide server-authoritative ordering.
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
	// Defaults to ReceivedAt when 0 on ingest.
	TS int64 `json:"ts"`
	// ReceivedAt is the server wall-clock timestamp (unix seconds) stamped when
	// this attempt version was first recorded. Ignored on ingest.
	ReceivedAt int64 `json:"received_at"`
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

// artifactKinds is the allowed set for a diary or evidence artifact. Empty defaults
// to "report" on write so generic artifact uploads succeed without ceremony.
var artifactKinds = map[string]bool{
	"snapshot": true,
	"report":   true,
	"artifact": true,
	"evidence": true,
	"harness":  true,
	"manifest": true,
	"result":   true,
}

// safeArtifactMediaTypes is the allowlist of non-executable MIME types that
// GET /v1/research/artifacts/:sha256 may echo back on Content-Type. HTML/SVG/JS
// are never served inline from the blob endpoint.
var safeArtifactMediaTypes = map[string]bool{
	"application/json":             true,
	"application/jsonl":            true,
	"application/x-ndjson":         true,
	"application/pdf":              true,
	"application/zstd":             true,
	"application/gzip":             true,
	"application/octet-stream":     true,
	"text/plain":                   true,
	"text/markdown":                true,
	"text/csv":                     true,
	"text/tab-separated-values":    true,
	"image/png":                    true,
	"image/jpeg":                   true,
	"image/webp":                   true,
}

// ResearchArtifact is one research-diary record — a raw-artifact-class record tied to a run: a
// snapshot (a PNG of the canonical board), report, manifest, harness, evidence, or result blob.
// The caller submits the bytes (base64 content); the SERVER hashes them and that sha256 is the
// identity + ref (sha256:<hash>) — never a client-asserted hash or ref, so it is genuinely
// content-addressed and un-poisonable. A re-POST of the same bytes is a no-op. Private by
// default; public only via the separate visibility grant. Carries the same provenance as a run.
type ResearchArtifact struct {
	// SHA256 is the SERVER's hash of the submitted bytes, hex, and the artifact's
	// identity — the key the blob route is addressed by. A caller MAY send one;
	// it is not trusted, only checked, and a mismatch with the bytes refuses the
	// write.
	SHA256 string `json:"sha256"`
	// ContentSHA256 mirrors the server-verified SHA-256 hex digest of the raw bytes.
	// If supplied on write it must match the server-computed digest (422 otherwise);
	// always populated on stored and returned records.
	ContentSHA256 string `json:"content_sha256,omitempty"`
	// ArtifactID is an optional producer-assigned logical identifier for the artifact
	// (e.g. "manifest-v1" or "rotten-tomatoes-predictions").
	ArtifactID string `json:"artifact_id,omitempty"`
	// MediaType is the MIME type of the stored bytes (e.g. "application/json",
	// "text/markdown", "image/png"). Validated on write and served on blob reads
	// when in the safe non-script allowlist.
	MediaType string `json:"media_type,omitempty"`
	// Canonicalization names the byte-canonicalization scheme applied by the producer
	// before hashing (e.g. "rfc8785-jcs" or "utf8-lf"), when applicable.
	Canonicalization string `json:"canonicalization,omitempty"`
	// UncompressedSize is the uncompressed byte length of the artifact; defaults to
	// len(rawBytes) on write when omitted or zero.
	UncompressedSize int64 `json:"uncompressed_size,omitempty"`
	// Content is the artifact's bytes, base64, on WRITE only, at most 16 MiB.
	// Never returned: a read answers metadata and the address, and the bytes come
	// from GET /v1/research/artifacts/{sha256}.
	Content string `json:"content,omitempty"`
	// Kind classifies the artifact: snapshot, report (default when omitted), artifact,
	// evidence, harness, manifest, or result.
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
	// ReceivedAt is the server wall-clock timestamp (unix seconds) stamped when
	// the artifact was first recorded. Ignored on write.
	ReceivedAt int64 `json:"received_at"`
	// TS is when the artifact was recorded, unix SECONDS — what the feed sorts
	// newest-first on and what ?since= is compared against. Defaults to ReceivedAt
	// when 0 on write.
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
	// Kind names what ID addresses: run, decision, evaluation or evidence. Empty
	// when the id names records of one kind alone; an id that names records of
	// more than one kind is refused 409 without it, so a grant never moves a
	// record it did not name.
	Kind string `json:"kind"`
	// ID addresses an experiment (run) by its stable id — or a decision by its
	// DDT id, an evaluation by its Evaluation Run id, evidence by its uri, which
	// take visibility only and are not narrowed by project. The grant lands on
	// EVERY retained version of that id, because the decision is about the record
	// and not about one version of it.
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

func Use(app cloud.Router, deps cloud.Deps) error {
	return cloud.Use(app, deps, "research", build, routes)
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
	mountedStores = s.State.stores // the in-process evidence client (compose.go)
	// cloud.Bridge is not installed here: the composer owns it — the fused host
	// installs it once at its root, and the plugin constructor does the same for a
	// plugin program — and typed ops read the validated org and the project scope
	// off the request it parks on the context.
	o := ops{s: s}
	exposeEvidence(o)
	g := app.Group("/v1/research")
	g.Post("/experiments", o.postExperiments)                   // ingest (idempotent) → SQLite → roll up
	g.Get("/experiments", o.listExperiments)                    // list canonical
	g.Get("/experiments/versions", o.listExperimentVersions)    // retained version history (?id=...)
	g.Get("/experiments/:id/versions", o.getExperimentVersions) // retained version history (URL-escaped :id)
	g.Get("/projects", o.getProjects)         // every project + real totals
	g.Get("/totals", o.getTotals)             // headline aggregate + per-kind
	g.Post("/grants", o.postGrant)            // visibility/consent (separate auth)
	g.Post("/artifacts", o.postArtifact)      // record a diary artifact (content-addressed)
	g.Get("/artifacts", o.listArtifacts)      // chronological diary feed (newest-first)
	// The experiment record (record.go): the task, the execution, the question it
	// answers, and the writing that cites it. One POST and one GET per kind — a
	// listing narrowed by ?id= IS the read of one record, so there is no second
	// address for fetching one thing — and one read that puts two runs together.
	g.Post("/benchmarks", o.postBenchmarks) // record benchmark definitions
	g.Get("/benchmarks", o.listBenchmarks)  // list definitions (?id= ?project=)
	g.Post("/runs", o.postRuns)             // record executions (completion derived)
	g.Get("/runs", o.listRuns)              // list runs (?id= ?benchmark= ?system= ?study= ?completion= ?since= ?until= ?org=)
	g.Post("/studies", o.postStudies)       // record the questions runs answer
	g.Get("/studies", o.listStudies)        // list studies (?id= ?project=)
	g.Post("/papers", o.postPapers)         // record writing that cites runs
	g.Get("/papers", o.listPapers)          // list papers (?id= ?study= ?cites=)
	g.Get("/compare", o.compareRuns)        // two runs side by side, with what stops the comparison
	// The decision thread (decision.go): evidence, and the DDT decisions,
	// evaluations, actions and commitments that rest on it.
	threadRoutes(app, o)
	// Hanzo Dō (do.go): HIP-1400 conformance manifest, HIP-1401 evolution
	// candidates + Pareto frontier, HIP-1402 deterministic XP projection,
	// HIP-1403 pre-registered hypotheses + replicated claims, HIP-1404 regressions.
	doRoutes(app, o)
	// UNTYPED, and it has to be: this route streams the artifact's raw BYTES with the
	// artifact's own Content-Type (image/png for a snapshot, application/octet-stream
	// otherwise). A typed op answers application/json from a Go value — it has no
	// vocabulary for a binary body — so typing it would change what every caller
	// receives. See LLM.md.
	g.Raw(http.MethodGet, "/artifacts/:sha256", cloud.Handle(s, getArtifactBlob)) // retrieve the bytes by content hash
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
	spec.Describe("/v1/research/artifacts/:sha256", http.MethodGet,
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
			if err := ssrfSafe(ctx, e.Endpoint); err != nil {
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

	if err := holdable(in); err != nil {
		return nil, err
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

// listIn narrows a canonical experiment listing. All three are query parameters.
type listIn struct {
	// Project narrows to one project. Empty reads the org's whole set across projects.
	Project string `json:"project"`
	// Kind narrows to one discriminator: benchmark, kernel-perf, training, ablation or policy-eval.
	Kind string `json:"kind"`
	// Tag narrows to experiments carrying this canonicalized tag string.
	Tag string `json:"tag"`
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
// a sub-scope of the one tenant); ?project= narrows to one, ?kind= to one
// discriminator, and ?tag= to one label.
func (o ops) listExperiments(ctx context.Context, in *listIn) (*experimentsOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	exps, err := st.listExperiments(ctx, in.Project, in.Kind, in.Tag)
	if err != nil {
		o.s.Log.Error("research list failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research list failed")
	}
	return &experimentsOut{Data: exps, Total: len(exps)}, nil
}

// ExperimentVersion is one retained version in an experiment's append-ordered audit trail.
type ExperimentVersion struct {
	// Seq is the per-org monotone append clock stamped when this version was recorded.
	Seq int64 `json:"seq"`
	// ReceivedAt is the server wall-clock timestamp (unix seconds) when this version arrived.
	ReceivedAt int64 `json:"received_at"`
	// TS is the producer's measured-at timestamp (unix seconds), defaulted to ReceivedAt when omitted.
	TS int64 `json:"ts"`
	// ContentHash is the server-computed SHA-256 identity over the measurement, provenance, and tags.
	ContentHash string `json:"content_hash"`
	// Revision is the producer's declared version kind: original, corrected, or retracted.
	Revision string `json:"revision"`
	// Status is the run's execution state on this version (planning, running, complete, faulted, ...).
	Status string `json:"status"`
	// Canonical is true on the single latest non-retracted version of the stable id and false otherwise.
	Canonical bool `json:"canonical"`
	// Value is the measured quantity recorded on this version.
	Value float64 `json:"value"`
	// N is the sample size recorded on this version.
	N int `json:"n"`
	// Meta is the canonicalized metadata JSON object recorded on this version.
	Meta json.RawMessage `json:"meta,omitempty"`
	// GitSHA is the commit SHA recorded on this version.
	GitSHA string `json:"git_sha"`
	// Tags are the canonicalized sorted unique labels recorded on this version.
	Tags []string `json:"tags,omitempty"`
}

// versionsQueryIn queries an experiment's version history by `?id=` (safe for unencoded slashes/colons).
type versionsQueryIn struct {
	// ID is the experiment's stable id (e.g. "policy-eval:subject:task/run"). Required.
	ID string `json:"id"`
	// Project narrows lookup to one project; empty resolves the unique holding project or caller scope.
	Project string `json:"project"`
}

// versionsPathIn queries an experiment's version history by URL-escaped `:id` path parameter.
type versionsPathIn struct {
	// ID is the URL-escaped experiment stable id bound from the path segment `:id`.
	ID string `json:"id"`
	// Project narrows lookup to one project; empty resolves the unique holding project or caller scope.
	Project string `json:"project"`
}

// experimentVersionsOut is the full append-ordered version history of one experiment stable id.
type experimentVersionsOut struct {
	// Project is the project scope the returned versions belong to.
	Project string `json:"project"`
	// ID is the experiment stable id whose retained versions are listed.
	ID string `json:"id"`
	// Versions are every retained version of the experiment ordered ascending by seq,
	// proving whether a planning/running pre-registration preceded its complete result.
	Versions []ExperimentVersion `json:"versions"`
}

// ListExperimentVersions returns every retained version of an experiment stable id
// (`?id=<stable_id>`), ordered ascending by server `seq` so anyone can audit that a
// `planning` or `running` pre-registration preceded its `complete` measurement.
func (o ops) listExperimentVersions(ctx context.Context, in *versionsQueryIn) (*experimentVersionsOut, error) {
	return o.readExperimentVersions(ctx, in.Project, strings.TrimSpace(in.ID))
}

// GetExperimentVersions returns every retained version of an experiment stable id
// addressed via URL-escaped `:id` in `/v1/research/experiments/:id/versions`,
// ordered ascending by server `seq`.
func (o ops) getExperimentVersions(ctx context.Context, in *versionsPathIn) (*experimentVersionsOut, error) {
	rawID := strings.TrimSpace(in.ID)
	if unescaped, err := url.PathUnescape(rawID); err == nil {
		rawID = strings.TrimSpace(unescaped)
	}
	return o.readExperimentVersions(ctx, in.Project, rawID)
}

func (o ops) readExperimentVersions(ctx context.Context, explicitProj, id string) (*experimentVersionsOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, zip.ErrBadRequest("experiment id required")
	}
	resolvedProj, versions, err := st.experimentVersions(ctx, explicitProj, project(ctx), id)
	if err != nil {
		o.s.Log.Error("research versions failed", "org", org, "id", id, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research versions failed")
	}
	return &experimentVersionsOut{
		Project:  resolvedProj,
		ID:       id,
		Versions: versions,
	}, nil
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
func (o ops) getProjects(ctx context.Context, _ *cloud.Unit) (*projectsOut, error) {
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
// the caller's project scope. Granting a decision, an evaluation or evidence (the
// decision thread's kinds) is a person's act: any caller that is not a person
// signed in as themselves, in their own org, is refused 403.
//
// Example: {"id": "benchmark:zen-1:mmlu", "visibility": "public", "trainable": true}
func (o ops) postGrant(ctx context.Context, in *GrantRequest) (*grantOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := holdable(in); err != nil {
		return nil, err
	}
	if in.ID == "" && in.SHA256 == "" {
		return nil, zip.ErrBadRequest("grant needs a stable id or an artifact sha256")
	}
	if in.Visibility != nil && !visibilities[*in.Visibility] {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "visibility must be private, org, or public")
	}
	if in.Kind != "" && in.Kind != "run" && !slices.Contains(threadKinds, in.Kind) {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "kind must be run, decision, evaluation or evidence")
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
		_, human := signed(ctx)
		n, err = st.setGrant(ctx, proj, in.Kind, in.ID, in.Visibility, in.Trainable, in.Publishable, human == nil)
		if errors.Is(err, errNotPerson) {
			return nil, human
		}
	}
	if a, ok := errors.AsType[ambiguous](err); ok {
		return nil, zip.ErrConflict(a.Error())
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

// artifactOut is the content address and verified metadata the server derived for the submitted bytes.
type artifactOut struct {
	// SHA256 is the SERVER's hash of the bytes — the artifact's identity.
	SHA256 string `json:"sha256"`
	// ContentSHA256 echoes the server-verified SHA-256 hex digest of the raw bytes.
	ContentSHA256 string `json:"content_sha256"`
	// Ref is the content address, "sha256:<hash>".
	Ref string `json:"ref"`
	// Kind is the normalized artifact kind recorded on the store ("report" when omitted).
	Kind string `json:"kind"`
	// ArtifactID echoes the optional logical artifact identifier when supplied.
	ArtifactID string `json:"artifact_id,omitempty"`
	// MediaType echoes the normalized MIME type when supplied.
	MediaType string `json:"media_type,omitempty"`
	// UncompressedSize is the recorded uncompressed byte length.
	UncompressedSize int64 `json:"uncompressed_size"`
	// ReceivedAt is the server wall-clock timestamp (unix seconds) when the artifact was first stored.
	ReceivedAt int64 `json:"received_at"`
	// Created is false when these exact bytes were already recorded — the write is a no-op.
	Created bool `json:"created"`
	// RolledUp is false when the OLAP roll-up was skipped; the SQLite write still stands.
	RolledUp bool `json:"rolled_up"`
}

func normalizeArtifactMediaType(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if strings.ContainsAny(raw, "\r\n\x00") {
		return "", errors.New("invalid media_type")
	}
	mt, _, err := mime.ParseMediaType(raw)
	if err != nil || mt == "" {
		return "", errors.New("invalid media_type")
	}
	return strings.ToLower(mt), nil
}

func servedArtifactContentType(kind, mediaType string) string {
	if mt := strings.ToLower(strings.TrimSpace(mediaType)); mt != "" {
		if base, _, err := mime.ParseMediaType(mt); err == nil && safeArtifactMediaTypes[strings.ToLower(base)] {
			return strings.ToLower(base)
		}
	}
	if kind == "snapshot" {
		return "image/png"
	}
	return "application/octet-stream"
}

// RecordResearchArtifact records one research-diary artifact — a board snapshot,
// report, manifest, harness, evidence, or result — CONTENT-ADDRESSED inside the trust
// boundary. The caller submits the bytes as base64 `content`; the SERVER hashes them
// and THAT hash is the identity and the ref, so the address can never be poisoned by
// a client-asserted one. Client-supplied `sha256` or `content_sha256`, if present,
// must match the bytes (422 otherwise). Empty `kind` defaults to `report`. Re-posting
// the same bytes is a no-op that reports created=false.
//
// Example: {"kind": "snapshot", "content": "iVBORw0KGgo=", "run_id": "benchmark:zen-1:mmlu"}
func (o ops) postArtifact(ctx context.Context, in *ResearchArtifact) (*artifactOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	proj := project(ctx)
	a := *in
	a.Kind = strings.TrimSpace(strings.ToLower(a.Kind))
	if a.Kind == "" {
		a.Kind = "report"
	}
	if !artifactKinds[a.Kind] {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "kind must be snapshot, report, artifact, evidence, harness, manifest, or result")
	}
	mt, err := normalizeArtifactMediaType(a.MediaType)
	if err != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "invalid media_type")
	}
	a.MediaType = mt
	if a.UncompressedSize < 0 {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "uncompressed_size must be non-negative")
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
	if a.ContentSHA256 != "" && !strings.EqualFold(a.ContentSHA256, hash) {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "content_sha256 does not match the content bytes")
	}
	a.SHA256 = hash
	a.ContentSHA256 = hash
	if a.UncompressedSize == 0 {
		a.UncompressedSize = int64(len(content))
	}
	a.Ref = "sha256:" + hash // server-derived content address — never a client file:// ref
	a.Content = ""
	if err := holdable(a); err != nil {
		return nil, err
	}
	created, stored, err := st.putArtifact(ctx, proj, a, content)
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
	return &artifactOut{
		SHA256:           stored.SHA256,
		ContentSHA256:    stored.ContentSHA256,
		Ref:              stored.Ref,
		Kind:             stored.Kind,
		ArtifactID:       stored.ArtifactID,
		MediaType:        stored.MediaType,
		UncompressedSize: stored.UncompressedSize,
		ReceivedAt:       stored.ReceivedAt,
		Created:          created == 1,
		RolledUp:         rolledUp,
	}, nil
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
		var he *zip.HTTPError
		errors.As(principal.Refused(c), &he)
		return errJSON(c, he.Status, he.Msg)
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
	content, meta, ok := st.artifactBlob(c.Context(), proj, c.Param("sha256"))
	if !ok {
		return errJSON(c, http.StatusNotFound, "artifact not found")
	}
	c.SetHeader("Content-Type", servedArtifactContentType(meta.Kind, meta.MediaType))
	c.SetHeader("X-Content-Type-Options", "nosniff")
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
