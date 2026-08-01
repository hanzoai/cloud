package risk

// drift.go answers the question a model plane exists to answer between fits: is
// the model still measuring the world it was fitted to?
//
// FOUR MEASURES, TWO SIDES, ONE INDEX. On the INPUT side, the coordinates the
// model reads: has the distribution of any feature moved away from the one the
// fit was estimated on. On the OUTPUT side: has the score distribution moved,
// is the realised alert share still the share the appetite states, and is the
// realised judgement rate still the rate the fit measured. All four are the SAME
// arithmetic — the population stability index over a fixed set of bins — except
// the alert share, which is a ratio against a governed target and is compared to
// it directly.
//
// WHY THE ALERT SHARE IS THE ONE WORTH ALARMING ON FIRST. It is the only measure
// with a number somebody committed to. Feature drift says the world changed,
// which happens constantly and mostly harmlessly; a realised share that has left
// the band its stated appetite implies says the CONTROL changed — it is either
// alerting on nothing, which reads exactly like a quiet week, or flooding, which
// reads as an incident nobody can triage. The engine already tracks scored and
// alerted per tenant, so this measure costs nothing.
//
// AND IT IS MEASURED AGAINST THE FIT'S OWN PROFILE, NOT AGAINST TODAY. The
// reference distributions were stored on the fit when it was estimated. A drift
// report that recomputed its own baseline from recent data would compare the
// present to the present, which is a number that is always small and always
// reassuring.

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sort"
	"time"
)

// The population-stability index bands, as they are conventionally read. They
// are named rather than inlined because two of them decide an alarm and a
// threshold that appears twice is a threshold that will differ once.
const (
	// psiMoved is where a distribution has moved enough to be worth reporting.
	psiMoved = 0.10
	// psiShifted is where it has moved enough to say the model is measuring a
	// different world than the one it was fitted to.
	psiShifted = 0.25
)

// appetiteBand is how far the realised alert share may sit from the stated
// appetite before the model is drifting rather than merely noisy.
//
// It is the SAME factor the exhaustive search already refuses a winner outside
// (learn.go's searchRun), and it is shared rather than restated so that
// "honoured its stated appetite" means one thing in both places. A model that
// would not have won the search that chose it is not one the search is still
// endorsing.
const appetiteBand = 2.0

// driftFloor is how many scored rows a measure needs before it says anything. A
// realised share over thirty rows is noise with a decimal point, and an alarm
// raised off it is an alarm an operator learns to ignore.
const driftFloor = 200

// driftDeadline bounds ONE tenant's examination. It is far tighter than the
// estimation's ceiling and deliberately so: the tick walks the resident tenants
// in one goroutine, so a tenant allowed ten minutes here is a tenant that can
// hold every other tenant's drift check — and their scheduled re-estimations —
// behind it. Thirty seconds is two orders of magnitude above the measured cost
// of replaying the thousand-row window this reads.
const driftDeadline = 30 * time.Second

// ── the index ───────────────────────────────────────────────────────────────

// psi is the population stability index between a reference distribution and an
// observed one: the symmetric sum of (observed - reference) * ln(observed /
// reference) over matching bins.
//
// Empty bins are floored rather than dropped. A bin the reference never filled
// and the present fills heavily is exactly the drift worth catching, and
// dropping it to avoid a logarithm of zero is dropping the finding.
func psi(reference, observed []float64) (float64, bool) {
	if len(reference) == 0 || len(reference) != len(observed) {
		return 0, false
	}
	const floor = 1e-4
	var sum float64
	for i := range reference {
		r, o := reference[i], observed[i]
		if r < floor {
			r = floor
		}
		if o < floor {
			o = floor
		}
		sum += (o - r) * math.Log(o/r)
	}
	if math.IsNaN(sum) || math.IsInf(sum, 0) {
		return 0, false
	}
	return round4(sum), true
}

// ── the reading ─────────────────────────────────────────────────────────────

