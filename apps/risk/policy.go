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
// so the very next rollout rebuilt it from [defaultRegime], which is SHADOW: the
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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/reference"
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

// defaultRegime is the posture every organisation's model starts under, and it is
// stated HERE because the regime's owner is this record and not the engine's config.
//
// LIVE IS FALSE. A model that has never been reviewed against an organisation's own
// traffic must not be able to change an outcome, and the way that is guaranteed is
// that turning it live is an explicit act by that organisation recorded on its own
// shelf. It is also the posture every failure to read a regime falls back to
// ([plane.restoreRegime]): refusing to honour a policy is survivable, running live
// because a policy failed to load is not.
//
// The three numbers are what the engine would have filled a zero appetite with, with
// one exception that is the reason this is stated rather than inherited: the engine
// leaves a zero SAMPLE at zero, so a default written as "the zero regime" would
// silently switch off the below-the-line measurement that makes the realised appetite
// checkable.
func defaultRegime() regime { return regime{Review: 0.01, Sample: 0.001, Live: false} }

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

// ── the determination's stated bounds ────────────────────────────────────────
//
// A rule over stated facts needs stated bounds, and they are policy in exactly
// the sense the rest of this file is: numbers and listings an operator has to be
// able to read, defend and change, rather than literals buried in the rule that
// applies them. [determine] reads them and holds none of its own.
//
// THEY ARE THE DEPLOYMENT'S, NOT THE ORGANISATION'S, and the reason is the row
// above. A version is FIXED WIDTH ([maxPolicyRowBytes]) — three numbers, a flag
// and two bounded identifiers — and that is precisely what makes a version count
// a byte bound. A per-organisation list of countries is the variable-length value
// that bound forbids, so stating these per organisation is a second record with
// its own budget and its own retention: a later cut, never a field smuggled onto
// this one.

// nanoPerUSD converts the wire's unit to a figure a person states thresholds in.
// A nano is 10^-9 USD, which is the unit [riskEvent.Nano] carries and the one
// [riskEvent.observation] divides by to reach the model's dimensionless ratio.
const nanoPerUSD = 1_000_000_000

// freezeNano is the value at or above which a top-up from a jurisdiction the
// listing CALLS FOR ACTION on is frozen rather than merely examined.
//
// Ten thousand USD. It is the figure supervisors build their own reporting
// obligations around, and it is far past what self-serve credit at a card endpoint
// is for — the endpoint exists so a customer can buy inference, not move money.
const freezeNano = 10_000 * nanoPerUSD

// reviewNano is the value at or above which a top-up is examined WHATEVER the
// jurisdiction, including one carrying no risk signal at all.
//
// Fifty thousand USD, and it is deliberately a review and never a freeze. Review
// PROCEEDS ([cloud.RiskVerdict.Allowed]) — it summons a person and serves the
// customer — so setting it where a large legitimate top-up lands costs a look
// rather than a refusal. An amount on its own is also not a determination that
// anything is wrong: it is a reason to look, which is exactly what review means.
const reviewNano = 50_000 * nanoPerUSD

// THE TWO VALUE BOUNDS ABOVE ARE A PAYMENTS APPETITE, AND THE POPULATION THEY ARE
// READ OVER IS PAYMENTS.
//
// [onPace] reads them over a WINDOW rather than over one event, which is what makes
// a payment split into pieces visible — and a windowed accrual is only a statement
// about payments if everything in it is a payment. It was not. The aggregates accrue
// whatever an observation states it moved, and this plane folds an organisation's own
// metered inference spend into observations too (feature.go's `account` rollup sums
// hanzo.cloud_usage.billed_nano). Under one subject kind a customer's inference bill
// and its top-ups accrued on ONE key, so fifty thousand dollars of legitimate
// month-end inference read as fifty thousand dollars of payments inside an hour: a
// customer examined, and eventually frozen, for buying a lot of what we sell. There
// is no number that fixes it, because one appetite stated over two populations is two
// appetites — raising it to clear the spenders lowers it out of reach of the payers.
//
// So the populations are SEPARATE SUBJECTS, not separate numbers ([contract.KindPayer]).
// A payment is observed under the payer kind, taught only by a settlement this
// deployment watched happen ([plane.RiskObserve]), and no rollup writes that kind — so
// the accrual these two bounds are read over holds money that moved IN and nothing
// else, and one statement of appetite still has one reading.

