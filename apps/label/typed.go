package label

// typed.go is the CONTRACT. Every operation this subsystem serves is declared
// here as a zip typed op, and that one declaration is the whole of it: the REST
// route, the OpenAPI operation, the MCP tool, the CLI command and every generated
// SDK method are projections of the same registry entry. An untyped route appends
// nothing to that registry and is invisible to all five.
//
// THE ORG IS NEVER AN In FIELD, AND NEITHER IS THE ASSERTER. A typed op receives
// only a context; the tenant arrives through cloud.Bridge and is read by tenantOf,
// and `by` is stamped from the same validated principal. An In field is
// caller-supplied, so a tenant read from one is a cross-tenant read the caller
// asserted for itself — and an attribution taken from one is an attribution the
// caller chose, which is not attribution at all.
//
// EVERY ADDRESS IS UNDER `/v1/risk`, AND THAT IS WHAT DECIDES THE PRODUCT.
// openapi.Fold takes an operation's product tag from the FIRST /v1 segment of its
// path and nothing else (openapi.Product), so the address is not a routing detail
// that a tag can override — it is the published product. An address under
// `/v1/ml` would therefore have filed these seven operations into the KServe
// model-SERVING product, which is a different product with its own four paths,
// its own consumers and its own SDK namespace; the floor ratchet would have read
// `ml: 7 -> 14` and passed, because it only refuses a shrink.
// addressTest walks the registry and pins it.
//
// EVERY SCHEMA NAME IS PREFIXED `risk`, matching the address. openapi.Weave
// refuses one name with two shapes across apps, because a generated SDK binds
// whichever it read last, and `fact`, `record`, `event` and `coverage` are
// exactly the names the next app reaches for — which is also why one event here
// is `riskLabelEvent` and not `riskEvent`: apps/risk already publishes a
// `riskEvent`, and it is a scored decision rather than a judged one.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op AND off every In/Out field into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool description a model reads to pick the tool — Go drops comments
// at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the service to the typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) and has no parameter for the service,
// so the service arrives as a RECEIVER and every op is a METHOD VALUE — which is
// also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[*state] }

// The bounds. Every one of them exists because the plane behind it is shared and
// single-writer: a tenant looping an unbounded read is a fleet outage.
const (
	// maxAssert bounds one batch of assertions.
	maxAssert = 1000
	// maxResolve bounds one resolve call. Each named event pulls ALL of its
	// assertions — a truncated set would silently return the wrong winner — so
	// the bound is on events, not rows.
	maxResolve = 500
	// maxWindow bounds a coverage window, matching the feature plane's own
	// retention: a coverage number over a window whose features have expired
	// describes nothing.
	maxWindow = 400 * 24 * time.Hour
	// maxRead bounds the ASSERTIONS a single op may materialise, which maxResolve
	// and maxWindow do not: those bound how many events a caller may NAME and how
	// long a window may be, and a tenant with ten million assertions inside either
	// still asks one shared pod to hold ten million rows.
	//
	// Neither read can be paged — a precedence rule applied to a truncated set
	// returns a confident wrong winner, and a coverage number over a truncated set
	// understates what is judged — so both REFUSE at the bound and say so. A
	// caller narrows; nobody guesses.
	//
	// 25,000 assertions across at most 500 named events is fifty apiece, well past
	// what a real event carries, and 50,000 over a coverage window is a tenant
	// filing five hundred labels a day for a hundred days. Both are generous and
	// both are finite, which is the property that matters: the numbers bound the
	// worst case a neighbour can impose, not the ordinary case a tenant meets.
	maxResolveRead  = 25000
	maxCoverageRead = 50000
	// maxDispose bounds one retention sweep, so a tenant with ten million
	// expired assertions loops rather than blocking the writer.
	maxDispose = 10000
	// minRetention is the PLATFORM FLOOR: no tenant may dispose of a label
	// younger than this. A label can be the input to an adverse action, and the
	// AML retention ledger holds such a record five years (luxfi/aml
	// pkg/retention). A tenant may keep records LONGER — retention is per tenant
	// upward, never downward.
	minRetention = 5 * 365 * 24 * time.Hour
	// defaultHorizon is the maturity horizon used when a caller states none:
	// 120 days, past the Visa and Mastercard dispute windows, so a payment-lane
	// row admitted under it has had time for its chargeback to arrive.
	defaultHorizon = 120
	// maxHorizon bounds a stated horizon. A horizon longer than the record's own
	// retention floor would admit no row at all.
	maxHorizon = 5 * 365
	// defaultSpan is how much MATURED history the coverage gate describes when
	// the caller bounds nothing: the 90 days ending where maturity begins.
	//
	// Ending there is the whole point. The first cut ran the default window to
	// NOW, under a 120-day default horizon — so no event in it could possibly
	// have aged past the horizon, and matured, judged, contested and explore were
	// identically zero on every default call. An operator reading the op
	// documented as the gate on training concluded the tenant had no ground truth
	// when it had a year of it. A default that can only ever answer zero is worse
	// than no default: it answers confidently.
	defaultSpan = 90 * 24 * time.Hour
	// maxHold bounds one hold call, the same bound one batch of assertions takes.
	maxHold = maxAssert
	// idMax bounds a record id on the wire. It is a 64-character digest; the
	// bound is what keeps a caller from binding megabytes into an IN list.
	idMax = 128
	// instantMax bounds an RFC 3339 timestamp on the wire. The longest legal one
	// is well under 40 bytes; the bound exists because every other ceiling here
	// would be pointless beside a time field that accepted a megabyte and only
	// discovered it was not a timestamp after time.Parse had walked it. It is
	// applied in stamp(), which is the ONE parser every instant on this surface
	// goes through, so there is one place a time field is bounded and not one per
	// field.
	instantMax = 64
)

// THE BYTE BOUND OF EVERY DOOR, WHICH IS WHAT THE COUNTS ABOVE ARE WORTH.
//
// A bound on COUNT over caller-sized values is not a bound. Every ceiling below
// is asked at the first statement of the op, before the value reaches a dedupe
// key, a grouping key, a bound parameter or a row, so `count × ceiling` is the
// byte bound on everything this plane allocates for one request:
//
//	riskLabel        maxAssert  × (subjectMax + evidenceMax + vocabularies + instants)
//	riskLabels       1          × (subjectMax + vocabularies + 2 instants)
//	riskResolveLabels maxResolve × (subjectMax + vocabulary + instant) + 1 instant
//	riskLabelCoverage 1          × 2 instants
//	riskDisposeLabels 1          × 1 instant
//	riskHoldLabels   maxHold    × idMax
//	riskLabelVocabulary — no caller-sized value at all
//
// What is read off the SOCKET is the edge's BodyLimit and is not this plane's to
// state (config.go, GATEWAY_BODY_LIMIT). What this plane BINDS, HOLDS and STORES
// is the product above, and it is finite in every term. wireBoundsTest walks each
// In type with reflect and fails on a caller-sized field that has no ceiling
// declared, so a NEW field cannot arrive unbounded and be noticed later.

func routes(app cloud.Router, s *cloud.Service[*state]) {
	// cloud.Bridge belongs to the composer — the fused host installs it at its
	// root, and a plugin program's constructor does the same — so the validated
	// principal is already parked on the context when these ops run.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		s.Log.Error("label: the router exposes no op registry; the ground-truth surface would serve routes no projection knows")
		return
	}
	o := ops{s: s}

	// Declared on the App with WHOLE paths: the collection route IS the prefix,
	// which a group cannot spell.
	zip.Post(zapp, "/v1/label", o.label,
		zip.WithOperationID("riskLabel"),
		zip.WithSummary("Assert ground truth about events"),
		zip.WithTags("label"))
	zip.Get(zapp, "/v1/label", o.labels,
		zip.WithOperationID("riskLabels"),
		zip.WithSummary("Read the assertions this tenant has recorded"),
		zip.WithTags("label"))
	zip.Post(zapp, "/v1/label/resolve", o.resolve,
		zip.WithOperationID("riskResolveLabels"),
		zip.WithSummary("Resolve the label in force for named events, as of each event's own horizon"),
		zip.WithTags("label"))
	zip.Get(zapp, "/v1/label/coverage", o.coverage,
		zip.WithOperationID("riskLabelCoverage"),
		zip.WithSummary("How much of the window has matured, and how much of that is judged"),
		zip.WithTags("label"))
	zip.Get(zapp, "/v1/label/vocabulary", o.vocabulary,
		zip.WithOperationID("riskLabelVocabulary"),
		zip.WithSummary("The closed vocabularies and the precedence rule that resolves a conflict"),
		zip.WithTags("label"))
	zip.Post(zapp, "/v1/label/dispose", o.dispose,
		zip.WithOperationID("riskDisposeLabels"),
		zip.WithSummary("Dispose of this tenant's expired assertions, whole records only"),
		zip.WithTags("label"))
	zip.Post(zapp, "/v1/label/hold", o.hold,
		zip.WithOperationID("riskHoldLabels"),
		zip.WithSummary("Place or release a litigation hold on named records"),
		zip.WithTags("label"))
}

