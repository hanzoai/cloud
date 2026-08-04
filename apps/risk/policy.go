package risk

// policy.go — the DECISION REGIME, versioned, on the tenant's own shelf.
//
// # Why this is its own record and not a field on the model
//
// A model's learned state and the policy it decides under are two different
// facts with two different lifetimes, and they used to share one row and one
// writer. That writer's guard is a fact about the STATE — it declines to write
// while the snapshot holds no learned mass, because there is no state to lose —
// so an organisation that stated a policy before its model had learned anything
// had that policy silently discarded. This app deploys Recreate at ONE replica,
// so the very next rollout rebuilt it from [defaultConfig], which is SHADOW: the
// organisation had been told live=true, and its model was deciding nothing. No
// error, no log, nothing to alert on. Decoupling the two is the fix, and the
// versioned record is what the fix is made of.
//
// # A regime is a VALUE; a version names it in one organisation's history
//
// Two regimes with the same numbers are the same regime. A version is therefore
// minted only when the regime CHANGES, which makes a version mean "the Nth
// distinct policy this organisation adopted" rather than "the Nth time somebody
// pressed save" — and makes a client that restates its config on every deploy
// free, rather than the cheapest way to fill a disk.
//
// # Why an adverse decision needs it
//
// A score is defensible only against the regime that produced its cut. The score
// says where the event sat; the causes say which coordinates carried it; the cut
// says what would have been alerted on — and the cut is derived from the stated
// appetite, which is policy. Without a version, a restated appetite makes every
// earlier decision unreconstructible: the cut it was measured against no longer
// exists anywhere. Every score therefore CITES the version in force, and every
// version is readable back for as long as it is retained.
//
// # The bounds, in the dimension that binds
//
// The row is FIXED WIDTH — three numbers, a flag and two bounded identifiers —
// so a version count IS a byte bound ([maxPolicyRowBytes], measured against a
// real file by TestPolicy_BoundsArePublishedInTheDimensionThatBinds). Two bounds
// hold, both per tenant and both on the tenant's OWN table, so neither is a cap
// one organisation's traffic can spend on another's behalf:
//
//	RATE   at most [maxPolicyPerWindow] distinct regimes per rolling
//	       [policyWindow], REFUSED past it with the tenant's own bound named.
//	       This is what makes growth a function of TIME rather than of caller
//	       volume — the property that matters, because an identical restatement
//	       mints nothing and so cannot be looped.
//	TOTAL  at most [policyVersions] retained, derived from [policyBudget] BYTES.
//	       At the ceiling the OLDEST is disposed of, and the number disposed of
//	       is REPORTED ([riskPolicyOut.Disposed]) — a retention that binds is a
//	       fact an operator must be able to read, not a silence.
//
// The pruned count is DERIVED, never stored: versions are contiguous from 1, so
// the lowest surviving version minus one IS how many were disposed of. A figure
// that cannot drift from the thing it describes.

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/zap-proto/zip"
)

// regime is the decision policy in force for one organisation: how much of its
// own stream it is willing to review, how much of the rest it samples for the
// below-the-line measurement, and whether its model DECIDES or only observes.
//
// It is the subset of [anomaly.Config] the tenant states, and it is the whole of
// it: everything else on that Config — the seed, the geometry — describes the
// store the engine planted and belongs to the learned state, not to policy.
type regime struct {
	// Review is the share of its own stream the organisation is willing to look
	// at. The cut is derived from it as a quantile of scores actually observed.
	Review float64
	// Sample is the share of below-the-line events retained for review.
	Sample float64
	// Live is whether the model may change an outcome. False is shadow, and it is
	// the default for a model nobody has reviewed yet.
	Live bool
}

// regimeOf reads the regime out of a Config. It is the one direction that
// projection is written, so the two cannot drift apart.
func regimeOf(c anomaly.Config) regime {
	return regime{Review: c.Appetite.Review, Sample: c.Appetite.Sample, Live: !c.Shadow}
}

// applyTo writes the regime onto a Config, leaving every other field — seed and
// geometry above all — exactly as it was. It is the ONLY writer of those three
// fields outside the engine, so "what policy is in force" has one answer.
func (r regime) applyTo(c anomaly.Config) anomaly.Config {
	c.Appetite.Review, c.Appetite.Sample, c.Shadow = r.Review, r.Sample, !r.Live
	return c
}

// enacted is one regime as it entered force, and it is immutable once written.
type enacted struct {
	// Version names this regime in THIS organisation's history, from 1, contiguous
	// until retention disposes of the oldest.
	Version int
	Regime  regime
	// At is the server clock when it entered force. The tenant does not supply it:
	// an audit record whose date the audited party chose is not an audit record.
	At time.Time
	// By is the identity that stated it, stamped from the validated principal and
	// never from a body — an attributable record whose attribution the caller
	// chose is not attributable.
	By string
}

