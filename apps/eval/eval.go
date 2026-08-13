// Package eval is scoring a model on your own data, with a judge you choose.
//
// /v1/evals: datasets, dataset items, evaluators, score configs, runs, scores
// and traces, per org. Native, and nothing proxies to the retired observability
// console.
//
// Storage split (CTO directive), two orthogonal stores this package composes:
//   - metastore  (store.go)     — Hanzo Base/SQLite, per-org config/metadata:
//     datasets, dataset items, evaluators, rubrics, dataset-run defs.
//   - telemetry  (telemetry.go) — apps/datastore, append-only event stream:
//     traces + scores-as-events (all AI observability). Optional at Mount; when
//     no datastore is wired the run still scores, but trace/score persistence is
//     honestly skipped (logged), never faked.
//
// The run orchestrator drives a REAL evaluation: for each ACTIVE dataset item it
// (1) calls the model-under-test through the runner, (2) records a trace, (3)
// calls the LLM-as-judge through the runner, (4) validates + records the score,
// (5) updates the durable run record. The runner is PLUGGABLE (EvalRunner):
// today an in-process gateway runner (the caller's bearer → loopback
// /v1/chat/completions); a DigitalOcean-backed runner can drop in behind the same
// interface without touching the store/API/FE.
//
// Tenant isolation is enforced SERVER-SIDE on every request: the org is c.Org()
// — the value SanitizeIdentity minted from the VALIDATED bearer owner (HIP-0026)
// — and NEVER a client-supplied X-Org-Id/X-Project-Id header. Every metastore
// query filters WHERE org=?; every telemetry read binds org as a named parameter.
//
// Surface (all org-scoped; /v1 only):
//
//	POST   /v1/evals/datasets         create/upsert a dataset            -> Dataset
//	GET    /v1/evals/datasets         list the org's datasets            -> {data:[…]}
//	GET    /v1/evals/datasets/:name   dataset detail + item count        -> Dataset
//	DELETE /v1/evals/datasets/:name   delete a dataset (+ its items)
//	POST   /v1/evals/datasets/:name/items  create/upsert an item         -> DatasetItem
//	GET    /v1/evals/datasets/:name/items  list a dataset's items (limit) -> {data:[…]}
//	POST   /v1/evals/evaluators       create/upsert an evaluator         -> Evaluator
//	GET    /v1/evals/evaluators       list the org's evaluators          -> {data:[…]}
//	POST   /v1/evals/rubrics          create/upsert a rubric             -> ScoreConfig
//	GET    /v1/evals/rubrics          list the org's rubrics             -> {data:[…]}
//	POST   /v1/evals/scores           record a score event               -> ScoreView
//	GET    /v1/evals/scores           list score events (filters+limit)  -> {data:[…]}
//	GET    /v1/evals/traces           list traces (filters+limit)        -> {data:[…]}
//	POST   /v1/evals/runs             run a dataset through model+judge   -> runSummary
//	GET    /v1/evals/runs             list run records (datasetName)     -> {data:[…]}
//	GET    /v1/evals/metrics          the org's AI overview board        -> board
//
// Order 145: binds /v1/evals/* BEFORE the AI subsystem's /v1/* catch-all (150),
// the same slot product uses. serve.go auto-registers GET /v1/evals/health.
package eval

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"

	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/internal/mint"
	"github.com/hanzoai/cloud/internal/shorten"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Run sizing: a synchronous run is bounded so one request cannot fan out into
// thousands of paired LLM calls.
const (
	defaultRunItems = 20
	maxRunItems     = 100

	// maxContent caps a single dataset-item field (input/expected/metadata) so an
	// unbounded blob can't amplify the shared SQLite file or a run's prompt.
	maxContent = 64 * 1024
	// maxComment caps a score comment / truncated output stored in telemetry.
	maxComment = 2000
	// defaultListLimit / maxListLimit bound metastore list responses.
	defaultListLimit = 100
	maxListLimit     = 500

	// maxConcurrentRunsPerOrg caps how many /v1/evals/runs one org may have
	// in-flight at once (Red MED). A run drives up to maxRunItems paired LLM calls
	// against the SHARED in-process gateway; without a cap, one org firing many
	// concurrent 100-item runs exhausts gateway connections/goroutines and
	// degrades prompts/agents/chat for EVERY tenant. Excess runs fail fast with
	// 429 (never queued — queuing just moves the exhaustion).
	maxConcurrentRunsPerOrg = 4
)

// maxRunDuration is the total wall-clock deadline for one run (Red MED). A run
// that exceeds it is cancelled; its unfinished items are recorded as errors and
// the partial summary is returned honestly, so a runaway run can never pin a
// request (and a gateway slot) indefinitely. A var so tests can shrink it.
var maxRunDuration = 10 * time.Minute

// runSlots is the per-org run-concurrency limiter: one buffered channel per org,
// capacity maxConcurrentRunsPerOrg. acquireRunSlot is non-blocking (fail-fast to
// 429); the map is keyed by the validated org, so its cardinality is bounded by
// real tenants, not attacker-chosen values.
var (
	runSlotsMu sync.Mutex
	runSlots   = map[string]chan struct{}{}
)

func acquireRunSlot(org string) bool {
	runSlotsMu.Lock()
	sem := runSlots[org]
	if sem == nil {
		sem = make(chan struct{}, maxConcurrentRunsPerOrg)
		runSlots[org] = sem
	}
	runSlotsMu.Unlock()
	select {
	case sem <- struct{}{}:
		return true
	default:
		return false // org is at its concurrent-run cap
	}
}

func releaseRunSlot(org string) {
	runSlotsMu.Lock()
	sem := runSlots[org]
	runSlotsMu.Unlock()
	if sem != nil {
		<-sem
	}
}

// nameRE constrains a dataset / evaluator / score-config / score name: it is BOTH
// the org-unique handle AND a URL path segment (/v1/evals/datasets/:name), so
// this is the injection/traversal guard at the boundary.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// validDataTypes are the score shapes a score-config / score may declare.
var validDataTypes = map[string]bool{"NUMERIC": true, "CATEGORICAL": true, "BOOLEAN": true}

// service holds the two stores, the pluggable runner, and the logger. It is the
// composition root for the eval surface; it owns no eval logic beyond wiring.
type service struct {
	store  *Store
	tel    Telemetry // nil ⇒ telemetry disabled (persistence skipped, logged)
	runner EvalRunner
	log    luxlog.Logger
}

// mounted is the active service so Shutdown can release the stores.
var mounted *service

// Mount registers the /v1/evals/* surface on app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("eval.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("eval.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "evals")
	if deps.DataDir == "" {
		return fmt.Errorf("eval.Mount: empty DataDir")
	}
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("eval.Mount: open metastore: %w", err)
	}
	tel, err := newDatastoreTelemetry(log)
	if err != nil {
		// Datastore is misconfigured (bad addr/creds) — an operator error worth
		// surfacing, but it must not take down the whole surface: log and run with
		// telemetry disabled rather than fail the mount. A nil datastore ADDR
		// returns (nil,nil) and lands here as "disabled".
		log.Warn("evals telemetry unavailable — traces/scores will not persist", "err", err)
		tel = nil
	}
	s := &service{
		store:  store,
		tel:    tel,
		runner: newGatewayRunner(),
		log:    log,
	}
	mounted = s

	routes(app, s)

	log.Info("evals surface mounted (native)", "brand", deps.Brand, "telemetry", tel != nil)
	return nil
}

