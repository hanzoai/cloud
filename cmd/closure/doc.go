// Command closure records what each app's document was generated FROM, and
// checks — in about five seconds — that no document has been left behind by a
// dependency that moved.
//
// plugin/<app>/openapi.json is a projection of that app's router, and the router
// is built from source: the app's own, and every package it links. `make -f
// mk/fleet.mk check` proves the projection is current by REGENERATING it. That is
// exact, it is the decider, and it stays. It is the wrong place to LEARN, because
// it costs one link per app — ten minutes warm on a twenty-core box, fifty on the
// runner — and it is the LAST gate in hanzo.yml, so it reports a thirty-second
// regeneration after everything cheap has already been paid for.
//
// THE BLIND SPOT THIS WAS LEARNED FROM. A commit that moves a version in go.mod
// and touches nothing else silently invalidates a committed document. hanzoai/iam
// v1.34.21 → v1.34.29 added EnableCodeSignin to the type behind iam.Application;
// the commit touched go.mod, go.sum and apps/iam and not one generated file;
// plugin/iam/openapi.json went stale on main, and the release train stopped for
// fifty-three minutes to say so. Nothing in the tree could have said it sooner,
// because nothing in the tree recorded what that document had been generated
// from. This records it.
//
// PACKAGES, NOT MODULES, AND THE MEASUREMENT SAYS WHY — including where it does
// NOT help, which matters more.
//
// Module granularity is unusable: github.com/hanzoai/iam is in the import closure
// of 122 of 125 apps, and so are eighteen other hanzoai modules. "iam moved, so
// regenerate everything that imports iam" therefore names the whole fleet on every
// bump, forever, whatever the bump did. A gate that always says the same thing is
// not consulted.
//
// Package granularity asks a strictly narrower question — which apps link a package
// whose SOURCE moved — and it is bounded by reach, which is the part that is
// guaranteed rather than lucky: an app's digest cannot move unless a package in ITS
// closure moved, so a module only 3 apps link can never implicate more than 3.
// github.com/hanzoai/o11y is in 1 closure, hanzoai/ai in 3, hanzoai/plans in 6:
// those bumps resolve to a handful of links and about a minute.
//
// It is NOT a promise of a small number, and the bump this was built for is exactly
// the case that shows it. v1.34.21 → v1.34.29 moved twelve iam packages. Ten of them
// are reached by four apps or fewer — including internal/bootstrap, where
// EnableCodeSignin actually lives — but pkg/schema moved too, and 122 apps link
// that. So this reports 121, and 121 is the CORRECT answer: those closures really
// did move, and `make describe` really was the fix that landed. Package granularity
// did not shrink that one. What it did was answer in two seconds instead of
// fifty-three minutes, which is the whole point and is true of every bump.
//
// The value is the SOURCE, not the module's version string, for the same reason: a
// bump that changes no byte this repo reaches moves no digest and asks for nothing,
// so a retag or a go.sum churn is not a false alarm.
//
// THE MAIN MODULE IS EXCLUDED, and not to save time. cloud's own source is
// already policed from source by app-contract, and every app links the root
// package — so hashing it would move all 125 digests on every commit and this
// would report the whole fleet, every time, forever. The dependency axis is the
// one with no other checker, and it is the axis this measures.
//
// IT PREDICTS, IT DOES NOT DECIDE. A moved closure means the document was
// generated from source that has since changed; it does not prove the projection
// differs, because most changes never reach a route. Regenerating is how you find
// out, and app-contract still regenerates everything and still decides. This
// exists to move that discovery from minute fifty to second five, and to name the
// apps worth regenerating rather than the fleet.
//
//	check:  go run ./cmd/closure          # the gate. reports EVERY stale app at once
//	record: go run ./cmd/closure -write   # run by `make describe`, never by hand
package main
