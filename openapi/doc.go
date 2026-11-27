// Package openapi composes the fleet's documents out of the per-app projections
// that sdk/spec writes, and serves them. What an app declares (bodies, prose, the
// credential) and the per-app projection (spec.Of) live in sdk/spec; this package
// is the host half and imports it.
//
// The rest of this comment describes the projection both halves share.
//
// The projection projects the LIVE zip/fiber router into an OpenAPI 3.1
// document. The spec is not a description of the router — it IS the router,
// read through app.Fiber().GetRoutes(). There is no second route registry, so a
// document and the app that produced it cannot drift: the only way to change it
// is to change the routes it is read from. (Register/Describe add a registry of
// BODIES and PROSE, never of routes: a declaration renders only on a route the
// router carries, so the paths remain the router's alone.)
//
// THAT GUARANTEE IS PER APP, AND IT ENDS AT THE APP. [Spec] and [Mount] read a
// router that is right there. The FLEET document — what api.hanzo.ai serves —
// cannot: the light host mounts no subsystem, so [UseFleet] composes the
// projections 116 app binaries wrote when they were BUILT (fleet.go). Between the
// projection and the request sit two gaps no reading of any router closes: the
// subset can be older than the code (mk/fleet.mk check regenerates it
// from source and refuses the diff), and the deployed edge can hand the
// path to somebody else entirely (only a probe of the live host sees that). See
// fleetInfo for what the published document may therefore claim.
//
// This mirrors the rule zapface/wire.go states for transports — two transports,
// ONE dispatch path. ZAP and OpenAPI are two PROJECTIONS of one route table.
//
// # Reading the router is not a workaround; it is the only total source
//
// Two properties make the LIVE router the only honest source, and no static scan
// of the source tree a substitute:
//
//   - Routes are composed at runtime. POST /v1/kms/auth/login is registered as
//     Group("/v1/kms/auth").Post("/login") — that path literal does not exist
//     anywhere in the tree, so no grep can find it. Only the assembled router
//     knows it.
//   - The route set is a function of DEPLOYMENT CONFIG. Subsystems mount only
//     when cfg.Enabled(name), and several gate routes internally (kms registers
//     its secret routes only `if kc != nil`). The spec therefore VARIES PER
//     DEPLOYMENT, correctly: a deployment that does not mount admin does not
//     advertise admin. That is a feature — each deployment describes itself —
//     and it is why the document is generated per-process, not built once in CI.
//
// # Two readings of one router, folded into one document
//
// zip ships its own OpenAPI projection (zip/openapi.go) driven by the typed-op
// registry (zip.Get[In, Out] → app.ops), which carries the real request/response
// Go types and therefore real JSON Schema — plus the prose cmd/zipdoc lifts out
// of the handlers' doc comments at build time. It is the better reading, and it
// is not duplicated here: Typed READS it and Fold lays it over the router
// projection.
//
// The two are not rivals because GetRoutes() is a strict SUPERSET of app.ops
// (registerTyped registers a fiber route too). So the router gives the TOTAL set
// of operations and the registry gives DETAIL for the subset that has any:
//
//	router   → every operation exists, with its address and product.
//	registry → the typed ones also carry schemas, parameters, responses,
//	           descriptions and examples.
//
// One document, no gaps and no invention. Migrating a raw handler to a typed op
// is what earns it the second half, and it needs no generator change: the same
// fold picks it up on the next run.
package openapi
