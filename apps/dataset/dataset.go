// Package dataset is the per-org dataset plane of /v1/risk: a dataset is a
// VERSIONED, IMMUTABLE snapshot of one tenant's own event surface, and this is
// where it is declared, materialised, described, exported and disposed of.
//
// THE ADDRESS IS THE PRODUCT, and that is why it is /v1/risk and not /v1/ml.
// openapi.Product reads an operation's product off the FIRST /v1 segment of its
// path and nothing else, so an address is a published product membership: the
// fleet's tag list, the floor ratchet, the doc headings, every generated SDK's
// namespace and the CLI's command tree are projections of that one segment. The
// rows here feed the RISK model, which learns in-process from the org's own
// events; they are not served by KServe. /v1/ml is the model-SERVING plane
// (apps/ml: InferenceServices, /v1/ml/models, /v1/ml/models/{name}/predict) — a
// different live product with its own consumers — so publishing seven dataset
// operations there filed them into it, and nothing in the fleet said so: the
// floor ratchet read `ml: 7 -> 14` as growth, because it refuses a shrink and
// only a shrink. apps/label and apps/reference each corrected the same address
// once; address_test.go makes it a gate here rather than a third recollection.
//
// WHY A DATASET IS A VALUE AND NOT A QUERY. Storing a spec and re-running it is
// the design that guarantees irreproducibility. The source is a SummingMergeTree
// whose parts merge, its retention drops the tail, and the rollup behind it can
// be re-run — so the same query asked twice is two different answers, and a model
// that cites "the query" has cited nothing. A dataset here is bytes: declared as
// a version, materialised once, fingerprinted, and never rewritten. A model can
// name the exact rows it was fitted on, forever, which is the only form in which
// an audit can be answered.
//
// THE FOUR PROPERTIES, AND WHERE EACH IS ENFORCED.
//
//	tenancy       plane.go — the tenant is the leading BOUND predicate of every
//	              statement, the first component of both tables' sort keys AND of
//	              their partition expressions, and it arrives only as a
//	              [tenant.Key], which cannot be written as a literal here and
//	              cannot be decoded from a request body.
//	immutability  A published version is `ready`, and the only rank above it is
//	              `disposed` — the tenant's own retention decision, the one write
//	              that may outrank a publication. No other stage can displace it,
//	              in the engine or at the door. Two layers.
//	bounds        spec.go — the window, the horizon, the row cap, the number of
//	              names and the number of versions are all bounded at the door, and
//	              every scan of the source is admitted through ONE gate: priced at
//	              the meter, one per tenant, [maxJobs] in the process, each with a
//	              deadline of its own ([plane.admit]).
//	expiry        There is NO table TTL. Disposal is the tenant's own DROP
//	              PARTITION on (org, dataset), which cannot be spelled cross-tenant.
//
// WHY IT IS ITS OWN APP. It shares no state with a scorer: there is no in-memory
// model, no ring, no single-writer file. Every ANSWER it gives is a function of
// the store, so it restarts empty and a restart loses nothing but the jobs in
// flight — which is exactly what a plane holding the record of what a model
// trained on must do, and exactly what a process pinned to one replica for its
// in-memory forests cannot promise. Its surface is five leaves under
// /v1/risk/datasets that no other app claims; zip refuses two owners for one
// prefix at compose time, so that is checked rather than agreed.
//
// WHAT IS PER PROCESS, SAID PLAINLY. Every read, every declaration and every
// disposal is a pure function of the store and answers identically from any
// process. ADMISSION is not: the one-scan-per-tenant gate and the [maxJobs]
// ceiling are this process's own map, so N replicas are N ceilings, and two
// processes can admit one version's materialisation between them — both would
// then write rows under one number and the register would keep whichever `ready`
// landed last. This plane is therefore deployed as a SINGLE WRITER. That is a
// deployment fact stated here rather than a property claimed and not held: a
// durable lease is the only thing that would make it a property, and inventing
// one for a plane that runs at one replica would be machinery nobody's
// requirements asked for.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc
package dataset

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/tenant"
	"github.com/zap-proto/zip"
)