// The credit endpoint's ARMED AXES, stated because the alternative is a half of a rule
// that is silently inert.
//
// [onFan] reads two link identifiers — the device and the counterparty — and a
// determination on either is only reachable if the asking gate STATES one.
// apps/commerce's credit endpoint states the counterparty ([axisPeer]) as the address
// our own edge resolved, which is the one identifier at that endpoint that several
// nominally unrelated payers can share, and it is what makes the fan-out reachable there.
//
// IT STATES NO DEVICE, AND THAT IS A FACT ABOUT THE ENDPOINT AND NOT A GAP IN THE RULE.
// No device fingerprint reaches this binary from a top-up: the request body is a card
// token, an amount and a currency; the card is tokenised in the browser and its number
// never arrives; the browser does not run the payment SDK's buyer-verification step, so
// there is no verification token either; and no header carries one. The DEVICE half of
// the fan-out is therefore UNARMED at the credit endpoint, deliberately, and it is
// declared at boot (apps/commerce's [paymentAxes]) rather than left to read as a rule
// that found nothing.
//
// What is emphatically NOT done is inventing one. A user-agent string, or a digest of
// the request's headers, is shared by millions of unrelated people — stated as a device
// it would put every customer past [fanSubjects] and summon a person for every payment,
// which is the same control being useless in the louder direction. The axis stays
// unarmed until a real fingerprint is collected, and the day it is, the endpoint states
// it in one line.
//
// The device half remains armed on the LEARN endpoint, where a caller that has a
// fingerprint states one — so the rule is exercised, and it is exercised on the axis
// where a real value exists.

// burstEvents is how many events on ONE of an event's identifiers — its subject,
// its counterparty pair or its device — inside the aggregates' narrowest window
// make a BURST ([onPace]).
//
// Sixty, which is one a minute for an hour on ONE identifier. Two bounds meet at
// that figure and both are held by
// [TestDetermine_TheAggregateBoundsCannotDisableTheRules]:
//
//	IT MUST BE ABOVE WHAT A FOLD ALONE PRODUCES. A tenant's own feature surface
//	folds into the aggregates one observation per (subject, [featureBucket]),
//	which is twelve an hour for a continuously active subject. A bound at or
//	under that is not a burst detector — it is a detector of having been active
//	all hour, firing on this organisation's most ordinary customers, and it
//	would fire on its OWN history the moment a residency rebuilt.
//
//	IT MUST BE ABOVE ZERO. A count bound at zero is not a permissive setting; it
//	is the rule firing on every event there is, which is the same control being
//	useless in the other direction.
//
// There is no matching bound for the VALUE a burst accrues, and deliberately not:
// [reviewNano] and [freezeNano] are that bound already, read over a window instead
// of over one event. What one payment may not move, an hour of payments may not
// move either — one statement of appetite, two readings.
const burstEvents = 60

// fanSubjects is how many DISTINCT subjects sharing ONE device or ONE
// counterparty make a network of nominally unrelated persons rather than a
// household, an office or a popular merchant ([onFan]).
//
// Twenty. It is deliberately generous, because the finding it supports is a
// REVIEW and never a freeze: twenty accounts on one device fingerprint is well
// past a family and well past a shared laptop, and the response to it is a person
// looking rather than a payment stopping. A tighter bound would summon that person
// for every office.
//
// IT MUST BE ABOVE ONE, and this is the direction that would fail silently in the
// other sense: one distinct subject is EVERY device, so a bound of one — or of
// zero, which the count is also the query's LIMIT for — turns "shared" into
// "exists" and the rule into noise. It must also stay under [recordRows], or it
// names a number the tenant's own retained record can never reach and the rule is
// switched off with nothing to see.
const fanSubjects = 20

