// Package dataset is the per-org dataset plane of /v1/ml: a dataset is a
// VERSIONED, IMMUTABLE snapshot of one tenant's own event surface, and this is
// where it is declared, materialised, described, exported and disposed of.
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
//	immutability  A published version is `ready`, which is the GREATEST rank of
//	              the ReplacingMergeTree version column — no later write of any
//	              other stage can displace it — and the door refuses any
//	              transition out of a terminal state. Two layers, engine and door.
//	bounds        spec.go — the window, the horizon, the row cap, the number of
//	              names and the number of versions are all bounded at the door, and
//	              materialisation is one job per tenant at a time.
//	expiry        There is NO table TTL. Disposal is the tenant's own DROP
//	              PARTITION on (org, name), which cannot be spelled cross-tenant.
//
// WHY IT IS ITS OWN APP. It shares no state with a scorer: there is no in-memory
// model, no ring, no single-writer file. Everything it knows is in the warehouse,
// so it restarts empty and answers identically — which is exactly what a plane
// holding the record of what a model trained on must do, and exactly what a
// process pinned to one replica for its in-memory forests cannot promise. Its
// surface is five leaves under /v1/ml that no other app claims; zip refuses two
// owners for one prefix at compose time, so that is checked rather than agreed.
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
// the rows are in the store, so two processes over one store answer identically
// and a restart loses nothing but the jobs in flight.
type plane struct {
	// store is the warehouse. An interface so a test drives the plane, and so
	// nothing here can reach the connection for anything else.
	store store
	// brand is the deployment's brand — half the tenant key, and never a header.
	brand string
	// bill gates and meters the one expensive op.
	bill *cloud.ResourceMeter
	// log is the scoped subsystem logger.
	log interface {
		Info(string, ...any)
		Warn(string, ...any)
		Error(string, ...any)
	}

	// mu guards the two facts that are this process's rather than the store's.
	mu sync.Mutex
	// busy is the in-flight materialisation per tenant — the R5 gate. ONE job per
	// tenant, so a tenant looping the op queues nothing and spends nothing: the
	// second call is refused, not admitted and then starved.
	busy map[string]inflight
	// schema records whether the owned tables have been created. Only success
	// latches, so a warehouse that was still connecting at boot is retried.
	schema bool
}

// inflight is one running materialisation, as this process sees it.
type inflight struct {
	Name    string
	Version int
	Since   time.Time
}

