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
// work a single request can cause, which — with one scan per tenant — bounds what
// the whole plane can cause. A job that hits it is refused with the reason,
// rather than left running against a store other tenants are also using.
const jobBudget = 15 * time.Minute

// recordBudget is the wall on the WRITE THAT ENDS A JOB, and it is a second
// budget on purpose.
//
// Writing the outcome is not the work; it is the RECORD of the work, and giving
// the two one deadline means the outcome of a job that ran out of time can never
// be written. That is not a rare corner: it is precisely the case the refusal
// exists for. The job hits [jobBudget], build fails with DeadlineExceeded, and
// the write of `refused` then runs on the very context that just expired — the
// driver returns before sending anything, nothing lands, and the version sits in
// `materializing` with an EMPTY refusal forever, telling the operator only that
// "the attempt did not complete" and never why. [plane.record] is the ONE writer
// a job uses, and it opens its own context, so that shape cannot be written here
// again.
const recordBudget = 30 * time.Second

// censusBudget is the wall on a source scan taken on the REQUEST path — today
// exactly one, the lineage measurement. A materialisation answers 202 and runs
// under [jobBudget] precisely so a client's patience is never a data plane's
// deadline; an op that must answer in line cannot do that, so it gets a wall short
// enough that a held store connection is bounded by the plane and not by the
// caller's socket.
const censusBudget = 60 * time.Second

// declareCost is what declaring a version costs at the meter, in cents. Declaring
// is a register write and is free; READING THE SOURCE is the priced act, because
// it is the one that spends the warehouse.
const declareCost int64 = 0

// materializeCost is the fee charged for one materialisation, in cents. It is a
// flat fee for admission to a bounded job — the bound is the product, so the price
// is for the bound and not for the rows.
const materializeCost int64 = 10

// lineageCost is the fee for one lineage answer, in cents.
//
// Lineage RE-RUNS the census a materialisation is charged for: the same exact
// distinct-count, over the same window of the same table, for the same tenant. It
// is priced BELOW a materialisation because it stops there — it measures and
// writes nothing — and above zero because a free re-run of the plane's most
// expensive statement is a free warehouse scan with a verb in front of it.
const lineageCost int64 = 2

// declare mints the next version of a dataset from a normalised spec.
//
// Versions are MONOTONE and never reused: the number is one past the highest this
// tenant holds for the name, whatever happened to the versions before it —
// INCLUDING disposal, which is why disposal marks the register rather than
// dropping it. A number that could be reused would make "version 3 of orders"
// ambiguous across time, and a citation that is ambiguous across time is not a
// citation.
//
// A NAME, ONCE DECLARED, IS ONE OF THE ORG'S [maxNames] FOR GOOD. Disposal
// reclaims the bytes, not the name: the register keeps the record of what was
// disposed and the number it reached, and re-declaring that name CONTINUES its
// numbering. The bound is unchanged either way — before, it counted the names a
// tenant held at once and the partitions they occupied; now it counts both, which
// are the same 64 partitions, and the tenant keeps every name it has ever used.
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
// the tenant's single scan slot at the priced gate, records the attempt, and
// hands the work to a job.
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
		// IMMUTABILITY, at the door. `ready`, `refused` and `disposed` are terminal,
		// and a version already attempted can never be attempted again — re-running
		// it would write a second run's rows under a number the first run's digest
		// already describes.
		return entry{}, zip.ErrConflict(refusalFor(e))
	}
	a, err := p.admit(ctx, c, e.Name, e.Version, materializeCost)
	if err != nil {
		return entry{}, err
	}

	e.Status = statusMaterialize
	e.At = time.Now().UTC()
	e.By = c.by
	if err := p.put(ctx, c.key, e); err != nil {
		p.release(c.key)
		return entry{}, err
	}
	go p.run(a, e, jobBudget)
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
	case statusDisposed:
		return fmt.Sprintf("version %d was disposed of and its rows are gone; declare a new version", e.Version)
	}
	return fmt.Sprintf("version %d is not declared", e.Version)
}

// record writes a version's TERMINAL state, on a context of its OWN.
//
// It is the only writer a job uses, and that is the whole point: a job cannot
// record its outcome on the deadline of the work it is reporting on, because the
// commonest reason there is an outcome to report is that the deadline expired.
// The context is fresh and its wall is [recordBudget], so a build that died at
// fifteen minutes still gets thirty seconds to say so.
func (p *plane) record(k tenant.Key, e entry) error {
	ctx, cancel := context.WithTimeout(context.Background(), recordBudget)
	defer cancel()
	return p.put(ctx, k, e)
}