// listedAsOf dates [defaultJurisdictions]. A listing with no date cannot have its
// currency assessed, and [reference.Jurisdictions] refuses one that has none —
// correctly, because "not listed" from an undated listing is not a fact.
var listedAsOf = time.Date(2026, time.June, 27, 0, 0, 0, 0, time.UTC)

// defaultJurisdictions is the higher-risk country listing in force when the
// operator has stated none, and it is a RISK tier — never a sanctions
// determination.
//
// THE DISTINCTION IS THE WHOLE POINT. A formal designation (OFAC, UN, EU, OFSI)
// is a legal finding about a named party, it is made by the screening engine that
// holds the designations, and nothing in this app makes one. What this is, is the
// tier a jurisdiction sits in for the purpose of pricing RISK at a credit endpoint —
// which is a judgement an operator is entitled to make and must be able to state.
// The two lists stay separate because the required response differs:
//
//	ACTION      countermeasures are called for. This tier may freeze.
//	MONITORING  increased monitoring. This tier may examine, and no further.
//
// Collapsing them into one "risky" flag loses exactly the distinction the rule
// needs in order to choose between the two, which is why [reference.Jurisdictions]
// keeps them apart and why this does too.
//
// WHY A COMPILED DEFAULT EXISTS AT ALL, given that the listing it defaults to is
// stated by an operator and changes several times a year: without one, an
// unconfigured deployment answers "not listed" for every country on earth, and the
// geography half of the rule is silently inert at the one endpoint it was built for.
// A control that switches itself off without saying so is worse than no control.
// So the default is stated here, dated, and it LOSES to anything the operator
// states — and because the rule ships in shadow, a stale entry cannot refuse
// anybody until an organisation is deliberately armed.
var defaultJurisdictions = reference.Jurisdictions{
	AsOf: listedAsOf,
	// Jurisdictions with no functioning anti-money-laundering supervision to
	// assess a payer against, or subject to comprehensive restrictions.
	Action: []string{"AF", "CU", "IR", "KP", "MM", "SY"},
	// Jurisdictions under increased monitoring.
	Monitoring: []string{"HT", "LY", "SS", "VE", "YE"},
}

// listing is the jurisdiction listing in force, and the account of how it was
// resolved. The two travel together because "which listing decided this" is part
// of what makes a freeze defensible, and because the ONE state an operator cannot
// see for themselves — a listing they stated that cannot decide anything — has to
// be reportable rather than merely survivable.
type listing struct {
	reference.Jurisdictions
	// Operator is whether this is the OPERATOR's listing rather than the compiled
	// default.
	Operator bool
	// Gap is why a stated operator listing was NOT taken. Empty when none was
	// stated, or when the one stated is in force.
	Gap string
}

// jurisdictions is the listing the rule evaluates against: the OPERATOR's if they
// stated a usable one, and [defaultJurisdictions] otherwise.
//
// The operator's wins whole rather than merging, because a merged listing is one
// nobody stated and nobody can reproduce. It is resolved once per process —
// membership is not something a request may move.
//
// A STATED LISTING WITH NO DATE IS NOT A LISTING, and this is the correction.
// [reference.Jurisdictions] refuses to answer from an undated one — rightly, since
// "not listed" from a listing of unknown currency is not a fact — so preferring one
// whole on the strength of its MEMBERSHIP alone put a listing in force that then
// errored on every country. Every determination fell to the unplaced branch, the
// ACTION tier became unreachable, and the freeze the rule exists for vanished:
// what remained was review at or past the freeze value, which looks exactly like a
// rule that is working. The operator sees no error, because the one that matters is
// swallowed per-country by design.
//
// So the date is part of what makes an operator listing USABLE, it is checked
// where the listing is chosen, and an unusable one loses to the dated compiled
// default instead of disarming the half of the rule it was stated to arm. The
// reason is carried out on [listing.Gap] rather than logged from in here: this is
// resolved once, lazily, and a control that switched itself off must be visible
// from OUTSIDE the process — [Mount] says it at startup and /v1/risk/health keeps
// saying it.
//
// UNSET and MALFORMED are left exactly as they were. [reference.JurisdictionsFromEnv]
// answers the empty listing for both, the empty listing falls to the default here,
// and the default is dated — so neither ever reaches the rule as a listing that
// cannot decide.
var jurisdictions = sync.OnceValue(func() listing { return resolve(reference.JurisdictionsFromEnv()) })

