package dataset

// materialize.go turns a declared version into bytes, exactly once, and answers
// where those bytes came from.
//
// THE LIFECYCLE, AND WHY IT HAS NO REPAIR STEP.
//
//	declared      the spec exists and is numbered; no rows.
//	materializing the attempt has been RECORDED before a single row is written.
//	ready         the rows are in and the digest is computed. Terminal.
//	refused       the attempt failed, with the reason. Terminal.
//
// A version is admitted to materialisation only from `declared`, and the move to
// `materializing` is written to the store BEFORE the first row. So a process that
// dies mid-job leaves the version in `materializing` forever, and that is the
// correct outcome, not a state needing a sweeper: the attempt is on record, the
// version can never be re-attempted (which would union two runs' rows under one
// number and make the digest a lie), and the next attempt is a NEW version —
// which is what a second run over a moving source honestly is.
//
// Nothing here can leave a HALF-published version looking whole: `ready` is
// written only after every row has landed and the digest has been computed over
// them. Every other outcome is `materializing` or `refused`, and neither is
// readable as a dataset.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/apps/tenant"
	"github.com/zap-proto/zip"
)

// jobBudget is the hard wall on one materialisation. It bounds the warehouse
// work a single request can cause, which — with one job per tenant — bounds what
// the whole plane can cause. A job that hits it is refused with the reason,
// rather than left running against a store other tenants are also using.
const jobBudget = 15 * time.Minute

// declareCost is what declaring a version costs at the meter, in cents. Declaring
// is a register write and is free; MATERIALISING is the priced act, because it is
// the one that spends the warehouse.
const declareCost int64 = 0

// materializeCost is the fee charged for one materialisation, in cents. It is a
// flat fee for admission to a bounded job — the bound is the product, so the price
// is for the bound and not for the rows.
const materializeCost int64 = 10

// declare mints the next version of a dataset from a normalised spec.
//
// Versions are MONOTONE and never reused: the number is one past the highest this
// tenant holds for the name, whatever happened to the versions before it. A
// number that could be reused would make "version 3 of orders" ambiguous across
// time, and a citation that is ambiguous across time is not a citation.
func (p *plane) declare(ctx context.Context, c caller, s spec) (entry, error) {
	if err := p.ready(ctx); err != nil {
		return entry{}, err
	}
	held, err := p.versions(ctx, c.key, s.Name)
	if err != nil {
		return entry{}, err
	}
	next := 1
	if len(held) > 0 {
		next = held[0].Version + 1
	} else {
		// A NEW name, so the tenant's name budget applies. Both tables are
		// partitioned by (org, name) and partition count is a resource the whole
		// store shares, so an unbounded number of names is a way to degrade every
		// other tenant from inside one tenant's own quota.
		all, err := p.names(ctx, c.key)
		if err != nil {
			return entry{}, err
		}
		seen := map[string]bool{}
		for _, e := range all {
			seen[e.Name] = true
		}
		if len(seen) >= maxNames {
			return entry{}, zip.ErrBadRequest(fmt.Sprintf("this org already holds %d datasets, which is the limit; dispose of one before declaring another", maxNames))
		}
	}
	if next > maxVersions {
		return entry{}, zip.ErrBadRequest(fmt.Sprintf("%q has reached %d versions, which is the limit; declare a new dataset", s.Name, maxVersions))
	}

	e := entry{
		Name:    s.Name,
		Version: next,
		At:      time.Now().UTC(),
		By:      c.by,
		Status:  statusDeclared,
		Spec:    s.record(),
		Horizon: int(s.Horizon / (24 * time.Hour)),
	}
	if err := p.put(ctx, c.key, e); err != nil {
		return entry{}, err
	}
	return e, nil
}

// start admits one materialisation: it checks the version is admissible, takes
// the tenant's single slot, records the attempt, and hands the work to a job.
//
// It answers as soon as the attempt is ON RECORD, never when the work is done. A
// materialisation is a bounded warehouse scan and holding a request open for it
// would make the timeout of an HTTP client the timeout of a data plane.
func (p *plane) start(ctx context.Context, c caller, name string) (entry, error) {
	if err := p.ready(ctx); err != nil {
		return entry{}, err
	}
	e, ok, err := p.latest(ctx, c.key, name)
	if err != nil {
		return entry{}, err
	}
	if !ok {
		return entry{}, zip.ErrNotFound("no such dataset")
	}
	if e.Status != statusDeclared {
		// IMMUTABILITY, at the door. `ready` and `refused` are terminal, and a
		// version already attempted can never be attempted again — re-running it
		// would write a second run's rows under a number the first run's digest
		// already describes.
		return entry{}, zip.ErrConflict(refusalFor(e))
	}
	if _, err := p.claim(c.key, e.Name, e.Version); err != nil {
		return entry{}, err
	}

	e.Status = statusMaterialize
	e.At = time.Now().UTC()
	e.By = c.by
	if err := p.put(ctx, c.key, e); err != nil {
		p.release(c.key)
		return entry{}, err
	}
	go p.run(c.key, e)
	return e, nil
}