// run is the job. It owns the whole materialisation and it always ends the
// version in a terminal state: `ready` when every row landed and the digest was
// computed over them, `refused` with the reason otherwise.
//
// It takes no request context. The caller's request is over — deliberately — so
// the work's life is `budget` and nothing else, and a client that hung up neither
// cancels the work nor keeps it alive. The budget is a parameter and not a
// constant read from inside because the wall is a fact about THIS job: stated at
// the one call site that starts one, and settable by a test that needs a job
// which has already run out of time.
//
// TWO CONTEXTS, deliberately. The work's ends when the work does — `cancel` is
// called before the outcome is written, not deferred past it — and the record of
// the work gets its own ([plane.record]). Sharing one is the defect that makes a
// timed-out materialisation unable to record that it timed out.
func (p *plane) run(a scan, e entry, budget time.Duration) {
	defer p.release(a.k)
	work, cancel := context.WithTimeout(context.Background(), budget)

	done, err := p.build(work, a, e)
	cancel() // the work is over, whichever way it went; what follows is the RECORD.

	if err != nil {
		e.Status = statusRefused
		e.Refusal = p.reason(err)
		e.At = time.Now().UTC()
		if err := p.record(a.k, e); err != nil {
			// The refusal itself could not be recorded. Say so loudly: the version
			// now reads as an attempt that did not complete, which is TRUE, but the
			// reason only exists in this line.
			p.log.Error("dataset: a refusal could not be recorded",
				"tenant", a.k.String(), "dataset", e.Name, "version", e.Version, "err", err)
		}
		p.log.Warn("dataset: materialisation refused",
			"tenant", a.k.String(), "dataset", e.Name, "version", e.Version, "reason", e.Refusal)
		return
	}
	if err := p.record(a.k, done); err != nil {
		// The rows are in the store but the version was never published, so it
		// stays `materializing` — unreadable, and a new version is the way
		// forward. Fail secure: an unpublished version is inert, a published one
		// whose counts were never written would be a dataset nobody can check.
		p.log.Error("dataset: rows landed but the version was not published",
			"tenant", a.k.String(), "dataset", e.Name, "version", e.Version, "err", err)
		return
	}
	p.log.Info("dataset materialised",
		"tenant", a.k.String(), "dataset", done.Name, "version", done.Version,
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
func (p *plane) build(ctx context.Context, a scan, e entry) (entry, error) {
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

	seen, err := p.census(ctx, a, s, until)
	if err != nil {
		return entry{}, err
	}
	if seen.Rows == 0 {
		return entry{}, zip.ErrBadRequest("the source holds nothing for this org in this window; there is no dataset to make")
	}
	share := share(seen.Rows, s.Rows)

	facts, hitLimit, err := p.facts(ctx, a, s, until, share)
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

	if err := p.insertRows(ctx, a.k, e.Name, e.Version, rows); err != nil {
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
// Reproducible is MEASURED, never asserted, and the measurement is EXACT
// AGREEMENT. The source is a SummingMergeTree fed by a rollup that runs behind the
// events, so a bucket inside a closed window keeps growing for as long as late
// events keep arriving: "the source now holds MORE than this version was built
// from" is the normal case, not an anomaly, and re-running the spec over it would
// produce different rows and therefore a different digest. Certifying that as
// re-derivable would put a false claim in the one place the design says a claim
// must be falsifiable — so any difference at all, in either direction, in the
// count, the subjects or the window's own extent, is reported as the drift it is.
//
// The recorded extremes are compared for EQUALITY rather than for expiry. A
// source that now starts later has expired its older rows; one that now starts
// EARLIER has been backfilled. Both mean the window no longer holds what this
// version was built from, and only the first was noticed before.
func (p *plane) lineage(ctx context.Context, a scan, e entry) (mlLineage, error) {
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
	now, err := p.census(ctx, a, s, until)
	if err != nil {
		return mlLineage{}, err
	}
	out.Holds = now.Rows
	out.Refusal = drift(o, now)
	out.Reproducible = out.Refusal == ""
	return out, nil
}

// drift names the first way the source no longer holds what a version was built
// from, or "" when it holds exactly that. It is a total comparison of the four
// measurements the manifest records, so "reproducible" means all four agree and
// nothing else.
func drift(o origin, now census) string {
	switch {
	case now.Rows == 0:
		return "the source no longer holds anything for this window, so these rows cannot be re-derived"
	case now.Rows != o.Rows:
		return fmt.Sprintf("the source now holds %d rows for this window where this version was built from %d, so re-running its spec would not reproduce it",
			now.Rows, o.Rows)
	case now.Subjects != o.Subjects:
		return fmt.Sprintf("the source now holds %d subjects for this window where this version was built from %d",
			now.Subjects, o.Subjects)
	case stamp(now.First) != o.First:
		return fmt.Sprintf("the source now starts at %s where this version was built from %s", stamp(now.First), o.First)
	case stamp(now.Last) != o.Last:
		return fmt.Sprintf("the source now ends at %s where this version was built from %s", stamp(now.Last), o.Last)
	}
	return ""
}
