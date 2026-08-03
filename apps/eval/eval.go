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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
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

	// Static sub-routes are registered before any :name param route so a real
	// dataset name can never shadow a collection route.
	g := app.Group("/v1/evals")
	g.Post("/datasets", s.createDataset)
	g.Get("/datasets", s.listDatasets)
	g.Get("/datasets/:name", s.getDataset)
	g.Delete("/datasets/:name", s.deleteDataset)

	g.Post("/datasets/:name/items", s.createItem)
	g.Get("/datasets/:name/items", s.listItems)

	g.Post("/evaluators", s.createEvaluator)
	g.Get("/evaluators", s.listEvaluators)

	g.Post("/rubrics", s.createScoreConfig)
	g.Get("/rubrics", s.listScoreConfigs)

	g.Post("/scores", s.createScore)
	g.Get("/scores", s.listScores)

	g.Get("/traces", s.listTraces)

	// AI observability dashboard (the native Langfuse home): per-org / per-project
	// counts, cost, tokens, error & success rate, and latency percentiles over a
	// window — aggregated from the SAME cloud_usage ledger + GenAI spans.
	g.Get("/metrics", s.metricsBoard)

	g.Post("/runs", s.runHandler)
	g.Get("/runs", s.listRuns)

	log.Info("evals surface mounted (native)", "brand", deps.Brand, "telemetry", tel != nil)
	return nil
}