// routes declares the sixteen ops of this surface. Every one is TYPED — its input
// and its answer are Go types, so the schema, the prose, the MCP tool, the CLI
// command and every generated SDK method are projections of the handler itself
// rather than of a hand-written table beside it.
//
// zipdoc lifts the doc comment off each op and each In/Out field into
// zipdoc_gen.go, which is the only way prose reaches the published registry: Go
// drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc
func routes(app cloud.Router, s *service) {
	// Static sub-routes are registered before any :name param route so a real
	// dataset name can never shadow a collection route.
	g := app.Group("/v1/evals")

	zip.Post(g, "/datasets", s.createDataset, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/datasets", s.listDatasets)
	zip.Get(g, "/datasets/:name", s.getDataset)
	zip.Delete(g, "/datasets/:name", s.deleteDataset)

	zip.Post(g, "/datasets/:name/items", s.createItem, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/datasets/:name/items", s.listItems)

	zip.Post(g, "/evaluators", s.createEvaluator, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/evaluators", s.listEvaluators)

	zip.Post(g, "/rubrics", s.createScoreConfig, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/rubrics", s.listScoreConfigs)

	zip.Post(g, "/scores", s.createScore, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/scores", s.listScores)

	zip.Get(g, "/traces", s.listTraces)

	// AI observability dashboard (the native Langfuse home): per-org / per-project
	// counts, cost, tokens, error & success rate, and latency percentiles over a
	// window — aggregated from the SAME cloud_usage ledger + GenAI spans.
	zip.Get(g, "/metrics", s.metricsBoard)

	// A run answers 200 with its summary, or 502 with the SAME summary when
	// nothing scored — the body is the evidence either way, which is why the
	// failure is a declared status rather than an error envelope.
	zip.Post(g, "/runs", s.runHandler, zip.WithStatus(http.StatusOK, http.StatusBadGateway))
	zip.Get(g, "/runs", s.listRuns)
}

// Shutdown releases the eval stores. Idempotent.
func Shutdown() error {
	if mounted == nil {
		return nil
	}
	var firstErr error
	if mounted.store != nil {
		if err := mounted.store.Close(); err != nil {
			firstErr = err
		}
	}
	if mounted.tel != nil {
		if err := mounted.tel.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	mounted = nil
	return firstErr
}

// ── tenant ───────────────────────────────────────────────────────────────────

// scope is the caller's project narrowing as a storage key: "" for the default
// project, which denotes the org's whole dataset, else the server-minted slug.
// It composes principal.ProjectFrom with the default-project rule, which is the
// context-side twin of principal.ProjectScope.
func scope(ctx context.Context) string {
	p := principal.ProjectFrom(ctx)
	if principal.IsDefaultProject(p) {
		return ""
	}
	return p
}

// page is the bound every list on this surface takes. It is embedded rather than
// repeated so one rule about how many rows a read returns is stated once.
type page struct {
	// Limit caps the rows returned. It defaults to 100 and is capped at 500; a
	// non-positive or unparseable value falls back to the default rather than
	// failing, because a typo about paging is not a reason to refuse a read.
	Limit int `json:"limit"`
}

// rows is the bounded row count a list actually reads.
func (p page) rows() int {
	if p.Limit <= 0 {
		return defaultListLimit
	}
	if p.Limit > maxListLimit {
		return maxListLimit
	}
	return p.Limit
}

// ── HTTP shapes (the contract the FE port consumes) ──────────────────────────

// datasetView is one dataset: the named set of graded examples a run scores a
// model against.
type datasetView struct {
	// Name is the dataset's org-unique handle and the segment that addresses it.
	Name string `json:"name"`
	// Description is free text the org wrote about what this set measures.
	Description string `json:"description"`
	// Metadata is the free-form object stored with the set, echoed back verbatim.
	Metadata map[string]any `json:"metadata"`
	// Items is how many examples the set holds. It is filled only by the single
	// read — a listing does not count, so it is absent there rather than zero.
	Items int `json:"items,omitempty"`
	// CreatedAt is when the name was first written, kept across later edits.
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is when the description or metadata last changed.
	UpdatedAt string `json:"updatedAt"`
}

// datasetList is the answer to a dataset listing.
type datasetList struct {
	// Data is the caller org's datasets, newest first, bounded by limit.
	Data []datasetView `json:"data"`
}

// itemView is one graded example: an input, the output it should produce, and
// the status that decides whether a run feeds it to the model.
type itemView struct {
	// ID is the example's handle, unique within the caller's org.
	ID string `json:"id"`
	// Dataset is the set this example belongs to.
	Dataset string `json:"datasetName"`
	// Input is what the model under test is given, as it was written.
	Input any `json:"input"`
	// Expected is the answer a correct model produces, which the judge grades against.
	Expected any `json:"expectedOutput"`
	// Metadata is the free-form object stored with the example.
	Metadata map[string]any `json:"metadata"`
	// Status is ACTIVE or ARCHIVED. Only ACTIVE examples are fed to a run, which
	// is how one is retired without being deleted.
	Status string `json:"status"`
	// CreatedAt is when the example was first written.
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is when it last changed.
	UpdatedAt string `json:"updatedAt"`
}

// itemList is the answer to an example listing.
type itemList struct {
	// Data is the examples of the one dataset named in the path, archived ones
	// included, so the caller sees the whole set rather than what a run would use.
	Data []itemView `json:"data"`
}

// evaluatorView is one judge: the model that grades, and the criteria it grades by.
type evaluatorView struct {
	// Name is the judge's org-unique handle.
	Name string `json:"name"`
	// Model is the model that does the grading.
	Model string `json:"model"`
	// Criteria is the written standard the judge applies.
	Criteria string `json:"criteria"`
	// ScoreName is the name the resulting scores are filed under.
	ScoreName string `json:"scoreName"`
	// CreatedAt is when the judge was first defined.
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is when it last changed.
	UpdatedAt string `json:"updatedAt"`
}

// evaluatorList is the answer to a judge listing.
type evaluatorList struct {
	// Data is the caller org's judges, bounded by limit.
	Data []evaluatorView `json:"data"`
}

// scoreConfigView is one rubric: what a score of a given name is allowed to be.
type scoreConfigView struct {
	// Name is the score name this rubric governs.
	Name string `json:"name"`
	// DataType is NUMERIC, CATEGORICAL or BOOLEAN, and is authoritative — a
	// score recorded under this name cannot claim a different one.
	DataType string `json:"dataType"`
	// MinValue is the inclusive floor a NUMERIC score must clear, absent when unbounded.
	MinValue *float64 `json:"minValue,omitempty"`
	// MaxValue is the inclusive ceiling a NUMERIC score must stay under, absent when unbounded.
	MaxValue *float64 `json:"maxValue,omitempty"`
	// Categories is the closed set of labels a CATEGORICAL score may carry.
	Categories []string `json:"categories,omitempty"`
	// CreatedAt is when the rubric was first declared.
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is when it last changed.
	UpdatedAt string `json:"updatedAt"`
}

// scoreConfigList is the answer to a rubric listing.
type scoreConfigList struct {
	// Data is the caller org's rubrics, bounded by limit.
	Data []scoreConfigView `json:"data"`
}

// scoreView is one recorded score event.
type scoreView struct {
	// ID is the score event's handle.
	ID string `json:"id"`
	// Name is the score name, which a rubric of the same name governs.
	Name string `json:"name"`
	// TraceID is the model call this score grades, when it grades one.
	TraceID string `json:"traceId,omitempty"`
	// RunName is the run this score was recorded under, when it came from one.
	RunName string `json:"runName,omitempty"`
	// DataType is NUMERIC, CATEGORICAL or BOOLEAN.
	DataType string `json:"dataType"`
	// Value is the numeric score; for BOOLEAN it is 0 or 1.
	Value float64 `json:"value"`
	// StringValue is the label of a CATEGORICAL score.
	StringValue string `json:"stringValue,omitempty"`
	// Comment is the grader's reasoning, truncated at 2000 characters.
	Comment string `json:"comment,omitempty"`
	// Timestamp is when the score was recorded.
	Timestamp string `json:"timestamp"`
}

// scoreList is the answer to a score listing.
type scoreList struct {
	// Data is the caller org's score events matching the filters, bounded by limit.
	Data []scoreView `json:"data"`
}

// traceView is one model call an evaluation made.
type traceView struct {
	// ID is the trace's handle, the value a score points at.
	ID string `json:"id"`
	// Name is the trace's label, "eval:<run>" for a call a run made.
	Name string `json:"name"`
	// ProjectID is the sub-scope within the org the call was made under.
	ProjectID string `json:"projectId,omitempty"`
	// SessionID groups the calls of one run.
	SessionID string `json:"sessionId,omitempty"`
	// Dataset is the set the graded example came from.
	Dataset string `json:"datasetName,omitempty"`
	// ItemID is the example the call answered.
	ItemID string `json:"datasetItemId,omitempty"`
	// RunName is the run the call belongs to.
	RunName string `json:"runName,omitempty"`
	// Model is the model that answered.
	Model string `json:"model,omitempty"`
	// Input is what the model was given.
	Input any `json:"input,omitempty"`
	// Output is what it answered.
	Output string `json:"output,omitempty"`
	// LatencyMs is EndTime-StartTime in milliseconds, nil when the trace carries
	// no timing (so the console renders "—", never a fabricated 0).
	LatencyMs *float64 `json:"latencyMs,omitempty"`
	// StartTime is when the call began.
	StartTime string `json:"startTime,omitempty"`
	// EndTime is when it returned.
	EndTime string `json:"endTime,omitempty"`
	// Timestamp is the trace's own clock, equal to StartTime for a timed call.
	Timestamp string `json:"timestamp"`
	// APIKeyHash is the non-reversible credential ref (never a plaintext key), so
	// a trace correlates to the key that drove it without the store holding a secret.
	APIKeyHash string `json:"apiKeyHash,omitempty"`
}

// traceList is the answer to a trace listing.
type traceList struct {
	// Data is the caller org's traces matching the filters, bounded by limit.
	Data []traceView `json:"data"`
}

// ── datasets ─────────────────────────────────────────────────────────────────

// datasetReq creates or edits one dataset. The NAME is the key: posting a name
// the org already has edits that dataset rather than adding a second one.
type datasetReq struct {
	// Name is the dataset's org-unique handle and the segment that will address
	// it, so it must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$.
	Name string `json:"name" validate:"required"`
	// Description is free text about what this set measures; over 64 KiB is refused.
	Description string `json:"description"`
	// Metadata is a free-form object stored with the set and echoed back verbatim.
	Metadata map[string]any `json:"metadata"`
}

// datasetRef addresses one of the caller org's datasets by name.
type datasetRef struct {
	// Name is the dataset the URL names.
	Name string `json:"name"`
}

// none is the answer of an op that removes something: there is nothing left to
// describe, so it answers 204 and no body.
type none struct{}

// createDataset writes a dataset — the named set of graded examples a run scores
// a model against — under the caller's org and answers 201 with it.
//
// The NAME is the key, not an id: posting a name the org already has updates that
// dataset's description and metadata and keeps its original creation time, so this
// is create-or-edit and never a duplicate. Its items are untouched.
//
// Requires a validated principal; 403 without one. The org comes from the
// validated owner claim, never from a client X-Org-Id, so a dataset can only ever
// be written under the caller's own tenant. A description over 64 KiB is 400.
func (s *service) createDataset(ctx context.Context, in *datasetReq) (*datasetView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	name, err := requireName(in.Name)
	if err != nil {
		return nil, err
	}
	meta, err := encodeMeta(in.Metadata)
	if err != nil {
		return nil, err
	}
	if len(in.Description) > maxContent {
		return nil, zip.ErrBadRequest("description too large")
	}
	id := mint.ID("ds")
	d, err := s.store.UpsertDataset(ctx, Dataset{
		ID: id, Org: org, Name: name, Description: in.Description, Metadata: meta,
		UpdatedAt: time.Now().Unix(),
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	out := toDatasetView(d, 0)
	return &out, nil
}

// listDatasets is the datasets your org has, each with its name, description,
// metadata and timestamps.
//
// It is the only way to enumerate what an org holds. Requires a validated
// principal; 403 without one. Every row is filtered on the validated org, so
// there is no parameter that reaches another tenant's datasets. The item count is
// NOT populated here — read one dataset to get it.
func (s *service) listDatasets(ctx context.Context, in *page) (*datasetList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListDatasets(ctx, org, in.rows())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]datasetView, 0, len(rows))
	for _, d := range rows {
		out = append(out, toDatasetView(d, 0))
	}
	return &datasetList{Data: out}, nil
}

// getDataset returns one dataset of the caller's org by name, together with its
// live item count — the one read that answers how big the set actually is.
//
// A name this org does not have is 404, which is also what another tenant's
// dataset looks like from here. Requires a validated principal; 403 without one.
func (s *service) getDataset(ctx context.Context, in *datasetRef) (*datasetView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	d, err := s.store.GetDataset(ctx, org, name)
	if err == errNotFound {
		return nil, zip.ErrNotFound("dataset not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	n, err := s.store.CountItems(ctx, org, name)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "items: %v", err)
	}
	out := toDatasetView(d, n)
	return &out, nil
}

// deleteDataset removes the named dataset of the caller's org AND all of its
// examples, in one transaction.
//
// This is not a detach: the examples are gone with the set, so a dataset cannot
// be resurrected by re-creating the name. A name this org does not have is 404 —
// never a silent success — and a name belonging to another tenant is the same
// 404, because the delete is predicated on the validated org. Requires a
// validated principal; 403 without one. Runs and scores already recorded against
// the dataset are telemetry events and are NOT deleted with it.
func (s *service) deleteDataset(ctx context.Context, in *datasetRef) (*none, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := s.store.DeleteDataset(ctx, org, strings.TrimSpace(in.Name))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("dataset not found")
	}
	return nil, nil
}

// ── dataset items ────────────────────────────────────────────────────────────

// itemReq adds a graded example to the dataset the URL names.
type itemReq struct {
	// Dataset is the set the example is written into, taken from the path. It
	// must already exist for the caller's org: an unknown one is 404, never a
	// silent create, so an example can never be attached to a set the caller does
	// not own. It rides the URL only, so the body never carries it.
	Dataset string `json:"-" url:"name"`
	// ID makes the write idempotent — re-posting the same id replaces that example
	// in place. Omit it and one is generated. An id that already exists in a
	// DIFFERENT dataset is 409 rather than a move.
	ID string `json:"id"`
	// Input is what the model under test will be given, stored as raw JSON exactly
	// as sent, up to 64 KiB.
	Input json.RawMessage `json:"input"`
	// Expected is the answer a correct model produces, which the judge grades
	// against, stored as raw JSON exactly as sent, up to 64 KiB.
	Expected json.RawMessage `json:"expectedOutput"`
	// Metadata is a free-form object stored with the example.
	Metadata map[string]any `json:"metadata"`
	// Status is ACTIVE (the default) or ARCHIVED. Only ACTIVE examples are fed to
	// a run, which is how an example is retired without being deleted.
	Status string `json:"status"`
}

// itemPage lists the examples of one dataset.
type itemPage struct {
	// Dataset is the set to read, from the path — this collection only exists
	// inside one.
	Dataset string `json:"name"`
	page
}

// createItem writes one graded example — its input, its expected output,
// free-form metadata and a status — into the dataset named in the path, and
// answers 201 with it.
//
// That dataset MUST already exist for this org: an unknown one is 404, never a
// silent create. Requires a validated principal; 403 without one.
func (s *service) createItem(ctx context.Context, in *itemReq) (*itemView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	dataset := strings.TrimSpace(in.Dataset)
	// The dataset MUST exist for THIS org — an item can never be attached to a
	// dataset the caller doesn't own (a real 404, not a silent create).
	if _, err := s.store.GetDataset(ctx, org, dataset); err == errNotFound {
		return nil, zip.ErrNotFound("dataset not found")
	} else if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "dataset: %v", err)
	}
	input, err := rawJSON(in.Input, "input")
	if err != nil {
		return nil, err
	}
	expected, err := rawJSON(in.Expected, "expectedOutput")
	if err != nil {
		return nil, err
	}
	meta, err := encodeMeta(in.Metadata)
	if err != nil {
		return nil, err
	}
	status := strings.ToUpper(strings.TrimSpace(in.Status))
	if status == "" {
		status = "ACTIVE"
	}
	if status != "ACTIVE" && status != "ARCHIVED" {
		return nil, zip.ErrBadRequest("status must be ACTIVE or ARCHIVED")
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		id = mint.ID("item")
	} else if !nameRE.MatchString(id) {
		return nil, zip.ErrBadRequest("id must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	it, err := s.store.PutItem(ctx, DatasetItem{
		ID: id, Org: org, Dataset: dataset, Input: input, Expected: expected,
		Metadata: meta, Status: status, UpdatedAt: time.Now().Unix(),
	})
	if err == errConflict {
		return nil, zip.ErrConflict("item id already exists in a different dataset")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	out := toItemView(it)
	return &out, nil
}

// listItems is the examples in one of your datasets — the set is named in the
// path, because this collection only exists inside one.
//
// Archived examples are included, so the caller sees the whole set rather than
// only what a run would use. Requires a validated principal; 403 without one, and
// the read is filtered on the validated org, so naming another tenant's dataset
// returns nothing rather than its contents.
func (s *service) listItems(ctx context.Context, in *itemPage) (*itemList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	items, err := s.store.ListItems(ctx, org, strings.TrimSpace(in.Dataset), false, in.rows())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]itemView, 0, len(items))
	for _, it := range items {
		out = append(out, toItemView(it))
	}
	return &itemList{Data: out}, nil
}

// ── evaluators ───────────────────────────────────────────────────────────────

// evaluatorReq defines a judge: a model plus the criteria it grades by.
type evaluatorReq struct {
	// Name is the judge's org-unique handle, matching
	// ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$. Re-posting a name edits that judge
	// rather than adding a second one.
	Name string `json:"name" validate:"required"`
	// Model is the model that will do the grading.
	Model string `json:"model"`
	// Criteria is the written standard the judge applies; over 64 KiB is refused.
	Criteria string `json:"criteria"`
	// ScoreName is the name the resulting scores are filed under. It defaults to
	// the judge's own name and must match the same pattern.
	ScoreName string `json:"scoreName"`
}

// createEvaluator saves a reusable judge for the caller's org — the judge model
// and the written criteria it grades against — and answers 201 with it.
//
// Like a dataset, the NAME is the key: re-posting a name edits that judge rather
// than adding a second one. Requires a validated principal; 403 without one.
func (s *service) createEvaluator(ctx context.Context, in *evaluatorReq) (*evaluatorView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	name, err := requireName(in.Name)
	if err != nil {
		return nil, err
	}
	if len(in.Criteria) > maxContent {
		return nil, zip.ErrBadRequest("criteria too large")
	}
	scoreName := strings.TrimSpace(in.ScoreName)
	if scoreName == "" {
		scoreName = name
	} else if !nameRE.MatchString(scoreName) {
		return nil, zip.ErrBadRequest("scoreName must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	id := mint.ID("eval")
	e, err := s.store.UpsertEvaluator(ctx, Evaluator{
		ID: id, Org: org, Name: name, Model: strings.TrimSpace(in.Model),
		Criteria: in.Criteria, ScoreName: scoreName, UpdatedAt: time.Now().Unix(),
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	out := toEvaluatorView(e)
	return &out, nil
}

// listEvaluators is the judges your org has defined, each with its judge model,
// criteria and the score name it writes under.
//
// Requires a validated principal; 403 without one, and the listing is filtered on
// the validated org.
func (s *service) listEvaluators(ctx context.Context, in *page) (*evaluatorList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListEvaluators(ctx, org, in.rows())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]evaluatorView, 0, len(rows))
	for _, e := range rows {
		out = append(out, toEvaluatorView(e))
	}
	return &evaluatorList{Data: out}, nil
}

// ── score configs ────────────────────────────────────────────────────────────

// scoreConfigReq declares what a score of a given name is allowed to be.
type scoreConfigReq struct {
	// Name is the score name this rubric governs, matching
	// ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$. The name is the key, so re-posting one
	// replaces its rules.
	Name string `json:"name" validate:"required"`
	// DataType is NUMERIC (the default), CATEGORICAL or BOOLEAN.
	DataType string `json:"dataType"`
	// MinValue is the inclusive floor a NUMERIC score must clear. It must be
	// finite and must not exceed MaxValue.
	MinValue *float64 `json:"minValue"`
	// MaxValue is the inclusive ceiling a NUMERIC score must stay under, finite.
	MaxValue *float64 `json:"maxValue"`
	// Categories is the closed set of labels a CATEGORICAL score may carry. A
	// CATEGORICAL rubric with none is refused.
	Categories []string `json:"categories"`
}

// createScoreConfig defines the shape of one score name for the caller's org and
// answers 201 with it.
//
// This is the integrity contract, not documentation: once a rubric exists for a
// name, every score recorded under that name is checked against it and the
// rubric's data type is AUTHORITATIVE — a caller cannot claim a different one.
// Out-of-range values, unlisted labels and non-finite numbers are refused at
// write time.
//
// A CATEGORICAL rubric with no categories is 400, as is a non-finite bound or a
// minValue above maxValue. Requires a validated principal; 403 without one.
func (s *service) createScoreConfig(ctx context.Context, in *scoreConfigReq) (*scoreConfigView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	name, err := requireName(in.Name)
	if err != nil {
		return nil, err
	}
	dt := strings.ToUpper(strings.TrimSpace(in.DataType))
	if dt == "" {
		dt = "NUMERIC"
	}
	if !validDataTypes[dt] {
		return nil, zip.ErrBadRequest("dataType must be NUMERIC, CATEGORICAL, or BOOLEAN")
	}
	if in.MinValue != nil && !finite(*in.MinValue) {
		return nil, zip.ErrBadRequest("minValue must be finite")
	}
	if in.MaxValue != nil && !finite(*in.MaxValue) {
		return nil, zip.ErrBadRequest("maxValue must be finite")
	}
	if in.MinValue != nil && in.MaxValue != nil && *in.MinValue > *in.MaxValue {
		return nil, zip.ErrBadRequest("minValue must not exceed maxValue")
	}
	cats := cleanCategories(in.Categories)
	if dt == "CATEGORICAL" && len(cats) == 0 {
		return nil, zip.ErrBadRequest("CATEGORICAL config requires at least one category")
	}
	id := mint.ID("sc")
	cfg, err := s.store.UpsertScoreConfig(ctx, ScoreConfig{
		ID: id, Org: org, Name: name, DataType: dt,
		MinValue: in.MinValue, MaxValue: in.MaxValue, Categories: cats,
		UpdatedAt: time.Now().Unix(),
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	out := toScoreConfigView(cfg)
	return &out, nil
}

// listScoreConfigs is the score shapes your org has declared — each name's data
// type, its numeric bounds and its allowed categories.
//
// Requires a validated principal; 403 without one, and the listing is filtered on
// the validated org.
func (s *service) listScoreConfigs(ctx context.Context, in *page) (*scoreConfigList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListScoreConfigs(ctx, org, in.rows())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]scoreConfigView, 0, len(rows))
	for _, cfg := range rows {
		out = append(out, toScoreConfigView(cfg))
	}
	return &scoreConfigList{Data: out}, nil
}

// ── scores (telemetry events) ────────────────────────────────────────────────

// scoreReq records one score against a trace, a run or an example.
type scoreReq struct {
	// Name is the score name. A rubric of the same name, if the org has declared
	// one, decides this score's type and the values it may take.
	Name string `json:"name" validate:"required"`
	// TraceID attaches the score to one model call.
	TraceID string `json:"traceId"`
	// RunName attaches the score to one run.
	RunName string `json:"runName"`
	// Dataset attaches the score to one dataset.
	Dataset string `json:"datasetName"`
	// ItemID attaches the score to one graded example.
	ItemID string `json:"datasetItemId"`
	// DataType is NUMERIC, CATEGORICAL or BOOLEAN. A declared rubric overrides it
	// — a caller cannot claim a type the org's rubric contradicts.
	DataType string `json:"dataType"`
	// Value is the numeric score, which must be finite: NaN and Inf are refused.
	// A BOOLEAN score takes 0 or 1.
	Value *float64 `json:"value"`
	// StringValue is the label of a CATEGORICAL score, which must be one the
	// rubric allows.
	StringValue string `json:"stringValue"`
	// Comment is the grader's reasoning, truncated at 2000 characters.
	Comment string `json:"comment"`
}

// createScore files one score event for the caller's org and answers 201 with it.
//
// This is how human review and out-of-band graders land beside the automatic
// ones: name the score, give it a value (or a stringValue for a categorical
// label), and attach it to a trace, a run, a dataset example, or any combination.
//
// Scores are validated fail-closed. A value must be FINITE — NaN and Inf are 400
// — and if the org has declared a rubric for this name, that rubric decides the
// type and the value must satisfy it: inside the numeric bounds, or one of the
// allowed categories. A caller cannot override the declared type by sending a
// different dataType.
//
// A score is TELEMETRY, not metadata, so it needs the datastore: a deployment
// with none wired answers 503 rather than accepting a score it cannot persist.
// Requires a validated principal; 403 without one, and the org is stamped from
// the validated claim rather than read off the body.
func (s *service) createScore(ctx context.Context, in *scoreReq) (*scoreView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if s.tel == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "evals telemetry (datastore) not configured")
	}
	name, err := requireName(in.Name)
	if err != nil {
		return nil, err
	}
	ev, err := s.validateScore(ctx, org, name, *in)
	if err != nil {
		return nil, err
	}
	if err := s.tel.RecordScore(ctx, ev); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "record score: %v", err)
	}
	out := toScoreView(ev)
	return &out, nil
}

// validateScore turns a score request into a validated, org-stamped ScoreEvent.
// It enforces score integrity: the value must be finite; if a score-config with
// this name exists for the org, the value/label must satisfy it (NUMERIC in
// [min,max]; CATEGORICAL in the allowed set; BOOLEAN 0/1). No config ⇒ a numeric
// score defaults to a finite float. This is the choke point Red will attack for
// NaN/Inf/out-of-range/label-forgery, so it fails closed on every violation.
func (s *service) validateScore(ctx context.Context, org, name string, body scoreReq) (ScoreEvent, error) {
	dt := strings.ToUpper(strings.TrimSpace(body.DataType))
	cfg, cfgErr := s.store.GetScoreConfig(ctx, org, name)
	hasCfg := cfgErr == nil
	if cfgErr != nil && cfgErr != errNotFound {
		return ScoreEvent{}, zip.Errorf(http.StatusInternalServerError, "score config: %v", cfgErr)
	}
	if hasCfg {
		// A configured score is authoritative for the type — a caller cannot claim
		// a different dataType than the org's config declares.
		dt = cfg.DataType
	}
	if dt == "" {
		dt = "NUMERIC"
	}
	if !validDataTypes[dt] {
		return ScoreEvent{}, zip.ErrBadRequest("dataType must be NUMERIC, CATEGORICAL, or BOOLEAN")
	}

	ev := ScoreEvent{
		Org: org, Name: name, DataType: dt,
		TraceID: strings.TrimSpace(body.TraceID), RunName: strings.TrimSpace(body.RunName),
		Dataset: strings.TrimSpace(body.Dataset), ItemID: strings.TrimSpace(body.ItemID),
		Comment: truncate(body.Comment, maxComment),
	}
	id := mint.ID("score")
	ev.ID = id

	switch dt {
	case "NUMERIC":
		if body.Value == nil {
			return ScoreEvent{}, zip.ErrBadRequest("NUMERIC score requires a value")
		}
		v := *body.Value
		if !finite(v) {
			return ScoreEvent{}, zip.ErrBadRequest("value must be finite (no NaN/Inf)")
		}
		if hasCfg && cfg.MinValue != nil && v < *cfg.MinValue {
			return ScoreEvent{}, zip.ErrBadRequest("value below configured minimum")
		}
		if hasCfg && cfg.MaxValue != nil && v > *cfg.MaxValue {
			return ScoreEvent{}, zip.ErrBadRequest("value above configured maximum")
		}
		ev.Value = v
	case "BOOLEAN":
		if body.Value == nil || (*body.Value != 0 && *body.Value != 1) {
			return ScoreEvent{}, zip.ErrBadRequest("BOOLEAN score requires value 0 or 1")
		}
		ev.Value = *body.Value
	case "CATEGORICAL":
		label := strings.TrimSpace(body.StringValue)
		if label == "" {
			return ScoreEvent{}, zip.ErrBadRequest("CATEGORICAL score requires stringValue")
		}
		if len(label) > 256 {
			return ScoreEvent{}, zip.ErrBadRequest("stringValue too long")
		}
		if hasCfg && !slices.Contains(cfg.Categories, label) {
			return ScoreEvent{}, zip.ErrBadRequest("stringValue not in the configured category set")
		}
		ev.StringValue = label
	}
	return ev, nil
}

// scoreFilter narrows a score listing. An absent filter simply does not narrow.
type scoreFilter struct {
	// Name narrows to one score name.
	Name string `json:"name"`
	// RunName narrows to the scores of one run.
	RunName string `json:"runName"`
	// TraceID narrows to the scores on one model call.
	TraceID string `json:"traceId"`
	page
}

// traceFilter narrows a trace listing. An absent filter simply does not narrow.
type traceFilter struct {
	// SessionID narrows to one session, which for an evaluation is one run.
	SessionID string `json:"sessionId"`
	// RunName narrows to the calls one run made.
	RunName string `json:"runName"`
	// Dataset narrows to the calls made against one dataset.
	Dataset string `json:"datasetName"`
	page
}

// listScores is the score events your org has recorded, narrowed by any of name,
// runName and traceId.
//
// The org is bound as an authoritative predicate on the query, never taken from a
// header, so a filter can narrow the caller's own scores but can never widen past
// them. Requires a validated principal; 403 without one. Scores live in the
// datastore, so a deployment with none wired answers 503 rather than an empty
// page that would read as "no scores".
func (s *service) listScores(ctx context.Context, in *scoreFilter) (*scoreList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if s.tel == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "evals telemetry (datastore) not configured")
	}
	scores, err := s.tel.ListScores(ctx, ScoreFilter{
		Org:     org, // authoritative — never a client header
		Name:    strings.TrimSpace(in.Name),
		RunName: strings.TrimSpace(in.RunName),
		TraceID: strings.TrimSpace(in.TraceID),
		Limit:   in.rows(),
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list scores: %v", err)
	}
	out := make([]scoreView, 0, len(scores))
	for _, sc := range scores {
		out = append(out, toScoreView(sc))
	}
	return &scoreList{Data: out}, nil
}

