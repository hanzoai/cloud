// Package postgres is the hanzo-sql engine of the SDK's locus package: a schema on the one
// shared PostgreSQL per store, a connection budget for the process, the registry, the
// leases, and the mover that carries a store from its file to the server. It registers
// itself into locus.Engine from init (engine.go).
//
// For a file it is one sentence — one connection — and it is a package so that the
// sentence has somewhere to be true. It used to be written at every store that opened a
// database, together with a block of PRAGMAs restating the driver's own defaults;
// fifty-odd copies of a fact is fifty-odd chances for one of them to drift.
//
// The PRAGMAs are gone because hanzoai/sqlite already applies them, on every
// connection, on every backend — see sdk/locus pragma_test.go, which asserts exactly that
// and fails if it ever stops being true. What is NOT already applied everywhere
// is the pool cap, so that is what remains here.
package postgres