// The surface's prose, in the route table's own order so a path and what it means
// are read (and changed) together. None of these sixteen is a typed op — each
// binds its input by hand off the query string or the body — so there is no doc
// comment for zipdoc to lift, and without this every one of them publishes an
// operationId and NOTHING else: an SDK method that cannot explain itself and a
// CLI command with no help text. Declared through the same registry Register
// uses, so a description renders only while the router actually serves the route
// and this list can never invent a path.
func init() {
	openapi.Describe("/v1/evals/datasets", http.MethodPost,
		"Create a dataset, or edit the one with that name",
		"Writes a dataset — the named set of graded examples a run scores a model against — under "+
			"the caller's org and answers 201 with it. The NAME is the key, not an id: posting a "+
			"name the org already has updates that dataset's description and metadata and keeps its "+
			"original creation time, so this is create-or-edit and never a duplicate. Its items are "+
			"untouched.\n\n"+
			"Requires a validated principal; 403 without one. The org comes from the validated "+
			"owner claim, never from a client `X-Org-Id`, so a dataset can only ever be written "+
			"under the caller's own tenant. `name` is required and must match "+
			"`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`; a description over 64 KiB is 400.")

	openapi.Describe("/v1/evals/datasets", http.MethodGet,
		"The datasets your org has",
		"Lists the caller org's datasets as `{data:[…]}`, each with its name, description, metadata "+
			"and timestamps. `limit` defaults to 100 and is capped at 500; an unparseable or "+
			"non-positive value falls back to the default rather than failing.\n\n"+
			"Requires a validated principal; 403 without one. Every row is filtered on the "+
			"validated org, so there is no parameter that reaches another tenant's datasets. The "+
			"`items` count is NOT populated here — read one dataset to get it.")

	openapi.Describe("/v1/evals/datasets/:name", http.MethodGet,
		"One dataset, with how many examples it holds",
		"Returns a single dataset of the caller's org by name, together with its live item count — "+
			"the one read that answers how big the set actually is. A name this org does not have "+
			"is 404, which is also what another tenant's dataset looks like from here. Requires a "+
			"validated principal; 403 without one.")

	openapi.Describe("/v1/evals/datasets/:name", http.MethodDelete,
		"Delete a dataset and every example in it",
		"Removes the named dataset of the caller's org AND all of its items, in one transaction, "+
			"and answers 204. This is not a detach: the examples are gone with the set, so a "+
			"dataset cannot be resurrected by re-creating the name.\n\n"+
			"A name this org does not have is 404 — never a silent success — and a name belonging "+
			"to another tenant is the same 404, because the delete is predicated on the validated "+
			"org. Requires a validated principal; 403 without one. Runs and scores already recorded "+
			"against the dataset are telemetry events and are NOT deleted with it.")

	openapi.Describe("/v1/evals/datasets/:name/items", http.MethodPost,
		"Add a graded example to one of your datasets",
		"Writes one example — its `input`, its `expectedOutput`, free-form metadata and a status — "+
			"into the dataset named in the path, and answers 201 with it. That dataset MUST "+
			"already exist for this org: an unknown one is 404, never a silent create, so an "+
			"example can never be attached to a set the caller does not own."+
			"\n\n"+
			"Supply `id` to make the write idempotent — re-posting the same id replaces that "+
			"example in place — or omit it and one is generated. An id that already exists in a "+
			"DIFFERENT dataset is 409 rather than a move. `status` is `ACTIVE` (the default) or "+
			"`ARCHIVED`; only ACTIVE examples are fed to a run, which is how an example is retired "+
			"without deleting it. `input` and `expectedOutput` are stored as raw JSON exactly as "+
			"sent. Requires a validated principal; 403 without one.")

	openapi.Describe("/v1/evals/datasets/:name/items", http.MethodGet,
		"The examples in one of your datasets",
		"Lists the examples of ONE dataset as `{data:[…]}` — the set is named in the path, "+
			"because this collection only exists inside one. Archived examples are included, so "+
			"the caller sees the whole set rather than only "+
			"what a run would use. `limit` defaults to 100 and is capped at 500.\n\n"+
			"Requires a validated principal; 403 without one, and the read is filtered on the "+
			"validated org, so naming another tenant's dataset returns nothing rather than its "+
			"contents.")

	openapi.Describe("/v1/evals/evaluators", http.MethodPost,
		"Define a judge: a model plus the criteria it grades by",
		"Saves a reusable evaluator for the caller's org — the judge model and the written criteria "+
			"it grades against — and answers 201 with it. Like a dataset, the NAME is the key: "+
			"re-posting a name edits that evaluator rather than adding a second one.\n\n"+
			"`scoreName` is the name the resulting scores are filed under and defaults to the "+
			"evaluator's own name; both must match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`. Criteria "+
			"over 64 KiB is 400. Requires a validated principal; 403 without one.")

	openapi.Describe("/v1/evals/evaluators", http.MethodGet,
		"The judges your org has defined",
		"Lists the caller org's evaluators as `{data:[…]}`, each with its judge model, criteria and "+
			"the score name it writes under. `limit` defaults to 100 and is capped at 500. Requires "+
			"a validated principal; 403 without one, and the listing is filtered on the validated "+
			"org.")

	openapi.Describe("/v1/evals/rubrics", http.MethodPost,
		"Declare what a score named X is allowed to be",
		"Defines the shape of one score name for the caller's org — `NUMERIC` (the default, "+
			"optionally bounded by `minValue`/`maxValue`), `CATEGORICAL` (a closed set of "+
			"`categories`) or `BOOLEAN` — and answers 201 with it. The NAME is the key, so "+
			"re-posting a name replaces its rules.\n\n"+
			"This is the integrity contract, not documentation: once a config exists for a name, "+
			"every score recorded under that name is checked against it and the config's data type "+
			"is AUTHORITATIVE — a caller cannot claim a different one. Out-of-range values, "+
			"unlisted labels and non-finite numbers are refused at write time.\n\n"+
			"A `CATEGORICAL` config with no categories is 400, as is a non-finite bound or a "+
			"`minValue` above `maxValue`. Requires a validated principal; 403 without one.")

	openapi.Describe("/v1/evals/rubrics", http.MethodGet,
		"The score shapes your org has declared",
		"Lists the caller org's rubrics as `{data:[…]}` — each name's data type, its numeric "+
			"bounds and its allowed categories. `limit` defaults to 100 and is capped at 500. "+
			"Requires a validated principal; 403 without one, and the listing is filtered on the "+
			"validated org.")

	openapi.Describe("/v1/evals/scores", http.MethodPost,
		"Record a score against a trace, a run or an example",
		"Files one score event for the caller's org and answers 201 with it. This is how human "+
			"review and out-of-band graders land beside the automatic ones: name the score, give it "+
			"a `value` (or a `stringValue` for a categorical label), and attach it to a `traceId`, "+
			"a `runName`, a `datasetName`/`datasetItemId`, or any combination.\n\n"+
			"Scores are validated fail-closed. A value must be FINITE — NaN and Inf are 400 — and "+
			"if the org has declared a score config for this name, that config decides the type and "+
			"the value must satisfy it: inside the numeric bounds, or one of the allowed "+
			"categories. A caller cannot override the declared type by sending a different "+
			"`dataType`. Comments are truncated at 2000 characters.\n\n"+
			"A score is TELEMETRY, not metadata, so it needs the datastore: a deployment with no "+
			"datastore wired answers 503 rather than accepting a score it cannot persist. Requires "+
			"a validated principal; 403 without one, and the org is stamped from the validated "+
			"claim rather than read off the body.")

	openapi.Describe("/v1/evals/scores", http.MethodGet,
		"Score events, filtered",
		"Lists the caller org's score events as `{data:[…]}`, narrowed by any of `name`, `runName` "+
			"and `traceId`; an absent filter simply does not narrow. `limit` defaults to 100 and is "+
			"capped at 500.\n\n"+
			"The org is bound as an authoritative predicate on the query, never taken from a "+
			"header, so a filter can narrow the caller's own scores but can never widen past them. "+
			"Requires a validated principal; 403 without one. Scores live in the datastore, so a "+
			"deployment with none wired answers 503 rather than an empty page that would read as "+
			"'no scores'.")

	openapi.Describe("/v1/evals/traces", http.MethodGet,
		"The traces behind your evaluations",
		"Lists the caller org's traces as `{data:[…]}` — one per model call an evaluation made, "+
			"carrying its input, output, model and timing — narrowed by any of `sessionId`, "+
			"`runName` and `datasetName`. `limit` defaults to 100 and is capped at 500.\n\n"+
			"Scoped by org AND by project: the project is the caller's server-minted scope, not a "+
			"parameter, so it cannot be widened by asking. Requires a validated principal; 403 "+
			"without one. Traces live in the datastore, so a deployment with none wired answers 503 "+
			"rather than an empty page.")

	openapi.Describe("/v1/evals/metrics", http.MethodGet,
		"Your org's AI overview board",
		"Returns the whole observability board for the caller's org over a window: totals "+
			"(generations, prompt and completion tokens, cost in cents, errors, success rate, "+
			"distinct models and users), a gap-filled time series, a per-model breakdown with the "+
			"long tail folded into `other`, and latency percentiles read from the GenAI spans.\n\n"+
			"`range` is `24h` (the default), `7d` or `30d`, and anything else normalises to `24h` "+
			"rather than failing; `interval` overrides the bucket with `hour` or `day`. The window "+
			"the answer was actually computed over is echoed back, so a client never has to infer "+
			"it. A platform admin sees the board across ALL orgs; everyone else sees their own.\n\n"+
			"The board is HONEST-EMPTY where it cannot be computed: with no datastore wired, or "+
			"under a named project scope the usage ledger does not yet carry, it answers a valid "+
			"board with zero totals and a flat series rather than a fabricated number or a 500. "+
			"Requires a validated principal; 403 without one.")

	openapi.Describe("/v1/evals/runs", http.MethodPost,
		"Score a dataset through a model and a judge, now",
		"Runs a real evaluation and answers the summary when it is finished — this is synchronous "+
			"work, not a job id. For each ACTIVE example in the dataset it calls the model under "+
			"test, records a trace, calls the LLM-as-judge, and records the judge's score with its "+
			"reasoning. The answer carries the per-item results (item id, trace id, score, output "+
			"or error) alongside `items`, `scored` and `avgScore`.\n\n"+
			"`dataset` and `model` are required; the dataset must belong to the caller's org (404 "+
			"otherwise) and must have at least one ACTIVE example (422 otherwise). `judge` is "+
			"optional — omitted, the model under test grades itself against a default correctness "+
			"criterion under the score name `llm-judge`. `limit` defaults to 20 and anything above "+
			"100 falls back to the default. `runName` is generated from the clock when omitted.\n\n"+
			"It runs as YOU: the caller's own `Authorization` bearer drives the model gateway, so a "+
			"request without one is 401 rather than a run made anonymously or under a service "+
			"identity. Only a non-reversible hash of that credential is recorded on the traces.\n\n"+
			"Bounded and honest about it: an org may have at most 4 runs in flight and the fifth is "+
			"429 rather than queued, and the whole run is capped at 10 minutes — items past the "+
			"deadline come back with an error instead of a score, and `scored` counts only real "+
			"successes. A run where NOTHING scored answers 502, not a 200 that looks like an "+
			"evaluation. A run must be able to persist what it produces, so a deployment with no "+
			"datastore wired is 503 up front. Requires a validated principal; 403 without one.")

	openapi.Describe("/v1/evals/runs", http.MethodGet,
		"Past runs and how they scored",
		"Lists the caller org's durable run records as `{data:[…]}` — the dataset and model, the "+
			"judge model, how many examples were attempted and how many scored, the average score, "+
			"and when it happened. Narrow to one dataset with `datasetName`; `limit` defaults to "+
			"100 and is capped at 500.\n\n"+
			"Requires a validated principal; 403 without one, and rows are filtered on the "+
			"validated org. These records come from the metastore rather than the datastore, so "+
			"they are readable on a deployment with no telemetry wired — but a run's traces and "+
			"scores are not.")
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

// tenant resolves the org — the tenant isolation KEY — for a request, but ONLY
// for a VALIDATED principal. Two gates, both mandatory:
//
//  1. A validated principal MUST be present: c.User() (X-User-Id) is non-empty.
//     SanitizeIdentity sets X-User-Id ONLY from a token/session it verified, and
//     strips any client copy on ingress — so c.User() is the one unforgeable
//     "this request carried a validated identity" signal. Its Phase-1 residual
//     RESTORES a client-supplied X-Org-Id on the NO-principal path (bearer-less,
//     opaque pk-/sk- API key, or invalid bearer). Without this gate, a
//     direct-to-pod / in-cluster caller could send `X-Org-Id: victim` with no
//     bearer and read/write/DELETE the victim org's datasets (golden outputs +
//     PII), scores and runs — a cross-tenant break (Red HIGH). This is the SAME
//     trust signal the audit layer uses (audit_middleware.go actorFromCtx): no
//     validated sub ⇒ untrusted org, treated as anonymous.
//  2. The org (c.Org()) MUST be present and sane. It is used verbatim — never
//     lowercased/trimmed/truncated — because normalizing collapses DISTINCT
//     owners into one storage bucket (Red HIGH-1). X-Org-Id is minted by
//     SanitizeIdentity from the validated owner claim; a client X-Org-Id/
//     X-Project-Id is stripped, so it is never a cross-tenant selector.
//
// Fails closed: an unvalidated or org-less request gets no tenant, so the caller
// returns 403 — never a fake success, never another org's data.
func tenant(c *zip.Ctx) (string, bool) { return principal.Org(c) }

// ── HTTP shapes (the contract the FE port consumes) ──────────────────────────

type datasetView struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Metadata    map[string]any `json:"metadata"`
	Items       int            `json:"items,omitempty"`
	CreatedAt   string         `json:"createdAt"`
	UpdatedAt   string         `json:"updatedAt"`
}