// decided is one verdict together with the version of the regime it was reached
// under. The two travel as ONE value because a score is defensible only against
// the regime that produced its cut: the score says where the event sat, the
// causes say which coordinates carried it, and the cut says what would have been
// alerted on — and the cut is derived from the stated appetite, which is policy.
// Nothing in this app may hold a verdict without the regime it came from, so the
// pair is the type rather than a convention.
type decided struct {
	A anomaly.Assessment
	// Version is the policy version in force when the verdict was reached. Zero
	// means the organisation has never stated a regime, so the verdict was reached
	// under the default posture — a fact the score reports rather than hides.
	Version int
	// Shape is the model space the verdict was reached in: the feature inventory in
	// order and the detector's geometry parameters. It travels with the verdict for
	// exactly the reason Version does — a score is defensible only against the model
	// that produced it, and the shape is what says which model space that was.
	//
	// It is the SHAPE and deliberately not an address of the learned state. The
	// masses at the instant of a score are in-process counters somewhere between two
	// published values (address.go), so naming a published value here would be a
	// claim that value produced this score, which is false for every score but the
	// one taken the instant after a publication. What IS true is stated: the space it
	// ran in, the regime that set its cut, and the event's own time — and the value
	// history's own clock brackets it between two values from there.
	Shape string
}

// regimeNow is the policy version an organisation's model is deciding under,
// read off the RESIDENCY and not the shelf.
//
// The residency is the right source: the version in force is a property of the
// model that would answer the next score, and a fresh disk read could disagree
// with it — during an enact, or for a tenant whose recorded regime could not be
// rebuilt and which is therefore correctly running the default posture while its
// shelf still holds the version it could not honour.
func (p *plane) regimeNow(t tenant) (int, error) {
	r, err := p.resident(t)
	if err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pol, nil
}

// policyWindow is the rolling window the rate bound is measured over.
const policyWindow = 24 * time.Hour

// maxPolicyPerWindow is how many DISTINCT regimes one organisation may adopt per
// [policyWindow]. A policy change is a deliberate governance act; twenty-four is
// one an hour, which is far past any real review cadence and still turns disk
// growth into a function of time rather than of request volume.
const maxPolicyPerWindow = 24

// maxPolicyRowBytes is what ONE retained version costs on the tenant's own
// shelf, worst case, including its primary-key index and SQLite's own per-row
// overhead. Every term is a BOUND and not a hope: the row carries no
// caller-sized value at all beyond the two identifiers, both already bounded.
const maxPolicyRowBytes = maxTenant + maxField + 5*8 + (maxTenant + 8) + 256

// policyBudget is what ONE organisation's policy history may cost ON DISK.
// Bytes, not versions, for the same reason [recordBudget] is: every org's shelf
// lives on one volume, so a per-tenant history bounded only in rows is one
// tenant filling the disk another tenant's model is stored on.
const policyBudget = 256 << 10

// policyVersions is that budget in versions. It is the bound the disposal
// enforces and the bound the read reports against, because they must be the same
// number.
const policyVersions = policyBudget / maxPolicyRowBytes

// policyDDL is the history table. It lives on the tenant's own shelf, so the
// tenant is not a column that has to be remembered in a predicate — it is the
// FILE — and it is still carried on every row, because the shelf is keyed on the
// bare org and the qualified key is what distinguishes two brands' identically
// named organisations.
//
// There is no UPDATE and no DELETE against it anywhere except the disposal below,
// and TestPolicy_IsAppendOnly holds that closed.
const policyDDL = `CREATE TABLE IF NOT EXISTS policy (
	tenant  TEXT    NOT NULL,
	version INTEGER NOT NULL,
	review  REAL    NOT NULL,
	sample  REAL    NOT NULL,
	live    INTEGER NOT NULL,
	by      TEXT    NOT NULL,
	at      INTEGER NOT NULL,
	PRIMARY KEY (tenant, version)
)`

// errPolicyRate is the named refusal at the rate bound. It carries the
// organisation's OWN bound, because a refusal that does not say what was
// exceeded is indistinguishable from a fault.
var errPolicyRate = zip.Errorf(429,
	"at most %d distinct policy changes per %s; the regime in force is unchanged",
	maxPolicyPerWindow, policyWindow)

