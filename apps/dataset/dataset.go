package dataset

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/tenant"
	"github.com/hanzoai/account"
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
	bill biller
	// log is the scoped subsystem logger.
	log interface {
		Info(string, ...any)
		Warn(string, ...any)
		Error(string, ...any)
	}

	// pods is the deployment's: it holds the single-use token a version is started
	// under. nil is this process's default.
	pods *coord.Coord

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

// meter is the plane's half of the fleet's billing client: GATE before a priced act
// and DEBIT after it. *cloud.Meter is the production value.
//
// It is an interface for one reason a concrete meter cannot give: a gate can fail
// with a FOREIGN error — "commerce unreachable: <transport detail>" — and what a
// caller is shown when that happens is a property this package must be able to
// test. Reaching that state with a real meter needs a commerce peer to be down,
// which is not a state a unit test can stand up.
//
// The first parameter is the money's ADDRESS and not an org slug, because that is
// what the real meter takes: an account carries both the books and the account
// within them, so a gate and a debit cannot name different ones by accident.
type biller interface {
	Authorize(ctx context.Context, payer account.Account, project string, projectValidated bool, kind string, costCents int64) error
	Record(payer account.Account, kind string, u meter.Usage)
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
//	meter+gate   cloud.NewMeter(deps, "dataset"); Gate before the one
//	             priced op and Meter after it.
//	logs         cloud.NewBase(deps, "dataset") gives the scoped logger.
//	traces       global and already ZAP-native (OTLZ). This package imports no
//	             otlp transport, deliberately.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("dataset: nil app")
	}
	if err := tenant.Vouches(cloud.Brand()); err != nil {
		// The brand is half the tenant key, and the half that must be a brand the
		// registry carries — the rollup that WRITES this plane's source refuses any
		// other, so a key minted under one would read a tenant nobody can write.
		// A deployment that cannot mint a key would 403 every request; better said
		// once at boot than once per caller.
		return fmt.Errorf("dataset.Use:  %q cannot mint a tenant key: %w", cloud.Brand(), err)
	}
	base := cloud.NewBase(deps, "dataset")
	p := &plane{
		store: warehouse{},
		brand: cloud.Brand(),
		bill:  base.Bill,
		log:   base.Log,
		pods:  base.Coord,
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
	base.Log.Info("dataset plane mounted", "brand", cloud.Brand(), "env", cloud.Env())
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
	// The group is this app's declared prefix, so the envelope below and every
	// leaf cover /v1/dataset and nowhere else. cloud.Bridge is the composer's
	// install, once at the root of every program, so this package installs only
	// its own envelope.
	g := app.Group("/v1/dataset")
	// DenyEnvelope renders a gate refusal as the money wire's own bytes, the same
	// ones every other Hanzo surface emits — one error contract for "no funds"
	// across the fleet instead of this plane's private spelling of it. It touches
	// nothing else: any error that is not a gate denial passes through untouched.
	g.Use(cloud.DenyEnvelope())
	o := ops{p: p}

	// The collection is the group's own address, declared as the empty leaf.
	g.Post("", o.create,
		zip.WithOperationID("riskCreateDataset"),
		zip.WithSummary("Declare the next version of a dataset"),
		zip.WithTags("dataset"))
	g.Get("", o.list,
		zip.WithOperationID("riskDatasets"),
		zip.WithSummary("List this org's datasets"),
		zip.WithTags("dataset"))
	g.Get("/:name", o.describe,
		zip.WithOperationID("riskDataset"),
		zip.WithSummary("Describe every version of one dataset"),
		zip.WithTags("dataset"))
	g.Delete("/:name", o.dispose,
		zip.WithOperationID("riskDeleteDataset"),
		zip.WithSummary("Dispose of one dataset and every version of it"),
		zip.WithTags("dataset"))
	g.Post("/:name/materialize", o.materialize,
		zip.WithOperationID("riskMaterializeDataset"),
		zip.WithSummary("Materialise the declared version into immutable rows"),
		zip.WithStatus(202),
		zip.WithTags("dataset"))
	g.Get("/:name/lineage", o.lineage,
		zip.WithOperationID("riskDatasetLineage"),
		zip.WithSummary("Show where a version's rows came from, and whether that can still be demonstrated"),
		zip.WithTags("dataset"))
	g.Get("/:name/export", o.export,
		zip.WithOperationID("riskExportDataset"),
		zip.WithSummary("Read a version's rows back, one page at a time"),
		zip.WithTags("dataset"))
	return nil
}

// ── the caller ───────────────────────────────────────────────────────────────

// caller is everything an op needs about who is asking: the DATA key, and the
// BILLING identity, which are deliberately different values. The data key is
// `<brand>/<org>` and indexes the rows; the payer is the account commerce debits.
// Conflating them would either bill the wrong account or key the wrong tenant.
type caller struct {
	key       tenant.Key
	by        string
	payer     account.Account
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
		payer:     meter.Payer(c),
		project:   project,
		validated: validated,
		request:   c.RequestID(),
		ip:        geo.IP(c),
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

// admit is THE entry point to the source surface: it prices the act at the meter,
// takes the tenant's single slot and one of the plane's, and hands back the [scan]
// the read itself requires.
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
	// c.payer is the account [plane.who] resolved off the request; the debit this
	// gate admits lands on the same one.
	err := p.bill.Authorize(ctx, c.payer, c.project, c.validated, "dataset", cost)
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

// live reports whether a version is being materialised right now, by any process.
// The register says a version is `materializing` from the moment its attempt is put on
// record until the job records its outcome, and a job has a wall: [jobBudget] to build
// and [recordBudget] to say how it ended. So a version in that status inside the wall is
// running, and one past it is an attempt whose process is gone, which the register
// cannot tell apart from a running one any other way, because the store cannot know
// which processes are alive.
func (p *plane) live(e entry) bool {
	return e.Status == statusMaterialize && time.Since(e.At) < jobBudget+recordBudget
}

// coord is the deployment's Coord: the one this plane was built with, else this
// process's default.
func (p *plane) coord() *coord.Coord {
	if p.pods != nil {
		return p.pods
	}
	return coord.Default()
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
	if he, ok := errors.AsType[*zip.HTTPError](p.gap(err)); ok {
		return he.Msg
	}
	return internal
}