// errNoTenant is the refusal every unkeyed call gets. It is an error rather than
// a panic because it is reachable — a CLI LocalInvoke has no principal — and a
// refusal is the right answer there too.
var errNoTenant = zip.ErrForbidden("no tenant, so there is no dataset plane to reach")

// plane is everything this app is. It holds no dataset state: the register and
// the rows are in the store, so every answer is a function of the store and a
// restart loses nothing but the jobs in flight.
type plane struct {
	// store is the warehouse. An interface so a test drives the plane, and so
	// nothing here can reach the connection for anything else.
	store store
	// brand is the deployment's brand — half the tenant key, and never a header.
	brand string
	// bill gates and meters every priced act.
	bill meter
	// log is the scoped subsystem logger.
	log interface {
		Info(string, ...any)
		Warn(string, ...any)
		Error(string, ...any)
	}

	// mu guards the two facts that are this process's rather than the store's.
	mu sync.Mutex
	// busy is the in-flight source scan per tenant — the R5 gate. ONE scan per
	// tenant, so a tenant looping any op that reaches the source queues nothing
	// and spends nothing: the second call is refused, not admitted and then
	// starved.
	busy map[string]scan
	// schema records whether the owned tables have been created. Only success
	// latches, so a warehouse that was still connecting at boot is retried.
	schema bool
}

// meter is the plane's half of the fleet's billing seam: GATE before a priced act
// and DEBIT after it. *cloud.ResourceMeter is the production value.
//
// It is an interface for one reason a concrete meter cannot give: a gate can fail
// with a FOREIGN error — "commerce unreachable: <transport detail>" — and what a
// caller is shown when that happens is a property this package must be able to
// test. Reaching that state with a real meter needs a commerce peer to be down,
// which is not a state a unit test can stand up.
type meter interface {
	Gate(ctx context.Context, org, project string, projectValidated bool, kind string, costCents int64) error
	Meter(org, project, kind string, amountCents int64, requestID, clientIP string)
	Enabled() bool
}

// scan is PROOF that a read of the source surface was ADMITTED: priced at the
// meter, bounded to one per tenant and to [maxJobs] in this process.
//
// It has no exported field and no constructor outside [plane.admit], so a
// warehouse scan nobody paid for and nobody counted is not a thing this package
// can spell. [plane.census] and [plane.facts] take one, and they are the only
// functions that read [sourceTable] — which is what makes "every source scan is
// gated, metered and bounded" a property of the type rather than a rule each new
// op has to remember. A free lineage read is how that rule was broken the first
// time.
type scan struct {
	k       tenant.Key
	name    string
	version int
	since   time.Time
}

// Mount registers the dataset leaves of /v1/risk onto app.
//
// EVERY INHERITED CAPABILITY IS NAMED HERE, EXPLICITLY. Being embedded in cloud
// makes each one AVAILABLE; none is automatic:
//
//	IAM auth     SanitizeIdentity mints X-Org-Id from the verified bearer, in
//	             serve.go. This app never validates a token and never can.
//	tenant gate  cloud.Bridge parks the validated org on the context a typed op
//	             receives; it is the composer's install — once at the root of
//	             every program — so this package does not install it.
//	meter+gate   cloud.NewResourceMeter(deps, "dataset"); Gate before the one
//	             priced op and Meter after it.
//	logs         cloud.NewBase(deps, "dataset") gives the scoped logger.
//	traces       global and already ZAP-native (OTLZ). This package imports no
//	             otlp transport, deliberately.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("dataset.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("dataset.Mount: nil deps.Logger")
	}
	if err := tenant.Vouches(deps.Brand); err != nil {
		// The brand is half the tenant key, and the half that must be a brand the
		// registry carries — the rollup that WRITES this plane's source refuses any
		// other, so a key minted under one would read a tenant nobody can write.
		// A deployment that cannot mint a key would 403 every request; better said
		// once at boot than once per caller.
		return fmt.Errorf("dataset.Mount: %q cannot mint a tenant key: %w", deps.Brand, err)
	}
	base := cloud.NewBase(deps, "dataset")
	p := &plane{
		store: warehouse{},
		brand: deps.Brand,
		bill:  base.Bill,
		log:   base.Log,
		busy:  map[string]scan{},
	}

	// The tables are created once, in the background, off every request path. A
	// warehouse that is down at boot must not stop the app from mounting: the
	// register is the honest gap either way, and every op re-attempts.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := p.ready(ctx); err != nil {
			p.log.Warn("dataset: the register is not reachable; every op will report the gap", "err", err)
		}
	}()

	if err := mount(p, app); err != nil {
		return err
	}
	base.Log.Info("dataset plane mounted", "brand", deps.Brand, "env", deps.Env, "billing", p.bill.Enabled())
	return nil
}

