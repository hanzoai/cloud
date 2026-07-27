// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package cloud

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tasksauth "github.com/hanzoai/tasks/pkg/auth"
	tasksengine "github.com/hanzoai/tasks/pkg/tasks"
)

// durableZAPPort is the loopback ZAP port the embedded tasks engine binds. Loopback +
// single binary ⇒ the engine shares cloud's trust boundary: no external tasks Service,
// no cross-service auth, no per-org token minting. Non-9999 to never collide with the
// cluster tasks Service if one also runs.
const durableZAPPort = 19999

// durableGatedZAPPort is the CLUSTER-reachable, identity-gated ZAP port the embedded
// engine exposes via Embedded.ServeGated (published on cloud's Service in universe).
// Unlike durableZAPPort (loopback, ungated, ai-ingest only), every request here must
// carry a valid IAM auth_token — the same trust anchor as HTTP SanitizeIdentity —
// org-scoped to the token owner. 9999 mirrors the port the retired tasksd exposed, so
// a consumer repoint changes only the host (tasks.hanzo.svc → cloud.hanzo.svc).
const durableGatedZAPPort = 9999

// embeddedTasks keeps the in-process engine alive for the process (a package ref the GC
// won't collect) and lets Serve stop it on shutdown.
var embeddedTasks *tasksengine.Embedded

// EmbeddedTasks returns the ONE in-process tasks engine, or nil until
// wireDurableIngest has run (or if it failed to start). Every consumer resolves it
// through THIS accessor, lazily, per request: the Tasks HTTP/UI surface
// (clients/tasks, mounted at /v1/tasks/*) and the inference module's ingest dialer
// (apps/ai.go) both run on the ONE shared engine, never a second Embed — and both
// are wired before the engine starts, since Mount and Wire run ahead of it.
func EmbeddedTasks() *tasksengine.Embedded { return embeddedTasks }

// wireDurableIngest embeds the ONE hanzoai/tasks engine IN-PROCESS — the unified durable
// queue (there is no second async system; tasks/CONTRACT). A long ingest
// (github/crawl/s3) then runs as a durable workflow in the OWNER's namespace
// (CONTRACT §6: namespace maps 1:1 to org), tracked in the ONE Tasks product.
// In-process ZAP = mega fast, low latency/memory, no HTTP. Fail-soft by
// construction: any embed error leaves EmbeddedTasks nil → the dialer reports
// ErrTasksNotConfigured → the handler runs ingest inline (always works). Called
// once, after MountAll and before Listen.
func wireDurableIngest(ctx context.Context, deps Deps) {
	// A stable data dir the engine owns. Cloud's container is distroless (no /tmp), so
	// Embed's default os.MkdirTemp("") fallback fails — pin it to cloud's data root.
	dataDir := filepath.Join(firstNonEmptyStr(deps.DataDir, "/data"), "tasks")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		deps.Logger.Warn("durable ingest: data dir unavailable; ingest runs inline", "err", err)
		return
	}
	emb, err := tasksengine.Embed(ctx, tasksengine.EmbedConfig{
		ZAPPort: durableZAPPort,
		DataDir: dataDir,
		NodeID:  "cloud-tasks",
		// RequireIdentity defaults false: the engine is loopback-only and shares cloud's
		// trust boundary. Data isolation is enforced in the workflow INPUT (IngestSource
		// is owner-scoped), so we enqueue into the engine's always-registered `default`
		// namespace rather than a per-org namespace. The embedded engine only registers
		// `default` at boot and does NOT lazily create namespaces on ExecuteWorkflow —
		// dialing an unregistered per-org namespace makes the worker poll a namespace
		// that doesn't exist and BLOCK, which silently forced ingest to fall back inline.
	})
	if err != nil {
		deps.Logger.Warn("durable ingest: tasks embed failed; ingest runs inline", "err", err)
		return
	}
	embeddedTasks = emb
	deps.Logger.Info("durable ingest: in-process tasks engine up",
		"addr", fmt.Sprintf("127.0.0.1:%d", emb.ZAPPort()), "dataDir", dataDir)

	// Expose the SAME engine on a cluster-reachable, IDENTITY-GATED ZAP listener so the
	// standalone tasksd's consumers (auto, hanzo-playground, platform) run their durable
	// work here. RequireIdentity: every request must carry an IAM auth_token, validated
	// against {IAMIssuer}/v1/iam/.well-known/jwks (HIP-0111) and org-scoped to its owner —
	// the SAME trust anchor as the HTTP SanitizeIdentity boundary. The loopback dialer above
	// stays ungated (in-process ai-ingest shares cloud's trust boundary). Fail-soft: a
	// missing issuer or a bind failure logs and leaves the gated surface down without
	// touching ai-ingest.
	if deps.IAMIssuer == "" {
		deps.Logger.Warn("durable tasks: no IAM issuer; gated cluster ZAP listener NOT exposed", "port", durableGatedZAPPort)
		return
	}
	validator := tasksauth.NewValidator(tasksauth.JWTConfig{
		Issuer:  deps.IAMIssuer,
		JWKSURL: strings.TrimRight(deps.IAMIssuer, "/") + "/v1/iam/.well-known/jwks",
	})
	if err := emb.ServeGated(ctx, durableGatedZAPPort, validator); err != nil {
		deps.Logger.Error("durable tasks: gated cluster ZAP listener failed to start", "err", err, "port", durableGatedZAPPort)
		return
	}
	deps.Logger.Info("durable tasks: gated cluster ZAP listener up", "port", durableGatedZAPPort, "issuer", deps.IAMIssuer)
}

// firstNonEmptyStr returns the first non-empty string, else the last.
func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