// drifted is one fit's drift report.
type drifted struct {
	Fit  string
	At   time.Time
	From time.Time
	To   time.Time
	// Rows is how many recent observations the live profile was built from, and
	// Scored how many of them the model was warm enough to score.
	Rows   int
	Scored int
	// Dims is the per-feature index, worst first.
	Dims []dimDrift
	// Score is the index over the score distribution. Absent when the fit
	// recorded no reference distribution to compare against.
	Score *float64
	// Stated is the appetite the fit's shape declared and Realised is the share
	// actually reached over the window. The gap between them is the one measure
	// with a governed target.
	Stated   float64
	Realised *float64
	// Rate is the productive share of the judged rows in the window, and Was is
	// the share the fit measured. Both absent when nothing in range is judged —
	// which is itself the finding, and is reported as Refusal rather than as a
	// zero rate that reads like a clean world.
	Rate *float64
	Was  *float64
	// Drifted is whether any measure left its band, and Says names which in
	// words an operator can act on.
	Drifted bool
	Says    []string
	Refusal string
}

type dimDrift struct {
	Dim   string
	Index float64
	Moved bool
}

// measure builds the LIVE profile through the same projection the fit's
// reference was built with — project(), in fit.go, the one function both sides
// share. Two implementations of one distribution would differ in the binning or
// the burn-in, and every index computed across them would then be measuring the
// difference between the implementations rather than a change in the world.
//
// It scores but never learns into the LIVE model: the sandbox is its own. A
// drift report that advanced the model it was reporting on would be a
// measurement that changes what it measures.
func measure(ctx context.Context, t Tenant, shape candidate, seed uint64, rows []replayed) (fitProfile, int, int, error) {
	model, vel, err := sandbox(shape, seed)
	if err != nil {
		return fitProfile{}, 0, 0, err
	}
	prof, seen, err := project(ctx, t, model, vel, rows)
	if err != nil {
		return fitProfile{}, 0, 0, err
	}
	return prof, seen.rows, seen.scored, nil
}

