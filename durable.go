// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package cloud

import (
	"cmp"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	luxlog "github.com/luxfi/log"

	"github.com/zap-proto/zip"

	tasksauth "github.com/hanzoai/tasks/pkg/auth"
	tasksclient "github.com/hanzoai/tasks/pkg/sdk/client"
	tasksengine "github.com/hanzoai/tasks/pkg/tasks"
)

// durableGatedZAPPort is the CLUSTER-reachable, identity-gated ZAP port the embedded
// engine exposes via Embedded.ServeGated (published on cloud's Service in universe).
// Unlike the loopback socket (ungated, ai-ingest only), every request here must
// carry a valid IAM auth_token — the same trust anchor as HTTP SanitizeIdentity —
// org-scoped to the token owner. 9999 mirrors the port the retired tasksd exposed, so
// a consumer repoint changes only the host (tasks.hanzo.svc → cloud.hanzo.svc).
const durableGatedZAPPort = 9999

// gatedAddr is the cluster-reachable tasks listener. It defaults to the port
// the retired tasksd exposed, so existing consumers keep their address, and
// CLOUD_TASKS_GATED_PORT moves it.
//
// It needs to move because "two instances can never coexist on one host" stopped
// being acceptable: apps are separate processes now, and a developer running a
// second stack — or a second agent on a shared box — has no way to bring one up
// while another holds the port. A fixed number is right for a deployment and wrong
// for a workstation.
// gatedAddr is where the CLUSTER-reachable, identity-gated listener binds. This
// one is a TCP address and must stay one: consumers in other pods dial it, and a
// unix socket does not leave the host. The engine's own loopback listener is a
// socket (see installDurableIngest) precisely because nothing off-host dials THAT.
func gatedAddr() string {
	if v := strings.TrimSpace(os.Getenv("CLOUD_TASKS_GATED_PORT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return ":" + strconv.Itoa(n)
		}
	}
	return ":" + strconv.Itoa(durableGatedZAPPort)
}

// embeddedTasks keeps the in-process engine alive for the process (a package ref the GC
// won't collect) and lets Serve stop it on shutdown.
var embeddedTasks *tasksengine.Embedded

// EmbeddedTasks returns the ONE in-process tasks engine, or nil until
// installDurableIngest has run (or if it failed to start). The Tasks HTTP/UI surface
// (apps/tasks, mounted at /v1/tasks/*) serves on THIS shared engine — there is
// exactly one engine per process, shared by ai's durable ingest AND the Tasks
// product surface, never a second Embed. The surface resolves it lazily (per
// request) because subsystem Mount runs during UseAll, before installDurableIngest.
func EmbeddedTasks() *tasksengine.Embedded { return embeddedTasks }

// installDurableIngest embeds the ONE hanzoai/tasks engine IN-PROCESS — the unified durable
// queue (there is no second async system; tasks/CONTRACT) — and injects a per-org
// loopback ZAP dialer into ai's ingest. A long ingest (github/crawl/s3) then runs as a
// durable workflow in the OWNER's namespace (CONTRACT §6: namespace maps 1:1 to org),
// tracked in the ONE Tasks product. In-process ZAP = mega fast, low latency/memory, no
// HTTP. Fail-soft by construction: any embed error leaves ai's dialer unset →
// EnqueueIngest returns ErrTasksNotConfigured → the handler runs ingest inline (always
// works). Called once, after UseAll (ai is mounted) and before Listen.
func installDurableIngest(ctx context.Context, deps Deps, app string) {
	// A stable data dir the engine owns. Cloud's container is distroless (no /tmp), so
	// Embed's default os.MkdirTemp("") fallback fails — pin it to cloud's data root.
	// PER PROCESS, both of them. Apps are their own binaries now, so a fixed port
	// and a shared directory are two processes' worth of contention over one
	// resource: seven of eight children lost the bind on one fixed port and ran with
	// NO durable engine at all — marketing's drip queue among them, which is how a
	// campaign resolved its audience and then mailed nobody. A shared store would be
	// the same collision one layer down, since the engine's SQLite has one writer.
	//
	// It is a UNIX SOCKET, not a port. A path per app needs no allocation and
	// cannot collide, which is what the internal plane does everywhere else — and
	// it is why the collision above cannot recur: two processes cannot want the
	// same free port when neither wants a port at all.
	dataDir := filepath.Join(cmp.Or(DataDir(), "/data"), "tasks", cmp.Or(app, "cloud"))
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		luxlog.Default().Warn("durable ingest: data dir unavailable; ingest runs inline", "err", err)
		return
	}
	sock := zip.SocketPath("tasks-" + cmp.Or(app, "cloud"))
	emb, err := tasksengine.Embed(ctx, tasksengine.EmbedConfig{
		Address: sock,
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
		luxlog.Default().Warn("durable ingest: tasks embed failed; ingest runs inline", "err", err)
		return
	}
	embeddedTasks = emb
	addr := emb.Address()
	ingestDialer = func(org string) (tasksclient.Client, error) {
		return tasksclient.Dial(tasksclient.Options{Address: addr, Namespace: "default"})
	}
	luxlog.Default().Info("durable ingest wired: in-process tasks engine", "app", app, "addr", addr, "dataDir", dataDir)

	// Expose the SAME engine on a cluster-reachable, IDENTITY-GATED ZAP listener so the
	// standalone tasksd's consumers (auto, hanzo-playground, platform) run their durable
	// work here. RequireIdentity: every request must carry an IAM auth_token, validated
	// against {IAMIssuer}/v1/iam/.well-known/jwks (HIP-0111) and org-scoped to its owner —
	// the SAME trust anchor as the HTTP SanitizeIdentity boundary. The loopback dialer above
	// stays ungated (in-process ai-ingest shares cloud's trust boundary). Fail-soft: a
	// missing issuer or a bind failure logs and leaves the gated surface down without
	// touching ai-ingest.
	// The GATED listener is a cluster-reachable port, so exactly one process may own
	// it — and the one that should is the app that serves the Tasks product. Every
	// process trying meant seven of eight logging "address already in use" for a
	// listener they had no business exposing.
	if app != "tasks" && app != "cloud" {
		return
	}
	if deps.IAMIssuer == "" {
		luxlog.Default().Warn("durable tasks: no IAM issuer; gated cluster ZAP listener NOT exposed", "addr", gatedAddr())
		return
	}
	validator := tasksauth.NewValidator(tasksauth.JWTConfig{
		Issuer:  deps.IAMIssuer,
		JWKSURL: JWKSURLFor(deps.IAMIssuer),
	})
	if err := emb.ServeGated(ctx, gatedAddr(), validator); err != nil {
		luxlog.Default().Error("durable tasks: gated cluster ZAP listener failed to start", "err", err, "addr", gatedAddr())
		return
	}
	luxlog.Default().Info("durable tasks: gated cluster ZAP listener up", "addr", gatedAddr(), "issuer", deps.IAMIssuer)
}
