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

// The unified `cloud` binary is the APPLICATION layer only. Infrastructure and
// edge subsystems run as their own deployments, NOT fused in:
//   - iam   → iam.hanzo.ai (Casdoor)        — identity, isolated control plane
//   - kms   → kms.hanzo.ai (luxfi/kms)       — secrets, isolated control plane
//   - mcp   → its own deployment             — tool surface
//   - gateway, ingress → the edge            — they route *to* this binary
//   - amqp  → removed (unused)
// Keeping them separate preserves blast-radius isolation and independent
// scaling for the security/edge tier.
import (
	_ "github.com/hanzoai/ai"        // order 150
	_ "github.com/hanzoai/authz"     // order 70
	_ "github.com/hanzoai/base"      // order 60
	_ "github.com/hanzoai/commerce"  // order 100
	_ "github.com/hanzoai/licensing" // order 110
	_ "github.com/hanzoai/metrics"   // order 40
	_ "github.com/hanzoai/o11y"      // order 70
	_ "github.com/hanzoai/vfs"       // order 20

	// Node-service subsystems hosted in-process via base+goja (HIP-0106);
	// the JS + catalog data live in hanzoai/plans, hanzoai/pricing.
	_ "github.com/hanzoai/cloud/clients/evalsvc"    // order 145 — /v1/evals/*
	_ "github.com/hanzoai/cloud/clients/plansvc"    // order 111 — /v1/plans/*
	_ "github.com/hanzoai/cloud/clients/pricingsvc" // order 112 — /v1/pricing/*

	// Provisioning control plane: creates logical resources (sql, vector,
	// datastore, kv, search, s3, docdb) inside the live shared backends.
	_ "github.com/hanzoai/cloud/clients/provisioningsvc" // order 120 — /v1/sql,/v1/vector,/v1/datastore,/v1/kv,/v1/search,/v1/s3,/v1/docdb

	// Console Search/Vector product panels (browser-facing read surface).
	_ "github.com/hanzoai/cloud/clients/productsvc" // order 145 — /api/search-docs/*, /api/vector/*
)
