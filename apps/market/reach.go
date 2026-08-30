package market

// reach.go — how far a read got, which is the ONE thing every answer here carries.
//
// Four figures on the exchange's front page are somebody else's arithmetic, and
// the interesting question about each is not what the number is but whether there
// was one. There are several distinct ways for there not to be, and a screen that
// draws 0 collapses every one of them into "this venue is worth nothing".
//
// So the distinction is made HERE, on the wire, where it can be tested, instead of
// being rediscovered by each caller from an empty slice. The vocabulary is the
// interface's own (apps/web/src/read/front.ts, `Answer<T>`): a caller reads the
// same words off this API that it reads off its own chain client, and needs no
// table to translate between them.
//
// A NIL SLICE AND AN EMPTY ONE MEAN THE SAME THING IN JSON, which is why the state
// is a field and never an absence. Hanzo, Pars and Osage have no AMM deployed; their
// indexer answers `{"data":{"factories":[]}}` at 200, and that is a TRUE sentence
// about those chains — Read with nothing in it. An indexer that never answered is
// Unreachable and knows nothing about the chain either way. Told apart here, a
// person can be shown "no pools yet" for the first and "we could not ask" for the
// second; flattened, both render as a dead venue and one of them is a lie.

// The states a read can end in. They are strings on the wire because the caller
// switches on them and a number would make the client's code unreadable.
const (
	// Read: the upstream answered. The payload is what it said — INCLUDING nothing,
	// which is an answer and not a failure.
	Read = "read"

	// Unconfigured: nobody deployed the thing that would answer. Devnet has no
	// indexer; a chain the registry carries with its graph switched off has none
	// either. It is a fact about the deployment, not a failure of this call, and
	// nothing is retried by reporting it.
	Unconfigured = "unconfigured"

	// Unreachable: the request never completed. DNS, connection, timeout, or a
	// response this side could not parse. It says nothing about the chain.
	Unreachable = "unreachable"

	// Refused: the upstream is up and would not serve the query. A GraphQL server
	// rejects an unknown field with HTTP 200 and an `errors` array, so a client that
	// reads only the status code turns a refusal into zero rows — which renders as a
	// venue with no pools. That is a fabrication told backwards, and it is why this
	// state exists apart from the one above.
	Refused = "refused"
)

// There is deliberately no `reading`.
//
// The interface has one, because a browser holds a request in flight and has to
// draw something meanwhile. A response cannot: by the time these bytes exist the
// read is over. Carrying the word anyway would put a state on the wire that no
// answer could ever truthfully hold, and every caller would have to write a branch
// it could not reach. The interface keeps `reading` where it belongs — in front of
// the call, not in its result.

// Reach is where a read got to, and why if it did not arrive.
//
// It is embedded in every answer rather than wrapping one, so a chain row can carry
// its OWN reach beside its own figures: one indexer being down makes one row say so
// and leaves the other four to answer. A single outcome for the whole response would
// let the worst chain speak for all of them.
type Reach struct {
	// At is `read`, `unconfigured`, `unreachable` or `refused`.
	//
	// The four values are written out here because this document cannot carry an
	// enum, so the description IS the contract a client reads. Spelling the Go
	// constant names instead would name four identifiers no caller can see.
	At string `json:"at"`

	// Why is the upstream's own words, on Unreachable and Refused, and empty
	// otherwise. It is the upstream's and not ours: a failure reported without its
	// reason sends the reader to look at the venue, which is the one place the fault
	// is not.
	Why string `json:"why,omitempty"`
}

// arrived reports whether the upstream answered at all. It is the one predicate
// worth naming, because "did this row's figures come from anywhere" is the question
// every consumer of an answer asks first.
func (r Reach) arrived() bool { return r.At == Read }

// read is the reach of an upstream that answered.
func read() Reach { return Reach{At: Read} }

// unconfigured is the reach of a thing nobody deployed. It carries no reason: there
// is no failure to explain, and inventing a sentence here would make an operator go
// looking for an outage that is not happening.
func unconfigured() Reach { return Reach{At: Unconfigured} }