// ready ensures the owned tables exist, once per process.
func (p *plane) ready(ctx context.Context) error {
	p.mu.Lock()
	done := p.schema
	p.mu.Unlock()
	if done {
		return nil
	}
	if err := p.ensure(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	p.schema = true
	p.mu.Unlock()
	return nil
}

// mount registers the surface. Separate from Mount because Mount BUILDS the
// plane and starts the background ensure, and the routes do not care where the
// plane came from — the same split apps/ml makes, for the same reason: a test
// pins the plane and exercises the wire.
func mount(p *plane, app cloud.Router) error {
	// cloud.ZipApp recovers the typed-op registry, which the Router interface does
	// not carry, and the ops register at ABSOLUTE paths on it. That is what keeps
	// the published address exactly `/v1/risk/datasets` — a group root composes to
	// `/v1/risk/datasets/`, and a trailing slash in the document is a trailing slash
	// in every generated SDK.
	//
	// cloud.Bridge is the composer's install — once at the root of every program —
	// so this package installs only its own envelope. app.Use lands that ONCE PER
	// DECLARED PREFIX — the scope reads manifest.Apps for that — so it covers
	// /v1/risk/datasets and nowhere else; grouping at /v1/risk for a shorter leaf
	// would spread it across a subtree this app does not own — that stem is the
	// decision plane's — which is the escape the scope exists to refuse.
	z := cloud.ZipApp(app)
	if z == nil {
		return fmt.Errorf("dataset.Mount: the router carries no typed-op registry")
	}
	// DenyEnvelope renders a gate refusal as the money wire's own bytes, the same
	// ones every other Hanzo surface emits — one error contract for "no funds"
	// across the fleet instead of this plane's private spelling of it. It touches
	// nothing else: any error that is not a gate denial passes through untouched.
	app.Use(cloud.DenyEnvelope())
	o := ops{p: p}

	zip.Post(z, "/v1/risk/datasets", o.create,
		zip.WithOperationID("riskCreateDataset"),
		zip.WithSummary("Declare the next version of a dataset"),
		zip.WithTags("risk"))
	zip.Get(z, "/v1/risk/datasets", o.list,
		zip.WithOperationID("riskDatasets"),
		zip.WithSummary("List this org's datasets"),
		zip.WithTags("risk"))
	zip.Get(z, "/v1/risk/datasets/:name", o.describe,
		zip.WithOperationID("riskDataset"),
		zip.WithSummary("Describe every version of one dataset"),
		zip.WithTags("risk"))
	zip.Delete(z, "/v1/risk/datasets/:name", o.dispose,
		zip.WithOperationID("riskDeleteDataset"),
		zip.WithSummary("Dispose of one dataset and every version of it"),
		zip.WithTags("risk"))
	zip.Post(z, "/v1/risk/datasets/:name/materialize", o.materialize,
		zip.WithOperationID("riskMaterializeDataset"),
		zip.WithSummary("Materialise the declared version into immutable rows"),
		zip.WithStatus(202),
		zip.WithTags("risk"))
	zip.Get(z, "/v1/risk/datasets/:name/lineage", o.lineage,
		zip.WithOperationID("riskDatasetLineage"),
		zip.WithSummary("Show where a version's rows came from, and whether that can still be demonstrated"),
		zip.WithTags("risk"))
	zip.Get(z, "/v1/risk/datasets/:name/export", o.export,
		zip.WithOperationID("riskExportDataset"),
		zip.WithSummary("Read a version's rows back, one page at a time"),
		zip.WithTags("risk"))
	return nil
}

// ── the caller ───────────────────────────────────────────────────────────────

// caller is everything an op needs about who is asking: the DATA key, and the
// BILLING identity, which are deliberately different values. The data key is
// `<brand>/<org>` and indexes the rows; the ledger is the org commerce debits.
// Conflating them would either bill the wrong account or key the wrong tenant.
type caller struct {
	key       tenant.Key
	by        string
	ledger    string
	project   string
	validated bool
	request   string
	ip        string
}

// who resolves the caller, and is the ONE place an op establishes a tenant.
//
// FAIL CLOSED off the HTTP path: no request, no validated principal, no key.
func (p *plane) who(ctx context.Context) (caller, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return caller{}, errNoTenant
	}
	k, err := tenant.Of(ctx, p.brand)
	if err != nil {
		return caller{}, err
	}
	// The mint's own shape check, asserted at the boundary rather than assumed. A
	// regression here is the whole product: this key is the register index, the row
	// index and the partition component at once.
	if !tenant.Qualified(p.brand, k) {
		return caller{}, zip.ErrForbidden("the tenant key is not qualified")
	}
	project, validated := principal.ValidatedProject(c)
	by := c.User()
	if by == "" {
		by = k.Org()
	}
	return caller{
		key:       k,
		by:        by,
		ledger:    principal.Ledger(c),
		project:   project,
		validated: validated,
		request:   c.RequestID(),
		ip:        cloud.ClientIP(c),
	}, nil
}