// listTraces is the traces behind your evaluations — one per model call an
// evaluation made, carrying its input, output, model and timing — narrowed by any
// of sessionId, runName and datasetName.
//
// Scoped by org AND by project: the project is the caller's server-minted scope,
// not a parameter, so it cannot be widened by asking. Requires a validated
// principal; 403 without one. Traces live in the datastore, so a deployment with
// none wired answers 503 rather than an empty page.
func (s *service) listTraces(ctx context.Context, in *traceFilter) (*traceList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if s.tel == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "evals telemetry (datastore) not configured")
	}
	traces, err := s.tel.ListTraces(ctx, TraceFilter{
		Org:       org,        // authoritative
		ProjectID: scope(ctx), // server-minted; "" for the default project (whole org)
		SessionID: strings.TrimSpace(in.SessionID),
		RunName:   strings.TrimSpace(in.RunName),
		Dataset:   strings.TrimSpace(in.Dataset),
		Limit:     in.rows(),
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list traces: %v", err)
	}
	out := make([]traceView, 0, len(traces))
	for _, tr := range traces {
		out = append(out, toTraceView(tr))
	}
	return &traceList{Data: out}, nil
}

// ── run orchestration ────────────────────────────────────────────────────────

// judgeSpec is the judge one run grades with.
type judgeSpec struct {
	// Model is the model that grades. It defaults to the model under test, so a
	// run with no judge named has the model grade itself.
	Model string `json:"model"`
	// Criteria is the standard the judge applies, defaulting to a correctness
	// criterion.
	Criteria string `json:"criteria"`
	// Name is the score name the judge's grades are filed under, "llm-judge" by
	// default. A name that is not a legal handle falls back to that default
	// rather than being stored as sent.
	Name string `json:"name"`
}