// ── assert ───────────────────────────────────────────────────────────────────

// riskLabelIn is a batch of ground truth.
type riskLabelIn struct {
	// Labels is the batch. Each member is judged on its own: one refusal does
	// not discard the rest, because a webhook redelivering five disputes must
	// not lose four of them to one malformed fifth.
	Labels []riskLabelFact `json:"labels"`
}

// riskLabelFact is one assertion. It is idempotent on its CONTENT: the same
// chargeback delivered twice is one record, and anything that differs in any
// field is a different assertion and is recorded beside the first. Nothing here
// ever overwrites anything.
type riskLabelFact struct {
	// Kind is what the subject is: account, agent, merchant, payout, person,
	// session or transaction. Closed, because a typo in an open field would shard
	// a tenant's labels into a partition nothing reads and nothing would say so.
	Kind string `json:"kind"`
	// Subject identifies the thing being judged, in the tenant's own namespace.
	Subject string `json:"subject"`
	// At is when the judged event happened, RFC 3339.
	At string `json:"at"`
	// Seen is when this assertion became KNOWABLE, RFC 3339. It is required and
	// it is not At: a chargeback lands 30 to 120 days after the transaction it
	// judges, and a training set joined on At alone knows the future. Everything
	// this plane does to prevent leakage is computed from Seen.
	Seen string `json:"seen"`
	// Disposition is productive, unproductive, or empty for an explicit
	// unjudged — the AML engine's own vocabulary, verbatim.
	Disposition string `json:"disposition"`
	// Source is who asserted: chargeoff, dispute, case, refund, review or
	// sample. It is the primary term of the precedence rule, so it is closed —
	// an unknown source has no rank and a conflict with it could not be resolved.
	Source string `json:"source"`
	// Evidence points at the record this conclusion came from: a dispute id, a
	// case id, a decision id. Required, because a label with no evidence cannot
	// be defended when the adverse action it fed is challenged.
	Evidence string `json:"evidence"`
	// Confidence in [0,1]. A processor chargeback is 1; an analyst's hunch is
	// not. It breaks a tie WITHIN a precedence rank and can never lift a weak
	// source above a strong one — otherwise every caller would send 1.
	//
	// A litigation hold is NOT a field here. It is a fact about the record and
	// not about the world, so it is not part of what was asserted, it is not in
	// the content digest, and it has its own op — which is also the only way one
	// can be released. Carried here it was silently a no-op on any record that
	// already existed: the digest was the same, the insert was ignored, and the
	// caller was told `duplicate` while the hold it asked for was never placed.
	Confidence float64 `json:"confidence,omitempty"`
}

// riskLabelOut reports what happened to each member of the batch.
type riskLabelOut struct {
	// Recorded is how many members became a NEW row in the tenant's record.
	// Recorded + Duplicate + Refused is exactly the number of labels sent, so a
	// caller reconciling a webhook delivery can do it on the counts alone.
	Recorded int `json:"recorded"`
	// Duplicate is how many members this tenant already held, byte for byte. The
	// idempotency key is the assertion's CONTENT digest — kind, subject, at, seen,
	// disposition, source, evidence, the asserting identity and confidence, folded
	// in length-prefixed — so a webhook redelivering one chargeback is a duplicate
	// and costs nothing, while an assertion differing in ANY of those fields is a
	// DIFFERENT assertion and is recorded beside the first. Nothing was written and
	// nothing was overwritten; it is an outcome, never an error. The asserting
	// identity is in the digest, so the same claim filed by a second credential is
	// two assertions and not a redelivery.
	Duplicate int `json:"duplicate"`
	// Refused is how many members failed admission and were NOT recorded. Refusal
	// is per member and never discards the rest of the batch: an empty or
	// over-512-byte subject or evidence, a kind, disposition or source outside the
	// closed vocabulary, an `at` or `seen` that is not RFC 3339, a `seen` before the
	// `at` it judges, either instant more than five minutes past the server clock,
	// or a confidence outside [0,1]. Results names which member and why, so the
	// refused ones are exactly the ones to fix and resend.
	Refused int `json:"refused"`
	// Results is per fact, in the order sent, so a caller can retry exactly the
	// members that were refused and can log the content digest of the ones that
	// landed.
	Results []riskLabelResult `json:"results"`
	// Mirror names why the columnar copy did not take this batch, when it did
	// not. The record is already durable in the tenant's own store by then — the
	// warehouse copy exists to make a training join cheap, and its absence is a
	// gap in that join, never a lost label.
	Mirror string `json:"mirror,omitempty"`
	// Pending is how many assertions the derived copy is still to take. Every
	// write attempt carries the backlog forward as well as its own batch, so a
	// warehouse that was unreachable closes its gap on the next write rather than
	// leaving a hole in a training join nothing would report. It is counted under
	// a cap and saturates there: zero means caught up, and a large number means a
	// backlog to work through rather than an inventory to reconcile.
	Pending int `json:"pending,omitempty"`
}

type riskLabelResult struct {
	// ID is the content digest of the assertion — the id a redelivery of the
	// same fact resolves to.
	ID string `json:"id"`
	// Status is recorded, duplicate or refused.
	Status string `json:"status"`
	// Refusal states what was wrong, for the refused.
	Refusal string `json:"refusal,omitempty"`
}

// label records a batch of ground truth against the entities it judges.
//
// Each assertion carries TWO times — when the judged event happened, and when
// the assertion became knowable — and both are required. The second is what
// keeps a chargeback that landed in June out of a model that had to decide in
// February.
//
// It is idempotent on the CONTENT of an assertion, so a webhook that redelivers
// is safe. It never overwrites: a source that corrects itself later files a NEW
// assertion, which wins from the moment it became knowable and leaves every
// earlier observation instant seeing exactly what it saw.
//
// The asserter is stamped from the validated credential and is not a body field.
func (o ops) label(ctx context.Context, in *riskLabelIn) (*riskLabelOut, error) {
	sc, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	switch {
	case in == nil || len(in.Labels) == 0:
		return nil, zip.Errorf(http.StatusBadRequest, "no labels")
	case len(in.Labels) > maxAssert:
		return nil, zip.Errorf(http.StatusBadRequest, "batch of %d exceeds the bound of %d", len(in.Labels), maxAssert)
	}

	now := time.Now().UTC()
	out := &riskLabelOut{Results: make([]riskLabelResult, 0, len(in.Labels))}

	for _, w := range in.Labels {
		f, err := decode(w, sc.by, now)
		if err != nil {
			out.Refused++
			out.Results = append(out.Results, riskLabelResult{Status: refused, Refusal: err.Error()})
			continue
		}
		res, err := st.record(ctx, f)
		if err != nil {
			// A store error is not the caller's fault and not a per-fact
			// refusal: the record plane is the thing this op exists to write, so
			// a failure to write it fails the request rather than reporting a
			// label as taken when it was not.
			o.s.Log.Error("label: the record plane refused a write", "tenant", sc.tenant.String(), "err", err)
			return nil, zip.Errorf(http.StatusInternalServerError, "the record could not be kept")
		}
		if res.Status == recorded {
			out.Recorded++
		} else {
			out.Duplicate++
		}
		out.Results = append(out.Results, riskLabelResult{ID: res.ID, Status: res.Status})
	}

	// Durable first, columnar after — and the columnar half sends the BACKLOG,
	// not just this batch. Mirroring only what landed here would mean a batch
	// that missed the warehouse could never be repaired: its redelivery resolves
	// to `duplicate`, nothing lands, and the gap in the training join outlives
	// every retry. Delivery is therefore a property of the store's watermark and
	// not of one request.
	sent, pending, err := deliver(ctx, sc.tenant, st, o.s.State.derived)
	out.Pending = pending
	if err != nil {
		out.Mirror = err.Error()
		o.s.Log.Warn("label: the columnar copy did not take a batch; the record stands and the backlog is carried",
			"tenant", sc.tenant.String(), "pending", pending, "err", err)
	}

	// SHIP BEFORE ACK. Both writes above land in the tenant's local file — the
	// assertions and the delivery watermark — and neither is durable until it is
	// fenced to the tenant's durable object. Answering 200 first would be telling
	// a caller its compliance record is kept while the next Recreate rollout
	// hydrates a snapshot that predates it.
	//
	// Only when something was actually written: a redelivery of a batch already
	// held, with the derived copy caught up, changes no byte and owes no ship.
	if out.Recorded > 0 || sent > 0 {
		if err := o.s.State.ship(sc.ns); err != nil {
			o.s.Log.Error("label: the record was written and could not be shipped",
				"tenant", sc.tenant.String(), "err", err)
			return nil, zip.Errorf(http.StatusServiceUnavailable,
				"the record was not acknowledged as durable, so it is not acknowledged at all; retry (every write here is idempotent on the assertion's content): %v", err)
		}
	}
	return out, nil
}