// maxJobs is how many source scans this process runs at once, across every
// tenant. It is the OTHER half of the bound, and the half a per-tenant limit
// cannot give: one-per-tenant stops a tenant spending the plane on itself, but a
// thousand tenants each holding their own one slot is still a thousand concurrent
// warehouse scans against a single stateful store, and a thousand times [maxRows]
// rows resident in one process. Both are fleet resources no tenant owns.
//
// Eight jobs of at most [maxRows] rows is [maxProcessBytes] resident and eight
// concurrent scans, which the store carries and this process survives. That first
// figure is a COMPUTED one, and it did not used to be: this comment read "a few
// hundred megabytes" while a row's subject was a string the caller sized, so the
// real number was whatever one tenant's `session_id` made it. See
// [maxSubjectBytes] — the count is only a byte bound because the value is bounded.
//
// The ceiling is stated here rather than inferred from a pool size so that raising
// it is a decision somebody made.
const maxJobs = 8

// admit is THE door to the source surface: it prices the act at the meter, takes
// the tenant's single slot and one of the plane's, and hands back the [scan] the
// read itself requires.
//
// EVERY op that touches the source goes through here — materialising, and tracing
// a lineage, which re-asks the source the SAME measured question and is therefore
// the same cost wearing a different verb. Lineage once did it with no gate, no
// meter and no bound at all: an unauthenticated-cheap GET that ran an exact
// distinct-count over up to 400 days of one tenant's feature surface, as many
// times in parallel as a client cared to ask. The fix is not a check added to that
// op; it is that the read cannot be spelled without the thing this returns.
//
// The DEBIT is not taken here. Admission can still fail after the gate — the
// attempt has to be recorded before any work starts — and a caller must not pay
// for work the plane then refused. Each op meters once its own act is on record.
func (p *plane) admit(ctx context.Context, c caller, name string, version int, cost int64) (scan, error) {
	if err := p.charge(ctx, c, cost); err != nil {
		return scan{}, err
	}
	return p.claim(c.key, name, version)
}

