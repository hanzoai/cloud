package bus

// budget.go — how the plane's file store is divided between the platform's streams.
//
// A stream that states MaxBytes RESERVES that many bytes the moment it is declared,
// empty or not, and JetStream refuses a declaration whose reservation does not fit
// what is left (API error 10047, "insufficient storage resources available"). So
// the ceilings are not independent facts: together they must fit the store the
// plane actually has, and that store is a production volume in one place and a
// laptop's microVM in another.
//
// A stream therefore states two things, its production ceiling and its share of the
// store, and holds the smaller. Both live in this one table, so what they add up to
// can be read and tested. The store they are a share of is the JetStream account's
// file limit, which the bus itself reports (AccountInfo) and every client can read:
// a shared cluster's operator sets it, and the embedded server states its own when
// the node is too small for the ceilings or the deployment declares a budget
// (apps/pubsub, CLOUD_PUBSUB_MAX_STORE).

// Reservation is what one platform stream may hold of the plane's file store.
type Reservation struct {
	// Ceiling is the stream's size in production, in bytes.
	Ceiling int64
	// Share is its part of a store too small for that, in percent.
	Share int
}

// The reserving streams, all of them. What the shares leave over is for the streams
// that state no ceiling (COMMERCE, the org streams of /v1/mq, the KV buckets), which
// reserve nothing and grow into what is free.
var (
	// Events is EVENT, the event plane's hand-off log (apps/event): the pod's
	// ceiling for facts no consumer has drained yet.
	Events = Reservation{Ceiling: 8 << 30, Share: 50}
	// Usage is USAGE, the debits on their way to the ledger (internal/driver/ledger).
	Usage = Reservation{Ceiling: 1 << 30, Share: 10}
)

// Reservations is every row above.
var Reservations = []Reservation{Events, Usage}

// Of is what the stream reserves on a plane whose file store is `store` bytes: its
// ceiling, or its share of a store too small to hold that. A store that states no
// limit (zero or negative, which is how JetStream spells "unlimited") leaves the
// ceiling as it is.
func (r Reservation) Of(store int64) int64 {
	if store <= 0 {
		return r.Ceiling
	}
	// In two parts, so a store of any size divides exactly and never overflows.
	p := int64(r.Share)
	return min(r.Ceiling, store/100*p+store%100*p/100)
}

// Need is the store that holds every ceiling at once: below it, a stream is refused
// unless the shares apply.
func Need() int64 {
	var n int64
	for _, r := range Reservations {
		n += r.Ceiling
	}
	return n
}

// Reserved is the part of a limited store the reserving streams take, in percent.
func Reserved() int {
	n := 0
	for _, r := range Reservations {
		n += r.Share
	}
	return n
}
