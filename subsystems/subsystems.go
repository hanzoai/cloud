// Package subsystems is the single source of truth for which Hanzo cloud
// subsystems are linked into a binary.
//
// Blank-importing this package pulls every subsystem into the build graph;
// each one registers a cloud.MountSpec into cloud.Registry from its own
// init() (gated by //go:build cloud in the subsystem package, so the
// registrations fire only when built with -tags cloud).
//
// Both entrypoints — cmd/cloud (the full fused surface) and cmd/hanzo (the
// subcommand dispatcher) — blank-import THIS package and nothing else. The
// subsystem set is therefore defined ONCE, here; adding or removing a
// subsystem is a one-line change in one file, never duplicated per binary.
//
// (This package must NOT live in the root `cloud` package: the subsystems
// import `cloud` for Deps + Register, so a root-package bundle would form an
// import cycle. As a sibling subpackage it composes them without one.)
package subsystems

import (
	_ "github.com/hanzoai/ai"          // order 150
	_ "github.com/hanzoai/amqp"        // order 30
	_ "github.com/hanzoai/authz"       // order 70
	_ "github.com/hanzoai/base"        // order 60
	_ "github.com/hanzoai/commerce"    // order 100
	_ "github.com/hanzoai/gateway/v2"  // order 80
	_ "github.com/hanzoai/iam/pkg/iam" // order 50 (Mount lives in the pkg/iam submodule)
	_ "github.com/hanzoai/ingress"     // order 90
	_ "github.com/hanzoai/kms"         // order 10 (thin wrapper over the canonical luxfi/kms)
	_ "github.com/hanzoai/licensing"   // order 110 (after iam + commerce)
	_ "github.com/hanzoai/mcp/go"      // order 160 (Mount lives in the go submodule)
	_ "github.com/hanzoai/metrics"     // order 40
	_ "github.com/hanzoai/o11y"        // order 70
	_ "github.com/hanzoai/vfs"         // order 20

	// Node-service subsystems hosted in-process via base+goja (HIP-0106);
	// the JS + catalog data live in hanzoai/plans, hanzoai/pricing.
	_ "github.com/hanzoai/cloud/clients/plansvc"    // order 111 — /v1/plans/*
	_ "github.com/hanzoai/cloud/clients/pricingsvc" // order 112 — /v1/pricing/*
)