// inForce reads the regime an organisation is currently deciding under.
//
// It returns held=false for an organisation that has never stated one, which is
// a DIFFERENT fact from "stated the default" and is why it is not defaulted here:
// the caller decides what an unstated policy means, and [plane.open] adopts the
// legacy config as version 1 rather than treating the absence as a fresh default.
func (p *plane) inForce(t tenant) (enacted, bool, error) {
	sh, err := p.for_(t)
	if err != nil {
		return enacted{}, false, err
	}
	var (
		e    enacted
		live int
		at   int64
	)
	err = sh.db.QueryRow(
		`SELECT version, review, sample, live, by, at FROM policy
		 WHERE tenant = ? ORDER BY version DESC LIMIT 1`, string(t),
	).Scan(&e.Version, &e.Regime.Review, &e.Regime.Sample, &live, &e.By, &at)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return enacted{}, false, nil
	case err != nil:
		return enacted{}, false, fmt.Errorf("risk: read policy: %w", err)
	}
	e.Regime.Live, e.At = live == 1, time.Unix(at, 0).UTC()
	return e, true, nil
}

// enact records a regime and returns it as it entered force.
//
// It is IDEMPOTENT ON THE REGIME: a restatement identical to the one in force
// mints no version and answers with the version already in force, minted=false.
// That is what makes a version a distinct adopted policy rather than a count of
// saves, and it is also the reason the rate bound below is not something a
// well-behaved client can trip.
//
// The write is the COMMIT POINT of a policy change. Nothing in memory moves until
// this returns, so a policy that could not be written down is REFUSED rather than
// answered from state the next rollout will silently undo.
func (p *plane) enact(t tenant, r regime, by string, now time.Time) (enacted, bool, error) {
	by = strings.TrimSpace(by)
	if by == "" {
		// Every version names who stated it. There is no anonymous policy change:
		// the record exists to be defended, and one with no author cannot be.
		return enacted{}, false, zip.ErrForbidden("no validated principal, so no policy change is attributable")
	}
	if len(by) > maxField {
		return enacted{}, false, zip.Errorf(413,
			"the stating identity is %d bytes, over the %d-byte bound every retained version is a multiple of",
			len(by), maxField)
	}
	if err := admitRegime(r); err != nil {
		return enacted{}, false, err
	}
	sh, err := p.for_(t)
	if err != nil {
		return enacted{}, false, err
	}
	held, ok, err := p.inForce(t)
	if err != nil {
		return enacted{}, false, err
	}
	if ok && held.Regime == r {
		return held, false, nil
	}
	// THE RATE BOUND, read off THIS tenant's own table. Counted before the insert
	// and over the window ending now, so it is the tenant's own recent history and
	// nothing another tenant does can move it.
	var recent int
	if err := sh.db.QueryRow(`SELECT COUNT(*) FROM policy WHERE tenant = ? AND at > ?`,
		string(t), now.Add(-policyWindow).UTC().Unix()).Scan(&recent); err != nil {
		return enacted{}, false, fmt.Errorf("risk: count policy changes: %w", err)
	}
	if recent >= maxPolicyPerWindow {
		return enacted{}, false, errPolicyRate
	}
	next := enacted{Version: held.Version + 1, Regime: r, At: now.UTC().Truncate(time.Second), By: by}
	live := 0
	if r.Live {
		live = 1
	}
	if _, err := sh.db.Exec(
		`INSERT INTO policy (tenant, version, review, sample, live, by, at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		string(t), next.Version, r.Review, r.Sample, live, by, next.At.Unix(),
	); err != nil {
		return enacted{}, false, fmt.Errorf("risk: record policy: %w", err)
	}
	if err := p.disposeOldPolicy(sh, t); err != nil {
		// The version LANDED. A disposal that failed is a retention that will be
		// retried on the next change, not a policy change to undo.
		p.log.Warn("policy history is over its retention and could not be trimmed",
			"tenant", string(t), "err", err)
	}
	return next, true, nil
}

// adopter is the identity recorded on a version this plane minted on the
// organisation's behalf rather than at its request — the one-time adoption of a
// regime that predates the policy record. It is not an organisation's user and it
// is deliberately not spellable as one.
const adopter = "risk-plane:adopted"

// restoreRegime puts an organisation's own decision regime back in force on a
// fresh residency, from the policy record — the ONE source of truth for it.
//
// legacy is the [anomaly.Config] off the model row, which is where the regime
// used to live. It is read for exactly one purpose: ADOPTION. An organisation
// that stated its appetite before this record existed has it on that row and
// nowhere else, so resolving the regime only from the policy record would return
// every such organisation to shadow on the first rollout after this ships — the
// very defect the record exists to fix. The legacy regime is therefore adopted as
// version 1, durably, and from that moment there is exactly one source.
//
// EVERY FAILURE KEEPS THE DEFAULT POSTURE, WHICH IS SHADOW, AND SAYS SO. Refusing
// to honour a policy is survivable; running live because a policy failed to load
// is not.
func (p *plane) restoreRegime(r *resident, legacy *anomaly.Config) {
	rec, held, err := p.inForce(r.key)
	if err != nil {
		p.log.Warn("policy history unavailable; the tenant keeps the default shadow posture",
			"tenant", string(r.key), "err", err)
		return
	}
	if !held {
		if legacy == nil {
			return // never stated a regime; the default stands, and that is the truth
		}
		adopted, _, err := p.enact(r.key, regimeOf(*legacy), adopter, time.Now())
		if err != nil {
			p.log.Warn("a regime predating the policy record could not be adopted; the tenant keeps the default shadow posture",
				"tenant", string(r.key), "err", err)
			return
		}
		p.log.Info("adopted a regime that predates the policy record",
			"tenant", string(r.key), "version", adopted.Version, "live", adopted.Regime.Live)
		rec = adopted
	}
	cfg := rec.Regime.applyTo(r.cfg)
	mod, err := anomaly.New(cfg, r.vel.vel)
	if err != nil {
		// The geometry seed is NOT reset with the posture: r.cfg has to keep
		// describing the store [plane.plant] built.
		p.log.Warn("the recorded regime could not be rebuilt; the tenant keeps the default shadow posture",
			"tenant", string(r.key), "version", rec.Version, "err", err)
		seed := r.cfg.Seed
		r.cfg = defaultConfig()
		r.cfg.Seed, r.cfg.MaxOrgs = seed, 1
		return
	}
	r.cfg, r.mod, r.pol = cfg, mod, rec.Version
}

// admitRegime refuses a regime no threshold can be derived from. It REFUSES
// rather than clamping: a coerced appetite is a policy the organisation did not
// state, recorded as though it had.
//
// It is the ONE door. The bounds were stated twice — once here and once at the
// typed op — and two spellings of one rule is one rule that will disagree with
// itself the first time either moves. The op's copy is gone; this is the whole
// rule, at the same strictness the published contract always had.
func admitRegime(r regime) error {
	switch {
	case r.Review <= 0 || r.Review > 0.5:
		return zip.ErrBadRequest("'review' must be in (0, 0.5] — a share of the stream, not a count")
	case r.Sample < 0 || r.Sample > 1:
		return zip.ErrBadRequest("'sample' must be in [0, 1]")
	}
	return nil
}

// disposeOldPolicy enforces the TOTAL bound by disposing of the oldest versions
// past [policyVersions]. It is the only statement in this package that removes a
// version, and what it removed stays countable: versions are contiguous from 1,
// so the lowest surviving version reports the loss without a counter to drift.
func (p *plane) disposeOldPolicy(sh *shelf, t tenant) error {
	_, err := sh.db.Exec(
		`DELETE FROM policy WHERE tenant = ? AND version <= (
			SELECT MAX(version) - ? FROM policy WHERE tenant = ?
		)`, string(t), policyVersions, string(t))
	if err != nil {
		return fmt.Errorf("risk: dispose policy: %w", err)
	}
	return nil
}

// history reads an organisation's own policy history, newest first, and says how
// many versions retention has disposed of.
//
// The tenant is the LEADING bound predicate and the shelf is the tenant's own
// file, so another organisation's history is not merely filtered out — it is not
// in the file being read.
func (p *plane) history(t tenant, limit int) ([]enacted, int, error) {
	if limit <= 0 || limit > policyVersions {
		limit = policyVersions
	}
	sh, err := p.for_(t)
	if err != nil {
		return nil, 0, err
	}
	rows, err := sh.db.Query(
		`SELECT version, review, sample, live, by, at FROM policy
		 WHERE tenant = ? ORDER BY version DESC LIMIT ?`, string(t), limit)
	if err != nil {
		return nil, 0, fmt.Errorf("risk: read policy history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []enacted
	for rows.Next() {
		var (
			e    enacted
			live int
			at   int64
		)
		if err := rows.Scan(&e.Version, &e.Regime.Review, &e.Regime.Sample, &live, &e.By, &at); err != nil {
			return nil, 0, fmt.Errorf("risk: scan policy: %w", err)
		}
		e.Regime.Live, e.At = live == 1, time.Unix(at, 0).UTC()
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("risk: read policy history: %w", err)
	}
	return out, p.disposed(sh, t), nil
}

// disposed is how many versions retention has taken, DERIVED from the lowest
// surviving version rather than counted: versions are contiguous from 1, so a
// history whose oldest row is version 7 has lost 6. Zero for an organisation
// that has never stated a policy.
func (p *plane) disposed(sh *shelf, t tenant) int {
	var low sql.NullInt64
	if err := sh.db.QueryRow(`SELECT MIN(version) FROM policy WHERE tenant = ?`, string(t)).Scan(&low); err != nil {
		return 0
	}
	if !low.Valid || low.Int64 <= 1 {
		return 0
	}
	return int(low.Int64 - 1)
}