// decode turns one wire fact into a validated record, stamping the asserter.
func decode(w riskLabelFact, by string, now time.Time) (Fact, error) {
	at, err := stamp(w.At)
	if err != nil {
		return Fact{}, fmt.Errorf("at: %w", err)
	}
	seen, err := stamp(w.Seen)
	if err != nil {
		return Fact{}, fmt.Errorf("seen: %w", err)
	}
	return admit(Fact{
		Kind:        Kind(strings.TrimSpace(w.Kind)),
		Subject:     w.Subject,
		At:          at,
		Seen:        seen,
		Disposition: Disposition(strings.TrimSpace(w.Disposition)),
		Source:      Source(strings.TrimSpace(w.Source)),
		Evidence:    w.Evidence,
		By:          by,
		Confidence:  w.Confidence,
	}, now)
}

// ── read ─────────────────────────────────────────────────────────────────────

// riskLabelsIn narrows a read of the record plane. Every field binds as a
// parameter; none becomes SQL text.
type riskLabelsIn struct {
	// Kind and Subject narrow to one entity.
	Kind    string `json:"kind,omitempty"`
	Subject string `json:"subject,omitempty"`
	// Source narrows to one asserter — the read that answers "what has commerce
	// told us", separately from "what has an analyst told us".
	Source string `json:"source,omitempty"`
	// From and To bound the EVENT time, half-open, RFC 3339.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Limit caps the page. Out of range takes the plane's own bound.
	Limit int `json:"limit,omitempty"`
}

type riskLabelsOut struct {
	// Labels is the page, newest event first.
	Labels []riskLabelRecord `json:"labels"`
	// Count is how many this page holds. It is not a total: a total over an
	// unbounded append-only log is a full scan of a single-writer file.
	Count int `json:"count"`
}

// riskLabelRecord is one assertion as it was recorded, with its whole provenance.
type riskLabelRecord struct {
	// ID is the assertion's content digest — SHA-256 over every semantic field,
	// rendered hex — computed server-side and never supplied. It is the key a
	// redelivery collapses onto, and it is the id the hold op names.
	ID string `json:"id"`
	// Kind is what the subject IS, from the closed set: account, agent, merchant,
	// payout, person, session or transaction. With Subject and At it is the IDENTITY
	// of the judged event — the triple a resolve names and the triple assertions are
	// grouped by, so a typo in it would file a label against an event nobody asks
	// about.
	Kind string `json:"kind"`
	// Subject is the entity that was judged, named in the TENANT'S OWN namespace and
	// at most 512 bytes. It is opaque here: stored, matched and returned verbatim,
	// never dereferenced. It has no meaning outside this tenant — the record is the
	// tenant's own file — so an id lifted from another tenant's response names
	// nothing.
	Subject string `json:"subject"`
	// At is when the judged EVENT happened, RFC 3339 in UTC, truncated to the
	// second. The filer supplies it, and it is what a maturity horizon measures
	// from: this event's as-of is At plus the horizon. A resolve names it back
	// exactly, to the second.
	At string `json:"at"`
	// Seen is when the FILER said the assertion became knowable. It is
	// provenance: it is recorded and published, and it decides nothing.
	Seen string `json:"seen"`
	// Knowable is when THIS PLANE could first have answered with the assertion:
	// the later of Seen and the server clock at the write, derived server-side.
	// It is the instant the leakage guard compares, so it is published beside the
	// claim it was derived from — an answer whose rule nobody can see is one
	// nobody can check.
	Knowable string `json:"knowable"`
	// Disposition is what was concluded, from the closed set: `productive` — the
	// event led somewhere, escalated, reported or charged back; `unproductive` —
	// judged not suspicious; or the empty string for an explicit UNJUDGED, which is
	// a real assertion ("we looked and could not say") and not the absence of one.
	Disposition string `json:"disposition"`
	// Source is WHO asserted, from the closed set: chargeoff, dispute, case, refund,
	// review or sample. It is the primary term of the precedence rule — an unknown
	// source has no rank and a conflict with it could not be resolved — so it is
	// what decides which of two disagreeing assertions is in force.
	Source string `json:"source"`
	// Evidence is the pointer to the record this conclusion came from: a dispute id,
	// a case id, a decision id. At most 512 bytes, required at the write, and opaque
	// to this plane — stored and returned verbatim, never resolved. It is what an
	// adverse action is defended with, which is why an assertion carrying none is
	// refused at the door.
	Evidence string `json:"evidence"`
	// By is the identity that asserted, stamped server-side at the write.
	By string `json:"by"`
	// Confidence is the filer's own confidence in [0,1] — 1 for a processor
	// chargeback, less for an analyst's hunch. Zero is the ordinary value for a
	// filer that stated none, and it means the weakest tie-break there is rather
	// than "unknown". It breaks a tie only WITHIN one precedence rank and can never
	// lift a weak source above a strong one.
	Confidence float64 `json:"confidence"`
	// Hold is true while a litigation hold is on this record: retention will not
	// dispose of it, at any age. False — and it is omitted then — leaves the record
	// disposable once it is older than the boundary a sweep names. It is a fact
	// about the RECORD and not about the world, so it is not folded into ID, no
	// write path can set it, and the hold op is the one way it moves in either
	// direction.
	Hold bool `json:"hold,omitempty"`
	// Wrote is the server clock at the write. It is the only time on the record
	// the tenant did not supply, and it is what retention measures against.
	Wrote string `json:"wrote"`
}

// labels reads the assertions this tenant has recorded, newest event first.
//
// It reads the RECORD — the tenant's own store — and not the columnar copy, so
// what it returns is what would be produced in an audit. Narrow it by entity, by
// asserter, or by event window.
func (o ops) labels(ctx context.Context, in *riskLabelsIn) (*riskLabelsOut, error) {
	_, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if in == nil {
		in = &riskLabelsIn{}
	}
	from, err := optional(in.From)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "from: %v", err)
	}
	to, err := optional(in.To)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "to: %v", err)
	}
	// EVERY NARROWING TERM IS ADMITTED BEFORE IT IS BOUND, through the same
	// functions the write door asks. A filter is caller-sized and it becomes a
	// bound parameter against a single-writer file, so an unbounded one is a
	// megabyte in a statement for a value that could not be in the store; and a
	// filter outside a closed vocabulary can only ever match zero rows, so
	// refusing it says so rather than charging for the scan and answering `[]`.
	q := query{From: from, To: to, Limit: in.Limit}
	if strings.TrimSpace(in.Kind) != "" {
		if q.Kind, err = admitKind(in.Kind); err != nil {
			return nil, zip.Errorf(http.StatusBadRequest, "kind: %v", err)
		}
	}
	if strings.TrimSpace(in.Subject) != "" {
		if q.Subject, err = admitSubject(in.Subject); err != nil {
			return nil, zip.Errorf(http.StatusBadRequest, "subject: %v", err)
		}
	}
	if strings.TrimSpace(in.Source) != "" {
		if q.Source, err = admitSource(in.Source); err != nil {
			return nil, zip.Errorf(http.StatusBadRequest, "source: %v", err)
		}
	}
	facts, err := st.facts(ctx, q)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "the record plane could not be read")
	}
	out := &riskLabelsOut{Labels: make([]riskLabelRecord, 0, len(facts)), Count: len(facts)}
	for _, f := range facts {
		out.Labels = append(out.Labels, render(f))
	}
	return out, nil
}