// runRequest scores a dataset through a model and a judge, synchronously.
type runRequest struct {
	// Dataset is the set to score, which must belong to the caller's org and hold
	// at least one ACTIVE example.
	Dataset string `json:"dataset" validate:"required"`
	// Model is the model under test.
	Model string `json:"model" validate:"required"`
	// RunName labels the run and is generated from the clock when omitted. It
	// must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$.
	RunName string `json:"runName"`
	// Limit caps how many examples this run scores. It defaults to 20, and
	// anything above 100 falls back to that default.
	Limit int `json:"limit"`
	// Judge is the judge to grade with. Omitted, the model under test grades
	// itself against a default correctness criterion under the score name
	// "llm-judge".
	Judge *judgeSpec `json:"judge"`
	// Authorization is the caller's own bearer, which drives the model gateway: a
	// run happens AS YOU, never anonymously or under a service identity, and a
	// request without one is 401 — a missing credential, not a malformed request.
	// Only a non-reversible hash of it is recorded on the traces.
	Authorization string `json:"-" url:"-" header:"Authorization"`
}

// itemResult is what one graded example produced.
type itemResult struct {
	// ItemID is the example that was scored.
	ItemID string `json:"itemId"`
	// TraceID is the model call this result came from.
	TraceID string `json:"traceId,omitempty"`
	// Score is the judge's grade.
	Score float64 `json:"score"`
	// Output is what the model under test answered, truncated at 2000 characters.
	Output string `json:"output,omitempty"`
	// Error is why this example produced no score — the model, the judge, or the
	// run's deadline. A result carrying one is not counted in Scored.
	Error string `json:"error,omitempty"`
}

