// Package twopod builds a fleet of replicas that share one object store and
// nothing else — the arrangement production runs in and the one a test is most
// likely to leave out.
//
// WHY IT EXISTS. Concurrency inside a process is easy to model and is not the
// hazard. Twenty-five goroutines on one *sql.DB exercise the SQLite compare-and-set
// beautifully and prove nothing about two pods, because the two pods are CASing
// two DIFFERENT FILES: each has its own volume, each reads its own copy, and each
// wins its own CAS. Every conclusion drawn from the single-file harness — the
// watermark serializes the meter, the ledger's idempotency key stops a double
// post, the reaper cannot end a lease twice — is true within one process and says
// nothing about the fleet. A test has to hold two files or it is testing the
// wrong object.
//
// WHAT A POD IS HERE. A DataDir of its own (the RWO volume), a Durability of its
// own over the SHARED object store (the S3 bucket every replica ships to), and a
// membership view onto one converged set (what the K8s API tells every pod). That
// is the whole of what makes a replica a replica, so `cloud.Base` built from it
// behaves exactly as it does in the cluster: OrgStore hydrates before it opens,
// the fencer elects one writer per org, and a ship at a stale round is refused.
//
// Two things are DELIBERATELY not modelled, and both would make the instrument
// lie. There is no network, so a ship is instantaneous — which removes latency and
// leaves ORDER, and order is what the fence is about. And the object store is a
// map rather than S3, so it is linearizable by construction; that is the property
// `org.ProbeCAS` proves of the real one at boot, so assuming it here is assuming
// what production checks rather than what production hopes.
package twopod