type itemView struct {
	ID        string         `json:"id"`
	Dataset   string         `json:"datasetName"`
	Input     any            `json:"input"`
	Expected  any            `json:"expectedOutput"`
	Metadata  map[string]any `json:"metadata"`
	Status    string         `json:"status"`
	CreatedAt string         `json:"createdAt"`
	UpdatedAt string         `json:"updatedAt"`
}

type evaluatorView struct {
	Name      string `json:"name"`
	Model     string `json:"model"`
	Criteria  string `json:"criteria"`
	ScoreName string `json:"scoreName"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type scoreConfigView struct {
	Name       string   `json:"name"`
	DataType   string   `json:"dataType"`
	MinValue   *float64 `json:"minValue,omitempty"`
	MaxValue   *float64 `json:"maxValue,omitempty"`
	Categories []string `json:"categories,omitempty"`
	CreatedAt  string   `json:"createdAt"`
	UpdatedAt  string   `json:"updatedAt"`
}

type scoreView struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	TraceID     string  `json:"traceId,omitempty"`
	RunName     string  `json:"runName,omitempty"`
	DataType    string  `json:"dataType"`
	Value       float64 `json:"value"`
	StringValue string  `json:"stringValue,omitempty"`
	Comment     string  `json:"comment,omitempty"`
	Timestamp   string  `json:"timestamp"`
}

type traceView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ProjectID string `json:"projectId,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Dataset   string `json:"datasetName,omitempty"`
	ItemID    string `json:"datasetItemId,omitempty"`
	RunName   string `json:"runName,omitempty"`
	Model     string `json:"model,omitempty"`
	Input     any    `json:"input,omitempty"`
	Output    string `json:"output,omitempty"`
	// LatencyMs is EndTime-StartTime in milliseconds, nil when the trace carries
	// no timing (so the console renders "—", never a fabricated 0).
	LatencyMs *float64 `json:"latencyMs,omitempty"`
	StartTime string   `json:"startTime,omitempty"`
	EndTime   string   `json:"endTime,omitempty"`
	Timestamp string   `json:"timestamp"`
	// APIKeyHash is the non-reversible credential ref (never a plaintext key).
	APIKeyHash string `json:"apiKeyHash,omitempty"`
}