// examine compares a fit's stored profile against the recent window and decides
// whether the model has degraded.
func examine(ctx context.Context, s *stateService, t Tenant, db *sql.DB, r fitRow, limit int, now time.Time) (drifted, error) {
	out := drifted{Fit: r.ID, At: now, Stated: r.Shape.Review}
	if r.Status != fitReady {
		out.Refusal = "this fit was never estimated, so there is no profile to compare against"
		return out, nil
	}
	if len(r.Profile.Score) == 0 && len(r.Profile.Dims) == 0 {
		out.Refusal = "this fit recorded no reference distribution, so drift cannot be measured against it"
		return out, nil
	}
	if r.Profile.Scored < driftFloor {
		out.Refusal = "this version's reference distribution was built from too few scored rows for an index " +
			"against it to mean anything"
		return out, nil
	}
	if limit <= 0 || limit > fitRows {
		limit = 1000
	}
	// The MOST RECENT rows since the version was estimated. Reading the oldest
	// page instead — which is what a plain history replay gives — means a tenant
	// with more history than the cap never has a window at all, and drift then
	// never fires on exactly the busiest tenants.
	window, err := replaySince(db, r.Source.To, limit)
	if err != nil {
		return out, err
	}
	if len(window) == 0 {
		out.Refusal = "no traffic since this version was estimated"
		return out, nil
	}
	out.From, out.To = window[0].obs.at, window[len(window)-1].obs.at

	live, rows, scored, err := measure(ctx, t, r.Shape, r.Profile.Seed, window)
	if err != nil {
		return out, err
	}
	out.Rows, out.Scored = rows, scored
	// THE FLOOR IS ON SCORED ROWS, not on rows read. A cold sandbox spends its
	// first Appetite.Warm rows unable to score, so a window of a thousand rows
	// can carry forty comparable ones — and an index over forty is noise with a
	// decimal point. An alarm raised off that is one an operator learns to
	// ignore, which switches off the real ones too.
	if scored < driftFloor {
		out.Refusal = "too little scored traffic since this version was estimated for a distribution to mean anything"
		return out, nil
	}

	for dim, reference := range r.Profile.Dims {
		observed, ok := live.Dims[dim]
		if !ok {
			continue
		}
		index, ok := psi(reference, observed)
		if !ok {
			continue
		}
		out.Dims = append(out.Dims, dimDrift{Dim: dim, Index: index, Moved: index >= psiMoved})
		if index >= psiShifted {
			out.Drifted = true
			out.Says = append(out.Says, dim+" is not the distribution this fit was estimated on")
		}
	}
	sort.SliceStable(out.Dims, func(i, j int) bool { return out.Dims[i].Index > out.Dims[j].Index })

	// The score index is only meaningful under the geometry the reference was
	// produced with. A version estimated before the seed was pinned has none, and
	// the measure is then SKIPPED rather than reported: an index computed across
	// two different forests is noise, and noise inside the reporting band reads
	// as a small finding.
	if r.Profile.Seed != 0 {
		if index, ok := psi(r.Profile.Score, live.Score); ok {
			out.Score = &index
			if index >= psiShifted {
				out.Drifted = true
				out.Says = append(out.Says, "the score distribution has moved away from the one this fit produced")
			}
		}
	}

	// The realised share is read off the LIVE model — the one actually deciding
	// — because that is the number the appetite was a promise about. The sandbox
	// above answers what this fit's shape would do; only the live store answers
	// what it did.
	if store, _ := champion(s, t, db); store != nil {
		st := store.State(t.String())
		if st.Scored >= driftFloor {
			realised := round4(st.Realised)
			out.Realised = &realised
			switch {
			case st.Saturated:
				out.Drifted = true
				out.Says = append(out.Says,
					"too much of the stream scores in the top band for any threshold to honour the appetite, so the model is alerting on nothing")
			case realised > out.Stated*appetiteBand:
				out.Drifted = true
				out.Says = append(out.Says, "the realised alert share is more than double the appetite this fit states")
			case realised*appetiteBand < out.Stated:
				out.Drifted = true
				out.Says = append(out.Says, "the realised alert share is less than half the appetite this fit states")
			}
		}
	}

	out.Rate, out.Was = live.Rate, r.Profile.Rate
	if out.Rate != nil && out.Was != nil && *out.Was > 0 {
		ratio := *out.Rate / *out.Was
		if ratio > appetiteBand || ratio*appetiteBand < 1 {
			out.Drifted = true
			out.Says = append(out.Says, "the realised judgement rate has moved away from the rate this fit measured")
		}
	}
	return out, nil
}

// ── the alarm ───────────────────────────────────────────────────────────────

// alarm examines the champion and RECORDS a degradation the first time it sees
// one, so that an operator reading the plane a week later can tell when it
// started rather than only that it is true now.
//
// It is deduplicated on the measure: a model that has been drifting for a month
// writes one row per measure per fit, not one row per tick. A drift alarm that
// repeats every five minutes is an alarm somebody mutes.
func alarm(s *stateService, t Tenant, db *sql.DB, now time.Time) error {
	r, ok, err := fitInRole(db, roleChampion)
	if err != nil || !ok || r.Status != fitReady {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), driftDeadline)
	defer cancel()
	d, err := examine(ctx, s, t, db, r, 0, now)
	if err != nil || !d.Drifted {
		return err
	}
	for _, says := range d.Says {
		raised, err := raise(db, driftRow{
			ID: newID("drift"), Fit: r.ID, At: now, Says: says,
			Rows: d.Rows, Scored: d.Scored, Stated: d.Stated,
		})
		if err != nil {
			return err
		}
		if !raised {
			continue
		}
		s.Log.Warn("risk: the champion model has drifted",
			"tenant", t.String(), "fit", r.ID, "says", says, "rows", d.Rows, "scored", d.Scored)
		// The analytics copy, last and fail-soft. The record above is the record;
		// /v1/event answers accepted-or-dropped and is never the source of one.
		emit(t.org(), "risk.drift", map[string]any{"fit": r.ID, "says": says, "rows": d.Rows})
	}
	return nil
}