// charge is the ONE call to the meter's gate, and the ONE place its refusal
// becomes an answer.
//
// A gate refuses in three ways and only two of them are the caller's business:
// out of funds and cap reached are 402s a caller can act on, and everything else
// means the BILLER could not be asked — which arrives as a foreign error carrying
// the peer's transport detail ("gate: commerce unreachable: dial tcp
// 10.x.y.z:8080..."). zip's default handler puts an unrecognised error's own text
// in the response body, so returning that verbatim publishes an internal address
// to whoever asked. [cloud.Denied] is the fleet's ONE rendering of all three —
// 402 insufficient_balance, 402 spend_cap_exceeded, 503 balance_unavailable —
// with a fixed sentence per class and no room for a peer's words. The reason goes
// to the log, where the operator is.
//
// It is one function because it is called from two ops, and "remember to wrap the
// gate" is not a property; being unable to call the gate any other way is.
func (p *plane) charge(ctx context.Context, c caller, cost int64) error {
	err := p.bill.Gate(ctx, c.ledger, c.project, c.validated, "dataset", cost)
	if err == nil {
		return nil
	}
	p.log.Warn("dataset: the gate refused", "tenant", c.key.String(), "cents", cost, "err", err)
	return cloud.Denied(err)
}

// claim takes the tenant's one source-scan slot.
//
// ONE SCAN PER TENANT, AND [maxJobs] IN THE PROCESS. Both refusals are REFUSALS
// and not queues: a queue admits the same work later, so it converts a bound into
// a delay and hides the fact that the plane is full. The two are distinguished
// because they are different facts — one is this tenant's own doing and is
// answered 409, the other is the plane's and is answered 503, which is the one a
// caller should retry.
func (p *plane) claim(k tenant.Key, name string, version int) (scan, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if held, busy := p.busy[k.String()]; busy {
		return scan{}, zip.ErrConflict(fmt.Sprintf(
			"this org is already reading the source for %q version %d; one at a time",
			held.name, held.version))
	}
	if len(p.busy) >= maxJobs {
		return scan{}, zip.Errorf(503,
			"this plane is running its %d concurrent source scans; try again shortly", maxJobs)
	}
	held := scan{k: k, name: name, version: version, since: time.Now().UTC()}
	p.busy[k.String()] = held
	return held, nil
}

// release gives the slot back. Always deferred by whoever claimed it, so a panic
// does not wedge a tenant out of its own plane for the life of the process.
func (p *plane) release(k tenant.Key) {
	p.mu.Lock()
	delete(p.busy, k.String())
	p.mu.Unlock()
}

// running reports the scan this process holds for a tenant, if any. It is how
// `describe` tells "in flight right now" from "started by a process that is
// gone" — two states the store cannot tell apart, because the store cannot know
// which processes are alive.
func (p *plane) running(k tenant.Key) (scan, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	held, ok := p.busy[k.String()]
	return held, ok
}

// internal is what a caller is told when the reason is not theirs to read.
const internal = "the dataset plane failed; this deployment's log names the reason"

// gap is the ONE funnel every op's error passes through, and the boundary
// between what this package AUTHORED and what came back from the store.
//
// Three cases, and the third is why this is a funnel rather than a pass-through.
// A [zip.HTTPError] was written here, in a literal, to be read by a caller, and
// is returned unchanged. [errStore] is the honest gap: 503, never an empty
// answer, because an empty list on a dead store reads exactly like a tenant that
// has declared nothing. ANYTHING ELSE is a foreign error — a driver message
// carrying the statement, the table and the host — and zip's default handler puts
// an unrecognised error's own text in the response body. So it is logged and
// replaced: the caller learns the plane failed, the operator learns why, and the
// two facts go to the two audiences that should have them.
func (p *plane) gap(err error) error {
	var he *zip.HTTPError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &he):
		return err
	case errors.Is(err, errStore):
		p.log.Error("dataset: the store did not answer", "err", err)
		return zip.Errorf(503, "the dataset plane's store is not reachable, so this answer would be a guess")
	}
	p.log.Error("dataset: an error a caller must not be shown", "err", err)
	return zip.ErrInternal(internal)
}

// reason is the sentence a refused version RECORDS, derived from [gap] rather
// than restated — a register row is served to a caller, so the two must never
// differ about what a caller may read.
func (p *plane) reason(err error) string {
	var he *zip.HTTPError
	if errors.As(p.gap(err), &he) {
		return he.Msg
	}
	return internal
}