// ── datasets ─────────────────────────────────────────────────────────────────

type datasetReq struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Metadata    map[string]any `json:"metadata"`
}

func (s *service) createDataset(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body datasetReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	name, err := requireName(body.Name)
	if err != nil {
		return err
	}
	meta, err := encodeMeta(body.Metadata)
	if err != nil {
		return err
	}
	if len(body.Description) > maxContent {
		return zip.ErrBadRequest("description too large")
	}
	id, err := genID("ds")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	d, err := s.store.UpsertDataset(c.Context(), Dataset{
		ID: id, Org: org, Name: name, Description: body.Description, Metadata: meta, UpdatedAt: now,
	})
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	return c.JSON(http.StatusCreated, toDatasetView(d, 0))
}

func (s *service) listDatasets(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := s.store.ListDatasets(c.Context(), org, listLimit(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]datasetView, 0, len(rows))
	for _, d := range rows {
		out = append(out, toDatasetView(d, 0))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

func (s *service) getDataset(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name := strings.TrimSpace(c.Param("name"))
	d, err := s.store.GetDataset(c.Context(), org, name)
	if err == errNotFound {
		return zip.ErrNotFound("dataset not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	n, err := s.store.CountItems(c.Context(), org, name)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "items: %v", err)
	}
	return c.JSON(http.StatusOK, toDatasetView(d, n))
}

func (s *service) deleteDataset(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name := strings.TrimSpace(c.Param("name"))
	deleted, err := s.store.DeleteDataset(c.Context(), org, name)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("dataset not found")
	}
	return c.NoContent(http.StatusNoContent)
}

// ── dataset items ────────────────────────────────────────────────────────────

type itemReq struct {
	ID       string          `json:"id"`
	Input    json.RawMessage `json:"input"`
	Expected json.RawMessage `json:"expectedOutput"`
	Metadata map[string]any  `json:"metadata"`
	Status   string          `json:"status"`
}

func (s *service) createItem(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body itemReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	dataset := strings.TrimSpace(c.Param("name"))
	// The dataset MUST exist for THIS org — an item can never be attached to a
	// dataset the caller doesn't own (a real 404, not a silent create).
	if _, err := s.store.GetDataset(c.Context(), org, dataset); err == errNotFound {
		return zip.ErrNotFound("dataset not found")
	} else if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "dataset: %v", err)
	}
	input, err := rawJSON(body.Input, "input")
	if err != nil {
		return err
	}
	expected, err := rawJSON(body.Expected, "expectedOutput")
	if err != nil {
		return err
	}
	meta, err := encodeMeta(body.Metadata)
	if err != nil {
		return err
	}
	status := strings.ToUpper(strings.TrimSpace(body.Status))
	if status == "" {
		status = "ACTIVE"
	}
	if status != "ACTIVE" && status != "ARCHIVED" {
		return zip.ErrBadRequest("status must be ACTIVE or ARCHIVED")
	}
	id := strings.TrimSpace(body.ID)
	if id == "" {
		if id, err = genID("item"); err != nil {
			return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
		}
	} else if !nameRE.MatchString(id) {
		return zip.ErrBadRequest("id must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	now := time.Now().Unix()
	it, err := s.store.PutItem(c.Context(), DatasetItem{
		ID: id, Org: org, Dataset: dataset, Input: input, Expected: expected,
		Metadata: meta, Status: status, UpdatedAt: now,
	})
	if err == errConflict {
		return zip.ErrConflict("item id already exists in a different dataset")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	return c.JSON(http.StatusCreated, toItemView(it))
}

func (s *service) listItems(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	dataset := strings.TrimSpace(c.Param("name"))
	items, err := s.store.ListItems(c.Context(), org, dataset, false, listLimit(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]itemView, 0, len(items))
	for _, it := range items {
		out = append(out, toItemView(it))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

// ── evaluators ───────────────────────────────────────────────────────────────

type evaluatorReq struct {
	Name      string `json:"name"`
	Model     string `json:"model"`
	Criteria  string `json:"criteria"`
	ScoreName string `json:"scoreName"`
}

func (s *service) createEvaluator(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body evaluatorReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	name, err := requireName(body.Name)
	if err != nil {
		return err
	}
	if len(body.Criteria) > maxContent {
		return zip.ErrBadRequest("criteria too large")
	}
	scoreName := strings.TrimSpace(body.ScoreName)
	if scoreName == "" {
		scoreName = name
	} else if !nameRE.MatchString(scoreName) {
		return zip.ErrBadRequest("scoreName must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	id, err := genID("eval")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	e, err := s.store.UpsertEvaluator(c.Context(), Evaluator{
		ID: id, Org: org, Name: name, Model: strings.TrimSpace(body.Model),
		Criteria: body.Criteria, ScoreName: scoreName, UpdatedAt: now,
	})
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	return c.JSON(http.StatusCreated, toEvaluatorView(e))
}

func (s *service) listEvaluators(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := s.store.ListEvaluators(c.Context(), org, listLimit(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]evaluatorView, 0, len(rows))
	for _, e := range rows {
		out = append(out, toEvaluatorView(e))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

// ── score configs ────────────────────────────────────────────────────────────

type scoreConfigReq struct {
	Name       string   `json:"name"`
	DataType   string   `json:"dataType"`
	MinValue   *float64 `json:"minValue"`
	MaxValue   *float64 `json:"maxValue"`
	Categories []string `json:"categories"`
}

func (s *service) createScoreConfig(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body scoreConfigReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	name, err := requireName(body.Name)
	if err != nil {
		return err
	}
	dt := strings.ToUpper(strings.TrimSpace(body.DataType))
	if dt == "" {
		dt = "NUMERIC"
	}
	if !validDataTypes[dt] {
		return zip.ErrBadRequest("dataType must be NUMERIC, CATEGORICAL, or BOOLEAN")
	}
	if body.MinValue != nil && !finite(*body.MinValue) {
		return zip.ErrBadRequest("minValue must be finite")
	}
	if body.MaxValue != nil && !finite(*body.MaxValue) {
		return zip.ErrBadRequest("maxValue must be finite")
	}
	if body.MinValue != nil && body.MaxValue != nil && *body.MinValue > *body.MaxValue {
		return zip.ErrBadRequest("minValue must not exceed maxValue")
	}
	cats := cleanCategories(body.Categories)
	if dt == "CATEGORICAL" && len(cats) == 0 {
		return zip.ErrBadRequest("CATEGORICAL config requires at least one category")
	}
	id, err := genID("sc")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	cfg, err := s.store.UpsertScoreConfig(c.Context(), ScoreConfig{
		ID: id, Org: org, Name: name, DataType: dt,
		MinValue: body.MinValue, MaxValue: body.MaxValue, Categories: cats, UpdatedAt: now,
	})
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	return c.JSON(http.StatusCreated, toScoreConfigView(cfg))
}

func (s *service) listScoreConfigs(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := s.store.ListScoreConfigs(c.Context(), org, listLimit(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]scoreConfigView, 0, len(rows))
	for _, cfg := range rows {
		out = append(out, toScoreConfigView(cfg))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

// ── scores (telemetry events) ────────────────────────────────────────────────

type scoreReq struct {
	Name        string   `json:"name"`
	TraceID     string   `json:"traceId"`
	RunName     string   `json:"runName"`
	Dataset     string   `json:"datasetName"`
	ItemID      string   `json:"datasetItemId"`
	DataType    string   `json:"dataType"`
	Value       *float64 `json:"value"`
	StringValue string   `json:"stringValue"`
	Comment     string   `json:"comment"`
}

func (s *service) createScore(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	if s.tel == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "evals telemetry (datastore) not configured")
	}
	var body scoreReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	name, err := requireName(body.Name)
	if err != nil {
		return err
	}
	ev, err := s.validateScore(c.Context(), org, name, body)
	if err != nil {
		return err
	}
	if err := s.tel.RecordScore(c.Context(), ev); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "record score: %v", err)
	}
	return c.JSON(http.StatusCreated, toScoreView(ev))
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
	id, err := genID("score")
	if err != nil {
		return ScoreEvent{}, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
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
		if hasCfg && !containsStr(cfg.Categories, label) {
			return ScoreEvent{}, zip.ErrBadRequest("stringValue not in the configured category set")
		}
		ev.StringValue = label
	}
	return ev, nil
}

func (s *service) listScores(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	if s.tel == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "evals telemetry (datastore) not configured")
	}
	scores, err := s.tel.ListScores(c.Context(), ScoreFilter{
		Org:     org, // authoritative — never a client header
		Name:    strings.TrimSpace(c.Query("name")),
		RunName: strings.TrimSpace(c.Query("runName")),
		TraceID: strings.TrimSpace(c.Query("traceId")),
		Limit:   listLimit(c),
	})
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list scores: %v", err)
	}
	out := make([]scoreView, 0, len(scores))
	for _, sc := range scores {
		out = append(out, toScoreView(sc))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

func (s *service) listTraces(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	if s.tel == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "evals telemetry (datastore) not configured")
	}
	traces, err := s.tel.ListTraces(c.Context(), TraceFilter{
		Org:       org,                       // authoritative
		ProjectID: principal.ProjectScope(c), // server-minted; "" for the default project (whole org)
		SessionID: strings.TrimSpace(c.Query("sessionId")),
		RunName:   strings.TrimSpace(c.Query("runName")),
		Dataset:   strings.TrimSpace(c.Query("datasetName")),
		Limit:     listLimit(c),
	})
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list traces: %v", err)
	}
	out := make([]traceView, 0, len(traces))
	for _, tr := range traces {
		out = append(out, toTraceView(tr))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

// ── run orchestration ────────────────────────────────────────────────────────

type judgeSpec struct {
	Model    string `json:"model"`
	Criteria string `json:"criteria"`
	Name     string `json:"name"`
}

type runRequest struct {
	Dataset string     `json:"dataset"`
	Model   string     `json:"model"`
	RunName string     `json:"runName"`
	Limit   int        `json:"limit"`
	Judge   *judgeSpec `json:"judge"`
}

type itemResult struct {
	ItemID  string  `json:"itemId"`
	TraceID string  `json:"traceId,omitempty"`
	Score   float64 `json:"score"`
	Output  string  `json:"output,omitempty"`
	Error   string  `json:"error,omitempty"`
}

type runSummary struct {
	Dataset    string       `json:"dataset"`
	Model      string       `json:"model"`
	JudgeModel string       `json:"judgeModel"`
	RunName    string       `json:"runName"`
	Items      int          `json:"items"`
	Scored     int          `json:"scored"`
	AvgScore   float64      `json:"avgScore"`
	Results    []itemResult `json:"results"`
}

func (s *service) runHandler(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	// A run has no durable record without telemetry — a real eval PERSISTS its
	// traces + scores. Rather than run models and return scores we can't store
	// (a fake success), fail closed when the datastore is not wired, exactly as
	// createScore/listScores do.
	if s.tel == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "evals/runs: telemetry (datastore) not configured; a run cannot persist traces/scores")
	}
	// The model gateway needs the caller's own credential — fail closed rather
	// than run models anonymously or with a service identity.
	authz := c.Header("Authorization")
	if authz == "" {
		return zip.ErrUnauthorized("evals/runs: missing Authorization bearer; the model gateway needs the caller's key/JWT")
	}
	var rr runRequest
	if err := c.Bind(&rr); err != nil {
		return err
	}
	if strings.TrimSpace(rr.Dataset) == "" || strings.TrimSpace(rr.Model) == "" {
		return zip.ErrBadRequest("evals/runs: 'dataset' and 'model' are required")
	}
	// The dataset must belong to THIS org (a real 404, never a cross-tenant read).
	if _, err := s.store.GetDataset(c.Context(), org, rr.Dataset); err == errNotFound {
		return zip.ErrNotFound("dataset not found")
	} else if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "dataset: %v", err)
	}

	limit := rr.Limit
	if limit <= 0 || limit > maxRunItems {
		limit = defaultRunItems
	}
	runName := strings.TrimSpace(rr.RunName)
	if runName == "" {
		runName = "run-" + time.Now().UTC().Format("20060102T150405Z")
	} else if !nameRE.MatchString(runName) {
		return zip.ErrBadRequest("runName must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	judge := normalizeJudge(rr.Judge, rr.Model)

	items, err := s.store.ListItems(c.Context(), org, rr.Dataset, true, limit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "items: %v", err)
	}
	if len(items) == 0 {
		return zip.Errorf(http.StatusUnprocessableEntity, "evals/runs: dataset %q has no active items", rr.Dataset)
	}

	// Bound the run (Red MED): fail fast if this org is already at its concurrent-
	// run cap (429, never queued), and cap the whole run's wall-clock so a runaway
	// can't pin the request + a shared-gateway slot. The slot is released and the
	// context cancelled on every return path.
	if !acquireRunSlot(org) {
		return zip.Errorf(http.StatusTooManyRequests,
			"evals/runs: too many concurrent runs for this org (max %d); retry when one finishes", maxConcurrentRunsPerOrg)
	}
	defer releaseRunSlot(org)
	runCtx, cancel := context.WithTimeout(c.Context(), maxRunDuration)
	defer cancel()

	// Attribution threaded into every item trace: the caller's server-minted
	// project narrows within the validated org; the credential is stored only as a
	// non-reversible ref, never in plaintext.
	attr := runAttribution{projectID: principal.ProjectScope(c), apiKeyHash: hashCredential(authz)}

	summary := runSummary{Dataset: rr.Dataset, Model: rr.Model, JudgeModel: judge.Model, RunName: runName, Items: len(items)}
	var sum float64
	for _, it := range items {
		// Stop early once the deadline is hit; remaining items are unrun, and the
		// partial summary is returned honestly (Scored counts only real successes).
		if runCtx.Err() != nil {
			summary.Results = append(summary.Results, itemResult{ItemID: it.ID, Error: "run: " + runCtx.Err().Error()})
			continue
		}
		res := s.runItem(runCtx, org, authz, runName, rr.Model, judge, attr, it)
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
	if id, gerr := genID("run"); gerr == nil {
		if _, uerr := s.store.UpsertRun(c.Context(), DatasetRun{
			ID: id, Org: org, Dataset: rr.Dataset, Name: runName, Model: rr.Model,
			JudgeModel: judge.Model, Items: summary.Items, Scored: summary.Scored,
			AvgScore: summary.AvgScore, UpdatedAt: time.Now().Unix(),
		}); uerr != nil {
			s.log.Warn("run record not persisted", "run", runName, "err", uerr)
		}
	}

	// Nothing scored is a real failure, not a fake 200.
	status := http.StatusOK
	if summary.Scored == 0 {
		status = http.StatusBadGateway
	}
	return c.JSON(status, summary)
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
		id, _ := genID("score")
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

func (s *service) listRuns(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	runs, err := s.store.ListRuns(c.Context(), org, strings.TrimSpace(c.Query("datasetName")), listLimit(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list runs: %v", err)
	}
	out := make([]map[string]any, 0, len(runs))
	for _, r := range runs {
		out = append(out, map[string]any{
			"dataset":    r.Dataset,
			"runName":    r.Name,
			"model":      r.Model,
			"judgeModel": r.JudgeModel,
			"items":      r.Items,
			"scored":     r.Scored,
			"avgScore":   r.AvgScore,
			"createdAt":  rfc3339(r.CreatedAt),
			"updatedAt":  rfc3339(r.UpdatedAt),
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
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

func containsStr(xs []string, target string) bool {
	for _, x := range xs {
		if x == target {
			return true
		}
	}
	return false
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

// listLimit reads a bounded ?limit from the request (metastore lists).
func listLimit(c *zip.Ctx) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	if err != nil || n <= 0 {
		return defaultListLimit
	}
	if n > maxListLimit {
		return maxListLimit
	}
	return n
}

// genID returns a prefixed, collision-resistant id (prefix + 128 random bits).
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
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
	if len(s) > n {
		return s[:n]
	}
	return s
}

func getenv(key string) string { return strings.TrimSpace(os.Getenv(key)) }
