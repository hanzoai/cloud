// Package dataset is the per-org dataset plane of /v1/risk: a dataset is a
// VERSIONED, IMMUTABLE snapshot of one tenant's own event surface, and this is
// where it is declared, materialised, described, exported and disposed of.
//
// THE ADDRESS IS THE PRODUCT, and that is why it is /v1/risk and not /v1/ml.
// openapi.Product reads an operation's product off the FIRST /v1 segment of its
// path and nothing else, so an address is a published product membership: the
// fleet's tag list, the floor ratchet, the doc headings, every generated SDK's
// namespace and the CLI's command tree are projections of that one segment. The
// rows here feed the RISK model, which learns in-process from the org's own
// events; they are not served by KServe. /v1/ml is the model-SERVING plane
// (apps/ml: InferenceServices, /v1/ml/models, /v1/ml/models/{name}/predict) — a
// different live product with its own consumers — so publishing seven dataset
// operations there filed them into it, and nothing in the fleet said so: the
// floor ratchet read `ml: 7 -> 14` as growth, because it refuses a shrink and
// only a shrink. apps/label and apps/reference each corrected the same address
// once; address_test.go makes it a gate here rather than a third recollection.
//
// WHY A DATASET IS A VALUE AND NOT A QUERY. Storing a spec and re-running it is
// the design that guarantees irreproducibility. The source is a SummingMergeTree
// whose parts merge, its retention drops the tail, and the rollup behind it can
// be re-run — so the same query asked twice is two different answers, and a model
// that cites "the query" has cited nothing. A dataset here is bytes: declared as
// a version, materialised once, fingerprinted, and never rewritten. A model can
// name the exact rows it was fitted on, forever, which is the only form in which
// an audit can be answered.
//
// THE FOUR PROPERTIES, AND WHERE EACH IS ENFORCED.
//
//	tenancy       peer.go — the tenant is the leading BOUND predicate of every
//	              statement, the first component of both tables' sort keys AND of
//	              their partition expressions, and it arrives only as a
//	              [tenant.Key], which cannot be written as a literal here and
//	              cannot be decoded from a request body.
//	immutability  A published version is `ready`, and the only rank above it is
//	              `disposed` — the tenant's own retention decision, the one write
//	              that may outrank a publication. No other stage can displace it,
//	              in the engine or at the endpoint. Two layers.
//	bounds        spec.go — the window, the horizon, the row cap, the number of
//	              names and the number of versions are all bounded at the
//	              endpoint, and every scan of the source is admitted through ONE
//	              gate: priced at the meter, one per tenant, [maxJobs] in the
//	              process, each with a deadline of its own ([plane.admit]).
//	expiry        There is NO table TTL. Disposal is the tenant's own DROP
//	              PARTITION on (org, dataset), which cannot be spelled cross-tenant.
//
// WHY IT IS ITS OWN APP. It shares no state with a scorer: there is no in-memory
// model, no ring, no single-writer file. Every ANSWER it gives is a function of
// the store, so it restarts empty and a restart loses nothing but the jobs in
// flight — which is exactly what a plane holding the record of what a model
// trained on must do, and exactly what a process pinned to one replica for its
// in-memory forests cannot promise. Its surface is five leaves under
// /v1/dataset that no other app claims; zip refuses two owners for one
// prefix at compose time, so that is checked rather than agreed.
//
// WHAT IS PER PROCESS, SAID PLAINLY. Every read, every declaration and every
// disposal is a pure function of the store and answers identically from any
// process. ADMISSION is split. A version is started ONCE for the deployment: the
// claim is a single-use token in the KV the pods share ([plane.start]), taken before
// the attempt is put on record, so two processes cannot both materialise one version
// and write rows under one number. And whether a version is running is read off the
// register and the clock ([plane.live]), which any process can ask. What stays a
// process's own are the two resource bounds: the one-scan-per-tenant gate and the
// [maxJobs] ceiling are this process's map, because what they spend is this
// process's memory and a tenant's scans in N processes are N scans, each bounded.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc
package dataset