// ── resolve ──────────────────────────────────────────────────────────────────

// riskResolveIn names the events to resolve and the observation they are resolved
// under.
type riskResolveIn struct {
	// Subjects are the exact events being judged. Each carries its own event
	// time, because the as-of that keeps the future out is derived from that
	// instant plus the horizon — one as-of over a whole batch would give a
	// January row six extra months of hindsight.
	//
	// One entry per DISTINCT (kind, subject, at): naming an event twice answers
	// once, because an event resolved twice would list its own winner as a
	// contrary claim and would hand a materialiser duplicate training rows.
	Subjects []riskLabelEvent `json:"subjects"`
	// Horizon is how many days an event must age before it may be resolved at
	// all, and it is the whole of the no-leakage rule. 120 for the payment lane
	// (past the Visa and Mastercard dispute windows), 14 for signup abuse.
	// Unstated takes 120.
	Horizon int `json:"horizon,omitempty"`
	// Now moves the observation instant BACKWARDS, RFC 3339. It exists so a
	// BACKTEST can resolve labels as the plane stood at a past moment; without it,
	// every backtest would score a model against knowledge that arrived after the
	// decision it is being scored on. An instant after the server clock is
	// refused: a backtest resolves the past, and a future one would declare
	// unmatured events matured and hand a training set negatives for rows whose
	// chargeback has not had time to arrive.
	Now string `json:"now,omitempty"`
}

type riskLabelEvent struct {
	// Kind is the judged entity's type, from the closed set: account, agent,
	// merchant, payout, person, session or transaction. One outside it is refused
	// rather than answered `unlabelled`, because it could only ever match nothing
	// and the caller would read a real absence into a typo.
	Kind string `json:"kind"`
	// Subject is the entity id in the tenant's own namespace, at most 512 bytes. It
	// is matched EXACTLY against what was recorded — this is a lookup, not a search,
	// and no prefix, pattern or normalisation is applied.
	Subject string `json:"subject"`
	// At is the event's own instant, RFC 3339. It is part of the event's IDENTITY
	// and not a filter: it is matched exactly, to the second, against the `at` the
	// assertions were filed under, so an instant a second off names a different
	// event and resolves to nothing. It is also what this event's as-of is measured
	// from — At plus the horizon.
	At string `json:"at"`
}

type riskResolveOut struct {
	// Now and Horizon echo the observation this answer was computed under. A
	// resolved label without them is a claim nobody can check.
	Now string `json:"now"`
	// Horizon is the maturity horizon this answer was computed under, IN DAYS — the
	// caller's, or 120 when it stated none. Each event's as-of is its own `at` plus
	// this many days, and that as-of is what decides which assertions were visible
	// to it; an event whose as-of falls after Now is not resolved at all and is
	// counted in Unmatured instead.
	Horizon int `json:"horizon"`
	// Labels is one entry per named event that BOTH matured and had at least one
	// assertion knowable by its own as-of, in the order the events were named. The
	// three outcomes partition the ask: len(labels) + Unmatured + Unlabelled is the
	// number of DISTINCT events named, an event named twice having been answered
	// once.
	Labels []riskResolved `json:"labels"`
	// Unmatured is how many named events had not aged past the horizon. They are
	// not unlabelled — they are not yet ASKABLE, and a supervised training set
	// must exclude them rather than treat them as negatives.
	Unmatured int `json:"unmatured"`
	// Unlabelled is how many matured events had no assertion knowable by their
	// own as-of. That is the ordinary state of most traffic and it is reported
	// rather than answered as unproductive: manufacturing negatives is how a
	// fraud model comes to describe the incumbent block list.
	Unlabelled int `json:"unlabelled"`
}

// riskResolved is the label in force for one event, and what it beat.
type riskResolved struct {
	// Kind is the judged entity's type, echoed from the event that was named. With
	// Subject and At it is how a caller joins this answer back onto the training row
	// or the decision it asked about.
	Kind string `json:"kind"`
	// Subject is the entity id, echoed from the event that was named — the tenant's
	// own key, returned verbatim.
	Subject string `json:"subject"`
	// At is the event's instant, RFC 3339, echoed. It is what the horizon is
	// measured from, so At plus the horizon is AsOf.
	At string `json:"at"`
	// AsOf is the instant this answer was true at: the event time plus the
	// horizon. Nothing seen after it was visible to this resolution.
	AsOf string `json:"asOf"`
	// Disposition is the claim IN FORCE at AsOf: productive, unproductive, or the
	// empty string for an explicit unjudged. It is the winning assertion's own
	// claim, never a vote or an average — an average of two adjudications is a third
	// claim nobody made. A matured event nobody judged is not answered here at all;
	// it is counted in Unlabelled, because manufacturing a negative there is how a
	// fraud model comes to describe the incumbent block list.
	Disposition string `json:"disposition"`
	// Source is who filed the winning assertion, and it is the PRIMARY term of the
	// rule that picked it. Sources rank by adjudication weight — chargeoff,
	// dispute, case, refund, review, sample, strongest first — and only inside one
	// rank do the tie-breaks run, in order: the assertion that became KNOWABLE
	// latest, then the higher confidence, then the lower id. The vocabulary op
	// publishes that order from the same declaration the resolver reads, so a caller
	// holding a contested answer can reproduce it.
	Source string `json:"source"`
	// Evidence is the winning assertion's pointer to the record behind it — the
	// dispute, case or decision id it was filed with, opaque and verbatim. It
	// travels with the answer so an adverse action can name what judged the subject
	// without a second read.
	Evidence string `json:"evidence"`
	// By is the identity that filed the WINNING assertion, `<home org>/<user>`,
	// stamped server-side from the validated principal at the write and never taken
	// from a body — an attribution the caller chose is not attribution. It is the
	// winner's alone; every losing assertion keeps its own and is returned whole in
	// Conflicts.
	By string `json:"by"`
	// ID is the winning assertion's content digest, so this answer traces to the
	// exact record it came from — and that record can be placed under litigation
	// hold by naming this id.
	ID string `json:"id"`
	// Confidence is the winning assertion's own confidence in [0,1], zero when its
	// filer stated none. It is reported because it is a term of the rule that picked
	// the winner, and it is the weakest term but one: it breaks a tie inside one
	// rank and never lifts a weak source above a strong one.
	Confidence float64 `json:"confidence"`
	// Contested is true when a visible assertion claimed a DIFFERENT disposition.
	// Two sources agreeing is corroboration, not conflict.
	Contested bool `json:"contested"`
	// Conflicts is every other visible assertion, strongest first, whole. They
	// are kept and returned rather than dropped, so an adverse action can show
	// that the plane knew of a contrary claim and say why it lost. They are
	// horizon-filtered exactly like the winner: an assertion that was not
	// knowable yet cannot even be named here, because naming it would leak its
	// existence into a past decision.
	Conflicts []riskLabelRecord `json:"conflicts,omitempty"`
}