// Mount wires the dataset leaves of /v1/ml onto app.
//
// EVERY INHERITED CAPABILITY IS WIRED HERE, EXPLICITLY. Being embedded in cloud
// makes each one AVAILABLE; none is automatic:
//
//	IAM auth     SanitizeIdentity mints X-Org-Id from the verified bearer, in
//	             serve.go. This app never validates a token and never can.
//	tenant gate  cloud.Bridge() on the group, FIRST, before any leaf — a typed op
//	             receives only a context, and Bridge is what parks the validated
//	             org in it. fiber orders middleware by registration.
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
	if deps.Brand == "" {
		// The brand is half the tenant key. A deployment that did not state one
		// cannot mint a key and every request would 403 — better said at boot than
		// once per request.
		return fmt.Errorf("dataset.Mount: no brand, so no tenant key can be minted")
	}
	base := cloud.NewBase(deps, "dataset")
	p := &plane{
		store: warehouse{},
		brand: deps.Brand,
		bill:  base.Bill,
		log:   base.Log,
		busy:  map[string]inflight{},
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
	// TWO SEAMS, EACH FOR WHAT IT IS FOR.
	//
	// app.Use installs the tenant bridge ONCE PER DECLARED PREFIX — the scope
	// reads manifest.Apps for that — so the middleware lands exactly on
	// /v1/ml/datasets and nowhere else. Grouping at /v1/ml to get a shorter leaf
	// would install it across a subtree this app does not own, which is the escape
	// the scope exists to refuse.
	//
	// cloud.ZipApp recovers the typed-op registry, which the Router interface does
	// not carry, and the ops register at ABSOLUTE paths on it. That is what keeps
	// the published address exactly `/v1/ml/datasets` — a group root composes to
	// `/v1/ml/datasets/`, and a trailing slash in the document is a trailing slash
	// in every generated SDK.
	//
	// Bridge FIRST, before any leaf: a typed op receives only a context, and this
	// is what parks the validated org that tenant.Of reads. fiber runs middleware
	// in registration order, so an install after its leaves never runs.
	z := cloud.ZipApp(app)
	if z == nil {
		return fmt.Errorf("dataset.Mount: the router carries no typed-op registry")
	}
	app.Use(cloud.Bridge())
	o := ops{p: p}

	zip.Post(z, "/v1/ml/datasets", o.create,
		zip.WithOperationID("mlCreateDataset"),
		zip.WithSummary("Declare the next version of a dataset"),
		zip.WithTags("ml"))
	zip.Get(z, "/v1/ml/datasets", o.list,
		zip.WithOperationID("mlDatasets"),
		zip.WithSummary("List this org's datasets"),
		zip.WithTags("ml"))
	zip.Get(z, "/v1/ml/datasets/:name", o.describe,
		zip.WithOperationID("mlDataset"),
		zip.WithSummary("Describe every version of one dataset"),
		zip.WithTags("ml"))
	zip.Delete(z, "/v1/ml/datasets/:name", o.dispose,
		zip.WithOperationID("mlDeleteDataset"),
		zip.WithSummary("Dispose of one dataset and every version of it"),
		zip.WithTags("ml"))
	zip.Post(z, "/v1/ml/datasets/:name/materialize", o.materialize,
		zip.WithOperationID("mlMaterializeDataset"),
		zip.WithSummary("Materialise the declared version into immutable rows"),
		zip.WithStatus(202),
		zip.WithTags("ml"))
	zip.Get(z, "/v1/ml/datasets/:name/lineage", o.lineage,
		zip.WithOperationID("mlDatasetLineage"),
		zip.WithSummary("Show where a version's rows came from, and whether that can still be demonstrated"),
		zip.WithTags("ml"))
	zip.Get(z, "/v1/ml/datasets/:name/export", o.export,
		zip.WithOperationID("mlExportDataset"),
		zip.WithSummary("Read a version's rows back, one page at a time"),
		zip.WithTags("ml"))
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

// maxJobs is how many materialisations this process runs at once, across every
// tenant. It is the OTHER half of the bound, and the half a per-tenant limit
// cannot give: one-per-tenant stops a tenant spending the plane on itself, but a
// thousand tenants each holding their own one slot is still a thousand concurrent
// warehouse scans against a single stateful store, and a thousand times [maxRows]
// rows resident in one process. Both are fleet resources no tenant owns.
//
// Eight jobs of at most 200k rows is a few hundred megabytes and eight concurrent
// scans, which the store carries and this process survives. The ceiling is stated
// here rather than inferred from a pool size so that raising it is a decision
// somebody made.
const maxJobs = 8

// claim takes the tenant's one materialisation slot.
//
// ONE JOB PER TENANT, AND [maxJobs] IN THE PROCESS. Both refusals are REFUSALS
// and not queues: a queue admits the same work later, so it converts a bound into
// a delay and hides the fact that the plane is full. The two are distinguished
// because they are different facts — one is this tenant's own doing and is
// answered 409, the other is the plane's and is answered 503, which is the one a
// caller should retry.
func (p *plane) claim(k tenant.Key, name string, version int) (inflight, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if held, busy := p.busy[k.String()]; busy {
		return held, zip.ErrConflict(fmt.Sprintf(
			"a materialisation of %q version %d is already running for this org; one at a time",
			held.Name, held.Version))
	}
	if len(p.busy) >= maxJobs {
		return inflight{}, zip.Errorf(503,
			"this plane is running its %d concurrent materialisations; try again shortly", maxJobs)
	}
	held := inflight{Name: name, Version: version, Since: time.Now().UTC()}
	p.busy[k.String()] = held
	return held, nil
}

// release gives the slot back. Always deferred by the job, so a panic in the job
// does not wedge a tenant out of its own plane for the life of the process.
func (p *plane) release(k tenant.Key) {
	p.mu.Lock()
	delete(p.busy, k.String())
	p.mu.Unlock()
}

// running reports the materialisation this process holds for a tenant, if any.
// It is how `describe` tells "in flight right now" from "started by a process
// that is gone" — two states the store cannot tell apart, because the store
// cannot know which processes are alive.
func (p *plane) running(k tenant.Key) (inflight, bool) {
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
