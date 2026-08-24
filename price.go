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

import (
	"strconv"
	"strings"
)

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

// Consumes reports whether an OPERATION spends resource somebody has to pay a
// provider for. It is the one fact that lets a price be declared per SURFACE
// without a per-route table: a surface's cost basis is shared by its writes, and
// its reads, with the exception below, have no cost basis at all.
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
// one outage, a balance view that 402s is unusable." What is new is that the
// CHARGE now asks the same question the STANDING check does, from the same line,
// so the two can no longer disagree about what a read is.
//
// IT TAKES THE PATH BECAUSE THE METHOD IS NOT THE OPERATION. The rest of that old
// sentence — "no read this binary serves calls a paid provider" — is measurably
// false, and reading the method alone is what made it unanswerable: a search
// embeds its query through the metered AI client, and the object plane meters
// every request its guard wraps. GET said nothing about whether money moved, so
// those operations were outside the standing gate by construction, and no
// declaration anywhere could bring them in. An operation that spends is not a
// read, whichever verb it answers on.
func Consumes(method, path string) bool {
	switch method {
	case "GET", "HEAD", "OPTIONS":
		for _, r := range paidReads {
			if under(path, r) {
				return true
			}
		}
		return false
	}
	return true
}

// paidReads are the operations whose READ IS the paid unit of work — the whole
// exception to "a read spends nothing", written where the rule is.
//
// price.go's own answer for this class used to be that such a surface "meters its
// own units downstream and declares Metered", and that half is right: the edge
// still charges nothing for them (each declares Metered, so Price.Cents is 0, and
// an edge charge on top of a downstream meter bills the same work twice). What
// the declaration could NOT say was that the READ spends, so Billable never asked
// for standing and the meter ran on whatever the caller had. This list is that
// missing half, and it is the only thing here the method cannot answer.
//
// It is short by construction and it is MEASURED — each entry names the line that
// moves the money — because a surface that wants to spend on a read has to be
// written down here, in the file the money rule lives in, rather than acquiring
// the property by adding a call inside a handler.
//
// Each entry is a root and covers everything beneath it, asked through scope.go's
// `under` so the comparison is the one the ROUTER makes: /V1/S3/... reaches the
// route registered at /v1/s3/..., and a raw prefix test would answer "not on the
// list" for a request that is about to spend.
//
// AN ENTRY HERE CARRIES A SECOND OBLIGATION, and it is on the surface rather than
// on this file: a read that spends can be driven by a page the caller never
// visited, because a browser sent there carries the cookie it already holds and
// the debit lands on them. So the surface named here also asks for the
// anti-forgery token on the ambient-cookie path — account.RequireCSRFOnSpend on
// its group, which reads Consumes below so the two can never disagree about which
// requests spend, or account.CSRF in the operation's own preamble where the
// operation is reachable by name as well as by route. Both cost a caller that
// presents any credential nothing.
var paidReads = []string{
	"/v1/code/ask",         // the answer is synthesized through deps.AI (apps/code/ask.go).
	"/v1/code/search",      // the semantic tier embeds the query through deps.Embed (apps/code/search.go).
	"/v1/websearch/search", // one debit per answer a bought engine served (apps/websearch/meter.go).
	"/v1/s3/",              // the guard meters every request it wraps (apps/s3/s3.go); the probe is outside it.
}

// DefaultPrice is what ONE invocation of the operation (method, path) costs.
//
// IT TAKES TWO STRINGS, AND THAT IS THE WHOLE POINT. It used to take a *zip.Ctx,
// which made the price a property of an HTTP REQUEST — so the only client that could
// ask it was HTTP middleware, and middleware reads the path the TRANSPORT carried.
// Over MCP that path is /mcp; over the ZAP plane it is /.well-known/zip/op/<name>.
// Neither names a declared surface, so both resolved to Undeclared and both were
// free, silently and permanently, however the operation inside them was priced.
// A price is a fact about an operation, and an operation is a method and a path,
// so those are the arguments. Every client that knows an operation can now ask.
//
// It holds NO table: it reads the price the surface DECLARED at the composition
// root (Plugin.Price → PriceOf), so the number the gate charges and the number a
// reviewer approved are the same number, in one place. It used to be the table,
// and its last line was `return 0` for anything unlisted — which made a new route
// free forever. That default is gone: an unpriced surface fails TestPriceDeclared
// before it can ship. Undeclared still charges nothing HERE (Price.Cents), because
// a missing declaration must break the build, never a customer's card.
//
// Two things it does on its own:
//
//   - Health probes are Free regardless of the surface they sit under. A liveness
//     probe that 402s hides whether the process is up, and /v1/<svc>/health is a
//     route of the same surface as everything else under /v1/<svc> — so the moment
//     any surface carries a positive price, its probe has to be exempted here or the
//     exemption has to be written 119 times.
//   - A surface declared Metered charges 0, because its meter is downstream: an edge
//     charge on top of it bills the same work twice. That is Price.Cents's job, not a
//     prefix list's.
func DefaultPrice(method, path string) int64 {
	// Liveness/health probes are never billed.
	if path == "/health" || path == "/healthz" || strings.HasSuffix(path, "/health") {
		return 0
	}
	// A read spends nothing, so a read costs nothing — the SAME rule the standing
	// gate applies (Consumes, above). Without it a declared surface price bills its
	// own listings and its own error pages, which is why every surface in the fleet
	// was Free: the declaration had no way to say "charge the work, not the index".
	// Checked before the price so an unpriced read costs nothing either way and the
	// two paths cannot diverge.
	if !Consumes(method, path) {
		return 0
	}
	return PriceOf(path).Cents()
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