// resolve answers, for each named event, which assertion was in force AS OF that
// event's own horizon — and what disagreed with it.
//
// This is the join surface: the dataset materialiser calls it to attach ground
// truth to training rows, and the evaluator calls it to score a past decision
// against what was knowable when the decision had to be made. One mechanism for
// both, so a model can never be trained under one leakage rule and scored under
// another.
//
// Three answers are distinct and all three are honest: a resolved label, an
// event that has not matured, and a matured event nobody has judged. The last is
// never reported as unproductive.
func (o ops) resolve(ctx context.Context, in *riskResolveIn) (*riskResolveOut, error) {
	_, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	switch {
	case in == nil || len(in.Subjects) == 0:
		return nil, zip.Errorf(http.StatusBadRequest, "no subjects")
	case len(in.Subjects) > maxResolve:
		return nil, zip.Errorf(http.StatusBadRequest, "%d subjects exceeds the bound of %d", len(in.Subjects), maxResolve)
	}
	horizon := in.Horizon
	if horizon == 0 {
		horizon = defaultHorizon
	}
	if horizon < 0 || horizon > maxHorizon {
		return nil, zip.Errorf(http.StatusBadRequest, "horizon %d days is outside [0,%d]", horizon, maxHorizon)
	}
	now := time.Now().UTC()
	if in.Now != "" {
		asked, err := stamp(in.Now)
		if err != nil {
			return nil, zip.Errorf(http.StatusBadRequest, "now: %v", err)
		}
		// A BACKTEST STANDS IN THE PAST, NEVER THE FUTURE. Maturity is measured
		// against this instant, so a caller that could move it forward would
		// declare an event matured before its horizon had run and take the
		// "nobody has judged this" answer as a negative — manufacturing exactly
		// the training rows the horizon exists to withhold. It is the one way the
		// leakage guard can be talked out of its own rule, so it is refused here
		// rather than clamped: a materialisation that silently observed a
		// different instant from the one it asked for is not reproducible.
		if asked.After(now.Add(skew)) {
			return nil, zip.Errorf(http.StatusUnprocessableEntity,
				"now %s is after the server clock; a backtest resolves the past, and a future instant would report unmatured events as unjudged",
				asked.Format(time.RFC3339))
		}
		now = asked
	}
	w := Window{Now: now.UTC(), Horizon: time.Duration(horizon) * 24 * time.Hour}

	// ONE ENTRY PER DISTINCT EVENT. A caller naming the same event twice — two
	// rows of a join, a retry appended to a list — must not be answered twice:
	// the store read would return that event's assertions once per naming, the
	// resolution would list the winner as its own conflict, the count checked
	// against maxResolveRead would be inflated, and a materialiser would get
	// duplicate training rows. The dedupe is here, at the door, on the same key
	// the grouping uses, so nothing below has to defend against it.
	want := make([]Fact, 0, len(in.Subjects))
	named := make(map[string]struct{}, len(in.Subjects))
	for i, e := range in.Subjects {
		at, err := stamp(e.At)
		if err != nil {
			return nil, zip.Errorf(http.StatusBadRequest, "subjects[%d].at: %v", i, err)
		}
		k, err := admitKind(e.Kind)
		if err != nil {
			return nil, zip.Errorf(http.StatusBadRequest, "subjects[%d].%v", i, err)
		}
		// The CEILING, at the door and before the value is amplified. Without it
		// maxResolve bounds the events and NOTHING bounds the bytes: each subject
		// is copied into a dedupe key, a grouping key and a bound parameter, so
		// 500 × whatever the edge let through is what one request could make a
		// shared single-writer pod hold. With it, 500 × subjectMax is the bound.
		subject, err := admitSubject(e.Subject)
		if err != nil {
			return nil, zip.Errorf(http.StatusBadRequest, "subjects[%d]: %v", i, err)
		}
		ev := Fact{Kind: k, Subject: subject, At: at.UTC().Truncate(time.Second)}
		key := eventKey(ev.Kind, ev.Subject, ev.At)
		if _, dup := named[key]; dup {
			continue
		}
		named[key] = struct{}{}
		want = append(want, ev)
	}

	facts, err := st.forSubjects(ctx, want, maxResolveRead)
	if err != nil {
		return nil, readErr(err)
	}

	out := &riskResolveOut{Now: w.Now.Format(time.RFC3339), Horizon: horizon}
	held := map[string][]Fact{}
	for _, f := range facts {
		held[eventKey(f.Kind, f.Subject, f.At)] = append(held[eventKey(f.Kind, f.Subject, f.At)], f)
	}
	for _, e := range want {
		if !w.Matured(e.At) {
			out.Unmatured++
			continue
		}
		r, ok := Resolve(held[eventKey(e.Kind, e.Subject, e.At)], w.AsOf(e.At))
		if !ok {
			out.Unlabelled++
			continue
		}
		out.Labels = append(out.Labels, project(r))
	}
	return out, nil
}

func eventKey(k Kind, subject string, at time.Time) string {
	return string(k) + "\x00" + subject + "\x00" + at.UTC().Format(time.RFC3339)
}

func project(r Resolved) riskResolved {
	out := riskResolved{
		Kind: string(r.Kind), Subject: r.Subject,
		At:          r.At.Format(time.RFC3339),
		AsOf:        r.AsOf.Format(time.RFC3339),
		Disposition: string(r.Winner.Disposition),
		Source:      string(r.Winner.Source),
		Evidence:    r.Winner.Evidence,
		By:          r.Winner.By,
		ID:          r.Winner.ID,
		Confidence:  r.Winner.Confidence,
		Contested:   r.Contested,
	}
	for _, f := range r.Conflicts {
		out.Conflicts = append(out.Conflicts, render(f))
	}
	return out
}

// ── coverage ─────────────────────────────────────────────────────────────────

// riskCoverageIn bounds the coverage read.
type riskCoverageIn struct {
	// From and To bound the EVENT window, half-open, RFC 3339.
	//
	// Unstated, the window is the 90 days ENDING where maturity begins — `to` is
	// the horizon ago, not now. A default window running to now under a default
	// horizon could not contain one matured event, so every count below it would
	// be zero however much ground truth the tenant held.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Horizon is the maturity horizon in days the coverage is measured under.
	// Unstated takes 120. It also moves the default window, which ends where
	// maturity begins.
	Horizon int `json:"horizon,omitempty"`
}

// riskLabelCoverage answers the only question that decides whether a model can be
// trained at all.
type riskLabelCoverage struct {
	// From is the INCLUSIVE start of the EVENT window these counts were folded over,
	// RFC 3339, echoed with the defaults filled in — the caller's, or 90 days before
	// To. An assertion is in the window when its event time satisfies at >= From.
	From string `json:"from"`
	// To is the EXCLUSIVE end of that window (at < To). Unstated it is one horizon
	// before now, never now: a window running to now under a maturity horizon can
	// hold no matured event at all, so every count below would read zero however
	// much ground truth the tenant held.
	To string `json:"to"`
	// Horizon is the maturity horizon these counts were measured under, IN DAYS —
	// the caller's, or 120. It decides Matured (an event is matured when its `at`
	// plus this many days is not after now), it sets each event's own as-of and so
	// which assertions were visible to it, and when the caller bounds nothing it
	// also places the default window's end.
	Horizon int `json:"horizon"`
	// Facts is how many assertions the window holds; Events is how many distinct
	// judged events they cover. The two differ by exactly the corroboration and
	// the conflict in the plane.
	Facts int `json:"facts"`
	// Events is how many DISTINCT judged events those assertions name, keyed on
	// (kind, subject, at). It counts only events something was ASSERTED about: what
	// share of the whole event stream carries a label is a question about the
	// feature plane's denominator and is not answerable here. Matured + Unmatured is
	// Events.
	Events int `json:"events"`
	// Matured is how many of those events have aged past the horizon and may
	// therefore be admitted to a supervised set at all. It counts every matured
	// event, judged or not — it is the DENOMINATOR an operator divides Judged by,
	// and a denominator that excluded the unjudged would read 1.0 on a plane with
	// one label in it.
	Matured int `json:"matured"`
	// Unmatured is how many events in the window have NOT aged past the horizon.
	// They are not unlabelled — they are not yet askable, and a supervised set
	// must exclude them rather than treat them as negatives. Matured + Unmatured
	// is Events.
	Unmatured int `json:"unmatured"`
	// Judged is how many MATURED events resolve, at their own as-of, to
	// something other than unjudged.
	Judged int `json:"judged"`
	// Unlabelled is how many MATURED events had no assertion knowable by their
	// own as-of — including every assertion that arrived after that instant. It
	// is the field that says WHY judged is low: a tenant whose ground truth was
	// filed long after the events it judges reads matured=n, judged=0,
	// unlabelled=n, which is diagnosable, rather than a bare zero, which is not.
	Unlabelled int `json:"unlabelled"`
	// Contested is how many matured events have two visible assertions that
	// disagree. It is the number that says whether the precedence rule is
	// load-bearing or decorative, and it is the one to watch after wiring a new
	// source.
	Contested int `json:"contested"`
	// Productive is how many matured events resolve, at their own as-of, to a
	// WINNING assertion of `productive` — the event led somewhere: escalated,
	// reported, charged back. It is the positive class a supervised fit would train
	// on, and a near-zero count is the number that says the fit is not worth
	// running.
	Productive int `json:"productive"`
	// Unproductive is every OTHER judged event: the winner claimed `unproductive`,
	// judged not suspicious. Productive + Unproductive is Judged exactly, because a
	// winner of the explicit unjudged is counted in neither — it is a matured event
	// somebody looked at and could not conclude about, and rolling it into the
	// negatives would hand a model a claim nobody made.
	Unproductive int `json:"unproductive"`
	// Sources breaks the judged events down by the source that WON, so a plane
	// that looks labelled because one noisy source dominates is visible as such.
	Sources []riskSourceCoverage `json:"sources"`
	// Explore is the share of judged events whose winning assertion came from
	// the below-the-line sample. A blocked transaction never produces a
	// chargeback, so a training set with no exploration in it is a description of
	// the incumbent block list rather than of the world — and a champion measured
	// on it is measured on whether it agrees with the incumbent.
	Explore float64 `json:"explore"`
	// Pending is how many of this tenant's assertions the DERIVED columnar copy is
	// not known to hold yet. Every count above is folded from the record, so they
	// are right regardless — but a materialiser that joins in the warehouse while
	// this is non-zero is joining against an incomplete answer key, and a missing
	// fraud label is indistinguishable from an honest customer. It is reported at
	// the training gate because that is where somebody is deciding whether the
	// ground truth is good enough to fit on. Counted under a cap, so it saturates
	// rather than costing a full scan on every read.
	Pending int `json:"pending,omitempty"`
}