// refusalFor says why a version cannot be materialised, in the version's own
// terms. Each answer is a different fact and reporting one for another is how an
// operator ends up retrying something that will never succeed.
func refusalFor(e entry) string {
	switch e.Status {
	case statusReady:
		return fmt.Sprintf("version %d is published and immutable; declare a new version", e.Version)
	case statusRefused:
		return fmt.Sprintf("version %d was refused (%s); declare a new version", e.Version, e.Refusal)
	case statusMaterialize:
		return fmt.Sprintf("version %d has already been attempted and did not complete; declare a new version rather than mixing two runs under one number", e.Version)
	}
	return fmt.Sprintf("version %d is not declared", e.Version)
}

// run is the job. It owns the whole materialisation and it always ends the
// version in a terminal state: `ready` when every row landed and the digest was
// computed over them, `refused` with the reason otherwise.
//
// It takes no request context. The caller's request is over — deliberately — so
// the job's life is the budget below and nothing else, and a client that hung up
// neither cancels the work nor keeps it alive.
func (p *plane) run(k tenant.Key, e entry) {
	defer p.release(k)
	ctx, cancel := context.WithTimeout(context.Background(), jobBudget)
	defer cancel()

	done, err := p.build(ctx, k, e)
	if err != nil {
		e.Status = statusRefused
		e.Refusal = p.reason(err)
		e.At = time.Now().UTC()
		if err := p.put(ctx, k, e); err != nil {
			// The refusal itself could not be recorded. Say so loudly: the version
			// now reads as an attempt that did not complete, which is TRUE, but the
			// reason only exists in this line.
			p.log.Error("dataset: a refusal could not be recorded",
				"tenant", k.String(), "dataset", e.Name, "version", e.Version, "err", err)
		}
		p.log.Warn("dataset: materialisation refused",
			"tenant", k.String(), "dataset", e.Name, "version", e.Version, "reason", e.Refusal)
		return
	}
	if err := p.put(ctx, k, done); err != nil {
		// The rows are in the store but the version was never published, so it
		// stays `materializing` — unreadable, and a new version is the way
		// forward. Fail secure: an unpublished version is inert, a published one
		// whose counts were never written would be a dataset nobody can check.
		p.log.Error("dataset: rows landed but the version was not published",
			"tenant", k.String(), "dataset", e.Name, "version", e.Version, "err", err)
		return
	}
	p.log.Info("dataset materialised",
		"tenant", k.String(), "dataset", done.Name, "version", done.Version,
		"rows", done.Counts.Rows, "train", done.Counts.Train, "val", done.Counts.Val,
		"test", done.Counts.Test, "digest", done.Digest)
}