// driftRow is one recorded degradation.
type driftRow struct {
	ID     string
	Fit    string
	At     time.Time
	Says   string
	Rows   int
	Scored int
	Stated float64
}

// raise appends a drift record unless the same fit already has an open one
// saying the same thing. Reports whether it wrote.
func raise(db *sql.DB, r driftRow) (bool, error) {
	var seen string
	err := db.QueryRow(`SELECT id FROM drift WHERE fit = ? AND says = ? AND cleared = ''`,
		r.Fit, r.Says).Scan(&seen)
	switch {
	case err == nil:
		return false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
	}
	_, err = db.Exec(`INSERT INTO drift (id, fit, at, says, rows, scored, stated, cleared)
		VALUES (?, ?, ?, ?, ?, ?, ?, '')`,
		r.ID, r.Fit, stamp(r.At), r.Says, r.Rows, r.Scored, r.Stated)
	return err == nil, err
}

// clearDrift closes every open alarm on a fit. Called when a fit is stood down:
// an alarm about a model that no longer decides is noise, and leaving it open
// makes the next real one harder to see. The row is CLOSED and never deleted —
// when the model started degrading is the fact that makes the promotion after it
// reviewable.
func clearDrift(db *sql.DB, fit string, now time.Time) error {
	_, err := db.Exec(`UPDATE drift SET cleared = ? WHERE fit = ? AND cleared = ''`, stamp(now), fit)
	return err
}