type riskSourceCoverage struct {
	// Source is the asserter these two counts are for — chargeoff, dispute, case,
	// refund, review or sample. There is one entry per source that either filed in
	// the window or won in it, in precedence order, strongest first. A source no
	// longer in the vocabulary still has rows and is reported after the known ones
	// rather than dropped out of a total that is supposed to add up.
	Source string `json:"source"`
	// Facts is how many assertions this source filed; Won is how many judged
	// events it was the assertion in force for. A source with many facts and few
	// wins is one that is being outranked, which is worth knowing before
	// concluding it is wired correctly.
	Facts int `json:"facts"`
	// Won is how many JUDGED events this source's assertion was the one IN FORCE
	// for, at that event's own as-of — it beat every other visible claim under the
	// precedence rule. Summed over the sources it is Judged. Read against Facts it
	// is the ratio that matters: many filed and few won is a source being outranked,
	// not a source that is broken, and one source winning nearly everything is a
	// plane that looks labelled because one noisy filer dominates it.
	Won int `json:"won"`
}

// coverage reports how much of a window has matured and how much of that is
// judged, per source.
//
// It is the gate on training. A supervised fit over a window whose judged count
// is near zero produces a number, and the number is meaningless; this op is what
// lets that be stated before the fit rather than discovered after it.
//
// It reads the RECORD plane and folds every assertion at that event's OWN as-of,
// so the counts obey exactly the leakage rule a materialisation would. It counts
// only what was ASSERTED: what share of the whole event STREAM carries a label is
// a question about the feature plane's denominator and is not answerable here.
func (o ops) coverage(ctx context.Context, in *riskCoverageIn) (*riskLabelCoverage, error) {
	_, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if in == nil {
		in = &riskCoverageIn{}
	}
	horizon := in.Horizon
	if horizon == 0 {
		horizon = defaultHorizon
	}
	if horizon < 0 || horizon > maxHorizon {
		return nil, zip.Errorf(http.StatusBadRequest, "horizon %d days is outside [0,%d]", horizon, maxHorizon)
	}
	now := time.Now().UTC()
	horizonFor := time.Duration(horizon) * 24 * time.Hour
	to, err := optional(in.To)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "to: %v", err)
	}
	if to.IsZero() {
		// The default window ENDS where maturity begins, so it describes the
		// population a supervised fit could actually use. Running it to now under
		// a horizon means every event in it is too young to have matured, and the
		// gate on training answers zero on its own defaults.
		to = now.Add(-horizonFor)
	}
	from, err := optional(in.From)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "from: %v", err)
	}
	if from.IsZero() {
		from = to.Add(-defaultSpan)
	}
	if !from.Before(to) {
		return nil, zip.Errorf(http.StatusBadRequest, "the window is empty")
	}
	if to.Sub(from) > maxWindow {
		return nil, zip.Errorf(http.StatusBadRequest, "the window exceeds the bound of %d days", int(maxWindow.Hours()/24))
	}

	facts, err := st.window(ctx, from, to, maxCoverageRead)
	if err != nil {
		return nil, readErr(err)
	}

	w := Window{Now: now, Horizon: horizonFor}
	out := &riskLabelCoverage{
		From: from.Format(time.RFC3339), To: to.Format(time.RFC3339),
		Horizon: horizon, Facts: len(facts),
	}
	filed := map[Source]int{}
	won := map[Source]int{}
	events := map[string]struct{}{}
	for _, f := range facts {
		filed[f.Source]++
		events[eventKey(f.Kind, f.Subject, f.At)] = struct{}{}
	}
	out.Events = len(events)

	var judged, explore int
	for _, c := range Group(facts, w) {
		out.Matured++
		if !c.Labelled {
			// Matured and nothing was knowable by its own as-of. It is counted
			// here rather than dropped: it is the denominator's own complement,
			// and it is the difference between "nobody judged these" and "the
			// judgements arrived too late to be usable", which is the question an
			// operator staring at judged=0 is actually asking.
			out.Unlabelled++
			continue
		}
		r := c.Label
		if r.Contested {
			out.Contested++
		}
		if r.Winner.Disposition == Unjudged {
			continue
		}
		judged++
		won[r.Winner.Source]++
		if r.Winner.Source == Sample {
			explore++
		}
		if r.Winner.Disposition == Productive {
			out.Productive++
		} else {
			out.Unproductive++
		}
	}
	out.Judged = judged
	out.Unmatured = out.Events - out.Matured
	if judged > 0 {
		out.Explore = float64(explore) / float64(judged)
	}
	for _, s := range sources() {
		if filed[s] == 0 && won[s] == 0 {
			continue
		}
		out.Sources = append(out.Sources, riskSourceCoverage{Source: string(s), Facts: filed[s], Won: won[s]})
	}
	// A source no longer in the vocabulary still has rows; report it rather than
	// silently dropping its count out of a total that is supposed to add up.
	extra := make([]string, 0)
	for s := range filed {
		if _, known := rank(s); !known {
			extra = append(extra, string(s))
		}
	}
	sort.Strings(extra)
	for _, s := range extra {
		out.Sources = append(out.Sources, riskSourceCoverage{Source: s, Facts: filed[Source(s)], Won: won[Source(s)]})
	}
	// Read, never repaired. Delivery is the write path's job — a read that
	// quietly wrote would be a surprise, and one that blocked on a warehouse
	// would make the training gate unavailable exactly when an operator most
	// needs to see the number.
	if at, err := st.mark(ctx); err == nil {
		out.Pending, _ = st.pending(ctx, at, maxPending)
	}
	return out, nil
}

// ── vocabulary ───────────────────────────────────────────────────────────────

type riskVocabularyIn struct{}

// riskLabelVocabulary publishes the closed sets and the precedence rule.
type riskLabelVocabulary struct {
	// Kinds, Dispositions and Sources are the closed vocabularies. A value
	// outside them is refused at the door.
	Kinds []string `json:"kinds"`
	// Dispositions is the closed set a write's `disposition` must be drawn from,
	// published in full so a caller can validate a batch before filing it instead of
	// discovering a refusal per member: "productive", "unproductive", and "" — the
	// EMPTY STRING is a member and means an explicit unjudged, so a client that
	// filters empties out of this list drops a third of the vocabulary and can never
	// file "we looked and could not say". They are the AML engine's own spelling,
	// verbatim, which is what lets a replay there report against these values.
	Dispositions []string `json:"dispositions"`
	// Precedence is the sources in the order that resolves a conflict, strongest
	// first. It is DERIVED from the same declaration the resolver reads, so the
	// published order is the enforced order and cannot drift from it.
	Precedence []string `json:"precedence"`
	// Rule states the tie-breaks below rank, in order, so a caller reading a
	// contested resolution can reproduce it.
	Rule []string `json:"rule"`
	// Retention is the platform floor in days: no tenant may dispose of a label
	// younger than this, because a label can be the input to an adverse action.
	Retention int `json:"retention"`
}