// runSummary is a finished run.
type runSummary struct {
	// Dataset is the set that was scored.
	Dataset string `json:"dataset"`
	// Model is the model under test.
	Model string `json:"model"`
	// JudgeModel is the model that graded.
	JudgeModel string `json:"judgeModel"`
	// RunName is the run's label, which scores and traces are filed under.
	RunName string `json:"runName"`
	// Items is how many examples the run attempted.
	Items int `json:"items"`
	// Scored is how many produced a real score. It counts successes only, so a
	// partial run is honest about what it achieved.
	Scored int `json:"scored"`
	// AvgScore is the mean over the scored examples, 0 when none scored.
	AvgScore float64 `json:"avgScore"`
	// Results is one row per attempted example.
	Results []itemResult `json:"results"`
}

// StatusCode is 200 for a run that scored something and 502 for one that scored
// nothing. A run where NOTHING scored is a real failure, not a 200 that looks
// like an evaluation — and the summary is the evidence, so it rides the failure
// rather than being replaced by an error envelope.
func (r *runSummary) StatusCode() int {
	if r.Scored == 0 {
		return http.StatusBadGateway
	}
	return http.StatusOK
}

// runFilter narrows a run listing.
type runFilter struct {
	// Dataset narrows to the runs against one dataset.
	Dataset string `json:"datasetName"`
	page
}

