package dataset

// typed.go is the whole contract. Every leaf of this plane is a zip TYPED op, so
// ONE registration is the REST route, the OpenAPI operation, the MCP tool, the
// CLI command and every generated SDK method at once. There is no untyped route
// here and no hand-written spec entry: a second copy of a contract drifts, and
// the drift is invisible until a customer finds it.
//
// A typed op's Go type name IS its schema name and the fleet's schema namespace
// is FLAT — openapi.Compose refuses one name with two shapes across apps — so every
// name below carries the plane it belongs to.

import (
	"context"
	"time"

	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// ops binds the plane to the typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) and has no parameter for the service,
// so the plane arrives as a RECEIVER and every op is a method value (o.create) —
// which is also the only bound form cmd/zipdoc can lift prose from: a closure
// returned by a helper is a call expression with nothing to read.
type ops struct{ p *plane }

// ── what a caller sends ──────────────────────────────────────────────────────

// riskDatasetSpec is the whole of what a dataset IS: a bound query over this org's
// own feature surface, a maturity horizon, where the splits cut, and the seed
// that decides membership. Declaring one mints the next VERSION; it never
// rewrites an existing one.
//
// Nothing here becomes a SQL identifier. Dims resolve through the published
// allowlist to fixed columns, the kind is checked against a closed set, and every
// remaining value binds.
type riskDatasetSpec struct {
	// Name identifies the dataset across its versions: lower-case letters, digits
	// and hyphens, starting with a letter.
	Name string `json:"name"`
	// Kind narrows to one subject kind — person, session or account. Empty takes
	// every kind.
	Kind string `json:"kind,omitempty"`
	// Dims are the coordinates to carry, by published name. Empty takes the whole
	// surface. They are stored in the plane's own order, never the order given, so
	// two requests naming the same dims produce identical rows.
	Dims []string `json:"dims,omitempty"`
	// From is where the event window opens, RFC 3339, INCLUSIVE. The window may
	// not be longer than the source's own retention: past that, its older half is
	// already gone and the dataset would silently be shorter than it says.
	From string `json:"from"`
	// To is where the window ends, EXCLUSIVE, so two datasets meeting at one
	// instant share no row. A materialisation reads less than this — the end is
	// pulled back by Horizon, and the lineage reports the window it actually read.
	To string `json:"to"`
	// Horizon is how many days a row must have aged before it may be admitted. It
	// is what keeps a fact that was not yet knowable at scoring time out of a
	// training set: a chargeback lands 30 to 120 days after the transaction it
	// condemns, so 120 for the payment lane and 14 for signup abuse. Zero admits
	// the whole window and is honest only where the outcome is immediate.
	Horizon int `json:"horizon"`
	// Cuts are the two RFC 3339 instants dividing train | val | test. Omit them to
	// take 70% and 85% of the window by time. Splitting is TEMPORAL and then
	// grouped by subject — a random split puts one device on both sides of the
	// line and the model memorises the entity instead of the behaviour.
	Cuts []string `json:"cuts,omitempty"`
	// Seed decides WHICH subjects are admitted when the window holds more rows
	// than the cap allows. It is recorded on the version, so a capped dataset is
	// reproducible rather than being whichever rows the store returned first.
	// Omit it to seed from the dataset's name.
	Seed string `json:"seed,omitempty"`
	// Rows caps the materialisation. Zero takes the plane's own bound.
	Rows int `json:"rows,omitempty"`
}

// riskDatasetsIn takes nothing off the wire. The whole input is the caller's
// validated principal, which is what decides whose datasets these are.
type riskDatasetsIn struct{}

// riskDatasetRef addresses one dataset by name. The name is the path segment: the
// URL is the addressing authority, so it binds from there whatever a body says.
type riskDatasetRef struct {
	// Name is the dataset, from the path.
	Name string `json:"name"`
}

// riskMaterializeIn asks for the declared version to be built. It carries only the
// name because a materialisation always targets the version that is DECLARED —
// there is no version to choose, and offering one would suggest a published
// version could be rebuilt.
type riskMaterializeIn struct {
	// Name is the dataset, from the path.
	Name string `json:"name"`
}

// riskDatasetDisposeIn asks for a whole dataset to be disposed of. It carries no version,
// because disposal is per DATASET: the register row and the bytes go together, in
// one partition drop that cannot name another tenant.
type riskDatasetDisposeIn struct {
	// Name is the dataset, from the path.
	Name string `json:"name"`
}

// riskLineageIn addresses one version's lineage.
type riskLineageIn struct {
	// Name is the dataset, from the path.
	Name string `json:"name"`
	// Version is the version to trace. Zero takes the newest published one.
	Version int `json:"version,omitempty"`
}

// riskExportIn reads a published version's rows back, one page at a time. The page
// is bounded by the plane, not by the caller: an export is a read of the same
// store every other tenant is using.
type riskExportIn struct {
	// Name is the dataset, from the path.
	Name string `json:"name"`
	// Version is the version to read. Zero takes the newest published one.
	Version int `json:"version,omitempty"`
	// Split narrows to train, val or test. Empty reads every split.
	Split string `json:"split,omitempty"`
	// Offset is where the page starts, in the version's own row order (by id,
	// which is derived from the row and therefore stable forever).
	Offset int `json:"offset,omitempty"`
	// Limit is how many rows to return. Zero and anything above the plane's bound
	// take the bound.
	Limit int `json:"limit,omitempty"`
}

// ── what a caller gets ───────────────────────────────────────────────────────

// riskDataset is one version of one dataset. A version is the unit of citation: a
// model names the dataset AND the version AND the digest, or it has not said what
// it was fitted on.
type riskDataset struct {
	// Name identifies the dataset across all of its versions.
	Name string `json:"name"`
	// Version is which version this is, from 1 and monotone within the dataset.
	// A number is never reused — not even after a disposal, where the next declare
	// continues the count — so "signups v3" means one thing forever, which is what
	// makes a model's citation of it checkable.
	Version int `json:"version"`
	// At is when this version last changed state, RFC 3339 UTC.
	At string `json:"at"`
	// By is who moved it there: the validated user, or the org itself when the
	// caller is a machine with no user behind it.
	By string `json:"by"`
	// Status is declared, materializing, ready or refused. Only `ready` has bytes,
	// and `ready` is terminal: a published version is never rewritten.
	Status string `json:"status"`
	// Running is true while THIS process is materialising the version. A version
	// that is `materializing` and not running was started by a process that is
	// gone — two states the register cannot tell apart, because a register cannot
	// know which processes are alive.
	Running bool `json:"running,omitempty"`
	// Refusal names why there are no bytes, when there are none.
	Refusal string `json:"refusal,omitempty"`
	// Digest fingerprints the SPEC and the ROWS together. Two materialisations of
	// one spec agree on it or the plane says they do not.
	Digest string `json:"digest,omitempty"`
	// Spec is the bound query this version was built from, exactly as recorded.
	Spec riskDatasetSpec `json:"spec"`
	// Counts is how the rows fall across the splits.
	Counts riskSplitCounts `json:"counts"`
	// Share is the fraction of the window's subjects admitted, in thousandths.
	// 1000 means the whole window fitted under the cap; anything less means the
	// version is a reproducible sample and says by how much.
	Share int `json:"share,omitempty"`
	// Truncated is true when the row cap bound before the window ran out. The
	// trailing subject is dropped whole when that happens, because half a subject
	// on one side of a split is exactly the leak the grouping prevents.
	Truncated bool `json:"truncated,omitempty"`
	// Oversize is how many of the window's subjects this version could NOT carry
	// because their subject identity exceeds the plane's per-subject byte bound.
	//
	// It is on the wire, not only in a log, because it is the one degradation a
	// caller cannot otherwise detect: the rows that are here look complete, and a
	// dataset silently missing a population is a model silently blind to it.
	// Non-zero does not make a version invalid — it makes it a version whose
	// coverage is STATED. Zero is the normal case and omits.
	Oversize int `json:"oversize,omitempty"`
}

// riskSplitCounts is how a version's rows fall, and how much of it is judged.
type riskSplitCounts struct {
	// Rows is how many rows the version holds across every split. It is the size
	// of the version, not of the source window — the horizon, the cuts and the row
	// cap all bind before this number.
	Rows int `json:"rows"`
	// Train is how many rows fall before the first cut — the EARLIEST slice of the
	// window, which is what a model is fitted on.
	Train int `json:"train"`
	// Val is how many fall between the two cuts, held out for tuning.
	Val int `json:"val"`
	// Test is how many fall after the second cut — the LATEST slice, and the only
	// one a score is honest about, since the split is temporal.
	Test int `json:"test"`
	// Subjects is how many distinct subjects the rows belong to. Every row of one
	// subject is in ONE split, so this is the real sample size — the row count
	// flatters it whenever a subject is active.
	Subjects int `json:"subjects"`
	// Judged is how many rows carry a disposition. It is zero until a label plane
	// writes one, and reporting it plainly is what lets a model plane refuse to
	// rank rather than name a winner it cannot justify.
	Judged int `json:"judged"`
	// Productive is how many judged rows carry the one disposition.
	Productive int `json:"productive"`
	// Unproductive is how many carry the other. With Productive it accounts for
	// Judged, so the class imbalance is visible before anyone trains on it; both
	// stay 0 while Judged is 0.
	Unproductive int `json:"unproductive"`
}

// riskDatasetList is every dataset this org holds, newest version first.
type riskDatasetList struct {
	// Items is one entry per dataset, carrying its newest version. Never null: an
	// org that has declared nothing gets an empty array.
	Items []riskDataset `json:"items"`
}

// riskDatasetVersions is every version of one dataset, newest first. The whole
// history is returned because the point of a version is that the old ones are
// still there: a model fitted last quarter cites one of them.
type riskDatasetVersions struct {
	// Name is the dataset these versions belong to, as the register holds it.
	Name string `json:"name"`
	// Items is every version of it, newest first — including the disposed ones,
	// whose record outlives their rows. Never null.
	Items []riskDataset `json:"items"`
}

// riskLineage is where a version's rows came from, and whether that can still be
// DEMONSTRATED. Reproducible is measured by asking the source the same question
// again — it is false when the source has since expired the window, which is a
// fact about the plane rather than a failure, and hiding it would make every
// lineage claim unfalsifiable.
type riskLineage struct {
	// Dataset is the dataset traced.
	Dataset string `json:"dataset"`
	// Version is the version traced — the one asked for, or the newest published
	// one when the request named none.
	Version int `json:"version"`
	// Source is the plane the rows were derived from.
	Source string `json:"source"`
	// From is where the window actually read opens, RFC 3339. Same as the spec's.
	From string `json:"from"`
	// To is where it ends: the spec's own end pulled BACK by the maturity horizon,
	// so it is usually earlier than the spec says. This is the window a
	// reproduction has to ask for — asking the spec's would not return these rows.
	To string `json:"to"`
	// Rows is how many rows the source held for that window at materialisation
	// time. Holds is the same question asked now, and the difference between them
	// is the whole of the reproducibility claim.
	Rows int `json:"rows"`
	// Subjects is how many distinct subjects those rows belonged to. It is the
	// real sample size — the row count flatters it whenever a subject is active.
	Subjects int `json:"subjects"`
	// Share is the fraction of subjects admitted, in thousandths.
	Share int `json:"share"`
	// Oversize is how many subjects the window held that were too large to
	// represent when this version was built. It is part of the fingerprint, so it
	// is part of what "reproducible" is measured over.
	Oversize int `json:"oversize,omitempty"`
	// Holds is what the source holds for the same window NOW. The difference
	// between it and Rows is the whole of the reproducibility claim.
	Holds int `json:"holds"`
	// Retention is the source's own expiry rule as the store reports it, read at
	// materialisation time rather than assumed. A source whose retention is
	// shorter than this window cannot re-derive it.
	Retention string `json:"retention,omitempty"`
	// Digest is the version's fingerprint, repeated here so a lineage answer is
	// self-contained.
	Digest string `json:"digest"`
	// Reproducible is true when the source still holds what this version was built
	// from — measured by asking it again, not recalled. False is ordinary: the
	// source is fed by a rollup that runs behind the events, so "it holds more
	// now" is the common case and it means re-running the spec would not produce
	// this version.
	Reproducible bool `json:"reproducible"`
	// Refusal says which way it failed — the window expired, or the source now
	// holds a different count. Absent when Reproducible is true.
	Refusal string `json:"refusal,omitempty"`
}

// riskDatasetRow is one row of a published version.
type riskDatasetRow struct {
	// ID names the row forever. It is DERIVED from the row's own subject and
	// instant, not allocated, so two materialisations of the same fact agree on it
	// without coordinating.
	ID string `json:"id"`
	// Split is train, val or test.
	Split string `json:"split"`
	// Kind is the subject kind: person, session or account.
	Kind string `json:"kind"`
	// Subject is the identity within that kind — whose row this is. Every row of
	// one subject is in ONE split, decided by that subject's earliest instant, so
	// a subject is never on both sides of a cut.
	Subject string `json:"subject"`
	// At is the row's instant.
	At string `json:"at"`
	// Point is the coordinates, in the order the version's spec names its dims.
	Point []float64 `json:"point"`
}

// riskDatasetRows is one page of a version's rows.
type riskDatasetRows struct {
	// Dataset is the dataset the page was read from.
	Dataset string `json:"dataset"`
	// Version is which published version it was read from — the one asked for, or
	// the newest published one when the request named none.
	Version int `json:"version"`
	// Digest is the version's fingerprint. An exported page that did not carry it
	// would be bytes with no way to say which dataset they are.
	Digest string `json:"digest"`
	// Dims names what each coordinate of Point means, in Point's own order.
	Dims []string `json:"dims"`
	// Offset is where this page starts in the version's own row order, which is by
	// row id and therefore stable forever.
	Offset int `json:"offset"`
	// Limit is the page size actually served: the one asked for, clamped to the
	// plane's own bound of 5000. Fewer rows than Limit means the version ended.
	Limit int `json:"limit"`
	// Rows is the page. Never null.
	Rows []riskDatasetRow `json:"rows"`
}

// riskDatasetDisposal is what a disposal removed. A retention action answers with what it
// destroyed, because "204 No Content" is a poor reply to "prove you deleted it".
type riskDatasetDisposal struct {
	// Dataset is the dataset that was disposed of. The NAME survives: declaring it
	// again continues the version count rather than starting over at 1.
	Dataset string `json:"dataset"`
	// Versions is how many versions went.
	Versions int `json:"versions"`
	// Rows is how many rows they held between them, as the REGISTER recorded them
	// when each was materialised — not a count of what the drop deleted, which is
	// gone by the time this answers.
	Rows int `json:"rows"`
}

// ── the ops ──────────────────────────────────────────────────────────────────

// CreateDataset declares the next version of a dataset from a bound query over
// this org's own feature surface.
//
// It mints a VERSION and writes no rows: a version is declared, then materialised
// once, then never rewritten. Version numbers are monotone and never reused, so
// "version 3 of signups" means one thing forever — which is the whole reason a
// model can cite one.
//
// The window is bounded by the source's retention, the horizon by a year, the
// rows by the plane's cap, and the number of datasets and versions per org by
// their own limits. Every refusal names which bound it hit.
//
// Example: {"name": "signups", "kind": "person", "from": "2026-01-01T00:00:00Z", "to": "2026-04-01T00:00:00Z", "horizon": 14}
func (o ops) create(ctx context.Context, in *riskDatasetSpec) (*riskDataset, error) {
	c, err := o.p.who(ctx)
	if err != nil {
		return nil, err
	}
	s, err := normalize(*in, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if err := o.p.charge(ctx, c, declareCost); err != nil {
		return nil, err
	}
	e, err := o.p.declare(ctx, c, s)
	if err != nil {
		return nil, o.p.gap(err)
	}
	return o.view(c, e), nil
}

// Datasets lists this org's datasets, each with its newest version. An org that
// has declared none gets an empty list; a store that cannot be reached gets a
// refusal, never an empty list, because the two read identically and only one of
// them is true.
func (o ops) list(ctx context.Context, _ *riskDatasetsIn) (*riskDatasetList, error) {
	c, err := o.p.who(ctx)
	if err != nil {
		return nil, err
	}
	if err := o.p.ready(ctx); err != nil {
		return nil, o.p.gap(err)
	}
	all, err := o.p.names(ctx, c.key)
	if err != nil {
		return nil, o.p.gap(err)
	}
	out := &riskDatasetList{Items: []riskDataset{}}
	seen := map[string]bool{}
	for _, e := range all {
		if seen[e.Name] {
			continue
		}
		seen[e.Name] = true
		out.Items = append(out.Items, *o.view(c, e))
	}
	return out, nil
}

// Dataset describes every version of one dataset, newest first — the whole
// history, because the point of a version is that the older ones are still there
// and a model fitted last quarter cites one of them.
//
// A name this org does not own answers 404, exactly as an unknown name does, so a
// probe learns nothing about another tenant's datasets.
//
// Example: {"name": "signups"}
func (o ops) describe(ctx context.Context, in *riskDatasetRef) (*riskDatasetVersions, error) {
	c, err := o.p.who(ctx)
	if err != nil {
		return nil, err
	}
	if err := o.p.ready(ctx); err != nil {
		return nil, o.p.gap(err)
	}
	es, err := o.p.versions(ctx, c.key, in.Name)
	if err != nil {
		return nil, o.p.gap(err)
	}
	if len(es) == 0 {
		return nil, zip.ErrNotFound("no such dataset")
	}
	out := &riskDatasetVersions{Name: es[0].Name, Items: make([]riskDataset, 0, len(es))}
	for _, e := range es {
		out.Items = append(out.Items, *o.view(c, e))
	}
	return out, nil
}

// MaterializeDataset builds the declared version into immutable rows and answers
// 202 as soon as the attempt is on record.
//
// It never holds the request open for the work: a materialisation is a bounded
// warehouse scan, and letting an HTTP client's timeout be a data plane's timeout
// is how one tenant's retry loop becomes everyone's outage. ONE materialisation
// runs per org at a time; a second is refused rather than queued, because a queue
// admits the same work later and the honest answer to "again" while one is
// running is that one is running.
//
// Only a DECLARED version is admitted. A published version is immutable, and a
// version whose earlier attempt did not complete is never re-attempted — that
// would union two runs' rows under one number and make the digest a lie. In both
// cases the answer is to declare a new version, which is what a second run over a
// moving source honestly is.
//
// Example: {"name": "signups"}
func (o ops) materialize(ctx context.Context, in *riskMaterializeIn) (*riskDataset, error) {
	c, err := o.p.who(ctx)
	if err != nil {
		return nil, err
	}
	// The gate, the tenant's slot and the plane's ceiling are all inside start —
	// it takes them through the same [plane.admit] a lineage does, because both
	// spend the same statement over the same source.
	e, err := o.p.start(ctx, c, in.Name)
	if err != nil {
		return nil, o.p.gap(err)
	}
	// c.ledger is a field on the resolved caller — a string the op is carrying — so the
	// address is parsed back out of it, the same way [plane.charge] does for the gate
	// this debit settles.
	o.p.bill.Meter(account.PayerOf("", c.ledger), c.project, "dataset", materializeCost, c.request, c.ip)
	return o.view(c, e), nil
}

// DatasetLineage shows where a version's rows came from and whether that can
// still be demonstrated.
//
// The answer is MEASURED, not recalled: the plane asks the source the same
// bounded question again and compares it to the fingerprint taken when the
// version was built. Anything but exact agreement is reported as drift — the
// source is fed by a rollup that runs behind the events, so "it holds more now"
// is the ordinary case and it means re-running the spec would not reproduce this
// version. An admitted gap is actionable; an unfalsifiable claim is not.
//
// IT IS A PRICED, BOUNDED READ, because it is the same statement a
// materialisation is charged for: an exact distinct-count over up to 400 days of
// this org's feature surface. It takes the org's ONE source-scan slot, so a
// tenant looping it spends one scan and not a thousand; it counts against the
// plane's ceiling, so the fleet's warehouse is bounded too; and it runs under
// this plane's own deadline rather than the caller's patience.
//
// Example: {"name": "signups", "version": 1}
func (o ops) lineage(ctx context.Context, in *riskLineageIn) (*riskLineage, error) {
	c, err := o.p.who(ctx)
	if err != nil {
		return nil, err
	}
	if err := o.p.ready(ctx); err != nil {
		return nil, o.p.gap(err)
	}
	e, err := o.published(ctx, c, in.Name, in.Version)
	if err != nil {
		return nil, err
	}
	a, err := o.p.admit(ctx, c, e.Name, e.Version, lineageCost)
	if err != nil {
		return nil, err
	}
	defer o.p.release(c.key)

	// The plane's wall, not the client's: an op that answers in line still must
	// not hold a store connection for as long as a caller is willing to wait.
	ctx, cancel := context.WithTimeout(ctx, censusBudget)
	defer cancel()

	out, err := o.p.lineage(ctx, a, e)
	if err != nil {
		return nil, o.p.gap(err)
	}
	// Same string boundary as the materialize debit above: c.ledger is a carried
	// field, parsed here so the debit lands where the gate looked.
	o.p.bill.Meter(account.PayerOf("", c.ledger), c.project, "dataset", lineageCost, c.request, c.ip)
	return &out, nil
}

// ExportDataset reads a published version's rows back, one bounded page at a
// time, in the version's own stable row order.
//
// Only a published version can be exported. Rows written by an attempt that never
// completed are inert — no register row names them — and they are disposed of with
// the dataset.
//
// Example: {"name": "signups", "version": 1, "split": "train", "limit": 500}
func (o ops) export(ctx context.Context, in *riskExportIn) (*riskDatasetRows, error) {
	c, err := o.p.who(ctx)
	if err != nil {
		return nil, err
	}
	if err := o.p.ready(ctx); err != nil {
		return nil, o.p.gap(err)
	}
	e, err := o.published(ctx, c, in.Name, in.Version)
	if err != nil {
		return nil, err
	}
	// The split is resolved AT THE ENDPOINT, so an unknown one is the caller's 400 and
	// everything the read can still fail with is the store's 503. Rebuilding a
	// store failure as a bad request — which is what deriving one error from
	// another's text does — would tell a caller to fix their request while the
	// warehouse was down.
	if in.Split != "" {
		if _, ok := splitOf(in.Split); !ok {
			return nil, zip.ErrBadRequest("a split is train, val or test")
		}
	}
	limit := in.Limit
	if limit <= 0 || limit > page {
		limit = page
	}
	offset := max(in.Offset, 0)
	rows, err := o.p.rows(ctx, c.key, e.Name, e.Version, offset, limit, in.Split)
	if err != nil {
		return nil, o.p.gap(err)
	}
	out := &riskDatasetRows{
		Dataset: e.Name,
		Version: e.Version,
		Digest:  e.Digest,
		Dims:    e.Spec.Dims,
		Offset:  offset,
		Limit:   limit,
		Rows:    make([]riskDatasetRow, 0, len(rows)),
	}
	for _, r := range rows {
		out.Rows = append(out.Rows, riskDatasetRow{
			ID:      r.ID,
			Split:   splitName(r.Split),
			Kind:    r.Kind,
			Subject: r.Subject,
			At:      stamp(r.At),
			Point:   r.Point,
		})
	}
	return out, nil
}

// page is the largest export page, in ROWS. An export is a read of the same store
// every other tenant is using, so the page size is the plane's to set.
//
// It bounds the response BODY — at most [maxPageBytes] — only because every row in
// the rows table came through [representable] on the way in, so a row's subject is
// at most [maxSubjectBytes]. A page count over rows carrying caller-sized strings
// would bound the row count and nothing else. There is deliberately no second
// size check on this read path: the bound is enforced where rows ENTER, once.
const page = 5_000

// DeleteDataset disposes of one dataset and every version of it: the rows are
// dropped and the register is marked with what went.
//
// This is the ONLY expiry in this plane. Neither table carries a TTL, deliberately:
// a table TTL is a fleet-wide clock no tenant can hold longer or shorten, which is
// the opposite of a retention decision belonging to the tenant whose records they
// are. The drop is a partition drop on (org, dataset), so the tenant is the first
// component of the thing being dropped and a disposal cannot be spelled across one.
//
// The BYTES are what goes. The register keeps one `disposed` row per version — the
// name, the number, the spec, the digest and who disposed of it when — for two
// reasons: a retention obligation is answered by a record of the deletion, not by
// silence; and version numbers must stay monotone, so that after `orders` is
// disposed of and declared again the next version is 4 and not 1. A number that
// could be reused would make every citation of `orders v3` ambiguous forever.
//
// It is not reversible and there is no soft state in between. A version a model
// cited has no rows once this returns, and every read of it says so.
//
// Example: {"name": "signups"}
func (o ops) dispose(ctx context.Context, in *riskDatasetDisposeIn) (*riskDatasetDisposal, error) {
	c, err := o.p.who(ctx)
	if err != nil {
		return nil, err
	}
	if err := o.p.ready(ctx); err != nil {
		return nil, o.p.gap(err)
	}
	es, err := o.p.versions(ctx, c.key, in.Name)
	if err != nil {
		return nil, o.p.gap(err)
	}
	if len(es) == 0 {
		return nil, zip.ErrNotFound("no such dataset")
	}
	if held, running := o.p.running(c.key); running && held.name == es[0].Name {
		// Refuse rather than race the job: dropping the partition under a running
		// materialisation would leave rows written after the drop under a version
		// the register has already marked disposed.
		return nil, zip.ErrConflict("a materialisation of this dataset is running; it must finish before the dataset can be disposed of")
	}
	out := &riskDatasetDisposal{Dataset: es[0].Name, Versions: len(es)}
	for _, e := range es {
		out.Rows += e.Counts.Rows
	}
	// A repeat disposal is ADMITTED, not refused. The two steps can fail apart —
	// the bytes drop and then the mark — and a caller told "already disposed"
	// would have no way to finish the half that did not happen. Both steps are
	// idempotent, and the mark keeps the instant it first recorded.
	if err := o.p.dispose(ctx, c.key, es[0].Name, es, c.by); err != nil {
		return nil, o.p.gap(err)
	}
	o.p.log.Info("dataset disposed",
		"tenant", c.key.String(), "dataset", out.Dataset, "versions", out.Versions, "rows", out.Rows, "by", c.by)
	return out, nil
}

// ── projection ───────────────────────────────────────────────────────────────

// published resolves the version an op names, and refuses anything that is not
// published. Zero takes the newest READY version, not the newest version: a
// caller asking for "the dataset" means the one with bytes.
func (o ops) published(ctx context.Context, c caller, name string, version int) (entry, error) {
	if version < 0 {
		return entry{}, zip.ErrBadRequest("a version is a positive number, or zero for the newest published one")
	}
	if version > 0 {
		e, ok, err := o.p.version(ctx, c.key, name, version)
		if err != nil {
			return entry{}, o.p.gap(err)
		}
		if !ok {
			return entry{}, zip.ErrNotFound("no such dataset version")
		}
		if e.Status != statusReady {
			return entry{}, zip.ErrConflict(refusalFor(e))
		}
		return e, nil
	}
	es, err := o.p.versions(ctx, c.key, name)
	if err != nil {
		return entry{}, o.p.gap(err)
	}
	if len(es) == 0 {
		return entry{}, zip.ErrNotFound("no such dataset")
	}
	if es[0].Status == statusDisposed {
		// A disposed dataset is not one that has no published version YET. Saying so
		// would send an operator looking for a materialisation that is never coming.
		return entry{}, zip.ErrConflict("this dataset was disposed of; its rows are gone and only the record of them remains")
	}
	for _, e := range es {
		if e.Status == statusReady {
			return e, nil
		}
	}
	return entry{}, zip.ErrConflict("this dataset has no published version yet")
}

// view projects a register entry onto the wire. It is the ONE projection, so the
// list, the description and the two mutations cannot describe a version
// differently.
func (o ops) view(c caller, e entry) *riskDataset {
	held, running := o.p.running(c.key)
	return &riskDataset{
		Name:    e.Name,
		Version: e.Version,
		At:      stamp(e.At),
		By:      e.By,
		Status:  e.Status,
		Running: running && held.name == e.Name && held.version == e.Version,
		Refusal: e.Refusal,
		Digest:  e.Digest,
		Spec: riskDatasetSpec{
			Name:    e.Spec.Name,
			Kind:    e.Spec.Kind,
			Dims:    e.Spec.Dims,
			From:    e.Spec.From,
			To:      e.Spec.To,
			Horizon: e.Spec.Horizon,
			Cuts:    e.Spec.Cuts,
			Seed:    e.Spec.Seed,
			Rows:    e.Spec.Rows,
		},
		Counts: riskSplitCounts{
			Rows:         e.Counts.Rows,
			Train:        e.Counts.Train,
			Val:          e.Counts.Val,
			Test:         e.Counts.Test,
			Subjects:     e.Counts.Subjects,
			Judged:       e.Counts.Judged,
			Productive:   e.Counts.Productive,
			Unproductive: e.Counts.Unproductive,
		},
		Share:     e.Share,
		Truncated: e.Truncated,
		Oversize:  e.Oversize,
	}
}
