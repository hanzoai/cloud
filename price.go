package cloud

// price.go — what a surface costs is part of DECLARING it, not something added to
// it afterwards.
//
// THE BUG THIS FILE EXISTS TO CLOSE. The edge gate answered "what does this path
// cost?" from a table inside itself whose last line was `return 0` — "metering is
// opt-in per path so a new route never silently starts billing". The intent was to
// avoid over-billing. The cost was the opposite asymmetry: over-billing is loud (a
// customer complains) and under-billing is SILENT and PERMANENT, because free never
// errors. A route nobody priced was free forever and nothing anywhere said so.
//
// So the price moved to where the surface is declared: App.Price, one field
// beside Name in the composition root. Its zero value is Undeclared, which is not a
// price but an unanswered question, and TestPriceDeclared fails on it. A new
// subsystem therefore cannot reach main until someone writes down what it costs, and
// the number lands in the same diff as the routes. There is no unpriced route at
// runtime to catch, so there is no runtime gate to maintain, and the failure lands on
// the author instead of on a customer.
//
// GRANULARITY: THE SURFACE, NOT THE ROUTE. A subsystem and the subtrees it owns are
// one surface; its routes share a cost basis by construction, because they call the
// same providers through the same meter. Pricing each of ~1100 routes separately
// would be a list nobody can keep true — the drift this file is meant to remove. So
// a route added to a declared surface adopts that surface's price, deliberately. A
// route whose cost basis DIFFERS is a different surface: it gets its own spec, and
// its own declaration.
//
// WHY NOT AN INT. Free and Metered are not the number 0; they are two different
// reasons the edge charges nothing, and the difference decides whether the request
// needs standing at all (spend.go's Billable). Collapsing them into 0 is the fusion
// spend.go already had to split apart — "we charge nothing HERE" silently meaning
// "we authorize NOTHING here". They stay distinguishable values.

import "strconv"

// Price is what ONE request to a subsystem's surface costs at the edge gate.
//
// A positive value is that many cents, charged per request by BillingGate. The three
// non-positive values are named, and each says something the number 0 cannot:
//
//	Undeclared — nobody has answered. The zero value, and a CI failure.
//	Free       — costs nothing, decided on purpose.
//	Metered    — a meter downstream of the edge owns the charge.
//
// It is deliberately NOT a struct: one field on 111 specs has to read as one line.
type Price int64

const (
	// Undeclared is the zero value — the surface's cost is an open question. It is
	// not Free: nobody chose, and TestPriceDeclared fails until somebody does.
	// The edge charges nothing for it (see Cents), so forgetting a declaration can
	// never over-bill a customer; it fails the build instead.
	Undeclared Price = 0

	// Free — this surface costs nothing, on purpose. Health probes, auth, reads, the
	// path to payment itself. Free IS a price: explicit, and reviewed in the diff.
	Free Price = -1

	// Metered — the charge for this surface is owned by a meter DOWNSTREAM of the
	// edge: the subsystem's own ResourceMeter, or the model plane's token meter. The
	// edge must add nothing on top or every request is billed twice. This is the value
	// that says "money moves here, just not here" — spend.go's Billable is what
	// requires standing before it runs.
	Metered Price = -2
)

// Cents is what the edge gate charges for one request: the declared cents when the
// surface is priced at the edge, and zero for every other value. Undeclared charges
// nothing — a missing declaration must fail the build, never a customer's card.
func (p Price) Cents() int64 {
	if p > 0 {
		return int64(p)
	}
	return 0
}

// Declared reports whether somebody answered what this surface costs.
func (p Price) Declared() bool { return p != Undeclared }

// Consumes reports whether a request by this METHOD spends resource somebody has
// to pay a provider for. It is the one fact that lets a price be declared per
// SURFACE without a per-route table: a surface's cost basis is shared by its
// writes, and its reads have no cost basis at all.
//
// THE BUG IT CLOSES, MEASURED. DefaultPrice priced by PATH alone, so a surface
// declared at 25¢ charged 25¢ for `GET /v1/<surface>/list` — a directory listing
// billed like the work it lists. That is why no surface in the fleet had ever
// declared a positive price: the only safe declaration was Free, and 95 of them
// took it. The read/write distinction is a RULE, not a table, so it belongs in
// one function rather than in 1100 route declarations nobody can keep true.
//
// The rule and its justification are not new here — spend.go's Billable has
// carried them since the standing gate was written: "gating reads already caused
// one outage, a balance view that 402s is unusable, and no read this binary
// serves calls a paid provider." What is new is that the CHARGE now asks the same
// question the STANDING check does, from the same line, so the two can no longer
// disagree about what a read is.
//
// A surface whose paid unit of work is a read is therefore unpriceable at the
// edge, deliberately. It is not a gap to paper over with a second knob: such a
// surface meters its own units downstream and declares Metered.
func Consumes(method string) bool {
	switch method {
	case "GET", "HEAD", "OPTIONS":
		return false
	}
	return true
}

// String renders the declaration for logs, the admin inventory and test failures.
func (p Price) String() string {
	switch p {
	case Undeclared:
		return "undeclared"
	case Free:
		return "free"
	case Metered:
		return "metered"
	default:
		return strconv.FormatInt(int64(p), 10) + "c"
	}
}

// MarshalText renders the same word JSON readers see, so /v1/admin/subsystems
// reports "free" / "metered" / "5c" rather than a bare -1 nobody can read.
func (p Price) MarshalText() ([]byte, error) { return []byte(p.String()), nil }