// runRecord is one durable run record.
type runRecord struct {
	// Dataset is the set that was scored.
	Dataset string `json:"dataset"`
	// RunName is the run's label.
	RunName string `json:"runName"`
	// Model is the model under test.
	Model string `json:"model"`
	// JudgeModel is the model that graded.
	JudgeModel string `json:"judgeModel"`
	// Items is how many examples were attempted.
	Items int `json:"items"`
	// Scored is how many produced a real score.
	Scored int `json:"scored"`
	// AvgScore is the mean over the scored examples.
	AvgScore float64 `json:"avgScore"`
	// CreatedAt is when the run first landed.
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is when the record last changed.
	UpdatedAt string `json:"updatedAt"`
}

// runs is the answer to a run listing.
type runs struct {
	// Data is the caller org's runs, bounded by limit.
	Data []runRecord `json:"data"`
}

// runHandler runs a real evaluation and answers the summary when it is finished —
// this is synchronous work, not a job id.
//
// For each ACTIVE example in the dataset it calls the model under test, records a
// trace, calls the LLM-as-judge, and records the judge's score with its
// reasoning. The answer carries the per-item results (item id, trace id, score,
// output or error) alongside items, scored and avgScore.
//
// The dataset must belong to the caller's org (404 otherwise) and must have at
// least one ACTIVE example (422 otherwise).
//
// It runs as YOU: the caller's own Authorization bearer drives the model gateway,
// so a request without one is 401 rather than a run made anonymously or under a
// service identity. Only a non-reversible hash of that credential is recorded on
// the traces.
//
// Bounded and honest about it: an org may have at most 4 runs in flight and the
// fifth is 429 rather than queued, and the whole run is capped at 10 minutes —
// examples past the deadline come back with an error instead of a score, and
// scored counts only real successes. A run where NOTHING scored answers 502, not
// a 200 that looks like an evaluation. A run must be able to persist what it
// produces, so a deployment with no datastore wired is 503 up front. Requires a
// validated principal; 403 without one.
func (s *service) runHandler(ctx context.Context, in *runRequest) (*runSummary, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	// A run has no durable record without telemetry — a real eval PERSISTS its
	// traces + scores. Rather than run models and return scores we can't store
	// (a fake success), fail closed when the datastore is not wired, exactly as
	// createScore/listScores do.
	if s.tel == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "evals/runs: telemetry (datastore) not configured; a run cannot persist traces/scores")
	}
	// The model gateway needs the caller's own credential — fail closed rather
	// than run models anonymously or with a service identity.
	authz := strings.TrimSpace(in.Authorization)
	if authz == "" {
		return nil, zip.ErrUnauthorized("evals/runs: missing Authorization bearer; the model gateway needs the caller's key/JWT")
	}
	if strings.TrimSpace(in.Dataset) == "" || strings.TrimSpace(in.Model) == "" {
		return nil, zip.ErrBadRequest("evals/runs: 'dataset' and 'model' are required")
	}
	// The dataset must belong to THIS org (a real 404, never a cross-tenant read).
	if _, err := s.store.GetDataset(ctx, org, in.Dataset); err == errNotFound {
		return nil, zip.ErrNotFound("dataset not found")
	} else if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "dataset: %v", err)
	}

	limit := in.Limit
	if limit <= 0 || limit > maxRunItems {
		limit = defaultRunItems
	}
	runName := strings.TrimSpace(in.RunName)
	if runName == "" {
		runName = "run-" + time.Now().UTC().Format("20060102T150405Z")
	} else if !nameRE.MatchString(runName) {
		return nil, zip.ErrBadRequest("runName must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	judge := normalizeJudge(in.Judge, in.Model)

	items, err := s.store.ListItems(ctx, org, in.Dataset, true, limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "items: %v", err)
	}
	if len(items) == 0 {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "evals/runs: dataset %q has no active items", in.Dataset)
	}

	// Bound the run (Red MED): fail fast if this org is already at its concurrent-
	// run cap (429, never queued), and cap the whole run's wall-clock so a runaway
	// can't pin the request + a shared-gateway slot. The slot is released and the
	// context cancelled on every return path.
	if !acquireRunSlot(org) {
		return nil, zip.Errorf(http.StatusTooManyRequests,
			"evals/runs: too many concurrent runs for this org (max %d); retry when one finishes", maxConcurrentRunsPerOrg)
	}
	defer releaseRunSlot(org)
	runCtx, cancel := context.WithTimeout(ctx, maxRunDuration)
	defer cancel()

	// Attribution threaded into every item trace: the caller's server-minted
	// project narrows within the validated org; the credential is stored only as a
	// non-reversible ref, never in plaintext.
	attr := runAttribution{projectID: scope(ctx), apiKeyHash: hashCredential(authz)}

	summary := runSummary{Dataset: in.Dataset, Model: in.Model, JudgeModel: judge.Model, RunName: runName, Items: len(items)}
	var sum float64
	for _, it := range items {
		// Stop early once the deadline is hit; remaining items are unrun, and the
		// partial summary is returned honestly (Scored counts only real successes).
		if runCtx.Err() != nil {
			summary.Results = append(summary.Results, itemResult{ItemID: it.ID, Error: "run: " + runCtx.Err().Error()})
			continue
		}
		res := s.runItem(runCtx, org, authz, runName, in.Model, judge, attr, it)
		summary.Results = append(summary.Results, res)
		if res.Error == "" {
			sum += res.Score
			summary.Scored++
		}
	}
	if summary.Scored > 0 {
		summary.AvgScore = sum / float64(summary.Scored)
	}

	// Persist the durable run record (metastore), best-effort — a metastore write
	// failure is logged, never masks the run result.
	id := mint.ID("run")
	if _, uerr := s.store.UpsertRun(ctx, DatasetRun{
		ID: id, Org: org, Dataset: in.Dataset, Name: runName, Model: in.Model,
		JudgeModel: judge.Model, Items: summary.Items, Scored: summary.Scored,
		AvgScore: summary.AvgScore, UpdatedAt: time.Now().Unix(),
	}); uerr != nil {
		s.log.Warn("run record not persisted", "run", runName, "err", uerr)
	}
	return &summary, nil
}

