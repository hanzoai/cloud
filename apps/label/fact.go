package label

// fact.go is the VALUE and its closed vocabularies. No I/O, no store, no clock:
// everything here is a pure function of its arguments, which is what lets the
// precedence rule and the leakage guard beside it be tested exhaustively.
//
// A label is an ASSERTION, not a property. Somebody, at some moment, asserted
// that a thing was fraud. That framing is the whole design: an assertion has an
// asserter, a moment it became knowable, and a claim — and two of them may
// disagree without either being wrong to have been recorded.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/claim"
)

// Disposition is what somebody concluded. The three values are the AML engine's
// own, verbatim (luxfi/aml pkg/replay), and are spelled identically here rather
// than translated: replay reports a false-positive proportion against them and
// topology refuses to rank a search without them, so a second vocabulary would
// leave the exhaustive search permanently unable to name a winner.
//
// vocabularyTest pins the three literals, so a drift from the engine's spelling
// fails a test here rather than silently halving a training set there.
type Disposition string

const (
	// Unjudged is an event somebody looked at and could not conclude about. It
	// is a real assertion, not the absence of one — "we reviewed this and could
	// not say" is evidence, and it is how an unmatured row is admitted to a
	// density model while staying invisible to a supervised one.
	Unjudged Disposition = ""
	// Productive is an event that led somewhere: escalated, reported, charged back.
	Productive Disposition = "productive"
	// Unproductive is an event judged not suspicious.
	Unproductive Disposition = "unproductive"
)

// dispositions is the closed set, published by the vocabulary op. A disposition
// outside it is refused at the door: an unknown claim has no meaning to the
// model plane and no meaning in an adverse action.
var dispositions = []Disposition{Productive, Unproductive, Unjudged}

// code renders a disposition for the columnar plane, whose column is Int8
// because a LowCardinality(String) that is sometimes empty sorts badly and reads
// worse. The mapping is one-way here and stated once.
func (d Disposition) code() int8 {
	switch d {
	case Productive:
		return 1
	case Unproductive:
		return 0
	default:
		return -1
	}
}

// Source names WHO asserted, and it is the primary term of the precedence rule.
// It is a closed set because an unknown source has no rank, and a precedence
// rule with an undefined term is not a rule.
type Source string

const (
	// Chargeoff is our own books writing the balance off. Terminal, ours, and
	// the only source whose claim costs us money to make.
	Chargeoff Source = "chargeoff"
	// Dispute is a card network's adjudicated chargeback, normalised by commerce
	// from every processor into one `dispute.created` fact. External and binding.
	Dispute Source = "dispute"
	// Case is a compliance determination closed under the /v1/aml workflow —
	// a regulated process with a documented file behind it.
	Case Source = "case"
	// Refund is a merchant refunding with a fraud reason coded. A unilateral act
	// with no counterparty adjudicating it, so it ranks below the three above.
	Refund Source = "refund"
	// Review is one analyst's call on one decision.
	Review Source = "review"
	// Sample is a judged draw from the below-the-line reproducible sample — the
	// arm that keeps a training set from being a description of the incumbent
	// block list. Deliberately the weakest claim: it is cheap and plentiful, and
	// it must never outvote an adjudicated one.
	Sample Source = "sample"
)

// precedence is the ORDER, lowest first, and it is the single declaration of the
// rule. Both the Go comparator (resolve.go) and the columnar ordering expression
// (mirror.go) are rendered from THIS map, so a source added here appears in both
// and cannot be forgotten in one.
//
// The ordering principle is ADJUDICATION WEIGHT: how much independent process
// stands behind the claim, and how hard it is to reverse. It is not a guess about
// accuracy, because accuracy is what the model plane measures and a precedence
// rule that pre-judged it would be measuring itself.
var precedence = map[Source]int{
	Chargeoff: 0,
	Dispute:   1,
	Case:      2,
	Refund:    3,
	Review:    4,
	Sample:    5,
}

