// Package datastore is cloud's one connection to the analytics warehouse — the
// columnar store behind usage, observability and billing.
//
// Every read and write of a warehouse table in this
// binary goes through it. It is NOT the tenant-facing Hanzo Datastore product
// (apps/provisioning's `datastore` kind at /v1/datastore, a dedicated instance
// per org); one word, two things, and this is the platform's own handle.
//
// One store, not one per tenant. Rows carry the tenant as a column and as the
// leading sort key, so a single-org read is a bound predicate over a shared
// table (WHERE organization = ?) and a fleet-wide aggregate is the same table
// with that predicate left off. That is the opposite of the relational plane in
// orgdb.go, where a tenant IS a SQLite file and a cross-tenant read is
// structurally impossible. The two planes share no connection and are
// configured independently; keeping them apart is what lets the leaderboard and
// the admin fleet views span every org while an entity read cannot leave one.
//
// The connection opens from the environment on first use — DATASTORE_ADDR and
// friends, read by datastore.Env. Open never blocks and never fails, so a
// warehouse that is down at boot leaves the ledgers unavailable rather than
// taking down the process that owns them; callers gate on Ready and report an
// honest gap instead of fabricating zeroes. Because the connection is
// established once and never replaced, there is no wiring order to get wrong
// and no window in which a caller sees a half-built client.
package datastore
