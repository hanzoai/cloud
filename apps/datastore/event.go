// The event plane's names, in the ONE package every reader already imports.
//
// An event is the FACT; a message is the container it travels in. This file
// names facts, so the database is named for what it holds and each table for
// what it IS — singular, because a row is one of them. The subject a fact
// travels on and the table it lands in carry the SAME name (event.span ->
// event.span), which is what lets a transport question and a storage question be
// answered by one word.
//
// They live here rather than in each reading package because a table is one
// fact: apps/o11y, apps/admin and apps/eval all read the span plane, and three
// spellings of one name is how a rename leaves two of them querying a table that
// no longer exists — the state this plane was found in. This package already owns
// the connection those reads run on, so it is where the names belong.
//
// The first fifteen columns are IDENTICAL across event, error, log and span —
// org, time, ingested_at, id, name, kind, product, session_id, distinct_id,
// anonymous_id, person_id, url, path, attributes, el — so a cross-kind read is a
// UNION ALL and not a translation layer. org leads every sort key, so a
// single-tenant read is a primary-key seek rather than a scan behind a map
// lookup.
//
// Fully qualified on purpose. A connection may carry a default database, and a
// query whose meaning depends on that is a query that reads a different table
// depending on who opened the connection.
package datastore

const (
	// Event is what someone did — kind = track | page | identify | group.
	Event = "event.event"
	// Error is what broke, grouped for issue lists.
	Error = "event.error"
	// Log is what a service said.
	Log = "event.log"
	// Span is what a service did, and how long it took.
	Span = "event.span"
	// Metric is a sample; Series is the dimension table its fingerprint resolves
	// through, and every metric read INNER JOINs it.
	Metric = "event.metric"
	Series = "event.series"
)

// SpanError is the value event.span.status carries for a failed span. It is a
// string because the span's outcome is one of a small named set, not a number to
// be compared against — `status = 'error'` says what it means where a
// `status_code = 2` needed a comment to.
const SpanError = "error"