// runAttribution is the per-run observability context threaded into every item
// trace: the caller's server-minted project (principal.Project) and the
// non-reversible ref of the credential the run drives the model with.
type runAttribution struct {
	projectID  string
	apiKeyHash string
}

// runItem is the single per-item seam, run synchronously via the pluggable
// runner: (1) model-under-test (timed), (2) trace → telemetry, (3) LLM-as-judge,
// (4) validate + record the score. Telemetry writes are best-effort when the
// datastore is present; a persistence miss is recorded as the item error so the
// summary never claims a score it failed to store.
func (s *service) runItem(ctx context.Context, org, authz, runName, model string, judge judgeSpec, attr runAttribution, it DatasetItem) itemResult {
	res := itemResult{ItemID: it.ID}

	// Time the model-under-test call so the trace carries real latency.
	start := time.Now().UTC()
	output, err := s.runner.Complete(ctx, authz, model, decodeAny(it.Input))
	end := time.Now().UTC()
	if err != nil {
		res.Error = "model: " + err.Error()
		return res
	}
	res.Output = truncate(output, maxComment)

	traceID := genUUID()
	res.TraceID = traceID
	if s.tel != nil {
		if err := s.tel.RecordTrace(ctx, Trace{
			ID: traceID, Org: org, ProjectID: attr.projectID, Name: "eval:" + runName,
			Dataset: it.Dataset, ItemID: it.ID, RunName: runName,
			SessionID: runName, APIKeyHash: attr.apiKeyHash, Model: model,
			Input: it.Input, Output: output, StartTime: start, EndTime: end,
			Timestamp: start,
		}); err != nil {
			res.Error = "trace: " + err.Error()
			return res
		}
	}

	score, reasoning, err := s.runner.Judge(ctx, authz, judge, decodeAny(it.Input), decodeAny(it.Expected), output)
	if err != nil {
		res.Error = "judge: " + err.Error()
		return res
	}
	res.Score = score
	if s.tel != nil {
		id := mint.ID("score")
		if err := s.tel.RecordScore(ctx, ScoreEvent{
			ID: id, Org: org, Name: judge.Name, TraceID: traceID, RunName: runName,
			Dataset: it.Dataset, ItemID: it.ID, DataType: "NUMERIC", Value: score,
			Comment: truncate(reasoning, maxComment), Timestamp: time.Now().UTC(),
		}); err != nil {
			res.Error = "score: " + err.Error()
			return res
		}
	}
	return res
}