// rank returns a source's precedence and whether it is known. An unknown source
// is refused at the door, so a false here off the write path means a row written
// by an older binary under a source since retired — it sorts last rather than
// first, which is the safe direction: an unrecognised claim must not win.
func rank(s Source) (int, bool) {
	r, ok := precedence[s]
	if !ok {
		return len(precedence), false
	}
	return r, true
}

// sources lists the vocabulary strongest-first. Derived from precedence, so the
// published order IS the enforced order.
func sources() []Source {
	out := make([]Source, 0, len(precedence))
	for s := range precedence {
		out = append(out, s)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			a, _ := rank(out[j])
			b, _ := rank(out[j-1])
			if a >= b {
				break
			}
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Kind is what the subject IS. Closed, because the kind is half the identity of
// the thing being judged: an open field lets one typo shard a tenant's labels
// into a partition nothing ever reads, and nothing would say so.
type Kind string

const (
	KindAccount     Kind = "account"
	KindAgent       Kind = "agent"
	KindMerchant    Kind = "merchant"
	KindPayout      Kind = "payout"
	KindPerson      Kind = "person"
	KindSession     Kind = "session"
	KindTransaction Kind = "transaction"
)

var kinds = []Kind{KindAccount, KindAgent, KindMerchant, KindPayout, KindPerson, KindSession, KindTransaction}

func knownKind(k Kind) bool {
	return slices.Contains(kinds, k)
}

func knownDisposition(d Disposition) bool {
	return slices.Contains(dispositions, d)
}

// Fact is one assertion, and it is immutable once written.
//
// THREE TIMES, AND THEY ARE DIFFERENT. At is when the judged event happened; Seen
// is when the caller says the assertion became knowable; Knowable is when this
// plane could first have answered with it. A chargeback lands 30 to 120 days
// after the transaction it judges. Join on At alone and the training set knows
// the future: offline AUC near 0.98, online worthless. Knowable is the field that
// stops it — see its own comment for why it is Seen that cannot.
type Fact struct {
	// Seq is the store's own delivery position, assigned by the record plane at
	// the INSERT and monotone for the life of the file. It is the total order the
	// derived copy catches up in, and it is not part of the assertion: nothing on
	// the wire carries it and the content digest does not fold it.
	Seq int64
	// ID is the content digest. Idempotence is therefore a property of the
	// assertion itself and not of a hand-picked key tuple: the same chargeback
	// delivered twice is one row, and anything that differs in any field is a
	// DIFFERENT assertion and is recorded as one. Nothing is ever overwritten.
	ID          string
	Kind        Kind
	Subject     string
	At          time.Time
	Seen        time.Time
	Disposition Disposition
	Source      Source
	// Evidence points at the record this conclusion came from: a dispute id, a
	// case id, a decision id. A label with no evidence cannot be defended when
	// the adverse action it fed is challenged, so it is required.
	Evidence string
	// By is the identity that asserted — the credential subject for an API
	// write, the analyst for a review. Stamped server-side from the validated
	// principal, never taken from the body: an attributable record whose
	// attribution the caller chose is not attributable.
	By string
	// Confidence in [0,1]. A processor chargeback is 1; an analyst's hunch is
	// not. It is a tie-breaker in the precedence rule, never a substitute for it.
	Confidence float64
	// Hold marks a litigation hold: retention never disposes of it, at any age.
	//
	// READ ONLY on this value. A hold is a fact about the record and not about
	// the world, so it is not part of the assertion, it is not folded into the
	// digest, and the write path never sets it — store.record does not name the
	// column and takes the schema default. The hold op is the one way it moves,
	// in either direction.
	Hold bool
	// Wrote is the server clock at the moment of the write. It is the ONLY time
	// on this record the tenant does not supply, and it is what a retention
	// sweep measures against — a tenant that could move At or Seen could
	// otherwise age its own records out early.
	Wrote time.Time
	// Knowable is the instant THIS PLANE could first have answered with the
	// assertion: the later of Seen and Wrote. It is derived here, from a value
	// the caller supplies and a value only the server holds, and it is the ONLY
	// time the leakage guard reads.
	//
	// WHY Seen CANNOT CARRY THE GUARD. Seen is the filer's claim about the
	// world's clock, bounded only by At <= Seen <= now+skew. A caller that files
	// a dispute today with seen == at — the natural integration mistake, "the
	// dispute is about this transaction" — makes a label written today look
	// knowable a year ago, and every horizon in the plane becomes decorative:
	// a backtest standing two days after the event resolves a row that did not
	// exist for another 298. A guard whose input is a caller-declared value is
	// exactly as strong as the caller's honesty, which is not a guard.
	//
	// The later of the two is the honest instant and it is not a compromise: an
	// assertion the plane did not hold could not have informed any decision the
	// plane served, whatever the world knew. For a live pipeline — the case this
	// is built for — Wrote is within minutes of Seen and the derivation changes
	// nothing. It bites exactly where it should: on history filed after the fact,
	// which is visible from the moment it is filed and never before.
	//
	// Seen is still recorded, published and returned. It is provenance — what the
	// filer claimed — and it no longer decides anything.
	Knowable time.Time
}

// digest is the content address of an assertion. Every semantic field is folded
// in length-prefixed, so no field's value can impersonate a field boundary and
// two distinct assertions cannot collide by concatenation.
//
// Wrote, Knowable and Seq are deliberately NOT folded in: they are the server's
// clock and the store's own position, so including them would make every
// redelivery a new row and destroy idempotence — which is the property a webhook
// that retries depends on. Hold is not folded in either, for a different reason
// and one worth stating: a litigation hold is a fact about the RECORD, not about
// the world, so it is not part of what was asserted and it is not placed here.
// It has its own op, which is also the only way it can be released.
func digest(f Fact) string {
	return claim.Digest(
		string(f.Kind),
		f.Subject,
		strconv.FormatInt(f.At.UTC().Unix(), 10),
		strconv.FormatInt(f.Seen.UTC().Unix(), 10),
		string(f.Disposition),
		string(f.Source),
		f.Evidence,
		f.By,
		strconv.FormatFloat(f.Confidence, 'f', 6, 64),
	)
}

// skew is the shared clock-drift bound. See claim.Skew.
const skew = claim.Skew

// subjectMax bounds a subject id. It is a store key and a columnar sort term,
// not free text.
const subjectMax = 512

// evidenceMax bounds the evidence pointer. It names a record; it is not the
// record.
const evidenceMax = 512

// admitSubject is the ONE ceiling on a subject reference, and every door that
// carries one asks it: the write, the resolve, the read filter.
//
// A COUNT OVER CALLER-SIZED VALUES IS NOT A BOUND. maxResolve caps a resolve at
// 500 named events; with no ceiling on a subject, 500 bounds the ROWS and nothing
// bounds the BYTES, and the only thing that did was the edge's BodyLimit — a fact
// about the deployment, not about this plane. Each subject is then amplified on
// the way down: the dedupe key, the grouping key, and one bound parameter per
// event in a statement against a single-writer file. With this ceiling asked at
// the door, `count × subjectMax` IS the byte bound of everything below it, which
// is the property the numbers were always claimed to have.
//
// The ceiling is also the only one that could be right, which is why there is one
// spelling of it and not two: the write refuses a longer subject, so a longer one
// cannot be IN the store, and a read that accepted it could only ever answer
// nothing after paying for the scan.
func admitSubject(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", fmt.Errorf("no subject, so nothing is named")
	case len(s) > subjectMax:
		return "", fmt.Errorf("subject is %d bytes and the bound is %d", len(s), subjectMax)
	}
	return s, nil
}

// admitKind admits a kind against the closed set. Asked on the READ path too: a
// kind outside the vocabulary can only ever match zero rows, so refusing says so
// instead of charging the caller for a scan — and a closed set is itself the byte
// bound on the field, which an open one would not be.
func admitKind(k string) (Kind, error) {
	out := Kind(strings.TrimSpace(k))
	if !knownKind(out) {
		return "", fmt.Errorf("kind %q is not one this plane judges", k)
	}
	return out, nil
}

// admitSource admits a source against the precedence declaration, which is the
// closed set: a source with no rank is one a conflict could not be resolved
// against. Asked on the read path for the same two reasons as the kind.
func admitSource(s string) (Source, error) {
	out := Source(strings.TrimSpace(s))
	if _, ok := rank(out); !ok {
		return "", fmt.Errorf("source %q has no precedence, so a conflict with it could not be resolved", s)
	}
	return out, nil
}

// admitEvidence is the ceiling on the evidence pointer. It names a record; it is
// not the record.
func admitEvidence(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", fmt.Errorf("no evidence, so this label could not be defended in an adverse action")
	case len(s) > evidenceMax:
		return "", fmt.Errorf("evidence is %d bytes and the bound is %d", len(s), evidenceMax)
	}
	return s, nil
}