// vocabulary publishes the closed vocabularies and the precedence rule that
// resolves a conflict between two sources.
//
// A precedence rule nobody can read is a rule nobody can audit or dispute, and
// the whole defensibility of a contested label rests on being able to say why
// one assertion beat another. The order returned here is derived from the same
// declaration the resolver reads — it is not a description of it.
func (o ops) vocabulary(ctx context.Context, _ *riskVocabularyIn) (*riskLabelVocabulary, error) {
	if _, _, err := tenantOf(ctx, o.s); err != nil {
		return nil, err
	}
	out := &riskLabelVocabulary{
		Retention: int(minRetention.Hours() / 24),
		// THE RULE NAMES THE FIELD THE RESOLVER ACTUALLY READS. This op exists so a
		// caller holding a contested resolution can reproduce it, and the second
		// term said `seen` while stronger() compares `knowable` — the derived
		// instant, the later of the filer's `seen` and the server clock at the
		// write. The two are equal for a live pipeline and differ for exactly the
		// history the derivation exists to hold back, so a caller reproducing the
		// published rule on backfilled ground truth got a different winner from the
		// plane and no way to see why. A precedence rule published against a field
		// that decides nothing is worse than none: it is checkable and wrong.
		Rule: []string{
			"rank: the source's adjudication weight, strongest first",
			"knowable: within one rank, the assertion that became KNOWABLE latest wins — knowable is the later of the filer's `seen` and the server clock at the write, and `seen` alone decides nothing — so a source correcting itself wins only from the moment the correction was knowable to this plane",
			"confidence: higher wins, and only within one rank",
			"id: the content digest, lowest wins, so a tie is broken deterministically rather than by storage order",
		},
	}
	for _, k := range kinds {
		out.Kinds = append(out.Kinds, string(k))
	}
	for _, d := range dispositions {
		out.Dispositions = append(out.Dispositions, string(d))
	}
	for _, s := range sources() {
		out.Precedence = append(out.Precedence, string(s))
	}
	return out, nil
}

// ── retention ────────────────────────────────────────────────────────────────

// riskDisposeIn states the retention boundary this tenant is applying.
type riskDisposeIn struct {
	// Before disposes of assertions WRITTEN before this instant, RFC 3339. It is
	// measured against the server clock at the write and not against the event
	// or observation times, both of which the asserting caller supplies — a
	// tenant that could back-date could delete a compliance record on demand.
	Before string `json:"before"`
}

type riskDisposeOut struct {
	// Before echoes the retention boundary that was applied, RFC 3339 in UTC, as
	// this plane parsed it from the request. What was disposed of is every record
	// WRITTEN strictly before it and not under litigation hold — written, measured
	// against the server clock at the write, and not against the event or
	// observation times the asserting caller supplies, because a tenant that could
	// back-date could delete a compliance record on demand. A boundary younger than
	// the platform floor of five years is refused before anything is removed.
	Before string `json:"before"`
	// Disposed is how many whole records were removed. Records are disposed of
	// whole, never redacted: a partially-erased compliance record is one nobody
	// can attest to.
	Disposed int `json:"disposed"`
	// Remaining is how many disposable records are still older than the
	// boundary. A sweep is bounded per call, so a non-zero value here means call
	// again rather than that something failed.
	Remaining int `json:"remaining"`
	// Held is how many records inside the boundary were kept under litigation
	// hold.
	Held int `json:"held"`
	// Restored is how many records this sweep had already removed from the derived
	// columnar copy and then did NOT dispose of, because a litigation hold arrived
	// between the identify and the delete — and which were therefore written back
	// to the derived copy before this answered.
	//
	// It is a NAMED state and not a silent repair. The copy is swept before the
	// record so nothing is orphaned in the warehouse, which means a record the
	// delete declines to remove is one the warehouse has already lost, with its
	// seq behind the delivery cursor and no retry that can reach it. Non-zero here
	// says the collision happened and was repaired; a non-zero that keeps
	// recurring says retention and hold are racing on the same records, which is
	// worth an operator's attention rather than a debug line.
	Restored int `json:"restored,omitempty"`
	// Total and Oldest describe what the tenant still holds afterwards, so a
	// disposal that removed nothing is distinguishable from a tenant that had
	// nothing.
	Total int `json:"total"`
	// Oldest is the WRITE time of the oldest assertion this tenant still holds after
	// the sweep, RFC 3339, and it is omitted exactly when nothing remains at all.
	// Still older than Before means records survived on purpose and says which
	// mechanism kept them: a litigation hold (Held), or the per-call bound with more
	// to sweep on the next call (Remaining).
	Oldest string `json:"oldest,omitempty"`
}

// dispose applies this tenant's retention, and only this tenant's.
//
// It is bounded three ways, each a compliance property rather than a
// convenience. It refuses a boundary younger than the platform floor, because a
// label can be the input to an adverse action and five years is what the
// retention ledger holds such a record for. It never touches a record under
// litigation hold. And it disposes of whole records rather than redacting
// fields.
//
// It removes the derived columnar copy BEFORE the record, and refuses the whole
// disposal if the warehouse cannot be reached. The other order would leave rows
// in the warehouse that nothing can identify any more, which is a disposal that
// did not happen and says it did.
func (o ops) dispose(ctx context.Context, in *riskDisposeIn) (*riskDisposeOut, error) {
	sc, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if in == nil || strings.TrimSpace(in.Before) == "" {
		return nil, zip.Errorf(http.StatusBadRequest, "no boundary, so nothing states what is expired")
	}
	before, err := stamp(in.Before)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "before: %v", err)
	}
	floor := time.Now().UTC().Add(-minRetention)
	if before.After(floor) {
		return nil, zip.Errorf(http.StatusUnprocessableEntity,
			"a label can be the input to an adverse action, so it is kept at least %d days; the earliest boundary is %s",
			int(minRetention.Hours()/24), floor.Format(time.RFC3339))
	}

	// Identify exactly what is being disposed of, bounded, before anything is
	// removed. The ids are what the columnar delete binds, so the two planes
	// dispose of the same rows or neither does.
	expired, held, remaining, err := st.expired(ctx, before, maxDispose)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "the record plane could not be read")
	}
	var kept []string
	if len(expired) > 0 {
		if err := o.s.State.derived.sweep(ctx, sc.tenant, expired); err != nil {
			o.s.Log.Warn("label: the columnar copy could not be disposed of; the record is kept",
				"tenant", sc.tenant.String(), "err", err)
			return nil, zip.Errorf(http.StatusServiceUnavailable,
				"the derived copy could not be reached, so disposing here would leave rows in the warehouse: %v", err)
		}
		kept, err = st.remove(ctx, expired)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "the record plane could not be written")
		}
		// A HOLD THAT ARRIVES MID-SWEEP KEEPS THE RECORD IN BOTH PLANES OR IN
		// NEITHER. The copy is swept FIRST so nothing is orphaned in the warehouse,
		// which means a record the delete then declines to remove is one the
		// warehouse has already lost — its seq is behind the delivery cursor, and
		// deliver() asks the cursor rather than the world, so no retry re-sends it
		// and pending() answers zero. The row would be present in the record,
		// absent from the answer key a training join reads, and the row it happens
		// to is the one somebody is litigating.
		//
		// So the repair is here, from the record that is still there, and a repair
		// that cannot be made FAILS the request: telling a tenant its hold held
		// while the copy it trains on quietly lost the row is the shape of defect
		// this plane exists to prevent. Retrying is free — the sweep identifies the
		// same rows again and the columnar copy collapses a byte-identical re-send.
		if len(kept) > 0 {
			facts, err := st.byIDs(ctx, kept)
			if err != nil {
				return nil, zip.Errorf(http.StatusInternalServerError, "the record plane could not be read")
			}
			if err := o.s.State.derived.send(ctx, sc.tenant, facts); err != nil {
				o.s.Log.Error("label: a hold kept records the derived copy had already lost, and they could not be written back",
					"tenant", sc.tenant.String(), "kept", len(kept), "err", err)
				return nil, zip.Errorf(http.StatusServiceUnavailable,
					"%d records were placed under litigation hold during this sweep and had already been removed from the derived copy; writing them back failed, so the copy is short and this sweep is not acknowledged; retry: %v", len(kept), err)
			}
			o.s.Log.Warn("label: a litigation hold arrived mid-sweep; the records were kept and written back to the derived copy",
				"tenant", sc.tenant.String(), "restored", len(kept))
		}
		// SHIP BEFORE ACK, for the same reason the write path does and with the
		// direction reversed: an unshipped disposal is a tenant told its records
		// are gone, and a successor pod that hydrates the older snapshot brings
		// every one of them back.
		if err := o.s.State.ship(sc.ns); err != nil {
			o.s.Log.Error("label: records were disposed of and the disposal could not be shipped",
				"tenant", sc.tenant.String(), "err", err)
			return nil, zip.Errorf(http.StatusServiceUnavailable,
				"the disposal was not acknowledged as durable, so it is not acknowledged at all; retry: %v", err)
		}
	}
	total, oldest, err := st.count(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "the record plane could not be read")
	}
	// Disposed is what was DISPOSED OF: what was identified, less what a hold
	// kept. Reporting the identified count told a tenant its retention had removed
	// a record it is still holding, and a compliance report that overstates a
	// deletion is not a rounding error — it is the wrong answer to the only
	// question the report is asked.
	out := &riskDisposeOut{
		Before: before.Format(time.RFC3339), Disposed: len(expired) - len(kept),
		Remaining: remaining, Held: held, Total: int(total), Restored: len(kept),
	}
	if !oldest.IsZero() {
		out.Oldest = oldest.Format(time.RFC3339)
	}
	o.s.Log.Info("label: retention applied", "tenant", sc.tenant.String(),
		"before", out.Before, "disposed", out.Disposed, "held", out.Held,
		"restored", out.Restored, "remaining", out.Remaining)
	return out, nil
}

