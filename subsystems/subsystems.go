// Package subsystems is the single source of truth for which Hanzo cloud
// subsystems are linked into a binary.
//
// Blank-importing this package pulls every subsystem into the build graph;
// each one registers a cloud.MountSpec into cloud.Registry from its own
// init(). Registration is unconditional — a plain `go build ./cmd/cloud`
// (no build tags) links and mounts the full set. There is no //go:build
// gate on any subsystem.
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

// The unified `cloud` binary is the APPLICATION layer plus the embedded KMS
// secrets plane (HIP-0106 "all Go embeds in cloud"). The remaining
// infrastructure/edge subsystems run as their own deployments, NOT fused in:
//   - iam   → iam.hanzo.ai (Casdoor)        — identity, isolated control plane
//   - mcp   → its own deployment             — tool surface
//   - gateway, ingress → the edge            — they route *to* this binary
//   - amqp  → removed (unused)
// Keeping those separate preserves blast-radius isolation and independent
// scaling for the security/edge tier.
//
// KMS is embedded in-process (clients/kms mounts /v1/kms/* backed by
// clients/kmsembed, replacing the legacy Infisical fork). Its master key is
// injected by the operator via a K8s Secret env; absent it the subsystem serves
// fail-closed health-only.
import (
	_ "github.com/hanzoai/ai"        // order 150
	_ "github.com/hanzoai/authz"     // order 70
	_ "github.com/hanzoai/base"      // order 60
	_ "github.com/hanzoai/commerce"  // order 100
	_ "github.com/hanzoai/licensing" // order 110
	_ "github.com/hanzoai/metrics"   // order 40
	_ "github.com/hanzoai/o11y"      // order 70
	_ "github.com/hanzoai/vfs"       // order 20

	// Embedded KMS secrets plane (HIP-0106): mounts /v1/kms/* backed by the
	// in-process luxfi/kms SecretStore under CLOUD_DATA_DIR. Registered as
	// "kmssvc" (order 10) so the real /v1/kms/health probe is not shadowed by the
	// generic liveness route; secret ops fail closed until the operator injects
	// CLOUD_KMS_MASTER_KEY_REF.
	_ "github.com/hanzoai/cloud/clients/kms" // order 10 — /v1/kms/*

	// Node-service subsystems hosted in-process via base+goja (HIP-0106);
	// the JS + catalog data live in hanzoai/plans, hanzoai/pricing.
	_ "github.com/hanzoai/cloud/clients/bot"       // order 143 — /v1/bot/* (reverse proxy → bot-gateway)
	_ "github.com/hanzoai/cloud/clients/eval"      // order 145 — /v1/evals/*
	_ "github.com/hanzoai/cloud/clients/exec"      // order 140 — /v1/exec,/v1/upload,/v1/download,/v1/files (Code Interpreter → sandbox)
	_ "github.com/hanzoai/cloud/clients/plan"      // order 111 — /v1/plans/*
	_ "github.com/hanzoai/cloud/clients/plugin"    // order 900 - runtime wasm/proxy plugins (goa wasm + ZAP proxy)
	_ "github.com/hanzoai/cloud/clients/pricing"   // order 112 — /v1/pricing/*
	_ "github.com/hanzoai/cloud/clients/websearch" // order 141 — /v1/websearch/* (SearXNG+Firecrawl-compat over Hanzo search+crawl)

	// S3 object-storage DATA plane: the org-scoped /v1/s3 file manager (buckets +
	// objects) over the shared SeaweedFS S3 gateway. Order 118 (< provisioning's
	// 120) so its static /v1/s3/buckets + /v1/s3/health register BEFORE
	// provisioning's /v1/s3/:name and win Fiber's first-match scan; registered as
	// "s3svc" so the generic health route does not shadow the real fail-closed
	// /v1/s3/health. It COMPLEMENTS provisioning (which owns the s3 RESOURCE
	// lifecycle at /v1/s3 + /v1/s3/:name) — both derive a tenant's physical bucket
	// name identically (provisioning.PhysicalName) so a provisioned bucket is
	// browsable here.
	_ "github.com/hanzoai/cloud/clients/s3" // order 118 — /v1/s3/buckets/*,/v1/s3/health

	// Provisioning control plane: creates logical resources (sql, vector,
	// datastore, kv, search, s3, docdb) inside the live shared backends.
	_ "github.com/hanzoai/cloud/clients/provisioning" // order 120 — /v1/sql,/v1/vector,/v1/datastore,/v1/kv,/v1/search,/v1/s3,/v1/docdb

	// Projects control plane: the ONE org-scoped store of buildable/deployable
	// sites, shared by hanzo.app (builder) and console.hanzo.ai (Projects), plus
	// the deploy pipeline (artifact/git → OUR S3 → live URL).
	_ "github.com/hanzoai/cloud/clients/projectsvc" // order 125 — /v1/projects/*

	// PaaS control plane: the native, in-process port of the standalone Dokploy
	// platform's deploy lifecycle. Reads the operator `Service` CR fleet as the
	// declared/running/drift board and deploys by merge-patching a CR's
	// `.spec.image` (the operator reconciles the rollout) — the ONE deploy path.
	// Global-admin only; the user-facing view lives in console2.
	_ "github.com/hanzoai/cloud/clients/paassvc" // order 128 — /v1/paas/*

	// Product control planes: per-org, Base/SQLite-backed application surfaces
	// mounted natively in the cloud binary (the "all products in the cloud
	// binary" thesis). Each is org-scoped by the gateway-minted X-Org-Id.
	// clients/prompts is the red-approved, versioned prompt library and the ONE
	// owner of /v1/prompts/* (it supersedes the earlier clients/prompt facade).
	_ "github.com/hanzoai/cloud/clients/agents"    // order 127 — /v1/agents/*
	_ "github.com/hanzoai/cloud/clients/analytics" // order 132 — /v1/analytics/* (native-Go analytics on datastore/ClickHouse: per-org LLM usage + web/commerce lenses)
	_ "github.com/hanzoai/cloud/clients/crm"       // order 131 — /v1/crm/* (native-Go CRM on Base: companies/contacts/opportunities)
	_ "github.com/hanzoai/cloud/clients/functions" // order 128 — /v1/functions/*
	_ "github.com/hanzoai/cloud/clients/prompts"   // order 126 — /v1/prompts/*
	_ "github.com/hanzoai/cloud/clients/templates" // order 129 — /v1/templates/* (starter-kit gallery, read-only)

	// ML/Train control plane: tenant-scoped k8s bridge fronting the kubeflow
	// forks (kserve InferenceService, trainer TrainJob, katib Experiment).
	_ "github.com/hanzoai/cloud/clients/ml" // order 130 — /v1/ml/*,/v1/train/*

	// Console Search/Vector product panels (browser-facing read surface).
	_ "github.com/hanzoai/cloud/clients/product" // order 145 — /v1/search-docs/*, /v1/vector/*

	// God-mode admin surface for the Hanzo Admin Console (admin.hanzo.ai). Fans
	// out to IAM (identity), commerce (billing) and o11y (health); global-admin
	// only, fail-closed.
	_ "github.com/hanzoai/cloud/clients/admin" // order 146 — /v1/admin/*

	// Installs the o11y runtime handler (reverse proxy to the dedicated o11y
	// Deployment) so hanzoai/o11y's /v1/o11y/* surface serves real telemetry
	// instead of the "runtime not initialized" 503.
	_ "github.com/hanzoai/cloud/clients/o11y" // order 71 — installs o11y.SetHandler
	// The console2 SPA is go:embed'd and served at "/" by webui.go's
	// mountConsole, called directly from Serve AFTER every /v1/* route mounts
	// (so real API routes always win; unmatched paths fall back to the SPA).
	// That is the "one binary" endgame — the unified cloud binary IS the
	// frontend too (Hanzo V8: Open Edition). It needs no import here; it is
	// wired in Serve, not registered as a subsystem.
)