// build does the work: measure the window, choose the membership share, read the
// facts, cut them into splits, fingerprint them, and write them.
//
// Split assignment and the digest are computed HERE, in Go, over rows this
// process holds — not pushed into the store. That is the R6 trade taken
// deliberately: pushing them down would put the definition of a split in SQL,
// where no test can reach it and where a second definition would eventually
// appear. The cost is that the rows pass through memory, which is exactly what
// the row cap bounds.
func (p *plane) build(ctx context.Context, k tenant.Key, e entry) (entry, error) {
	s, err := e.Spec.spec()
	if err != nil {
		return entry{}, err
	}

	// THE MATURITY HORIZON, applied to the window's end. A row is admitted only
	// once it has aged past the horizon, because a label for it could not have
	// existed before then: a chargeback lands 30 to 120 days after the transaction
	// it condemns, and a training set built without the wait knows the future.
	// Offline it scores beautifully. Online it is worthless.
	until := s.To
	if mature := time.Now().UTC().Add(-s.Horizon); mature.Before(until) {
		until = mature
	}
	if !until.After(s.From) {
		return entry{}, zip.ErrBadRequest(fmt.Sprintf("no part of the window has aged past the %d-day maturity horizon yet", int(s.Horizon.Hours()/24)))
	}

	seen, err := p.census(ctx, k, s, until)
	if err != nil {
		return entry{}, err
	}
	if seen.Rows == 0 {
		return entry{}, zip.ErrBadRequest("the source holds nothing for this org in this window; there is no dataset to make")
	}
	share := share(seen.Rows, s.Rows)

	facts, hitLimit, err := p.facts(ctx, k, s, until, share)
	if err != nil {
		return entry{}, err
	}
	facts = trim(facts, hitLimit)
	if len(facts) == 0 {
		return entry{}, zip.ErrBadRequest("every row in this window was excluded, so the version would be empty")
	}

	rows := cut(facts, s.Cuts)
	e.Counts = count(rows)
	e.Digest = digest(s, e.Version, rows)
	e.Share = share
	e.Truncated = hitLimit
	e.Status = statusReady
	e.Refusal = ""
	e.At = time.Now().UTC()
	e.Source = fingerprint(origin{
		Table:     sourceTable,
		From:      stamp(s.From),
		To:        stamp(until),
		Rows:      seen.Rows,
		Subjects:  seen.Subjects,
		First:     stamp(seen.First),
		Last:      stamp(seen.Last),
		Share:     share,
		Retention: p.retention(ctx),
	})

	if err := p.insertRows(ctx, k, e.Name, e.Version, rows); err != nil {
		return entry{}, err
	}
	return e, nil
}

// ── lineage ──────────────────────────────────────────────────────────────────

// origin is what the source looked like at materialisation time. It is recorded
// on the manifest so the claim "these rows came from that window of that plane"
// can be CHECKED later rather than believed.
type origin struct {
	Table     string `json:"table"`
	From      string `json:"from"`
	To        string `json:"to"`
	Rows      int    `json:"rows"`
	Subjects  int    `json:"subjects"`
	First     string `json:"first"`
	Last      string `json:"last"`
	Share     int    `json:"share"`
	Retention string `json:"retention"`
}

func fingerprint(o origin) string {
	b, _ := json.Marshal(o)
	return string(b)
}

func readOrigin(s string) (origin, error) {
	var o origin
	if s == "" {
		return origin{}, fmt.Errorf("this version records no source, so its lineage cannot be shown")
	}
	if err := json.Unmarshal([]byte(s), &o); err != nil {
		return origin{}, fmt.Errorf("the recorded source is unreadable: %w", err)
	}
	return o, nil
}

// lineage answers where a version's rows came from AND whether that can still be
// demonstrated — by asking the source the same question again, now.
//
// Reproducible is MEASURED, never asserted. When the source's retention has since
// dropped the window's older half, the dataset still holds its own rows and this
// says plainly that they can no longer be re-derived. A lineage claim a plane
// cannot demonstrate is worse than an admitted gap: the gap is actionable and the
// claim is not falsifiable.
func (p *plane) lineage(ctx context.Context, k tenant.Key, e entry) (mlLineage, error) {
	o, err := readOrigin(e.Source)
	if err != nil {
		return mlLineage{}, err
	}
	s, err := e.Spec.spec()
	if err != nil {
		return mlLineage{}, err
	}
	out := mlLineage{
		Dataset:   e.Name,
		Version:   e.Version,
		Source:    o.Table,
		From:      o.From,
		To:        o.To,
		Rows:      o.Rows,
		Subjects:  o.Subjects,
		Share:     o.Share,
		Digest:    e.Digest,
		Retention: o.Retention,
	}

	until, err := instant("to", o.To)
	if err != nil {
		return mlLineage{}, err
	}
	now, err := p.census(ctx, k, s, until)
	if err != nil {
		return mlLineage{}, err
	}
	out.Holds = now.Rows
	recorded, err := instant("first", o.First)
	if err != nil {
		return mlLineage{}, err
	}
	switch {
	case now.Rows == 0:
		out.Refusal = "the source no longer holds anything for this window, so these rows cannot be re-derived"
	case now.First.After(recorded):
		out.Refusal = fmt.Sprintf("the source now starts at %s, later than the %s this version was built from, so its older rows have expired",
			stamp(now.First), o.First)
	case now.Rows < o.Rows:
		out.Refusal = fmt.Sprintf("the source now holds %d rows for this window where this version was built from %d", now.Rows, o.Rows)
	default:
		out.Reproducible = true
	}
	return out, nil
}