// admit validates one assertion and completes it. It is the ONE gate: every
// write path goes through it, so a rule stated here cannot be bypassed by a
// caller that found another door.
//
// FAIL CLOSED. Every branch refuses; none coerces. A coerced label is a label
// somebody will later defend in front of a regulator as though a human meant it.
//
// The per-value ceilings are the same functions the read doors ask, so there is
// one spelling of "how long may a subject be" and not one per door.
func admit(f Fact, now time.Time) (Fact, error) {
	subject, err := admitSubject(f.Subject)
	if err != nil {
		return Fact{}, err
	}
	f.Subject = subject
	evidence, err := admitEvidence(f.Evidence)
	if err != nil {
		return Fact{}, err
	}
	f.Evidence = evidence
	switch {
	case !knownKind(f.Kind):
		return Fact{}, fmt.Errorf("kind %q is not one this plane judges", f.Kind)
	case !knownDisposition(f.Disposition):
		return Fact{}, fmt.Errorf("disposition %q is not in the closed vocabulary", f.Disposition)
	case f.Source == "":
		return Fact{}, fmt.Errorf("no source, so nothing asserted this")
	default:
		if _, ok := rank(f.Source); !ok {
			return Fact{}, fmt.Errorf("source %q has no precedence, so a conflict with it could not be resolved", f.Source)
		}
	}
	switch {
	case f.At.IsZero():
		return Fact{}, fmt.Errorf("no event time, so there is nothing for a maturity horizon to measure")
	case f.Seen.IsZero():
		return Fact{}, fmt.Errorf("no observation time, so this label cannot be kept out of a training set that predates it")
	case f.Seen.Before(f.At):
		return Fact{}, fmt.Errorf("the label was seen before the event it judges, so one of the two clocks is wrong")
	case f.At.After(now.Add(skew)):
		return Fact{}, fmt.Errorf("the event is in the future")
	case f.Seen.After(now.Add(skew)):
		return Fact{}, fmt.Errorf("the label is not knowable yet")
	case f.Confidence < 0 || f.Confidence > 1:
		return Fact{}, fmt.Errorf("confidence %v is outside [0,1]", f.Confidence)
	}
	f.At = f.At.UTC().Truncate(time.Second)
	f.Seen = f.Seen.UTC().Truncate(time.Second)
	f.Wrote = now.UTC().Truncate(time.Second)
	f.Knowable = later(f.Seen, f.Wrote)
	f.ID = digest(f)
	return f, nil
}

// later is the whole of the derivation: the plane knew it when the world knew it
// or when we wrote it down, whichever came second.
func later(a, b time.Time) time.Time {
	if a.Before(b) {
		return b
	}
	return a
}