// listRuns is your past runs and how they scored — the dataset and model, the
// judge model, how many examples were attempted and how many scored, the average
// score, and when it happened.
//
// Requires a validated principal; 403 without one, and rows are filtered on the
// validated org. These records come from the metastore rather than the datastore,
// so they are readable on a deployment with no telemetry wired — but a run's
// traces and scores are not.
func (s *service) listRuns(ctx context.Context, in *runFilter) (*runs, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListRuns(ctx, org, strings.TrimSpace(in.Dataset), in.rows())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list runs: %v", err)
	}
	out := make([]runRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, runRecord{
			Dataset: r.Dataset, RunName: r.Name, Model: r.Model, JudgeModel: r.JudgeModel,
			Items: r.Items, Scored: r.Scored, AvgScore: r.AvgScore,
			CreatedAt: rfc3339(r.CreatedAt), UpdatedAt: rfc3339(r.UpdatedAt),
		})
	}
	return &runs{Data: out}, nil
}

// ── view converters ──────────────────────────────────────────────────────────

func toDatasetView(d Dataset, items int) datasetView {
	return datasetView{
		Name: d.Name, Description: d.Description, Metadata: decodeMeta(d.Metadata), Items: items,
		CreatedAt: rfc3339(d.CreatedAt), UpdatedAt: rfc3339(d.UpdatedAt),
	}
}

func toItemView(it DatasetItem) itemView {
	return itemView{
		ID: it.ID, Dataset: it.Dataset, Input: decodeAny(it.Input), Expected: decodeAny(it.Expected),
		Metadata: decodeMeta(it.Metadata), Status: it.Status,
		CreatedAt: rfc3339(it.CreatedAt), UpdatedAt: rfc3339(it.UpdatedAt),
	}
}

func toEvaluatorView(e Evaluator) evaluatorView {
	return evaluatorView{
		Name: e.Name, Model: e.Model, Criteria: e.Criteria, ScoreName: e.ScoreName,
		CreatedAt: rfc3339(e.CreatedAt), UpdatedAt: rfc3339(e.UpdatedAt),
	}
}

func toScoreConfigView(c ScoreConfig) scoreConfigView {
	return scoreConfigView{
		Name: c.Name, DataType: c.DataType, MinValue: c.MinValue, MaxValue: c.MaxValue,
		Categories: c.Categories, CreatedAt: rfc3339(c.CreatedAt), UpdatedAt: rfc3339(c.UpdatedAt),
	}
}

func toScoreView(sc ScoreEvent) scoreView {
	return scoreView{
		ID: sc.ID, Name: sc.Name, TraceID: sc.TraceID, RunName: sc.RunName, DataType: sc.DataType,
		Value: sc.Value, StringValue: sc.StringValue, Comment: sc.Comment,
		Timestamp: sc.Timestamp.UTC().Format(time.RFC3339),
	}
}

func toTraceView(tr Trace) traceView {
	v := traceView{
		ID: tr.ID, Name: tr.Name, ProjectID: tr.ProjectID, SessionID: tr.SessionID,
		Dataset: tr.Dataset, ItemID: tr.ItemID, RunName: tr.RunName,
		Model: tr.Model, Input: decodeAny(tr.Input), Output: tr.Output,
		APIKeyHash: tr.APIKeyHash,
		Timestamp:  tr.Timestamp.UTC().Format(time.RFC3339),
	}
	if !tr.StartTime.IsZero() {
		v.StartTime = tr.StartTime.UTC().Format(time.RFC3339)
	}
	if !tr.EndTime.IsZero() {
		v.EndTime = tr.EndTime.UTC().Format(time.RFC3339)
	}
	if !tr.StartTime.IsZero() && !tr.EndTime.IsZero() && !tr.EndTime.Before(tr.StartTime) {
		ms := round2ms(tr.EndTime.Sub(tr.StartTime))
		v.LatencyMs = &ms
	}
	return v
}

// round2ms renders a duration as milliseconds to 2 decimals (the same precision
// the metrics board's latency percentiles use).
func round2ms(d time.Duration) float64 {
	ms := float64(d) / float64(time.Millisecond)
	return float64(int64(ms*100+0.5)) / 100
}

// hashCredential returns a non-reversible ref for a caller credential: the SHA-256
// hex of the bearer token (the "Bearer " prefix stripped). It is NEVER the
// plaintext key — a trace correlates to a key by this ref without the store ever
// holding a secret. An empty credential yields "" (no ref), never a hash of "".
func hashCredential(authz string) string {
	tok := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(authz), "Bearer "))
	if tok == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// ── input validation + pure helpers ──────────────────────────────────────────

// requireName trims + validates a name against nameRE (the injection/traversal
// guard). Returns the clean name or a 400.
func requireName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", zip.ErrBadRequest("name is required")
	}
	if !nameRE.MatchString(name) {
		return "", zip.ErrBadRequest("name must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	return name, nil
}

// rawJSON validates that a raw item field is JSON within the size cap, returning
// its canonical string form ("" for absent). It rejects oversize blobs (MED-1
// analog) and malformed JSON at the boundary so the store only holds valid JSON.
func rawJSON(raw json.RawMessage, field string) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	if len(raw) > maxContent {
		return "", zip.ErrBadRequest(field + " too large (max 64KiB)")
	}
	if !json.Valid(raw) {
		return "", zip.ErrBadRequest(field + " must be valid JSON")
	}
	return string(raw), nil
}

// encodeMeta validates + serializes a metadata object within the cap.
func encodeMeta(m map[string]any) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", zip.ErrBadRequest("metadata must be JSON-serializable")
	}
	if len(b) > maxContent {
		return "", zip.ErrBadRequest("metadata too large (max 64KiB)")
	}
	return string(b), nil
}

func decodeMeta(s string) map[string]any {
	if s == "" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil || m == nil {
		return map[string]any{}
	}
	return m
}

// decodeAny turns a stored JSON string back into its native value (object,
// array, string, number) for response bodies and prompt building. Falls back to
// the raw string when it is not valid JSON.
func decodeAny(s string) any {
	if s == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	return v
}

func cleanCategories(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x == "" || len(x) > 256 || seen[x] {
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

// normalizeJudge fills a judge spec: the judge model defaults to the model under
// test; name/criteria have safe defaults. The judge NAME becomes a stored score
// name, so a name that fails nameRE is rejected in favor of the safe default
// (never persisted as-is).
func normalizeJudge(j *judgeSpec, model string) judgeSpec {
	out := judgeSpec{
		Model:    model,
		Name:     "llm-judge",
		Criteria: "The output should be correct, relevant, and match the expected output.",
	}
	if j != nil {
		if v := strings.TrimSpace(j.Model); v != "" {
			out.Model = v
		}
		if v := strings.TrimSpace(j.Name); v != "" && nameRE.MatchString(v) {
			out.Name = v
		}
		if v := strings.TrimSpace(j.Criteria); v != "" {
			out.Criteria = truncate(v, maxContent)
		}
	}
	return out
}

// genUUID returns a v4 UUID (trace ids follow the OTel trace-id shape).
func genUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func rfc3339(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func truncate(s string, n int) string {
	return shorten.To(s, n)
}

func getenv(key string) string { return strings.TrimSpace(os.Getenv(key)) }