// resolve chooses the listing in force from whatever the operator stated. It is
// separated from the memoization above because they are two things: reading the
// environment happens once per process, and CHOOSING is a rule — one that has to be
// exercised against every shape an operator can stated, which a value resolved once
// at first use cannot be.
func resolve(stated reference.Jurisdictions) listing {
	switch {
	case len(stated.Action) == 0 && len(stated.Monitoring) == 0:
		// Unset, or malformed JSON: [reference.JurisdictionsFromEnv] answers the empty
		// listing for both, and both correctly take the dated default.
		return listing{Jurisdictions: defaultJurisdictions}
	case stated.AsOf.IsZero():
		return listing{
			Jurisdictions: defaultJurisdictions,
			Gap: "AML_JURISDICTIONS states " + strconv.Itoa(len(stated.Action)+len(stated.Monitoring)) +
				" countries with no `as_of` date, so no country can be assessed against it; " +
				"the compiled default listing of " + listedAsOf.Format("2006-01-02") + " is in force",
		}
	}
	return listing{Jurisdictions: stated, Operator: true}
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
// row is the [anomaly.Config] off the resume row, which is where the regime used to
// live and where it is still braided with the geometry on disk ([legacy]). It is read
// for exactly one purpose: ADOPTION. An organisation that stated its appetite before
// this record existed has it on that row and nowhere else, so resolving the regime
// only from the policy record would return every such organisation to shadow on the
// first rollout after this ships — the very defect the record exists to fix. The
// row's regime is therefore adopted as version 1, durably, and from that moment
// there is exactly one source.
//
// EVERY FAILURE KEEPS THE DEFAULT POSTURE, WHICH IS SHADOW, AND SAYS SO. Refusing
// to honour a policy is survivable; running live because a policy failed to load
// is not.
func (p *plane) restoreRegime(r *resident, row *anomaly.Config) {
	rec, held, err := p.inForce(r.key)
	if err != nil {
		p.log.Warn("policy history unavailable; the tenant keeps the default shadow posture",
			"tenant", string(r.key), "err", err)
		return
	}
	if !held {
		if row == nil {
			return // never stated a regime; the default stands, and that is the truth
		}
		adopted, _, err := p.enact(r.key, regimeOf(*row), adopter, time.Now())
		if err != nil {
			p.log.Warn("a regime predating the policy record could not be adopted; the tenant keeps the default shadow posture",
				"tenant", string(r.key), "err", err)
			return
		}
		p.log.Info("adopted a regime that predates the policy record",
			"tenant", string(r.key), "version", adopted.Version, "live", adopted.Regime.Live)
		rec = adopted
	}
	// THE REGIME AND NOTHING ELSE. The space and the partition are the ones
	// [plane.plant] built and stay exactly where it put them, so restating a posture is
	// not a way to move a model into another space.
	next, err := r.geom.build(r.key, rec.Regime, r.seed, r.vel)
	if err != nil {
		// Only the POSTURE falls back. The space and the partition are left describing
		// the model still running, which is the one plant built — at this default
		// posture, which is why nothing else has to be undone.
		p.log.Warn("the recorded regime could not be rebuilt; the tenant keeps the default shadow posture",
			"tenant", string(r.key), "version", rec.Version, "err", err)
		r.reg = defaultRegime()
		return
	}
	r.run(next, r.geom)
	r.reg, r.pol = rec.Regime, rec.Version
}

// admitRegime refuses a regime no threshold can be derived from. It REFUSES
// rather than clamping: a coerced appetite is a policy the organisation did not
// state, recorded as though it had.
//
// It is the ONE check. The bounds were stated twice — once here and once at the
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
