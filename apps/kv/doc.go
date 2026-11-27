// Package kv is your key-value store: buckets of versioned values your apps
// read and write by key.
//
// A bucket holds values. Each key keeps up to History revisions, entries can
// expire by TTL, and every write is a new revision rather than an overwrite —
// so a read can ask for the value or for how it got there. Six typed ops at
// /v1/kv (create and drop a bucket; get, put, delete and history a key), each
// org confined to its own buckets by the validated principal, never by anything
// a caller asserts.
//
// # WHY IT IS NOT PUBSUB
//
// It sat under /v1/pubsub because the engine behind both is NATS, which ships a
// key-value store alongside its bus. That is an implementation's packaging
// deciding a product's shape — the same error jetstream made by naming an
// engine in an address. Nothing about a bucket publishes, subscribes or waits
// for a reply, so a caller reading /v1/pubsub/kv learns the wrong thing about
// what it is holding. The address moved first (HIP-0139 §3: a capability's
// routes are under its own name), and the name has now followed it: kv is its
// own capability, its own package, its own binary, its own row.
//
// # ONE BUS, NOT A SECOND ONE
//
// It is still the same plane. apps/pubsub runs the ONE embedded NATS +
// JetStream node this cloud has, and this app rides it through the four calls
// that package exports for exactly this — [bus.Bus] to reach it,
// [bus.Org] for who is asking, [bus.Qualify] for what a caller's bucket
// is called out there, and [bus.Err] for what a refusal from it means on the
// wire. Composition, not duplication: no second server, no second connection
// policy, and one place the tenancy rule is written.
//
// In its own binary that dial goes over CLOUD_PUBSUB_URL — the ONE bus knob,
// defaulting to the loopback address the embedded server binds — so the two
// products are one process apart and zero servers apart.
//
// Mount registers routes and nothing else. It does NOT check the bus first: the
// plane is another process, so refusing to mount until it answers would make
// boot order load-bearing and turn a slow neighbour into this app's outage.
// [bus.Bus] fails closed per request instead — 503 while the plane is
// unreachable, and correct the moment it is back.
//
// Values are TEXT. `value` is a JSON string carried verbatim as UTF-8 bytes, so
// its round trip is exact; bytes written on the NATS port that are not UTF-8
// read back lossily here.
package kv