func openDrift(db *sql.DB, fit string) ([]driftRow, error) {
	rows, err := db.Query(`SELECT id, fit, at, says, rows, scored, stated
		FROM drift WHERE fit = ? AND cleared = '' ORDER BY at ASC`, fit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []driftRow
	for rows.Next() {
		var d driftRow
		var at string
		if err := rows.Scan(&d.ID, &d.Fit, &at, &d.Says, &d.Rows, &d.Scored, &d.Stated); err != nil {
			return nil, err
		}
		d.At = unstamp(at)
		out = append(out, d)
	}
	return out, rows.Err()
}

// ── the contract ────────────────────────────────────────────────────────────

// mlDriftIn asks for a drift reading.
type mlDriftIn struct {
	// Fit is the version to measure. Absent takes the champion, which is the one
	// whose drift actually costs anything.
	Fit string `json:"fit,omitempty"`
	// Limit bounds how many recent observations the live profile is built from,
	// 1..5000. Default 1000.
	Limit int `json:"limit,omitempty"`
}

// mlDriftOut is whether the deciding model still fits the world it was
// estimated on.
type mlDriftOut struct {
	// Fit is the version measured.
	Fit string `json:"fit"`
	// From is the first instant in the window measured, RFC 3339.
	From string `json:"from,omitempty"`
	// To is the last instant in the window measured, RFC 3339.
	To string `json:"to,omitempty"`
	// Rows is how many recent observations the live profile was built from.
	Rows int `json:"rows"`
	// Scored is how many of those rows the model was warm enough to score. A
	// reading over too little scored traffic refuses rather than reporting a
	// small index, because a small index over forty rows is not evidence of
	// stability — it is the absence of a measurement wearing one's clothes.
	Scored int `json:"scored"`
	// Dims is the population-stability index per feature, worst first. This is
	// the INPUT side: has the distribution of what the model reads moved away
	// from the one it was estimated on.
	Dims []mlDimDrift `json:"dims,omitempty"`
	// Score is the same index over the score distribution — the OUTPUT side.
	// Absent when the version recorded no reference distribution.
	Score *float64 `json:"score,omitempty"`
	// Stated is the alert share this version's shape declared. It is the one
	// number in the whole reading somebody committed to, which is why the alarm
	// hangs off the gap between it and Realised rather than off feature drift.
	Stated float64 `json:"stated"`
	// Realised is the alert share the live model actually reached over the
	// window. Far under the stated appetite is a control alerting on nothing,
	// and that reads exactly like a quiet week. Absent when too little of the
	// stream has been scored for the share to mean anything.
	Realised *float64 `json:"realised,omitempty"`
	// Rate is the productive share of the judged rows in the window. Absent when
	// nothing in range is judged, which is itself the finding.
	Rate *float64 `json:"rate,omitempty"`
	// Was is the productive share this version measured when it was estimated —
	// what Rate is compared against. Absent when the version judged nothing.
	Was *float64 `json:"was,omitempty"`
	// Drifted is whether any measure left its band.
	Drifted bool `json:"drifted"`
	// Says names which measures left their band, in words an operator can act
	// on. Empty when nothing did.
	Says []string `json:"says,omitempty"`
	// Open is every degradation already recorded against this version, so the
	// reading says when it started and not only that it is true now.
	Open []mlDriftAlarm `json:"open,omitempty"`
	// Refusal says why nothing was measured, when nothing was.
	Refusal string `json:"refusal,omitempty"`
}

// mlDimDrift is one feature's population-stability index.
type mlDimDrift struct {
	// Dim is the feature.
	Dim string `json:"dim"`
	// Index is the population-stability index against this version's own
	// reference distribution. Below 0.1 is stable, 0.1 to 0.25 has moved, above
	// 0.25 is a different world.
	Index float64 `json:"index"`
	// Moved marks an index past 0.1 — reported without alarming, because a
	// feature moving is ordinary and only a shifted one is a finding.
	Moved bool `json:"moved"`
}

// mlDriftAlarm is one recorded degradation. It is DURABLE and appended: when a
// model started degrading is what makes the promotion that followed reviewable,
// and a reading that only ever says "true now" cannot answer that.
type mlDriftAlarm struct {
	// At is when it was first recorded.
	At string `json:"at"`
	// Says is what degraded.
	Says string `json:"says"`
	// Rows is how many observations the finding rests on.
	Rows int `json:"rows"`
	// Scored is how many of them the model was warm enough to score — the number
	// that decides whether the finding is a measurement or noise.
	Scored int `json:"scored"`
}

// Drift measures whether the deciding model still fits the world it was
// estimated on: the distribution of every feature it reads, the distribution of
// the scores it produces, the alert share against the appetite it states, and
// the judgement rate against the one it measured.
//
// Every reference is the version's OWN, stored when it was estimated. A report
// that recomputed its baseline from recent data would be comparing the present
// to the present, which is a number that is always small and always reassuring.
func (o ops) drift(ctx context.Context, in *mlDriftIn) (*mlDriftOut, error) {
	sc, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	var r fitRow
	if id := in.Fit; id != "" {
		r, err = getFit(db, id)
		if err != nil {
			return nil, err
		}
	} else {
		champ, ok, err := fitInRole(db, roleChampion)
		if err != nil {
			return nil, err
		}
		if !ok {
			return &mlDriftOut{Refusal: "this tenant runs the shipped default shape, so there is no estimated " +
				"version to measure drift against; estimate one first"}, nil
		}
		r = champ
	}
	d, err := examine(ctx, o.s, sc.tenant, db, r, in.Limit, time.Now())
	if err != nil {
		return nil, err
	}
	out := &mlDriftOut{
		Fit: d.Fit, Rows: d.Rows, Scored: d.Scored, Score: d.Score,
		Stated: d.Stated, Realised: d.Realised, Rate: d.Rate, Was: d.Was,
		Drifted: d.Drifted, Says: d.Says, Refusal: d.Refusal,
	}
	if !d.From.IsZero() {
		out.From, out.To = stamp(d.From), stamp(d.To)
	}
	for _, dd := range d.Dims {
		out.Dims = append(out.Dims, mlDimDrift{Dim: dd.Dim, Index: dd.Index, Moved: dd.Moved})
	}
	open, err := openDrift(db, r.ID)
	if err != nil {
		return nil, err
	}
	for _, a := range open {
		out.Open = append(out.Open, mlDriftAlarm{At: stamp(a.At), Says: a.Says, Rows: a.Rows, Scored: a.Scored})
	}
	return out, nil
}