// ── litigation hold ──────────────────────────────────────────────────────────

// riskHoldIn names the records a hold is placed on or released from.
type riskHoldIn struct {
	// IDs are the content digests of the records, as returned by the write and
	// by the read. They name records in THIS tenant's plane; an id belonging to
	// anybody else names nothing here, because the statement runs against this
	// tenant's own file and there is no other file it could reach.
	IDs []string `json:"ids"`
	// Hold is the state to put them in: true places the hold, false releases it.
	// One op both ways, because a hold that can be placed and not released pins a
	// compliance record past every retention boundary with nothing able to let it
	// go — and an operator who cannot release a hold stops placing them.
	Hold bool `json:"hold"`
}

type riskHoldOut struct {
	// Hold echoes the state asked for.
	Hold bool `json:"hold"`
	// Changed is how many records moved into that state. A record already in it
	// is not counted and is not an error: the op is idempotent, so a retry after
	// a network failure is safe.
	Changed int `json:"changed"`
	// Missing is how many of the named ids this tenant does not hold. It is
	// reported rather than refused, so a sweep over a list that includes disposed
	// records still places every hold it can — but it is REPORTED, because a hold
	// that silently did nothing is a compliance control that lies.
	Missing int `json:"missing"`
	// Held is how many records this tenant is now holding, at any age. Retention
	// never disposes of one.
	Held int `json:"held"`
}

// hold places or releases a litigation hold on named records.
//
// A hold is a fact about the RECORD, not about the world: it says retention may
// not dispose of this row, and it asserts nothing about what happened. So it is
// not a field on an assertion and it is not folded into the content digest —
// carried there it was silently a no-op on any record that already existed, since
// re-filing the same assertion with a hold flag produced the same digest, the
// insert was ignored, and the caller was answered `duplicate` while the hold it
// asked for was never placed. This op is the one way a hold moves, in either
// direction, and the move is written to the audit log.
//
// Every named id is this tenant's or is nothing. The statement runs against the
// tenant's own file, which holds no other tenant's rows and has no column that
// could name one.
func (o ops) hold(ctx context.Context, in *riskHoldIn) (*riskHoldOut, error) {
	sc, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if in == nil || len(in.IDs) == 0 {
		return nil, zip.Errorf(http.StatusBadRequest, "no ids, so the hold names no record")
	}
	if len(in.IDs) > maxHold {
		return nil, zip.Errorf(http.StatusBadRequest, "%d ids exceeds the bound of %d", len(in.IDs), maxHold)
	}
	// One entry per distinct id: a list naming a record twice must not report it
	// changed twice, and must not bind it twice.
	ids := make([]string, 0, len(in.IDs))
	named := make(map[string]struct{}, len(in.IDs))
	for i, raw := range in.IDs {
		id := strings.TrimSpace(raw)
		switch {
		case id == "":
			return nil, zip.Errorf(http.StatusBadRequest, "ids[%d] is empty", i)
		case len(id) > idMax:
			return nil, zip.Errorf(http.StatusBadRequest, "ids[%d] is longer than %d bytes", i, idMax)
		}
		if _, dup := named[id]; dup {
			continue
		}
		named[id] = struct{}{}
		ids = append(ids, id)
	}

	changed, present, err := st.setHold(ctx, ids, in.Hold)
	if err != nil {
		o.s.Log.Error("label: the hold could not be written", "tenant", sc.tenant.String(), "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "the record plane could not be written")
	}
	if changed > 0 {
		// SHIP BEFORE ACK. A hold that a rollout forgets is a record disposed of
		// while somebody believed it was preserved.
		if err := o.s.State.ship(sc.ns); err != nil {
			o.s.Log.Error("label: the hold was written and could not be shipped",
				"tenant", sc.tenant.String(), "err", err)
			return nil, zip.Errorf(http.StatusServiceUnavailable,
				"the hold was not acknowledged as durable, so it is not acknowledged at all; retry: %v", err)
		}
	}
	held, err := st.held(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "the record plane could not be read")
	}
	o.s.Log.Info("label: litigation hold", "tenant", sc.tenant.String(), "by", sc.by,
		"hold", in.Hold, "named", len(ids), "changed", changed, "missing", len(ids)-present, "held", held)
	return &riskHoldOut{Hold: in.Hold, Changed: changed, Missing: len(ids) - present, Held: held}, nil
}

// ── shared ───────────────────────────────────────────────────────────────────

func render(f Fact) riskLabelRecord {
	return riskLabelRecord{
		ID: f.ID, Kind: string(f.Kind), Subject: f.Subject,
		At: f.At.Format(time.RFC3339), Seen: f.Seen.Format(time.RFC3339),
		Knowable:    f.Knowable.Format(time.RFC3339),
		Disposition: string(f.Disposition), Source: string(f.Source),
		Evidence: f.Evidence, By: f.By, Confidence: f.Confidence,
		Hold: f.Hold, Wrote: f.Wrote.Format(time.RFC3339),
	}
}

// readErr separates a read that was too wide from a read that failed. The first
// is the caller's to fix and says exactly how — narrow it — and returning a 500
// there would tell an operator the plane is broken when it is working precisely
// as designed. The second says nothing about the store's internals.
func readErr(err error) error {
	if errors.Is(err, errTooWide) {
		return zip.Errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	return zip.Errorf(http.StatusInternalServerError, "the record plane could not be read")
}

// stamp parses a required RFC 3339 instant. It refuses anything else rather than
// defaulting to now: a label whose time was invented by the parser is a label
// whose maturity is invented too.
//
// The length is checked BEFORE the parse and before the value reaches an error
// message: this is the one parser every instant on this surface goes through, so
// it is the one place a time field is bounded, and %q on a megabyte that is not a
// timestamp is a megabyte in a log line.
func stamp(s string) (time.Time, error) {
	if len(s) > instantMax {
		return time.Time{}, fmt.Errorf("an instant is %d bytes and the bound is %d", len(s), instantMax)
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not an RFC 3339 instant", s)
	}
	return t.UTC(), nil
}

// optional parses an instant that may be absent. Absent is the zero time; present
// and malformed is still a refusal.
func optional(s string) (time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, nil
	}
	return stamp(s)
}
